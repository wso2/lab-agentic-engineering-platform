package webhook

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"
)

// ErrNoRoutingKey is returned when an event lacks any routable identifier.
// PR B treats these as 200-ack-noop (Phase 0 audit-only behaviour) so the
// receiver doesn't 5xx on events for which no handler is configured.
var ErrNoRoutingKey = errors.New("webhook: no routing key")

// RoutingKey extracts whichever identifier maps an event to an ocOrgId.
type RoutingKey struct {
	Kind            string // "installation" or "repository" or "platform"
	InstallationID  int64
	RepoFullName    string
}

// extractRoutingKey peeks at the relevant fields by event class.
//
//   - installation, installation_repositories → installation.id
//   - pull_request, push, issue_comment, issues → repository.full_name
//   - everything else → "platform" (audit-only Phase 0 carry-over)
func extractRoutingKey(event string, payload []byte) RoutingKey {
	switch event {
	case "installation", "installation_repositories":
		var p struct {
			Installation struct {
				ID int64 `json:"id"`
			} `json:"installation"`
		}
		_ = json.Unmarshal(payload, &p)
		return RoutingKey{Kind: "installation", InstallationID: p.Installation.ID}
	case "pull_request", "push", "issue_comment", "issues":
		var p struct {
			Repository struct {
				FullName string `json:"full_name"`
			} `json:"repository"`
		}
		_ = json.Unmarshal(payload, &p)
		return RoutingKey{Kind: "repository", RepoFullName: p.Repository.FullName}
	default:
		return RoutingKey{Kind: "platform"}
	}
}

// OcOrgIDLookup is the dependency the webhook controller uses to resolve
// (installation_id | repo_full_name) → ocOrgId. The implementation is
// gitservice.Client.OrgIDByInstallationID + a separate path for repos.
type OcOrgIDLookup interface {
	OrgIDByInstallationID(ctx context.Context, installationID int64) (string, error)
	// OrgIDByRepoFullName is wired against a small repo-records table the
	// BFF already maintains via /api/v1/repos. For PR B we look up via
	// the existing gitservice.GetRepo (which carries the orgId), keyed
	// on full_name → repo lookup. The PR plan keeps the BFF lookup
	// surgical: we add a small in-process map seeded by webhook routing
	// data the receiver already sees.
	OrgIDByRepoFullName(ctx context.Context, fullName string) (string, error)
}

// ResolveOcOrgID is the entry point the webhook controller calls. It
// extracts the routing key and consults `lookup` to resolve ocOrgId.
//
// Caches resolved (key → ocOrgId) for 60s in-process to avoid
// hot-pathing git-service on every push event. Cache invalidation is
// natural — if a credential is reconnected to a different ocOrgId, the
// 60s window is the maximum lag.
func ResolveOcOrgID(ctx context.Context, lookup OcOrgIDLookup, cache *RoutingCache, event string, payload []byte) (string, error) {
	key := extractRoutingKey(event, payload)
	switch key.Kind {
	case "installation":
		if key.InstallationID == 0 {
			return "", ErrNoRoutingKey
		}
		if v, ok := cache.getInstallation(key.InstallationID); ok {
			return v, nil
		}
		oc, err := lookup.OrgIDByInstallationID(ctx, key.InstallationID)
		if err != nil {
			return "", err
		}
		cache.putInstallation(key.InstallationID, oc)
		return oc, nil
	case "repository":
		if key.RepoFullName == "" {
			return "", ErrNoRoutingKey
		}
		if v, ok := cache.getRepo(key.RepoFullName); ok {
			return v, nil
		}
		oc, err := lookup.OrgIDByRepoFullName(ctx, key.RepoFullName)
		if err != nil {
			return "", err
		}
		cache.putRepo(key.RepoFullName, oc)
		return oc, nil
	default:
		return "", ErrNoRoutingKey
	}
}

// ----------------------------------------------------------------------------
// In-process routing cache
// ----------------------------------------------------------------------------

// RoutingCache is a tiny TTL cache keyed by installation_id and
// repo_full_name. Reads on the hot webhook path bypass git-service when
// the entry is fresh (≤60s).
type RoutingCache struct {
	mu            sync.RWMutex
	ttl           time.Duration
	installations map[int64]routingCacheEntry
	repos         map[string]routingCacheEntry
}

type routingCacheEntry struct {
	value    string
	expireAt time.Time
}

// NewRoutingCache constructs a cache with the supplied TTL (default 60s).
func NewRoutingCache(ttl time.Duration) *RoutingCache {
	if ttl == 0 {
		ttl = 60 * time.Second
	}
	return &RoutingCache{
		ttl:           ttl,
		installations: map[int64]routingCacheEntry{},
		repos:         map[string]routingCacheEntry{},
	}
}

func (c *RoutingCache) getInstallation(id int64) (string, bool) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	e, ok := c.installations[id]
	if !ok || time.Now().After(e.expireAt) {
		return "", false
	}
	return e.value, true
}

func (c *RoutingCache) putInstallation(id int64, val string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.installations[id] = routingCacheEntry{value: val, expireAt: time.Now().Add(c.ttl)}
}

func (c *RoutingCache) getRepo(fullName string) (string, bool) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	e, ok := c.repos[strings.ToLower(fullName)]
	if !ok || time.Now().After(e.expireAt) {
		return "", false
	}
	return e.value, true
}

func (c *RoutingCache) putRepo(fullName, val string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.repos[strings.ToLower(fullName)] = routingCacheEntry{value: val, expireAt: time.Now().Add(c.ttl)}
}

// String dumps cache stats for the debug endpoint (if/when added).
func (c *RoutingCache) String() string {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return fmt.Sprintf("RoutingCache{installations=%d, repos=%d, ttl=%s}", len(c.installations), len(c.repos), c.ttl)
}

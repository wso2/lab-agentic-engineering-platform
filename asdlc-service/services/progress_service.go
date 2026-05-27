package services

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"golang.org/x/sync/singleflight"
	"gorm.io/gorm"

	"github.com/wso2/asdlc/asdlc-service/clients/clustergatewayproxy"
	"github.com/wso2/asdlc/asdlc-service/clients/observer"
	"github.com/wso2/asdlc/asdlc-service/clients/openchoreo"
	"github.com/wso2/asdlc/asdlc-service/models"
	"github.com/wso2/asdlc/asdlc-service/services/codingagent"
)

// Default cap on lines per /progress/* response. Keeps Observer query
// cost bounded — design §6.5.
const defaultProgressLimit = 200

// ProgressResponse is the shape returned by both /progress/agent and
// /progress/build. Schema-versioned so the console can branch on future
// envelope changes without flag-flipping.
type ProgressResponse struct {
	SchemaVersion int                       `json:"schemaVersion"`
	Lines         []observer.ProgressEvent  `json:"lines"`
	CursorMillis  int64                     `json:"cursorMillis"`
	Phase         string                    `json:"phase,omitempty"`
	Truncated     bool                      `json:"truncated,omitempty"`
	Final         bool                      `json:"final"`
}

// ProgressService backs the BFF's /progress/* endpoints.
type ProgressService interface {
	// GetAgentProgress returns coding-agent NDJSON lines emitted at or
	// after sinceMillis (inclusive). When sinceMillis is 0 the service
	// substitutes (task.DispatchedAt - 1s); when the task has no run
	// recorded yet the response is empty + Final=false.
	GetAgentProgress(ctx context.Context, taskID string, sinceMillis int64, limit int) (*ProgressResponse, error)

	// GetBuildProgress returns synthetic build_step lines derived from
	// the build WorkflowRun's Status.Tasks[]. Same cursor semantics as
	// GetAgentProgress. Final=true once the task is past `building`.
	GetBuildProgress(ctx context.Context, taskID string, sinceMillis int64) (*ProgressResponse, error)
}

type progressService struct {
	taskSvc        TaskService
	ocClient       openchoreo.ComponentClient
	observerClient observer.Client

	// proxy + db drive the new-path log surface (cgw-proxy pods/log +
	// coding_agent_logs sidecar). Both nil-safe — the service falls
	// back to Observer when either is missing OR when the task's
	// runName format matches the legacy ClusterWorkflow shape.
	proxy *clustergatewayproxy.Client
	db    *gorm.DB

	// Singleflight collapses N concurrent viewers of the same
	// (runName, sinceMillis-bucket) into one Observer call. The leader
	// owns the upstream context with a 10s cap; followers wait on the
	// channel.
	sf singleflight.Group

	// In-memory cache of last-seen per-step phase used to compute
	// build_step deltas. Outer key = taskID (so eviction on terminal
	// task status drops the whole tree); inner key = stepName.
	mu       sync.Mutex
	stepSeen map[string]map[string]string

	// Monotonic seq for synthetic build_step events. Only used to break
	// ties on identical timestamps.
	buildSeq int64
}

// NewProgressService constructs a progress service. observerClient may
// be nil — methods then return ErrProgressUnavailable so the controller
// can map to 503.
//
// The workflow plane namespace is derived per-call from the task's
// OrgID (== OC namespace name): Observer queries take the source OC
// namespace and prepend `workflows-` to address the actual K8s
// namespace where Argo schedules pods. There is no platform-wide
// workflow-plane namespace once orgs are not collapsed.
func NewProgressService(
	taskSvc TaskService,
	ocClient openchoreo.ComponentClient,
	observerClient observer.Client,
) ProgressService {
	return &progressService{
		taskSvc:        taskSvc,
		ocClient:       ocClient,
		observerClient: observerClient,
		stepSeen:       map[string]map[string]string{},
	}
}

// WithCodingAgentLogSource wires the cluster-gateway-proxy + DB so
// GetAgentProgress can serve new-path (`ca-…` runName) tasks via
// pods/log + the `coding_agent_logs` sidecar instead of Observer.
// Both must be non-nil to enable the path. Idempotent.
func (s *progressService) WithCodingAgentLogSource(proxy *clustergatewayproxy.Client, db *gorm.DB) ProgressService {
	s.proxy = proxy
	s.db = db
	return s
}

// ErrProgressUnavailable signals that the Observer is not reachable.
// The controller maps this to HTTP 503 progress_unavailable.
var ErrProgressUnavailable = errors.New("progress unavailable")

func (s *progressService) GetAgentProgress(ctx context.Context, taskID string, sinceMillis int64, limit int) (*ProgressResponse, error) {
	// New-path branch: when the task's runName matches `ca-…` (the
	// dispatcher's deterministic format) AND the proxy + DB are wired,
	// serve from cgw-proxy pods/log + coding_agent_logs sidecar.
	// Falls through to the Observer path on any other runName shape so
	// legacy-dispatch tasks keep working.
	if s.proxy != nil && s.db != nil {
		task, err := s.taskSvc.GetTask(ctx, taskID)
		if err != nil {
			return nil, err
		}
		if isNewPathRunName(task.LastCodingAgentRunName) {
			return s.getAgentProgressNewPath(ctx, task, sinceMillis, limit)
		}
	}
	if s.observerClient == nil {
		return nil, ErrProgressUnavailable
	}
	if limit <= 0 || limit > defaultProgressLimit {
		limit = defaultProgressLimit
	}

	task, err := s.taskSvc.GetTask(ctx, taskID)
	if err != nil {
		return nil, err
	}

	final := !isAgentActive(task)
	resp := &ProgressResponse{
		SchemaVersion: observer.ProgressSchemaVersion,
		Lines:         []observer.ProgressEvent{},
		CursorMillis:  sinceMillis,
		Final:         final,
	}
	if task.LastCodingAgentRunName == "" {
		// Run not provisioned yet — nothing to report. Cursor is unchanged.
		return resp, nil
	}

	sinceTime := resolveSinceTime(sinceMillis, task.DispatchedAt)
	lines, err := s.fetchObserverLogs(ctx, task.LastCodingAgentRunName, task.OrgID, sinceTime, limit+1)
	if err != nil {
		return nil, err
	}

	events := parseAndSort(lines)
	if len(events) > limit {
		events = events[:limit]
		resp.Truncated = true
	}
	resp.Lines = events
	resp.CursorMillis = nextCursor(events, sinceMillis)
	resp.Phase = lastPhase(events)
	return resp, nil
}

func (s *progressService) GetBuildProgress(ctx context.Context, taskID string, sinceMillis int64) (*ProgressResponse, error) {
	task, err := s.taskSvc.GetTask(ctx, taskID)
	if err != nil {
		return nil, err
	}

	final := !isBuildActive(task)
	resp := &ProgressResponse{
		SchemaVersion: observer.ProgressSchemaVersion,
		Lines:         []observer.ProgressEvent{},
		CursorMillis:  sinceMillis,
		Final:         final,
	}
	if task.LastBuildRunName == "" {
		return resp, nil
	}

	run, err := s.ocClient.GetWorkflowRun(ctx, task.OrgID, task.LastBuildRunName)
	if err != nil {
		return nil, fmt.Errorf("get build run: %w", err)
	}

	events := s.diffBuildSteps(task.ID, run, sinceMillis)
	resp.Lines = events
	resp.CursorMillis = nextCursor(events, sinceMillis)
	resp.Phase = lastPhase(events)
	if run.Completed {
		resp.Final = true
		// Once the run is terminal there will be no more diffs; clear
		// the per-task entry so the map doesn't accumulate forever.
		s.evictStepSeen(task.ID)
	}
	return resp, nil
}

// diffBuildSteps emits a build_step event for each task whose phase
// changed since the last observation, plus any task whose StartedAt is
// strictly after sinceMillis. Stateful via stepSeen so the same step is
// not emitted twice for the same phase. Per-task entries are evicted
// when the run reaches terminal state (see GetBuildProgress).
func (s *progressService) diffBuildSteps(taskID string, run *models.WorkflowRun, sinceMillis int64) []observer.ProgressEvent {
	if run == nil {
		return nil
	}
	out := make([]observer.ProgressEvent, 0, len(run.Tasks))
	s.mu.Lock()
	defer s.mu.Unlock()
	taskSteps, ok := s.stepSeen[taskID]
	if !ok {
		taskSteps = map[string]string{}
		s.stepSeen[taskID] = taskSteps
	}
	for _, step := range run.Tasks {
		if taskSteps[step.Name] == step.Phase {
			continue
		}
		taskSteps[step.Name] = step.Phase
		ts := pickStepTs(step)
		if ts != "" && sinceMillis > 0 {
			if t, err := time.Parse(time.RFC3339, ts); err == nil && t.UnixMilli() < sinceMillis {
				continue
			}
		}
		s.buildSeq++
		out = append(out, observer.ProgressEvent{
			SchemaVersion: observer.ProgressSchemaVersion,
			Ts:            ts,
			Seq:           s.buildSeq,
			Kind:          "build_step",
			Step:          step.Name,
			Phase:         step.Phase,
			Message:       step.Message,
			StartedAt:     step.StartedAt,
			CompletedAt:   step.CompletedAt,
		})
	}
	sortEvents(out)
	return out
}

func (s *progressService) evictStepSeen(taskID string) {
	s.mu.Lock()
	delete(s.stepSeen, taskID)
	s.mu.Unlock()
}

// fetchObserverLogs wraps the Observer call in a singleflight bucket so
// concurrent viewers of the same (runName, sinceMillis-bucket) share
// one upstream call. The leader owns a detached context with a 10s
// timeout — followers wait on the channel and don't fall through to a
// non-deduped direct call (matches agent-manager's singleflight idiom
// in clients/thundersvc/client.go).
//
// ouHandle is the OC org namespace name (== ouHandle); Observer prepends
// `workflows-` to address the workflow-plane namespace where Argo
// schedules the pod that produced the logs.
func (s *progressService) fetchObserverLogs(ctx context.Context, runName, ouHandle string, sinceTime time.Time, limit int) ([]observer.LogLine, error) {
	bucket := sinceTime.Unix()
	key := fmt.Sprintf("agent|%s|%s|%d", runName, ouHandle, bucket)
	type result struct {
		lines []observer.LogLine
		err   error
	}
	ch := s.sf.DoChan(key, func() (any, error) {
		// Detach from the caller's context so a follower cancelling
		// doesn't kill the leader's upstream call. 10s hard cap keeps
		// a stuck Observer bounded.
		callCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		lines, err := s.observerClient.GetWorkflowRunLogs(callCtx, runName, ouHandle, sinceTime, limit)
		return result{lines: lines, err: err}, nil
	})
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case r := <-ch:
		if r.Err != nil {
			return nil, r.Err
		}
		res := r.Val.(result)
		if errors.Is(res.err, observer.ErrUnavailable) {
			return nil, ErrProgressUnavailable
		}
		if res.err != nil {
			return nil, res.err
		}
		return res.lines, nil
	}
}

func parseAndSort(lines []observer.LogLine) []observer.ProgressEvent {
	if len(lines) == 0 {
		return []observer.ProgressEvent{}
	}
	out := make([]observer.ProgressEvent, 0, len(lines))
	for _, l := range lines {
		ev := observer.ParseProgressLine(l.Log)
		if ev.Ts == "" && !l.Timestamp.IsZero() {
			ev.Ts = l.Timestamp.UTC().Format(time.RFC3339Nano)
		}
		out = append(out, ev)
	}
	sortEvents(out)
	out = dedupOnTsSeq(out)
	return out
}

func sortEvents(events []observer.ProgressEvent) {
	sort.SliceStable(events, func(i, j int) bool {
		if events[i].Ts != events[j].Ts {
			return events[i].Ts < events[j].Ts
		}
		return events[i].Seq < events[j].Seq
	})
}

// dedupOnTsSeq removes consecutive entries with identical (ts, seq).
// The Observer can return overlapping windows on cursor edges; the
// design (§6.5) specifies dedup on this composite key.
func dedupOnTsSeq(events []observer.ProgressEvent) []observer.ProgressEvent {
	if len(events) < 2 {
		return events
	}
	out := events[:1]
	for i := 1; i < len(events); i++ {
		prev := out[len(out)-1]
		cur := events[i]
		if cur.Ts == prev.Ts && cur.Seq == prev.Seq && cur.Seq != 0 {
			continue
		}
		out = append(out, cur)
	}
	return out
}

func lastPhase(events []observer.ProgressEvent) string {
	for i := len(events) - 1; i >= 0; i-- {
		if events[i].Kind == "phase" && events[i].Phase != "" {
			return events[i].Phase
		}
	}
	return ""
}

func nextCursor(events []observer.ProgressEvent, fallback int64) int64 {
	cursor := fallback
	for _, ev := range events {
		if ev.Ts == "" {
			continue
		}
		t, err := time.Parse(time.RFC3339Nano, ev.Ts)
		if err != nil {
			t, err = time.Parse(time.RFC3339, ev.Ts)
			if err != nil {
				continue
			}
		}
		ms := t.UnixMilli()
		if ms+1 > cursor {
			cursor = ms + 1
		}
	}
	return cursor
}

func resolveSinceTime(sinceMillis int64, dispatchedAt *time.Time) time.Time {
	if sinceMillis > 0 {
		return time.UnixMilli(sinceMillis)
	}
	if dispatchedAt != nil && !dispatchedAt.IsZero() {
		return dispatchedAt.Add(-1 * time.Second)
	}
	return time.Time{}
}

func pickStepTs(step models.WorkflowRunTask) string {
	if step.CompletedAt != "" {
		return step.CompletedAt
	}
	if step.StartedAt != "" {
		return step.StartedAt
	}
	return ""
}

func isAgentActive(task *models.ComponentTask) bool {
	return task.Status == string(models.TaskStatusInProgress)
}

func isBuildActive(task *models.ComponentTask) bool {
	return task.Status == string(models.TaskStatusBuilding)
}

// isNewPathRunName returns true for runNames minted by
// dispatch_service.codingAgentRunName (`ca-<id8>-<minute-bucket>`).
// Legacy WP-SSA dispatches use `coding-agent-<id8>-<unix-millis>`
// which doesn't start with `ca-`. Used to branch GetAgentProgress
// onto the new pods/log + sidecar source.
func isNewPathRunName(runName string) bool {
	return strings.HasPrefix(runName, "ca-")
}

// newPathLogPageBytes caps how much sidecar/live-tail text we surface
// per /progress/agent call. Together with the dispatcher's
// 256KiB final-capture limit this keeps a single response from
// blowing past the BFF's response-buffer expectations.
const newPathLogPageBytes = 64 * 1024

// getAgentProgressNewPath serves logs for `ca-…` runNames. While the
// Job is active it tails `pods/log` via the proxy; once terminal it
// reads the captured snapshot from `coding_agent_logs`. The Observer
// client is not touched here.
//
// Cursor semantics — both branches filter lines by the K8s
// `timestamps=true` prefix (RFC3339Nano), returning only events
// strictly newer than `sinceMillis`. This is required because:
//   - the live tail returns the same `?limitBytes=64KiB` window on
//     every poll, so without per-line filtering the console would
//     see the same lines on every refresh and accumulate duplicates;
//   - the live → snapshot handoff (when the Job hits terminal and the
//     watcher persists) would otherwise dump the full snapshot on top
//     of the already-appended live-tail lines — same duplicate problem.
//
// CursorMillis returned is the latest line's ts when there are lines,
// else max(sinceMillis, captured_at) on the snapshot path / now()
// on the live path. Final is true iff the snapshot row exists.
func (s *progressService) getAgentProgressNewPath(ctx context.Context, task *models.ComponentTask, sinceMillis int64, limit int) (*ProgressResponse, error) {
	// Normalize `limit` here too — `GetAgentProgress` does the same
	// before its legacy Observer branch but our new-path code runs
	// before that, so a caller hitting `/progress/agent` without
	// `?limit=` would otherwise get limit=0 which truncates every
	// line and surfaces as "blank but Final=true" in the console.
	if limit <= 0 || limit > defaultProgressLimit {
		limit = defaultProgressLimit
	}
	resp := &ProgressResponse{
		SchemaVersion: observer.ProgressSchemaVersion,
		Lines:         []observer.ProgressEvent{},
		CursorMillis:  sinceMillis,
		Final:         false,
	}
	if task.LastCodingAgentRunName == "" {
		return resp, nil
	}
	taskUUID, perr := uuid.Parse(task.ID)
	if perr != nil {
		return nil, fmt.Errorf("parse task id: %w", perr)
	}

	// Snapshot path: prefer the persisted sidecar row when present
	// (the watcher wrote it on terminal Job state).
	var snap models.CodingAgentLog
	switch err := s.db.WithContext(ctx).
		Where("task_id = ? AND run_name = ?", taskUUID, task.LastCodingAgentRunName).
		First(&snap).Error; {
	case err == nil:
		all := textToProgressEvents(snap.LogText, defaultProgressLimit, nil)
		newer := filterEventsAfter(all, sinceMillis)
		if len(newer) > limit {
			newer = newer[:limit]
			resp.Truncated = true
		}
		resp.Lines = newer
		// Cursor advances to either the last line's ts or — if the
		// snapshot is fully consumed (no new lines) — the snapshot's
		// captured_at so a second poll's sinceMillis is at or past
		// captured_at and the cursor doesn't slide backwards.
		if cur := lastEventMillis(newer); cur > resp.CursorMillis {
			resp.CursorMillis = cur
		}
		if capturedMs := snap.CapturedAt.UnixMilli(); capturedMs > resp.CursorMillis {
			resp.CursorMillis = capturedMs
		}
		resp.Final = true
		return resp, nil
	case errors.Is(err, gorm.ErrRecordNotFound):
		// fall through to live tail
	default:
		return nil, fmt.Errorf("read sidecar log: %w", err)
	}

	// Live tail path: dispatch-side persistent state hasn't captured
	// yet. Compute the NS the same way the dispatcher + watcher do
	// and tail through the proxy.
	ns, ok := s.resolveNewPathNS(ctx, task)
	if !ok {
		// NS resolution fails when the Org row is missing or has no
		// Thunder UUID yet. Surface as 503 so the console keeps polling
		// — the orgensure middleware will fill the row on the next
		// authed request, and the watcher will capture on terminal
		// state regardless.
		return nil, ErrProgressUnavailable
	}
	podName, err := s.proxy.GetJobPodName(ctx, ns, task.LastCodingAgentRunName)
	if err != nil {
		if errors.Is(err, clustergatewayproxy.ErrNotFound) {
			// Job pod hasn't been scheduled yet (Job just created) OR
			// has been GC'd past TTL with no snapshot captured. Return
			// empty + non-final so the console keeps polling for the
			// pod to appear, OR for an admin to ack the lost run.
			return resp, nil
		}
		return nil, fmt.Errorf("get job pod name: %w", err)
	}

	// Live tail: `?limitBytes=64KiB` returns the LAST 64KB of pod
	// stdout. Old lines scroll off; the console gets the freshest
	// content each poll. K8s has no native `?sinceMillis` knob — we
	// request the most-recent window then filter by ts client-side.
	body, err := s.proxy.TailPodLog(ctx, ns, podName, clustergatewayproxy.PodLogOptions{
		Timestamps: true,
		LimitBytes: newPathLogPageBytes,
	})
	if err != nil {
		if errors.Is(err, clustergatewayproxy.ErrNotFound) {
			return resp, nil
		}
		return nil, fmt.Errorf("tail pod log: %w", err)
	}
	all := textToProgressEvents(string(body), defaultProgressLimit, nil)
	newer := filterEventsAfter(all, sinceMillis)
	if len(newer) > limit {
		newer = newer[:limit]
		resp.Truncated = true
	}
	resp.Lines = newer
	// Advance the cursor to the newest line we saw, NOT to now() —
	// using now() would make the next poll's window start past lines
	// that landed in the meantime (between this fetch and `now()`),
	// silently dropping them.
	if cur := lastEventMillis(newer); cur > resp.CursorMillis {
		resp.CursorMillis = cur
	}
	// Final is false while Job is live; the watcher's snapshot capture
	// (or the next poll after Job becomes terminal) will flip Final
	// via the snapshot branch above.
	return resp, nil
}

// filterEventsAfter drops events whose ts is at or before
// `sinceMillis`. Events without a parseable ts are kept (we have no
// basis to compare them) so the BFF never silently loses content.
// Events whose ts equals sinceMillis are dropped, mirroring the
// half-open `(sinceMillis, +∞)` interval the legacy Observer path
// uses (see fetchObserverLogs's `sinceTime.Unix()` bucketing).
func filterEventsAfter(events []observer.ProgressEvent, sinceMillis int64) []observer.ProgressEvent {
	if sinceMillis <= 0 {
		return events
	}
	out := events[:0:len(events)]
	for _, e := range events {
		if e.Ts == "" {
			out = append(out, e)
			continue
		}
		t, err := time.Parse(time.RFC3339Nano, e.Ts)
		if err != nil {
			out = append(out, e)
			continue
		}
		if t.UnixMilli() > sinceMillis {
			out = append(out, e)
		}
	}
	return out
}

// lastEventMillis returns the highest UnixMilli ts in `events`, or 0
// when none of the events carry a parseable ts. Used to advance the
// cursor only as far as actually-emitted content reaches, so the
// next poll's window never skips past late-arriving lines.
func lastEventMillis(events []observer.ProgressEvent) int64 {
	var max int64
	for _, e := range events {
		if e.Ts == "" {
			continue
		}
		t, err := time.Parse(time.RFC3339Nano, e.Ts)
		if err != nil {
			continue
		}
		if m := t.UnixMilli(); m > max {
			max = m
		}
	}
	return max
}

// resolveNewPathNS mirrors JobWatcher.resolveNS — keep them in sync
// or extract to a shared helper if a third caller appears.
func (s *progressService) resolveNewPathNS(ctx context.Context, task *models.ComponentTask) (string, bool) {
	var org models.Organization
	if err := s.db.WithContext(ctx).Where("name = ?", task.OrgID).First(&org).Error; err != nil {
		return "", false
	}
	var uid string
	if org.ThunderOrgUUID != nil && *org.ThunderOrgUUID != uuid.Nil {
		uid = org.ThunderOrgUUID.String()
	} else {
		uid = org.UUID.String()
	}
	if uid == "" || uid == "00000000-0000-0000-0000-000000000000" {
		return "", false
	}
	return codingagent.RemoteWorkerNamespace(uid), true
}

// textToProgressEvents splits raw stdout/stderr into the
// observer.ProgressEvent envelope the console already knows how to
// render. Each line becomes one event; the K8s `timestamps=true`
// prefix (`YYYY-MM-DDTHH:MM:SS.NNNNNNNNNZ <line>`) is split off the
// front when present and used as the event Ts.
func textToProgressEvents(text string, limit int, truncated *bool) []observer.ProgressEvent {
	if text == "" {
		return []observer.ProgressEvent{}
	}
	if limit <= 0 || limit > defaultProgressLimit {
		limit = defaultProgressLimit
	}
	out := make([]observer.ProgressEvent, 0, 256)
	scanner := bufio.NewScanner(strings.NewReader(text))
	// Allow long lines — agent output occasionally dumps long JSON
	// blobs (Anthropic API responses) that exceed the default 64K
	// scanner buffer.
	scanner.Buffer(make([]byte, 0, 4096), 1024*1024)
	for scanner.Scan() {
		ts, msg := splitTimestampPrefix(scanner.Text())
		out = append(out, observer.ProgressEvent{
			SchemaVersion: observer.ProgressSchemaVersion,
			Ts:            ts,
			// `kind: log` + `summary` matches the typed envelope the
			// console's `summaryFor`/`iconFor` switch on (see
			// console/src/components/tasks/TaskActivityFeed.tsx:38, 52
			// and console/src/services/api/types.ts:260). The legacy
			// Observer path uses the same shape (see
			// clients/observer/schema.go:64). Using `summary` (not
			// `message`) lets the console render without any client
			// change.
			Kind:    "log",
			Summary: msg,
		})
	}
	if len(out) > limit {
		// Keep the newest `limit` events so the live tail surfaces
		// fresh output rather than the oldest captured window.
		out = out[len(out)-limit:]
		if truncated != nil {
			*truncated = true
		}
	}
	return out
}

// splitTimestampPrefix peels the K8s `?timestamps=true` prefix off a
// log line. Returns (ts, rest); ts="" when no prefix is present so
// the caller's event Ts falls back to the empty string and the
// console renders without a timestamp.
func splitTimestampPrefix(line string) (string, string) {
	i := strings.IndexByte(line, ' ')
	if i <= 0 {
		return "", line
	}
	candidate := line[:i]
	// K8s emits RFC3339Nano always; cheap shape check.
	if _, err := time.Parse(time.RFC3339Nano, candidate); err != nil {
		return "", line
	}
	return candidate, line[i+1:]
}

// silence unused-import linter on io for environments where this
// file is included without the streaming-tail branch (none today —
// kept for future expansion to true streaming via ResponseWriter).
var _ = io.EOF

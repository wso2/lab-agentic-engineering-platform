package controllers

import (
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"strings"

	"github.com/wso2/asdlc/asdlc-service/services"
	"github.com/wso2/asdlc/asdlc-service/services/webhook"
)

// isLookupNotFound reports whether err is a 404 surfaced by the routing
// lookup. A 404 means "this event is for a repo or installation that
// isn't connected to ASDLC" — ack noop instead of 5xx-retrying for hours.
func isLookupNotFound(err error) bool {
	var nfe *services.NotFoundError
	if errors.As(err, &nfe) {
		return true
	}
	if err == nil {
		return false
	}
	s := err.Error()
	return strings.Contains(s, "not found") || strings.Contains(s, "no rows")
}

// WebhookController is the BFF's inbound GitHub webhook receiver.
//
// Pipeline order (github-integration-phase0.md §8.2; PR B fills in routing):
//
//   1. Read raw body.
//   2. Parse routing key (PR B: installation.id for App-mode events,
//      repository.full_name for per-repo events).
//   3. Resolve ocOrgID via git-service (60s in-process cache).
//   4. HMAC-validate against that org's secrets.
//   5. Dedup INSERT into webhook_deliveries.
//   6. Dispatch the handler.
//   7. Mark processed → ack 200 on success; ack 5xx on handler failure
//      (GitHub redelivers up to ~9 hours).
type WebhookController interface {
	Receive(w http.ResponseWriter, r *http.Request)
}

type webhookController struct {
	verifier   *webhook.Verifier
	deliveries *webhook.DeliveryStore
	router     *webhook.Router
	lookup     webhook.OcOrgIDLookup // PR B: served by CredentialService
	cache      *webhook.RoutingCache // PR B: 60s in-process cache
}

// NewWebhookController wires the receiver. lookup + cache are required
// in PR B; passing nil disables the receiver.
func NewWebhookController(verifier *webhook.Verifier, deliveries *webhook.DeliveryStore, router *webhook.Router, lookup webhook.OcOrgIDLookup, cache *webhook.RoutingCache) WebhookController {
	return &webhookController{
		verifier:   verifier,
		deliveries: deliveries,
		router:     router,
		lookup:     lookup,
		cache:      cache,
	}
}

func (c *webhookController) Receive(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	body, err := io.ReadAll(r.Body)
	if err != nil {
		slog.ErrorContext(ctx, "webhook: read body", "error", err)
		http.Error(w, "read body", http.StatusBadRequest)
		return
	}

	deliveryID := r.Header.Get("X-GitHub-Delivery")
	event := r.Header.Get("X-GitHub-Event")
	signature := r.Header.Get("X-Hub-Signature-256")
	if deliveryID == "" || event == "" {
		http.Error(w, "missing X-GitHub-Delivery or X-GitHub-Event", http.StatusBadRequest)
		return
	}

	ocOrgID, err := webhook.ResolveOcOrgID(ctx, c.lookup, c.cache, event, body)
	if err != nil {
		// No-routing-key events (ping, etc.) are 200 ack'd as Phase 0 audit
		// behaviour.
		if errors.Is(err, webhook.ErrNoRoutingKey) {
			slog.DebugContext(ctx, "webhook: no routing key — ack noop",
				"event", event, "deliveryId", deliveryID, "result", "no_routing_key")
			w.WriteHeader(http.StatusOK)
			return
		}
		// 404 from the routing lookup means "the install / repo isn't
		// connected to this ASDLC instance." Ack 200 noop so GitHub
		// stops retrying — the event is genuinely not for us. Other
		// errors (5xx, network) bubble up as 5xx so GitHub retries.
		if isLookupNotFound(err) {
			slog.InfoContext(ctx, "webhook: routing miss — ack noop (event not for this instance)",
				"event", event, "deliveryId", deliveryID, "error", err, "result", "routing_miss")
			w.WriteHeader(http.StatusOK)
			return
		}
		slog.WarnContext(ctx, "webhook: routing failed (transient — will be retried)",
			"deliveryId", deliveryID, "event", event, "error", err, "result", "routing_failed")
		http.Error(w, "routing", http.StatusServiceUnavailable)
		return
	}

	// Refetch limiter key — bucket per (ocOrgID, sourceIP) so a single
	// remote can't amplify forged-event load against git-service.
	limiterKey := ocOrgID + "|" + r.RemoteAddr

	if err := c.verifier.VerifyWithKey(ctx, ocOrgID, limiterKey, signature, body); err != nil {
		if errors.Is(err, webhook.ErrSignatureMismatch) || errors.Is(err, webhook.ErrSignatureMalformed) {
			slog.WarnContext(ctx, "webhook: signature rejected",
				"deliveryId", deliveryID, "event", event, "ocOrgId", ocOrgID, "error", err, "result", "hmac_failed")
			http.Error(w, "signature", http.StatusUnauthorized)
			return
		}
		slog.ErrorContext(ctx, "webhook: verify error",
			"deliveryId", deliveryID, "event", event, "ocOrgId", ocOrgID, "error", err, "result", "verify_error")
		http.Error(w, "verify", http.StatusInternalServerError)
		return
	}

	action := actionFromPayload(body)
	res, err := c.deliveries.Persist(ctx, deliveryID, ocOrgID, event, action, body)
	if err != nil {
		slog.ErrorContext(ctx, "webhook: persist failed",
			"deliveryId", deliveryID, "event", event, "error", err, "result", "persist_failed")
		http.Error(w, "persist", http.StatusInternalServerError)
		return
	}
	if res.AlreadyProcessed {
		// Replay of finished work — ack and move on.
		slog.InfoContext(ctx, "webhook: dedup — already processed",
			"deliveryId", deliveryID, "event", event, "action", action, "ocOrgId", ocOrgID, "result", "dedup")
		w.WriteHeader(http.StatusOK)
		return
	}

	// Dispatch synchronously (Phase 0). Durable queue + async processing is
	// a §9.1 hardening item. Errors drive the ack: 5xx → GitHub retries; the
	// dedup row is preserved so retries re-enter the handler.
	if err := c.router.Dispatch(ctx, event, body); err != nil {
		_ = c.deliveries.MarkFailed(ctx, deliveryID, err.Error())
		slog.ErrorContext(ctx, "webhook: handler failed",
			"deliveryId", deliveryID, "event", event, "error", err, "result", "handler_failed")
		http.Error(w, "handler", http.StatusInternalServerError)
		return
	}

	if err := c.deliveries.MarkProcessed(ctx, deliveryID); err != nil {
		slog.WarnContext(ctx, "webhook: mark processed failed",
			"deliveryId", deliveryID, "error", err)
	}
	slog.InfoContext(ctx, "webhook: accepted",
		"deliveryId", deliveryID, "event", event, "action", action, "ocOrgId", ocOrgID, "result", "accepted")
	w.WriteHeader(http.StatusOK)
}

func actionFromPayload(body []byte) string {
	var withAction struct {
		Action string `json:"action"`
	}
	_ = json.Unmarshal(body, &withAction)
	return withAction.Action
}

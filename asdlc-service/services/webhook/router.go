package webhook

import (
	"context"
	"encoding/json"
	"log/slog"
)

// Router dispatches a verified, dedup'd delivery to the per-event handler.
//
// Phase 0 implementation logs and no-ops every event — Step 8 of the
// migration plugs in real handlers (pull_request.{opened,ready_for_review,
// closed,reopened}, push, issue_comment). The seam exists so the receiver
// pipeline can land first; handlers attach later without touching the
// pipeline shape.
type Router struct {
	handlers map[string]EventHandler
}

// EventHandler is the contract every per-event handler implements. The
// (event, action) tuple drives dispatch; the raw payload is provided for
// the handler to parse what it needs. Idempotency is the handler's
// responsibility — a redelivery may invoke the handler again.
type EventHandler interface {
	Handle(ctx context.Context, event, action string, payload []byte) error
}

// EventHandlerFunc adapts a function to EventHandler.
type EventHandlerFunc func(ctx context.Context, event, action string, payload []byte) error

func (f EventHandlerFunc) Handle(ctx context.Context, event, action string, payload []byte) error {
	return f(ctx, event, action, payload)
}

func NewRouter() *Router {
	return &Router{handlers: map[string]EventHandler{}}
}

// Register installs a handler for an event class. Pass action="" to register
// a fallback for the event when no action-specific handler matches.
//
// Lookup order: (event, action) → (event, "") → log + no-op.
func (r *Router) Register(event, action string, h EventHandler) {
	r.handlers[key(event, action)] = h
}

// Dispatch parses the action from the payload and runs the matching handler.
// Returns the handler error verbatim — the receiver decides ack 200 vs. 5xx
// based on whether the error is nil.
func (r *Router) Dispatch(ctx context.Context, event string, payload []byte) error {
	action := parseAction(payload)
	if h, ok := r.handlers[key(event, action)]; ok {
		return h.Handle(ctx, event, action, payload)
	}
	if h, ok := r.handlers[key(event, "")]; ok {
		return h.Handle(ctx, event, action, payload)
	}
	// Persisted, no-op. Phase 0 intentionally swallows unknown events.
	slog.DebugContext(ctx, "webhook: no handler", "event", event, "action", action, "result", "unhandled_event")
	return nil
}

// ParseAction returns the payload's "action" field if present, else "".
func parseAction(payload []byte) string {
	var body struct {
		Action string `json:"action"`
	}
	if err := json.Unmarshal(payload, &body); err != nil {
		return ""
	}
	return body.Action
}

func key(event, action string) string {
	if action == "" {
		return event
	}
	return event + ":" + action
}

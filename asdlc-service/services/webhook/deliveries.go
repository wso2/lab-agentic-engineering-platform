package webhook

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"gorm.io/gorm"

	"github.com/wso2/asdlc/asdlc-service/models"
)

// DeliveryStore persists inbound webhook deliveries with free dedup via the
// PK on delivery_id. Payloads are stored in a sibling table so retention
// can drop the bulk while keeping dedup history.
type DeliveryStore struct {
	db *gorm.DB
}

func NewDeliveryStore(db *gorm.DB) *DeliveryStore {
	return &DeliveryStore{db: db}
}

// PersistResult is what the receiver acts on to decide ack vs. dispatch.
type PersistResult struct {
	// Created is true when the delivery row was newly inserted (not a replay).
	Created bool
	// AlreadyProcessed is true when an existing row has ProcessedAt set —
	// the receiver acks 200 without re-running the handler.
	AlreadyProcessed bool
}

// Persist atomically dedups + stores the delivery and the raw payload.
// Returns Created=true on a fresh insert, AlreadyProcessed=true when a row
// exists with processed_at set, and (false, false) when the row exists but
// processing failed earlier — the receiver re-runs the handler in that case
// (GitHub-redelivery semantics: handler must be idempotent).
func (s *DeliveryStore) Persist(ctx context.Context, deliveryID, ocOrgID, event, action string, payload []byte) (PersistResult, error) {
	if deliveryID == "" {
		return PersistResult{}, fmt.Errorf("delivery id required")
	}

	now := time.Now().UTC()
	row := models.WebhookDelivery{
		DeliveryID: deliveryID,
		OcOrgID:    ocOrgID,
		Event:      event,
		Action:     action,
		ReceivedAt: now,
	}
	res := s.db.WithContext(ctx).Clauses().Create(&row)
	if res.Error == nil {
		// Fresh insert. Persist the payload too.
		if err := s.db.WithContext(ctx).Create(&models.WebhookPayload{
			DeliveryID: deliveryID,
			Payload:    payload,
			CreatedAt:  now,
		}).Error; err != nil {
			return PersistResult{}, fmt.Errorf("persist payload: %w", err)
		}
		return PersistResult{Created: true}, nil
	}

	// Conflict on PK (existing delivery). Look up to decide whether it was
	// already processed.
	if !isUniqueViolation(res.Error) {
		return PersistResult{}, fmt.Errorf("persist delivery: %w", res.Error)
	}

	var existing models.WebhookDelivery
	if err := s.db.WithContext(ctx).
		Where("delivery_id = ?", deliveryID).
		First(&existing).Error; err != nil {
		return PersistResult{}, fmt.Errorf("lookup existing: %w", err)
	}
	return PersistResult{AlreadyProcessed: existing.ProcessedAt != nil}, nil
}

// MarkProcessed records successful processing on the delivery row so a
// redelivery is acked without re-running.
func (s *DeliveryStore) MarkProcessed(ctx context.Context, deliveryID string) error {
	now := time.Now().UTC()
	return s.db.WithContext(ctx).
		Model(&models.WebhookDelivery{}).
		Where("delivery_id = ?", deliveryID).
		Updates(map[string]any{
			"processed_at":  &now,
			"process_error": "",
		}).Error
}

// MarkFailed records a processing error so on-call can audit; the row's
// processed_at stays null so GitHub redelivery re-runs the handler.
func (s *DeliveryStore) MarkFailed(ctx context.Context, deliveryID string, errMsg string) error {
	return s.db.WithContext(ctx).
		Model(&models.WebhookDelivery{}).
		Where("delivery_id = ?", deliveryID).
		Update("process_error", errMsg).Error
}

func isUniqueViolation(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, gorm.ErrDuplicatedKey) {
		return true
	}
	msg := err.Error()
	return strings.Contains(msg, "duplicate key") || strings.Contains(msg, "unique constraint")
}

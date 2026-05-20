package models

import "time"

// WebhookDelivery is the dedup row for an inbound GitHub event.
//
// The PK on DeliveryID (X-GitHub-Delivery, a UUID) gives us free dedup via
// `INSERT … ON CONFLICT DO NOTHING`. Payload retention is handled separately
// in WebhookPayload so a retention sweep can drop the bulk while preserving
// dedup history.
type WebhookDelivery struct {
	DeliveryID   string     `gorm:"primaryKey;type:text" json:"deliveryId"`
	OcOrgID      string     `gorm:"index;not null;type:text" json:"ocOrgId"`
	Event        string     `gorm:"index;not null;type:text" json:"event"`
	Action       string     `gorm:"index;type:text" json:"action,omitempty"`
	ReceivedAt   time.Time  `gorm:"index;not null" json:"receivedAt"`
	ProcessedAt  *time.Time `json:"processedAt,omitempty"`
	ProcessError string     `gorm:"type:text" json:"processError,omitempty"`
}

// WebhookPayload holds the raw event body. Split from WebhookDelivery so
// payload retention can run independently of dedup-history retention.
//
// Stored as raw bytes (jsonb in Postgres) so we don't pay parse cost at write
// time. Handlers re-parse on read.
type WebhookPayload struct {
	DeliveryID string    `gorm:"primaryKey;type:text" json:"deliveryId"`
	Payload    []byte    `gorm:"type:jsonb;not null" json:"payload"`
	CreatedAt  time.Time `gorm:"index;not null" json:"createdAt"`
}

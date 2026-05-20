package models

import (
	"time"

	"github.com/google/uuid"
)

// Organization is a local UUID side-car for an OpenChoreo namespace.
//
// OpenChoreo has no Organization CRD — namespaces *are* organizational
// boundaries. The BFF maintains UUIDs locally so other tables can foreign-key
// onto an org without depending on the OC namespace name (which is mutable in
// principle and ambiguous as a join key across renames).
type Organization struct {
	UUID        uuid.UUID `gorm:"type:uuid;primaryKey" json:"uuid"`
	Name        string    `gorm:"uniqueIndex;not null" json:"name"`
	DisplayName string    `gorm:"" json:"displayName,omitempty"`
	CreatedBy   string    `gorm:"" json:"createdBy,omitempty"`
	CreatedAt   time.Time `gorm:"not null;default:CURRENT_TIMESTAMP" json:"createdAt"`
}

// OrganizationView is the API response shape — joins the local UUID with the
// OC namespace's display fields.
type OrganizationView struct {
	UUID        uuid.UUID `json:"uuid"`
	Name        string    `json:"name"`
	DisplayName string    `json:"displayName,omitempty"`
	Description string    `json:"description,omitempty"`
	Status      string    `json:"status,omitempty"`
	CreatedAt   time.Time `json:"createdAt"`
}

// OrganizationList is the list-endpoint response.
type OrganizationList struct {
	Items []OrganizationView `json:"items"`
}

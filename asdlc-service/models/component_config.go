package models

import "time"

// EnvVar represents a single environment variable key-value pair.
type EnvVar struct {
	Key   string `json:"key"`
	Value string `json:"value"`
}

// EnvVarSlice is a typed slice for JSONB storage in PostgreSQL.
type EnvVarSlice []EnvVar

// ComponentConfig stores environment variable configuration for a component.
// Scoped by (OrgID, ProjectName, ComponentName) — one config record per component.
type ComponentConfig struct {
	ID            string      `gorm:"primaryKey;type:uuid;default:gen_random_uuid()" json:"id"`
	OrgID         string      `gorm:"uniqueIndex:idx_component_config;not null" json:"-"`
	ProjectName   string      `gorm:"uniqueIndex:idx_component_config;not null" json:"projectName"`
	ComponentName string      `gorm:"uniqueIndex:idx_component_config;not null" json:"componentName"`
	EnvVars       EnvVarSlice `gorm:"type:jsonb;serializer:json" json:"envVars"`
	CreatedAt     time.Time   `json:"createdAt"`
	UpdatedAt     time.Time   `json:"updatedAt"`
}

// Package entity defines the base entity type for all Relay domain objects.
package entity

import "time"

// Entity is the base type embedded by all relay domain objects.
type Entity struct {
	CreatedAt time.Time `json:"created_at" bun:"created_at,notnull,default:current_timestamp"`
	UpdatedAt time.Time `json:"updated_at" bun:"updated_at,notnull,default:current_timestamp"`
}

// New returns an Entity with both timestamps set to the current UTC time.
func New() Entity {
	now := time.Now().UTC()
	return Entity{CreatedAt: now, UpdatedAt: now}
}

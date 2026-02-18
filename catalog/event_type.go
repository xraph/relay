package catalog

import (
	"time"

	"github.com/xraph/relay/id"
	"github.com/xraph/relay/internal/entity"
)

// EventType is the database entity for a registered webhook event type.
// It wraps WebhookDefinition with identity, state, and scope.
type EventType struct {
	entity.Entity

	// ID is the unique TypeID for this event type.
	ID id.ID `json:"id"`

	// Definition contains the webhook event type descriptor.
	Definition WebhookDefinition `json:"definition"`

	// IsDeprecated indicates whether this event type has been soft-deleted.
	IsDeprecated bool `json:"deprecated"`

	// DeprecatedAt is when the event type was deprecated.
	DeprecatedAt *time.Time `json:"deprecated_at,omitempty"`

	// ScopeAppID scopes the event type to a specific app (platform-level).
	ScopeAppID string `json:"scope_app_id,omitempty"`

	// Metadata holds user-defined key-value pairs.
	Metadata map[string]string `json:"metadata,omitempty"`
}

// ListOpts configures filtering and pagination for event type listing.
type ListOpts struct {
	Offset            int
	Limit             int
	Group             string
	IncludeDeprecated bool
}

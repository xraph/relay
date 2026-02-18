package event

import (
	"time"

	"github.com/xraph/relay/id"
	"github.com/xraph/relay/internal/entity"
)

// Event represents a webhook event submitted for delivery.
type Event struct {
	entity.Entity

	// ID is the unique TypeID for this event.
	ID id.ID `json:"id"`

	// Type is the dot-separated event type name (e.g. "invoice.created").
	Type string `json:"type"`

	// TenantID identifies the tenant that sent this event.
	TenantID string `json:"tenant_id"`

	// Data is the event payload. Validated against JSON Schema if configured.
	Data any `json:"data"`

	// ScopeAppID scopes the event to a specific app.
	ScopeAppID string `json:"scope_app_id,omitempty"`

	// ScopeOrgID scopes the event to a specific organization.
	ScopeOrgID string `json:"scope_org_id,omitempty"`

	// IdempotencyKey prevents duplicate event processing.
	IdempotencyKey string `json:"idempotency_key,omitempty"`
}

// ListOpts configures filtering and pagination for event listing.
type ListOpts struct {
	Offset int
	Limit  int
	Type   string
	From   *time.Time
	To     *time.Time
}

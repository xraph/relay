package endpoint

import (
	"github.com/xraph/relay/id"
	"github.com/xraph/relay/internal/entity"
)

// Endpoint represents a webhook delivery target registered by a tenant.
type Endpoint struct {
	entity.Entity

	// ID is the unique TypeID for this endpoint.
	ID id.ID `json:"id"`

	// TenantID identifies the tenant that owns this endpoint.
	TenantID string `json:"tenant_id"`

	// URL is the webhook delivery URL.
	URL string `json:"url"`

	// Description is a human-readable description of this endpoint.
	Description string `json:"description"`

	// Secret is the HMAC signing secret for this endpoint. Never serialized.
	Secret string `json:"-"`

	// EventTypes are glob patterns for event type subscriptions.
	EventTypes []string `json:"event_types"`

	// Headers are custom HTTP headers sent with each delivery.
	Headers map[string]string `json:"headers,omitempty"`

	// Enabled indicates whether the endpoint is active for deliveries.
	Enabled bool `json:"enabled"`

	// RateLimit is the maximum deliveries per second. 0 means unlimited.
	RateLimit int `json:"rate_limit"`

	// ScopeAppID scopes the endpoint to a specific app.
	ScopeAppID string `json:"scope_app_id,omitempty"`

	// ScopeOrgID scopes the endpoint to a specific organization.
	ScopeOrgID string `json:"scope_org_id,omitempty"`

	// Metadata holds user-defined key-value pairs.
	Metadata map[string]string `json:"metadata,omitempty"`
}

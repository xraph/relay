package endpoint

// Input is the creation/update payload for endpoints.
type Input struct {
	// TenantID identifies the tenant that owns this endpoint.
	TenantID string `json:"tenant_id"`

	// URL is the webhook delivery URL.
	URL string `json:"url"`

	// Description is a human-readable description.
	Description string `json:"description"`

	// Secret is the HMAC signing secret. Auto-generated if empty on create.
	Secret string `json:"secret"`

	// EventTypes are glob patterns for event type subscriptions.
	EventTypes []string `json:"event_types"`

	// Headers are custom HTTP headers sent with each delivery.
	Headers map[string]string `json:"headers,omitempty"`

	// RateLimit is the maximum deliveries per second. 0 means unlimited.
	RateLimit int `json:"rate_limit"`

	// Metadata holds user-defined key-value pairs.
	Metadata map[string]string `json:"metadata,omitempty"`
}

// ListOpts configures filtering and pagination for endpoint listing.
type ListOpts struct {
	Offset  int
	Limit   int
	Enabled *bool
}

package api

import (
	"encoding/json"

	"github.com/xraph/relay/id"
)

// ---------------------------------------------------------------------------
// Event type requests
// ---------------------------------------------------------------------------

// CreateEventTypeForgeRequest binds the body for POST /event-types.
type CreateEventTypeForgeRequest struct {
	Name          string            `description:"Event type name (e.g. order.created)" json:"name"`
	Description   string            `description:"Human-readable description"           json:"description"`
	Group         string            `description:"Grouping key"                         json:"group,omitempty"`
	Schema        json.RawMessage   `description:"JSON Schema for payload validation"   json:"schema,omitempty"`
	SchemaVersion string            `description:"Schema version"                       json:"schema_version,omitempty"`
	Version       string            `description:"Event type version"                   json:"version,omitempty"`
	ScopeAppID    string            `description:"Scope to specific app"                json:"scope_app_id,omitempty"`
	Metadata      map[string]string `description:"Arbitrary key-value metadata"         json:"metadata,omitempty"`
}

// ListEventTypesForgeRequest binds query parameters for GET /event-types.
type ListEventTypesForgeRequest struct {
	Group             string `description:"Filter by group"             query:"group"`
	IncludeDeprecated string `description:"Include deprecated types"    query:"include_deprecated"`
	Offset            int    `description:"Pagination offset"           query:"offset"`
	Limit             int    `description:"Page size (default 50)"      query:"limit"`
}

// GetEventTypeForgeRequest binds the path for GET /event-types/:name.
type GetEventTypeForgeRequest struct {
	Name string `description:"Event type name" path:"name"`
}

// DeleteEventTypeForgeRequest binds the path for DELETE /event-types/:name.
type DeleteEventTypeForgeRequest struct {
	Name string `description:"Event type name" path:"name"`
}

// ---------------------------------------------------------------------------
// Endpoint requests
// ---------------------------------------------------------------------------

// CreateEndpointForgeRequest binds the body for POST /endpoints.
type CreateEndpointForgeRequest struct {
	TenantID    string            `description:"Tenant identifier"          json:"tenant_id"`
	URL         string            `description:"Webhook delivery URL"       json:"url"`
	Description string            `description:"Endpoint description"       json:"description,omitempty"`
	EventTypes  []string          `description:"Subscribed event patterns"  json:"event_types"`
	Headers     map[string]string `description:"Custom HTTP headers"        json:"headers,omitempty"`
	RateLimit   int               `description:"Requests per second limit"  json:"rate_limit,omitempty"`
	Metadata    map[string]string `description:"Arbitrary key-value metadata" json:"metadata,omitempty"`
}

// ListEndpointsForgeRequest binds query parameters for GET /endpoints.
type ListEndpointsForgeRequest struct {
	TenantID string `description:"Filter by tenant"     query:"tenant_id"`
	Offset   int    `description:"Pagination offset"    query:"offset"`
	Limit    int    `description:"Page size (default 50)" query:"limit"`
}

// GetEndpointForgeRequest binds the path for GET /endpoints/:endpointId.
type GetEndpointForgeRequest struct {
	EndpointID string `description:"Endpoint identifier" path:"endpointId"`
}

// UpdateEndpointForgeRequest binds path + body for PUT /endpoints/:endpointId.
type UpdateEndpointForgeRequest struct {
	EndpointID  string            `description:"Endpoint identifier"        path:"endpointId"`
	URL         string            `description:"Webhook delivery URL"       json:"url,omitempty"`
	Description string            `description:"Endpoint description"       json:"description,omitempty"`
	EventTypes  []string          `description:"Subscribed event patterns"  json:"event_types,omitempty"`
	Headers     map[string]string `description:"Custom HTTP headers"        json:"headers,omitempty"`
	RateLimit   int               `description:"Requests per second limit"  json:"rate_limit,omitempty"`
	Metadata    map[string]string `description:"Arbitrary key-value metadata" json:"metadata,omitempty"`
}

// DeleteEndpointForgeRequest binds the path for DELETE /endpoints/:endpointId.
type DeleteEndpointForgeRequest struct {
	EndpointID string `description:"Endpoint identifier" path:"endpointId"`
}

// EndpointActionForgeRequest binds the path for enable/disable/rotate-secret.
type EndpointActionForgeRequest struct {
	EndpointID string `description:"Endpoint identifier" path:"endpointId"`
}

// ---------------------------------------------------------------------------
// Event requests
// ---------------------------------------------------------------------------

// CreateEventForgeRequest binds the body for POST /events.
type CreateEventForgeRequest struct {
	Type           string          `description:"Event type name"       json:"type"`
	TenantID       string          `description:"Tenant identifier"    json:"tenant_id"`
	Data           json.RawMessage `description:"Event payload"        json:"data"`
	IdempotencyKey string          `description:"Idempotency key"      json:"idempotency_key,omitempty"`
}

// ListEventsForgeRequest binds query parameters for GET /events.
type ListEventsForgeRequest struct {
	Type   string `description:"Filter by event type"  query:"type"`
	Offset int    `description:"Pagination offset"     query:"offset"`
	Limit  int    `description:"Page size (default 50)" query:"limit"`
}

// GetEventForgeRequest binds the path for GET /events/:eventId.
type GetEventForgeRequest struct {
	EventID string `description:"Event identifier" path:"eventId"`
}

// ---------------------------------------------------------------------------
// Delivery requests
// ---------------------------------------------------------------------------

// ListDeliveriesForgeRequest binds path + query for GET /endpoints/:endpointId/deliveries.
type ListDeliveriesForgeRequest struct {
	EndpointID string `description:"Endpoint identifier"  path:"endpointId"`
	State      string `description:"Filter by state"      query:"state"`
	Offset     int    `description:"Pagination offset"    query:"offset"`
	Limit      int    `description:"Page size (default 50)" query:"limit"`
}

// ---------------------------------------------------------------------------
// DLQ requests
// ---------------------------------------------------------------------------

// ListDLQForgeRequest binds query parameters for GET /dlq.
type ListDLQForgeRequest struct {
	TenantID string `description:"Filter by tenant"     query:"tenant_id"`
	Offset   int    `description:"Pagination offset"    query:"offset"`
	Limit    int    `description:"Page size (default 50)" query:"limit"`
}

// ReplayDLQForgeRequest binds the path for POST /dlq/:dlqId/replay.
type ReplayDLQForgeRequest struct {
	DLQID string `description:"DLQ entry identifier" path:"dlqId"`
}

// ReplayBulkDLQForgeRequest binds the body for POST /dlq/replay.
type ReplayBulkDLQForgeRequest struct {
	From string `description:"Start time (RFC3339)" json:"from"`
	To   string `description:"End time (RFC3339)"   json:"to"`
}

// ---------------------------------------------------------------------------
// Stats requests
// ---------------------------------------------------------------------------

// StatsForgeRequest is empty — GET /stats has no parameters.
type StatsForgeRequest struct{}

// StatsForgeResponse is the response for GET /stats.
type StatsForgeResponse struct {
	PendingDeliveries int64 `json:"pending_deliveries"`
	DLQSize           int64 `json:"dlq_size"`
}

// SecretForgeResponse is the response for POST /endpoints/:endpointId/rotate-secret.
type SecretForgeResponse struct {
	Secret string `json:"secret"`
}

// ReplayBulkForgeResponse is the response for POST /dlq/replay.
type ReplayBulkForgeResponse struct {
	Replayed int64 `json:"replayed"`
}

// ---------------------------------------------------------------------------
// Helper -- compile-time check that id.ID is used (keep import alive).
// ---------------------------------------------------------------------------

var _ = id.Nil

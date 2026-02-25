package mongo

import (
	"encoding/json"
	"fmt"
	"time"

	"github.com/xraph/grove"

	"github.com/xraph/relay/catalog"
	"github.com/xraph/relay/delivery"
	"github.com/xraph/relay/dlq"
	"github.com/xraph/relay/endpoint"
	"github.com/xraph/relay/event"
	"github.com/xraph/relay/id"
	"github.com/xraph/relay/internal/entity"
)

// --- Event Type models ---

type eventTypeModel struct {
	grove.BaseModel `grove:"table:relay_event_types"`

	ID            string            `grove:"id,pk"           bson:"_id"`
	Name          string            `grove:"name,unique"     bson:"name"`
	Description   string            `grove:"description"     bson:"description"`
	GroupName     string            `grove:"group_name"      bson:"group_name"`
	Schema        json.RawMessage   `grove:"schema"          bson:"schema,omitempty"`
	SchemaVersion string            `grove:"schema_version"  bson:"schema_version"`
	Version       string            `grove:"version"         bson:"version"`
	Example       json.RawMessage   `grove:"example"         bson:"example,omitempty"`
	IsDeprecated  bool              `grove:"is_deprecated"   bson:"is_deprecated"`
	DeprecatedAt  *time.Time        `grove:"deprecated_at"   bson:"deprecated_at,omitempty"`
	ScopeAppID    string            `grove:"scope_app_id"    bson:"scope_app_id"`
	Metadata      map[string]string `grove:"metadata"        bson:"metadata,omitempty"`
	CreatedAt     time.Time         `grove:"created_at"      bson:"created_at"`
	UpdatedAt     time.Time         `grove:"updated_at"      bson:"updated_at"`
}

func toEventTypeModel(et *catalog.EventType) *eventTypeModel {
	return &eventTypeModel{
		ID:            et.ID.String(),
		Name:          et.Definition.Name,
		Description:   et.Definition.Description,
		GroupName:     et.Definition.Group,
		Schema:        et.Definition.Schema,
		SchemaVersion: et.Definition.SchemaVersion,
		Version:       et.Definition.Version,
		Example:       et.Definition.Example,
		IsDeprecated:  et.IsDeprecated,
		DeprecatedAt:  et.DeprecatedAt,
		ScopeAppID:    et.ScopeAppID,
		Metadata:      et.Metadata,
		CreatedAt:     et.CreatedAt,
		UpdatedAt:     et.UpdatedAt,
	}
}

func fromEventTypeModel(m *eventTypeModel) (*catalog.EventType, error) {
	etID, err := id.ParseEventTypeID(m.ID)
	if err != nil {
		return nil, fmt.Errorf("parse event type ID %q: %w", m.ID, err)
	}

	return &catalog.EventType{
		Entity: entity.Entity{
			CreatedAt: m.CreatedAt,
			UpdatedAt: m.UpdatedAt,
		},
		ID: etID,
		Definition: catalog.WebhookDefinition{
			Name:          m.Name,
			Description:   m.Description,
			Group:         m.GroupName,
			Schema:        m.Schema,
			SchemaVersion: m.SchemaVersion,
			Version:       m.Version,
			Example:       m.Example,
		},
		IsDeprecated: m.IsDeprecated,
		DeprecatedAt: m.DeprecatedAt,
		ScopeAppID:   m.ScopeAppID,
		Metadata:     m.Metadata,
	}, nil
}

// --- Endpoint models ---

type endpointModel struct {
	grove.BaseModel `grove:"table:relay_endpoints"`

	ID          string            `grove:"id,pk"       bson:"_id"`
	TenantID    string            `grove:"tenant_id"   bson:"tenant_id"`
	URL         string            `grove:"url"         bson:"url"`
	Description string            `grove:"description" bson:"description"`
	Secret      string            `grove:"secret"      bson:"secret"`
	EventTypes  []string          `grove:"event_types" bson:"event_types"`
	Headers     map[string]string `grove:"headers"     bson:"headers,omitempty"`
	Enabled     bool              `grove:"enabled"     bson:"enabled"`
	RateLimit   int               `grove:"rate_limit"  bson:"rate_limit"`
	Metadata    map[string]string `grove:"metadata"    bson:"metadata,omitempty"`
	CreatedAt   time.Time         `grove:"created_at"  bson:"created_at"`
	UpdatedAt   time.Time         `grove:"updated_at"  bson:"updated_at"`
}

func toEndpointModel(ep *endpoint.Endpoint) *endpointModel {
	return &endpointModel{
		ID:          ep.ID.String(),
		TenantID:    ep.TenantID,
		URL:         ep.URL,
		Description: ep.Description,
		Secret:      ep.Secret,
		EventTypes:  ep.EventTypes,
		Headers:     ep.Headers,
		Enabled:     ep.Enabled,
		RateLimit:   ep.RateLimit,
		Metadata:    ep.Metadata,
		CreatedAt:   ep.CreatedAt,
		UpdatedAt:   ep.UpdatedAt,
	}
}

func fromEndpointModel(m *endpointModel) (*endpoint.Endpoint, error) {
	epID, err := id.ParseEndpointID(m.ID)
	if err != nil {
		return nil, fmt.Errorf("parse endpoint ID %q: %w", m.ID, err)
	}

	return &endpoint.Endpoint{
		Entity: entity.Entity{
			CreatedAt: m.CreatedAt,
			UpdatedAt: m.UpdatedAt,
		},
		ID:          epID,
		TenantID:    m.TenantID,
		URL:         m.URL,
		Description: m.Description,
		Secret:      m.Secret,
		EventTypes:  m.EventTypes,
		Headers:     m.Headers,
		Enabled:     m.Enabled,
		RateLimit:   m.RateLimit,
		Metadata:    m.Metadata,
	}, nil
}

// --- Event models ---

type eventModel struct {
	grove.BaseModel `grove:"table:relay_events"`

	ID             string    `grove:"id,pk"           bson:"_id"`
	Type           string    `grove:"type"            bson:"type"`
	TenantID       string    `grove:"tenant_id"       bson:"tenant_id"`
	Data           any       `grove:"data"            bson:"data,omitempty"`
	IdempotencyKey string    `grove:"idempotency_key" bson:"idempotency_key,omitempty"`
	ScopeAppID     string    `grove:"scope_app_id"    bson:"scope_app_id"`
	ScopeOrgID     string    `grove:"scope_org_id"    bson:"scope_org_id"`
	CreatedAt      time.Time `grove:"created_at"      bson:"created_at"`
	UpdatedAt      time.Time `grove:"updated_at"      bson:"updated_at"`
}

func toEventModel(evt *event.Event) *eventModel {
	return &eventModel{
		ID:             evt.ID.String(),
		Type:           evt.Type,
		TenantID:       evt.TenantID,
		Data:           evt.Data,
		IdempotencyKey: evt.IdempotencyKey,
		ScopeAppID:     evt.ScopeAppID,
		ScopeOrgID:     evt.ScopeOrgID,
		CreatedAt:      evt.CreatedAt,
		UpdatedAt:      evt.UpdatedAt,
	}
}

func fromEventModel(m *eventModel) (*event.Event, error) {
	evtID, err := id.ParseEventID(m.ID)
	if err != nil {
		return nil, fmt.Errorf("parse event ID %q: %w", m.ID, err)
	}

	return &event.Event{
		Entity: entity.Entity{
			CreatedAt: m.CreatedAt,
			UpdatedAt: m.UpdatedAt,
		},
		ID:             evtID,
		Type:           m.Type,
		TenantID:       m.TenantID,
		Data:           m.Data,
		IdempotencyKey: m.IdempotencyKey,
		ScopeAppID:     m.ScopeAppID,
		ScopeOrgID:     m.ScopeOrgID,
	}, nil
}

// --- Delivery models ---

type deliveryModel struct {
	grove.BaseModel `grove:"table:relay_deliveries"`

	ID             string     `grove:"id,pk"            bson:"_id"`
	EventID        string     `grove:"event_id"         bson:"event_id"`
	EndpointID     string     `grove:"endpoint_id"      bson:"endpoint_id"`
	State          string     `grove:"state"            bson:"state"`
	AttemptCount   int        `grove:"attempt_count"    bson:"attempt_count"`
	MaxAttempts    int        `grove:"max_attempts"     bson:"max_attempts"`
	NextAttemptAt  time.Time  `grove:"next_attempt_at"  bson:"next_attempt_at"`
	LastError      string     `grove:"last_error"       bson:"last_error"`
	LastStatusCode int        `grove:"last_status_code" bson:"last_status_code"`
	LastResponse   string     `grove:"last_response"    bson:"last_response"`
	LastLatencyMs  int        `grove:"last_latency_ms"  bson:"last_latency_ms"`
	CompletedAt    *time.Time `grove:"completed_at"     bson:"completed_at,omitempty"`
	CreatedAt      time.Time  `grove:"created_at"       bson:"created_at"`
	UpdatedAt      time.Time  `grove:"updated_at"       bson:"updated_at"`
}

func toDeliveryModel(d *delivery.Delivery) *deliveryModel {
	return &deliveryModel{
		ID:             d.ID.String(),
		EventID:        d.EventID.String(),
		EndpointID:     d.EndpointID.String(),
		State:          string(d.State),
		AttemptCount:   d.AttemptCount,
		MaxAttempts:    d.MaxAttempts,
		NextAttemptAt:  d.NextAttemptAt,
		LastError:      d.LastError,
		LastStatusCode: d.LastStatusCode,
		LastResponse:   d.LastResponse,
		LastLatencyMs:  d.LastLatencyMs,
		CompletedAt:    d.CompletedAt,
		CreatedAt:      d.CreatedAt,
		UpdatedAt:      d.UpdatedAt,
	}
}

func fromDeliveryModel(m *deliveryModel) (*delivery.Delivery, error) {
	delID, err := id.ParseDeliveryID(m.ID)
	if err != nil {
		return nil, fmt.Errorf("parse delivery ID %q: %w", m.ID, err)
	}

	evtID, err := id.ParseEventID(m.EventID)
	if err != nil {
		return nil, fmt.Errorf("parse event ID %q: %w", m.EventID, err)
	}

	epID, err := id.ParseEndpointID(m.EndpointID)
	if err != nil {
		return nil, fmt.Errorf("parse endpoint ID %q: %w", m.EndpointID, err)
	}

	return &delivery.Delivery{
		Entity: entity.Entity{
			CreatedAt: m.CreatedAt,
			UpdatedAt: m.UpdatedAt,
		},
		ID:             delID,
		EventID:        evtID,
		EndpointID:     epID,
		State:          delivery.State(m.State),
		AttemptCount:   m.AttemptCount,
		MaxAttempts:    m.MaxAttempts,
		NextAttemptAt:  m.NextAttemptAt,
		LastError:      m.LastError,
		LastStatusCode: m.LastStatusCode,
		LastResponse:   m.LastResponse,
		LastLatencyMs:  m.LastLatencyMs,
		CompletedAt:    m.CompletedAt,
	}, nil
}

// --- DLQ models ---

type dlqEntryModel struct {
	grove.BaseModel `grove:"table:relay_dlq"`

	ID             string     `grove:"id,pk"            bson:"_id"`
	DeliveryID     string     `grove:"delivery_id"      bson:"delivery_id"`
	EventID        string     `grove:"event_id"         bson:"event_id"`
	EndpointID     string     `grove:"endpoint_id"      bson:"endpoint_id"`
	TenantID       string     `grove:"tenant_id"        bson:"tenant_id"`
	EventType      string     `grove:"event_type"       bson:"event_type"`
	URL            string     `grove:"url"              bson:"url"`
	Payload        any        `grove:"payload"          bson:"payload,omitempty"`
	Error          string     `grove:"error"            bson:"error"`
	AttemptCount   int        `grove:"attempt_count"    bson:"attempt_count"`
	LastStatusCode int        `grove:"last_status_code" bson:"last_status_code"`
	ReplayedAt     *time.Time `grove:"replayed_at"      bson:"replayed_at,omitempty"`
	FailedAt       time.Time  `grove:"failed_at"        bson:"failed_at"`
	CreatedAt      time.Time  `grove:"created_at"       bson:"created_at"`
	UpdatedAt      time.Time  `grove:"updated_at"       bson:"updated_at"`
}

func toDLQEntryModel(e *dlq.Entry) *dlqEntryModel {
	return &dlqEntryModel{
		ID:             e.ID.String(),
		DeliveryID:     e.DeliveryID.String(),
		EventID:        e.EventID.String(),
		EndpointID:     e.EndpointID.String(),
		TenantID:       e.TenantID,
		EventType:      e.EventType,
		URL:            e.URL,
		Payload:        e.Payload,
		Error:          e.Error,
		AttemptCount:   e.AttemptCount,
		LastStatusCode: e.LastStatusCode,
		ReplayedAt:     e.ReplayedAt,
		FailedAt:       e.FailedAt,
		CreatedAt:      e.CreatedAt,
		UpdatedAt:      e.UpdatedAt,
	}
}

func fromDLQEntryModel(m *dlqEntryModel) (*dlq.Entry, error) {
	dlqID, err := id.ParseDLQID(m.ID)
	if err != nil {
		return nil, fmt.Errorf("parse DLQ ID %q: %w", m.ID, err)
	}

	delID, err := id.ParseDeliveryID(m.DeliveryID)
	if err != nil {
		return nil, fmt.Errorf("parse delivery ID %q: %w", m.DeliveryID, err)
	}

	evtID, err := id.ParseEventID(m.EventID)
	if err != nil {
		return nil, fmt.Errorf("parse event ID %q: %w", m.EventID, err)
	}

	epID, err := id.ParseEndpointID(m.EndpointID)
	if err != nil {
		return nil, fmt.Errorf("parse endpoint ID %q: %w", m.EndpointID, err)
	}

	return &dlq.Entry{
		Entity: entity.Entity{
			CreatedAt: m.CreatedAt,
			UpdatedAt: m.UpdatedAt,
		},
		ID:             dlqID,
		DeliveryID:     delID,
		EventID:        evtID,
		EndpointID:     epID,
		TenantID:       m.TenantID,
		EventType:      m.EventType,
		URL:            m.URL,
		Payload:        m.Payload,
		Error:          m.Error,
		AttemptCount:   m.AttemptCount,
		LastStatusCode: m.LastStatusCode,
		ReplayedAt:     m.ReplayedAt,
		FailedAt:       m.FailedAt,
	}, nil
}

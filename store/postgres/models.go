package postgres

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

	ID            string            `grove:"id,pk"`
	Name          string            `grove:"name,unique"`
	Description   string            `grove:"description"`
	GroupName     string            `grove:"group_name"`
	Schema        json.RawMessage   `grove:"schema,type:jsonb"`
	SchemaVersion string            `grove:"schema_version"`
	Version       string            `grove:"version"`
	Example       json.RawMessage   `grove:"example,type:jsonb"`
	IsDeprecated  bool              `grove:"is_deprecated"`
	DeprecatedAt  *time.Time        `grove:"deprecated_at"`
	ScopeAppID    string            `grove:"scope_app_id"`
	Metadata      map[string]string `grove:"metadata,type:jsonb"`
	CreatedAt     time.Time         `grove:"created_at"`
	UpdatedAt     time.Time         `grove:"updated_at"`
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

	ID          string            `grove:"id,pk"`
	TenantID    string            `grove:"tenant_id"`
	URL         string            `grove:"url"`
	Description string            `grove:"description"`
	Secret      string            `grove:"secret"`
	EventTypes  []string          `grove:"event_types,array"`
	Headers     map[string]string `grove:"headers,type:jsonb"`
	Enabled     bool              `grove:"enabled"`
	RateLimit   int               `grove:"rate_limit"`
	Metadata    map[string]string `grove:"metadata,type:jsonb"`
	CreatedAt   time.Time         `grove:"created_at"`
	UpdatedAt   time.Time         `grove:"updated_at"`
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

	ID             string          `grove:"id,pk"`
	Type           string          `grove:"type"`
	TenantID       string          `grove:"tenant_id"`
	Data           json.RawMessage `grove:"data,type:jsonb"`
	IdempotencyKey string          `grove:"idempotency_key"`
	ScopeAppID     string          `grove:"scope_app_id"`
	ScopeOrgID     string          `grove:"scope_org_id"`
	CreatedAt      time.Time       `grove:"created_at"`
	UpdatedAt      time.Time       `grove:"updated_at"`
}

func toEventModel(evt *event.Event) *eventModel {
	data, _ := json.Marshal(evt.Data) //nolint:errcheck // best-effort serialization
	return &eventModel{
		ID:             evt.ID.String(),
		Type:           evt.Type,
		TenantID:       evt.TenantID,
		Data:           data,
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
	var data any = m.Data
	return &event.Event{
		Entity: entity.Entity{
			CreatedAt: m.CreatedAt,
			UpdatedAt: m.UpdatedAt,
		},
		ID:             evtID,
		Type:           m.Type,
		TenantID:       m.TenantID,
		Data:           data,
		IdempotencyKey: m.IdempotencyKey,
		ScopeAppID:     m.ScopeAppID,
		ScopeOrgID:     m.ScopeOrgID,
	}, nil
}

// --- Delivery models ---

type deliveryModel struct {
	grove.BaseModel `grove:"table:relay_deliveries"`

	ID             string     `grove:"id,pk"`
	EventID        string     `grove:"event_id"`
	EndpointID     string     `grove:"endpoint_id"`
	State          string     `grove:"state"`
	AttemptCount   int        `grove:"attempt_count"`
	MaxAttempts    int        `grove:"max_attempts"`
	NextAttemptAt  time.Time  `grove:"next_attempt_at"`
	LastError      string     `grove:"last_error"`
	LastStatusCode int        `grove:"last_status_code"`
	LastResponse   string     `grove:"last_response"`
	LastLatencyMs  int        `grove:"last_latency_ms"`
	CompletedAt    *time.Time `grove:"completed_at"`
	CreatedAt      time.Time  `grove:"created_at"`
	UpdatedAt      time.Time  `grove:"updated_at"`
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

	ID             string     `grove:"id,pk"`
	DeliveryID     string     `grove:"delivery_id"`
	EventID        string     `grove:"event_id"`
	EndpointID     string     `grove:"endpoint_id"`
	TenantID       string     `grove:"tenant_id"`
	EventType      string     `grove:"event_type"`
	URL            string     `grove:"url"`
	Payload        []byte     `grove:"payload,type:jsonb"`
	Error          string     `grove:"error"`
	AttemptCount   int        `grove:"attempt_count"`
	LastStatusCode int        `grove:"last_status_code"`
	ReplayedAt     *time.Time `grove:"replayed_at"`
	FailedAt       time.Time  `grove:"failed_at"`
	CreatedAt      time.Time  `grove:"created_at"`
	UpdatedAt      time.Time  `grove:"updated_at"`
}

func toDLQEntryModel(e *dlq.Entry) *dlqEntryModel {
	payload, _ := json.Marshal(e.Payload) //nolint:errcheck // best-effort serialization
	return &dlqEntryModel{
		ID:             e.ID.String(),
		DeliveryID:     e.DeliveryID.String(),
		EventID:        e.EventID.String(),
		EndpointID:     e.EndpointID.String(),
		TenantID:       e.TenantID,
		EventType:      e.EventType,
		URL:            e.URL,
		Payload:        payload,
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
	var payload any = json.RawMessage(m.Payload)
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
		Payload:        payload,
		Error:          m.Error,
		AttemptCount:   m.AttemptCount,
		LastStatusCode: m.LastStatusCode,
		ReplayedAt:     m.ReplayedAt,
		FailedAt:       m.FailedAt,
	}, nil
}

package contract

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"github.com/xraph/forge/extensions/dashboard/contract"

	"github.com/xraph/relay"
	"github.com/xraph/relay/catalog"
	"github.com/xraph/relay/endpoint"
	"github.com/xraph/relay/event"
	"github.com/xraph/relay/id"
)

// ─── Events ────────────────────────────────────────────────────────────────

// EventSummary is one row of the event log.
type EventSummary struct {
	ID             string `json:"id"`
	Type           string `json:"type"`
	TenantID       string `json:"tenantId"`
	IdempotencyKey string `json:"idempotencyKey,omitempty"`
	CreatedAt      string `json:"createdAt"`
}

// EventDetail is an event with its payload and the deliveries it fanned out.
type EventDetail struct {
	EventSummary
	Data       json.RawMessage   `json:"data"`
	ScopeAppID string            `json:"scopeAppId,omitempty"`
	ScopeOrgID string            `json:"scopeOrgId,omitempty"`
	Deliveries []DeliverySummary `json:"deliveries"`
}

// ListEventsInput is the event log's filter bar.
type ListEventsInput struct {
	Cursor   string     `json:"cursor,omitempty"`
	Limit    int        `json:"limit,omitempty"`
	Type     string     `json:"type,omitempty"`
	TenantID string     `json:"tenantId,omitempty"`
	From     *time.Time `json:"from,omitempty"`
	To       *time.Time `json:"to,omitempty"`
}

// ListEventsResponse is one page of the event log.
type ListEventsResponse struct {
	Events     []EventSummary `json:"events"`
	NextCursor string         `json:"nextCursor,omitempty"`
	Complete   bool           `json:"complete"`
}

// SendEventInput publishes an event through relay.Send, so it is validated
// against its type's schema exactly as an application's own send would be.
type SendEventInput struct {
	Type           string          `json:"type"`
	TenantID       string          `json:"tenantId"`
	Data           json.RawMessage `json:"data"`
	IdempotencyKey string          `json:"idempotencyKey,omitempty"`
}

// SendEventResponse says what happened. Duplicate is true when the key had
// been used before: relay.Send treats that as done and sends nothing.
type SendEventResponse struct {
	OK        bool   `json:"ok"`
	ID        string `json:"id,omitempty"`
	Duplicate bool   `json:"duplicate,omitempty"`
}

func projectEvent(e *event.Event) EventSummary {
	return EventSummary{
		ID:             e.ID.String(),
		Type:           e.Type,
		TenantID:       e.TenantID,
		IdempotencyKey: e.IdempotencyKey,
		CreatedAt:      rfc3339(e.CreatedAt),
	}
}

func eventsListHandler(deps Deps) func(context.Context, ListEventsInput, contract.Principal) (ListEventsResponse, error) {
	return func(ctx context.Context, in ListEventsInput, _ contract.Principal) (ListEventsResponse, error) {
		page, err := deps.Relay.Store().ListEventsPage(ctx, event.Query{
			Cursor: in.Cursor, Limit: in.Limit, Type: in.Type, TenantID: in.TenantID, From: in.From, To: in.To,
		})
		if err != nil {
			return ListEventsResponse{}, mapRelayError(err)
		}
		out := ListEventsResponse{Events: make([]EventSummary, 0, len(page.Events)),
			NextCursor: page.NextCursor, Complete: page.Complete}
		for _, e := range page.Events {
			out.Events = append(out.Events, projectEvent(e))
		}
		return out, nil
	}
}

func eventsDetailHandler(deps Deps) func(context.Context, GetByIDInput, contract.Principal) (EventDetail, error) {
	return func(ctx context.Context, in GetByIDInput, _ contract.Principal) (EventDetail, error) {
		evtID, err := parseID(in.ID, id.ParseEventID, "event")
		if err != nil {
			return EventDetail{}, err
		}
		e, err := deps.Relay.Store().GetEvent(ctx, evtID)
		if err != nil {
			return EventDetail{}, mapRelayError(err)
		}
		data, err := json.Marshal(e.Data)
		if err != nil {
			return EventDetail{}, mapRelayError(err)
		}
		dels, err := deps.Relay.Store().ListByEvent(ctx, evtID)
		if err != nil {
			return EventDetail{}, mapRelayError(err)
		}
		urls := &endpointURLs{deps: deps, cache: map[id.ID]*endpoint.Endpoint{}}
		out := EventDetail{EventSummary: projectEvent(e), Data: data,
			ScopeAppID: e.ScopeAppID, ScopeOrgID: e.ScopeOrgID,
			Deliveries: make([]DeliverySummary, 0, len(dels))}
		for _, d := range dels {
			out.Deliveries = append(out.Deliveries, projectDelivery(d, urls.get(ctx, d.EndpointID)))
		}
		return out, nil
	}
}

func eventsSendHandler(deps Deps) func(context.Context, SendEventInput, contract.Principal) (SendEventResponse, error) {
	return func(ctx context.Context, in SendEventInput, _ contract.Principal) (SendEventResponse, error) {
		if strings.TrimSpace(in.Type) == "" {
			return SendEventResponse{}, fieldError("type", "Event type: required")
		}
		if strings.TrimSpace(in.TenantID) == "" {
			return SendEventResponse{}, fieldError("tenant_id", "Tenant ID: required")
		}
		var data any
		if len(in.Data) > 0 {
			if err := json.Unmarshal(in.Data, &data); err != nil {
				return SendEventResponse{}, fieldError("data", "Data: not valid JSON")
			}
		}
		evt := &event.Event{Type: in.Type, TenantID: in.TenantID, Data: data, IdempotencyKey: in.IdempotencyKey}
		if err := deps.Relay.Send(ctx, evt); err != nil {
			if errors.Is(err, relay.ErrEventTypeNotFound) {
				return SendEventResponse{}, fieldError("type", "Event type: not registered")
			}
			return SendEventResponse{}, mapRelayError(err)
		}
		// Send reports a reused idempotency key as success without storing
		// anything, so the id it assigned names no event. Say so rather than
		// hand the page a link to nothing.
		if _, err := deps.Relay.Store().GetEvent(ctx, evt.ID); errors.Is(err, relay.ErrEventNotFound) {
			return SendEventResponse{OK: true, Duplicate: true}, nil
		}
		return SendEventResponse{OK: true, ID: evt.ID.String()}, nil
	}
}

func fieldError(field, message string) error {
	return &contract.Error{Code: contract.CodeBadRequest, Message: message, Details: map[string]any{"field": field}}
}

// ─── Event types ───────────────────────────────────────────────────────────

// EventTypeSummary is one row of the catalog.
type EventTypeSummary struct {
	ID            string  `json:"id"`
	Name          string  `json:"name"`
	Description   string  `json:"description"`
	Group         string  `json:"group,omitempty"`
	Version       string  `json:"version"`
	SchemaVersion string  `json:"schemaVersion,omitempty"`
	HasSchema     bool    `json:"hasSchema"`
	Deprecated    bool    `json:"deprecated"`
	DeprecatedAt  *string `json:"deprecatedAt,omitempty"`
	CreatedAt     string  `json:"createdAt"`
	UpdatedAt     string  `json:"updatedAt"`
}

// EventTypeDetail adds the schema, the example and the metadata.
type EventTypeDetail struct {
	EventTypeSummary
	Schema   json.RawMessage   `json:"schema,omitempty"`
	Example  json.RawMessage   `json:"example,omitempty"`
	Metadata map[string]string `json:"metadata,omitempty"`
}

// ListEventTypesInput filters the catalog. It is small and unpaged.
type ListEventTypesInput struct {
	Group             string `json:"group,omitempty"`
	IncludeDeprecated bool   `json:"includeDeprecated,omitempty"`
}

// ListEventTypesResponse is the whole catalog, or one group of it.
type ListEventTypesResponse struct {
	Types []EventTypeSummary `json:"types"`
}

// GetByNameInput names one event type.
type GetByNameInput struct {
	Name string `json:"name"`
}

// MatchEventTypesInput is a glob, as an endpoint's event types are written.
type MatchEventTypesInput struct {
	Pattern string `json:"pattern"`
}

// RegisterEventTypeInput is catalog.WebhookDefinition. Registering an
// existing name updates it: catalog.RegisterType is an upsert.
type RegisterEventTypeInput struct {
	Name          string          `json:"name"`
	Description   string          `json:"description"`
	Group         string          `json:"group,omitempty"`
	Schema        json.RawMessage `json:"schema,omitempty"`
	SchemaVersion string          `json:"schemaVersion,omitempty"`
	Version       string          `json:"version,omitempty"`
	Example       json.RawMessage `json:"example,omitempty"`
}

func projectEventType(et *catalog.EventType) EventTypeSummary {
	return EventTypeSummary{
		ID:            et.ID.String(),
		Name:          et.Definition.Name,
		Description:   et.Definition.Description,
		Group:         et.Definition.Group,
		Version:       et.Definition.Version,
		SchemaVersion: et.Definition.SchemaVersion,
		HasSchema:     len(et.Definition.Schema) > 0,
		Deprecated:    et.IsDeprecated,
		DeprecatedAt:  rfc3339Ptr(et.DeprecatedAt),
		CreatedAt:     rfc3339(et.CreatedAt),
		UpdatedAt:     rfc3339(et.UpdatedAt),
	}
}

func eventTypesListHandler(deps Deps) func(context.Context, ListEventTypesInput, contract.Principal) (ListEventTypesResponse, error) {
	return func(ctx context.Context, in ListEventTypesInput, _ contract.Principal) (ListEventTypesResponse, error) {
		types, err := deps.Relay.Store().ListTypes(ctx, catalog.ListOpts{Group: in.Group, IncludeDeprecated: in.IncludeDeprecated})
		if err != nil {
			return ListEventTypesResponse{}, mapRelayError(err)
		}
		out := ListEventTypesResponse{Types: make([]EventTypeSummary, 0, len(types))}
		for _, et := range types {
			out.Types = append(out.Types, projectEventType(et))
		}
		return out, nil
	}
}

func eventTypesDetailHandler(deps Deps) func(context.Context, GetByNameInput, contract.Principal) (EventTypeDetail, error) {
	return func(ctx context.Context, in GetByNameInput, _ contract.Principal) (EventTypeDetail, error) {
		if strings.TrimSpace(in.Name) == "" {
			return EventTypeDetail{}, &contract.Error{Code: contract.CodeBadRequest, Message: "event type name is required"}
		}
		et, err := deps.Relay.Store().GetType(ctx, in.Name)
		if err != nil {
			return EventTypeDetail{}, mapRelayError(err)
		}
		return EventTypeDetail{EventTypeSummary: projectEventType(et),
			Schema: et.Definition.Schema, Example: et.Definition.Example, Metadata: et.Metadata}, nil
	}
}

func eventTypesMatchHandler(deps Deps) func(context.Context, MatchEventTypesInput, contract.Principal) (ListEventTypesResponse, error) {
	return func(ctx context.Context, in MatchEventTypesInput, _ contract.Principal) (ListEventTypesResponse, error) {
		if strings.TrimSpace(in.Pattern) == "" {
			return ListEventTypesResponse{Types: []EventTypeSummary{}}, nil
		}
		types, err := deps.Relay.Store().MatchTypes(ctx, in.Pattern)
		if err != nil {
			return ListEventTypesResponse{}, mapRelayError(err)
		}
		out := ListEventTypesResponse{Types: make([]EventTypeSummary, 0, len(types))}
		for _, et := range types {
			out.Types = append(out.Types, projectEventType(et))
		}
		return out, nil
	}
}

func eventTypesRegisterHandler(deps Deps) func(context.Context, RegisterEventTypeInput, contract.Principal) (AckResponse, error) {
	return func(ctx context.Context, in RegisterEventTypeInput, _ contract.Principal) (AckResponse, error) {
		if strings.TrimSpace(in.Name) == "" {
			return AckResponse{}, fieldError("name", "Name: required")
		}
		// A JSON Schema is an object. The envelope guarantees the field is
		// JSON of some kind; a string or a number here is a mistake that
		// would otherwise surface only when the first event fails to send.
		if len(in.Schema) > 0 && !isJSONObject(in.Schema) {
			return AckResponse{}, fieldError("schema", "Schema: must be a JSON object")
		}
		et, err := deps.Relay.RegisterEventType(ctx, catalog.WebhookDefinition{
			Name: in.Name, Description: in.Description, Group: in.Group, Schema: in.Schema,
			SchemaVersion: in.SchemaVersion, Version: in.Version, Example: in.Example,
		})
		if err != nil {
			return AckResponse{}, mapRelayError(err)
		}
		return AckResponse{OK: true, ID: et.ID.String()}, nil
	}
}

func eventTypesDeprecateHandler(deps Deps) func(context.Context, GetByNameInput, contract.Principal) (AckResponse, error) {
	return func(ctx context.Context, in GetByNameInput, _ contract.Principal) (AckResponse, error) {
		if strings.TrimSpace(in.Name) == "" {
			return AckResponse{}, &contract.Error{Code: contract.CodeBadRequest, Message: "event type name is required"}
		}
		// DeleteType is a soft delete: the row stays, marked deprecated, and
		// Send refuses new events of the type.
		if err := deps.Relay.Catalog().DeleteType(ctx, in.Name); err != nil {
			return AckResponse{}, mapRelayError(err)
		}
		return AckResponse{OK: true}, nil
	}
}

func isJSONObject(raw json.RawMessage) bool {
	var m map[string]json.RawMessage
	return json.Unmarshal(raw, &m) == nil && m != nil
}

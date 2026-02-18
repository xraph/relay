package api

import (
	"encoding/json"
	"net/http"
	"time"

	"github.com/xraph/forge"

	"github.com/xraph/relay"
	"github.com/xraph/relay/catalog"
	"github.com/xraph/relay/delivery"
	"github.com/xraph/relay/dlq"
	"github.com/xraph/relay/endpoint"
	"github.com/xraph/relay/event"
	"github.com/xraph/relay/id"
	"github.com/xraph/relay/store"
)

// ForgeAPI wires all Forge-style HTTP handlers together.
type ForgeAPI struct {
	store       store.Store
	catalog     *catalog.Catalog
	endpointSvc *endpoint.Service
	dlqSvc      *dlq.Service
	relay       *relay.Relay
}

// NewForgeAPI creates a ForgeAPI from Relay services.
func NewForgeAPI(
	s store.Store,
	cat *catalog.Catalog,
	epSvc *endpoint.Service,
	dlqSvc *dlq.Service,
	r *relay.Relay,
) *ForgeAPI {
	return &ForgeAPI{
		store:       s,
		catalog:     cat,
		endpointSvc: epSvc,
		dlqSvc:      dlqSvc,
		relay:       r,
	}
}

// RegisterRoutes registers all Relay admin API routes into the given Forge router
// with full OpenAPI metadata.
func (a *ForgeAPI) RegisterRoutes(router forge.Router) {
	a.registerEventTypeRoutes(router)
	a.registerEndpointRoutes(router)
	a.registerEventRoutes(router)
	a.registerDeliveryRoutes(router)
	a.registerDLQRoutes(router)
	a.registerStatsRoutes(router)
}

// ---------------------------------------------------------------------------
// Event type routes
// ---------------------------------------------------------------------------

func (a *ForgeAPI) registerEventTypeRoutes(router forge.Router) {
	g := router.Group("", forge.WithGroupTags("event-types"))

	_ = g.POST("/event-types", a.createEventType,
		forge.WithSummary("Register event type"),
		forge.WithDescription("Registers a new webhook event type in the catalog."),
		forge.WithOperationID("createEventType"),
		forge.WithRequestSchema(CreateEventTypeForgeRequest{}),
		forge.WithCreatedResponse(catalog.EventType{}),
		forge.WithErrorResponses(),
	)

	_ = g.GET("/event-types", a.listEventTypes,
		forge.WithSummary("List event types"),
		forge.WithDescription("Returns a paginated list of registered event types."),
		forge.WithOperationID("listEventTypes"),
		forge.WithRequestSchema(ListEventTypesForgeRequest{}),
		forge.WithListResponse(catalog.EventType{}, http.StatusOK),
		forge.WithErrorResponses(),
	)

	_ = g.GET("/event-types/:name", a.getEventType,
		forge.WithSummary("Get event type"),
		forge.WithDescription("Returns details of a specific event type."),
		forge.WithOperationID("getEventType"),
		forge.WithResponseSchema(http.StatusOK, "Event type details", catalog.EventType{}),
		forge.WithErrorResponses(),
	)

	_ = g.DELETE("/event-types/:name", a.deleteEventType,
		forge.WithSummary("Deprecate event type"),
		forge.WithDescription("Soft-deletes an event type. Sending events of this type will fail."),
		forge.WithOperationID("deleteEventType"),
		forge.WithNoContentResponse(),
		forge.WithErrorResponses(),
	)
}

func (a *ForgeAPI) createEventType(ctx forge.Context, req *CreateEventTypeForgeRequest) (*catalog.EventType, error) {
	if req.Name == "" {
		return nil, forge.BadRequest("name is required")
	}

	def := catalog.WebhookDefinition{
		Name:          req.Name,
		Description:   req.Description,
		Group:         req.Group,
		Schema:        req.Schema,
		SchemaVersion: req.SchemaVersion,
		Version:       req.Version,
	}

	var opts []catalog.RegisterOption
	if req.ScopeAppID != "" {
		opts = append(opts, catalog.WithScopeAppID(req.ScopeAppID))
	}
	if req.Metadata != nil {
		opts = append(opts, catalog.WithMetadata(req.Metadata))
	}

	et, err := a.catalog.RegisterType(ctx.Context(), def, opts...)
	if err != nil {
		return nil, mapError(err)
	}

	_ = ctx.JSON(http.StatusCreated, et)

	//nolint:nilnil // response already written via ctx.JSON.
	return nil, nil
}

func (a *ForgeAPI) listEventTypes(ctx forge.Context, req *ListEventTypesForgeRequest) ([]*catalog.EventType, error) {
	limit := req.Limit
	if limit == 0 {
		limit = 50
	}

	opts := catalog.ListOpts{
		Offset:            req.Offset,
		Limit:             limit,
		Group:             req.Group,
		IncludeDeprecated: req.IncludeDeprecated == "true",
	}

	types, err := a.catalog.ListTypes(ctx.Context(), opts)
	if err != nil {
		return nil, mapError(err)
	}

	return types, nil
}

func (a *ForgeAPI) getEventType(ctx forge.Context, req *GetEventTypeForgeRequest) (*catalog.EventType, error) {
	et, err := a.catalog.GetType(ctx.Context(), req.Name)
	if err != nil {
		return nil, mapError(err)
	}

	return et, nil
}

func (a *ForgeAPI) deleteEventType(ctx forge.Context, req *DeleteEventTypeForgeRequest) (*catalog.EventType, error) {
	if err := a.catalog.DeleteType(ctx.Context(), req.Name); err != nil {
		return nil, mapError(err)
	}

	_ = ctx.NoContent(http.StatusNoContent)

	//nolint:nilnil // response already written via ctx.NoContent.
	return nil, nil
}

// ---------------------------------------------------------------------------
// Endpoint routes
// ---------------------------------------------------------------------------

func (a *ForgeAPI) registerEndpointRoutes(router forge.Router) {
	g := router.Group("", forge.WithGroupTags("endpoints"))

	_ = g.POST("/endpoints", a.createEndpoint,
		forge.WithSummary("Create endpoint"),
		forge.WithDescription("Creates a new webhook endpoint for a tenant."),
		forge.WithOperationID("createEndpoint"),
		forge.WithRequestSchema(CreateEndpointForgeRequest{}),
		forge.WithCreatedResponse(endpoint.Endpoint{}),
		forge.WithErrorResponses(),
	)

	_ = g.GET("/endpoints", a.listEndpoints,
		forge.WithSummary("List endpoints"),
		forge.WithDescription("Returns a paginated list of endpoints for a tenant."),
		forge.WithOperationID("listEndpoints"),
		forge.WithRequestSchema(ListEndpointsForgeRequest{}),
		forge.WithListResponse(endpoint.Endpoint{}, http.StatusOK),
		forge.WithErrorResponses(),
	)

	_ = g.GET("/endpoints/:endpointId", a.getEndpoint,
		forge.WithSummary("Get endpoint"),
		forge.WithDescription("Returns details of a specific endpoint."),
		forge.WithOperationID("getEndpoint"),
		forge.WithResponseSchema(http.StatusOK, "Endpoint details", endpoint.Endpoint{}),
		forge.WithErrorResponses(),
	)

	_ = g.PUT("/endpoints/:endpointId", a.updateEndpoint,
		forge.WithSummary("Update endpoint"),
		forge.WithDescription("Updates mutable fields of an endpoint."),
		forge.WithOperationID("updateEndpoint"),
		forge.WithRequestSchema(UpdateEndpointForgeRequest{}),
		forge.WithResponseSchema(http.StatusOK, "Updated endpoint", endpoint.Endpoint{}),
		forge.WithErrorResponses(),
	)

	_ = g.DELETE("/endpoints/:endpointId", a.deleteEndpoint,
		forge.WithSummary("Delete endpoint"),
		forge.WithDescription("Permanently deletes an endpoint."),
		forge.WithOperationID("deleteEndpoint"),
		forge.WithNoContentResponse(),
		forge.WithErrorResponses(),
	)

	_ = g.PATCH("/endpoints/:endpointId/enable", a.enableEndpoint,
		forge.WithSummary("Enable endpoint"),
		forge.WithDescription("Re-enables a disabled endpoint."),
		forge.WithOperationID("enableEndpoint"),
		forge.WithNoContentResponse(),
		forge.WithErrorResponses(),
	)

	_ = g.PATCH("/endpoints/:endpointId/disable", a.disableEndpoint,
		forge.WithSummary("Disable endpoint"),
		forge.WithDescription("Disables an endpoint, pausing all deliveries."),
		forge.WithOperationID("disableEndpoint"),
		forge.WithNoContentResponse(),
		forge.WithErrorResponses(),
	)

	_ = g.POST("/endpoints/:endpointId/rotate-secret", a.rotateSecret,
		forge.WithSummary("Rotate secret"),
		forge.WithDescription("Generates a new signing secret for the endpoint."),
		forge.WithOperationID("rotateEndpointSecret"),
		forge.WithResponseSchema(http.StatusOK, "New signing secret", SecretForgeResponse{}),
		forge.WithErrorResponses(),
	)
}

func (a *ForgeAPI) createEndpoint(ctx forge.Context, req *CreateEndpointForgeRequest) (*endpoint.Endpoint, error) {
	input := endpoint.Input{
		TenantID:   req.TenantID,
		URL:        req.URL,
		EventTypes: req.EventTypes,
		Headers:    req.Headers,
		RateLimit:  req.RateLimit,
		Metadata:   req.Metadata,
	}

	ep, err := a.endpointSvc.Create(ctx.Context(), input)
	if err != nil {
		return nil, mapError(err)
	}

	_ = ctx.JSON(http.StatusCreated, ep)

	//nolint:nilnil // response already written via ctx.JSON.
	return nil, nil
}

func (a *ForgeAPI) listEndpoints(ctx forge.Context, req *ListEndpointsForgeRequest) ([]*endpoint.Endpoint, error) {
	if req.TenantID == "" {
		return nil, forge.BadRequest("tenant_id query parameter is required")
	}

	limit := req.Limit
	if limit == 0 {
		limit = 50
	}

	opts := endpoint.ListOpts{
		Offset: req.Offset,
		Limit:  limit,
	}

	eps, err := a.endpointSvc.List(ctx.Context(), req.TenantID, opts)
	if err != nil {
		return nil, mapError(err)
	}

	return eps, nil
}

func (a *ForgeAPI) getEndpoint(ctx forge.Context, req *GetEndpointForgeRequest) (*endpoint.Endpoint, error) {
	epID, err := id.ParseEndpointID(req.EndpointID)
	if err != nil {
		return nil, forge.BadRequest("invalid endpoint ID")
	}

	ep, getErr := a.endpointSvc.Get(ctx.Context(), epID)
	if getErr != nil {
		return nil, mapError(getErr)
	}

	return ep, nil
}

func (a *ForgeAPI) updateEndpoint(ctx forge.Context, req *UpdateEndpointForgeRequest) (*endpoint.Endpoint, error) {
	epID, err := id.ParseEndpointID(req.EndpointID)
	if err != nil {
		return nil, forge.BadRequest("invalid endpoint ID")
	}

	input := endpoint.Input{
		URL:        req.URL,
		EventTypes: req.EventTypes,
		Headers:    req.Headers,
		RateLimit:  req.RateLimit,
		Metadata:   req.Metadata,
	}

	ep, updateErr := a.endpointSvc.Update(ctx.Context(), epID, input)
	if updateErr != nil {
		return nil, mapError(updateErr)
	}

	return ep, nil
}

func (a *ForgeAPI) deleteEndpoint(ctx forge.Context, req *DeleteEndpointForgeRequest) (*endpoint.Endpoint, error) {
	epID, err := id.ParseEndpointID(req.EndpointID)
	if err != nil {
		return nil, forge.BadRequest("invalid endpoint ID")
	}

	if deleteErr := a.endpointSvc.Delete(ctx.Context(), epID); deleteErr != nil {
		return nil, mapError(deleteErr)
	}

	_ = ctx.NoContent(http.StatusNoContent)

	//nolint:nilnil // response already written via ctx.NoContent.
	return nil, nil
}

func (a *ForgeAPI) enableEndpoint(ctx forge.Context, req *EndpointActionForgeRequest) (*endpoint.Endpoint, error) {
	epID, err := id.ParseEndpointID(req.EndpointID)
	if err != nil {
		return nil, forge.BadRequest("invalid endpoint ID")
	}

	if setErr := a.endpointSvc.SetEnabled(ctx.Context(), epID, true); setErr != nil {
		return nil, mapError(setErr)
	}

	_ = ctx.NoContent(http.StatusNoContent)

	//nolint:nilnil // response already written via ctx.NoContent.
	return nil, nil
}

func (a *ForgeAPI) disableEndpoint(ctx forge.Context, req *EndpointActionForgeRequest) (*endpoint.Endpoint, error) {
	epID, err := id.ParseEndpointID(req.EndpointID)
	if err != nil {
		return nil, forge.BadRequest("invalid endpoint ID")
	}

	if setErr := a.endpointSvc.SetEnabled(ctx.Context(), epID, false); setErr != nil {
		return nil, mapError(setErr)
	}

	_ = ctx.NoContent(http.StatusNoContent)

	//nolint:nilnil // response already written via ctx.NoContent.
	return nil, nil
}

func (a *ForgeAPI) rotateSecret(ctx forge.Context, req *EndpointActionForgeRequest) (*SecretForgeResponse, error) {
	epID, err := id.ParseEndpointID(req.EndpointID)
	if err != nil {
		return nil, forge.BadRequest("invalid endpoint ID")
	}

	newSecret, rotateErr := a.endpointSvc.RotateSecret(ctx.Context(), epID)
	if rotateErr != nil {
		return nil, mapError(rotateErr)
	}

	return &SecretForgeResponse{Secret: newSecret}, nil
}

// ---------------------------------------------------------------------------
// Event routes
// ---------------------------------------------------------------------------

func (a *ForgeAPI) registerEventRoutes(router forge.Router) {
	g := router.Group("", forge.WithGroupTags("events"))

	_ = g.POST("/events", a.sendEvent,
		forge.WithSummary("Send event"),
		forge.WithDescription("Validates an event, persists it, and fans out deliveries to matching endpoints."),
		forge.WithOperationID("sendEvent"),
		forge.WithRequestSchema(CreateEventForgeRequest{}),
		forge.WithCreatedResponse(event.Event{}),
		forge.WithErrorResponses(),
	)

	_ = g.GET("/events", a.listEvents,
		forge.WithSummary("List events"),
		forge.WithDescription("Returns a paginated list of events."),
		forge.WithOperationID("listEvents"),
		forge.WithRequestSchema(ListEventsForgeRequest{}),
		forge.WithListResponse(event.Event{}, http.StatusOK),
		forge.WithErrorResponses(),
	)

	_ = g.GET("/events/:eventId", a.getEvent,
		forge.WithSummary("Get event"),
		forge.WithDescription("Returns details of a specific event."),
		forge.WithOperationID("getEvent"),
		forge.WithResponseSchema(http.StatusOK, "Event details", event.Event{}),
		forge.WithErrorResponses(),
	)
}

func (a *ForgeAPI) sendEvent(ctx forge.Context, req *CreateEventForgeRequest) (*event.Event, error) {
	if req.Type == "" {
		return nil, forge.BadRequest("type is required")
	}
	if req.TenantID == "" {
		return nil, forge.BadRequest("tenant_id is required")
	}

	evt := &event.Event{
		Type:           req.Type,
		TenantID:       req.TenantID,
		Data:           json.RawMessage(req.Data),
		IdempotencyKey: req.IdempotencyKey,
	}

	if err := a.relay.Send(ctx.Context(), evt); err != nil {
		return nil, mapError(err)
	}

	_ = ctx.JSON(http.StatusCreated, evt)

	//nolint:nilnil // response already written via ctx.JSON.
	return nil, nil
}

func (a *ForgeAPI) listEvents(ctx forge.Context, req *ListEventsForgeRequest) ([]*event.Event, error) {
	limit := req.Limit
	if limit == 0 {
		limit = 50
	}

	opts := event.ListOpts{
		Offset: req.Offset,
		Limit:  limit,
		Type:   req.Type,
	}

	events, err := a.store.ListEvents(ctx.Context(), opts)
	if err != nil {
		return nil, mapError(err)
	}

	return events, nil
}

func (a *ForgeAPI) getEvent(ctx forge.Context, req *GetEventForgeRequest) (*event.Event, error) {
	evtID, err := id.ParseEventID(req.EventID)
	if err != nil {
		return nil, forge.BadRequest("invalid event ID")
	}

	evt, getErr := a.store.GetEvent(ctx.Context(), evtID)
	if getErr != nil {
		return nil, mapError(getErr)
	}

	return evt, nil
}

// ---------------------------------------------------------------------------
// Delivery routes
// ---------------------------------------------------------------------------

func (a *ForgeAPI) registerDeliveryRoutes(router forge.Router) {
	g := router.Group("", forge.WithGroupTags("deliveries"))

	_ = g.GET("/endpoints/:endpointId/deliveries", a.listDeliveries,
		forge.WithSummary("List deliveries"),
		forge.WithDescription("Returns deliveries for a specific endpoint."),
		forge.WithOperationID("listDeliveries"),
		forge.WithRequestSchema(ListDeliveriesForgeRequest{}),
		forge.WithListResponse(delivery.Delivery{}, http.StatusOK),
		forge.WithErrorResponses(),
	)
}

func (a *ForgeAPI) listDeliveries(ctx forge.Context, req *ListDeliveriesForgeRequest) ([]*delivery.Delivery, error) {
	epID, err := id.ParseEndpointID(req.EndpointID)
	if err != nil {
		return nil, forge.BadRequest("invalid endpoint ID")
	}

	limit := req.Limit
	if limit == 0 {
		limit = 50
	}

	opts := delivery.ListOpts{
		Offset: req.Offset,
		Limit:  limit,
	}

	if req.State != "" {
		state := delivery.State(req.State)
		opts.State = &state
	}

	deliveries, listErr := a.store.ListByEndpoint(ctx.Context(), epID, opts)
	if listErr != nil {
		return nil, mapError(listErr)
	}

	return deliveries, nil
}

// ---------------------------------------------------------------------------
// DLQ routes
// ---------------------------------------------------------------------------

func (a *ForgeAPI) registerDLQRoutes(router forge.Router) {
	g := router.Group("", forge.WithGroupTags("dlq"))

	_ = g.GET("/dlq", a.listDLQ,
		forge.WithSummary("List DLQ entries"),
		forge.WithDescription("Returns dead letter queue entries, optionally filtered by tenant."),
		forge.WithOperationID("listDLQ"),
		forge.WithRequestSchema(ListDLQForgeRequest{}),
		forge.WithListResponse(dlq.Entry{}, http.StatusOK),
		forge.WithErrorResponses(),
	)

	_ = g.POST("/dlq/:dlqId/replay", a.replayDLQ,
		forge.WithSummary("Replay DLQ entry"),
		forge.WithDescription("Re-enqueues a single DLQ entry for delivery."),
		forge.WithOperationID("replayDLQ"),
		forge.WithNoContentResponse(),
		forge.WithErrorResponses(),
	)

	_ = g.POST("/dlq/replay", a.replayBulkDLQ,
		forge.WithSummary("Bulk replay DLQ"),
		forge.WithDescription("Re-enqueues DLQ entries within a time range."),
		forge.WithOperationID("replayBulkDLQ"),
		forge.WithRequestSchema(ReplayBulkDLQForgeRequest{}),
		forge.WithResponseSchema(http.StatusOK, "Replay result", ReplayBulkForgeResponse{}),
		forge.WithErrorResponses(),
	)
}

func (a *ForgeAPI) listDLQ(ctx forge.Context, req *ListDLQForgeRequest) ([]*dlq.Entry, error) {
	limit := req.Limit
	if limit == 0 {
		limit = 50
	}

	opts := dlq.ListOpts{
		Offset:   req.Offset,
		Limit:    limit,
		TenantID: req.TenantID,
	}

	entries, err := a.dlqSvc.List(ctx.Context(), opts)
	if err != nil {
		return nil, mapError(err)
	}

	return entries, nil
}

func (a *ForgeAPI) replayDLQ(ctx forge.Context, req *ReplayDLQForgeRequest) (*dlq.Entry, error) {
	dlqID, err := id.ParseDLQID(req.DLQID)
	if err != nil {
		return nil, forge.BadRequest("invalid DLQ ID")
	}

	if replayErr := a.dlqSvc.Replay(ctx.Context(), dlqID); replayErr != nil {
		return nil, mapError(replayErr)
	}

	_ = ctx.NoContent(http.StatusNoContent)

	//nolint:nilnil // response already written via ctx.NoContent.
	return nil, nil
}

func (a *ForgeAPI) replayBulkDLQ(ctx forge.Context, req *ReplayBulkDLQForgeRequest) (*ReplayBulkForgeResponse, error) {
	from, err := time.Parse(time.RFC3339, req.From)
	if err != nil {
		return nil, forge.BadRequest("invalid 'from' time format (use RFC3339)")
	}
	to, err := time.Parse(time.RFC3339, req.To)
	if err != nil {
		return nil, forge.BadRequest("invalid 'to' time format (use RFC3339)")
	}

	count, replayErr := a.dlqSvc.ReplayBulk(ctx.Context(), from, to)
	if replayErr != nil {
		return nil, mapError(replayErr)
	}

	return &ReplayBulkForgeResponse{Replayed: count}, nil
}

// ---------------------------------------------------------------------------
// Stats routes
// ---------------------------------------------------------------------------

func (a *ForgeAPI) registerStatsRoutes(router forge.Router) {
	g := router.Group("", forge.WithGroupTags("stats"))

	_ = g.GET("/stats", a.getStats,
		forge.WithSummary("System statistics"),
		forge.WithDescription("Returns aggregate counts of pending deliveries and DLQ entries."),
		forge.WithOperationID("getStats"),
		forge.WithResponseSchema(http.StatusOK, "System statistics", StatsForgeResponse{}),
		forge.WithErrorResponses(),
	)
}

func (a *ForgeAPI) getStats(ctx forge.Context, req *StatsForgeRequest) (*StatsForgeResponse, error) {
	pending, err := a.store.CountPending(ctx.Context())
	if err != nil {
		return nil, mapError(err)
	}

	dlqCount, err := a.store.CountDLQ(ctx.Context())
	if err != nil {
		return nil, mapError(err)
	}

	return &StatsForgeResponse{
		PendingDeliveries: pending,
		DLQSize:           dlqCount,
	}, nil
}

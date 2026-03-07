package dashboard

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/a-h/templ"

	"github.com/xraph/forge/extensions/dashboard/contributor"

	"github.com/xraph/relay"
	"github.com/xraph/relay/catalog"
	"github.com/xraph/relay/dashboard/pages"
	"github.com/xraph/relay/dashboard/widgets"
	"github.com/xraph/relay/delivery"
	"github.com/xraph/relay/dlq"
	"github.com/xraph/relay/endpoint"
	"github.com/xraph/relay/event"
	"github.com/xraph/relay/id"
)

// Ensure Contributor implements the required interface at compile time.
var _ contributor.LocalContributor = (*Contributor)(nil)

// Contributor implements the dashboard LocalContributor interface for the
// relay extension. It renders pages, widgets, and settings using templ
// components and ForgeUI.
type Contributor struct {
	manifest *contributor.Manifest
	r        *relay.Relay
	config   relay.Config
}

// New creates a new relay dashboard contributor.
func New(manifest *contributor.Manifest, r *relay.Relay, config relay.Config) *Contributor {
	return &Contributor{
		manifest: manifest,
		r:        r,
		config:   config,
	}
}

// Manifest returns the contributor manifest.
func (c *Contributor) Manifest() *contributor.Manifest { return c.manifest }

// RenderPage renders a page for the given route.
func (c *Contributor) RenderPage(ctx context.Context, route string, params contributor.Params) (templ.Component, error) {
	switch route {
	case "/", "":
		return c.renderOverview(ctx)
	case "/event-types":
		return c.renderEventTypes(ctx, params)
	case "/event-types/detail":
		return c.renderEventTypeDetail(ctx, params)
	case "/endpoints":
		return c.renderEndpoints(ctx, params)
	case "/endpoints/create":
		return c.renderEndpointCreate(ctx, params)
	case "/endpoints/detail":
		return c.renderEndpointDetail(ctx, params)
	case "/events":
		return c.renderEvents(ctx, params)
	case "/events/detail":
		return c.renderEventDetail(ctx, params)
	case "/deliveries":
		return c.renderDeliveries(ctx, params)
	case "/deliveries/detail":
		return c.renderDeliveryDetail(ctx, params)
	case "/dlq":
		return c.renderDLQ(ctx, params)
	case "/dlq/detail":
		return c.renderDLQDetail(ctx, params)
	case "/settings":
		return c.renderSettings(ctx)
	default:
		return nil, contributor.ErrPageNotFound
	}
}

// RenderWidget renders a widget by ID.
func (c *Contributor) RenderWidget(ctx context.Context, widgetID string) (templ.Component, error) {
	switch widgetID {
	case "relay-stats":
		return c.renderStatsWidget(ctx)
	case "relay-recent-deliveries":
		return c.renderRecentDeliveriesWidget(ctx)
	default:
		return nil, contributor.ErrWidgetNotFound
	}
}

// RenderSettings renders a settings panel by ID.
func (c *Contributor) RenderSettings(ctx context.Context, settingID string) (templ.Component, error) {
	switch settingID {
	case "relay-config":
		return c.renderSettingsPanel(ctx)
	default:
		return nil, contributor.ErrSettingNotFound
	}
}

// ─── Page Renderers ──────────────────────────────────────────────────────────

func (c *Contributor) renderOverview(ctx context.Context) (templ.Component, error) {
	stats := pages.OverviewStats{
		EventTypeCount: fetchEventTypeCount(ctx, c.r),
		PendingCount:   fetchPendingCount(ctx, c.r),
		DLQCount:       fetchDLQCount(ctx, c.r),
	}

	// Count endpoints
	eps, err := fetchAllEndpoints(ctx, c.r, endpoint.ListOpts{Limit: 1000})
	if err == nil {
		stats.EndpointCount = len(eps)
	}

	// Recent events
	recentEvents, _ := fetchEvents(ctx, c.r, event.ListOpts{Limit: 10})

	// Recent deliveries - we get them from the first endpoint or empty
	var recentDeliveries []*delivery.Delivery
	if len(eps) > 0 {
		for _, ep := range eps {
			dels, err := fetchDeliveriesByEndpoint(ctx, c.r, ep.ID, delivery.ListOpts{Limit: 10})
			if err == nil {
				recentDeliveries = append(recentDeliveries, dels...)
			}
			if len(recentDeliveries) >= 10 {
				break
			}
		}
		if len(recentDeliveries) > 10 {
			recentDeliveries = recentDeliveries[:10]
		}
	}

	return pages.OverviewPage(stats, recentEvents, recentDeliveries), nil
}

func (c *Contributor) renderEventTypes(ctx context.Context, params contributor.Params) (templ.Component, error) {
	opts := catalog.ListOpts{
		Limit:             100,
		IncludeDeprecated: true,
	}
	if group := params.QueryParams["group"]; group != "" {
		opts.Group = group
	}

	types, err := fetchEventTypes(ctx, c.r, opts)
	if err != nil {
		types = nil
	}

	return pages.EventTypesPage(types), nil
}

func (c *Contributor) renderEventTypeDetail(ctx context.Context, params contributor.Params) (templ.Component, error) {
	name := params.QueryParams["name"]
	if name == "" {
		name = params.PathParams["name"]
	}
	if name == "" {
		return nil, contributor.ErrPageNotFound
	}

	et, err := c.r.Catalog().GetType(ctx, name)
	if err != nil {
		return nil, fmt.Errorf("dashboard: resolve event type: %w", err)
	}

	return pages.EventTypeDetailPage(et), nil
}

func (c *Contributor) renderEndpoints(ctx context.Context, params contributor.Params) (templ.Component, error) {
	tenantID := params.QueryParams["tenant_id"]

	var eps []*endpoint.Endpoint
	var err error

	if tenantID != "" {
		eps, err = fetchEndpoints(ctx, c.r, tenantID, endpoint.ListOpts{Limit: 100})
	} else {
		eps, err = fetchAllEndpoints(ctx, c.r, endpoint.ListOpts{Limit: 100})
	}
	if err != nil {
		eps = nil
	}

	return pages.EndpointsPage(pages.EndpointsPageData{
		Endpoints: eps,
		TenantID:  tenantID,
	}), nil
}

func (c *Contributor) renderEndpointCreate(ctx context.Context, params contributor.Params) (templ.Component, error) {
	// Fetch event types for auto-suggestion dropdown.
	eventTypes, _ := fetchEventTypes(ctx, c.r, catalog.ListOpts{Limit: 200})

	// Handle form POST submission.
	if params.FormData["action"] == "create_endpoint" {
		in := endpoint.Input{
			TenantID:    strings.TrimSpace(params.FormData["tenant_id"]),
			URL:         strings.TrimSpace(params.FormData["url"]),
			Description: strings.TrimSpace(params.FormData["description"]),
		}

		// Parse event types (selectbox sends comma-separated values in a single field).
		if et := strings.TrimSpace(params.FormData["event_types"]); et != "" {
			for _, t := range strings.Split(et, ",") {
				t = strings.TrimSpace(t)
				if t != "" {
					in.EventTypes = append(in.EventTypes, t)
				}
			}
		}

		// Parse rate limit.
		if rl := params.FormData["rate_limit"]; rl != "" {
			if v, err := strconv.Atoi(rl); err == nil {
				in.RateLimit = v
			}
		}

		// Parse custom headers from indexed form fields.
		in.Headers = make(map[string]string)
		for i := 0; i < 10; i++ {
			key := strings.TrimSpace(params.FormData[fmt.Sprintf("header_key_%d", i)])
			val := strings.TrimSpace(params.FormData[fmt.Sprintf("header_value_%d", i)])
			if key != "" && val != "" {
				in.Headers[key] = val
			}
		}
		if len(in.Headers) == 0 {
			in.Headers = nil
		}

		// Parse metadata from indexed form fields.
		in.Metadata = make(map[string]string)
		for i := 0; i < 10; i++ {
			key := strings.TrimSpace(params.FormData[fmt.Sprintf("meta_key_%d", i)])
			val := strings.TrimSpace(params.FormData[fmt.Sprintf("meta_value_%d", i)])
			if key != "" && val != "" {
				in.Metadata[key] = val
			}
		}
		if len(in.Metadata) == 0 {
			in.Metadata = nil
		}

		// Create the endpoint.
		_, err := c.r.Endpoints().Create(ctx, in)
		if err != nil {
			return pages.EndpointCreatePage(pages.EndpointCreateData{
				EventTypes: eventTypes,
				Error:      err.Error(),
			}), nil
		}

		// Success: render the endpoints list page.
		return c.renderEndpoints(ctx, params)
	}

	return pages.EndpointCreatePage(pages.EndpointCreateData{
		EventTypes: eventTypes,
	}), nil
}

func (c *Contributor) renderEndpointDetail(ctx context.Context, params contributor.Params) (templ.Component, error) {
	epIDStr := params.QueryParams["id"]
	if epIDStr == "" {
		epIDStr = params.PathParams["id"]
	}
	if epIDStr == "" {
		return nil, contributor.ErrPageNotFound
	}

	epID, err := id.Parse(epIDStr)
	if err != nil {
		return nil, contributor.ErrPageNotFound
	}

	// Handle actions.
	if action := params.QueryParams["action"]; action != "" {
		switch action {
		case "enable":
			if err := c.r.Endpoints().SetEnabled(ctx, epID, true); err != nil {
				return nil, fmt.Errorf("dashboard: enable endpoint: %w", err)
			}
		case "disable":
			if err := c.r.Endpoints().SetEnabled(ctx, epID, false); err != nil {
				return nil, fmt.Errorf("dashboard: disable endpoint: %w", err)
			}
		case "rotate_secret":
			if _, err := c.r.Endpoints().RotateSecret(ctx, epID); err != nil {
				return nil, fmt.Errorf("dashboard: rotate secret: %w", err)
			}
		}
	}

	ep, err := c.r.Endpoints().Get(ctx, epID)
	if err != nil {
		return nil, fmt.Errorf("dashboard: resolve endpoint: %w", err)
	}

	deliveries, _ := fetchDeliveriesByEndpoint(ctx, c.r, epID, delivery.ListOpts{Limit: 20})

	return pages.EndpointDetailPage(pages.EndpointDetailData{
		Endpoint:   ep,
		Deliveries: deliveries,
	}), nil
}

func (c *Contributor) renderEvents(ctx context.Context, params contributor.Params) (templ.Component, error) {
	opts := event.ListOpts{Limit: 50}
	typeFilter := params.QueryParams["type"]
	if typeFilter != "" {
		opts.Type = typeFilter
	}

	events, err := fetchEvents(ctx, c.r, opts)
	if err != nil {
		events = nil
	}

	return pages.EventsPage(pages.EventsPageData{
		Events:     events,
		TypeFilter: typeFilter,
	}), nil
}

func (c *Contributor) renderEventDetail(ctx context.Context, params contributor.Params) (templ.Component, error) {
	evtIDStr := params.QueryParams["id"]
	if evtIDStr == "" {
		evtIDStr = params.PathParams["id"]
	}
	if evtIDStr == "" {
		return nil, contributor.ErrPageNotFound
	}

	evtID, err := id.Parse(evtIDStr)
	if err != nil {
		return nil, contributor.ErrPageNotFound
	}

	evt, err := c.r.Store().GetEvent(ctx, evtID)
	if err != nil {
		return nil, fmt.Errorf("dashboard: resolve event: %w", err)
	}

	deliveries, _ := fetchDeliveriesByEvent(ctx, c.r, evtID)

	return pages.EventDetailPage(pages.EventDetailData{
		Event:      evt,
		Deliveries: deliveries,
	}), nil
}

func (c *Contributor) renderDeliveries(ctx context.Context, params contributor.Params) (templ.Component, error) {
	stateFilter := params.QueryParams["state"]

	// Get deliveries from all endpoints.
	eps, _ := fetchAllEndpoints(ctx, c.r, endpoint.ListOpts{Limit: 100})

	opts := delivery.ListOpts{Limit: 50}
	if stateFilter != "" {
		state := delivery.State(stateFilter)
		opts.State = &state
	}

	var allDeliveries []*delivery.Delivery
	for _, ep := range eps {
		dels, err := fetchDeliveriesByEndpoint(ctx, c.r, ep.ID, opts)
		if err == nil {
			allDeliveries = append(allDeliveries, dels...)
		}
		if len(allDeliveries) >= 50 {
			break
		}
	}
	if len(allDeliveries) > 50 {
		allDeliveries = allDeliveries[:50]
	}

	return pages.DeliveriesPage(pages.DeliveriesPageData{
		Deliveries:  allDeliveries,
		StateFilter: stateFilter,
	}), nil
}

func (c *Contributor) renderDeliveryDetail(ctx context.Context, params contributor.Params) (templ.Component, error) {
	delIDStr := params.QueryParams["id"]
	if delIDStr == "" {
		delIDStr = params.PathParams["id"]
	}
	if delIDStr == "" {
		return nil, contributor.ErrPageNotFound
	}

	delID, err := id.Parse(delIDStr)
	if err != nil {
		return nil, contributor.ErrPageNotFound
	}

	d, err := c.r.Store().GetDelivery(ctx, delID)
	if err != nil {
		return nil, fmt.Errorf("dashboard: resolve delivery: %w", err)
	}

	return pages.DeliveryDetailPage(d), nil
}

func (c *Contributor) renderDLQ(ctx context.Context, params contributor.Params) (templ.Component, error) {
	// Handle bulk replay action.
	if params.QueryParams["action"] == "replay_all" {
		now := time.Now()
		past := now.Add(-365 * 24 * time.Hour)
		_, _ = c.r.DLQ().ReplayBulk(ctx, past, now)
	}

	entries, err := fetchDLQEntries(ctx, c.r, dlq.ListOpts{Limit: 50})
	if err != nil {
		entries = nil
	}

	return pages.DLQPage(entries), nil
}

func (c *Contributor) renderDLQDetail(ctx context.Context, params contributor.Params) (templ.Component, error) {
	dlqIDStr := params.QueryParams["id"]
	if dlqIDStr == "" {
		dlqIDStr = params.PathParams["id"]
	}
	if dlqIDStr == "" {
		return nil, contributor.ErrPageNotFound
	}

	dlqID, err := id.Parse(dlqIDStr)
	if err != nil {
		return nil, contributor.ErrPageNotFound
	}

	// Handle replay action.
	if params.QueryParams["action"] == "replay" {
		if replayErr := c.r.DLQ().Replay(ctx, dlqID); replayErr != nil {
			return nil, fmt.Errorf("dashboard: replay DLQ entry: %w", replayErr)
		}
	}

	entry, err := c.r.DLQ().Get(ctx, dlqID)
	if err != nil {
		return nil, fmt.Errorf("dashboard: resolve DLQ entry: %w", err)
	}

	return pages.DLQDetailPage(entry), nil
}

func (c *Contributor) renderSettings(_ context.Context) (templ.Component, error) {
	return pages.SettingsPage(pages.SettingsData{
		Concurrency:     c.config.Concurrency,
		PollInterval:    c.config.PollInterval,
		BatchSize:       c.config.BatchSize,
		RequestTimeout:  c.config.RequestTimeout,
		MaxRetries:      c.config.MaxRetries,
		RetrySchedule:   c.config.RetrySchedule,
		ShutdownTimeout: c.config.ShutdownTimeout,
		CacheTTL:        c.config.CacheTTL,
	}), nil
}

// ─── Widget Renderers ────────────────────────────────────────────────────────

func (c *Contributor) renderStatsWidget(ctx context.Context) (templ.Component, error) {
	data := widgets.StatsData{
		EventTypes: fetchEventTypeCount(ctx, c.r),
		Pending:    fetchPendingCount(ctx, c.r),
		DLQSize:    fetchDLQCount(ctx, c.r),
	}

	eps, err := fetchAllEndpoints(ctx, c.r, endpoint.ListOpts{Limit: 1000})
	if err == nil {
		data.Endpoints = len(eps)
	}

	return widgets.StatsWidget(data), nil
}

func (c *Contributor) renderRecentDeliveriesWidget(ctx context.Context) (templ.Component, error) {
	eps, _ := fetchAllEndpoints(ctx, c.r, endpoint.ListOpts{Limit: 10})

	var recentDeliveries []*delivery.Delivery
	for _, ep := range eps {
		dels, err := fetchDeliveriesByEndpoint(ctx, c.r, ep.ID, delivery.ListOpts{Limit: 5})
		if err == nil {
			recentDeliveries = append(recentDeliveries, dels...)
		}
		if len(recentDeliveries) >= 5 {
			break
		}
	}
	if len(recentDeliveries) > 5 {
		recentDeliveries = recentDeliveries[:5]
	}

	return widgets.RecentDeliveriesWidget(recentDeliveries), nil
}

// ─── Settings Renderer ───────────────────────────────────────────────────────

func (c *Contributor) renderSettingsPanel(_ context.Context) (templ.Component, error) {
	return pages.SettingsPage(pages.SettingsData{
		Concurrency:     c.config.Concurrency,
		PollInterval:    c.config.PollInterval,
		BatchSize:       c.config.BatchSize,
		RequestTimeout:  c.config.RequestTimeout,
		MaxRetries:      c.config.MaxRetries,
		RetrySchedule:   c.config.RetrySchedule,
		ShutdownTimeout: c.config.ShutdownTimeout,
		CacheTTL:        c.config.CacheTTL,
	}), nil
}

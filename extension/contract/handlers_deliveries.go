package contract

import (
	"context"
	"time"

	"github.com/xraph/forge/extensions/dashboard/contract"

	"github.com/xraph/relay/delivery"
	"github.com/xraph/relay/endpoint"
	"github.com/xraph/relay/id"
)

// DeliverySummary is one row of the delivery log.
//
// State is the domain's own: pending, delivering, delivered or failed. The
// page shows a pending delivery with attempts as retrying, so AttemptCount is
// here for it to tell the difference.
type DeliverySummary struct {
	ID             string  `json:"id"`
	EventID        string  `json:"eventId"`
	EndpointID     string  `json:"endpointId"`
	EndpointURL    string  `json:"endpointUrl"`
	EventType      string  `json:"eventType"`
	TenantID       string  `json:"tenantId"`
	State          string  `json:"state"`
	AttemptCount   int     `json:"attemptCount"`
	MaxAttempts    int     `json:"maxAttempts"`
	NextAttemptAt  string  `json:"nextAttemptAt"`
	LastStatusCode int     `json:"lastStatusCode"`
	LastError      string  `json:"lastError,omitempty"`
	LastLatencyMs  int     `json:"lastLatencyMs"`
	CompletedAt    *string `json:"completedAt,omitempty"`
	CreatedAt      string  `json:"createdAt"`
	UpdatedAt      string  `json:"updatedAt"`
}

// AttemptView is one node of the retry sequence.
type AttemptView struct {
	ID            string  `json:"id"`
	AttemptNum    int     `json:"attemptNum"`
	StatusCode    int     `json:"statusCode"`
	Error         string  `json:"error,omitempty"`
	Response      string  `json:"response,omitempty"`
	LatencyMs     int     `json:"latencyMs"`
	Outcome       string  `json:"outcome"`
	NextAttemptAt *string `json:"nextAttemptAt,omitempty"`
	AttemptedAt   string  `json:"attemptedAt"`
}

// DeliveryDetail is a delivery with its attempts inline. Attempts are bounded
// by MaxAttempts, so a second round trip would buy nothing.
type DeliveryDetail struct {
	DeliverySummary
	LastResponse    string        `json:"lastResponse,omitempty"`
	EndpointEnabled *bool         `json:"endpointEnabled,omitempty"`
	Attempts        []AttemptView `json:"attempts"`
}

// ListDeliveriesInput is the delivery log's filter bar.
type ListDeliveriesInput struct {
	Cursor      string     `json:"cursor,omitempty"`
	Limit       int        `json:"limit,omitempty"`
	State       string     `json:"state,omitempty"`
	EndpointID  string     `json:"endpointId,omitempty"`
	EventID     string     `json:"eventId,omitempty"`
	EventType   string     `json:"eventType,omitempty"`
	TenantID    string     `json:"tenantId,omitempty"`
	StatusClass string     `json:"statusClass,omitempty"`
	From        *time.Time `json:"from,omitempty"`
	To          *time.Time `json:"to,omitempty"`
}

// ListDeliveriesResponse is one page of the log.
type ListDeliveriesResponse struct {
	Deliveries []DeliverySummary `json:"deliveries"`
	NextCursor string            `json:"nextCursor,omitempty"`
	Complete   bool              `json:"complete"`
}

// GetByIDInput names one record.
type GetByIDInput struct {
	ID string `json:"id"`
}

func rfc3339(t time.Time) string { return t.UTC().Format(time.RFC3339Nano) }

func rfc3339Ptr(t *time.Time) *string {
	if t == nil {
		return nil
	}
	s := rfc3339(*t)
	return &s
}

// endpointURLs resolves each distinct endpoint once per page. A deleted
// endpoint resolves to "" rather than failing the page: its deliveries are
// still history worth reading.
type endpointURLs struct {
	deps  Deps
	cache map[id.ID]*endpoint.Endpoint
}

func (u *endpointURLs) get(ctx context.Context, epID id.ID) *endpoint.Endpoint {
	if ep, ok := u.cache[epID]; ok {
		return ep
	}
	ep, err := u.deps.Relay.Store().GetEndpoint(ctx, epID)
	if err != nil {
		ep = nil
	}
	u.cache[epID] = ep
	return ep
}

func projectDelivery(d *delivery.Delivery, ep *endpoint.Endpoint) DeliverySummary {
	s := DeliverySummary{
		ID:             d.ID.String(),
		EventID:        d.EventID.String(),
		EndpointID:     d.EndpointID.String(),
		EventType:      d.EventType,
		TenantID:       d.TenantID,
		State:          string(d.State),
		AttemptCount:   d.AttemptCount,
		MaxAttempts:    d.MaxAttempts,
		NextAttemptAt:  rfc3339(d.NextAttemptAt),
		LastStatusCode: d.LastStatusCode,
		LastError:      d.LastError,
		LastLatencyMs:  d.LastLatencyMs,
		CompletedAt:    rfc3339Ptr(d.CompletedAt),
		CreatedAt:      rfc3339(d.CreatedAt),
		UpdatedAt:      rfc3339(d.UpdatedAt),
	}
	if ep != nil {
		s.EndpointURL = ep.URL
	}
	return s
}

func optionalID(raw string, parse func(string) (id.ID, error), what string) (*id.ID, error) {
	if raw == "" {
		return nil, nil
	}
	v, err := parse(raw)
	if err != nil {
		return nil, &contract.Error{Code: contract.CodeBadRequest, Message: "malformed " + what + " id",
			Details: map[string]any{"field": what + "Id"}}
	}
	return &v, nil
}

func deliveriesListHandler(deps Deps) func(context.Context, ListDeliveriesInput, contract.Principal) (ListDeliveriesResponse, error) {
	return func(ctx context.Context, in ListDeliveriesInput, _ contract.Principal) (ListDeliveriesResponse, error) {
		q := delivery.Query{
			Cursor:      in.Cursor,
			Limit:       in.Limit,
			EventType:   in.EventType,
			TenantID:    in.TenantID,
			StatusClass: delivery.StatusClass(in.StatusClass),
			From:        in.From,
			To:          in.To,
		}
		if in.State != "" {
			st := delivery.State(in.State)
			q.State = &st
		}
		var err error
		if q.EndpointID, err = optionalID(in.EndpointID, id.ParseEndpointID, "endpoint"); err != nil {
			return ListDeliveriesResponse{}, err
		}
		if q.EventID, err = optionalID(in.EventID, id.ParseEventID, "event"); err != nil {
			return ListDeliveriesResponse{}, err
		}

		page, err := deps.Relay.Store().ListDeliveries(ctx, q)
		if err != nil {
			return ListDeliveriesResponse{}, mapRelayError(err)
		}
		urls := &endpointURLs{deps: deps, cache: map[id.ID]*endpoint.Endpoint{}}
		out := ListDeliveriesResponse{
			Deliveries: make([]DeliverySummary, 0, len(page.Deliveries)),
			NextCursor: page.NextCursor,
			Complete:   page.Complete,
		}
		for _, d := range page.Deliveries {
			out.Deliveries = append(out.Deliveries, projectDelivery(d, urls.get(ctx, d.EndpointID)))
		}
		return out, nil
	}
}

func deliveriesDetailHandler(deps Deps) func(context.Context, GetByIDInput, contract.Principal) (DeliveryDetail, error) {
	return func(ctx context.Context, in GetByIDInput, _ contract.Principal) (DeliveryDetail, error) {
		delID, err := parseID(in.ID, id.ParseDeliveryID, "delivery")
		if err != nil {
			return DeliveryDetail{}, err
		}
		d, err := deps.Relay.Store().GetDelivery(ctx, delID)
		if err != nil {
			return DeliveryDetail{}, mapRelayError(err)
		}
		attempts, err := deps.Relay.Store().ListAttempts(ctx, delID)
		if err != nil {
			return DeliveryDetail{}, mapRelayError(err)
		}
		urls := &endpointURLs{deps: deps, cache: map[id.ID]*endpoint.Endpoint{}}
		ep := urls.get(ctx, d.EndpointID)
		out := DeliveryDetail{
			DeliverySummary: projectDelivery(d, ep),
			LastResponse:    d.LastResponse,
			Attempts:        make([]AttemptView, 0, len(attempts)),
		}
		if ep != nil {
			enabled := ep.Enabled
			out.EndpointEnabled = &enabled
		}
		for _, a := range attempts {
			out.Attempts = append(out.Attempts, AttemptView{
				ID:            a.ID.String(),
				AttemptNum:    a.AttemptNum,
				StatusCode:    a.StatusCode,
				Error:         a.Error,
				Response:      a.Response,
				LatencyMs:     a.LatencyMs,
				Outcome:       string(a.Outcome),
				NextAttemptAt: rfc3339Ptr(a.NextAttemptAt),
				AttemptedAt:   rfc3339(a.AttemptedAt),
			})
		}
		return out, nil
	}
}

// parseID parses a required id, answering BAD_REQUEST rather than a server
// error for one the client got wrong.
func parseID(raw string, parse func(string) (id.ID, error), what string) (id.ID, error) {
	if raw == "" {
		return id.ID{}, &contract.Error{Code: contract.CodeBadRequest, Message: what + " id is required"}
	}
	v, err := parse(raw)
	if err != nil {
		return id.ID{}, &contract.Error{Code: contract.CodeBadRequest, Message: "malformed " + what + " id"}
	}
	return v, nil
}

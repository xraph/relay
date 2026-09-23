package contract

import (
	"context"
	"net/url"
	"strings"
	"time"

	"github.com/xraph/forge/extensions/dashboard/contract"

	"github.com/xraph/relay/endpoint"
)

// EndpointSummary is the list row. It has no field for the signing secret, so
// no read path can carry one.
type EndpointSummary struct {
	ID          string   `json:"id"`
	TenantID    string   `json:"tenantId"`
	URL         string   `json:"url"`
	Description string   `json:"description,omitempty"`
	EventTypes  []string `json:"eventTypes"`
	Enabled     bool     `json:"enabled"`
	RateLimit   int      `json:"rateLimit"`

	// Signed reports whether the endpoint has a signing secret at all. Relay
	// now refuses to deliver to one without, but an endpoint written before
	// that fix, or straight to the store, can still exist without one, and
	// nothing else on the row would say so.
	Signed bool `json:"signed"`

	CreatedAt string `json:"createdAt"`
	UpdatedAt string `json:"updatedAt"`
}

// EndpointDetail adds what only the detail page needs.
type EndpointDetail struct {
	EndpointSummary
	Headers    map[string]string `json:"headers,omitempty"`
	Metadata   map[string]string `json:"metadata,omitempty"`
	ScopeAppID string            `json:"scopeAppId,omitempty"`
	ScopeOrgID string            `json:"scopeOrgId,omitempty"`
}

// ListEndpointsInput filters the list. An empty TenantID lists every tenant.
type ListEndpointsInput struct {
	TenantID string `json:"tenantId,omitempty"`
	Enabled  *bool  `json:"enabled,omitempty"`
}

type ListEndpointsResponse struct {
	Endpoints []EndpointSummary `json:"endpoints"`
}

type GetEndpointInput struct {
	ID string `json:"id"`
}

type CreateEndpointInput struct {
	TenantID    string            `json:"tenantId"`
	URL         string            `json:"url"`
	Description string            `json:"description,omitempty"`
	EventTypes  []string          `json:"eventTypes"`
	Headers     map[string]string `json:"headers,omitempty"`
	RateLimit   int               `json:"rateLimit,omitempty"`
	Metadata    map[string]string `json:"metadata,omitempty"`
}

// UpdateEndpointInput uses pointers so a nil field means "leave it alone" and
// an empty one means "clear it". endpoint.Service.Update guards every field
// with != "" and cannot clear any of them, so this handler writes the store
// directly.
type UpdateEndpointInput struct {
	ID          string             `json:"id"`
	URL         *string            `json:"url,omitempty"`
	Description *string            `json:"description,omitempty"`
	EventTypes  *[]string          `json:"eventTypes,omitempty"`
	Headers     *map[string]string `json:"headers,omitempty"`
	RateLimit   *int               `json:"rateLimit,omitempty"`
	Metadata    *map[string]string `json:"metadata,omitempty"`
}

type SetEnabledInput struct {
	ID      string `json:"id"`
	Enabled bool   `json:"enabled"`
}

// RotateSecretResponse carries the new signing secret. It is returned here and
// never again: the stored endpoint tags Secret json:"-" and no read intent has
// a field for it. Show it once and say so.
type RotateSecretResponse struct {
	ID     string `json:"id"`
	Secret string `json:"secret"`
}

type ResolveEndpointsInput struct {
	TenantID  string `json:"tenantId"`
	EventType string `json:"eventType"`
}

type ResolveEndpointsResponse struct {
	Endpoints []EndpointSummary `json:"endpoints"`
}

func projectEndpoint(ep *endpoint.Endpoint) EndpointSummary {
	eventTypes := ep.EventTypes
	if eventTypes == nil {
		eventTypes = []string{}
	}
	return EndpointSummary{
		ID:          ep.ID.String(),
		TenantID:    ep.TenantID,
		URL:         ep.URL,
		Description: ep.Description,
		EventTypes:  eventTypes,
		Enabled:     ep.Enabled,
		RateLimit:   ep.RateLimit,
		Signed:      strings.TrimSpace(ep.Secret) != "",
		CreatedAt:   ep.CreatedAt.UTC().Format(time.RFC3339),
		UpdatedAt:   ep.UpdatedAt.UTC().Format(time.RFC3339),
	}
}

func projectEndpoints(eps []*endpoint.Endpoint) []EndpointSummary {
	out := make([]EndpointSummary, 0, len(eps))
	for _, ep := range eps {
		out = append(out, projectEndpoint(ep))
	}
	return out
}

func endpointsListHandler(deps Deps) func(context.Context, ListEndpointsInput, contract.Principal) (ListEndpointsResponse, error) {
	return func(ctx context.Context, in ListEndpointsInput, _ contract.Principal) (ListEndpointsResponse, error) {
		// Limit 0 means every match on every backend. The list is not paged,
		// so a cap here would cut it off and still show it as complete.
		eps, err := deps.Relay.Store().ListEndpoints(ctx, in.TenantID,
			endpoint.ListOpts{Enabled: in.Enabled})
		if err != nil {
			return ListEndpointsResponse{}, mapRelayError(err)
		}
		return ListEndpointsResponse{Endpoints: projectEndpoints(eps)}, nil
	}
}

func endpointsDetailHandler(deps Deps) func(context.Context, GetEndpointInput, contract.Principal) (EndpointDetail, error) {
	return func(ctx context.Context, in GetEndpointInput, _ contract.Principal) (EndpointDetail, error) {
		epID, err := parseEndpointID(in.ID)
		if err != nil {
			return EndpointDetail{}, err
		}
		ep, err := deps.Relay.Store().GetEndpoint(ctx, epID)
		if err != nil {
			return EndpointDetail{}, mapRelayError(err)
		}
		return EndpointDetail{
			EndpointSummary: projectEndpoint(ep),
			Headers:         ep.Headers,
			Metadata:        ep.Metadata,
			ScopeAppID:      ep.ScopeAppID,
			ScopeOrgID:      ep.ScopeOrgID,
		}, nil
	}
}

func endpointsResolveHandler(deps Deps) func(context.Context, ResolveEndpointsInput, contract.Principal) (ResolveEndpointsResponse, error) {
	return func(ctx context.Context, in ResolveEndpointsInput, _ contract.Principal) (ResolveEndpointsResponse, error) {
		eps, err := deps.Relay.Store().Resolve(ctx, in.TenantID, in.EventType)
		if err != nil {
			return ResolveEndpointsResponse{}, mapRelayError(err)
		}
		return ResolveEndpointsResponse{Endpoints: projectEndpoints(eps)}, nil
	}
}

func endpointsCreateHandler(deps Deps) func(context.Context, CreateEndpointInput, contract.Principal) (AckResponse, error) {
	return func(ctx context.Context, in CreateEndpointInput, _ contract.Principal) (AckResponse, error) {
		ep, err := deps.Relay.Endpoints().Create(ctx, endpoint.Input{
			TenantID:    in.TenantID,
			URL:         in.URL,
			Description: in.Description,
			EventTypes:  in.EventTypes,
			Headers:     in.Headers,
			RateLimit:   in.RateLimit,
			Metadata:    in.Metadata,
		})
		if err != nil {
			return AckResponse{}, mapRelayError(err)
		}
		return AckResponse{OK: true, ID: ep.ID.String()}, nil
	}
}

func endpointsUpdateHandler(deps Deps) func(context.Context, UpdateEndpointInput, contract.Principal) (AckResponse, error) {
	return func(ctx context.Context, in UpdateEndpointInput, _ contract.Principal) (AckResponse, error) {
		epID, err := parseEndpointID(in.ID)
		if err != nil {
			return AckResponse{}, err
		}
		ep, err := deps.Relay.Store().GetEndpoint(ctx, epID)
		if err != nil {
			return AckResponse{}, mapRelayError(err)
		}
		// Bypassing the service means doing its URL check here.
		if in.URL != nil {
			if _, perr := url.ParseRequestURI(*in.URL); perr != nil {
				return AckResponse{}, mapRelayError(&endpoint.ValidationError{Field: "url", Message: "invalid URL"})
			}
			ep.URL = *in.URL
		}
		if in.Description != nil {
			ep.Description = *in.Description
		}
		if in.EventTypes != nil {
			if len(*in.EventTypes) == 0 {
				return AckResponse{}, mapRelayError(&endpoint.ValidationError{
					Field: "event_types", Message: "at least one event type pattern required"})
			}
			ep.EventTypes = *in.EventTypes
		}
		if in.Headers != nil {
			ep.Headers = *in.Headers
		}
		if in.RateLimit != nil {
			ep.RateLimit = *in.RateLimit
		}
		if in.Metadata != nil {
			ep.Metadata = *in.Metadata
		}
		ep.UpdatedAt = time.Now().UTC()
		if err := deps.Relay.Store().UpdateEndpoint(ctx, ep); err != nil {
			return AckResponse{}, mapRelayError(err)
		}
		return AckResponse{OK: true, ID: ep.ID.String()}, nil
	}
}

func endpointsDeleteHandler(deps Deps) func(context.Context, GetEndpointInput, contract.Principal) (AckResponse, error) {
	return func(ctx context.Context, in GetEndpointInput, _ contract.Principal) (AckResponse, error) {
		epID, err := parseEndpointID(in.ID)
		if err != nil {
			return AckResponse{}, err
		}
		if err := deps.Relay.Endpoints().Delete(ctx, epID); err != nil {
			return AckResponse{}, mapRelayError(err)
		}
		return AckResponse{OK: true, ID: epID.String()}, nil
	}
}

func endpointsSetEnabledHandler(deps Deps) func(context.Context, SetEnabledInput, contract.Principal) (AckResponse, error) {
	return func(ctx context.Context, in SetEnabledInput, _ contract.Principal) (AckResponse, error) {
		epID, err := parseEndpointID(in.ID)
		if err != nil {
			return AckResponse{}, err
		}
		if err := deps.Relay.Endpoints().SetEnabled(ctx, epID, in.Enabled); err != nil {
			return AckResponse{}, mapRelayError(err)
		}
		return AckResponse{OK: true, ID: epID.String()}, nil
	}
}

func endpointsRotateSecretHandler(deps Deps) func(context.Context, GetEndpointInput, contract.Principal) (RotateSecretResponse, error) {
	return func(ctx context.Context, in GetEndpointInput, _ contract.Principal) (RotateSecretResponse, error) {
		epID, err := parseEndpointID(in.ID)
		if err != nil {
			return RotateSecretResponse{}, err
		}
		secret, err := deps.Relay.Endpoints().RotateSecret(ctx, epID)
		if err != nil {
			return RotateSecretResponse{}, mapRelayError(err)
		}
		return RotateSecretResponse{ID: epID.String(), Secret: secret}, nil
	}
}

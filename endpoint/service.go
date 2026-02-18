package endpoint

import (
	"context"
	"log/slog"
	"net/url"

	"github.com/xraph/relay/id"
	"github.com/xraph/relay/internal/entity"
	"github.com/xraph/relay/signature"
)

// Service provides endpoint management operations.
type Service struct {
	store  Store
	logger *slog.Logger
}

// NewService creates a new endpoint service.
func NewService(store Store, logger *slog.Logger) *Service {
	if logger == nil {
		logger = slog.Default()
	}
	return &Service{
		store:  store,
		logger: logger,
	}
}

// Create registers a new webhook endpoint.
func (svc *Service) Create(ctx context.Context, in Input) (*Endpoint, error) {
	if _, err := url.ParseRequestURI(in.URL); err != nil {
		return nil, &ValidationError{Field: "url", Message: "invalid URL"}
	}

	if in.TenantID == "" {
		return nil, &ValidationError{Field: "tenant_id", Message: "required"}
	}

	if len(in.EventTypes) == 0 {
		return nil, &ValidationError{Field: "event_types", Message: "at least one event type pattern required"}
	}

	secret := in.Secret
	if secret == "" {
		secret = signature.GenerateSecret()
	}

	ep := &Endpoint{
		Entity:      entity.New(),
		ID:          id.NewEndpointID(),
		TenantID:    in.TenantID,
		URL:         in.URL,
		Description: in.Description,
		Secret:      secret,
		EventTypes:  in.EventTypes,
		Headers:     in.Headers,
		Enabled:     true,
		RateLimit:   in.RateLimit,
		Metadata:    in.Metadata,
	}

	if err := svc.store.CreateEndpoint(ctx, ep); err != nil {
		return nil, err
	}

	return ep, nil
}

// Get returns an endpoint by ID.
func (svc *Service) Get(ctx context.Context, epID id.ID) (*Endpoint, error) {
	return svc.store.GetEndpoint(ctx, epID)
}

// Update modifies an existing endpoint.
func (svc *Service) Update(ctx context.Context, epID id.ID, in Input) (*Endpoint, error) {
	ep, err := svc.store.GetEndpoint(ctx, epID)
	if err != nil {
		return nil, err
	}

	if in.URL != "" {
		if _, err := url.ParseRequestURI(in.URL); err != nil {
			return nil, &ValidationError{Field: "url", Message: "invalid URL"}
		}
		ep.URL = in.URL
	}
	if in.Description != "" {
		ep.Description = in.Description
	}
	if len(in.EventTypes) > 0 {
		ep.EventTypes = in.EventTypes
	}
	if in.Headers != nil {
		ep.Headers = in.Headers
	}
	if in.RateLimit >= 0 {
		ep.RateLimit = in.RateLimit
	}
	if in.Metadata != nil {
		ep.Metadata = in.Metadata
	}

	if err := svc.store.UpdateEndpoint(ctx, ep); err != nil {
		return nil, err
	}

	return ep, nil
}

// Delete removes an endpoint.
func (svc *Service) Delete(ctx context.Context, epID id.ID) error {
	return svc.store.DeleteEndpoint(ctx, epID)
}

// List returns endpoints for a tenant.
func (svc *Service) List(ctx context.Context, tenantID string, opts ListOpts) ([]*Endpoint, error) {
	return svc.store.ListEndpoints(ctx, tenantID, opts)
}

// SetEnabled enables or disables an endpoint.
func (svc *Service) SetEnabled(ctx context.Context, epID id.ID, enabled bool) error {
	return svc.store.SetEnabled(ctx, epID, enabled)
}

// RotateSecret generates a new signing secret for an endpoint.
func (svc *Service) RotateSecret(ctx context.Context, epID id.ID) (string, error) {
	ep, err := svc.store.GetEndpoint(ctx, epID)
	if err != nil {
		return "", err
	}

	newSecret := signature.GenerateSecret()

	ep.Secret = newSecret
	if err := svc.store.UpdateEndpoint(ctx, ep); err != nil {
		return "", err
	}

	return newSecret, nil
}

// ValidationError indicates invalid input.
type ValidationError struct {
	Field   string
	Message string
}

func (e *ValidationError) Error() string {
	return "endpoint validation: " + e.Field + ": " + e.Message
}

package endpoint

import (
	"context"
	"net/url"
	"strings"

	log "github.com/xraph/go-utils/log"

	"github.com/xraph/relay/id"
	"github.com/xraph/relay/internal/entity"
	"github.com/xraph/relay/signature"
)

// Service provides endpoint management operations.
type Service struct {
	store  Store
	logger log.Logger
}

// NewService creates a new endpoint service.
func NewService(store Store, logger log.Logger) *Service {
	if logger == nil {
		logger = log.NewNoopLogger()
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

	// Whitespace counts as absent, matching signature.Sign. If the two
	// definitions disagreed, an endpoint created here could be stored with a
	// secret the signer then refuses, and it would never receive a delivery.
	secret := in.Secret
	if strings.TrimSpace(secret) == "" {
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

// ListUnsigned returns endpoints that have no signing secret. Deliveries to
// these fail rather than being sent, because a signature derived from an
// empty key verifies against that same empty key and so looks authentic to a
// receiver doing everything right.
//
// This exists to be run before upgrading: it names what the signing change
// will start failing. Create has always generated a secret when none is
// supplied (whitespace included), so an endpoint can only reach this state
// through the store interface directly.
//
// tenantID is required and is matched literally. There is no way to scan
// every tenant: ListEndpoints compares tenant_id for equality on all five
// backends. An empty tenantID returns a ValidationError rather than an empty
// list, because an empty list would read as a clean audit.
//
// At most 10000 endpoints are examined per tenant. A tenant with more than
// that gets a partial audit.
func (svc *Service) ListUnsigned(ctx context.Context, tenantID string) ([]*Endpoint, error) {
	// An empty tenant would match nothing and return an empty list with no
	// error, which reads as a clean audit. Refuse it instead.
	if strings.TrimSpace(tenantID) == "" {
		return nil, &ValidationError{Field: "tenant_id", Message: "required: ListUnsigned audits one tenant at a time"}
	}
	eps, err := svc.store.ListEndpoints(ctx, tenantID, ListOpts{Limit: 10000})
	if err != nil {
		return nil, err
	}
	out := make([]*Endpoint, 0)
	for _, ep := range eps {
		if strings.TrimSpace(ep.Secret) == "" {
			out = append(out, ep)
		}
	}
	return out, nil
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

package storetest

import (
	"context"
	"testing"

	"github.com/xraph/relay/endpoint"
	"github.com/xraph/relay/id"
	"github.com/xraph/relay/internal/entity"
	"github.com/xraph/relay/signature"
)

// EndpointBackend is the slice of a store the endpoint suite exercises.
type EndpointBackend interface {
	CreateEndpoint(ctx context.Context, ep *endpoint.Endpoint) error
	ListEndpoints(ctx context.Context, tenantID string, opts endpoint.ListOpts) ([]*endpoint.Endpoint, error)
	Resolve(ctx context.Context, tenantID string, eventType string) ([]*endpoint.Endpoint, error)
}

func newEndpoint(t *testing.T, s EndpointBackend, tenant string, enabled bool) *endpoint.Endpoint {
	t.Helper()
	ep := &endpoint.Endpoint{
		Entity:     entity.New(),
		ID:         id.NewEndpointID(),
		TenantID:   tenant,
		URL:        "https://receiver.example/hook",
		Secret:     signature.GenerateSecret(),
		EventTypes: []string{"*"},
		Enabled:    enabled,
	}
	if err := s.CreateEndpoint(context.Background(), ep); err != nil {
		t.Fatalf("create endpoint: %v", err)
	}
	return ep
}

func idSet(eps []*endpoint.Endpoint) map[string]bool {
	out := make(map[string]bool, len(eps))
	for _, ep := range eps {
		out[ep.ID.String()] = true
	}
	return out
}

// RunEndpointSuite asserts the endpoint listing semantics every backend must
// share. Before it existed, ListEndpoints matched an empty tenant literally on
// all five backends while ListDLQ treated it as every tenant, and postgres and
// sqlite ignored the Enabled filter that the other three honoured.
func RunEndpointSuite(t *testing.T, newStore func(t *testing.T) EndpointBackend) {
	t.Helper()

	// An empty tenant means every tenant, the same as ListDLQ. A caller that
	// wants one tenant says which. Identity, not a total, so a shared database
	// cannot skew it.
	t.Run("empty tenant lists every tenant", func(t *testing.T) {
		s := newStore(t)
		a := newEndpoint(t, s, uniqueTenant(t), true)
		b := newEndpoint(t, s, uniqueTenant(t), true)

		got, err := s.ListEndpoints(context.Background(), "", endpoint.ListOpts{Limit: 10000})
		if err != nil {
			t.Fatalf("list: %v", err)
		}
		ids := idSet(got)
		if !ids[a.ID.String()] || !ids[b.ID.String()] {
			t.Fatalf("an empty tenant did not list endpoints from two different "+
				"tenants (got a=%v b=%v). It is matching the empty string literally",
				ids[a.ID.String()], ids[b.ID.String()])
		}
	})

	t.Run("a tenant lists only its own", func(t *testing.T) {
		s := newStore(t)
		mine := newEndpoint(t, s, uniqueTenant(t), true)
		newEndpoint(t, s, uniqueTenant(t), true)

		got, err := s.ListEndpoints(context.Background(), mine.TenantID, endpoint.ListOpts{Limit: 100})
		if err != nil {
			t.Fatalf("list: %v", err)
		}
		if len(got) != 1 || got[0].ID != mine.ID {
			t.Fatalf("a tenant's list returned %d endpoints, want exactly its own", len(got))
		}
	})

	t.Run("Enabled filters", func(t *testing.T) {
		s := newStore(t)
		tenant := uniqueTenant(t)
		on := newEndpoint(t, s, tenant, true)
		off := newEndpoint(t, s, tenant, false)

		for _, tc := range []struct {
			enabled bool
			want    *endpoint.Endpoint
			not     *endpoint.Endpoint
		}{{false, off, on}, {true, on, off}} {
			enabled := tc.enabled
			got, err := s.ListEndpoints(context.Background(), tenant,
				endpoint.ListOpts{Limit: 100, Enabled: &enabled})
			if err != nil {
				t.Fatalf("list enabled=%v: %v", enabled, err)
			}
			ids := idSet(got)
			if !ids[tc.want.ID.String()] || ids[tc.not.ID.String()] || len(got) != 1 {
				t.Fatalf("Enabled=%v returned %d endpoints, want only the one with "+
					"enabled=%v. A backend ignoring the filter returns both", enabled, len(got), enabled)
			}
		}
	})

	// Guard. Resolve is the delivery hot path and must keep matching the
	// tenant literally: an event with no tenant must never fan out to another
	// tenant's endpoints. This passes before and after the ListEndpoints fix.
	t.Run("Resolve stays literal for an empty tenant", func(t *testing.T) {
		s := newStore(t)
		ep := newEndpoint(t, s, uniqueTenant(t), true)

		got, err := s.Resolve(context.Background(), "", "invoice.created")
		if err != nil {
			t.Fatalf("resolve: %v", err)
		}
		if idSet(got)[ep.ID.String()] {
			t.Fatal("Resolve with an empty tenant returned a real tenant's endpoint: " +
				"an untenanted event would be delivered to another tenant")
		}
	})

	t.Run("Resolve returns a tenant's matching enabled endpoints", func(t *testing.T) {
		s := newStore(t)
		tenant := uniqueTenant(t)
		on := newEndpoint(t, s, tenant, true)
		off := newEndpoint(t, s, tenant, false)

		got, err := s.Resolve(context.Background(), tenant, "invoice.created")
		if err != nil {
			t.Fatalf("resolve: %v", err)
		}
		ids := idSet(got)
		if !ids[on.ID.String()] || ids[off.ID.String()] {
			t.Fatalf("Resolve returned %d endpoints, want only the enabled one", len(got))
		}
	})
}

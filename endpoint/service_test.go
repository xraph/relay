package endpoint_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/xraph/relay"
	"github.com/xraph/relay/endpoint"
	"github.com/xraph/relay/id"
	"github.com/xraph/relay/store/memory"
)

func ctx() context.Context { return context.Background() }

func newService() *endpoint.Service {
	s := memory.New()
	return endpoint.NewService(s, nil)
}

func TestEndpointServiceCreate(t *testing.T) {
	svc := newService()

	ep, err := svc.Create(ctx(), endpoint.Input{
		TenantID:   "tenant-1",
		URL:        "https://example.com/webhook",
		EventTypes: []string{"invoice.*"},
	})
	if err != nil {
		t.Fatal(err)
	}

	if ep.ID.String() == "" {
		t.Fatal("expected non-empty ID")
	}
	if !strings.HasPrefix(ep.Secret, "whsec_") {
		t.Fatalf("expected auto-generated secret, got %q", ep.Secret)
	}
	if !ep.Enabled {
		t.Fatal("expected enabled by default")
	}
}

func TestEndpointServiceCreateValidation(t *testing.T) {
	svc := newService()

	// Missing URL
	_, err := svc.Create(ctx(), endpoint.Input{
		TenantID:   "t1",
		EventTypes: []string{"*"},
	})
	if err == nil {
		t.Fatal("expected error for missing URL")
	}

	// Missing tenant ID
	_, err = svc.Create(ctx(), endpoint.Input{
		URL:        "https://example.com",
		EventTypes: []string{"*"},
	})
	if err == nil {
		t.Fatal("expected error for missing tenant_id")
	}

	// Missing event types
	_, err = svc.Create(ctx(), endpoint.Input{
		TenantID: "t1",
		URL:      "https://example.com",
	})
	if err == nil {
		t.Fatal("expected error for missing event_types")
	}
}

func TestEndpointServiceGetUpdateDelete(t *testing.T) {
	svc := newService()

	ep, _ := svc.Create(ctx(), endpoint.Input{
		TenantID:   "t1",
		URL:        "https://example.com/webhook",
		EventTypes: []string{"*"},
	})

	// Get
	got, err := svc.Get(ctx(), ep.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.URL != "https://example.com/webhook" {
		t.Fatalf("got URL %q", got.URL)
	}

	// Update
	updated, err := svc.Update(ctx(), ep.ID, endpoint.Input{
		Description: "Updated description",
	})
	if err != nil {
		t.Fatal(err)
	}
	if updated.Description != "Updated description" {
		t.Fatalf("expected updated description, got %q", updated.Description)
	}

	// Delete
	err = svc.Delete(ctx(), ep.ID)
	if err != nil {
		t.Fatal(err)
	}

	_, err = svc.Get(ctx(), ep.ID)
	if !errors.Is(err, relay.ErrEndpointNotFound) {
		t.Fatalf("expected deleted, got %v", err)
	}
}

func TestEndpointServiceList(t *testing.T) {
	svc := newService()

	for i := 0; i < 3; i++ {
		_, _ = svc.Create(ctx(), endpoint.Input{
			TenantID:   "t1",
			URL:        "https://example.com/webhook",
			EventTypes: []string{"*"},
		})
	}
	_, _ = svc.Create(ctx(), endpoint.Input{
		TenantID:   "t2",
		URL:        "https://example.com/webhook",
		EventTypes: []string{"*"},
	})

	list, err := svc.List(ctx(), "t1", endpoint.ListOpts{})
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 3 {
		t.Fatalf("expected 3, got %d", len(list))
	}
}

func TestEndpointServiceSetEnabled(t *testing.T) {
	svc := newService()

	ep, _ := svc.Create(ctx(), endpoint.Input{
		TenantID:   "t1",
		URL:        "https://example.com/webhook",
		EventTypes: []string{"*"},
	})

	if err := svc.SetEnabled(ctx(), ep.ID, false); err != nil {
		t.Fatal(err)
	}

	got, _ := svc.Get(ctx(), ep.ID)
	if got.Enabled {
		t.Fatal("expected disabled")
	}
}

func TestEndpointServiceRotateSecret(t *testing.T) {
	svc := newService()

	ep, _ := svc.Create(ctx(), endpoint.Input{
		TenantID:   "t1",
		URL:        "https://example.com/webhook",
		EventTypes: []string{"*"},
	})

	oldSecret := ep.Secret
	newSecret, err := svc.RotateSecret(ctx(), ep.ID)
	if err != nil {
		t.Fatal(err)
	}

	if newSecret == oldSecret {
		t.Fatal("expected different secret after rotation")
	}
	if !strings.HasPrefix(newSecret, "whsec_") {
		t.Fatalf("expected whsec_ prefix, got %q", newSecret)
	}

	got, _ := svc.Get(ctx(), ep.ID)
	if got.Secret != newSecret {
		t.Fatal("secret not persisted after rotation")
	}
}

func TestEndpointServiceRotateSecretNotFound(t *testing.T) {
	svc := newService()

	_, err := svc.RotateSecret(ctx(), id.NewEndpointID())
	if !errors.Is(err, relay.ErrEndpointNotFound) {
		t.Fatalf("expected ErrEndpointNotFound, got %v", err)
	}
}

// ListUnsigned exists so an operator can find endpoints that will start
// failing before the signing change reaches them. An endpoint can only reach
// this state through the store directly, since Create always generates a
// secret, but the store interface is public and relay's own dashboard already
// reaches past the service to it.
func TestListUnsignedFindsEndpointsWithNoSecret(t *testing.T) {
	store := memory.New()
	svc := endpoint.NewService(store, nil)

	signed, err := svc.Create(ctx(), endpoint.Input{
		TenantID:   "t1",
		URL:        "https://a.example/hook",
		EventTypes: []string{"invoice.*"},
	})
	if err != nil {
		t.Fatalf("create signed: %v", err)
	}

	unsigned := &endpoint.Endpoint{
		ID:         id.NewEndpointID(),
		TenantID:   "t1",
		URL:        "https://b.example/hook",
		EventTypes: []string{"invoice.*"},
		Secret:     "",
		Enabled:    true,
	}
	if createErr := store.CreateEndpoint(ctx(), unsigned); createErr != nil {
		t.Fatalf("create unsigned: %v", createErr)
	}

	got, err := svc.ListUnsigned(ctx(), "t1")
	if err != nil {
		t.Fatalf("list unsigned: %v", err)
	}
	// Assert on identity, not on count. A count assertion passes when the
	// wrong rows come back in the right quantity.
	if len(got) != 1 {
		t.Fatalf("got %d unsigned endpoints, want 1", len(got))
	}
	if got[0].ID != unsigned.ID {
		t.Fatalf("found %s, want the unsigned endpoint %s (the signed one is %s)",
			got[0].ID, unsigned.ID, signed.ID)
	}
}

func TestListUnsignedTreatsWhitespaceAsAbsent(t *testing.T) {
	store := memory.New()
	svc := endpoint.NewService(store, nil)

	blank := &endpoint.Endpoint{
		ID:         id.NewEndpointID(),
		TenantID:   "t1",
		URL:        "https://c.example/hook",
		EventTypes: []string{"*"},
		Secret:     "   ",
		Enabled:    true,
	}
	if err := store.CreateEndpoint(ctx(), blank); err != nil {
		t.Fatalf("create: %v", err)
	}

	got, err := svc.ListUnsigned(ctx(), "t1")
	if err != nil {
		t.Fatalf("list unsigned: %v", err)
	}
	if len(got) != 1 || got[0].ID != blank.ID {
		t.Fatalf("a whitespace secret was not reported as unsigned: got %d rows", len(got))
	}
}

func TestListUnsignedIsEmptyWhenEveryEndpointIsSigned(t *testing.T) {
	svc := newService()
	if _, err := svc.Create(ctx(), endpoint.Input{
		TenantID:   "t1",
		URL:        "https://a.example/hook",
		EventTypes: []string{"*"},
	}); err != nil {
		t.Fatalf("create: %v", err)
	}

	got, err := svc.ListUnsigned(ctx(), "t1")
	if err != nil {
		t.Fatalf("list unsigned: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("got %d unsigned endpoints, want 0", len(got))
	}
}

// Pins a behaviour every backend shares and one caller has wrong.
//
// ListEndpoints matches tenant_id literally: postgres and sqlite issue
// `WHERE tenant_id = ?`, mongo filters on the same field, redis reads a
// per-tenant sorted set, and memory compares with !=. So an empty tenantID
// returns only endpoints whose tenant is the empty string, never every
// tenant. dashboard/data.go's fetchAllEndpoints passes "" with the comment
// "returns all endpoints across all tenants", which is why the templ
// dashboard's endpoint count, endpoints page, deliveries page and both
// widgets render empty.
//
// Recorded rather than fixed here: making empty mean "all" is a semantic
// change to a core method across five backends and belongs in its own
// change, not in a signing fix.
func TestListEndpointsTreatsAnEmptyTenantLiterally(t *testing.T) {
	store := memory.New()
	svc := endpoint.NewService(store, nil)

	if _, err := svc.Create(ctx(), endpoint.Input{
		TenantID:   "t1",
		URL:        "https://a.example/hook",
		EventTypes: []string{"*"},
	}); err != nil {
		t.Fatalf("create: %v", err)
	}

	got, err := store.ListEndpoints(ctx(), "", endpoint.ListOpts{Limit: 100})
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("an empty tenant returned %d endpoints. If this backend now "+
			"means 'every tenant', the backends disagree and callers cannot "+
			"tell which they are talking to", len(got))
	}
}

// Create generated a secret only for "", so "   " was stored as the secret.
// The signing primitive treats whitespace as absent, which meant an endpoint
// created through the ordinary service path could never receive a delivery,
// contradicting the README's claim that only a direct store write reaches
// this state. The two definitions of "absent" have to agree.
func TestCreateReplacesAWhitespaceSecret(t *testing.T) {
	svc := newService()
	ep, err := svc.Create(ctx(), endpoint.Input{
		TenantID:   "t1",
		URL:        "https://a.example/hook",
		EventTypes: []string{"*"},
		Secret:     "   ",
	})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if strings.TrimSpace(ep.Secret) == "" {
		t.Fatalf("Create stored a whitespace secret %q instead of generating one", ep.Secret)
	}
	if !strings.HasPrefix(ep.Secret, "whsec_") {
		t.Fatalf("Secret = %q, want a generated whsec_ secret", ep.Secret)
	}

	unsigned, err := svc.ListUnsigned(ctx(), "t1")
	if err != nil {
		t.Fatalf("list unsigned: %v", err)
	}
	if len(unsigned) != 0 {
		t.Fatalf("an endpoint created through the service is reported unsigned")
	}
}

// An empty tenant returned an empty list and a nil error, which reads as
// "you have no unsigned endpoints". On a security audit that is the worst
// possible wrong answer, because the operator stops looking. The plan this
// was written from told readers to call it exactly that way.
func TestListUnsignedRefusesAnEmptyTenant(t *testing.T) {
	svc := newService()
	for _, tenant := range []string{"", "   "} {
		got, err := svc.ListUnsigned(ctx(), tenant)
		var verr *endpoint.ValidationError
		if !errors.As(err, &verr) {
			t.Fatalf("ListUnsigned(%q) error = %v, want a ValidationError; "+
				"got %d rows, which reads as a clean audit", tenant, err, len(got))
		}
		if verr.Field != "tenant_id" {
			t.Fatalf("ValidationError.Field = %q, want tenant_id", verr.Field)
		}
	}
}

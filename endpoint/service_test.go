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

// Pins what an empty tenant means to ListEndpoints: every tenant, the same as
// ListDLQ. Until this was fixed it matched the empty string literally on all
// five backends while ListDLQ did the opposite, and dashboard/data.go's
// fetchAllEndpoints, which passes "" expecting every tenant, got nothing. That
// emptied the templ overview count, endpoints page, deliveries page and both
// widgets. This test previously pinned the literal behaviour; it flipped when
// the behaviour was fixed, which is what a pinning test is for.
func TestListEndpointsListsEveryTenantForAnEmptyTenant(t *testing.T) {
	store := memory.New()
	svc := endpoint.NewService(store, nil)

	a, err := svc.Create(ctx(), endpoint.Input{
		TenantID:   "t1",
		URL:        "https://a.example/hook",
		EventTypes: []string{"*"},
	})
	if err != nil {
		t.Fatalf("create a: %v", err)
	}
	b, err := svc.Create(ctx(), endpoint.Input{
		TenantID:   "t2",
		URL:        "https://b.example/hook",
		EventTypes: []string{"*"},
	})
	if err != nil {
		t.Fatalf("create b: %v", err)
	}

	got, err := store.ListEndpoints(ctx(), "", endpoint.ListOpts{Limit: 100})
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	seen := map[string]bool{}
	for _, ep := range got {
		seen[ep.ID.String()] = true
	}
	if !seen[a.ID.String()] || !seen[b.ID.String()] {
		t.Fatalf("an empty tenant did not list both tenants' endpoints (t1=%v t2=%v)",
			seen[a.ID.String()], seen[b.ID.String()])
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

func unsignedEndpoint(t *testing.T, store *memory.Store, tenant string) *endpoint.Endpoint {
	t.Helper()
	ep := &endpoint.Endpoint{
		ID:         id.NewEndpointID(),
		TenantID:   tenant,
		URL:        "https://unsigned.example/hook",
		EventTypes: []string{"*"},
		Enabled:    true,
	}
	if err := store.CreateEndpoint(ctx(), ep); err != nil {
		t.Fatalf("create unsigned: %v", err)
	}
	return ep
}

// An empty tenant audits every tenant. It used to be refused, because
// ListEndpoints("") returned nothing and an empty list would have read as a
// clean result. Now that ListEndpoints("") means every tenant, refusing it
// would only make the audit harder to run.
func TestListUnsignedAuditsEveryTenant(t *testing.T) {
	store := memory.New()
	svc := endpoint.NewService(store, nil)
	a := unsignedEndpoint(t, store, "t1")
	b := unsignedEndpoint(t, store, "t2")

	for _, tenant := range []string{"", "   "} {
		got, err := svc.ListUnsigned(ctx(), tenant)
		if err != nil {
			t.Fatalf("ListUnsigned(%q): %v", tenant, err)
		}
		seen := map[string]bool{}
		for _, ep := range got {
			seen[ep.ID.String()] = true
		}
		// A tenant of spaces is no tenant. Reading it literally would match
		// nothing and report a clean audit, the worst possible wrong answer.
		if !seen[a.ID.String()] || !seen[b.ID.String()] {
			t.Fatalf("ListUnsigned(%q) did not find unsigned endpoints in both tenants "+
				"(t1=%v t2=%v)", tenant, seen[a.ID.String()], seen[b.ID.String()])
		}
	}
}

// An audit that stops at one page reports a partial result as a whole one.
// Across every tenant, one page is a real risk, so it reads until the store
// runs out.
func TestListUnsignedReadsPastOnePage(t *testing.T) {
	defer endpoint.SetUnsignedPageSizeForTest(2)()
	store := memory.New()
	svc := endpoint.NewService(store, nil)

	want := map[string]bool{}
	for i := 0; i < 5; i++ {
		want[unsignedEndpoint(t, store, "t1").ID.String()] = true
	}
	got, err := svc.ListUnsigned(ctx(), "t1")
	if err != nil {
		t.Fatalf("list unsigned: %v", err)
	}
	if len(got) != len(want) {
		t.Fatalf("found %d of %d unsigned endpoints across pages of 2", len(got), len(want))
	}
	for _, ep := range got {
		if !want[ep.ID.String()] {
			t.Fatalf("returned an endpoint it should not have: %s", ep.ID)
		}
	}
}

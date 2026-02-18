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

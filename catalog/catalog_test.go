package catalog_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/xraph/relay"
	"github.com/xraph/relay/catalog"
	"github.com/xraph/relay/store/memory"
)

func ctx() context.Context { return context.Background() }

func newCatalog() *catalog.Catalog {
	s := memory.New()
	return catalog.NewCatalog(s, catalog.Config{CacheTTL: 30 * time.Second}, nil)
}

func TestCatalogRegisterAndGet(t *testing.T) {
	c := newCatalog()

	et, err := c.RegisterType(ctx(), catalog.WebhookDefinition{
		Name:        "invoice.created",
		Description: "Invoice created",
		Group:       "invoice",
	})
	if err != nil {
		t.Fatal(err)
	}
	if et.ID.String() == "" {
		t.Fatal("expected non-empty ID")
	}

	got, err := c.GetType(ctx(), "invoice.created")
	if err != nil {
		t.Fatal(err)
	}
	if got.Definition.Name != "invoice.created" {
		t.Fatalf("got %q", got.Definition.Name)
	}
}

func TestCatalogCacheHit(t *testing.T) {
	c := newCatalog()

	_, err := c.RegisterType(ctx(), catalog.WebhookDefinition{Name: "a.event"})
	if err != nil {
		t.Fatal(err)
	}

	// First call populates cache.
	got1, _ := c.GetType(ctx(), "a.event")
	// Second call should return same pointer (cache hit).
	got2, _ := c.GetType(ctx(), "a.event")

	if got1 != got2 {
		t.Fatal("expected cache hit (same pointer)")
	}
}

func TestCatalogCacheTTLExpiry(t *testing.T) {
	s := memory.New()
	c := catalog.NewCatalog(s, catalog.Config{CacheTTL: 1 * time.Millisecond}, nil)

	_, err := c.RegisterType(ctx(), catalog.WebhookDefinition{Name: "b.event"})
	if err != nil {
		t.Fatal(err)
	}

	// Wait for cache to expire.
	time.Sleep(5 * time.Millisecond)

	// Should still find it (re-read from store).
	_, err = c.GetType(ctx(), "b.event")
	if err != nil {
		t.Fatal("expected to re-read from store after TTL, got:", err)
	}
}

func TestCatalogGetNotFound(t *testing.T) {
	c := newCatalog()

	_, err := c.GetType(ctx(), "does.not.exist")
	if !errors.Is(err, relay.ErrEventTypeNotFound) {
		t.Fatalf("expected ErrEventTypeNotFound, got %v", err)
	}
}

func TestCatalogUpsert(t *testing.T) {
	c := newCatalog()

	_, err := c.RegisterType(ctx(), catalog.WebhookDefinition{
		Name:        "invoice.created",
		Description: "v1",
	})
	if err != nil {
		t.Fatal(err)
	}

	_, err = c.RegisterType(ctx(), catalog.WebhookDefinition{
		Name:        "invoice.created",
		Description: "v2",
	})
	if err != nil {
		t.Fatal(err)
	}

	got, _ := c.GetType(ctx(), "invoice.created")
	if got.Definition.Description != "v2" {
		t.Fatalf("expected v2, got %q", got.Definition.Description)
	}
}

func TestCatalogDelete(t *testing.T) {
	c := newCatalog()

	_, _ = c.RegisterType(ctx(), catalog.WebhookDefinition{Name: "x.event"})

	if err := c.DeleteType(ctx(), "x.event"); err != nil {
		t.Fatal(err)
	}

	// Cache should be cleared.
	_, err := c.GetType(ctx(), "x.event")
	// Depending on store implementation, this might return the deprecated type
	// or not. The memory store's GetType returns deprecated types.
	if err != nil {
		t.Fatal(err)
	}
}

func TestCatalogMatchTypesForEvent(t *testing.T) {
	c := newCatalog()

	_, _ = c.RegisterType(ctx(), catalog.WebhookDefinition{Name: "invoice.created"})
	_, _ = c.RegisterType(ctx(), catalog.WebhookDefinition{Name: "invoice.paid"})
	_, _ = c.RegisterType(ctx(), catalog.WebhookDefinition{Name: "user.created"})

	result, err := c.MatchTypesForEvent(ctx(), "invoice.*")
	if err != nil {
		t.Fatal(err)
	}
	if len(result) != 2 {
		t.Fatalf("expected 2, got %d", len(result))
	}
}

func TestCatalogInvalidateCache(t *testing.T) {
	c := newCatalog()

	_, _ = c.RegisterType(ctx(), catalog.WebhookDefinition{Name: "cached.event"})

	// Get to populate cache.
	_, _ = c.GetType(ctx(), "cached.event")

	// Invalidate.
	c.InvalidateCache()

	// Should still work (re-reads from store).
	_, err := c.GetType(ctx(), "cached.event")
	if err != nil {
		t.Fatal(err)
	}
}

func TestCatalogRegisterWithOptions(t *testing.T) {
	c := newCatalog()

	et, err := c.RegisterType(ctx(), catalog.WebhookDefinition{Name: "scoped.event"},
		catalog.WithScopeAppID("app-123"),
		catalog.WithMetadata(map[string]string{"key": "value"}),
	)
	if err != nil {
		t.Fatal(err)
	}
	if et.ScopeAppID != "app-123" {
		t.Fatalf("expected app-123, got %q", et.ScopeAppID)
	}
	if et.Metadata["key"] != "value" {
		t.Fatal("expected metadata")
	}
}

// Package relay provides a composable webhook delivery engine for Go.
//
// Relay is a library — not a service. Import it into your application to get
// tenant-scoped webhook endpoints, dynamic event type definitions, guaranteed
// delivery with signature verification, and replay capabilities.
//
// Key features:
//   - Dynamic, persisted webhook definitions with JSON Schema validation
//   - Composable store pattern with multiple backends (Postgres, Bun, SQLite, Redis, Memory)
//   - HMAC-SHA256 signature verification on every delivery
//   - Exponential backoff retries with dead letter queue
//   - Per-endpoint rate limiting
//   - Forge-native with standalone fallback
//
// Quick start:
//
//	r, err := relay.New(
//	    relay.WithStore(memoryStore),
//	)
//	if err != nil {
//	    log.Fatal(err)
//	}
//
//	r.RegisterEventType(catalog.WebhookDefinition{
//	    Name:    "invoice.created",
//	    Version: "2025-01-01",
//	})
//
//	r.Send(ctx, &event.Event{
//	    Type:     "invoice.created",
//	    TenantID: "tenant_123",
//	    Data:     map[string]any{"invoice_id": "inv_01h..."},
//	})
package relay

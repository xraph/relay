// Example: basic usage of Relay with an in-memory store.
//
// Demonstrates:
//   - Creating a Relay instance with a memory store
//   - Registering an event type in the catalog
//   - Creating a webhook endpoint
//   - Sending an event (which fans out to matching endpoints)
//   - Starting and stopping the delivery engine
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"time"

	relay "github.com/xraph/relay"
	"github.com/xraph/relay/catalog"
	"github.com/xraph/relay/endpoint"
	"github.com/xraph/relay/event"
	"github.com/xraph/relay/store/memory"
)

func main() {
	ctx := context.Background()
	logger := slog.Default()

	// 1. Create a mock webhook receiver.
	receiver := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Println("Webhook received!")
		fmt.Println("  Signature:", r.Header.Get("X-Relay-Signature"))
		fmt.Println("  Event ID:", r.Header.Get("X-Relay-Event-ID"))
		w.WriteHeader(http.StatusOK)
	}))
	defer receiver.Close()

	// 2. Create Relay with memory store.
	r, err := relay.New(
		relay.WithStore(memory.New()),
		relay.WithLogger(logger),
	)
	if err != nil {
		log.Fatal(err)
	}

	// 3. Register an event type.
	_, err = r.RegisterEventType(ctx, catalog.WebhookDefinition{
		Name:        "order.created",
		Description: "Fired when a new order is placed",
		Version:     "2025-01-01",
	})
	if err != nil {
		log.Fatal(err)
	}
	fmt.Println("Registered event type: order.created")

	// 4. Create a webhook endpoint.
	ep, err := r.Endpoints().Create(ctx, endpoint.Input{
		TenantID:   "tenant-acme",
		URL:        receiver.URL,
		EventTypes: []string{"order.*"},
	})
	if err != nil {
		log.Fatal(err)
	}
	fmt.Println("Created endpoint:", ep.ID, "->", ep.URL)

	// 5. Send an event.
	err = r.Send(ctx, &event.Event{
		Type:     "order.created",
		TenantID: "tenant-acme",
		Data:     json.RawMessage(`{"order_id":"ORD-001","amount":99.99}`),
	})
	if err != nil {
		log.Fatal(err)
	}
	fmt.Println("Event sent!")

	// 6. Start the delivery engine.
	r.Start(ctx)
	fmt.Println("Delivery engine started")

	// Wait for the webhook to be delivered.
	time.Sleep(3 * time.Second)

	// 7. Stop the engine gracefully.
	r.Stop(ctx)
	fmt.Println("Done!")
}

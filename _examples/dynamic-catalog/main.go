// Example: dynamic event type catalog with the admin API.
//
// Demonstrates:
//   - Mounting the Relay admin API on an HTTP server
//   - Registering event types at runtime via the API
//   - Listing and querying event types
package main

import (
	"context"
	"fmt"
	"log"
	"log/slog"
	"net/http"

	relay "github.com/xraph/relay"
	"github.com/xraph/relay/api"
	"github.com/xraph/relay/store/memory"
)

func main() {
	ctx := context.Background()
	logger := slog.Default()

	// 1. Create Relay with memory store.
	r, err := relay.New(
		relay.WithStore(memory.New()),
		relay.WithLogger(logger),
	)
	if err != nil {
		log.Fatal(err)
	}

	// 2. Start the delivery engine.
	r.Start(ctx)
	defer r.Stop(ctx)

	// 3. Create the admin API handler.
	handler := api.NewHandler(r.Store(), r.Catalog(), r.Endpoints(), r.DLQ(), logger)

	// 4. Mount under /webhooks prefix.
	mux := http.NewServeMux()
	mux.Handle("/webhooks/", http.StripPrefix("/webhooks", handler))

	// 5. Start the HTTP server.
	addr := ":8080"
	fmt.Println("Admin API available at http://localhost" + addr + "/webhooks/")
	fmt.Println()
	fmt.Println("Try:")
	fmt.Println("  curl -X POST http://localhost:8080/webhooks/event-types \\")
	fmt.Println("    -H 'Content-Type: application/json' \\")
	fmt.Println("    -d '{\"name\":\"invoice.created\",\"description\":\"New invoice\",\"version\":\"2025-01-01\"}'")
	fmt.Println()
	fmt.Println("  curl http://localhost:8080/webhooks/event-types")
	fmt.Println()
	fmt.Println("  curl http://localhost:8080/webhooks/stats")

	if err := http.ListenAndServe(addr, mux); err != nil {
		log.Fatal(err)
	}
}

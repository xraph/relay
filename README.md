# Relay

Composable webhook delivery engine for Go.

Relay is a **library** — not a service. Import it into your Go application to get tenant-scoped webhook endpoints, dynamic event type definitions, guaranteed delivery with signature verification, and replay capabilities.

## Features

- **Dynamic webhook definitions** — Register event types at runtime with optional JSON Schema validation
- **Composable store pattern** — Plug in PostgreSQL, SQLite, Redis, MongoDB, or in-memory backends. Implement the `store.Store` interface for anything else.
- **HMAC-SHA256 signatures** — Every delivery is signed. Receivers verify authenticity using the `signature` package.
- **Exponential backoff retries** — Configurable schedule (default: 5s → 30s → 2m → 15m → 2h). Failed deliveries land in the dead letter queue.
- **Per-endpoint rate limiting** — Token bucket limiter prevents overloading downstream services
- **Admin HTTP API** — Full CRUD for event types, endpoints, events, deliveries, and DLQ replay
- **OpenTelemetry + Prometheus** — Traces per delivery span, counters, latency histograms, and gauges out of the box
- **Multi-tenant by default** — Every endpoint and event is scoped to a tenant ID

## Install

```bash
go get github.com/xraph/relay
```

Requires Go 1.22 or later.

## Quick Start

```go
package main

import (
    "context"
    "encoding/json"
    "log"

    "github.com/xraph/relay"
    "github.com/xraph/relay/catalog"
    "github.com/xraph/relay/endpoint"
    "github.com/xraph/relay/event"
    "github.com/xraph/relay/store/memory"
)

func main() {
    ctx := context.Background()

    // 1. Create a Relay instance with a store backend.
    r, err := relay.New(
        relay.WithStore(memory.New()),
    )
    if err != nil {
        log.Fatal(err)
    }

    // 2. Register an event type in the catalog.
    r.RegisterEventType(ctx, catalog.WebhookDefinition{
        Name:        "order.created",
        Description: "Fired when a new order is placed",
        Version:     "2025-01-01",
    })

    // 3. Create a webhook endpoint for a tenant.
    r.Endpoints().Create(ctx, endpoint.Input{
        TenantID:   "tenant-acme",
        URL:        "https://acme.example.com/webhook",
        EventTypes: []string{"order.*"},   // glob pattern
    })

    // 4. Send an event — Relay fans out to all matching endpoints.
    r.Send(ctx, &event.Event{
        Type:     "order.created",
        TenantID: "tenant-acme",
        Data:     json.RawMessage(`{"order_id":"ORD-001","amount":99.99}`),
    })

    // 5. Start the delivery engine and stop gracefully.
    r.Start(ctx)
    defer r.Stop(ctx)
}
```

## Configuration

All options are set via functional options on `relay.New()`:

| Option | Default | Description |
|--------|---------|-------------|
| `WithStore(s)` | *required* | Persistence backend (`memory.New()`, `postgres.New(db)`, `sqlite.New(db)`, `redis.New(kv)`, `mongo.New(db)`) |
| `WithLogger(l)` | `slog.Default()` | Structured logger |
| `WithConcurrency(n)` | `10` | Delivery worker goroutines |
| `WithPollInterval(d)` | `1s` | How often the engine checks for pending deliveries |
| `WithBatchSize(n)` | `50` | Max deliveries dequeued per poll cycle |
| `WithRequestTimeout(d)` | `30s` | HTTP timeout per delivery attempt |
| `WithMaxRetries(n)` | `5` | Maximum delivery attempts before moving to DLQ |
| `WithRetrySchedule(s)` | `5s, 30s, 2m, 15m, 2h` | Backoff intervals between retries |
| `WithShutdownTimeout(d)` | `30s` | Grace period for in-flight deliveries on shutdown |
| `WithCacheTTL(d)` | `30s` | Catalog in-memory cache TTL |

## Webhook Verification

Receivers verify incoming webhooks using the `signature` package:

```go
import "github.com/xraph/relay/signature"

func handleWebhook(w http.ResponseWriter, r *http.Request) {
    body, _ := io.ReadAll(r.Body)

    sig := r.Header.Get("X-Relay-Signature")       // "v1=<hex>"
    ts, _ := strconv.ParseInt(r.Header.Get("X-Relay-Timestamp"), 10, 64)

    if !signature.Verify(body, endpointSecret, ts, sig) {
        http.Error(w, "invalid signature", http.StatusUnauthorized)
        return
    }

    // Process the verified webhook...
}
```

Every delivery includes these headers:

| Header | Description |
|--------|-------------|
| `X-Relay-Signature` | `v1=<hmac-sha256-hex>` computed over `timestamp.body` |
| `X-Relay-Timestamp` | Unix timestamp (seconds) of the delivery attempt |
| `X-Relay-Event-ID` | The event's TypeID (e.g. `evt_01h6rz...`) |
| `Content-Type` | `application/json` |

## Admin API

Mount the admin HTTP handler to manage webhooks at runtime:

```go
import "github.com/xraph/relay/api"

handler := api.NewHandler(r.Store(), r.Catalog(), r.Endpoints(), r.DLQ(), logger)
mux.Handle("/webhooks/", http.StripPrefix("/webhooks", handler))
```

### Routes

| Method | Path | Description |
|--------|------|-------------|
| POST | `/event-types` | Register an event type |
| GET | `/event-types` | List event types |
| GET | `/event-types/{name}` | Get event type by name |
| DELETE | `/event-types/{name}` | Deprecate an event type |
| POST | `/endpoints` | Create an endpoint |
| GET | `/endpoints` | List endpoints |
| GET | `/endpoints/{id}` | Get endpoint |
| PUT | `/endpoints/{id}` | Update endpoint |
| DELETE | `/endpoints/{id}` | Delete endpoint |
| PATCH | `/endpoints/{id}/enable` | Enable endpoint |
| PATCH | `/endpoints/{id}/disable` | Disable endpoint |
| POST | `/endpoints/{id}/rotate-secret` | Rotate signing secret |
| GET | `/endpoints/{id}/deliveries` | List deliveries for endpoint |
| POST | `/events` | Create an event |
| GET | `/events` | List events |
| GET | `/events/{id}` | Get event |
| GET | `/dlq` | List DLQ entries |
| POST | `/dlq/{id}/replay` | Replay a single DLQ entry |
| POST | `/dlq/replay` | Bulk replay DLQ entries |
| GET | `/stats` | Get delivery statistics |

## Store Backends

### Memory (testing)

```go
import "github.com/xraph/relay/store/memory"

r, _ := relay.New(relay.WithStore(memory.New()))
```

### PostgreSQL

```go
import (
    "github.com/xraph/grove"
    "github.com/xraph/grove/drivers/pgdriver"
    "github.com/xraph/relay/store/postgres"
)

pgdb := pgdriver.New()
pgdb.Open(ctx, "postgres://localhost:5432/mydb?sslmode=disable")

db, _ := grove.Open(pgdb)
store := postgres.New(db)
store.Migrate(ctx)  // creates relay_* tables

r, _ := relay.New(relay.WithStore(store))
```

### SQLite

```go
import (
    "github.com/xraph/grove"
    "github.com/xraph/grove/drivers/sqlitedriver"
    "github.com/xraph/relay/store/sqlite"
)

sdb := sqlitedriver.New()
sdb.Open(ctx, "relay.db")

db, _ := grove.Open(sdb)
store := sqlite.New(db)
store.Migrate(ctx)  // creates relay_* tables

r, _ := relay.New(relay.WithStore(store))
```

### Redis

```go
import (
    "github.com/xraph/grove/kv"
    "github.com/xraph/grove/kv/drivers/redisdriver"
    redisstore "github.com/xraph/relay/store/redis"
)

rdb := redisdriver.New("redis://localhost:6379")
kvStore, _ := kv.New(rdb)

store := redisstore.New(kvStore)

r, _ := relay.New(relay.WithStore(store))
```

### MongoDB

```go
import (
    "github.com/xraph/grove"
    "github.com/xraph/grove/drivers/mongodriver"
    "github.com/xraph/relay/store/mongo"
)

mdb := mongodriver.New()
mdb.Open(ctx, "mongodb://localhost:27017/relay")

db, _ := grove.Open(mdb)
store := mongo.New(db)
store.Migrate(ctx)  // creates indexes

r, _ := relay.New(relay.WithStore(store))
```

## Package Index

| Package | Description |
|---------|-------------|
| `relay` | Root package — `Relay` engine, `Send()`, `Start()`/`Stop()`, functional options |
| `catalog` | Event type registry with in-memory cache and JSON Schema validation |
| `endpoint` | Webhook endpoint CRUD service with secret rotation |
| `event` | Event entity and store interface |
| `delivery` | Delivery engine, HTTP sender, retry logic with exponential backoff |
| `dlq` | Dead letter queue with replay and bulk operations |
| `id` | TypeID-based identity — single `ID` struct with prefix constants |
| `signature` | HMAC-SHA256 signing and verification |
| `ratelimit` | Token bucket rate limiter per endpoint |
| `observability` | Prometheus metrics and OpenTelemetry tracing |
| `api` | HTTP admin API handlers (Go 1.22+ ServeMux) |
| `store` | Composite `Store` interface (catalog + endpoint + event + delivery + dlq) |
| `store/memory` | In-memory store for testing |
| `store/postgres` | PostgreSQL backend using Grove ORM |
| `store/sqlite` | SQLite backend for embedded/edge deployments |
| `store/redis` | Redis backend using Grove KV |
| `store/mongo` | MongoDB backend |
| `extension` | Forge framework extension integration |
| `scope` | Multi-tenant context helpers |

## Architecture

```
┌─────────────────────────────────────────────┐
│                  relay.Relay                 │
│  Send() → validate → persist → fan-out      │
│  Start() / Stop()                           │
├────────────┬────────────┬───────────────────┤
│  Catalog   │  Endpoint  │  Delivery Engine  │
│  (cache +  │  Service   │  (workers + poll  │
│  validate) │  (CRUD)    │   + retry + DLQ)  │
├────────────┴────────────┴───────────────────┤
│              store.Store                     │
│  (catalog + endpoint + event + delivery +   │
│   dlq interfaces composed)                  │
├────────┬────────┬───────┬───────┬─────────────┤
│Postgres│ SQLite │ Redis │ Mongo │   Memory    │
│ (Grove)│(Grove) │(KV)   │(Grove)│(testing)    │
└────────┴────────┴───────┴───────┴─────────────┘
```

## Examples

See the [`_examples/`](./_examples/) directory:

- **[basic](./_examples/basic/)** — Memory store, register type, create endpoint, send event, start engine
- **[dynamic-catalog](./_examples/dynamic-catalog/)** — Mount admin API, register event types at runtime
- **[stripe-style](./_examples/stripe-style/)** — Webhook receiver with HMAC-SHA256 signature verification

## License

See [LICENSE](LICENSE) for details.

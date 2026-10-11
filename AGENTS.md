# Relay

Relay is a webhook delivery engine for Go. You register event types (with JSON Schema validation), tenants register endpoints, and relay signs every delivery with HMAC-SHA256, retries with exponential backoff, rate-limits per endpoint and parks permanent failures in a dead letter queue you can replay from. You import it as a library, and there's a Forge extension for when you want it mounted in a Forge app.

Module path: `github.com/xraph/relay`. One `go.mod` covers the whole repo. `_examples/` sits inside that module, but Go skips directories whose names start with an underscore, so nothing you run with `./...` builds or tests it.

## Commands

```sh
GOWORK=off go test ./...                  # starts Postgres, Mongo and Redis containers if Docker is up
GOWORK=off go test -short ./...           # skips every container-backed test
GOWORK=off go test -race -count=1 ./...   # what release.yml runs
GOWORK=off go vet ./...
golangci-lint run ./...                   # v2, configured by .golangci.yml
gofmt -s -w . && goimports -w -local github.com/xraph/relay .
```

If you'd rather use make, the Makefile wraps the same things as `make test` (`go test -v ./...`), `make test-race`, `make vet`, `make lint`, `make lint-fix`, `make fmt`, `make check` and `make coverage`. Don't bother with `make build`, `make run`, `make install`, `make dev` or `make all`: they target `./cmd/relay`, which doesn't exist.

CI is `.github/workflows/ci.yml`, a thin caller of the shared `xraph/workflows` `go-ci.yml@v1`. Every push to `main` and every pull request gets `make test`, a race-enabled coverage pass, golangci-lint at the version that workflow pins, `gofmt -l .`, `make vet`, gosec, govulncheck, and a tidiness check that runs `go mod tidy` and fails if `go.mod` or `go.sum` changed. Gosec and govulncheck findings fail the build. `ci.yml` adds a Format job (`goimports -l -local github.com/xraph/relay .`) and a Docs job that runs `pnpm install --frozen-lockfile`, `pnpm types:check`, `pnpm lint` and `pnpm build` in `docs/`.

## Tests that need Docker

You don't set any environment variables for relay's tests. The Postgres, Mongo and Redis store tests start their own containers through testcontainers-go, using helpers in `store/postgres/wake_test.go` (`startPostgres`), `store/mongo/dequeue_test.go` (`startMongo`) and `store/redis/wake_test.go` (`startRedis`). The image tags are pinned in those files, and each container gets memory and CPU caps.

Those helpers skip in two cases: under `-short`, and when no container runtime answers (`t.Skipf("container runtime unavailable: ...")`). Watch the second one. Run `go test ./...` with Docker stopped and it passes, having quietly dropped nearly every test in `store/postgres`, `store/mongo` and `store/redis`, so if you touched one of those backends you should read the `-v` output for skips before you believe the green. CI runs `make test` without `-short` on runners that have Docker, so the containers do run there.

Memory and SQLite always run in-process and need nothing from you. SQLite tests open a fresh file under `t.TempDir()` per subtest, because with a connection pool every connection to `:memory:` would get its own empty database.

`store/storetest` holds the cross-backend conformance suites: `RunReplaySuite`, `RunEndpointSuite`, `RunEngineSuite`, `RunDeliverySuite`, `RunEventPageSuite`, `RunDLQPageSuite` and `RunAcceptanceSuite`. All five backends run the first six from their `conformance_test.go`. `RunAcceptanceSuite` runs only for memory and Postgres (`acceptance_test.go`), the two backends that implement reliable acceptance. When you add store behaviour, put its assertions in `storetest`. Then every backend gets checked.

## Layout

| Path | What lives there |
|---|---|
| root (`package relay`) | `Relay`, `New` and its options, `Send`, `SendReliable` (`reliable.go`), sentinel errors, `Config` |
| `catalog/` | The event type registry and JSON Schema validation |
| `endpoint/`, `event/`, `delivery/`, `dlq/` | One subsystem each, with its entities and its own `Store` interface. `delivery/` also holds the engine, `dlq/` the replay service |
| `store/` | `store.Store`, which embeds the subsystem stores plus `Migrate`, `Ping`, `Close`, and the optional `WakeNotifier` and `AtomicSender` capabilities |
| `store/memory`, `store/sqlite`, `store/postgres`, `store/mongo`, `store/redis` | The five backends |
| `store/storetest` | Conformance suites every backend runs |
| `acceptance/` | The reliable acceptance protocol. `RELIABILITY.md` describes it from the caller's side |
| `signature/` | HMAC-SHA256 signing and verification, `GenerateSecret` |
| `ratelimit/`, `observability/`, `scope/` | Per-endpoint token buckets, OpenTelemetry tracing and metrics, tenant scope capture and restore |
| `api/` | Admin HTTP handlers (`ForgeAPI`) |
| `extension/` | The Forge extension. `extension/contract` wires the dashboard contract (`manifest.yaml`) |
| `id/` | TypeID identifiers |
| `internal/` | `errs` (shared sentinels), `cursor` (list cursors), `entity`, `acceptanceutil`, `acceptanceobs` |
| `docs/` | Fumadocs site (Next.js, pnpm). `MIGRATION.md` covers the dashboard's move from templ to the React shell |

## Conventions

Sentinel errors live in `errors.go`, prefixed `relay: `. A few have to be defined lower down, because subpackages can't import the root without a cycle: `internal/errs` holds `ErrEndpointNotFound`, `ErrEventNotFound` and `ErrInvalidFilter`, `dlq` holds `ErrAlreadyReplayed`, and `errors.go` aliases each one, so `errors.Is` works with either name. If a subpackage needs a new shared sentinel, define it in `internal/errs` and alias it in `errors.go`. Backends wrap with a `relay/<backend>: <op>: %w` prefix (`relay/redis: purge attempt: %w`) and turn a miss into the root sentinel: `isNoRows` in `store/postgres/store.go`, `kv.ErrNotFound` in Redis, `ErrNoDocuments` in Mongo.

IDs come from `id/`. There's one `ID` struct over a TypeID, with a prefix per entity (`evtype`, `ep`, `evt`, `del`, `dlq`, `att`, `whsec`). You generate with `id.NewEndpointID()` and parse with `id.ParseEndpointID(s)`, and each entity has the same pair.

Each backend asserts `var _ relaystore.Store = (*Store)(nil)` (Mongo spells it `store.Store`), so a method you add to a subsystem interface breaks the build until all five implement it. Optional capabilities are separate interfaces that relay finds with a type assertion. `store.WakeNotifier` is implemented by Postgres (LISTEN/NOTIFY) and Redis. Treat it as a latency optimisation: polling is what keeps delivery correct, so a missed wake only delays work until the next poll. `acceptance.Store` is implemented by memory and Postgres, and `SendReliable` returns `acceptance.ErrUnsupported` on any other backend. Migrations are a grove `migrate.NewGroup("relay")` in the postgres, sqlite and mongo `migrations.go`.

Backends take no options (`postgres.New(db)`, `redis.New(kvStore)`). Relay itself uses `type Option func(*Relay) error`, so an option can fail `New`. The extension has `type ExtOption func(*Extension)`, and `WithRelayOption` passes a root option through.

The Forge extension is `extension.New(opts...)`, embedding `*forge.BaseExtension`. `Register` loads config from `extensions.relay` (falling back to the legacy `relay` key), calls `Init`, then publishes `*relay.Relay` with `vessel.Provide`. `Init` picks the store: a named `*grove.DB` under `WithGroveDatabase`, a named `*kv.Store` with the Redis store under `WithGroveKV`, otherwise whatever default `*grove.DB` the container has. It wires metrics through `fapp.Metrics()`, runs `Migrate` unless you disabled it, and mounts the API under `BasePath` (`/relay` by default). `Start` and `Stop` run the delivery engine. Herald's webhook driver and dispatch's `relay_hook` both take a `*relay.Relay`, so its exported methods are what they build against.

Logging goes through `github.com/xraph/go-utils/log`. If you don't pass a logger, you get `log.NewNoopLogger()`. Fields are typed (`log.String`, `log.Any("error", err)`), and inside the extension you use `e.Logger()` with `forge.F`.

Tests use the standard `testing` package. Testify shows up in `go.mod` only as an indirect dependency.

### Lint rules that bite

- `govet` runs with `enable-all` (minus `fieldalignment`), which turns on `shadow`, and the gocritic `diagnostic` tag brings in `sloppyReassign`. Between them, an inner `err :=` that shadows an outer one fails, and so does `if err = f(); err != nil`. Give the inner error a name of its own.
- `errcheck` has `check-blank` and `check-type-assertions` on. Beyond the `std-error-handling` preset, only `Close` and the pgx `Rollback` are excused. A best-effort call that drops its error carries `//nolint:errcheck` with a reason, the way `store/redis/wake.go` does.
- `.golangci.yml` excludes gosec G104, G117, G304, G402 and G704. CI's standalone gosec job excludes only G115, and gosec never reads `//nolint`. If gosec raises a false positive outside G115, you need `// #nosec Gxxx` followed by the reason.
- `goimports` groups `github.com/xraph/relay/...` imports last. `revive` wants proper receiver names, lowercase error strings and `_` for unused parameters. `nolintlint` reports a `//nolint` that suppresses nothing.
- `_test.go` files are exempt from gosec, errcheck and gocritic, and `_examples/` from errcheck and gosec.

## Dependencies

Relay depends on grove (`grove`, `drivers/mongodriver`, `drivers/pgdriver`, `drivers/sqlitedriver`, `kv`, `kv/drivers/redisdriver`) and forge, plus `go-utils` and `vessel`. Herald (`drivers/webhook`), dispatch and authsome consume it. Every version is in `go.mod`.

Grove and forge tag all their submodules with one version, so you bump every line from either repo together:

```sh
GOWORK=off go get github.com/xraph/grove@vX.Y.Z github.com/xraph/grove/kv@vX.Y.Z  # and every other grove line
GOWORK=off go get github.com/xraph/forge@vA.B.C
GOWORK=off go mod tidy
GOWORK=off go test ./...
```

Relay is a single module, so plain `go mod tidy` is all the tidying there is. Keep `GOWORK=off` on your Go commands so a `go.work` above the checkout can't change what resolves (CI has none). Never commit a `go.work`, and never commit a `replace` pointing at a sibling checkout. It works on your machine and breaks `go get` for everyone else.

## Releasing

You release by dispatching the workflow, never by pushing a tag yourself.

```sh
gh run list --workflow ci.yml --branch main --limit 1   # wait for green
gh workflow run release.yml --ref main -f tag=vX.Y.Z
```

`release.yml` creates the tag on the dispatched commit and pushes it straight away. Then it runs `go mod download`, `go build ./...` and `go test -race -count=1 ./...`, and only if those pass does it publish the GitHub release, with notes built from `git log` since the previous tag. Since the tag is already public when those steps run, a failure there means you fix forward with the next patch version.

Release relay after the grove and forge versions it needs are out and pinned here. Herald, dispatch and authsome re-pin afterwards.

Commit subjects mostly use conventional prefixes with a scope: `feat(delivery):`, `fix(redis):`, `chore(deps):`.

## Branch rules

The `main` ruleset blocks deletion and force-push and nothing else, so you can push to `main` directly. It has no required checks and no signed-commit rule.

# Reliable event acceptance

Use `Relay.SendReliable(ctx, acceptance.Request{...})` when your producer retries
an unknown outcome. Memory and PostgreSQL implement this capability. PostgreSQL
is the persistent production backend; memory loses its state when the process
exits. Other backends return `acceptance.ErrUnsupported` from this API.

You must supply an authorized producer, installation ID, source key, source
fingerprint, app ID and tenant ID. Org ID can be empty for an app-owned tenant.
These are trusted host inputs, not authentication credentials. Map your source's
persisted tenant to `TenantID`; don't substitute a routing namespace. Relay's
context scope helpers do not establish ownership.

```go
receipt, err := relayEngine.SendReliable(ctx, acceptance.Request{
    Producer:          "orders",
    InstallationID:    installationID,
    SourceKey:         sourceDeliveryID,
    SourceFingerprint: sourceEnvelopeSHA256,
    AppID:             appID,
    OrgID:             orgID,
    TenantID:          tenantID,
    Type:              "order.created",
    Data:              json.RawMessage(`{"order_id":"order-42"}`),
})
```

Keep the request unchanged when you retry. `(Producer, InstallationID,
SourceKey)` identifies one acceptance. App/org/tenant, event type, payload and
source fingerprint are immutable bindings. A changed binding returns
`acceptance.ErrConflict` without the original receipt. Another installation can
reuse the same key. Source fingerprints must be lowercase SHA-256 hex strings;
Relay binds them as source evidence and computes its own semantic fingerprint.
It cannot verify a source envelope that you haven't supplied.

`acceptance.Fingerprint(request)` computes the expected version 1 semantic hash,
and `receipt.Verify(request)` checks that binding. Version 1 hashes the JSON
encoding of the normalized request with an explicit version field. Object keys
are sorted, exact decimal values retain all digits, equivalent numeric forms
normalize together, and duplicate keys, invalid UTF-8, unpaired Unicode
surrogates and NUL escapes are rejected. Requests allow at most 1 MiB of JSON,
64 nested containers, 256 bytes per identifier and decimal exponents from
-10000 to 10000. Acceptance allows at most 1000 enabled endpoint candidates in
the exact scope, including candidates whose subscriptions don't match.

The receipt pins the event ID, acceptance time, endpoint membership and delivery
IDs. An empty recipient list is valid. Acceptance persists the event, complete
fanout and receipt together, so an error cannot leave a successful receipt with
partial fanout. Memory prepares fallible work before changing its maps under one
lock. PostgreSQL serializes source identity with a transaction advisory lock,
checks the exact receipt key, selects scoped recipients and commits all writes
in that transaction. Hash collisions in the advisory lock only serialize work;
they do not merge identities.

Receipts are recovered before catalog and endpoint validation. A retry cannot
append newly registered endpoints or lose its receipt because an event type was
deprecated. Receipts have no automatic TTL and no foreign key to event payloads.
You must retain them independently when you archive or erase event data. A retry
after payload removal returns the receipt and does not recreate the payload.
Removing a receipt manually reopens acceptance and is outside this guarantee.

Endpoint configuration is read at delivery time. Changing a URL or signing
secret affects delivery; deleting an endpoint can cause delivery failure. The
receipt does not pin those settings. Inspect delivery and attempt records for
actual delivery status. Retain event payloads while deliveries still need them.
HTTP retries can produce duplicate external effects, so receivers still need
idempotency. Acceptance does not promise exactly-once HTTP delivery.

Engine wakes happen after commit. PostgreSQL notification failure does not undo
acceptance, and polling remains the recovery mechanism. An uncertain commit
error requires retrying the same request to recover the stored receipt.

## Migration and legacy calls

Run the normal Relay migrations before serving traffic. Migration
`20261009000001` creates `relay_acceptances` and expands `relay_endpoints` with
app/org ownership columns and an index. Existing endpoint rows retain empty
app/org values. Those values are exact values, never wildcards, so you must
assign trusted ownership before scoped reliable requests can select those rows.
Endpoint create, read and update preserve this scope in PostgreSQL.

Legacy `Send` uses atomic event and fanout writes on memory and PostgreSQL. It
keeps its existing key-only no-op semantics, catalog validation order and
tenant-only recipient selection. It does not provide a retained receipt or
content conflict detection. Existing event rows created before this upgrade are
not repaired by retrying a legacy key. MongoDB, Redis and SQLite keep the legacy
persist-then-fanout path and do not support reliable acceptance.

## Verification

Run `go test -short ./...` for the local suite and
`go test -race ./store/postgres -run '^TestReliableAcceptance' -count=1 -v` for
PostgreSQL qualification. Integration tests launch disposable PostgreSQL 16
containers capped at 256 MiB and one CPU; inspect the output for actual execution
because the shared container helper skips when Docker is unavailable.

Coverage includes concurrent identical and conflicting first requests, lost
commit acknowledgements, exact scope selection, endpoint membership changes
during acceptance, catalog changes, empty fanout and retained receipts after
payload removal. PostgreSQL triggers inject failures at event, delivery,
receipt and deferred commit stages. Resolution failure is also checked. These
tests establish local acceptance, not external webhook delivery or an assembled
publisher deployment.

# Dashboard migration: templ to the React shell

Relay's dashboard used to render server-side with templ and ForgeUI, from
`relay/dashboard/`. It now lives in the Forge dashboard's React shell as
`@forge-go/dashboard-plugin-relay`, reading the `relay` contract contributor in
`extension/contract`. The templ package is gone.

This file is the record of that move. It was written by walking every templ
page before deleting it, because once the directory is gone there is nothing
left to check against, and anything not written down here is a feature that
went missing by accident. Every page, column, action, filter, badge and empty
state is listed, and each one says whether it moved, changed, or was dropped,
and why.

## What you need to do

If you ran the templ dashboard through `DashboardAware`, you don't need to do
anything in Relay: the extension registers the contract contributor through
`ContractContributorAware`, and the shell finds it. Add the plugin to your
shell:

```tsx
import relayPlugin from "@forge-go/dashboard-plugin-relay"

const plugins = [corePlugin, relayPlugin]
```

The plugin's pages use Tailwind classes of their own, so the shell's stylesheet
has to scan the package. If your shell declares its sources with `@source`, add
`@source "<path to>/packages/plugin-relay/src";` next to the others.

Run `Migrate` on your store after upgrading. It adds the delivery attempts
table, copies event type and tenant onto existing deliveries, replaces mongo's
idempotency index, and on redis builds the global delivery index. The delivery
log refuses to list anything on redis until that index exists, rather than
showing an empty log that looks like "nothing was sent".

## Bugs found on the way

Reading the domain closely enough to build the React pages turned up bugs the
templ pages had been hiding. All of them are fixed.

- An empty signing key produced a signature that verified, so a delivery could
  be forged.
- DLQ replay left `MaxAttempts` at 0 on postgres, sqlite and redis, so a
  replayed delivery went straight back to the DLQ with no retries. Two replays
  racing each other could both send.
- `ListEndpoints` with an empty tenant matched the tenant literally on every
  backend and returned nothing. The templ overview, endpoints page, both widgets
  and the whole deliveries page were empty for that reason.
- A delivery the engine decided to retry was written back in the state
  `Dequeue` gave it (`delivering`, or `delivered` on redis) and never picked up
  again. On every persistent backend a 5xx or a timeout silently ended the
  delivery. Only the in-memory store, which the engine's tests used, retried.
- Mongo's idempotency index was sparse, but an event with no key is stored with
  `""`, so every un-keyed event after the first was refused as a duplicate, and
  `relay.Send` reports a duplicate as success. The events were dropped while the
  caller was told they went through.
- `RateLimit` on an endpoint was documented and settable and never enforced.
- Redis marked a claimed delivery `delivered` before its attempt had been made.
- The memory store purged dead letters by `created_at` where every other
  backend used `failed_at`.

## Page by page

Status is one of **migrated** (same thing, same place), **changed** (the
behaviour is different on purpose, with the reason), **dropped** (gone, with
the reason) or **blocked** (wanted, and waiting on work outside Relay).

### Overview

| templ | React | status |
|---|---|---|
| Four counters: event types, endpoints, pending, dead letters | same four, `overview.stats` | migrated |
| Recent events card | not on the overview; the Events page is one click away | changed: the overview leads with what went wrong |
| Recent deliveries card, which was not sorted by recency | Latest failures: the five newest failed deliveries | changed: someone opening the overview is looking for failures, and the old card showed deliveries in store order |
| Empty states "No events yet", "No deliveries yet" | "No failed deliveries." | changed with the card |

### Deliveries

| templ | React | status |
|---|---|---|
| List of deliveries, from each endpoint's own list, fanned out and merged in the page | `deliveries.list`, one query over the whole log | changed: the fan-out skipped and repeated rows and was wrong on an empty tenant |
| State filter: All, Pending, Delivered, Failed | State filter: All, Queued or retrying, Delivered, Failed | migrated |
| No other filters | Tenant, response class (2xx, 4xx, 5xx, no response), endpoint, event type, created window | new |
| No paging | Cursor paging, 50 a page, Previous and Next | new |
| Columns ID, Event, Endpoint (both truncated ids), State, Attempts, Last Error, Created | State, Event type (links to the delivery), Endpoint URL, Response, Attempts n of max, Tenant, Created | changed: the ids told you nothing without a second lookup. The last error is on the delivery's page, attempt by attempt |
| State badges: delivered default, failed destructive, others secondary | Delivered outline, Queued secondary, Retrying default, Sending secondary, Failed destructive | changed: the ramp goes by proportion, so the healthy majority is quietest and only failures are destructive. Pending is split in two |
| Empty state "No deliveries found" | three: nothing sent yet, nothing matches these filters, or nothing found in the part searched so far (redis only) | changed: an empty filtered log is not an empty log, and a search that stopped early is not a search that found nothing |

### Delivery detail

| templ | React | status |
|---|---|---|
| Delivery ID, State, Event ID, Endpoint ID, Attempts, Created, Completed | same, with the event and endpoint as links | migrated |
| Next attempt | shown on the retry sequence as the attempt that is due | changed |
| Updated | dropped | dropped: the retry sequence times every change that matters |
| Last Attempt card: HTTP status, latency, response body | the retry sequence: every attempt with its status, latency, error and response body, the wait between attempts, and why it stopped | changed: only the last attempt was ever recorded before. Attempts are a table now |
| Buttons to the event and the endpoint | links in the aside | migrated |

### Endpoints

| templ | React | status |
|---|---|---|
| Columns URL, Description, Events, Status, Tenant, Created | URL, Tenant, Event types, State, Signing, Rate limit, Created | changed: signing and rate limit were invisible, and an unsigned endpoint is worth seeing on the list |
| Tenant filter, as you type | same | migrated |
| No state filter | Enabled or disabled | new |
| Create Endpoint button | New endpoint | migrated |
| Empty state "No endpoints found" | "No endpoints yet" and "No endpoints match these filters" | changed |

### Endpoint detail

| templ | React | status |
|---|---|---|
| ID, tenant, URL, enabled, rate limit, created, updated, app scope, org scope | same | migrated |
| Event subscriptions, custom headers, metadata | same | migrated |
| Enable and disable | same | migrated |
| Rotate secret, with no confirmation, and the new secret never shown | confirms first, says what breaks at the receiver, and shows the new secret once | changed: a rotation you cannot see the result of is a rotation you cannot finish |
| No edit, no delete | Edit, Delete (confirmed, then back to the list) | new |
| Recent deliveries, 20 | same, 20, with a link to the log | migrated |
| Empty state "No deliveries yet." | "Nothing has been sent to this endpoint yet." | migrated |

### Endpoint create

| templ | React | status |
|---|---|---|
| Tenant, URL, description, rate limit, headers, metadata | same | migrated |
| Event types: a multi-select from the catalog plus free text | free text, one pattern per line or comma separated, with a line under the field saying what each pattern matches in the catalog | changed: a pattern is not a type, and the old picker could not express `invoice.*`. The match line keeps the catalog in view |
| Errors as a page message | the message names the field, the field is marked, and focus moves to it | changed |

### Events

| templ | React | status |
|---|---|---|
| Columns ID, Type, Tenant, Idempotency Key, Created | Type (links to the event), Tenant, Event ID, Sent | changed: the idempotency key is on the event's page |
| No filters, no paging | Tenant and type filters, cursor paging | new |
| No way to send | Send an event, checked against its type's schema like any other send | new |
| Empty state "No events found" | "No events yet" and "No events match these filters" | changed |

### Event detail

| templ | React | status |
|---|---|---|
| Event ID, type, tenant, idempotency key, app scope, org scope, created | same | migrated |
| Payload | same, in a folding, searchable viewer | migrated |
| Deliveries | same, as the delivery log's columns | migrated |
| Empty state "No deliveries created for this event." | "No endpoint matched this event, so nothing was sent." | changed: says why |

### Event types

| templ | React | status |
|---|---|---|
| List with name linking to the detail | same, plus description, group, version, whether a schema validates payloads, and state | changed |
| Active only | Active, or active and deprecated | new |
| No register, no deprecate | Register a type (an upsert, as the catalog is), Deprecate (confirmed) | new |
| Empty state "No event types registered" | "No event types yet. Register one before sending events of it." | migrated |

### Event type detail

| templ | React | status |
|---|---|---|
| Name, group, version, schema version, id, created, updated, deprecated at, app scope | same | migrated |
| JSON schema, example payload, metadata | same | migrated |

### Dead letter queue

| templ | React | status |
|---|---|---|
| Columns Event Type, URL, Error, Attempts, Status Code, Failed At, Replayed | Event type (links to the entry), Endpoint, Response, Tenant, Failed, Replayed, and a Replay action per row | changed: the error and attempt count are on the entry's page, which kept the Replay button on screen |
| Replayed column, which never showed anything on a real backend because replay deleted the row | Replayed badge; replayed rows stay, marked, and cannot be replayed twice | changed |
| No filters, no paging | Tenant, replayed or not, cursor paging | new |
| "Replay all": one button, a browser confirm, a hardcoded 365 day window, no count | Replay a time window: pick 24 hours, 7 days or 30 days, see how many will be sent before the button can send them | changed on purpose: bulk replay sends real webhooks to real receivers, and the old button could not tell you how many |
| No purge | Delete old entries, older than 30 days, 90 days or a year, confirmed | new |
| Empty state "Dead letter queue is empty" | "The dead letter queue is empty. Every delivery either arrived or is still being retried." | migrated |

### Dead letter detail

| templ | React | status |
|---|---|---|
| Entry id, event type, delivery, event, endpoint, tenant, URL, attempt count, last status, failed at, created, replayed at | the same facts, with delivery, event and endpoint as links | migrated |
| Error details, payload | same | migrated |
| Replay, with a browser confirm | Replay, in a dialog that names the URL and says the receiver cannot tell it from the original; the error stays in the dialog; a second replay is refused | changed |

### Settings

| templ | React | status |
|---|---|---|
| Concurrency, poll interval, batch size, request timeout, max retries, shutdown timeout, cache TTL | same, plus the idle poll backoff | migrated |
| Retry schedule, or "Using default retry schedule" | the schedule as waits, "5s, then 30s, then 2m" | migrated |
| Webhook signatures: HMAC-SHA256, X-Relay-Signature, v1=hex, {timestamp}.{payload} | How a receiver verifies a delivery: the same, plus the timestamp header, read from the server | migrated |

## Outside the pages

| templ | status |
|---|---|
| Widgets "Webhook Stats" and "Recent Deliveries" for the Forge dashboard's home | blocked: the React shell has no widget surface for plugins yet. The same numbers are on Relay's overview |
| Settings panel "Relay Configuration" in the Forge dashboard's settings | migrated as Relay's own Settings page |
| Topbar action "API Docs" linking to `/docs` | dropped: the shell has no per-plugin topbar actions |
| Topbar accent colour and logo | dropped: the shell themes every plugin the same way |
| Nav groups Overview, Catalog, Webhooks, Delivery, Configuration | changed to Overview, Traffic (deliveries, events, dead letters) and Configuration (endpoints, event types, settings) |
| `searchable` capability | covered by the shell's own page search |

## Not done, and known

- Delivery-health charts need a time series the domain does not keep. They
  were not in templ either.
- `signature.preview`, which would compute the signature a receiver should
  expect, was left out on purpose: it is the only intent that would derive a
  value from a signing secret. Rotate the secret and test the receiver directly.
- A worker that dies mid-delivery leaves the row `delivering` on postgres,
  sqlite and mongo, and nothing ever picks it up. The engine's own early exits
  no longer do this, but a crash still can. It needs a lease that expires, which
  is its own change.
- Redis filters some delivery, event and DLQ fields in memory over at most
  1000 index entries a call. The pages say so when a search stops early. If
  your redis deployment is large and you filter a lot, a SQL backend answers
  those filters at the index.

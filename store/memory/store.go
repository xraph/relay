// Package memory provides an in-memory Store implementation for unit testing.
package memory

import (
	"context"
	"sort"
	"sync"
	"time"

	"github.com/xraph/relay"
	"github.com/xraph/relay/acceptance"
	"github.com/xraph/relay/catalog"
	"github.com/xraph/relay/delivery"
	"github.com/xraph/relay/dlq"
	"github.com/xraph/relay/endpoint"
	"github.com/xraph/relay/event"
	"github.com/xraph/relay/id"
	relaystore "github.com/xraph/relay/store"
)

// compile-time interface check.
var _ relaystore.Store = (*Store)(nil)

// Store is an in-memory implementation of store.Store for testing.
type Store struct {
	mu sync.RWMutex

	eventTypes      map[string]*catalog.EventType  // keyed by name
	eventTypesByID  map[string]*catalog.EventType  // keyed by ID string
	endpoints       map[string]*endpoint.Endpoint  // keyed by ID string
	events          map[string]*event.Event        // keyed by ID string
	eventsByIdemKey map[string]*event.Event        // keyed by idempotency key
	deliveries      map[string]*delivery.Delivery  // keyed by ID string
	locked          map[string]bool                // simulates SKIP LOCKED
	dlqEntries      map[string]*dlq.Entry          // keyed by ID string
	attempts        map[string][]*delivery.Attempt // keyed by delivery ID string

	receipts map[string]*acceptance.Receipt
	closed   bool
}

// New creates a new in-memory store.
func New() *Store {
	return &Store{
		receipts:        make(map[string]*acceptance.Receipt),
		eventTypes:      make(map[string]*catalog.EventType),
		eventTypesByID:  make(map[string]*catalog.EventType),
		endpoints:       make(map[string]*endpoint.Endpoint),
		events:          make(map[string]*event.Event),
		eventsByIdemKey: make(map[string]*event.Event),
		deliveries:      make(map[string]*delivery.Delivery),
		locked:          make(map[string]bool),
		dlqEntries:      make(map[string]*dlq.Entry),
		attempts:        make(map[string][]*delivery.Attempt),
	}
}

// ──────────────────────────────────────────────────
// Lifecycle
// ──────────────────────────────────────────────────

// Migrate is a no-op for the in-memory store.
func (s *Store) Migrate(_ context.Context) error { return nil }

// Ping is a no-op for the in-memory store.
func (s *Store) Ping(_ context.Context) error {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.closed {
		return relay.ErrStoreClosed
	}
	return nil
}

// Close marks the store as closed.
func (s *Store) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.closed = true
	return nil
}

// ──────────────────────────────────────────────────
// catalog.Store
// ──────────────────────────────────────────────────

// RegisterType creates or updates an event type definition (upsert by name).
func (s *Store) RegisterType(_ context.Context, et *catalog.EventType) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if existing, ok := s.eventTypes[et.Definition.Name]; ok {
		existing.Definition = copyEventType(et).Definition
		existing.UpdatedAt = time.Now().UTC()
		existing.Metadata = copyEventType(et).Metadata
		et.ID = existing.ID
		return nil
	}

	snapshot := copyEventType(et)
	s.eventTypes[et.Definition.Name] = snapshot
	s.eventTypesByID[et.ID.String()] = snapshot
	return nil
}

// GetType returns an event type by name.
func (s *Store) GetType(_ context.Context, name string) (*catalog.EventType, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	et, ok := s.eventTypes[name]
	if !ok {
		return nil, relay.ErrEventTypeNotFound
	}
	return copyEventType(et), nil
}

// GetTypeByID returns an event type by its TypeID.
func (s *Store) GetTypeByID(_ context.Context, etID id.ID) (*catalog.EventType, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	et, ok := s.eventTypesByID[etID.String()]
	if !ok {
		return nil, relay.ErrEventTypeNotFound
	}
	return copyEventType(et), nil
}

// ListTypes returns all registered event types, optionally filtered.
func (s *Store) ListTypes(_ context.Context, opts catalog.ListOpts) ([]*catalog.EventType, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	result := make([]*catalog.EventType, 0, len(s.eventTypes))
	for _, et := range s.eventTypes {
		if !opts.IncludeDeprecated && et.IsDeprecated {
			continue
		}
		if opts.Group != "" && et.Definition.Group != opts.Group {
			continue
		}
		result = append(result, copyEventType(et))
	}

	sort.Slice(result, func(i, j int) bool {
		return result[i].Definition.Name < result[j].Definition.Name
	})

	result = applyPagination(result, opts.Offset, opts.Limit)
	return result, nil
}

// DeleteType soft-deletes (deprecates) an event type.
func (s *Store) DeleteType(_ context.Context, name string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	et, ok := s.eventTypes[name]
	if !ok {
		return relay.ErrEventTypeNotFound
	}

	now := time.Now().UTC()
	et.IsDeprecated = true
	et.DeprecatedAt = &now
	et.UpdatedAt = now
	return nil
}

// MatchTypes returns event types matching a glob pattern.
func (s *Store) MatchTypes(_ context.Context, pattern string) ([]*catalog.EventType, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	var result []*catalog.EventType
	for _, et := range s.eventTypes {
		if et.IsDeprecated {
			continue
		}
		if catalog.Match(pattern, et.Definition.Name) {
			result = append(result, copyEventType(et))
		}
	}
	return result, nil
}

// ──────────────────────────────────────────────────
// endpoint.Store
// ──────────────────────────────────────────────────

// CreateEndpoint persists a new endpoint.
func (s *Store) CreateEndpoint(_ context.Context, ep *endpoint.Endpoint) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.endpoints[ep.ID.String()] = copyEndpoint(ep)
	return nil
}

// GetEndpoint returns an endpoint by ID.
func (s *Store) GetEndpoint(_ context.Context, epID id.ID) (*endpoint.Endpoint, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	ep, ok := s.endpoints[epID.String()]
	if !ok {
		return nil, relay.ErrEndpointNotFound
	}
	return copyEndpoint(ep), nil
}

// UpdateEndpoint modifies an existing endpoint.
func (s *Store) UpdateEndpoint(_ context.Context, ep *endpoint.Endpoint) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if _, ok := s.endpoints[ep.ID.String()]; !ok {
		return relay.ErrEndpointNotFound
	}
	ep.UpdatedAt = time.Now().UTC()
	s.endpoints[ep.ID.String()] = copyEndpoint(ep)
	return nil
}

// DeleteEndpoint removes an endpoint.
func (s *Store) DeleteEndpoint(_ context.Context, epID id.ID) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if _, ok := s.endpoints[epID.String()]; !ok {
		return relay.ErrEndpointNotFound
	}
	delete(s.endpoints, epID.String())
	return nil
}

// ListEndpoints returns endpoints for a tenant, optionally filtered.
func (s *Store) ListEndpoints(_ context.Context, tenantID string, opts endpoint.ListOpts) ([]*endpoint.Endpoint, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	result := make([]*endpoint.Endpoint, 0, len(s.endpoints))
	for _, ep := range s.endpoints {
		// An empty tenant lists every tenant, the same as ListDLQ. It used to match
		// the empty string literally, so a caller asking for every endpoint got none.
		if tenantID != "" && ep.TenantID != tenantID {
			continue
		}
		if opts.Enabled != nil && ep.Enabled != *opts.Enabled {
			continue
		}
		result = append(result, copyEndpoint(ep))
	}

	// created_at alone is not a total order: equal timestamps come back in an
	// undefined order, and Offset paging then skips some endpoints and repeats
	// others. id breaks the tie. Ranging over the map gives a random order on every
	// call, so without it the ties came back shuffled each time.
	sort.Slice(result, func(i, j int) bool {
		if !result[i].CreatedAt.Equal(result[j].CreatedAt) {
			return result[i].CreatedAt.Before(result[j].CreatedAt)
		}
		return result[i].ID.String() < result[j].ID.String()
	})

	result = applyPagination(result, opts.Offset, opts.Limit)
	return result, nil
}

// Resolve finds all active endpoints matching an event type for a tenant.
func (s *Store) Resolve(_ context.Context, tenantID, eventType string) ([]*endpoint.Endpoint, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	var result []*endpoint.Endpoint
	for _, ep := range s.endpoints {
		if ep.TenantID != tenantID || !ep.Enabled {
			continue
		}
		for _, pattern := range ep.EventTypes {
			if catalog.Match(pattern, eventType) {
				result = append(result, copyEndpoint(ep))
				break
			}
		}
	}
	return result, nil
}

// SetEnabled enables or disables an endpoint.
func (s *Store) SetEnabled(_ context.Context, epID id.ID, enabled bool) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	ep, ok := s.endpoints[epID.String()]
	if !ok {
		return relay.ErrEndpointNotFound
	}
	ep.Enabled = enabled
	ep.UpdatedAt = time.Now().UTC()
	return nil
}

// ──────────────────────────────────────────────────
// event.Store
// ──────────────────────────────────────────────────

// CreateEvent persists an event. Returns ErrDuplicateIdempotencyKey on conflict.
func (s *Store) CreateEvent(_ context.Context, evt *event.Event) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if evt.IdempotencyKey != "" {
		if _, ok := s.eventsByIdemKey[evt.IdempotencyKey]; ok {
			return relay.ErrDuplicateIdempotencyKey
		}
		s.eventsByIdemKey[evt.IdempotencyKey] = evt
	}

	s.events[evt.ID.String()] = evt
	return nil
}

// GetEvent returns an event by ID.
func (s *Store) GetEvent(_ context.Context, evtID id.ID) (*event.Event, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	evt, ok := s.events[evtID.String()]
	if !ok {
		return nil, relay.ErrEventNotFound
	}
	return evt, nil
}

// ListEvents returns events, optionally filtered.
func (s *Store) ListEvents(_ context.Context, opts event.ListOpts) ([]*event.Event, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	result := make([]*event.Event, 0, len(s.events))
	for _, evt := range s.events {
		if !matchEventOpts(evt, opts) {
			continue
		}
		result = append(result, evt)
	}

	sort.Slice(result, func(i, j int) bool {
		return result[i].CreatedAt.After(result[j].CreatedAt)
	})

	result = applyPagination(result, opts.Offset, opts.Limit)
	return result, nil
}

// ListEventsByTenant returns events for a specific tenant.
func (s *Store) ListEventsByTenant(_ context.Context, tenantID string, opts event.ListOpts) ([]*event.Event, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	result := make([]*event.Event, 0, len(s.events))
	for _, evt := range s.events {
		if evt.TenantID != tenantID {
			continue
		}
		if !matchEventOpts(evt, opts) {
			continue
		}
		result = append(result, evt)
	}

	sort.Slice(result, func(i, j int) bool {
		return result[i].CreatedAt.After(result[j].CreatedAt)
	})

	result = applyPagination(result, opts.Offset, opts.Limit)
	return result, nil
}

// ──────────────────────────────────────────────────
// delivery.Store
// ──────────────────────────────────────────────────

// Enqueue creates a pending delivery.
func (s *Store) Enqueue(_ context.Context, d *delivery.Delivery) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.deliveries[d.ID.String()] = d
	return nil
}

// EnqueueBatch creates multiple deliveries atomically.
func (s *Store) EnqueueBatch(_ context.Context, ds []*delivery.Delivery) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	for _, d := range ds {
		s.deliveries[d.ID.String()] = d
	}
	return nil
}

// copyDelivery returns a shallow copy of the delivery.
func copyDelivery(d *delivery.Delivery) *delivery.Delivery {
	cp := *d
	return &cp
}

// Dequeue fetches pending deliveries ready for attempt (concurrent-safe).
// Returns copies so callers can mutate without holding a lock.
func (s *Store) Dequeue(_ context.Context, limit int) ([]*delivery.Delivery, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	now := time.Now()
	candidates := make([]*delivery.Delivery, 0, len(s.deliveries))

	for _, d := range s.deliveries {
		if d.State != delivery.StatePending {
			continue
		}
		if d.NextAttemptAt.After(now) {
			continue
		}
		if s.locked[d.ID.String()] {
			continue
		}
		candidates = append(candidates, d)
	}

	sort.Slice(candidates, func(i, j int) bool {
		return candidates[i].NextAttemptAt.Before(candidates[j].NextAttemptAt)
	})

	if limit > 0 && limit < len(candidates) {
		candidates = candidates[:limit]
	}

	result := make([]*delivery.Delivery, 0, len(candidates))
	for _, d := range candidates {
		s.locked[d.ID.String()] = true
		result = append(result, copyDelivery(d))
	}

	return result, nil
}

// UpdateDelivery modifies a delivery and releases its lock.
func (s *Store) UpdateDelivery(_ context.Context, d *delivery.Delivery) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if _, ok := s.deliveries[d.ID.String()]; !ok {
		return relay.ErrDeliveryNotFound
	}
	d.UpdatedAt = time.Now().UTC()
	s.deliveries[d.ID.String()] = d
	delete(s.locked, d.ID.String())
	return nil
}

// GetDelivery returns a copy of the delivery by ID.
func (s *Store) GetDelivery(_ context.Context, delID id.ID) (*delivery.Delivery, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	d, ok := s.deliveries[delID.String()]
	if !ok {
		return nil, relay.ErrDeliveryNotFound
	}
	return copyDelivery(d), nil
}

// ListByEndpoint returns delivery history for an endpoint.
func (s *Store) ListByEndpoint(_ context.Context, epID id.ID, opts delivery.ListOpts) ([]*delivery.Delivery, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	result := make([]*delivery.Delivery, 0, len(s.deliveries))
	for _, d := range s.deliveries {
		if d.EndpointID.String() != epID.String() {
			continue
		}
		if opts.State != nil && d.State != *opts.State {
			continue
		}
		result = append(result, d)
	}

	sort.Slice(result, func(i, j int) bool {
		return result[i].CreatedAt.After(result[j].CreatedAt)
	})

	result = applyPagination(result, opts.Offset, opts.Limit)
	return result, nil
}

// ListByEvent returns all deliveries for a specific event.
func (s *Store) ListByEvent(_ context.Context, evtID id.ID) ([]*delivery.Delivery, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	result := make([]*delivery.Delivery, 0, len(s.deliveries))
	for _, d := range s.deliveries {
		if d.EventID.String() != evtID.String() {
			continue
		}
		result = append(result, d)
	}
	return result, nil
}

// CountPending returns the number of deliveries awaiting attempt.
func (s *Store) CountPending(ctx context.Context) (int64, error) {
	if err := s.lockPendingCount(ctx); err != nil {
		return 0, err
	}
	defer s.mu.RUnlock()

	var count int64
	for _, d := range s.deliveries {
		if err := ctx.Err(); err != nil {
			return 0, err
		}
		if d.State == delivery.StatePending {
			count++
		}
	}
	return count, nil
}

// ──────────────────────────────────────────────────
// dlq.Store
// ──────────────────────────────────────────────────

// Push moves a permanently failed delivery into the DLQ.
func (s *Store) Push(_ context.Context, entry *dlq.Entry) error {
	payload, err := dlq.EncodePayload(entry.Payload)
	if err != nil {
		return err
	}
	// A copy holding the encoded payload, so this store hands back the same
	// json.RawMessage every persistent backend does, and the caller's entry
	// is left as it was.
	cp := *entry
	cp.Payload = payload

	s.mu.Lock()
	defer s.mu.Unlock()

	s.dlqEntries[entry.ID.String()] = &cp
	return nil
}

// ListDLQ returns DLQ entries, optionally filtered.
func (s *Store) ListDLQ(_ context.Context, opts dlq.ListOpts) ([]*dlq.Entry, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	result := make([]*dlq.Entry, 0, len(s.dlqEntries))
	for _, e := range s.dlqEntries {
		if opts.TenantID != "" && e.TenantID != opts.TenantID {
			continue
		}
		if opts.EndpointID != nil && e.EndpointID.String() != opts.EndpointID.String() {
			continue
		}
		if opts.From != nil && e.FailedAt.Before(*opts.From) {
			continue
		}
		if opts.To != nil && e.FailedAt.After(*opts.To) {
			continue
		}
		cp := *e // a copy: see GetDLQ
		result = append(result, &cp)
	}

	sort.Slice(result, func(i, j int) bool {
		return result[i].FailedAt.After(result[j].FailedAt)
	})

	result = applyPagination(result, opts.Offset, opts.Limit)
	return result, nil
}

// GetDLQ returns a DLQ entry by ID.
func (s *Store) GetDLQ(_ context.Context, dlqID id.ID) (*dlq.Entry, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	e, ok := s.dlqEntries[dlqID.String()]
	if !ok {
		return nil, relay.ErrDLQNotFound
	}
	// A copy, not the stored entry. Handing out the internal pointer let a
	// caller read ReplayedAt while MarkReplayed wrote it under the lock, which
	// the race detector flags; every other backend returns a fresh value.
	cp := *e
	return &cp, nil
}

// AllDeliveries returns every delivery currently held. Test support: the
// delivery store interface has no unfiltered list.
func (s *Store) AllDeliveries() []*delivery.Delivery {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]*delivery.Delivery, 0, len(s.deliveries))
	for _, d := range s.deliveries {
		out = append(out, d)
	}
	return out
}

// MarkReplayed claims a DLQ entry for replay. It sets replayed_at only if it
// is not already set, and does so atomically, so of any number of concurrent
// callers exactly one succeeds. The service calls this before it sends the
// webhook: a claim that overwrote instead would let two replays both win and
// both send. Returns relay.ErrAlreadyReplayed when the entry is already
// claimed and relay.ErrDLQNotFound when it does not exist.
func (s *Store) MarkReplayed(_ context.Context, dlqID id.ID, at time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	e, ok := s.dlqEntries[dlqID.String()]
	if !ok {
		return relay.ErrDLQNotFound
	}
	if e.ReplayedAt != nil {
		return relay.ErrAlreadyReplayed
	}
	t := at.UTC()
	e.ReplayedAt = &t
	return nil
}

// ReleaseReplay undoes a claim made by MarkReplayed, clearing replayed_at so
// the entry can be replayed again. The service calls it when the send it
// claimed the entry for fails, so a failure never leaves an entry marked as
// sent when nothing went out. Only the claim holder calls it. Returns
// relay.ErrDLQNotFound when the entry does not exist.
func (s *Store) ReleaseReplay(_ context.Context, dlqID id.ID) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	e, ok := s.dlqEntries[dlqID.String()]
	if !ok {
		return relay.ErrDLQNotFound
	}
	e.ReplayedAt = nil
	return nil
}

// Purge deletes DLQ entries older than a threshold.
func (s *Store) Purge(_ context.Context, before time.Time) (int64, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	var count int64
	for k, e := range s.dlqEntries {
		// By when it failed, as every other backend does.
		if e.FailedAt.Before(before) {
			delete(s.dlqEntries, k)
			count++
		}
	}
	return count, nil
}

// CountDLQ returns the total number of DLQ entries.
func (s *Store) CountDLQ(_ context.Context) (int64, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	return int64(len(s.dlqEntries)), nil
}

// ──────────────────────────────────────────────────
// Helpers
// ──────────────────────────────────────────────────

func matchEventOpts(evt *event.Event, opts event.ListOpts) bool {
	if opts.Type != "" && evt.Type != opts.Type {
		return false
	}
	if opts.From != nil && evt.CreatedAt.Before(*opts.From) {
		return false
	}
	if opts.To != nil && evt.CreatedAt.After(*opts.To) {
		return false
	}
	return true
}

func applyPagination[T any](items []*T, offset, limit int) []*T {
	if offset > 0 && offset < len(items) {
		items = items[offset:]
	} else if offset >= len(items) {
		return nil
	}

	if limit > 0 && limit < len(items) {
		items = items[:limit]
	}

	return items
}

// ──────────────────────────────────────────────────
// Attempts
// ──────────────────────────────────────────────────

// RecordAttempt stores one attempt.
func (s *Store) RecordAttempt(_ context.Context, a *delivery.Attempt) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	cp := *a
	key := a.DeliveryID.String()
	s.attempts[key] = append(s.attempts[key], &cp)
	return nil
}

// ListAttempts returns a delivery's attempts by attempt number.
func (s *Store) ListAttempts(_ context.Context, delID id.ID) ([]*delivery.Attempt, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	src := s.attempts[delID.String()]
	out := make([]*delivery.Attempt, 0, len(src))
	for _, a := range src {
		cp := *a
		out = append(out, &cp)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].AttemptNum < out[j].AttemptNum })
	return out, nil
}

// PurgeAttempts deletes attempts made before the cutoff.
func (s *Store) PurgeAttempts(_ context.Context, before time.Time) (int64, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var n int64
	for key, list := range s.attempts {
		kept := list[:0]
		for _, a := range list {
			if a.AttemptedAt.Before(before) {
				n++
				continue
			}
			kept = append(kept, a)
		}
		if len(kept) == 0 {
			delete(s.attempts, key)
		} else {
			s.attempts[key] = kept
		}
	}
	return n, nil
}

// ListDeliveries returns one page of the delivery log, newest first.
func (s *Store) ListDeliveries(_ context.Context, q delivery.Query) (*delivery.Page, error) {
	limit, pos, err := q.Prepare()
	if err != nil {
		return nil, err
	}

	s.mu.RLock()
	matched := make([]*delivery.Delivery, 0, len(s.deliveries))
	for _, d := range s.deliveries {
		if q.Matches(d) && pos.After(d) {
			matched = append(matched, copyDelivery(d))
		}
	}
	s.mu.RUnlock()

	sort.Slice(matched, func(i, j int) bool {
		a, b := matched[i], matched[j]
		if !a.CreatedAt.Equal(b.CreatedAt) {
			return a.CreatedAt.After(b.CreatedAt)
		}
		return a.ID.String() > b.ID.String()
	})

	page := &delivery.Page{Deliveries: matched, Complete: true}
	if len(matched) > limit {
		page.Deliveries = matched[:limit]
		page.NextCursor = delivery.CursorFor(matched[limit-1])
	}
	return page, nil
}

// ListEventsPage returns one page of the event log, newest first.
func (s *Store) ListEventsPage(_ context.Context, q event.Query) (*event.Page, error) {
	limit, pos, err := q.Prepare()
	if err != nil {
		return nil, err
	}
	s.mu.RLock()
	matched := make([]*event.Event, 0, len(s.events))
	for _, e := range s.events {
		if q.Matches(e) && pos.After(e) {
			cp := *e
			matched = append(matched, &cp)
		}
	}
	s.mu.RUnlock()
	sort.Slice(matched, func(i, j int) bool {
		a, b := matched[i], matched[j]
		if !a.CreatedAt.Equal(b.CreatedAt) {
			return a.CreatedAt.After(b.CreatedAt)
		}
		return a.ID.String() > b.ID.String()
	})
	page := &event.Page{Events: matched, Complete: true}
	if len(matched) > limit {
		page.Events = matched[:limit]
		page.NextCursor = event.CursorFor(matched[limit-1])
	}
	return page, nil
}

// ListDLQPage returns one page of the DLQ, most recent failure first.
func (s *Store) ListDLQPage(_ context.Context, q dlq.Query) (*dlq.Page, error) {
	limit, pos, err := q.Prepare()
	if err != nil {
		return nil, err
	}
	s.mu.RLock()
	matched := make([]*dlq.Entry, 0, len(s.dlqEntries))
	for _, e := range s.dlqEntries {
		if q.Matches(e) && pos.After(e) {
			cp := *e
			matched = append(matched, &cp)
		}
	}
	s.mu.RUnlock()
	sort.Slice(matched, func(i, j int) bool {
		a, b := matched[i], matched[j]
		if !a.FailedAt.Equal(b.FailedAt) {
			return a.FailedAt.After(b.FailedAt)
		}
		return a.ID.String() > b.ID.String()
	})
	page := &dlq.Page{Entries: matched, Complete: true}
	if len(matched) > limit {
		page.Entries = matched[:limit]
		page.NextCursor = dlq.CursorFor(matched[limit-1])
	}
	return page, nil
}

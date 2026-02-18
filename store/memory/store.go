// Package memory provides an in-memory Store implementation for unit testing.
package memory

import (
	"context"
	"sort"
	"sync"
	"time"

	"github.com/xraph/relay"
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

	eventTypes      map[string]*catalog.EventType // keyed by name
	eventTypesByID  map[string]*catalog.EventType // keyed by ID string
	endpoints       map[string]*endpoint.Endpoint // keyed by ID string
	events          map[string]*event.Event       // keyed by ID string
	eventsByIdemKey map[string]*event.Event       // keyed by idempotency key
	deliveries      map[string]*delivery.Delivery // keyed by ID string
	locked          map[string]bool               // simulates SKIP LOCKED
	dlqEntries      map[string]*dlq.Entry         // keyed by ID string

	closed bool
}

// New creates a new in-memory store.
func New() *Store {
	return &Store{
		eventTypes:      make(map[string]*catalog.EventType),
		eventTypesByID:  make(map[string]*catalog.EventType),
		endpoints:       make(map[string]*endpoint.Endpoint),
		events:          make(map[string]*event.Event),
		eventsByIdemKey: make(map[string]*event.Event),
		deliveries:      make(map[string]*delivery.Delivery),
		locked:          make(map[string]bool),
		dlqEntries:      make(map[string]*dlq.Entry),
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
		existing.Definition = et.Definition
		existing.UpdatedAt = time.Now().UTC()
		existing.Metadata = et.Metadata
		et.ID = existing.ID
		return nil
	}

	s.eventTypes[et.Definition.Name] = et
	s.eventTypesByID[et.ID.String()] = et
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
	return et, nil
}

// GetTypeByID returns an event type by its TypeID.
func (s *Store) GetTypeByID(_ context.Context, etID id.ID) (*catalog.EventType, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	et, ok := s.eventTypesByID[etID.String()]
	if !ok {
		return nil, relay.ErrEventTypeNotFound
	}
	return et, nil
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
		result = append(result, et)
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
			result = append(result, et)
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

	s.endpoints[ep.ID.String()] = ep
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
	return ep, nil
}

// UpdateEndpoint modifies an existing endpoint.
func (s *Store) UpdateEndpoint(_ context.Context, ep *endpoint.Endpoint) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if _, ok := s.endpoints[ep.ID.String()]; !ok {
		return relay.ErrEndpointNotFound
	}
	ep.UpdatedAt = time.Now().UTC()
	s.endpoints[ep.ID.String()] = ep
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
		if ep.TenantID != tenantID {
			continue
		}
		if opts.Enabled != nil && ep.Enabled != *opts.Enabled {
			continue
		}
		result = append(result, ep)
	}

	sort.Slice(result, func(i, j int) bool {
		return result[i].CreatedAt.Before(result[j].CreatedAt)
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
				result = append(result, ep)
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
func (s *Store) CountPending(_ context.Context) (int64, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	var count int64
	for _, d := range s.deliveries {
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
	s.mu.Lock()
	defer s.mu.Unlock()

	s.dlqEntries[entry.ID.String()] = entry
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
		result = append(result, e)
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
	return e, nil
}

// Replay marks a DLQ entry for redelivery and re-enqueues the delivery.
func (s *Store) Replay(_ context.Context, dlqID id.ID) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	e, ok := s.dlqEntries[dlqID.String()]
	if !ok {
		return relay.ErrDLQNotFound
	}

	now := time.Now().UTC()
	e.ReplayedAt = &now

	d := &delivery.Delivery{
		Entity:        relay.NewEntity(),
		ID:            id.NewDeliveryID(),
		EventID:       e.EventID,
		EndpointID:    e.EndpointID,
		State:         delivery.StatePending,
		AttemptCount:  0,
		MaxAttempts:   5,
		NextAttemptAt: now,
	}
	s.deliveries[d.ID.String()] = d
	return nil
}

// ReplayBulk replays all DLQ entries in a time window.
func (s *Store) ReplayBulk(_ context.Context, from, to time.Time) (int64, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	now := time.Now().UTC()
	var count int64

	for _, e := range s.dlqEntries {
		if e.FailedAt.Before(from) || e.FailedAt.After(to) {
			continue
		}
		if e.ReplayedAt != nil {
			continue
		}

		e.ReplayedAt = &now

		d := &delivery.Delivery{
			Entity:        relay.NewEntity(),
			ID:            id.NewDeliveryID(),
			EventID:       e.EventID,
			EndpointID:    e.EndpointID,
			State:         delivery.StatePending,
			AttemptCount:  0,
			MaxAttempts:   5,
			NextAttemptAt: now,
		}
		s.deliveries[d.ID.String()] = d
		count++
	}
	return count, nil
}

// Purge deletes DLQ entries older than a threshold.
func (s *Store) Purge(_ context.Context, before time.Time) (int64, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	var count int64
	for k, e := range s.dlqEntries {
		if e.CreatedAt.Before(before) {
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

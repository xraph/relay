// Package storetest holds cross-backend conformance suites. Every store
// implementation must produce the same observable behaviour for the
// operations covered here, which is not something a per-backend test can
// establish on its own.
package storetest

import (
	"context"
	"testing"
	"time"

	"github.com/xraph/relay/delivery"
	"github.com/xraph/relay/dlq"
	"github.com/xraph/relay/id"
	"github.com/xraph/relay/internal/entity"
)

// ReplayBackend is the slice of a store the replay suite exercises.
type ReplayBackend interface {
	Push(ctx context.Context, entry *dlq.Entry) error
	GetDLQ(ctx context.Context, dlqID id.ID) (*dlq.Entry, error)
	ListDLQ(ctx context.Context, opts dlq.ListOpts) ([]*dlq.Entry, error)
	MarkReplayed(ctx context.Context, dlqID id.ID, at time.Time) error
	CountDLQ(ctx context.Context) (int64, error)
	Enqueue(ctx context.Context, d *delivery.Delivery) error
	GetDelivery(ctx context.Context, delID id.ID) (*delivery.Delivery, error)
}

// NewEntry builds a DLQ entry suitable for the suite.
func NewEntry() *dlq.Entry {
	return &dlq.Entry{
		Entity:         entity.New(),
		ID:             id.NewDLQID(),
		DeliveryID:     id.NewDeliveryID(),
		EventID:        id.NewEventID(),
		EndpointID:     id.NewEndpointID(),
		EventType:      "invoice.created",
		TenantID:       "tenant-1",
		URL:            "https://receiver.example/hook",
		Payload:        []byte(`{"id":"inv_1"}`),
		Error:          "connection refused",
		AttemptCount:   5,
		LastStatusCode: 0,
		FailedAt:       time.Now().UTC().Add(-time.Hour),
	}
}

// RunReplaySuite asserts the replay semantics every backend must share.
func RunReplaySuite(t *testing.T, newStore func(t *testing.T) ReplayBackend) {
	t.Helper()

	t.Run("MarkReplayed keeps the row and sets the timestamp", func(t *testing.T) {
		ctx := context.Background()
		s := newStore(t)
		e := NewEntry()
		if err := s.Push(ctx, e); err != nil {
			t.Fatalf("push: %v", err)
		}

		at := time.Now().UTC().Truncate(time.Millisecond)
		if err := s.MarkReplayed(ctx, e.ID, at); err != nil {
			t.Fatalf("mark replayed: %v", err)
		}

		got, err := s.GetDLQ(ctx, e.ID)
		if err != nil {
			t.Fatalf("the row must survive a replay, got: %v", err)
		}
		if got.ReplayedAt == nil {
			t.Fatal("ReplayedAt is nil after MarkReplayed")
		}
		if got.ReplayedAt.Unix() != at.Unix() {
			t.Fatalf("ReplayedAt = %v, want %v", got.ReplayedAt, at)
		}
	})

	t.Run("a marked row still counts and still lists", func(t *testing.T) {
		ctx := context.Background()
		s := newStore(t)
		e := NewEntry()
		if err := s.Push(ctx, e); err != nil {
			t.Fatalf("push: %v", err)
		}
		if err := s.MarkReplayed(ctx, e.ID, time.Now().UTC()); err != nil {
			t.Fatalf("mark replayed: %v", err)
		}

		n, err := s.CountDLQ(ctx)
		if err != nil {
			t.Fatalf("count: %v", err)
		}
		if n != 1 {
			t.Fatalf("CountDLQ = %d, want 1: a replayed entry is still an entry", n)
		}

		list, err := s.ListDLQ(ctx, dlq.ListOpts{Limit: 10})
		if err != nil {
			t.Fatalf("list: %v", err)
		}
		if len(list) != 1 {
			t.Fatalf("ListDLQ returned %d entries, want 1", len(list))
		}
		if list[0].ReplayedAt == nil {
			t.Fatal("ListDLQ dropped ReplayedAt on the way back out")
		}
	})

	// Pinning subtests. These do not assert a behaviour chosen in advance;
	// they record what this backend actually does and fail when backends
	// disagree with each other. Reading five stores by hand is how the
	// replay divergence was found in the first place. This makes that
	// discovery automatic, and it would have caught the MaxAttempts case
	// as a byproduct.
	t.Run("pin: what an empty tenant filter returns", func(t *testing.T) {
		ctx := context.Background()
		s := newStore(t)
		a := NewEntry()
		a.TenantID = "tenant-1"
		b := NewEntry()
		b.TenantID = "tenant-2"
		for _, e := range []*dlq.Entry{a, b} {
			if err := s.Push(ctx, e); err != nil {
				t.Fatalf("push: %v", err)
			}
		}

		got, err := s.ListDLQ(ctx, dlq.ListOpts{Limit: 10, TenantID: ""})
		if err != nil {
			t.Fatalf("list: %v", err)
		}
		// An empty tenant filter means "every tenant" on every backend that
		// has been checked. If a backend returns zero here it is treating
		// empty as a literal match, which makes a page that forgot to send
		// a tenant id look perfectly correct.
		if len(got) != 2 {
			t.Fatalf("an empty TenantID returned %d of 2 entries. If this "+
				"backend intends empty to mean 'no tenant', say so here and "+
				"raise it: the backends disagree and callers cannot tell",
				len(got))
		}
		ids := map[string]bool{}
		for _, e := range got {
			ids[e.ID.String()] = true
		}
		// Identity, not count. A count assertion passes when the wrong rows
		// come back in the right quantity.
		if !ids[a.ID.String()] || !ids[b.ID.String()] {
			t.Fatalf("an empty TenantID returned two entries but not the two "+
				"that were pushed: got %v", ids)
		}
	})

	t.Run("pin: tenant isolation by identity", func(t *testing.T) {
		ctx := context.Background()
		s := newStore(t)
		mine := NewEntry()
		mine.TenantID = "tenant-1"
		theirs := NewEntry()
		theirs.TenantID = "tenant-2"
		for _, e := range []*dlq.Entry{mine, theirs} {
			if err := s.Push(ctx, e); err != nil {
				t.Fatalf("push: %v", err)
			}
		}

		got, err := s.ListDLQ(ctx, dlq.ListOpts{Limit: 10, TenantID: "tenant-1"})
		if err != nil {
			t.Fatalf("list: %v", err)
		}
		if len(got) != 1 {
			t.Fatalf("got %d entries for tenant-1, want 1", len(got))
		}
		if got[0].ID != mine.ID {
			t.Fatalf("tenant-1's filter returned tenant-2's entry: %s", got[0].ID)
		}
	})

	t.Run("MarkReplayed on a missing row reports not found", func(t *testing.T) {
		ctx := context.Background()
		s := newStore(t)
		err := s.MarkReplayed(ctx, id.NewDLQID(), time.Now().UTC())
		if err == nil {
			t.Fatal("MarkReplayed on a missing row returned nil")
		}
	})

	t.Run("an enqueued delivery round-trips its retry budget", func(t *testing.T) {
		ctx := context.Background()
		s := newStore(t)
		d := &delivery.Delivery{
			Entity:        entity.New(),
			ID:            id.NewDeliveryID(),
			EventID:       id.NewEventID(),
			EndpointID:    id.NewEndpointID(),
			State:         delivery.StatePending,
			MaxAttempts:   5,
			NextAttemptAt: time.Now().UTC(),
		}
		if err := s.Enqueue(ctx, d); err != nil {
			t.Fatalf("enqueue: %v", err)
		}
		got, err := s.GetDelivery(ctx, d.ID)
		if err != nil {
			t.Fatalf("get delivery: %v", err)
		}
		if got.MaxAttempts != 5 {
			t.Fatalf("MaxAttempts = %d, want 5: a backend that drops this "+
				"reintroduces the 1 < 0 bug", got.MaxAttempts)
		}
	})
}

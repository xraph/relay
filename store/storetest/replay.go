// Package storetest holds cross-backend conformance suites. Every store
// implementation must produce the same observable behaviour for the
// operations covered here, which is not something a per-backend test can
// establish on its own.
package storetest

import (
	"context"
	"crypto/rand"
	"encoding/hex"
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

// uniqueTenant returns a tenant id no other subtest uses. The suite runs
// against real databases that one container shares across subtests, so every
// assertion is scoped to rows this subtest created rather than relying on an
// empty table.
func uniqueTenant(t *testing.T) string {
	t.Helper()
	b := make([]byte, 6)
	if _, err := rand.Read(b); err != nil {
		t.Fatalf("rand: %v", err)
	}
	return "tenant-" + hex.EncodeToString(b)
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
		before, err := s.CountDLQ(ctx)
		if err != nil {
			t.Fatalf("count before: %v", err)
		}
		e := NewEntry()
		e.TenantID = uniqueTenant(t)
		if pushErr := s.Push(ctx, e); pushErr != nil {
			t.Fatalf("push: %v", pushErr)
		}
		if markErr := s.MarkReplayed(ctx, e.ID, time.Now().UTC()); markErr != nil {
			t.Fatalf("mark replayed: %v", markErr)
		}

		// A delta, not an absolute, so rows from other subtests cannot skew it.
		after, err := s.CountDLQ(ctx)
		if err != nil {
			t.Fatalf("count after: %v", err)
		}
		if after != before+1 {
			t.Fatalf("CountDLQ went %d -> %d, want +1: a replayed entry is still an entry",
				before, after)
		}

		list, err := s.ListDLQ(ctx, dlq.ListOpts{Limit: 10, TenantID: e.TenantID})
		if err != nil {
			t.Fatalf("list: %v", err)
		}
		if len(list) != 1 || list[0].ID != e.ID {
			t.Fatalf("ListDLQ for its own tenant returned %d entries, want exactly this one", len(list))
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
		a.TenantID = uniqueTenant(t)
		b := NewEntry()
		b.TenantID = uniqueTenant(t)
		for _, e := range []*dlq.Entry{a, b} {
			if err := s.Push(ctx, e); err != nil {
				t.Fatalf("push: %v", err)
			}
		}

		// Large enough to include rows other subtests left in a shared
		// database. The question is only whether both of ours come back.
		got, err := s.ListDLQ(ctx, dlq.ListOpts{Limit: 10000, TenantID: ""})
		if err != nil {
			t.Fatalf("list: %v", err)
		}
		ids := map[string]bool{}
		for _, e := range got {
			ids[e.ID.String()] = true
		}
		// An empty tenant filter means "every tenant" on every backend checked
		// so far. A backend treating empty as a literal match would return
		// neither of these, since neither has an empty tenant. Asserting on
		// identity rather than a total is what lets this run against a shared
		// database, and it discriminates the two behaviours just as sharply.
		//
		// Note that ListEndpoints does the opposite and matches literally on
		// all five backends. The two list methods disagree with each other.
		if !ids[a.ID.String()] || !ids[b.ID.String()] {
			t.Fatalf("an empty TenantID did not return both entries from two "+
				"different tenants (got a=%v b=%v). If this backend treats empty "+
				"as a literal match, the backends disagree and callers cannot tell",
				ids[a.ID.String()], ids[b.ID.String()])
		}
	})

	t.Run("pin: tenant isolation by identity", func(t *testing.T) {
		ctx := context.Background()
		s := newStore(t)
		mine := NewEntry()
		mine.TenantID = uniqueTenant(t)
		theirs := NewEntry()
		theirs.TenantID = uniqueTenant(t)
		for _, e := range []*dlq.Entry{mine, theirs} {
			if err := s.Push(ctx, e); err != nil {
				t.Fatalf("push: %v", err)
			}
		}

		got, err := s.ListDLQ(ctx, dlq.ListOpts{Limit: 10, TenantID: mine.TenantID})
		if err != nil {
			t.Fatalf("list: %v", err)
		}
		if len(got) != 1 {
			t.Fatalf("got %d entries for one tenant, want 1", len(got))
		}
		if got[0].ID != mine.ID {
			t.Fatalf("one tenant's filter returned another tenant's entry: %s", got[0].ID)
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

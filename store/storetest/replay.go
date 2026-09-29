// Package storetest holds cross-backend conformance suites. Every store
// implementation must produce the same observable behaviour for the
// operations covered here, which is not something a per-backend test can
// establish on its own.
package storetest

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"reflect"
	"sync"
	"testing"
	"time"

	"github.com/xraph/relay"

	"github.com/xraph/relay/delivery"
	"github.com/xraph/relay/dlq"
	"github.com/xraph/relay/id"
	"github.com/xraph/relay/internal/entity"
)

// ReplayBackend is the slice of a store the replay suite exercises.
type ReplayBackend interface {
	Push(ctx context.Context, entry *dlq.Entry) error
	Purge(ctx context.Context, before time.Time) (int64, error)
	GetDLQ(ctx context.Context, dlqID id.ID) (*dlq.Entry, error)
	ListDLQ(ctx context.Context, opts dlq.ListOpts) ([]*dlq.Entry, error)
	MarkReplayed(ctx context.Context, dlqID id.ID, at time.Time) error
	ReleaseReplay(ctx context.Context, dlqID id.ID) error
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

	// Purge is "entries that failed before the cutoff". The memory store
	// compared created_at instead, which agrees only when the two are equal.
	t.Run("Purge removes entries by when they failed", func(t *testing.T) {
		ctx := context.Background()
		s := newStore(t)
		cutoff := time.Date(2001, 6, 1, 0, 0, 0, 0, time.UTC)
		failedLongAgo := NewEntry()
		failedLongAgo.FailedAt = cutoff.Add(-time.Hour) // created now, failed long ago
		recent := NewEntry()
		recent.FailedAt = cutoff.Add(time.Hour)
		recent.CreatedAt = cutoff.Add(-24 * time.Hour) // created long ago, failed later
		for _, e := range []*dlq.Entry{failedLongAgo, recent} {
			if err := s.Push(ctx, e); err != nil {
				t.Fatalf("push: %v", err)
			}
		}
		if _, err := s.Purge(ctx, cutoff); err != nil {
			t.Fatalf("purge: %v", err)
		}
		if _, err := s.GetDLQ(ctx, failedLongAgo.ID); err == nil {
			t.Error("an entry that failed before the cutoff survived the purge")
		}
		if _, err := s.GetDLQ(ctx, recent.ID); err != nil {
			t.Errorf("an entry that failed after the cutoff was purged: %v", err)
		}
	})

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

	// Bulk replay selects its window through ListDLQ, so on a real backend its
	// correctness rests entirely on this filter. Every backend is meant to
	// filter From and To on failed_at, the column bulk replay always used.
	t.Run("ListDLQ selects a window on failed_at", func(t *testing.T) {
		ctx := context.Background()
		s := newStore(t)
		tenant := uniqueTenant(t)

		// Second precision so every backend stores these values exactly, which
		// lets the edge entries sit precisely on From and To.
		now := time.Now().UTC().Truncate(time.Second)
		from := now.Add(-4 * time.Hour)
		to := now.Add(-2 * time.Hour)

		mk := func(at time.Time) *dlq.Entry {
			e := NewEntry()
			e.TenantID = tenant
			e.FailedAt = at
			if err := s.Push(ctx, e); err != nil {
				t.Fatalf("push: %v", err)
			}
			return e
		}
		before := mk(from.Add(-time.Hour)) // outside, below
		atFrom := mk(from)                 // on the lower edge
		inside := mk(from.Add(time.Hour))  // strictly inside
		atTo := mk(to)                     // on the upper edge
		after := mk(to.Add(time.Hour))     // outside, above

		got, err := s.ListDLQ(ctx, dlq.ListOpts{TenantID: tenant, From: &from, To: &to})
		if err != nil {
			t.Fatalf("list: %v", err)
		}
		ids := map[string]bool{}
		for _, e := range got {
			ids[e.ID.String()] = true
		}
		// Both bounds matter. A broken lower bound replays older failures the
		// operator did not ask for; a broken upper bound replays newer ones.
		// Both edges are inclusive on every backend today, which is pinned here.
		for name, e := range map[string]*dlq.Entry{"on From": atFrom, "inside": inside, "on To": atTo} {
			if !ids[e.ID.String()] {
				t.Errorf("the window dropped the entry %s", name)
			}
		}
		for name, e := range map[string]*dlq.Entry{"before From": before, "after To": after} {
			if ids[e.ID.String()] {
				t.Errorf("the window included the entry %s: bulk replay would "+
					"send a webhook outside the window the operator chose", name)
			}
		}
	})

	// MarkReplayed is a claim, not a write. Replay calls it before sending, so
	// it must refuse an entry someone else already claimed; if it overwrote,
	// two replays would both think they won and both send the webhook.
	t.Run("MarkReplayed refuses an entry already marked", func(t *testing.T) {
		ctx := context.Background()
		s := newStore(t)
		e := NewEntry()
		e.TenantID = uniqueTenant(t)
		if err := s.Push(ctx, e); err != nil {
			t.Fatalf("push: %v", err)
		}
		first := time.Now().UTC().Truncate(time.Second)
		if err := s.MarkReplayed(ctx, e.ID, first); err != nil {
			t.Fatalf("first claim: %v", err)
		}
		err := s.MarkReplayed(ctx, e.ID, first.Add(time.Minute))
		if !errors.Is(err, relay.ErrAlreadyReplayed) {
			t.Fatalf("second claim: error = %v, want ErrAlreadyReplayed", err)
		}
		// The refused claim must not have moved the timestamp.
		got, err := s.GetDLQ(ctx, e.ID)
		if err != nil {
			t.Fatalf("get: %v", err)
		}
		if got.ReplayedAt == nil || got.ReplayedAt.Unix() != first.Unix() {
			t.Fatalf("ReplayedAt = %v, want the first claim's %v", got.ReplayedAt, first)
		}
	})

	// The case the claim exists for. Every caller races for the same entry
	// at once; exactly one may win.
	t.Run("concurrent claims: exactly one wins", func(t *testing.T) {
		ctx := context.Background()
		s := newStore(t)
		e := NewEntry()
		e.TenantID = uniqueTenant(t)
		if err := s.Push(ctx, e); err != nil {
			t.Fatalf("push: %v", err)
		}

		const n = 8
		var (
			wg    sync.WaitGroup
			start = make(chan struct{})
			errs  = make([]error, n)
		)
		for i := 0; i < n; i++ {
			wg.Add(1)
			go func(i int) {
				defer wg.Done()
				<-start
				errs[i] = s.MarkReplayed(ctx, e.ID, time.Now().UTC())
			}(i)
		}
		close(start)
		wg.Wait()

		won := 0
		for i, err := range errs {
			switch {
			case err == nil:
				won++
			case errors.Is(err, relay.ErrAlreadyReplayed):
			default:
				t.Errorf("claim %d: unexpected error %v", i, err)
			}
		}
		if won != 1 {
			t.Fatalf("%d of %d concurrent claims won, want exactly 1: every "+
				"winner sends the webhook", won, n)
		}
	})

	// A failed send releases its claim; the entry must then be claimable
	// again, or a transient failure would strand it forever.
	t.Run("ReleaseReplay makes an entry claimable again", func(t *testing.T) {
		ctx := context.Background()
		s := newStore(t)
		e := NewEntry()
		e.TenantID = uniqueTenant(t)
		if err := s.Push(ctx, e); err != nil {
			t.Fatalf("push: %v", err)
		}
		if err := s.MarkReplayed(ctx, e.ID, time.Now().UTC()); err != nil {
			t.Fatalf("claim: %v", err)
		}
		if err := s.ReleaseReplay(ctx, e.ID); err != nil {
			t.Fatalf("release: %v", err)
		}
		got, err := s.GetDLQ(ctx, e.ID)
		if err != nil {
			t.Fatalf("get: %v", err)
		}
		if got.ReplayedAt != nil {
			t.Fatalf("ReplayedAt = %v after release, want nil", got.ReplayedAt)
		}
		if err := s.MarkReplayed(ctx, e.ID, time.Now().UTC()); err != nil {
			t.Fatalf("re-claim after release: %v", err)
		}
	})

	t.Run("ReleaseReplay on a missing row reports not found", func(t *testing.T) {
		ctx := context.Background()
		s := newStore(t)
		if err := s.ReleaseReplay(ctx, id.NewDLQID()); !errors.Is(err, relay.ErrDLQNotFound) {
			t.Fatalf("error = %v, want ErrDLQNotFound", err)
		}
	})

	t.Run("MarkReplayed on a missing row reports not found", func(t *testing.T) {
		ctx := context.Background()
		s := newStore(t)
		err := s.MarkReplayed(ctx, id.NewDLQID(), time.Now().UTC())
		if !errors.Is(err, relay.ErrDLQNotFound) {
			t.Fatalf("MarkReplayed on a missing row: error = %v, want ErrDLQNotFound", err)
		}
	})

	// PushFailed hands the store the event's data as JSON bytes. Postgres,
	// sqlite and redis marshalled those bytes again and kept a base64 string,
	// and mongo kept a binary, so only the memory store gave the JSON back.
	t.Run("a payload reads back as the JSON that was pushed", func(t *testing.T) {
		ctx := context.Background()
		s := newStore(t)
		const want = `{"id":"inv_1","lines":[{"sku":"A-1","qty":2}]}`
		for _, payload := range []any{json.RawMessage(want), []byte(want)} {
			e := NewEntry()
			e.Payload = payload
			if err := s.Push(ctx, e); err != nil {
				t.Fatalf("push %T: %v", payload, err)
			}
			got, err := s.GetDLQ(ctx, e.ID)
			if err != nil {
				t.Fatalf("get %T: %v", payload, err)
			}
			b, err := json.Marshal(got.Payload)
			if err != nil {
				t.Fatalf("marshal read-back payload: %v", err)
			}
			if !jsonEqual(t, b, []byte(want)) {
				t.Fatalf("pushed %T %s, read back %s", payload, want, b)
			}
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
			MaxAttempts:   7,
			NextAttemptAt: time.Now().UTC(),
		}
		if err := s.Enqueue(ctx, d); err != nil {
			t.Fatalf("enqueue: %v", err)
		}
		got, err := s.GetDelivery(ctx, d.ID)
		if err != nil {
			t.Fatalf("get delivery: %v", err)
		}
		// 7, not 5: the old memory store hardcoded 5, so 5 could round-trip by
		// coincidence without the column being written at all.
		if got.MaxAttempts != 7 {
			t.Fatalf("MaxAttempts = %d, want 7: a backend that drops this "+
				"reintroduces the 1 < 0 bug", got.MaxAttempts)
		}
	})
}

// jsonEqual reports whether a and b hold the same JSON value, ignoring key
// order and spacing, which jsonb does not keep.
func jsonEqual(t *testing.T, a, b []byte) bool {
	t.Helper()
	var x, y any
	if err := json.Unmarshal(a, &x); err != nil {
		return false
	}
	if err := json.Unmarshal(b, &y); err != nil {
		t.Fatalf("want is not JSON: %v", err)
	}
	return reflect.DeepEqual(x, y)
}

package dlq_test

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/xraph/relay"
	"github.com/xraph/relay/delivery"
	"github.com/xraph/relay/dlq"
	"github.com/xraph/relay/endpoint"
	"github.com/xraph/relay/event"
	"github.com/xraph/relay/id"
	"github.com/xraph/relay/internal/entity"
	"github.com/xraph/relay/store/memory"
)

func ctx() context.Context { return context.Background() }

// seedEntry pushes an un-replayed DLQ entry and returns it.
func seedEntry(t *testing.T, store *memory.Store) *dlq.Entry {
	t.Helper()
	e := &dlq.Entry{
		Entity:       entity.New(),
		ID:           id.NewDLQID(),
		DeliveryID:   id.NewDeliveryID(),
		EventID:      id.NewEventID(),
		EndpointID:   id.NewEndpointID(),
		EventType:    "invoice.created",
		TenantID:     "tenant-1",
		URL:          "https://receiver.example/hook",
		Error:        "connection refused",
		AttemptCount: 5,
		FailedAt:     time.Now().UTC().Add(-time.Hour),
	}
	if err := store.Push(ctx(), e); err != nil {
		t.Fatalf("seed: %v", err)
	}
	return e
}

func newService() (*dlq.Service, *memory.Store) {
	store := memory.New()
	svc := dlq.NewService(store, store, dlq.Config{MaxAttempts: 5}, nil)
	return svc, store
}

func TestPushFailed(t *testing.T) {
	svc, store := newService()

	d := &delivery.Delivery{
		Entity:         entity.New(),
		ID:             id.NewDeliveryID(),
		EventID:        id.NewEventID(),
		EndpointID:     id.NewEndpointID(),
		AttemptCount:   5,
		LastStatusCode: 500,
	}
	ep := &endpoint.Endpoint{
		Entity:   entity.New(),
		ID:       d.EndpointID,
		TenantID: "tenant-1",
		URL:      "https://example.com/webhook",
	}
	evt := &event.Event{
		Entity: entity.New(),
		ID:     d.EventID,
		Type:   "invoice.created",
		Data:   json.RawMessage(`{"amount":100}`),
	}

	err := svc.PushFailed(ctx(), d, ep, evt, "server error", 500)
	if err != nil {
		t.Fatal(err)
	}

	// Verify entry was stored.
	entries, err := store.ListDLQ(ctx(), dlq.ListOpts{Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 {
		t.Fatalf("expected 1 entry, got %d", len(entries))
	}

	entry := entries[0]
	if entry.DeliveryID != d.ID {
		t.Fatalf("delivery ID mismatch: got %v, want %v", entry.DeliveryID, d.ID)
	}
	if entry.EventID != d.EventID {
		t.Fatalf("event ID mismatch")
	}
	if entry.EndpointID != d.EndpointID {
		t.Fatalf("endpoint ID mismatch")
	}
	if entry.EventType != "invoice.created" {
		t.Fatalf("event type: got %q, want %q", entry.EventType, "invoice.created")
	}
	if entry.TenantID != "tenant-1" {
		t.Fatalf("tenant ID: got %q, want %q", entry.TenantID, "tenant-1")
	}
	if entry.URL != "https://example.com/webhook" {
		t.Fatalf("URL mismatch")
	}
	if entry.Error != "server error" {
		t.Fatalf("error: got %q, want %q", entry.Error, "server error")
	}
	if entry.AttemptCount != 5 {
		t.Fatalf("attempt count: got %d, want 5", entry.AttemptCount)
	}
	if entry.LastStatusCode != 500 {
		t.Fatalf("status code: got %d, want 500", entry.LastStatusCode)
	}
}

func TestPushMultipleAndList(t *testing.T) {
	svc, _ := newService()

	for range 3 {
		d := &delivery.Delivery{
			Entity:     entity.New(),
			ID:         id.NewDeliveryID(),
			EventID:    id.NewEventID(),
			EndpointID: id.NewEndpointID(),
		}
		ep := &endpoint.Endpoint{ID: d.EndpointID, TenantID: "t1", URL: "https://example.com"}
		evt := &event.Event{ID: d.EventID, Type: "test.event", Data: json.RawMessage(`{}`)}
		if err := svc.PushFailed(ctx(), d, ep, evt, "err", 500); err != nil {
			t.Fatal(err)
		}
	}

	entries, err := svc.List(ctx(), dlq.ListOpts{Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 3 {
		t.Fatalf("expected 3 entries, got %d", len(entries))
	}
}

func TestGetDLQEntry(t *testing.T) {
	svc, _ := newService()

	d := &delivery.Delivery{
		Entity:     entity.New(),
		ID:         id.NewDeliveryID(),
		EventID:    id.NewEventID(),
		EndpointID: id.NewEndpointID(),
	}
	ep := &endpoint.Endpoint{ID: d.EndpointID, TenantID: "t1", URL: "https://example.com"}
	evt := &event.Event{ID: d.EventID, Type: "test.event", Data: json.RawMessage(`{}`)}

	if err := svc.PushFailed(ctx(), d, ep, evt, "err", 500); err != nil {
		t.Fatal(err)
	}

	entries, _ := svc.List(ctx(), dlq.ListOpts{Limit: 1})
	if len(entries) == 0 {
		t.Fatal("expected at least 1 entry")
	}

	got, err := svc.Get(ctx(), entries[0].ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.ID != entries[0].ID {
		t.Fatal("ID mismatch on Get")
	}
}

func TestCount(t *testing.T) {
	svc, _ := newService()

	count, err := svc.Count(ctx())
	if err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatalf("expected 0, got %d", count)
	}

	for range 5 {
		d := &delivery.Delivery{
			Entity:     entity.New(),
			ID:         id.NewDeliveryID(),
			EventID:    id.NewEventID(),
			EndpointID: id.NewEndpointID(),
		}
		ep := &endpoint.Endpoint{ID: d.EndpointID, TenantID: "t1", URL: "https://example.com"}
		evt := &event.Event{ID: d.EventID, Type: "test.event", Data: json.RawMessage(`{}`)}
		svc.PushFailed(ctx(), d, ep, evt, "err", 500)
	}

	count, err = svc.Count(ctx())
	if err != nil {
		t.Fatal(err)
	}
	if count != 5 {
		t.Fatalf("expected 5, got %d", count)
	}
}

func TestReplay(t *testing.T) {
	svc, store := newService()

	d := &delivery.Delivery{
		Entity:     entity.New(),
		ID:         id.NewDeliveryID(),
		EventID:    id.NewEventID(),
		EndpointID: id.NewEndpointID(),
	}
	ep := &endpoint.Endpoint{ID: d.EndpointID, TenantID: "t1", URL: "https://example.com"}
	evt := &event.Event{ID: d.EventID, Type: "test.event", Data: json.RawMessage(`{}`)}

	svc.PushFailed(ctx(), d, ep, evt, "err", 500)

	entries, _ := svc.List(ctx(), dlq.ListOpts{Limit: 1})
	if len(entries) == 0 {
		t.Fatal("expected entry")
	}

	// Replay should mark the entry.
	err := svc.Replay(ctx(), entries[0].ID)
	if err != nil {
		t.Fatal(err)
	}

	// Verify replayed_at is set.
	got, _ := store.GetDLQ(ctx(), entries[0].ID)
	if got.ReplayedAt == nil {
		t.Fatal("expected replayed_at to be set")
	}
}

func TestPurge(t *testing.T) {
	svc, _ := newService()

	for range 3 {
		d := &delivery.Delivery{
			Entity:     entity.New(),
			ID:         id.NewDeliveryID(),
			EventID:    id.NewEventID(),
			EndpointID: id.NewEndpointID(),
		}
		ep := &endpoint.Endpoint{ID: d.EndpointID, TenantID: "t1", URL: "https://example.com"}
		evt := &event.Event{ID: d.EventID, Type: "test.event", Data: json.RawMessage(`{}`)}
		svc.PushFailed(ctx(), d, ep, evt, "err", 500)
	}

	// Purge entries before "now + 1 second" should remove all.
	purged, err := svc.Purge(ctx(), time.Now().Add(time.Second))
	if err != nil {
		t.Fatal(err)
	}
	if purged != 3 {
		t.Fatalf("expected 3 purged, got %d", purged)
	}

	count, _ := svc.Count(ctx())
	if count != 0 {
		t.Fatalf("expected 0 after purge, got %d", count)
	}
}

// A zero MaxAttempts is the exact value that caused the replay bug: the
// retrier evaluates `1 < 0` and sends the delivery straight back to the DLQ.
// So the service refuses to honour it and falls back to a sane default.
func TestConfigDefaultsMaxAttempts(t *testing.T) {
	store := memory.New()
	svc := dlq.NewService(store, store, dlq.Config{MaxAttempts: 0}, nil)
	if got := svc.MaxAttempts(); got != dlq.DefaultMaxAttempts {
		t.Fatalf("MaxAttempts() = %d, want %d", got, dlq.DefaultMaxAttempts)
	}
}

func TestConfigRefusesANegativeMaxAttempts(t *testing.T) {
	store := memory.New()
	svc := dlq.NewService(store, store, dlq.Config{MaxAttempts: -3}, nil)
	if got := svc.MaxAttempts(); got != dlq.DefaultMaxAttempts {
		t.Fatalf("MaxAttempts() = %d, want %d", got, dlq.DefaultMaxAttempts)
	}
}

func TestConfigHonoursMaxAttempts(t *testing.T) {
	store := memory.New()
	svc := dlq.NewService(store, store, dlq.Config{MaxAttempts: 3}, nil)
	if got := svc.MaxAttempts(); got != 3 {
		t.Fatalf("MaxAttempts() = %d, want 3", got)
	}
}

// The budget must come from config. 7 is deliberate: the old memory store
// hardcoded 5 and the other backends left it at 0, so a test at 5 would pass
// on the old code by coincidence and prove nothing.
func TestReplayEnqueuesWithTheConfiguredBudget(t *testing.T) {
	store := memory.New()
	svc := dlq.NewService(store, store, dlq.Config{MaxAttempts: 7}, nil)
	e := seedEntry(t, store)

	if err := svc.Replay(ctx(), e.ID); err != nil {
		t.Fatalf("replay: %v", err)
	}

	dels := store.AllDeliveries()
	if len(dels) != 1 {
		t.Fatalf("got %d deliveries, want 1", len(dels))
	}
	if dels[0].MaxAttempts != 7 {
		t.Fatalf("MaxAttempts = %d, want 7 from config", dels[0].MaxAttempts)
	}
	if dels[0].AttemptCount != 0 {
		t.Fatalf("AttemptCount = %d, want 0: a replay starts over", dels[0].AttemptCount)
	}
	if dels[0].State != delivery.StatePending {
		t.Fatalf("State = %q, want pending", dels[0].State)
	}
	if dels[0].EventID != e.EventID || dels[0].EndpointID != e.EndpointID {
		t.Fatal("the replayed delivery does not target the original event and endpoint")
	}
	if dels[0].EventType != e.EventType || dels[0].TenantID != e.TenantID {
		t.Errorf("replayed delivery carries (%q, %q), want the entry's (%q, %q)",
			dels[0].EventType, dels[0].TenantID, e.EventType, e.TenantID)
	}
}

// Regression guard. The old memory store already kept and marked the row, so
// this passes before and after on memory; the conformance suite is what proves
// row-keeping on the real backends.
func TestReplayKeepsAndMarksTheEntry(t *testing.T) {
	svc, store := newService()
	e := seedEntry(t, store)

	if err := svc.Replay(ctx(), e.ID); err != nil {
		t.Fatalf("replay: %v", err)
	}
	got, err := store.GetDLQ(ctx(), e.ID)
	if err != nil {
		t.Fatalf("the entry must survive a replay: %v", err)
	}
	if got.ReplayedAt == nil {
		t.Fatal("ReplayedAt is nil after a replay")
	}
}

// Replaying sends a real webhook. A second replay of the same entry must be
// refused before anything is enqueued, not after.
func TestReplayTwiceIsRefused(t *testing.T) {
	svc, store := newService()
	e := seedEntry(t, store)

	if err := svc.Replay(ctx(), e.ID); err != nil {
		t.Fatalf("first replay: %v", err)
	}
	err := svc.Replay(ctx(), e.ID)
	if !errors.Is(err, relay.ErrAlreadyReplayed) {
		t.Fatalf("second replay error = %v, want ErrAlreadyReplayed", err)
	}
	if !errors.Is(err, dlq.ErrAlreadyReplayed) {
		t.Fatal("relay.ErrAlreadyReplayed and dlq.ErrAlreadyReplayed are not the same value")
	}
	if n := len(store.AllDeliveries()); n != 1 {
		t.Fatalf("got %d deliveries after a refused replay, want 1: "+
			"the refusal must happen before the enqueue", n)
	}
}

func TestReplayMissingEntry(t *testing.T) {
	svc, _ := newService()
	err := svc.Replay(ctx(), id.NewDLQID())
	if !errors.Is(err, relay.ErrDLQNotFound) {
		t.Fatalf("error = %v, want ErrDLQNotFound", err)
	}
}

// Bulk replay takes the same budget from config as single replay. 7 is
// deliberate: the old memory ReplayBulk hardcoded 5 and the other backends left
// it at 0, so only a value neither could produce proves the budget is read.
func TestReplayBulkUsesTheConfiguredBudget(t *testing.T) {
	store := memory.New()
	svc := dlq.NewService(store, store, dlq.Config{MaxAttempts: 7}, nil)
	seedEntry(t, store)
	seedEntry(t, store)

	n, err := svc.ReplayBulk(ctx(), time.Now().UTC().Add(-24*time.Hour), time.Now().UTC())
	if err != nil {
		t.Fatalf("replay bulk: %v", err)
	}
	if n != 2 {
		t.Fatalf("replayed %d, want 2", n)
	}
	for _, d := range store.AllDeliveries() {
		if d.MaxAttempts != 7 {
			t.Fatalf("a bulk-replayed delivery has MaxAttempts = %d, want 7 from config", d.MaxAttempts)
		}
	}
}

func TestReplayBulkSkipsAlreadyReplayed(t *testing.T) {
	svc, store := newService()
	a := seedEntry(t, store)
	seedEntry(t, store)

	if err := svc.Replay(ctx(), a.ID); err != nil {
		t.Fatalf("seed replay: %v", err)
	}

	n, err := svc.ReplayBulk(ctx(), time.Now().UTC().Add(-24*time.Hour), time.Now().UTC())
	if err != nil {
		t.Fatalf("replay bulk: %v", err)
	}
	if n != 1 {
		t.Fatalf("replayed %d, want 1: the already-replayed entry must be skipped", n)
	}
	if got := len(store.AllDeliveries()); got != 2 {
		t.Fatalf("got %d deliveries, want 2 (one per entry, none twice)", got)
	}
}

// Calling bulk replay twice over the same window must not re-send every
// webhook in it. The old real backends only got this right by accident: they
// deleted the rows, so the second call found nothing left to send.
func TestReplayBulkTwiceSendsNothingTheSecondTime(t *testing.T) {
	svc, store := newService()
	seedEntry(t, store)
	seedEntry(t, store)

	from := time.Now().UTC().Add(-24 * time.Hour)
	to := time.Now().UTC()
	if _, err := svc.ReplayBulk(ctx(), from, to); err != nil {
		t.Fatalf("first bulk: %v", err)
	}
	n, err := svc.ReplayBulk(ctx(), from, to)
	if err != nil {
		t.Fatalf("second bulk: %v", err)
	}
	if n != 0 {
		t.Fatalf("second bulk replayed %d, want 0", n)
	}
	if got := len(store.AllDeliveries()); got != 2 {
		t.Fatalf("got %d deliveries, want 2: a second bulk must not re-send", got)
	}
}

// failingEnqueuer refuses every delivery, standing in for a store that cannot
// accept the redelivery.
type failingEnqueuer struct{ err error }

func (f failingEnqueuer) Enqueue(context.Context, *delivery.Delivery) error { return f.err }

// The race the claim exists to close. Before it, several concurrent replays of
// one entry all passed the "not yet replayed" check, all enqueued, and all
// reported success: postgres double-sent 40 times in 40.
func TestReplayConcurrentSendsOnce(t *testing.T) {
	svc, store := newService()
	e := seedEntry(t, store)

	const n = 16
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
			errs[i] = svc.Replay(ctx(), e.ID)
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
			t.Errorf("replay %d: unexpected error %v", i, err)
		}
	}
	if won != 1 {
		t.Fatalf("%d of %d concurrent replays reported success, want exactly 1", won, n)
	}
	if got := len(store.AllDeliveries()); got != 1 {
		t.Fatalf("%d deliveries enqueued, want 1: every extra one is a duplicate webhook", got)
	}
}

// Two operators bulk-replaying the same window at once must send each entry
// once between them. Before, 200 entries produced 255 deliveries.
func TestReplayBulkConcurrentSendsOncePerEntry(t *testing.T) {
	svc, store := newService()
	const entries = 50
	for i := 0; i < entries; i++ {
		seedEntry(t, store)
	}
	from := time.Now().UTC().Add(-24 * time.Hour)
	to := time.Now().UTC()

	var (
		wg     sync.WaitGroup
		start  = make(chan struct{})
		counts [2]int64
		errs   [2]error
	)
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-start
			counts[i], errs[i] = svc.ReplayBulk(ctx(), from, to)
		}(i)
	}
	close(start)
	wg.Wait()

	for i, err := range errs {
		if err != nil {
			t.Fatalf("bulk %d: %v", i, err)
		}
	}
	if got := len(store.AllDeliveries()); got != entries {
		t.Fatalf("%d deliveries for %d entries, want exactly one each", got, entries)
	}
	if counts[0]+counts[1] != entries {
		t.Fatalf("the two bulks reported %d + %d = %d replayed, want %d between them",
			counts[0], counts[1], counts[0]+counts[1], entries)
	}
}

// Guard for the ordering. It passes before and after the claim moves in front
// of the send, and it is here so that move cannot silently change what a
// failed send leaves behind: nothing enqueued, the entry unclaimed, and a
// later replay free to try again.
func TestReplayReleasesTheClaimWhenEnqueueFails(t *testing.T) {
	store := memory.New()
	boom := errors.New("queue unavailable")
	svc := dlq.NewService(store, failingEnqueuer{err: boom}, dlq.Config{MaxAttempts: 5}, nil)
	e := seedEntry(t, store)

	err := svc.Replay(ctx(), e.ID)
	if !errors.Is(err, boom) {
		t.Fatalf("error = %v, want the enqueue failure", err)
	}
	if n := len(store.AllDeliveries()); n != 0 {
		t.Fatalf("%d deliveries after a failed enqueue, want 0", n)
	}
	got, getErr := store.GetDLQ(ctx(), e.ID)
	if getErr != nil {
		t.Fatalf("get: %v", getErr)
	}
	if got.ReplayedAt != nil {
		t.Fatal("the entry is still marked replayed after its send failed: " +
			"nothing was sent, and it can never be replayed now")
	}

	// And it really is free: a working service replays it.
	ok := dlq.NewService(store, store, dlq.Config{MaxAttempts: 5}, nil)
	if err := ok.Replay(ctx(), e.ID); err != nil {
		t.Fatalf("replay after a failed attempt: %v", err)
	}
	if n := len(store.AllDeliveries()); n != 1 {
		t.Fatalf("%d deliveries, want 1", n)
	}
}

// With the claim first, a bulk that hits a failing send must not count it.
// Before, n came back 0 while a delivery had in fact been queued.
func TestReplayBulkCountsOnlyWhatWasSent(t *testing.T) {
	store := memory.New()
	boom := errors.New("queue unavailable")
	svc := dlq.NewService(store, failingEnqueuer{err: boom}, dlq.Config{MaxAttempts: 5}, nil)
	seedEntry(t, store)

	n, err := svc.ReplayBulk(ctx(), time.Now().UTC().Add(-24*time.Hour), time.Now().UTC())
	if !errors.Is(err, boom) {
		t.Fatalf("error = %v, want the enqueue failure", err)
	}
	if n != 0 {
		t.Fatalf("n = %d, want 0: nothing was sent", n)
	}
	if got := len(store.AllDeliveries()); got != 0 {
		t.Fatalf("%d deliveries queued, want 0", got)
	}
}

// unreleasableStore is a memory store whose ReleaseReplay always fails.
type unreleasableStore struct {
	*memory.Store
	err error
}

func (u unreleasableStore) ReleaseReplay(context.Context, id.ID) error { return u.err }

// The one way the claim-first order can lose a redelivery: the send fails and
// then the release fails as well. The entry is left marked with nothing sent,
// so the caller must hear about both failures, not only the first.
func TestReplayReportsBothErrorsWhenReleaseAlsoFails(t *testing.T) {
	mem := memory.New()
	sendErr := errors.New("queue unavailable")
	relErr := errors.New("store unavailable")
	svc := dlq.NewService(unreleasableStore{Store: mem, err: relErr},
		failingEnqueuer{err: sendErr}, dlq.Config{MaxAttempts: 5}, nil)
	e := seedEntry(t, mem)

	err := svc.Replay(ctx(), e.ID)
	if !errors.Is(err, sendErr) {
		t.Errorf("error does not carry the send failure: %v", err)
	}
	if !errors.Is(err, relErr) {
		t.Errorf("error does not carry the release failure: %v", err)
	}
	if err != nil && !strings.Contains(err.Error(), "nothing was sent") {
		t.Errorf("error does not say the entry is marked with nothing sent: %v", err)
	}
}

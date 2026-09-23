package storetest

import (
	"context"
	"errors"
	"sort"
	"testing"
	"time"

	"github.com/xraph/relay"

	"github.com/xraph/relay/delivery"
	"github.com/xraph/relay/id"
	"github.com/xraph/relay/internal/entity"
)

// DeliveryBackend is the slice of a store the delivery suite exercises.
type DeliveryBackend interface {
	delivery.Store
}

// RunDeliverySuite pins what the dashboard reads from deliveries.
func RunDeliverySuite(t *testing.T, open func(t *testing.T) DeliveryBackend) {
	t.Run("a delivery keeps its event type and tenant", func(t *testing.T) {
		s := open(t)
		d := newDelivery(uniqueTenant(t), "invoice.paid", id.NewEndpointID(), id.NewEventID())
		if err := s.Enqueue(context.Background(), d); err != nil {
			t.Fatalf("enqueue: %v", err)
		}
		got, err := s.GetDelivery(context.Background(), d.ID)
		if err != nil {
			t.Fatalf("get: %v", err)
		}
		if got.EventType != d.EventType || got.TenantID != d.TenantID {
			t.Errorf("got (%q, %q), want (%q, %q)", got.EventType, got.TenantID, d.EventType, d.TenantID)
		}
	})

	t.Run("attempts list in order, per delivery", func(t *testing.T) {
		s := open(t)
		ctx := context.Background()
		d := newDelivery(uniqueTenant(t), "invoice.paid", id.NewEndpointID(), id.NewEventID())
		other := newDelivery(d.TenantID, "invoice.sent", d.EndpointID, d.EventID)
		for _, x := range []*delivery.Delivery{d, other} {
			if err := s.Enqueue(ctx, x); err != nil {
				t.Fatalf("enqueue: %v", err)
			}
		}
		base := time.Now().UTC().Truncate(time.Millisecond)
		next := base.Add(time.Minute)
		// Recorded out of order: the list sorts by attempt number, not by
		// write order.
		for _, a := range []*delivery.Attempt{
			newAttempt(d.ID, 3, 200, delivery.OutcomeDelivered, nil, base.Add(3*time.Second)),
			newAttempt(d.ID, 1, 500, delivery.OutcomeRetry, &next, base.Add(1*time.Second)),
			newAttempt(d.ID, 2, 0, delivery.OutcomeRetry, &next, base.Add(2*time.Second)),
			newAttempt(other.ID, 1, 200, delivery.OutcomeDelivered, nil, base),
		} {
			if err := s.RecordAttempt(ctx, a); err != nil {
				t.Fatalf("record: %v", err)
			}
		}

		got, err := s.ListAttempts(ctx, d.ID)
		if err != nil {
			t.Fatalf("list: %v", err)
		}
		if len(got) != 3 {
			t.Fatalf("got %d attempts, want 3 (another delivery's attempt leaked in?)", len(got))
		}
		for i, a := range got {
			if a.AttemptNum != i+1 || a.DeliveryID != d.ID {
				t.Errorf("attempt %d is #%d of %s", i, a.AttemptNum, a.DeliveryID)
			}
		}
		if got[0].StatusCode != 500 || got[0].Outcome != delivery.OutcomeRetry {
			t.Errorf("first attempt (%d, %q), want (500, retry)", got[0].StatusCode, got[0].Outcome)
		}
		if got[0].NextAttemptAt == nil || !got[0].NextAttemptAt.Equal(next) {
			t.Errorf("first attempt's next attempt %v, want %v", got[0].NextAttemptAt, next)
		}
		if got[2].NextAttemptAt != nil {
			t.Errorf("the delivered attempt has a next attempt %v, want none", got[2].NextAttemptAt)
		}
		if !got[1].AttemptedAt.Equal(base.Add(2 * time.Second)) {
			t.Errorf("attempted at %v, want %v", got[1].AttemptedAt, base.Add(2*time.Second))
		}
		if got[1].Error != "connection refused" || got[1].Response != "body" || got[1].LatencyMs != 42 {
			t.Errorf("attempt fields did not round-trip: %+v", got[1])
		}
	})

	t.Run("an unknown delivery has no attempts, not an error", func(t *testing.T) {
		got, err := open(t).ListAttempts(context.Background(), id.NewDeliveryID())
		if err != nil || len(got) != 0 {
			t.Errorf("got (%v, %v), want an empty list", got, err)
		}
	})

	t.Run("purge removes exactly the attempts before the cutoff", func(t *testing.T) {
		s := open(t)
		ctx := context.Background()
		d := newDelivery(uniqueTenant(t), "invoice.paid", id.NewEndpointID(), id.NewEventID())
		if err := s.Enqueue(ctx, d); err != nil {
			t.Fatalf("enqueue: %v", err)
		}
		// Far in the past, so no other subtest's attempts are older.
		old := time.Date(2001, 1, 1, 0, 0, 0, 0, time.UTC)
		for n, at := range []time.Time{old, old.Add(time.Hour), time.Now().UTC()} {
			if err := s.RecordAttempt(ctx, newAttempt(d.ID, n+1, 500, delivery.OutcomeRetry, nil, at)); err != nil {
				t.Fatalf("record: %v", err)
			}
		}
		n, err := s.PurgeAttempts(ctx, old.Add(2*time.Hour))
		if err != nil {
			t.Fatalf("purge: %v", err)
		}
		if n != 2 {
			t.Errorf("purged %d, want 2", n)
		}
		left, err := s.ListAttempts(ctx, d.ID)
		if err != nil {
			t.Fatalf("list: %v", err)
		}
		if len(left) != 1 || left[0].AttemptNum != 3 {
			t.Errorf("left %v, want only attempt 3", left)
		}
	})

	t.Run("the log pages newest first, every row exactly once", func(t *testing.T) {
		s := open(t)
		tenant := uniqueTenant(t)
		base := time.Now().UTC().Truncate(time.Millisecond).Add(-time.Hour)
		want := seedLog(t, s, tenant, 5, func(i int, d *delivery.Delivery) {
			d.CreatedAt = base.Add(time.Duration(i) * time.Second)
		})
		pages := pageAll(t, s, delivery.Query{TenantID: tenant, Limit: 2})
		if sizes := pageSizes(pages); !equalInts(sizes, []int{2, 2, 1}) {
			t.Errorf("page sizes %v, want [2 2 1]", sizes)
		}
		assertOrder(t, flatten(pages), reverse(want))
	})

	// Two rows created in the same instant straddling a page boundary are
	// where a created_at-only cursor skips or repeats a row.
	t.Run("rows created in the same instant still page exactly once", func(t *testing.T) {
		s := open(t)
		tenant := uniqueTenant(t)
		at := time.Now().UTC().Truncate(time.Millisecond).Add(-time.Hour)
		want := seedLog(t, s, tenant, 5, func(_ int, d *delivery.Delivery) { d.CreatedAt = at })
		got := flatten(pageAll(t, s, delivery.Query{TenantID: tenant, Limit: 2}))
		sortByIDDesc(want)
		assertOrder(t, got, want)
	})

	t.Run("each filter narrows the log", func(t *testing.T) {
		s := open(t)
		ctx := context.Background()
		tenant := uniqueTenant(t)
		base := time.Now().UTC().Truncate(time.Millisecond).Add(-time.Hour)
		epA, epB := id.NewEndpointID(), id.NewEndpointID()
		evtA, evtB := id.NewEventID(), id.NewEventID()
		failed := delivery.StateFailed
		rows := []*delivery.Delivery{
			newDelivery(tenant, "invoice.paid", epA, evtA),
			newDelivery(tenant, "invoice.paid", epB, evtA),
			newDelivery(tenant, "customer.created", epA, evtB),
			newDelivery(tenant, "customer.created", epB, evtB),
		}
		for i, d := range rows {
			d.CreatedAt = base.Add(time.Duration(i) * time.Minute)
			if i == 3 {
				d.State = delivery.StateFailed
			}
			if err := s.Enqueue(ctx, d); err != nil {
				t.Fatalf("enqueue: %v", err)
			}
		}
		from, to := base.Add(time.Minute), base.Add(2*time.Minute)
		for _, tc := range []struct {
			name string
			q    delivery.Query
			want []*delivery.Delivery
		}{
			{"endpoint", delivery.Query{EndpointID: &epA}, []*delivery.Delivery{rows[2], rows[0]}},
			{"event", delivery.Query{EventID: &evtB}, []*delivery.Delivery{rows[3], rows[2]}},
			{"event type", delivery.Query{EventType: "invoice.paid"}, []*delivery.Delivery{rows[1], rows[0]}},
			{"state", delivery.Query{State: &failed}, []*delivery.Delivery{rows[3]}},
			{"from and to, inclusive", delivery.Query{From: &from, To: &to}, []*delivery.Delivery{rows[2], rows[1]}},
			{"combined", delivery.Query{EndpointID: &epB, EventType: "customer.created"}, []*delivery.Delivery{rows[3]}},
		} {
			tc.q.TenantID = tenant
			got := flatten(pageAll(t, s, tc.q))
			t.Run(tc.name, func(t *testing.T) { assertOrder(t, got, tc.want) })
		}
		// Tenant is itself a filter: none of these rows under another one.
		if got := flatten(pageAll(t, s, delivery.Query{TenantID: uniqueTenant(t)})); len(got) != 0 {
			t.Errorf("another tenant sees %d of these rows", len(got))
		}
	})

	t.Run("status classes, with none covering both kinds of zero", func(t *testing.T) {
		s := open(t)
		ctx := context.Background()
		tenant := uniqueTenant(t)
		codes := []int{204, 404, 503, 0, 0}
		rows := make([]*delivery.Delivery, len(codes))
		for i, code := range codes {
			d := newDelivery(tenant, "invoice.paid", id.NewEndpointID(), id.NewEventID())
			d.CreatedAt = time.Now().UTC().Truncate(time.Millisecond).Add(time.Duration(-i) * time.Second)
			d.LastStatusCode = code
			if i == 4 {
				d.AttemptCount = 1
				d.LastError = "dial tcp: connection refused"
			}
			if err := s.Enqueue(ctx, d); err != nil {
				t.Fatalf("enqueue: %v", err)
			}
			rows[i] = d
		}
		for class, want := range map[delivery.StatusClass][]*delivery.Delivery{
			delivery.Status2xx:  {rows[0]},
			delivery.Status4xx:  {rows[1]},
			delivery.Status5xx:  {rows[2]},
			delivery.StatusNone: {rows[3], rows[4]},
		} {
			got := flatten(pageAll(t, s, delivery.Query{TenantID: tenant, StatusClass: class}))
			t.Run(string(class), func(t *testing.T) { assertOrder(t, got, want) })
		}
	})

	t.Run("refuses a cursor it did not issue and an unknown status class", func(t *testing.T) {
		s := open(t)
		if _, err := s.ListDeliveries(context.Background(), delivery.Query{Cursor: "not-a-cursor"}); !errors.Is(err, relay.ErrInvalidCursor) {
			t.Errorf("garbage cursor: %v, want ErrInvalidCursor", err)
		}
		if _, err := s.ListDeliveries(context.Background(), delivery.Query{StatusClass: "3xx"}); !errors.Is(err, relay.ErrInvalidFilter) {
			t.Errorf("unknown status class: %v, want ErrInvalidFilter", err)
		}
	})

	// Redis used to write "delivered" on claim, so an in-flight delivery,
	// and one about to fail, read as delivered to anyone who looked.
	t.Run("a claimed delivery does not read as finished", func(t *testing.T) {
		claimedStateCase(t, open(t))
	})
}

func claimedStateCase(t *testing.T, s DeliveryBackend) {
	ctx := context.Background()
	d := newDelivery(uniqueTenant(t), "invoice.paid", id.NewEndpointID(), id.NewEventID())
	d.NextAttemptAt = time.Now().UTC().Add(-time.Second)
	if err := s.Enqueue(ctx, d); err != nil {
		t.Fatalf("enqueue: %v", err)
	}
	claimed, err := s.Dequeue(ctx, 1000)
	if err != nil {
		t.Fatalf("dequeue: %v", err)
	}
	found := false
	for _, c := range claimed {
		found = found || c.ID == d.ID
	}
	if !found {
		t.Fatalf("delivery %s was due and not dequeued", d.ID)
	}
	got, err := s.GetDelivery(ctx, d.ID)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got.State != delivery.StateDelivering && got.State != delivery.StatePending {
		t.Errorf("a claimed delivery reads as %q, want delivering (or pending on memory)", got.State)
	}
}

func newAttempt(delID id.ID, n, status int, outcome delivery.Outcome, next *time.Time, at time.Time) *delivery.Attempt {
	return &delivery.Attempt{
		ID:            id.NewAttemptID(),
		DeliveryID:    delID,
		AttemptNum:    n,
		StatusCode:    status,
		Error:         map[bool]string{true: "connection refused"}[status == 0],
		Response:      map[bool]string{true: "body"}[status == 0],
		LatencyMs:     map[bool]int{true: 42}[status == 0],
		Outcome:       outcome,
		NextAttemptAt: next,
		AttemptedAt:   at,
	}
}

func newDelivery(tenant, eventType string, epID, evtID id.ID) *delivery.Delivery {
	return &delivery.Delivery{
		Entity:        entity.New(),
		ID:            id.NewDeliveryID(),
		EventID:       evtID,
		EndpointID:    epID,
		EventType:     eventType,
		TenantID:      tenant,
		State:         delivery.StatePending,
		MaxAttempts:   3,
		NextAttemptAt: time.Now().UTC().Add(time.Hour), // never due: the engine is not running
	}
}

// seedLog enqueues n deliveries under tenant, in index order, after letting
// shape adjust each one.
func seedLog(t *testing.T, s DeliveryBackend, tenant string, n int, shape func(i int, d *delivery.Delivery)) []*delivery.Delivery {
	t.Helper()
	out := make([]*delivery.Delivery, n)
	for i := range n {
		d := newDelivery(tenant, "invoice.paid", id.NewEndpointID(), id.NewEventID())
		shape(i, d)
		if err := s.Enqueue(context.Background(), d); err != nil {
			t.Fatalf("enqueue: %v", err)
		}
		out[i] = d
	}
	return out
}

// pageAll follows cursors to the end and returns every page. It fails the
// test on a loop, on an incomplete page (only redis may report one, and not
// at the sizes this suite uses), and on a final page that still has a cursor.
func pageAll(t *testing.T, s DeliveryBackend, q delivery.Query) []*delivery.Page {
	t.Helper()
	var pages []*delivery.Page
	for range 50 {
		p, err := s.ListDeliveries(context.Background(), q)
		if err != nil {
			t.Fatalf("list: %v", err)
		}
		if !p.Complete {
			t.Fatalf("page %d reports an incomplete search", len(pages))
		}
		pages = append(pages, p)
		if p.NextCursor == "" {
			return pages
		}
		q.Cursor = p.NextCursor
	}
	t.Fatalf("still paging after 50 pages")
	return nil
}

func flatten(pages []*delivery.Page) []*delivery.Delivery {
	var out []*delivery.Delivery
	for _, p := range pages {
		out = append(out, p.Deliveries...)
	}
	return out
}

func pageSizes(pages []*delivery.Page) []int {
	out := make([]int, 0, len(pages))
	for _, p := range pages {
		out = append(out, len(p.Deliveries))
	}
	// A backend may end on an empty page when the last full page landed on
	// the end exactly; that is not a size worth asserting on.
	if n := len(out); n > 1 && out[n-1] == 0 {
		out = out[:n-1]
	}
	return out
}

func equalInts(a, b []int) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func reverse(ds []*delivery.Delivery) []*delivery.Delivery {
	out := make([]*delivery.Delivery, len(ds))
	for i, d := range ds {
		out[len(ds)-1-i] = d
	}
	return out
}

func sortByIDDesc(ds []*delivery.Delivery) {
	sort.Slice(ds, func(i, j int) bool { return ds[i].ID.String() > ds[j].ID.String() })
}

func assertOrder(t *testing.T, got, want []*delivery.Delivery) {
	t.Helper()
	ids := func(ds []*delivery.Delivery) []string {
		out := make([]string, len(ds))
		for i, d := range ds {
			out[i] = d.ID.String()
		}
		return out
	}
	g, w := ids(got), ids(want)
	if len(g) != len(w) {
		t.Fatalf("got %d rows %v, want %d %v", len(g), g, len(w), w)
	}
	for i := range g {
		if g[i] != w[i] {
			t.Fatalf("row %d is %s, want %s\ngot  %v\nwant %v", i, g[i], w[i], g, w)
		}
	}
}

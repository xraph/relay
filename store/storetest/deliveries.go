package storetest

import (
	"context"
	"testing"
	"time"

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

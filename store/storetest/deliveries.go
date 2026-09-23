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

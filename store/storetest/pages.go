package storetest

import (
	"context"
	"errors"
	"sort"
	"testing"
	"time"

	"github.com/xraph/relay"
	"github.com/xraph/relay/dlq"
	"github.com/xraph/relay/event"
	"github.com/xraph/relay/id"
	"github.com/xraph/relay/internal/entity"
)

// EventPageBackend is what the event page suite needs.
type EventPageBackend interface {
	CreateEvent(ctx context.Context, evt *event.Event) error
	ListEventsPage(ctx context.Context, q event.Query) (*event.Page, error)
}

// RunEventPageSuite pins the event log's cursor pages.
func RunEventPageSuite(t *testing.T, open func(t *testing.T) EventPageBackend) {
	seed := func(t *testing.T, s EventPageBackend, tenant string, types []string, at func(i int) time.Time) []*event.Event {
		t.Helper()
		out := make([]*event.Event, len(types))
		for i, typ := range types {
			e := &event.Event{Entity: entity.New(), ID: id.NewEventID(), Type: typ, TenantID: tenant,
				Data: map[string]any{"i": i}}
			e.CreatedAt = at(i)
			if err := s.CreateEvent(context.Background(), e); err != nil {
				t.Fatalf("create event: %v", err)
			}
			out[i] = e
		}
		return out
	}
	all := func(t *testing.T, s EventPageBackend, q event.Query) []string {
		t.Helper()
		var ids []string
		for range 50 {
			p, err := s.ListEventsPage(context.Background(), q)
			if err != nil {
				t.Fatalf("list: %v", err)
			}
			if !p.Complete {
				t.Fatalf("incomplete page")
			}
			for _, e := range p.Events {
				ids = append(ids, e.ID.String())
			}
			if p.NextCursor == "" {
				return ids
			}
			q.Cursor = p.NextCursor
		}
		t.Fatal("still paging after 50 pages")
		return nil
	}
	base := time.Now().UTC().Truncate(time.Millisecond).Add(-time.Hour)

	t.Run("pages newest first, ties broken by id, every row once", func(t *testing.T) {
		s := open(t)
		tenant := uniqueTenant(t)
		// Three share an instant, so a page boundary falls inside the tie.
		evs := seed(t, s, tenant, []string{"a", "a", "a", "a", "a"}, func(i int) time.Time {
			return base.Add(time.Duration(min(i, 2)) * time.Second)
		})
		want := eventIDsNewestFirst(evs)
		got := all(t, s, event.Query{TenantID: tenant, Limit: 2})
		assertIDs(t, got, want)
	})

	t.Run("type and time filters narrow the log", func(t *testing.T) {
		s := open(t)
		tenant := uniqueTenant(t)
		evs := seed(t, s, tenant, []string{"invoice.paid", "customer.created", "invoice.paid"}, func(i int) time.Time {
			return base.Add(time.Duration(i) * time.Minute)
		})
		got := all(t, s, event.Query{TenantID: tenant, Type: "invoice.paid"})
		assertIDs(t, got, []string{evs[2].ID.String(), evs[0].ID.String()})
		from, to := base.Add(time.Minute), base.Add(time.Minute)
		got = all(t, s, event.Query{TenantID: tenant, From: &from, To: &to})
		assertIDs(t, got, []string{evs[1].ID.String()})
	})

	t.Run("refuses a cursor it did not issue", func(t *testing.T) {
		if _, err := open(t).ListEventsPage(context.Background(), event.Query{Cursor: "x"}); !errors.Is(err, relay.ErrInvalidCursor) {
			t.Errorf("got %v, want ErrInvalidCursor", err)
		}
	})
}

// DLQPageBackend is what the DLQ page suite needs.
type DLQPageBackend interface {
	Push(ctx context.Context, entry *dlq.Entry) error
	MarkReplayed(ctx context.Context, dlqID id.ID, at time.Time) error
	ListDLQPage(ctx context.Context, q dlq.Query) (*dlq.Page, error)
}

// RunDLQPageSuite pins the dead letter queue's cursor pages.
func RunDLQPageSuite(t *testing.T, open func(t *testing.T) DLQPageBackend) {
	seed := func(t *testing.T, s DLQPageBackend, tenant string, n int, at func(i int) time.Time) []*dlq.Entry {
		t.Helper()
		out := make([]*dlq.Entry, n)
		for i := range n {
			e := NewEntry()
			e.TenantID = tenant
			e.FailedAt = at(i)
			if err := s.Push(context.Background(), e); err != nil {
				t.Fatalf("push: %v", err)
			}
			out[i] = e
		}
		return out
	}
	all := func(t *testing.T, s DLQPageBackend, q dlq.Query) []string {
		t.Helper()
		var ids []string
		for range 50 {
			p, err := s.ListDLQPage(context.Background(), q)
			if err != nil {
				t.Fatalf("list: %v", err)
			}
			if !p.Complete {
				t.Fatalf("incomplete page")
			}
			for _, e := range p.Entries {
				ids = append(ids, e.ID.String())
			}
			if p.NextCursor == "" {
				return ids
			}
			q.Cursor = p.NextCursor
		}
		t.Fatal("still paging after 50 pages")
		return nil
	}
	base := time.Now().UTC().Truncate(time.Millisecond).Add(-time.Hour)

	t.Run("pages most recent failure first, ties broken by id, every row once", func(t *testing.T) {
		s := open(t)
		tenant := uniqueTenant(t)
		es := seed(t, s, tenant, 5, func(i int) time.Time { return base.Add(time.Duration(min(i, 2)) * time.Second) })
		got := all(t, s, dlq.Query{TenantID: tenant, Limit: 2})
		assertIDs(t, got, dlqIDsNewestFirst(es))
	})

	t.Run("endpoint, window and replayed filters narrow the queue", func(t *testing.T) {
		s := open(t)
		ctx := context.Background()
		tenant := uniqueTenant(t)
		es := seed(t, s, tenant, 3, func(i int) time.Time { return base.Add(time.Duration(i) * time.Minute) })
		if err := s.MarkReplayed(ctx, es[1].ID, time.Now().UTC()); err != nil {
			t.Fatalf("mark replayed: %v", err)
		}
		yes, no := true, false
		assertIDs(t, all(t, s, dlq.Query{TenantID: tenant, Replayed: &yes}), []string{es[1].ID.String()})
		assertIDs(t, all(t, s, dlq.Query{TenantID: tenant, Replayed: &no}), []string{es[2].ID.String(), es[0].ID.String()})
		assertIDs(t, all(t, s, dlq.Query{TenantID: tenant, EndpointID: &es[0].EndpointID}), []string{es[0].ID.String()})
		from, to := base.Add(time.Minute), base.Add(2*time.Minute)
		assertIDs(t, all(t, s, dlq.Query{TenantID: tenant, From: &from, To: &to}), []string{es[2].ID.String(), es[1].ID.String()})
	})

	t.Run("refuses a cursor it did not issue", func(t *testing.T) {
		if _, err := open(t).ListDLQPage(context.Background(), dlq.Query{Cursor: "x"}); !errors.Is(err, relay.ErrInvalidCursor) {
			t.Errorf("got %v, want ErrInvalidCursor", err)
		}
	})
}

func eventIDsNewestFirst(evs []*event.Event) []string {
	cp := append([]*event.Event(nil), evs...)
	sort.Slice(cp, func(i, j int) bool {
		if !cp[i].CreatedAt.Equal(cp[j].CreatedAt) {
			return cp[i].CreatedAt.After(cp[j].CreatedAt)
		}
		return cp[i].ID.String() > cp[j].ID.String()
	})
	out := make([]string, len(cp))
	for i, e := range cp {
		out[i] = e.ID.String()
	}
	return out
}

func dlqIDsNewestFirst(es []*dlq.Entry) []string {
	cp := append([]*dlq.Entry(nil), es...)
	sort.Slice(cp, func(i, j int) bool {
		if !cp[i].FailedAt.Equal(cp[j].FailedAt) {
			return cp[i].FailedAt.After(cp[j].FailedAt)
		}
		return cp[i].ID.String() > cp[j].ID.String()
	})
	out := make([]string, len(cp))
	for i, e := range cp {
		out[i] = e.ID.String()
	}
	return out
}

func assertIDs(t *testing.T, got, want []string) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("got %d rows %v, want %d %v", len(got), got, len(want), want)
	}
	for i, w := range want {
		if got[i] != w {
			t.Fatalf("row %d is %s, want %s\ngot  %v\nwant %v", i, got[i], w, got, want)
		}
	}
}

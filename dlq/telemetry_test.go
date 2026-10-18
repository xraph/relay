package dlq_test

import (
	"errors"
	"testing"
	"time"

	"go.opentelemetry.io/otel/codes"

	"github.com/xraph/relay/dlq"
	"github.com/xraph/relay/id"
	"github.com/xraph/relay/internal/tracing/tracingtest"
	"github.com/xraph/relay/store/memory"
)

func TestReplaySpans(t *testing.T) {
	t.Run("single", func(t *testing.T) {
		rec := tracingtest.Record(t)
		svc, store := newService()
		e := seedEntry(t, store)
		if err := svc.Replay(ctx(), e.ID); err != nil {
			t.Fatal(err)
		}
		span := tracingtest.Named(t, rec, "replay dlq")
		a := tracingtest.Attrs(span)
		if a["relay.dlq.mode"].AsString() != "single" || a["relay.dlq.replayed"].AsInt64() != 1 ||
			a["relay.dlq.entry.id"].AsString() != e.ID.String() || span.Status().Code != codes.Unset {
			t.Errorf("attributes %v status %v", span.Attributes(), span.Status())
		}
		tracingtest.AssertNoLeak(t, rec.Ended(), "receiver.example", "tenant-1")
	})

	t.Run("lost race and missing entry are not failures", func(t *testing.T) {
		rec := tracingtest.Record(t)
		svc, store := newService()
		e := seedEntry(t, store)
		if err := svc.Replay(ctx(), e.ID); err != nil {
			t.Fatal(err)
		}
		if err := svc.Replay(ctx(), e.ID); !errors.Is(err, dlq.ErrAlreadyReplayed) {
			t.Fatalf("second replay = %v", err)
		}
		if err := svc.Replay(ctx(), id.NewDLQID()); err == nil {
			t.Fatal("replaying a missing entry succeeded")
		}
		for _, span := range rec.Ended() {
			if span.Status().Code == codes.Error {
				t.Errorf("%s failed: %v", span.Name(), span.Status())
			}
		}
		if n := len(rec.Ended()); n != 3 {
			t.Fatalf("%d spans, want 3", n)
		}
		if !tracingtest.Attrs(rec.Ended()[1])["relay.dlq.already_replayed"].AsBool() {
			t.Error("the lost race is not marked already_replayed")
		}
	})

	t.Run("bulk with a failing enqueue", func(t *testing.T) {
		rec := tracingtest.Record(t)
		store := memory.New()
		svc := dlq.NewService(store, failingEnqueuer{err: errors.New("alice@example.com unavailable")}, dlq.Config{MaxAttempts: 5}, nil)
		seedEntry(t, store)
		if _, err := svc.ReplayBulk(ctx(), time.Now().Add(-2*time.Hour), time.Now()); err == nil {
			t.Fatal("bulk replay succeeded through a failing enqueuer")
		}
		span := tracingtest.Named(t, rec, "replay dlq")
		a := tracingtest.Attrs(span)
		if a["relay.dlq.mode"].AsString() != "bulk" || a["relay.dlq.replayed"].AsInt64() != 0 {
			t.Errorf("attributes = %v", span.Attributes())
		}
		if span.Status().Code != codes.Error || span.Status().Description != "*errors.errorString" {
			t.Errorf("status = %v", span.Status())
		}
		tracingtest.AssertNoLeak(t, rec.Ended(), "alice")
	})
}

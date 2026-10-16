package relay_test

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"

	"github.com/xraph/relay"
	"github.com/xraph/relay/acceptance"
	"github.com/xraph/relay/catalog"
	"github.com/xraph/relay/delivery"
	"github.com/xraph/relay/event"
	"github.com/xraph/relay/internal/metricstest"
	"github.com/xraph/relay/internal/tracing/tracingtest"
	"github.com/xraph/relay/observability"
	"github.com/xraph/relay/store"
	"github.com/xraph/relay/store/memory"
)

func TestSendSpanIsAChildOfTheCaller(t *testing.T) {
	rec := tracingtest.Record(t)
	r, _ := setup(t)
	registerType(t, r, "invoice.paid")
	createEndpoint(t, r, "t1", []string{"invoice.*"})
	createEndpoint(t, r, "t1", []string{"*"})

	parentCtx, parent := otel.Tracer("test").Start(context.Background(), "handle request")
	evt := &event.Event{Type: "invoice.paid", TenantID: "t1", Data: map[string]any{"email": "alice@example.com"}}
	if err := r.Send(parentCtx, evt); err != nil {
		t.Fatal(err)
	}
	parent.End()

	send := tracingtest.Named(t, rec, "send event")
	if send.Parent().SpanID() != parent.SpanContext().SpanID() {
		t.Error("send event is not a child of the caller's span")
	}
	if send.SpanKind() != trace.SpanKindInternal || send.Status().Code != codes.Unset {
		t.Errorf("kind %v status %v", send.SpanKind(), send.Status())
	}
	a := tracingtest.Attrs(send)
	if a["relay.event.type"].AsString() != "invoice.paid" || a["relay.event.id"].AsString() != evt.ID.String() ||
		a["relay.fanout.count"].AsInt64() != 2 {
		t.Errorf("attributes = %v", send.Attributes())
	}
	tracingtest.AssertNoLeak(t, rec.Ended(), "alice", "t1")
}

func TestSendSpanNamesTheFailureWithoutQuotingIt(t *testing.T) {
	cases := []struct {
		name  string
		setup func(*testing.T, *relay.Relay)
		want  error
		class string
		typ   string
	}{
		{"unknown type", func(*testing.T, *relay.Relay) {}, relay.ErrEventTypeNotFound,
			"event type not found", "relay.ErrEventTypeNotFound"},
		{"invalid payload", func(t *testing.T, r *relay.Relay) {
			if _, err := r.RegisterEventType(ctx(), catalog.WebhookDefinition{
				Name: "user.created",
				Schema: mustJSON(map[string]any{
					"type":       "object",
					"properties": map[string]any{"email": map[string]any{"type": "number"}},
				}),
			}); err != nil {
				t.Fatal(err)
			}
		}, relay.ErrPayloadValidationFailed, "payload validation failed", "relay.ErrPayloadValidationFailed"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			rec := tracingtest.Record(t)
			r, _ := setup(t)
			c.setup(t, r)
			err := r.Send(ctx(), &event.Event{Type: "user.created", TenantID: "t1", Data: map[string]any{"email": "alice@example.com"}})
			if !errors.Is(err, c.want) {
				t.Fatalf("Send = %v, want %v", err, c.want)
			}

			send := tracingtest.Named(t, rec, "send event")
			if send.Status().Code != codes.Error || send.Status().Description != c.class {
				t.Errorf("status = %v, want Error %q", send.Status(), c.class)
			}
			if types := tracingtest.ExceptionTypes(send); len(types) != 1 || types[0] != c.typ {
				t.Errorf("exception types = %v, want [%s]", types, c.typ)
			}
			tracingtest.AssertNoLeak(t, rec.Ended(), "alice")
		})
	}
}

func TestAcceptSpanTellsANewCommitFromARetry(t *testing.T) {
	rec := tracingtest.Record(t)
	s := memory.New()
	setupMetricData(t, s, "https://example.com/hook")
	r, err := relay.New(relay.WithStore(s))
	if err != nil {
		t.Fatal(err)
	}
	req := metricRequest()
	req.SourceKey = "alice@example.com"
	for range 2 {
		if _, err = r.SendReliable(ctx(), req); err != nil {
			t.Fatal(err)
		}
	}

	var created []bool
	for _, sp := range rec.Ended() {
		if sp.Name() != "accept event" {
			continue
		}
		a := tracingtest.Attrs(sp)
		created = append(created, a["relay.acceptance.created"].AsBool())
		if a["relay.event.type"].AsString() != "test.metrics" || a["relay.fanout.count"].AsInt64() != 1 {
			t.Errorf("attributes = %v", sp.Attributes())
		}
	}
	if len(created) != 2 || !created[0] || created[1] {
		t.Errorf("relay.acceptance.created over two calls = %v, want [true false]", created)
	}
	tracingtest.AssertNoLeak(t, rec.Ended(), "alice", "tenant", "metrics-test-signing")
}

// plainStore hides memory's AcceptEvent behind the bare store interface, the
// way a backend without reliable acceptance looks.
type plainStore struct{ store.Store }

func TestAcceptSpanFailsOnAnUnsupportedStore(t *testing.T) {
	rec := tracingtest.Record(t)
	r, err := relay.New(relay.WithStore(plainStore{memory.New()}))
	if err != nil {
		t.Fatal(err)
	}
	if _, err = r.SendReliable(ctx(), metricRequest()); !errors.Is(err, acceptance.ErrUnsupported) {
		t.Fatalf("SendReliable = %v, want ErrUnsupported", err)
	}
	accept := tracingtest.Named(t, rec, "accept event")
	if accept.Status().Code != codes.Error || accept.Status().Description != "unsupported store" {
		t.Errorf("status = %v", accept.Status())
	}
	if types := tracingtest.ExceptionTypes(accept); len(types) != 1 || types[0] != "acceptance.ErrUnsupported" {
		t.Errorf("exception types = %v", types)
	}
}

// spanProbeStore records whether the background queries relay makes on its
// own (the poll, the pending count, the wake listener) run under a span.
type spanProbeStore struct {
	*memory.Store
	mu     sync.Mutex
	valid  map[string][]bool
	polled chan struct{}
}

func (s *spanProbeStore) record(ctx context.Context, op string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.valid[op] = append(s.valid[op], trace.SpanContextFromContext(ctx).IsValid())
}

func (s *spanProbeStore) Dequeue(ctx context.Context, limit int) ([]*delivery.Delivery, error) {
	s.record(ctx, "dequeue")
	select {
	case s.polled <- struct{}{}:
	default:
	}
	return s.Store.Dequeue(ctx, limit)
}

func (s *spanProbeStore) CountPending(ctx context.Context) (int64, error) {
	s.record(ctx, "count pending")
	return s.Store.CountPending(ctx)
}

func (s *spanProbeStore) StartWakeListener(ctx context.Context, _ func()) (func(), error) {
	s.record(ctx, "wake listener")
	return func() {}, nil
}

// Forge starts relay inside its "start relay" span. Grove starts a db.client
// span under any valid span in the query's context, so if the engine kept
// that span every idle poll would add a query to the long-finished boot
// trace. Nothing relay runs on its own may see it.
func TestBackgroundQueriesDoNotInheritTheStartSpan(t *testing.T) {
	tracingtest.Record(t)
	probe := &spanProbeStore{Store: memory.New(), valid: map[string][]bool{}, polled: make(chan struct{})}
	r, err := relay.New(relay.WithStore(probe), relay.WithMetrics(observability.NewMetrics(metricstest.New())),
		relay.WithPollInterval(time.Millisecond), relay.WithMaxPollInterval(2*time.Millisecond))
	if err != nil {
		t.Fatal(err)
	}

	bootCtx, boot := otel.Tracer("test").Start(context.Background(), "start relay")
	r.Start(bootCtx)
	for range 3 {
		<-probe.polled
	}
	r.Stop(context.Background())
	boot.End()

	probe.mu.Lock()
	defer probe.mu.Unlock()
	for _, op := range []string{"dequeue", "count pending", "wake listener"} {
		calls := probe.valid[op]
		if len(calls) == 0 {
			t.Errorf("no %s call was observed", op)
		}
		for i, valid := range calls {
			if valid {
				t.Errorf("%s call %d ran under the start span", op, i)
			}
		}
	}
}

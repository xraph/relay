package relay_test

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	gu "github.com/xraph/go-utils/metrics"

	"github.com/xraph/relay"
	"github.com/xraph/relay/acceptance"
	"github.com/xraph/relay/catalog"
	"github.com/xraph/relay/delivery"
	"github.com/xraph/relay/endpoint"
	"github.com/xraph/relay/id"
	"github.com/xraph/relay/internal/entity"
	"github.com/xraph/relay/observability"
	"github.com/xraph/relay/store"
	"github.com/xraph/relay/store/memory"
)

func metricsRelay(t *testing.T, s store.Store) (*relay.Relay, *observability.Metrics) {
	t.Helper()
	m := observability.NewMetrics(gu.NewMetricsCollector("reliable-test"))
	r, err := relay.New(relay.WithStore(s), relay.WithMetrics(m), relay.WithPollInterval(5*time.Millisecond), relay.WithMaxPollInterval(10*time.Millisecond), relay.WithRetrySchedule([]time.Duration{100 * time.Millisecond}))
	if err != nil {
		t.Fatal(err)
	}
	return r, m
}

func metricRequest() acceptance.Request {
	return acceptance.Request{Producer: "metrics", InstallationID: "one", SourceKey: "key", SourceFingerprint: strings.Repeat("a", 64), AppID: "app", TenantID: "tenant", Type: "test.metrics", Data: []byte(`1`)}
}

func setupMetricData(t *testing.T, s *memory.Store, url string) {
	t.Helper()
	ctx := context.Background()
	if err := s.RegisterType(ctx, &catalog.EventType{Entity: entity.New(), ID: id.NewEventTypeID(), Definition: catalog.WebhookDefinition{Name: "test.metrics"}}); err != nil {
		t.Fatal(err)
	}
	if err := s.CreateEndpoint(ctx, &endpoint.Endpoint{Entity: entity.New(), ID: id.NewEndpointID(), ScopeAppID: "app", TenantID: "tenant", URL: url, Secret: "metric-test-signing-secret", Enabled: true, EventTypes: []string{"*"}}); err != nil {
		t.Fatal(err)
	}
}

func waitMetric(t *testing.T, predicate func() bool) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for !predicate() {
		if time.Now().After(deadline) {
			t.Fatal("metric/state did not converge")
		}
		time.Sleep(time.Millisecond)
	}
}

func TestReliableAcceptanceMetrics(t *testing.T) {
	for _, terminal := range []int{http.StatusOK, http.StatusBadRequest, http.StatusGone} {
		t.Run(http.StatusText(terminal), func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(terminal) }))
			defer srv.Close()
			s := memory.New()
			setupMetricData(t, s, srv.URL)
			r, m := metricsRelay(t, s)
			req := metricRequest()
			receipt, err := r.SendReliable(context.Background(), req)
			if err != nil {
				t.Fatal(err)
			}
			if m.EventsSentTotal.Value() != 1 || m.PendingDeliveries.Value() != 1 {
				t.Fatal("first acceptance metrics", m.EventsSentTotal.Value(), m.PendingDeliveries.Value())
			}
			if _, err = r.SendReliable(context.Background(), req); err != nil {
				t.Fatal(err)
			}
			if m.EventsSentTotal.Value() != 1 || m.PendingDeliveries.Value() != 1 {
				t.Fatal("replay inflated metrics")
			}
			r.Start(context.Background())
			defer r.Stop(context.Background())
			waitMetric(t, func() bool {
				d, e := s.GetDelivery(context.Background(), receipt.Recipients[0].DeliveryID)
				return e == nil && (d.State == delivery.StateDelivered || d.State == delivery.StateFailed) && m.PendingDeliveries.Value() == 0
			})
			if m.EventsSentTotal.Value() != 1 {
				t.Fatal("delivery changed acceptance counter")
			}
			// A terminal receipt is retained but contributes no queued work.
			if _, err = r.SendReliable(context.Background(), req); err != nil {
				t.Fatal(err)
			}
			if m.EventsSentTotal.Value() != 1 || m.PendingDeliveries.Value() != 0 {
				t.Fatal("terminal receipt altered metrics")
			}
		})
	}
}

func TestReliableMetricsLostAckAndRestart(t *testing.T) {
	gate := make(chan struct{})
	defer func() {
		select {
		case <-gate:
		default:
			close(gate)
		}
	}()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { <-gate; w.WriteHeader(http.StatusOK) }))
	defer srv.Close()
	s := memory.New()
	setupMetricData(t, s, srv.URL)
	wrapper := &lostAckStore{Store: s, acceptor: s}
	r, m := metricsRelay(t, wrapper)
	req := metricRequest()
	if receipt, err := r.SendReliable(context.Background(), req); err == nil || receipt != nil {
		t.Fatal("expected unknown acknowledgement")
	}
	if m.EventsSentTotal.Value() != 0 || m.PendingDeliveries.Value() != 1 {
		t.Fatal("unknown outcome metrics must use store backlog")
	}
	if _, err := r.SendReliable(context.Background(), req); err != nil {
		t.Fatal(err)
	}
	if m.EventsSentTotal.Value() != 0 || m.PendingDeliveries.Value() != 1 {
		t.Fatal("recovery guessed a new commit")
	}
	restarted, restartedMetrics := metricsRelay(t, s)
	t.Cleanup(func() {
		select {
		case <-gate:
		default:
			close(gate)
		}
		restarted.Stop(context.Background())
	})
	restarted.Start(context.Background())
	if restartedMetrics.EventsSentTotal.Value() != 0 || restartedMetrics.PendingDeliveries.Value() != 1 {
		t.Fatal("restart did not load existing backlog")
	}
	close(gate)
	waitMetric(t, func() bool { return restartedMetrics.PendingDeliveries.Value() == 0 })
	restarted.Stop(context.Background())
	if _, err := restarted.SendReliable(context.Background(), req); err != nil {
		t.Fatal(err)
	}
	if restartedMetrics.EventsSentTotal.Value() != 0 || restartedMetrics.PendingDeliveries.Value() != 0 {
		t.Fatal("retained receipt invented metrics after restart")
	}
}

func TestReliableMetricsRetry(t *testing.T) {
	var requests atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if requests.Add(1) == 1 {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()
	s := memory.New()
	setupMetricData(t, s, srv.URL)
	r, m := metricsRelay(t, s)
	receipt, err := r.SendReliable(context.Background(), metricRequest())
	if err != nil {
		t.Fatal(err)
	}
	r.Start(context.Background())
	defer r.Stop(context.Background())
	waitMetric(t, func() bool {
		d, e := s.GetDelivery(context.Background(), receipt.Recipients[0].DeliveryID)
		return e == nil && d.AttemptCount == 1 && d.State == delivery.StatePending
	})
	if m.PendingDeliveries.Value() != 1 {
		t.Fatal("retry lost pending backlog")
	}
	waitMetric(t, func() bool { return requests.Load() == 2 && m.PendingDeliveries.Value() == 0 })
	if m.EventsSentTotal.Value() != 1 {
		t.Fatal("retry counted as acceptance")
	}
}

// Embedding the concrete store must not bypass the overridden acceptance path.
type lostOutcomeStore struct {
	*memory.Store
	lose  bool
	calls int
}

func (s *lostOutcomeStore) AcceptEvent(ctx context.Context, req acceptance.Request, maxAttempts int) (*acceptance.Receipt, error) {
	s.calls++
	receipt, err := s.Store.AcceptEvent(ctx, req, maxAttempts)
	if err == nil && s.lose {
		s.lose = false
		return nil, errors.New("commit acknowledgement lost")
	}
	return receipt, err
}

func TestReliableMetricsLostOutcomeAcknowledgement(t *testing.T) {
	s := memory.New()
	setupMetricData(t, s, "https://example.com")
	wrapper := &lostOutcomeStore{Store: s, lose: true}
	r, m := metricsRelay(t, wrapper)
	if _, err := r.SendReliable(context.Background(), metricRequest()); err == nil {
		t.Fatal("expected unknown commit")
	}
	if m.EventsSentTotal.Value() != 0 || m.PendingDeliveries.Value() != 1 {
		t.Fatal("unknown outcome fabricated counter or lost backlog")
	}
	if _, err := r.SendReliable(context.Background(), metricRequest()); err != nil {
		t.Fatal(err)
	}
	if m.EventsSentTotal.Value() != 0 || m.PendingDeliveries.Value() != 1 {
		t.Fatal("recovery incremented observed new commits")
	}
	if wrapper.calls != 2 {
		t.Fatal("acceptance wrapper was bypassed")
	}
}

func TestReliableMetricsMissingEndpoint(t *testing.T) {
	s := memory.New()
	setupMetricData(t, s, "https://example.com")
	r, m := metricsRelay(t, s)
	receipt, err := r.SendReliable(context.Background(), metricRequest())
	if err != nil {
		t.Fatal(err)
	}
	if err = s.DeleteEndpoint(context.Background(), receipt.Recipients[0].EndpointID); err != nil {
		t.Fatal(err)
	}
	r.Start(context.Background())
	defer r.Stop(context.Background())
	waitMetric(t, func() bool {
		d, e := s.GetDelivery(context.Background(), receipt.Recipients[0].DeliveryID)
		return e == nil && d.State == delivery.StateFailed && m.PendingDeliveries.Value() == 0
	})
}

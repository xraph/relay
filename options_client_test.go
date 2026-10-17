package relay_test

import (
	"context"
	"io"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/xraph/relay"
	"github.com/xraph/relay/event"
	"github.com/xraph/relay/store/memory"
)

func TestWithHTTPClientReachesTheConfig(t *testing.T) {
	client := &http.Client{}

	r, err := relay.New(relay.WithStore(memory.New()), relay.WithHTTPClient(client))
	if err != nil {
		t.Fatal(err)
	}

	if r.Config().HTTPClient != client {
		t.Fatal("WithHTTPClient did not reach the config")
	}

	if _, err := relay.New(relay.WithStore(memory.New()), relay.WithHTTPClient(nil)); err == nil {
		t.Fatal("a nil client was accepted")
	}
}

// answering is a RoundTripper that answers every request itself and
// remembers where each one went.
type answering struct {
	mu   sync.Mutex
	urls []string
}

func (a *answering) RoundTrip(req *http.Request) (*http.Response, error) {
	a.mu.Lock()
	a.urls = append(a.urls, req.URL.String())
	a.mu.Unlock()
	return &http.Response{
		StatusCode: http.StatusOK,
		Body:       io.NopCloser(strings.NewReader("")),
		Header:     http.Header{},
		Request:    req,
	}, nil
}

func (a *answering) sent() []string {
	a.mu.Lock()
	defer a.mu.Unlock()
	return append([]string(nil), a.urls...)
}

// The client reaches the engine that sends deliveries, not just the config:
// a caller relying on it to refuse some addresses depends on every hop.
func TestWithHTTPClientSendsTheDeliveries(t *testing.T) {
	transport := &answering{}
	r, err := relay.New(
		relay.WithStore(memory.New()),
		relay.WithHTTPClient(&http.Client{Transport: transport}),
		relay.WithPollInterval(10*time.Millisecond),
	)
	if err != nil {
		t.Fatal(err)
	}
	registerType(t, r, "invoice.created")
	// Only the supplied client's transport sees the request. Any other
	// client would go out to the network instead.
	createEndpoint(t, r, "t1", []string{"invoice.*"})

	run, cancel := context.WithCancel(context.Background())
	defer cancel()
	r.Start(run)
	defer r.Stop(context.Background())

	if err := r.Send(ctx(), &event.Event{Type: "invoice.created", TenantID: "t1", Data: map[string]any{"amount": 100}}); err != nil {
		t.Fatal(err)
	}
	for deadline := time.Now().Add(5 * time.Second); len(transport.sent()) == 0; time.Sleep(10 * time.Millisecond) {
		if time.Now().After(deadline) {
			t.Fatal("the delivery didn't go through the supplied client")
		}
	}
	if got := transport.sent(); got[0] != "https://example.com/webhook" {
		t.Fatalf("delivered to %v", got)
	}
}

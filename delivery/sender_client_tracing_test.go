package delivery

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/cookiejar"
	"strings"
	"testing"
	"time"

	"github.com/xraph/relay/endpoint"
	"github.com/xraph/relay/event"
	"github.com/xraph/relay/internal/tracing/tracingtest"
)

// forwardingTransport stands in for a caller's transport, such as one whose
// dialer refuses private addresses. It records what it was asked to send.
type forwardingTransport struct{ requests []*http.Request }

func (f *forwardingTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	f.requests = append(f.requests, req)
	return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader("ok")), Header: http.Header{}}, nil
}

var errNoRedirects = errors.New("no redirects")

// A client you supply keeps its own transport, timeout, redirect policy and
// jar, and its deliveries are traced like any other: a client span with the
// URL redacted, and the trace context on the request.
func TestSuppliedClientIsTracedAndKeepsItsSettings(t *testing.T) {
	rec := tracingtest.Record(t)
	transport := &forwardingTransport{}
	jar, err := cookiejar.New(nil)
	if err != nil {
		t.Fatal(err)
	}
	supplied := &http.Client{
		Transport:     transport,
		Timeout:       3 * time.Second,
		Jar:           jar,
		CheckRedirect: func(*http.Request, []*http.Request) error { return errNoRedirects },
	}
	sender := NewSenderWithClient(supplied, 30*time.Second)

	if sender.client.Timeout != 3*time.Second || sender.client.Jar != jar ||
		!errors.Is(sender.client.CheckRedirect(nil, nil), errNoRedirects) {
		t.Error("the supplied client's timeout, jar or redirect policy was lost")
	}
	if supplied.Transport != transport {
		t.Error("the caller's client was changed")
	}

	url := "https://hooks.example.com/services/T000/XXXXSECRET?token=sk_live_SECRET"
	ep := &endpoint.Endpoint{URL: url, Secret: "whsec_test"}
	result := sender.Send(context.Background(), ep, &event.Event{Type: "invoice.paid"}, &Delivery{})
	if result.Error != "" || result.StatusCode != http.StatusOK {
		t.Fatalf("send: %d %s", result.StatusCode, result.Error)
	}

	if len(transport.requests) != 1 {
		t.Fatalf("the supplied transport sent %d requests, want 1", len(transport.requests))
	}
	sent := transport.requests[0]
	if sent.URL.String() != url {
		t.Errorf("the supplied transport got %q, want the real URL", sent.URL)
	}
	client := tracingtest.Named(t, rec, "HTTP POST")
	if !strings.Contains(sent.Header.Get("traceparent"), client.SpanContext().TraceID().String()) {
		t.Errorf("traceparent %q does not carry the client span's trace", sent.Header.Get("traceparent"))
	}
	a := tracingtest.Attrs(client)
	if a["url.full"].AsString() != "https://REDACTED" || a["server.address"].AsString() != "REDACTED" {
		t.Errorf("url.full=%q server.address=%q, want both redacted", a["url.full"].AsString(), a["server.address"].AsString())
	}
	tracingtest.AssertNoLeak(t, rec.Ended(), "hooks.example.com", "SECRET", "services")
}

package delivery

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/xraph/relay/endpoint"
	"github.com/xraph/relay/event"
)

type recordingTransport struct{ requests []*http.Request }

func (r *recordingTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	r.requests = append(r.requests, req)

	return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader("ok")), Header: http.Header{}}, nil
}

func TestSenderUsesTheClientItIsGiven(t *testing.T) {
	transport := &recordingTransport{}
	sender := NewSenderWithClient(&http.Client{Transport: transport}, 5*time.Second)

	if sender.client.Timeout != 5*time.Second {
		t.Fatalf("timeout %s, want the configured 5s when the client sets none", sender.client.Timeout)
	}

	ep := &endpoint.Endpoint{URL: "https://hooks.example.com/in", Secret: "whsec_test"}
	evt := &event.Event{Type: "deployment.updated.v1", TenantID: "org-1"}

	result := sender.Send(context.Background(), ep, evt, &Delivery{})
	if result.Error != "" {
		t.Fatalf("send: %s", result.Error)
	}

	if len(transport.requests) != 1 || transport.requests[0].URL.String() != ep.URL {
		t.Fatalf("the given transport was not used: %d requests", len(transport.requests))
	}
}

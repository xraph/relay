package delivery

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"time"

	"github.com/xraph/relay/endpoint"
	"github.com/xraph/relay/event"
	"github.com/xraph/relay/signature"
)

const maxResponseBody = 1024 // 1KB cap on response body storage

// Failures before a request goes out. Like the errClass values, they describe
// a failed attempt without quoting its error.
const (
	failInvalidPayload = "invalid_payload"
	failInvalidRequest = "invalid_request"
	failNoSigningKey   = "no_signing_key"
)

// Sender performs HTTP webhook delivery.
type Sender struct {
	client *http.Client
}

// NewSender creates a sender with the given HTTP timeout.
// The headers a receiver verifies a delivery with. The dashboard's settings
// page reads them from here, so it cannot describe a header Relay stopped
// sending.
const (
	// HeaderSignature carries "v1=" and the hex HMAC-SHA256 of
	// "{timestamp}.{body}" under the endpoint's secret.
	HeaderSignature = "X-Relay-Signature"
	// HeaderTimestamp carries the unix seconds the signature was made at.
	HeaderTimestamp = "X-Relay-Timestamp"
)

func NewSender(timeout time.Duration) *Sender {
	return &Sender{
		client: &http.Client{Timeout: timeout},
	}
}

// NewSenderWithClient sends deliveries through client, for example one whose
// dialer refuses private addresses. A client with no Timeout gets timeout.
// The client is copied, so the caller's value is never changed.
func NewSenderWithClient(client *http.Client, timeout time.Duration) *Sender {
	c := *client
	if c.Timeout == 0 {
		c.Timeout = timeout
	}

	return &Sender{client: &c}
}

// Send delivers an event to an endpoint and returns the result.
func (s *Sender) Send(ctx context.Context, ep *endpoint.Endpoint, evt *event.Event, d *Delivery) Result {
	body, err := json.Marshal(evt.Data)
	if err != nil {
		return Result{Error: fmt.Sprintf("marshal payload: %v", err), errClass: failInvalidPayload}
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, ep.URL, bytes.NewReader(body))
	if err != nil {
		return Result{Error: fmt.Sprintf("create request: %v", err), errClass: failInvalidRequest}
	}

	// Standard headers.
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", "Relay/1.0")
	req.Header.Set("X-Relay-Event-ID", evt.ID.String())
	req.Header.Set("X-Relay-Event-Type", evt.Type)
	req.Header.Set("X-Relay-Delivery-ID", d.ID.String())

	// HMAC signature. An endpoint with no secret is not delivered to at all:
	// an unsigned delivery cannot be authenticated by the receiver, and one
	// signed with an absent key is worse, because HMAC produces a well-formed
	// digest from an empty key that verifies against the same empty key. That
	// looks authentic to a receiver doing everything right.
	ts := time.Now().Unix()
	sig, sigErr := signature.Sign(body, ep.Secret, ts)
	if sigErr != nil {
		return Result{Error: fmt.Sprintf("endpoint has no signing secret: %v", sigErr), errClass: failNoSigningKey}
	}
	req.Header.Set(HeaderSignature, sig)
	req.Header.Set(HeaderTimestamp, strconv.FormatInt(ts, 10))

	// Custom endpoint headers.
	for k, v := range ep.Headers {
		req.Header.Set(k, v)
	}

	start := time.Now()
	resp, err := s.client.Do(req)
	elapsed := time.Since(start)
	latency := int(elapsed.Milliseconds())

	if err != nil {
		return Result{
			Error:     err.Error(),
			LatencyMs: latency,
			sent:      true,
			duration:  elapsed,
			errClass:  classify(err),
		}
	}
	defer resp.Body.Close()

	respBody, readErr := io.ReadAll(io.LimitReader(resp.Body, maxResponseBody))
	if readErr != nil {
		return Result{
			StatusCode: resp.StatusCode,
			Error:      fmt.Sprintf("read response: %v", readErr),
			LatencyMs:  latency,
			sent:       true,
			duration:   elapsed,
		}
	}

	return Result{
		StatusCode: resp.StatusCode,
		Response:   string(respBody),
		LatencyMs:  latency,
		sent:       true,
		duration:   elapsed,
	}
}

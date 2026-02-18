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

// Sender performs HTTP webhook delivery.
type Sender struct {
	client *http.Client
}

// NewSender creates a sender with the given HTTP timeout.
func NewSender(timeout time.Duration) *Sender {
	return &Sender{
		client: &http.Client{Timeout: timeout},
	}
}

// Send delivers an event to an endpoint and returns the result.
func (s *Sender) Send(ctx context.Context, ep *endpoint.Endpoint, evt *event.Event, d *Delivery) Result {
	body, err := json.Marshal(evt.Data)
	if err != nil {
		return Result{Error: fmt.Sprintf("marshal payload: %v", err)}
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, ep.URL, bytes.NewReader(body))
	if err != nil {
		return Result{Error: fmt.Sprintf("create request: %v", err)}
	}

	// Standard headers.
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", "Relay/1.0")
	req.Header.Set("X-Relay-Event-ID", evt.ID.String())
	req.Header.Set("X-Relay-Event-Type", evt.Type)
	req.Header.Set("X-Relay-Delivery-ID", d.ID.String())

	// HMAC signature.
	ts := time.Now().Unix()
	sig := signature.Sign(body, ep.Secret, ts)
	req.Header.Set("X-Relay-Signature", sig)
	req.Header.Set("X-Relay-Timestamp", strconv.FormatInt(ts, 10))

	// Custom endpoint headers.
	for k, v := range ep.Headers {
		req.Header.Set(k, v)
	}

	start := time.Now()
	resp, err := s.client.Do(req) //nolint:gosec // G704: URL is a user-configured webhook destination; SSRF is by design.
	latency := time.Since(start).Milliseconds()

	if err != nil {
		return Result{
			Error:     err.Error(),
			LatencyMs: int(latency),
		}
	}
	defer resp.Body.Close()

	respBody, readErr := io.ReadAll(io.LimitReader(resp.Body, maxResponseBody))
	if readErr != nil {
		return Result{
			StatusCode: resp.StatusCode,
			Error:      fmt.Sprintf("read response: %v", readErr),
			LatencyMs:  int(latency),
		}
	}

	return Result{
		StatusCode: resp.StatusCode,
		Response:   string(respBody),
		LatencyMs:  int(latency),
	}
}

package contract

import (
	"context"
	"encoding/json"
	"time"

	"github.com/xraph/forge/extensions/dashboard/contract"

	"github.com/xraph/relay/catalog"
	"github.com/xraph/relay/dlq"
	"github.com/xraph/relay/endpoint"
	"github.com/xraph/relay/id"
)

// ─── Dead letter queue ─────────────────────────────────────────────────────

// DLQEntrySummary is one row of the dead letter queue.
type DLQEntrySummary struct {
	ID             string  `json:"id"`
	DeliveryID     string  `json:"deliveryId"`
	EventID        string  `json:"eventId"`
	EndpointID     string  `json:"endpointId"`
	EventType      string  `json:"eventType"`
	TenantID       string  `json:"tenantId"`
	URL            string  `json:"url"`
	Error          string  `json:"error,omitempty"`
	AttemptCount   int     `json:"attemptCount"`
	LastStatusCode int     `json:"lastStatusCode"`
	ReplayedAt     *string `json:"replayedAt,omitempty"`
	FailedAt       string  `json:"failedAt"`
}

// DLQEntryDetail adds the payload that a replay would send again.
type DLQEntryDetail struct {
	DLQEntrySummary
	Payload json.RawMessage `json:"payload"`
}

// ListDLQInput is the DLQ's filter bar.
type ListDLQInput struct {
	Cursor     string     `json:"cursor,omitempty"`
	Limit      int        `json:"limit,omitempty"`
	TenantID   string     `json:"tenantId,omitempty"`
	EndpointID string     `json:"endpointId,omitempty"`
	From       *time.Time `json:"from,omitempty"`
	To         *time.Time `json:"to,omitempty"`
	Replayed   *bool      `json:"replayed,omitempty"`
}

// ListDLQResponse is one page of the DLQ.
type ListDLQResponse struct {
	Entries    []DLQEntrySummary `json:"entries"`
	NextCursor string            `json:"nextCursor,omitempty"`
	Complete   bool              `json:"complete"`
}

// WindowInput is a failed-at window, both ends required. Bulk replay used to
// run over a hardcoded 365 days with no confirmation; the window is explicit
// now, and the page shows what it matches before anything is sent.
type WindowInput struct {
	From *time.Time `json:"from"`
	To   *time.Time `json:"to"`
}

// BulkPreviewResponse is what a bulk replay over the window would send.
type BulkPreviewResponse struct {
	Replayable      int64 `json:"replayable"`
	AlreadyReplayed int64 `json:"alreadyReplayed"`
}

// ReplayBulkResponse is how many entries were sent again.
type ReplayBulkResponse struct {
	Replayed int64 `json:"replayed"`
}

// PurgeInput removes entries that failed before a cutoff.
type PurgeInput struct {
	Before *time.Time `json:"before"`
}

// PurgeResponse is how many entries went.
type PurgeResponse struct {
	Purged int64 `json:"purged"`
}

func projectDLQ(e *dlq.Entry) DLQEntrySummary {
	return DLQEntrySummary{
		ID:             e.ID.String(),
		DeliveryID:     e.DeliveryID.String(),
		EventID:        e.EventID.String(),
		EndpointID:     e.EndpointID.String(),
		EventType:      e.EventType,
		TenantID:       e.TenantID,
		URL:            e.URL,
		Error:          e.Error,
		AttemptCount:   e.AttemptCount,
		LastStatusCode: e.LastStatusCode,
		ReplayedAt:     rfc3339Ptr(e.ReplayedAt),
		FailedAt:       rfc3339(e.FailedAt),
	}
}

// payloadJSON returns the entry's payload as JSON. Backends hand it back as
// raw bytes of JSON or as a decoded value, depending on how they store it.
func payloadJSON(p any) json.RawMessage {
	switch v := p.(type) {
	case nil:
		return json.RawMessage("null")
	case []byte:
		if json.Valid(v) {
			return v
		}
	case json.RawMessage:
		if json.Valid(v) {
			return v
		}
	case string:
		if json.Valid([]byte(v)) {
			return json.RawMessage(v)
		}
	}
	b, err := json.Marshal(p)
	if err != nil {
		return json.RawMessage("null")
	}
	return b
}

func dlqListHandler(deps Deps) func(context.Context, ListDLQInput, contract.Principal) (ListDLQResponse, error) {
	return func(ctx context.Context, in ListDLQInput, _ contract.Principal) (ListDLQResponse, error) {
		epID, err := optionalID(in.EndpointID, id.ParseEndpointID, "endpoint")
		if err != nil {
			return ListDLQResponse{}, err
		}
		page, err := deps.Relay.Store().ListDLQPage(ctx, dlq.Query{
			Cursor: in.Cursor, Limit: in.Limit, TenantID: in.TenantID, EndpointID: epID,
			From: in.From, To: in.To, Replayed: in.Replayed,
		})
		if err != nil {
			return ListDLQResponse{}, mapRelayError(err)
		}
		out := ListDLQResponse{Entries: make([]DLQEntrySummary, 0, len(page.Entries)),
			NextCursor: page.NextCursor, Complete: page.Complete}
		for _, e := range page.Entries {
			out.Entries = append(out.Entries, projectDLQ(e))
		}
		return out, nil
	}
}

func dlqDetailHandler(deps Deps) func(context.Context, GetByIDInput, contract.Principal) (DLQEntryDetail, error) {
	return func(ctx context.Context, in GetByIDInput, _ contract.Principal) (DLQEntryDetail, error) {
		dlqID, err := parseID(in.ID, id.ParseDLQID, "dead letter")
		if err != nil {
			return DLQEntryDetail{}, err
		}
		e, err := deps.Relay.DLQ().Get(ctx, dlqID)
		if err != nil {
			return DLQEntryDetail{}, mapRelayError(err)
		}
		return DLQEntryDetail{DLQEntrySummary: projectDLQ(e), Payload: payloadJSON(e.Payload)}, nil
	}
}

func dlqReplayHandler(deps Deps) func(context.Context, GetByIDInput, contract.Principal) (AckResponse, error) {
	return func(ctx context.Context, in GetByIDInput, _ contract.Principal) (AckResponse, error) {
		dlqID, err := parseID(in.ID, id.ParseDLQID, "dead letter")
		if err != nil {
			return AckResponse{}, err
		}
		if err := deps.Relay.DLQ().Replay(ctx, dlqID); err != nil {
			return AckResponse{}, mapRelayError(err)
		}
		return AckResponse{OK: true, ID: dlqID.String()}, nil
	}
}

func requireWindow(in WindowInput) (from, to time.Time, err error) {
	if in.From == nil || in.To == nil {
		return time.Time{}, time.Time{}, &contract.Error{Code: contract.CodeBadRequest,
			Message: "a bulk replay needs both ends of the window"}
	}
	if in.To.Before(*in.From) {
		return time.Time{}, time.Time{}, &contract.Error{Code: contract.CodeBadRequest,
			Message: "the window ends before it starts"}
	}
	return *in.From, *in.To, nil
}

func dlqBulkPreviewHandler(deps Deps) func(context.Context, WindowInput, contract.Principal) (BulkPreviewResponse, error) {
	return func(ctx context.Context, in WindowInput, _ contract.Principal) (BulkPreviewResponse, error) {
		from, to, err := requireWindow(in)
		if err != nil {
			return BulkPreviewResponse{}, err
		}
		// The same read ReplayBulk makes, so the count is what it would send.
		entries, err := deps.Relay.DLQ().List(ctx, dlq.ListOpts{From: &from, To: &to})
		if err != nil {
			return BulkPreviewResponse{}, mapRelayError(err)
		}
		var out BulkPreviewResponse
		for _, e := range entries {
			if e.ReplayedAt != nil {
				out.AlreadyReplayed++
			} else {
				out.Replayable++
			}
		}
		return out, nil
	}
}

func dlqReplayBulkHandler(deps Deps) func(context.Context, WindowInput, contract.Principal) (ReplayBulkResponse, error) {
	return func(ctx context.Context, in WindowInput, _ contract.Principal) (ReplayBulkResponse, error) {
		from, to, err := requireWindow(in)
		if err != nil {
			return ReplayBulkResponse{}, err
		}
		n, err := deps.Relay.DLQ().ReplayBulk(ctx, from, to)
		if err != nil {
			// Some entries may have gone before the error: they were
			// enqueued and will be sent. Say how many.
			return ReplayBulkResponse{}, &contract.Error{Code: contract.CodeInternal,
				Message: err.Error(), Details: map[string]any{"replayed": n}}
		}
		return ReplayBulkResponse{Replayed: n}, nil
	}
}

func dlqPurgeHandler(deps Deps) func(context.Context, PurgeInput, contract.Principal) (PurgeResponse, error) {
	return func(ctx context.Context, in PurgeInput, _ contract.Principal) (PurgeResponse, error) {
		if in.Before == nil {
			return PurgeResponse{}, &contract.Error{Code: contract.CodeBadRequest, Message: "purge needs a cutoff"}
		}
		n, err := deps.Relay.DLQ().Purge(ctx, *in.Before)
		if err != nil {
			return PurgeResponse{}, mapRelayError(err)
		}
		return PurgeResponse{Purged: n}, nil
	}
}

// ─── Overview and settings ─────────────────────────────────────────────────

// OverviewStats are the four counters the overview leads with.
type OverviewStats struct {
	EventTypes  int   `json:"eventTypes"`
	Endpoints   int   `json:"endpoints"`
	Pending     int64 `json:"pending"`
	DeadLetters int64 `json:"deadLetters"`
}

// EmptyInput takes nothing.
type EmptyInput struct{}

func overviewStatsHandler(deps Deps) func(context.Context, EmptyInput, contract.Principal) (OverviewStats, error) {
	return func(ctx context.Context, _ EmptyInput, _ contract.Principal) (OverviewStats, error) {
		st := deps.Relay.Store()
		types, err := st.ListTypes(ctx, catalog.ListOpts{}) // active types, as the templ overview counted
		if err != nil {
			return OverviewStats{}, mapRelayError(err)
		}
		eps, err := st.ListEndpoints(ctx, "", endpoint.ListOpts{})
		if err != nil {
			return OverviewStats{}, mapRelayError(err)
		}
		pending, err := st.CountPending(ctx)
		if err != nil {
			return OverviewStats{}, mapRelayError(err)
		}
		dead, err := st.CountDLQ(ctx)
		if err != nil {
			return OverviewStats{}, mapRelayError(err)
		}
		return OverviewStats{EventTypes: len(types), Endpoints: len(eps), Pending: pending, DeadLetters: dead}, nil
	}
}

// SettingsConfig is relay.Config, read-only. It is fixed when Relay is built
// and has no setter, so a write intent here would be a lie.
type SettingsConfig struct {
	Concurrency       int     `json:"concurrency"`
	BatchSize         int     `json:"batchSize"`
	MaxRetries        int     `json:"maxRetries"`
	PollIntervalMs    int64   `json:"pollIntervalMs"`
	MaxPollIntervalMs int64   `json:"maxPollIntervalMs"`
	RequestTimeoutMs  int64   `json:"requestTimeoutMs"`
	ShutdownTimeoutMs int64   `json:"shutdownTimeoutMs"`
	CacheTTLMs        int64   `json:"cacheTtlMs"`
	RetryScheduleMs   []int64 `json:"retryScheduleMs"`
}

func settingsConfigHandler(deps Deps) func(context.Context, EmptyInput, contract.Principal) (SettingsConfig, error) {
	return func(context.Context, EmptyInput, contract.Principal) (SettingsConfig, error) {
		c := deps.Relay.Config()
		out := SettingsConfig{
			Concurrency: c.Concurrency, BatchSize: c.BatchSize, MaxRetries: c.MaxRetries,
			PollIntervalMs: c.PollInterval.Milliseconds(), MaxPollIntervalMs: c.MaxPollInterval.Milliseconds(),
			RequestTimeoutMs: c.RequestTimeout.Milliseconds(), ShutdownTimeoutMs: c.ShutdownTimeout.Milliseconds(),
			CacheTTLMs: c.CacheTTL.Milliseconds(), RetryScheduleMs: make([]int64, 0, len(c.RetrySchedule)),
		}
		for _, d := range c.RetrySchedule {
			out.RetryScheduleMs = append(out.RetryScheduleMs, d.Milliseconds())
		}
		return out, nil
	}
}

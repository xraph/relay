package dlq

import (
	"time"

	"github.com/xraph/relay/id"
	"github.com/xraph/relay/internal/cursor"
)

// Query selects a page of the dead letter queue, most recent failure first.
// The DLQ orders and windows on failed_at, as ListDLQ always has.
type Query struct {
	Cursor     string
	Limit      int // 0 means 50, capped at 200
	TenantID   string
	EndpointID *id.ID
	From, To   *time.Time // failed_at, inclusive
	// Replayed selects entries that have (true) or have not (false) been
	// replayed. Nil selects both.
	Replayed *bool
}

// Page is one page of the DLQ. Complete means what it means on
// delivery.Page.
type Page struct {
	Entries    []*Entry `json:"entries"`
	NextCursor string   `json:"next_cursor,omitempty"`
	Complete   bool     `json:"complete"`
}

// Position is a decoded cursor.
type Position struct {
	FailedAt time.Time
	ID       string
}

// Prepare returns the clamped limit and the decoded cursor, nil on page one.
func (q Query) Prepare() (int, *Position, error) {
	limit := cursor.Limit(q.Limit)
	if q.Cursor == "" {
		return limit, nil, nil
	}
	at, rowID, err := cursor.Decode(q.Cursor)
	if err != nil {
		return 0, nil, err
	}
	return limit, &Position{FailedAt: at, ID: rowID}, nil
}

// Matches reports whether e passes every filter in q.
func (q Query) Matches(e *Entry) bool {
	if q.TenantID != "" && e.TenantID != q.TenantID {
		return false
	}
	if q.EndpointID != nil && e.EndpointID != *q.EndpointID {
		return false
	}
	if q.From != nil && e.FailedAt.Before(*q.From) {
		return false
	}
	if q.To != nil && e.FailedAt.After(*q.To) {
		return false
	}
	if q.Replayed != nil && (e.ReplayedAt != nil) != *q.Replayed {
		return false
	}
	return true
}

// After reports whether e sorts strictly after p: failed_at descending, then
// id descending.
func (p *Position) After(e *Entry) bool {
	if p == nil {
		return true
	}
	if !e.FailedAt.Equal(p.FailedAt) {
		return e.FailedAt.Before(p.FailedAt)
	}
	return e.ID.String() < p.ID
}

// CursorFor returns the cursor that continues after e.
func CursorFor(e *Entry) string { return cursor.Encode(e.FailedAt, e.ID.String()) }

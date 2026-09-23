package event

import (
	"time"

	"github.com/xraph/relay/internal/cursor"
)

// Query selects a page of the event log, newest first.
type Query struct {
	Cursor   string
	Limit    int // 0 means 50, capped at 200
	Type     string
	TenantID string
	From, To *time.Time // created_at, inclusive
}

// Page is one page of the event log. Complete means what it means on
// delivery.Page.
type Page struct {
	Events     []*Event `json:"events"`
	NextCursor string   `json:"next_cursor,omitempty"`
	Complete   bool     `json:"complete"`
}

// Position is a decoded cursor.
type Position struct {
	CreatedAt time.Time
	ID        string
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
	return limit, &Position{CreatedAt: at, ID: rowID}, nil
}

// Matches reports whether e passes every filter in q.
func (q Query) Matches(e *Event) bool {
	if q.Type != "" && e.Type != q.Type {
		return false
	}
	if q.TenantID != "" && e.TenantID != q.TenantID {
		return false
	}
	if q.From != nil && e.CreatedAt.Before(*q.From) {
		return false
	}
	if q.To != nil && e.CreatedAt.After(*q.To) {
		return false
	}
	return true
}

// After reports whether e sorts strictly after p: created_at descending,
// then id descending.
func (p *Position) After(e *Event) bool {
	if p == nil {
		return true
	}
	if !e.CreatedAt.Equal(p.CreatedAt) {
		return e.CreatedAt.Before(p.CreatedAt)
	}
	return e.ID.String() < p.ID
}

// CursorFor returns the cursor that continues after e.
func CursorFor(e *Event) string { return cursor.Encode(e.CreatedAt, e.ID.String()) }

package delivery

import (
	"fmt"
	"time"

	"github.com/xraph/relay/id"
	"github.com/xraph/relay/internal/cursor"
	"github.com/xraph/relay/internal/errs"
)

// StatusClass filters on a delivery's last status code by hundreds.
type StatusClass string

const (
	// StatusAny applies no status filter.
	StatusAny StatusClass = ""
	// Status2xx matches 200 to 299.
	Status2xx StatusClass = "2xx"
	// Status4xx matches 400 to 499.
	Status4xx StatusClass = "4xx"
	// Status5xx matches 500 to 599.
	Status5xx StatusClass = "5xx"
	// StatusNone matches a status of 0: a delivery never attempted, or one
	// whose last attempt got no response at all (a connection error or a
	// timeout). Both read the same on the row, and on an endpoint that has
	// gone away it is the most common value, so it gets its own name.
	StatusNone StatusClass = "none"
)

// Range returns the inclusive status range the class covers.
func (c StatusClass) Range() (lo, hi int, ok bool) {
	switch c {
	case Status2xx:
		return 200, 299, true
	case Status4xx:
		return 400, 499, true
	case Status5xx:
		return 500, 599, true
	case StatusNone:
		return 0, 0, true
	default:
		return 0, 0, false
	}
}

// Query selects a page of the delivery log, newest first. Every field is
// optional; an empty Query returns the whole log.
type Query struct {
	// Cursor continues from the last row of a previous page.
	Cursor string
	// Limit is the page size: 0 means 50, and it is capped at 200.
	Limit int

	State       *State
	EndpointID  *id.ID
	EventID     *id.ID
	EventType   string
	TenantID    string
	StatusClass StatusClass

	// From and To bound created_at, both inclusive.
	From *time.Time
	To   *time.Time
}

// Page is one page of the delivery log.
type Page struct {
	Deliveries []*Delivery `json:"deliveries"`
	NextCursor string      `json:"next_cursor,omitempty"`

	// Complete reports whether every row up to NextCursor (or to the end of
	// the range, when there is no cursor) was examined. It is false only when
	// a backend post-filtered over a bounded scan window and stopped before
	// the page filled, which redis does for the filters its sorted sets
	// cannot express. An empty page with Complete false means "none found in
	// what was searched", not "none".
	Complete bool `json:"complete"`
}

// Position is a decoded cursor: the page starts strictly after this row.
type Position struct {
	CreatedAt time.Time
	ID        string
}

// Prepare validates q and decodes its cursor. It returns the clamped limit
// and the position to continue from, nil for the first page.
func (q Query) Prepare() (int, *Position, error) {
	if q.StatusClass != StatusAny {
		if _, _, ok := q.StatusClass.Range(); !ok {
			return 0, nil, fmt.Errorf("%w: status class %q", errs.ErrInvalidFilter, q.StatusClass)
		}
	}
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

// Matches reports whether d passes every filter in q. The cursor is not a
// filter and is not checked. Backends that filter in Go use this, so none of
// them can disagree about what a filter means.
func (q Query) Matches(d *Delivery) bool {
	if q.State != nil && d.State != *q.State {
		return false
	}
	if q.EndpointID != nil && d.EndpointID != *q.EndpointID {
		return false
	}
	if q.EventID != nil && d.EventID != *q.EventID {
		return false
	}
	if q.EventType != "" && d.EventType != q.EventType {
		return false
	}
	if q.TenantID != "" && d.TenantID != q.TenantID {
		return false
	}
	if lo, hi, ok := q.StatusClass.Range(); ok && (d.LastStatusCode < lo || d.LastStatusCode > hi) {
		return false
	}
	if q.From != nil && d.CreatedAt.Before(*q.From) {
		return false
	}
	if q.To != nil && d.CreatedAt.After(*q.To) {
		return false
	}
	return true
}

// After reports whether d sorts strictly after p in the log's order:
// created_at descending, then id descending.
func (p *Position) After(d *Delivery) bool {
	if p == nil {
		return true
	}
	if !d.CreatedAt.Equal(p.CreatedAt) {
		return d.CreatedAt.Before(p.CreatedAt)
	}
	return d.ID.String() < p.ID
}

// CursorFor returns the cursor that continues after d.
func CursorFor(d *Delivery) string {
	return cursor.Encode(d.CreatedAt, d.ID.String())
}

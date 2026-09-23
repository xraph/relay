package sqlite

import (
	"context"
	"fmt"
	"strings"

	"github.com/xraph/relay/dlq"
	"github.com/xraph/relay/event"
)

// ListEventsPage returns one page of the event log, newest first.
func (s *Store) ListEventsPage(ctx context.Context, q event.Query) (*event.Page, error) {
	limit, pos, err := q.Prepare()
	if err != nil {
		return nil, err
	}
	var w where
	if q.Type != "" {
		w.add("type = %s", q.Type)
	}
	if q.TenantID != "" {
		w.add("tenant_id = %s", q.TenantID)
	}
	if q.From != nil {
		w.add("created_at >= %s", q.From.UTC())
	}
	if q.To != nil {
		w.add("created_at <= %s", q.To.UTC())
	}
	if pos != nil {
		at := pos.CreatedAt.UTC()
		w.add("(created_at < %s OR (created_at = %s AND id < %s))", at, at, pos.ID)
	}

	var models []eventModel
	sel := s.sdb.NewSelect(&models)
	if len(w.conds) > 0 {
		sel = sel.Where(strings.Join(w.conds, " AND "), w.args...)
	}
	if err := sel.OrderExpr("created_at DESC, id DESC").Limit(limit + 1).Scan(ctx); err != nil {
		return nil, err
	}
	page := &event.Page{Events: make([]*event.Event, 0, min(len(models), limit)), Complete: true}
	for i := range models {
		if i == limit {
			page.NextCursor = event.CursorFor(page.Events[limit-1])
			break
		}
		e, err := fromEventModel(&models[i])
		if err != nil {
			return nil, err
		}
		page.Events = append(page.Events, e)
	}
	return page, nil
}

// ListDLQPage returns one page of the DLQ, most recent failure first.
func (s *Store) ListDLQPage(ctx context.Context, q dlq.Query) (*dlq.Page, error) {
	limit, pos, err := q.Prepare()
	if err != nil {
		return nil, err
	}
	var w where
	if q.TenantID != "" {
		w.add("tenant_id = %s", q.TenantID)
	}
	if q.EndpointID != nil {
		w.add("endpoint_id = %s", q.EndpointID.String())
	}
	if q.From != nil {
		w.add("failed_at >= %s", q.From.UTC())
	}
	if q.To != nil {
		w.add("failed_at <= %s", q.To.UTC())
	}
	if q.Replayed != nil {
		if *q.Replayed {
			w.conds = append(w.conds, "replayed_at IS NOT NULL")
		} else {
			w.conds = append(w.conds, "replayed_at IS NULL")
		}
	}
	if pos != nil {
		at := pos.FailedAt.UTC()
		w.add("(failed_at < %s OR (failed_at = %s AND id < %s))", at, at, pos.ID)
	}

	var models []dlqEntryModel
	sel := s.sdb.NewSelect(&models)
	if len(w.conds) > 0 {
		sel = sel.Where(strings.Join(w.conds, " AND "), w.args...)
	}
	if err := sel.OrderExpr("failed_at DESC, id DESC").Limit(limit + 1).Scan(ctx); err != nil {
		return nil, err
	}
	page := &dlq.Page{Entries: make([]*dlq.Entry, 0, min(len(models), limit)), Complete: true}
	for i := range models {
		if i == limit {
			page.NextCursor = dlq.CursorFor(page.Entries[limit-1])
			break
		}
		e, err := fromDLQEntryModel(&models[i])
		if err != nil {
			return nil, err
		}
		page.Entries = append(page.Entries, e)
	}
	return page, nil
}

// where collects conditions with numbered or positional placeholders, so
// one clause can use a value more than once without the caller counting.
type where struct {
	conds []string
	args  []any
}

func (w *where) add(format string, vals ...any) {
	marks := make([]any, len(vals))
	for i, v := range vals {
		w.args = append(w.args, v)
		marks[i] = placeholder(len(w.args))
	}
	w.conds = append(w.conds, fmt.Sprintf(format, marks...))
}

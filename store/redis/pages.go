package redis

import (
	"context"
	"fmt"
	"math"

	"github.com/xraph/relay/dlq"
	"github.com/xraph/relay/event"
)

// ListEventsPage returns one page of the event log, newest first. The tenant
// picks the set to walk; the type is filtered in Go, within the scan window.
func (s *Store) ListEventsPage(ctx context.Context, q event.Query) (*event.Page, error) {
	limit, pos, err := q.Prepare()
	if err != nil {
		return nil, err
	}
	index := zEventAll
	if q.TenantID != "" {
		index = zEventTenant + q.TenantID
	}
	upper, lower := bounds(q.From, q.To)
	if pos != nil {
		upper = math.Min(upper, scoreFromTime(pos.CreatedAt))
	}
	items, next, complete, walkErr := walk(ctx, s, index, upper, lower, limit,
		func(ctx context.Context, member string) (*event.Event, error) {
			var m eventModel
			if getErr := s.getEntity(ctx, entityKey(prefixEvent, member), &m); getErr != nil {
				return nil, getErr
			}
			return fromEventModel(&m)
		},
		pos.After, q.Matches, event.CursorFor)
	if walkErr != nil {
		return nil, fmt.Errorf("relay/redis: list events page: %w", walkErr)
	}
	return &event.Page{Events: items, NextCursor: next, Complete: complete}, nil
}

// ListDLQPage returns one page of the DLQ, most recent failure first. The
// endpoint or tenant picks the set to walk; replayed is filtered in Go.
func (s *Store) ListDLQPage(ctx context.Context, q dlq.Query) (*dlq.Page, error) {
	limit, pos, err := q.Prepare()
	if err != nil {
		return nil, err
	}
	index := zDLQAll
	switch {
	case q.EndpointID != nil:
		index = zDLQEndpoint + q.EndpointID.String()
	case q.TenantID != "":
		index = zDLQTenant + q.TenantID
	}
	upper, lower := bounds(q.From, q.To)
	if pos != nil {
		upper = math.Min(upper, scoreFromTime(pos.FailedAt))
	}
	items, next, complete, walkErr := walk(ctx, s, index, upper, lower, limit,
		func(ctx context.Context, member string) (*dlq.Entry, error) {
			var m dlqEntryModel
			if getErr := s.getEntity(ctx, entityKey(prefixDLQ, member), &m); getErr != nil {
				return nil, getErr
			}
			return fromDLQEntryModel(&m)
		},
		pos.After, q.Matches, dlq.CursorFor)
	if walkErr != nil {
		return nil, fmt.Errorf("relay/redis: list dlq page: %w", walkErr)
	}
	return &dlq.Page{Entries: items, NextCursor: next, Complete: complete}, nil
}

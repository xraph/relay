package redis

import (
	"context"
	"errors"
	"fmt"
	"math"
	"strconv"
	"time"

	goredis "github.com/redis/go-redis/v9"

	"github.com/xraph/relay/delivery"
)

// defaultScanWindow is how many index entries one ListDeliveries call
// examines before it gives up on filling the page and says so.
const defaultScanWindow = 1000

// scanBatch is how many index entries are read per round trip.
const scanBatch = 200

// ErrDeliveryIndexNotBuilt is returned by a delivery listing that needs the
// global delivery index before Migrate has built it. Without this the log
// would be empty, and an empty log reads as "nothing was delivered".
var ErrDeliveryIndexNotBuilt = errors.New(
	"relay/redis: the delivery index has not been built; run Migrate")

// ListDeliveries returns one page of the delivery log, newest first.
//
// Redis can walk a sorted set in time order and nothing else. The endpoint and
// event filters pick which set to walk, and the time range and cursor bound
// the walk. Every other filter is applied in Go to the rows the walk reads, at
// most scanWindow of them per call. When the window runs out before the page
// fills, the page comes back with Complete false and a cursor at the last row
// examined, so an empty result says "none found in what was searched" rather
// than "none".
func (s *Store) ListDeliveries(ctx context.Context, q delivery.Query) (*delivery.Page, error) {
	limit, pos, err := q.Prepare()
	if err != nil {
		return nil, err
	}

	index := zDeliveryAll
	switch {
	case q.EndpointID != nil:
		index = zDeliveryEP + q.EndpointID.String()
	case q.EventID != nil:
		index = zDeliveryEvt + q.EventID.String()
	default:
		built, existsErr := s.exists(ctx, deliveryIndexBuilt)
		if existsErr != nil {
			return nil, fmt.Errorf("relay/redis: check delivery index: %w", existsErr)
		}
		if !built {
			return nil, ErrDeliveryIndexNotBuilt
		}
	}

	upper, lower := bounds(q.From, q.To)
	if pos != nil {
		upper = math.Min(upper, scoreFromTime(pos.CreatedAt))
	}

	items, next, complete, walkErr := walk(ctx, s, index, upper, lower, limit,
		func(ctx context.Context, member string) (*delivery.Delivery, error) {
			var m deliveryModel
			if getErr := s.getEntity(ctx, entityKey(prefixDelivery, member), &m); getErr != nil {
				return nil, getErr
			}
			return fromDeliveryModel(&m)
		},
		pos.After, q.Matches, delivery.CursorFor)
	if walkErr != nil {
		return nil, fmt.Errorf("relay/redis: list deliveries: %w", walkErr)
	}
	return &delivery.Page{Deliveries: items, NextCursor: next, Complete: complete}, nil
}

// walk reads a sorted set newest first between lower and upper (inclusive),
// loads each member, and keeps the ones that sort after the cursor and match.
// It examines at most s.scanWindow members. Stopping at the window before the
// page fills returns complete false with a cursor at the last row examined.
// A member whose record is gone is skipped.
func walk[T any](
	ctx context.Context, s *Store, index string, upper, lower float64, limit int,
	load func(context.Context, string) (T, error),
	after, matches func(T) bool,
	cursorFor func(T) string,
) (items []T, next string, complete bool, err error) {
	items = make([]T, 0, limit)
	var last T
	haveLast := false
	examined := 0

	for offset := int64(0); ; offset += scanBatch {
		entries, rangeErr := s.zRevRangeByScoreWithScores(ctx, index, &goredis.ZRangeBy{
			Max:    scoreString(upper),
			Min:    scoreString(lower),
			Offset: offset,
			Count:  scanBatch,
		})
		if rangeErr != nil {
			return nil, "", false, rangeErr
		}
		for _, e := range entries {
			if examined == s.scanWindow {
				// Out of window with more index left. What was examined is
				// covered; the rest is reachable from the cursor.
				if len(items) == limit {
					return items, cursorFor(items[limit-1]), true, nil
				}
				if haveLast {
					next = cursorFor(last)
				}
				return items, next, false, nil
			}
			examined++

			member, ok := e.Member.(string)
			if !ok {
				continue
			}
			row, loadErr := load(ctx, member)
			if loadErr != nil {
				if isNotFound(loadErr) {
					continue
				}
				return nil, "", false, loadErr
			}
			// The score bound is inclusive at the cursor, so rows in the
			// cursor's instant are checked by id here.
			if !after(row) {
				continue
			}
			last, haveLast = row, true
			if !matches(row) {
				continue
			}
			if len(items) == limit {
				// A match beyond the page: there is a next one.
				return items, cursorFor(items[limit-1]), true, nil
			}
			items = append(items, row)
		}
		if len(entries) < scanBatch {
			return items, "", true, nil
		}
	}
}

func scoreString(f float64) string {
	switch {
	case math.IsInf(f, 1):
		return "+inf"
	case math.IsInf(f, -1):
		return "-inf"
	default:
		return strconv.FormatFloat(f, 'f', -1, 64)
	}
}

// backfillDeliveryAll builds the global delivery index from the per-endpoint
// indexes, which every delivery has always been added to. Same shape and same
// markers as backfillEndpointAll: re-run now and then to repair the index
// after a rollback to a version that did not maintain it.
func (s *Store) backfillDeliveryAll(ctx context.Context) error {
	recent, err := s.exists(ctx, migratedDeliveryAllV1)
	if err != nil {
		return fmt.Errorf("relay/redis: check delivery backfill marker: %w", err)
	}
	if recent {
		return nil
	}
	keys, err := s.keysMatching(ctx, zDeliveryEP+"*")
	if err != nil {
		return fmt.Errorf("relay/redis: scan delivery indexes: %w", err)
	}
	for _, key := range keys {
		members, err := s.zRangeAllWithScores(ctx, key)
		if err != nil {
			if isWrongType(err) {
				continue
			}
			return fmt.Errorf("relay/redis: read delivery index %s: %w", key, err)
		}
		if len(members) == 0 {
			continue
		}
		if err := s.zAdd(ctx, zDeliveryAll, members...); err != nil {
			return fmt.Errorf("relay/redis: backfill delivery index: %w", err)
		}
	}
	pipe := s.pipeline(ctx)
	pipe.set(deliveryIndexBuilt, "1", 0)
	pipe.set(migratedDeliveryAllV1, "1", endpointBackfillTTL)
	if err := pipe.exec(); err != nil {
		return fmt.Errorf("relay/redis: record delivery backfill: %w", err)
	}
	return nil
}

// backfillDeliveryFields copies event type and tenant from each delivery's
// event onto deliveries written before those fields existed, once. The log
// filters on both in Go, so an old row missing them would never match a
// tenant or type filter.
func (s *Store) backfillDeliveryFields(ctx context.Context) error {
	done, err := s.exists(ctx, deliveryFieldsBackfilled)
	if err != nil {
		return fmt.Errorf("relay/redis: check delivery fields marker: %w", err)
	}
	if done {
		return nil
	}
	keys, err := s.keysMatching(ctx, prefixDelivery+"*")
	if err != nil {
		return fmt.Errorf("relay/redis: scan deliveries: %w", err)
	}
	for _, key := range keys {
		var m deliveryModel
		if err := s.getEntity(ctx, key, &m); err != nil {
			// Not a delivery record after all, or gone since the scan.
			continue
		}
		if m.EventType != "" || m.EventID == "" {
			continue
		}
		var evt eventModel
		if err := s.getEntity(ctx, entityKey(prefixEvent, m.EventID), &evt); err != nil {
			continue
		}
		m.EventType, m.TenantID = evt.Type, evt.TenantID
		if err := s.setEntity(ctx, key, &m); err != nil {
			return fmt.Errorf("relay/redis: backfill delivery %s: %w", m.ID, err)
		}
	}
	if err := s.setString(ctx, deliveryFieldsBackfilled, "1", 0); err != nil {
		return fmt.Errorf("relay/redis: record delivery fields backfill: %w", err)
	}
	return nil
}

// bounds turns an inclusive time window into sorted-set score bounds.
func bounds(from, to *time.Time) (upper, lower float64) {
	upper, lower = math.Inf(1), math.Inf(-1)
	if to != nil {
		upper = scoreFromTime(*to)
	}
	if from != nil {
		lower = scoreFromTime(*from)
	}
	return upper, lower
}

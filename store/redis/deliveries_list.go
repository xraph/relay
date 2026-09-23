package redis

import (
	"context"
	"errors"
	"fmt"
	"math"
	"strconv"

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
		built, err := s.rdb.Exists(ctx, deliveryIndexBuilt).Result()
		if err != nil {
			return nil, fmt.Errorf("relay/redis: check delivery index: %w", err)
		}
		if built == 0 {
			return nil, ErrDeliveryIndexNotBuilt
		}
	}

	upper, lower := math.Inf(1), math.Inf(-1)
	if q.To != nil {
		upper = scoreFromTime(*q.To)
	}
	if pos != nil {
		upper = math.Min(upper, scoreFromTime(pos.CreatedAt))
	}
	if q.From != nil {
		lower = scoreFromTime(*q.From)
	}

	page := &delivery.Page{Deliveries: make([]*delivery.Delivery, 0, limit), Complete: true}
	var lastExamined *delivery.Delivery
	examined := 0

	for offset := int64(0); ; offset += scanBatch {
		entries, err := s.rdb.ZRevRangeByScoreWithScores(ctx, index, &goredis.ZRangeBy{
			Max:    scoreString(upper),
			Min:    scoreString(lower),
			Offset: offset,
			Count:  scanBatch,
		}).Result()
		if err != nil {
			return nil, fmt.Errorf("relay/redis: list deliveries: %w", err)
		}
		for _, e := range entries {
			if examined == s.scanWindow {
				// Out of window with more index left. What was examined is
				// covered; the rest is reachable from the cursor.
				if len(page.Deliveries) < limit {
					page.Complete = false
					if lastExamined != nil {
						page.NextCursor = delivery.CursorFor(lastExamined)
					}
				} else {
					page.NextCursor = delivery.CursorFor(page.Deliveries[limit-1])
				}
				return page, nil
			}
			examined++

			member, ok := e.Member.(string)
			if !ok {
				continue
			}
			var m deliveryModel
			if err := s.getEntity(ctx, entityKey(prefixDelivery, member), &m); err != nil {
				if isNotFound(err) {
					continue
				}
				return nil, fmt.Errorf("relay/redis: list deliveries get: %w", err)
			}
			d, err := fromDeliveryModel(&m)
			if err != nil {
				return nil, err
			}
			// The score bound is inclusive at the cursor, so rows created in
			// the cursor's instant are checked by id here.
			if !pos.After(d) {
				continue
			}
			lastExamined = d
			if !q.Matches(d) {
				continue
			}
			if len(page.Deliveries) == limit {
				// A match beyond the page: there is a next one.
				page.NextCursor = delivery.CursorFor(page.Deliveries[limit-1])
				return page, nil
			}
			page.Deliveries = append(page.Deliveries, d)
		}
		if len(entries) < scanBatch {
			return page, nil
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
	recent, err := s.rdb.Exists(ctx, migratedDeliveryAllV1).Result()
	if err != nil {
		return fmt.Errorf("relay/redis: check delivery backfill marker: %w", err)
	}
	if recent == 1 {
		return nil
	}
	keys, err := s.keysMatching(ctx, zDeliveryEP+"*")
	if err != nil {
		return fmt.Errorf("relay/redis: scan delivery indexes: %w", err)
	}
	for _, key := range keys {
		members, err := s.rdb.ZRangeWithScores(ctx, key, 0, -1).Result()
		if err != nil {
			if isWrongType(err) {
				continue
			}
			return fmt.Errorf("relay/redis: read delivery index %s: %w", key, err)
		}
		if len(members) == 0 {
			continue
		}
		if err := s.rdb.ZAdd(ctx, zDeliveryAll, members...).Err(); err != nil {
			return fmt.Errorf("relay/redis: backfill delivery index: %w", err)
		}
	}
	pipe := s.rdb.Pipeline()
	pipe.Set(ctx, deliveryIndexBuilt, "1", 0)
	pipe.Set(ctx, migratedDeliveryAllV1, "1", endpointBackfillTTL)
	if _, err := pipe.Exec(ctx); err != nil {
		return fmt.Errorf("relay/redis: record delivery backfill: %w", err)
	}
	return nil
}

// backfillDeliveryFields copies event type and tenant from each delivery's
// event onto deliveries written before those fields existed, once. The log
// filters on both in Go, so an old row missing them would never match a
// tenant or type filter.
func (s *Store) backfillDeliveryFields(ctx context.Context) error {
	done, err := s.rdb.Exists(ctx, deliveryFieldsBackfilled).Result()
	if err != nil {
		return fmt.Errorf("relay/redis: check delivery fields marker: %w", err)
	}
	if done == 1 {
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
	if err := s.rdb.Set(ctx, deliveryFieldsBackfilled, "1", 0).Err(); err != nil {
		return fmt.Errorf("relay/redis: record delivery fields backfill: %w", err)
	}
	return nil
}

package redis

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"strconv"
	"time"

	goredis "github.com/redis/go-redis/v9"

	"github.com/xraph/grove/kv"
	"github.com/xraph/grove/kv/drivers/redisdriver"

	relaystore "github.com/xraph/relay/store"
)

// compile-time interface check
var _ relaystore.Store = (*Store)(nil)

// Store implements store.Store using Redis via Grove KV.
type Store struct {
	kv  *kv.Store
	rdb goredis.UniversalClient
}

// New creates a new Redis store backed by Grove KV.
func New(store *kv.Store) *Store {
	return &Store{
		kv:  store,
		rdb: redisdriver.UnwrapClient(store),
	}
}

// Migrate is a no-op for Redis (no schema migrations needed).
// Migrate runs one-time data migrations. Redis has no schema, so the only work
// is backfilling indexes that newer code maintains and older code did not.
// Each migration checks a marker first, so a normal boot does no scanning.
func (s *Store) Migrate(ctx context.Context) error {
	return s.backfillEndpointAll(ctx)
}

// backfillEndpointAll adds every existing endpoint to zEndpointAll. Endpoints
// created before that index existed are only in their per-tenant index, so
// without this an every-tenant listing would silently omit all of them.
//
// ZADD is idempotent, so a run that dies partway is safe to repeat; the marker
// is written only after the scan completes. On a cluster it scans every master,
// since a plain SCAN walks a single node and would miss most keys.
func (s *Store) backfillEndpointAll(ctx context.Context) error {
	done, err := s.rdb.Exists(ctx, migratedEndpointAllV1).Result()
	if err != nil {
		return fmt.Errorf("relay/redis: check endpoint backfill marker: %w", err)
	}
	if done == 1 {
		return nil
	}

	index := func(ctx context.Context, node goredis.UniversalClient) error {
		var cursor uint64
		for {
			keys, next, scanErr := node.Scan(ctx, cursor, prefixEndpoint+"*", 500).Result()
			if scanErr != nil {
				return fmt.Errorf("relay/redis: scan endpoints: %w", scanErr)
			}
			queued := 0
			pipe := s.rdb.Pipeline()
			for _, key := range keys {
				var m endpointModel
				if getErr := s.getEntity(ctx, key, &m); getErr != nil {
					if isNotFound(getErr) {
						continue // deleted since the scan saw it
					}
					return fmt.Errorf("relay/redis: read endpoint %s: %w", key, getErr)
				}
				pipe.ZAdd(ctx, zEndpointAll, goredis.Z{Score: scoreFromTime(m.CreatedAt), Member: m.ID})
				queued++
			}
			if queued > 0 {
				if _, execErr := pipe.Exec(ctx); execErr != nil {
					return fmt.Errorf("relay/redis: backfill endpoint index: %w", execErr)
				}
			}
			cursor = next
			if cursor == 0 {
				return nil
			}
		}
	}

	if cluster, ok := s.rdb.(*goredis.ClusterClient); ok {
		err = cluster.ForEachMaster(ctx, func(ctx context.Context, node *goredis.Client) error {
			return index(ctx, node)
		})
	} else {
		err = index(ctx, s.rdb)
	}
	if err != nil {
		return err
	}
	return s.rdb.Set(ctx, migratedEndpointAllV1, "1", 0).Err()
}

// Ping checks Redis connectivity.
func (s *Store) Ping(ctx context.Context) error {
	return s.kv.Ping(ctx)
}

// Close closes the KV store.
func (s *Store) Close() error {
	return s.kv.Close()
}

// now returns the current UTC time.
func now() time.Time {
	return time.Now().UTC()
}

// scoreFromTime converts a time.Time to a sorted set score (unix seconds as float64).
func scoreFromTime(t time.Time) float64 {
	return float64(t.UnixNano()) / 1e9
}

// isNotFound checks if an error is a KV not-found sentinel.
func isNotFound(err error) bool {
	return errors.Is(err, kv.ErrNotFound)
}

// isRedisNil checks if an error is a Redis nil (key not found).
func isRedisNil(err error) bool {
	return errors.Is(err, goredis.Nil)
}

// getEntity retrieves and decodes a JSON entity from a KV key.
func (s *Store) getEntity(ctx context.Context, key string, dest any) error {
	raw, err := s.kv.GetRaw(ctx, key)
	if err != nil {
		return err
	}
	return json.Unmarshal(raw, dest)
}

// setEntity encodes and stores a JSON entity under a KV key.
func (s *Store) setEntity(ctx context.Context, key string, value any) error {
	raw, err := json.Marshal(value)
	if err != nil {
		return fmt.Errorf("relay/redis: marshal entity: %w", err)
	}
	return s.kv.SetRaw(ctx, key, raw)
}

// zRangeByScoreIDs returns all member IDs from a sorted set within a score range.
func (s *Store) zRangeByScoreIDs(ctx context.Context, key string, lo, hi float64) ([]string, error) {
	minStr := "-inf"
	maxStr := "+inf"
	if !math.IsInf(lo, -1) {
		minStr = strconv.FormatFloat(lo, 'f', -1, 64)
	}
	if !math.IsInf(hi, 1) {
		maxStr = strconv.FormatFloat(hi, 'f', -1, 64)
	}
	return s.rdb.ZRangeByScore(ctx, key, &goredis.ZRangeBy{
		Min: minStr,
		Max: maxStr,
	}).Result()
}

// applyPagination applies offset and limit to a slice.
func applyPagination[T any](items []*T, offset, limit int) []*T {
	if offset > 0 && offset < len(items) {
		items = items[offset:]
	} else if offset >= len(items) {
		return nil
	}
	if limit > 0 && limit < len(items) {
		items = items[:limit]
	}
	return items
}

package redis

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	goredis "github.com/redis/go-redis/v9"

	"github.com/xraph/grove/hook"
	"github.com/xraph/grove/kv"
)

// This file is the one place relay's redis store touches the raw go-redis
// client for anything that names a key.
//
// Records go through kv: GetRaw, SetRaw and Delete run grove's pre-query hooks,
// so a namespace hook moves them under its prefix. Indexes, markers, the replay
// claim and the delivery queue use commands kv has no method for, or that
// are pipelined, so they run on the raw client, which no hook ever sees. If
// they used the logical key as is, a namespaced store would keep its records
// under "app1:relay:ep:..." and its indexes under "relay:z:ep:...", and two
// namespaces on one redis would share every index while owning separate
// records.
//
// So every raw command gets its key from physicalKey, which runs the store's
// own hooks over the key the way grove does for the equivalent kv command.
// Index members are record ids, never keys, and none of the Lua we run builds a
// key inside the script, so keys passed in are the only ones there are.

// kvKeysValue is the QueryContext.Values entry grove's kv store hands the
// pre-query hooks the keys of a command in, and reads the result back from.
// Grove does not export a function that resolves a key, so this mirrors its
// unexported newCommandContext and resolveKeys. Migrate checks the result
// against what kv itself does, so a change to that contract fails loudly there
// instead of silently dropping the namespace.
const kvKeysValue = "_kv_keys"

// relayOps is every kv operation whose key mapping relay depends on. A key has
// to resolve to the same physical key under all of them: records are written
// with OpSet and read with OpGet, an index is added to with OpZAdd and read with
// OpZRange, and so on.
var relayOps = []hook.Operation{
	kv.OpGet, kv.OpSet, kv.OpDelete, kv.OpExists, kv.OpScan,
	kv.OpZAdd, kv.OpZRange, kv.OpZRem, kv.OpZCard,
	kv.OpSAdd, kv.OpSRem, kv.OpSMbrs,
	kv.OpEval, kv.OpPublish, kv.OpSubscr,
}

// physicalKey returns the key redis sees for the logical key, after the kv
// store's pre-query hooks have run for op. Without a key-rewriting hook it
// returns key unchanged. A hook that denies the operation, or that adds or drops
// keys, is an error, as it is for the equivalent kv call.
func (s *Store) physicalKey(ctx context.Context, op hook.Operation, key string) (string, error) {
	qc := &hook.QueryContext{
		Operation: op,
		RawQuery:  key,
		Values:    map[string]any{kvKeysValue: []string{key}},
	}
	result, err := s.kv.Hooks().RunPreQuery(ctx, qc)
	if err != nil {
		return "", err
	}
	if result != nil && result.Decision == hook.Deny {
		return "", kv.ErrHookDenied
	}
	keys, ok := qc.Values[kvKeysValue].([]string)
	if !ok {
		return key, nil
	}
	if len(keys) != 1 {
		return "", fmt.Errorf("%w: 1 key became %d", kv.ErrHookKeyCount, len(keys))
	}
	return keys[0], nil
}

func (s *Store) zAdd(ctx context.Context, key string, members ...goredis.Z) error {
	k, err := s.physicalKey(ctx, kv.OpZAdd, key)
	if err != nil {
		return err
	}
	return s.rdb.ZAdd(ctx, k, members...).Err()
}

func (s *Store) zRem(ctx context.Context, key string, members ...any) error {
	k, err := s.physicalKey(ctx, kv.OpZRem, key)
	if err != nil {
		return err
	}
	return s.rdb.ZRem(ctx, k, members...).Err()
}

// zRangeAll returns every member of a sorted set, lowest score first.
func (s *Store) zRangeAll(ctx context.Context, key string) ([]string, error) {
	k, err := s.physicalKey(ctx, kv.OpZRange, key)
	if err != nil {
		return nil, err
	}
	return s.rdb.ZRange(ctx, k, 0, -1).Result()
}

func (s *Store) zRangeAllWithScores(ctx context.Context, key string) ([]goredis.Z, error) {
	k, err := s.physicalKey(ctx, kv.OpZRange, key)
	if err != nil {
		return nil, err
	}
	return s.rdb.ZRangeWithScores(ctx, k, 0, -1).Result()
}

func (s *Store) zRevRangeByScoreWithScores(ctx context.Context, key string, by *goredis.ZRangeBy) ([]goredis.Z, error) {
	k, err := s.physicalKey(ctx, kv.OpZRange, key)
	if err != nil {
		return nil, err
	}
	return s.rdb.ZRevRangeByScoreWithScores(ctx, k, by).Result()
}

func (s *Store) zRangeByScore(ctx context.Context, key string, by *goredis.ZRangeBy) ([]string, error) {
	k, err := s.physicalKey(ctx, kv.OpZRange, key)
	if err != nil {
		return nil, err
	}
	return s.rdb.ZRangeByScore(ctx, k, by).Result()
}

func (s *Store) zCard(ctx context.Context, key string) (int64, error) {
	k, err := s.physicalKey(ctx, kv.OpZCard, key)
	if err != nil {
		return 0, err
	}
	return s.rdb.ZCard(ctx, k).Result()
}

func (s *Store) sAdd(ctx context.Context, key string, members ...any) error {
	k, err := s.physicalKey(ctx, kv.OpSAdd, key)
	if err != nil {
		return err
	}
	return s.rdb.SAdd(ctx, k, members...).Err()
}

func (s *Store) sRem(ctx context.Context, key string, members ...any) error {
	k, err := s.physicalKey(ctx, kv.OpSRem, key)
	if err != nil {
		return err
	}
	return s.rdb.SRem(ctx, k, members...).Err()
}

func (s *Store) sMembers(ctx context.Context, key string) ([]string, error) {
	k, err := s.physicalKey(ctx, kv.OpSMbrs, key)
	if err != nil {
		return nil, err
	}
	return s.rdb.SMembers(ctx, k).Result()
}

// exists reports whether a plain key is set.
func (s *Store) exists(ctx context.Context, key string) (bool, error) {
	k, err := s.physicalKey(ctx, kv.OpExists, key)
	if err != nil {
		return false, err
	}
	n, err := s.rdb.Exists(ctx, k).Result()
	return n > 0, err
}

// getString reads a plain key. A missing key is goredis.Nil, as it is from the
// client.
func (s *Store) getString(ctx context.Context, key string) (string, error) {
	k, err := s.physicalKey(ctx, kv.OpGet, key)
	if err != nil {
		return "", err
	}
	return s.rdb.Get(ctx, k).Result()
}

func (s *Store) setString(ctx context.Context, key, value string, ttl time.Duration) error {
	k, err := s.physicalKey(ctx, kv.OpSet, key)
	if err != nil {
		return err
	}
	return s.rdb.Set(ctx, k, value, ttl).Err()
}

func (s *Store) setNX(ctx context.Context, key, value string) (bool, error) {
	k, err := s.physicalKey(ctx, kv.OpSet, key)
	if err != nil {
		return false, err
	}
	return s.rdb.SetNX(ctx, k, value, 0).Result()
}

// rawPipe is a pipeline whose keys are resolved as they are queued. The first
// key that cannot be resolved is held and returned by exec, which then sends
// nothing: a hook that denies one command must not leave the others applied.
type rawPipe struct {
	s    *Store
	ctx  context.Context //nolint:containedctx // a pipeline lives for one call
	pipe goredis.Pipeliner
	err  error
}

func (s *Store) pipeline(ctx context.Context) *rawPipe {
	return &rawPipe{s: s, ctx: ctx, pipe: s.rdb.Pipeline()}
}

func (p *rawPipe) key(op hook.Operation, key string) (string, bool) {
	if p.err != nil {
		return "", false
	}
	k, err := p.s.physicalKey(p.ctx, op, key)
	if err != nil {
		p.err = err
		return "", false
	}
	return k, true
}

func (p *rawPipe) zAdd(key string, members ...goredis.Z) {
	if k, ok := p.key(kv.OpZAdd, key); ok {
		p.pipe.ZAdd(p.ctx, k, members...)
	}
}

func (p *rawPipe) zRem(key string, members ...any) {
	if k, ok := p.key(kv.OpZRem, key); ok {
		p.pipe.ZRem(p.ctx, k, members...)
	}
}

func (p *rawPipe) sAdd(key string, members ...any) {
	if k, ok := p.key(kv.OpSAdd, key); ok {
		p.pipe.SAdd(p.ctx, k, members...)
	}
}

func (p *rawPipe) sRem(key string, members ...any) {
	if k, ok := p.key(kv.OpSRem, key); ok {
		p.pipe.SRem(p.ctx, k, members...)
	}
}

func (p *rawPipe) set(key string, value any, ttl time.Duration) {
	if k, ok := p.key(kv.OpSet, key); ok {
		p.pipe.Set(p.ctx, k, value, ttl)
	}
}

func (p *rawPipe) exec() error {
	if p.err != nil {
		return p.err
	}
	_, err := p.pipe.Exec(p.ctx)
	return err
}

// scanKeys calls fn with every logical key matching the logical pattern. The
// pattern is resolved first, so a namespaced store walks only its own
// keyspace, and the prefix the hook added is taken off each key found so fn can
// hand it straight to the other methods here, which resolve it again.
//
// A hook that rewrites keys other than by putting something in front of them
// leaves no prefix to take off. Migrate refuses such a store, and this returns
// ErrKVKeysInconsistent if it is reached anyway.
//
// On a cluster fn is called from one goroutine per master.
func (s *Store) scanKeys(ctx context.Context, pattern string, fn func(key string) error) error {
	resolved, err := s.physicalKey(ctx, kv.OpScan, pattern)
	if err != nil {
		return err
	}
	prefix, ok := strings.CutSuffix(resolved, pattern)
	if !ok {
		return fmt.Errorf("%w: scan %q resolves to %q, which is not a prefix of it",
			ErrKVKeysInconsistent, pattern, resolved)
	}
	return s.scanPhysical(ctx, resolved, func(key string) error {
		return fn(strings.TrimPrefix(key, prefix))
	})
}

// keysMatching returns every logical key matching the logical pattern.
func (s *Store) keysMatching(ctx context.Context, pattern string) ([]string, error) {
	var (
		mu  sync.Mutex
		out []string
	)
	err := s.scanKeys(ctx, pattern, func(key string) error {
		mu.Lock()
		out = append(out, key)
		mu.Unlock()
		return nil
	})
	return out, err
}

// scanPhysical calls fn with every key matching a pattern as redis sees it.
// On a cluster it scans every master, since a plain SCAN walks a single node,
// and fn is called from one goroutine per master.
func (s *Store) scanPhysical(ctx context.Context, pattern string, fn func(key string) error) error {
	scan := func(ctx context.Context, node goredis.UniversalClient) error {
		var cursor uint64
		for {
			keys, next, err := node.Scan(ctx, cursor, pattern, 500).Result()
			if err != nil {
				return err
			}
			for _, k := range keys {
				if fnErr := fn(k); fnErr != nil {
					return fnErr
				}
			}
			cursor = next
			if cursor == 0 {
				return nil
			}
		}
	}
	if cluster, ok := s.rdb.(*goredis.ClusterClient); ok {
		return cluster.ForEachMaster(ctx, func(ctx context.Context, node *goredis.Client) error {
			return scan(ctx, node)
		})
	}
	return scan(ctx, s.rdb)
}

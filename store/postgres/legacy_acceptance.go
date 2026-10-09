package postgres

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/json"

	"github.com/xraph/relay/catalog"
	"github.com/xraph/relay/event"
	"github.com/xraph/relay/id"
	"github.com/xraph/relay/internal/acceptanceutil"
)

func (s *Store) SendEvent(ctx context.Context, evt *event.Event, maxAttempts int) (int, error) {
	raw, err := json.Marshal(evt.Data)
	if err != nil {
		return 0, err
	}
	tx, err := s.pg.BeginTxQuery(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer func() { _ = tx.Rollback() }() //nolint:errcheck // rollback also runs after a successful commit
	if evt.IdempotencyKey != "" {
		sum := sha256.Sum256([]byte("legacy:" + evt.IdempotencyKey))
		lockID := int64(binary.BigEndian.Uint64(sum[:8])) //nolint:gosec // signed lock ID uses all hash bits
		if _, err = tx.NewRaw("SELECT pg_advisory_xact_lock($1)", lockID).Exec(ctx); err != nil {
			return 0, err
		}
		m := new(eventModel)
		err = tx.NewSelect(m).Where("idempotency_key = $1", evt.IdempotencyKey).Scan(ctx)
		if err == nil {
			return 0, nil
		}
		if !isNoRows(err) {
			return 0, err
		}
	}
	var endpoints []endpointModel
	if checkErr := tx.NewSelect(&endpoints).Where("tenant_id = $1", evt.TenantID).Where("enabled = TRUE").Scan(ctx); checkErr != nil {
		return 0, checkErr
	}
	ids := []id.ID{}
	for _, ep := range endpoints {
		for _, pattern := range ep.EventTypes {
			if catalog.Match(pattern, evt.Type) {
				epID, e := id.ParseEndpointID(ep.ID)
				if e != nil {
					return 0, e
				}
				ids = append(ids, epID)
				break
			}
		}
	}
	ds := acceptanceutil.Fanout(evt, ids, maxAttempts)
	model := toEventModel(evt)
	model.Data = raw
	if _, err = tx.NewInsert(model).Exec(ctx); err != nil {
		return 0, err
	}
	for _, d := range ds {
		if _, err = tx.NewInsert(toDeliveryModel(d)).Exec(ctx); err != nil {
			return 0, err
		}
	}
	if checkErr := tx.Commit(); checkErr != nil {
		return 0, checkErr
	}
	s.notifyWake(ctx)
	return len(ds), nil
}

package memory

import (
	"context"
	"encoding/json"

	"github.com/xraph/relay"
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
	s.mu.Lock()
	defer s.mu.Unlock()
	if checkErr := ctx.Err(); checkErr != nil {
		return 0, checkErr
	}
	if s.closed {
		return 0, relay.ErrStoreClosed
	}
	if evt.IdempotencyKey != "" && s.eventsByIdemKey[evt.IdempotencyKey] != nil {
		return 0, nil
	}
	ids := []id.ID{}
	for _, ep := range s.endpoints {
		if !ep.Enabled || ep.TenantID != evt.TenantID {
			continue
		}
		for _, pattern := range ep.EventTypes {
			if catalog.Match(pattern, evt.Type) {
				ids = append(ids, ep.ID)
				break
			}
		}
	}
	ds := acceptanceutil.Fanout(evt, ids, maxAttempts)
	cp := *evt
	cp.Data = json.RawMessage(raw)
	s.events[evt.ID.String()] = &cp
	if evt.IdempotencyKey != "" {
		s.eventsByIdemKey[evt.IdempotencyKey] = &cp
	}
	for _, d := range ds {
		s.deliveries[d.ID.String()] = d
	}
	return len(ds), nil
}

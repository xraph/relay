package redis

import (
	"context"
	"fmt"
	"math"
	"time"

	goredis "github.com/redis/go-redis/v9"

	"github.com/xraph/relay/delivery"
	"github.com/xraph/relay/id"
)

type attemptModel struct {
	ID            string     `json:"id"`
	DeliveryID    string     `json:"delivery_id"`
	AttemptNum    int        `json:"attempt_num"`
	StatusCode    int        `json:"status_code"`
	Error         string     `json:"error"`
	Response      string     `json:"response"`
	LatencyMs     int        `json:"latency_ms"`
	Outcome       string     `json:"outcome"`
	NextAttemptAt *time.Time `json:"next_attempt_at,omitempty"`
	AttemptedAt   time.Time  `json:"attempted_at"`
}

// RecordAttempt stores one attempt: the entity, a per-delivery set ordered by
// attempt number, and a global set ordered by time for PurgeAttempts.
func (s *Store) RecordAttempt(ctx context.Context, a *delivery.Attempt) error {
	m := &attemptModel{
		ID:            a.ID.String(),
		DeliveryID:    a.DeliveryID.String(),
		AttemptNum:    a.AttemptNum,
		StatusCode:    a.StatusCode,
		Error:         a.Error,
		Response:      a.Response,
		LatencyMs:     a.LatencyMs,
		Outcome:       string(a.Outcome),
		NextAttemptAt: a.NextAttemptAt,
		AttemptedAt:   a.AttemptedAt,
	}
	if err := s.setEntity(ctx, entityKey(prefixAttempt, m.ID), m); err != nil {
		return fmt.Errorf("relay/redis: record attempt: %w", err)
	}
	pipe := s.pipeline(ctx)
	pipe.zAdd(zAttemptDel+m.DeliveryID, goredis.Z{Score: float64(m.AttemptNum), Member: m.ID})
	pipe.zAdd(zAttemptAll, goredis.Z{Score: scoreFromTime(m.AttemptedAt), Member: m.ID})
	if err := pipe.exec(); err != nil {
		return fmt.Errorf("relay/redis: index attempt: %w", err)
	}
	return nil
}

// ListAttempts returns a delivery's attempts by attempt number.
func (s *Store) ListAttempts(ctx context.Context, delID id.ID) ([]*delivery.Attempt, error) {
	ids, err := s.zRangeAll(ctx, zAttemptDel+delID.String())
	if err != nil {
		return nil, fmt.Errorf("relay/redis: list attempts: %w", err)
	}
	out := make([]*delivery.Attempt, 0, len(ids))
	for _, attID := range ids {
		var m attemptModel
		if err := s.getEntity(ctx, entityKey(prefixAttempt, attID), &m); err != nil {
			if isNotFound(err) {
				continue
			}
			return nil, fmt.Errorf("relay/redis: get attempt: %w", err)
		}
		a, err := fromAttemptModel(&m)
		if err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	return out, nil
}

// PurgeAttempts deletes attempts made before the cutoff.
func (s *Store) PurgeAttempts(ctx context.Context, before time.Time) (int64, error) {
	// The set's upper bound is inclusive; the cutoff is not.
	ids, err := s.zRangeByScoreIDs(ctx, zAttemptAll, math.Inf(-1), math.Nextafter(scoreFromTime(before), math.Inf(-1)))
	if err != nil {
		return 0, fmt.Errorf("relay/redis: purge attempts: %w", err)
	}
	var n int64
	for _, attID := range ids {
		var m attemptModel
		if err := s.getEntity(ctx, entityKey(prefixAttempt, attID), &m); err != nil && !isNotFound(err) {
			return n, fmt.Errorf("relay/redis: purge get attempt: %w", err)
		}
		// The record goes through kv, which resolves its key and runs the
		// delete hooks. A raw DEL would miss both.
		if err := s.kv.Delete(ctx, entityKey(prefixAttempt, attID)); err != nil {
			return n, fmt.Errorf("relay/redis: purge attempt: %w", err)
		}
		pipe := s.pipeline(ctx)
		pipe.zRem(zAttemptAll, attID)
		if m.DeliveryID != "" {
			pipe.zRem(zAttemptDel+m.DeliveryID, attID)
		}
		if err := pipe.exec(); err != nil {
			return n, fmt.Errorf("relay/redis: purge attempt: %w", err)
		}
		n++
	}
	return n, nil
}

func fromAttemptModel(m *attemptModel) (*delivery.Attempt, error) {
	attID, err := id.ParseAttemptID(m.ID)
	if err != nil {
		return nil, fmt.Errorf("parse attempt ID %q: %w", m.ID, err)
	}
	delID, err := id.ParseDeliveryID(m.DeliveryID)
	if err != nil {
		return nil, fmt.Errorf("parse delivery ID %q: %w", m.DeliveryID, err)
	}
	return &delivery.Attempt{
		ID:            attID,
		DeliveryID:    delID,
		AttemptNum:    m.AttemptNum,
		StatusCode:    m.StatusCode,
		Error:         m.Error,
		Response:      m.Response,
		LatencyMs:     m.LatencyMs,
		Outcome:       delivery.Outcome(m.Outcome),
		NextAttemptAt: m.NextAttemptAt,
		AttemptedAt:   m.AttemptedAt,
	}, nil
}

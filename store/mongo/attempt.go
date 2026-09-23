package mongo

import (
	"context"
	"fmt"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"

	"github.com/xraph/grove"

	"github.com/xraph/relay/delivery"
	"github.com/xraph/relay/id"
)

type attemptModel struct {
	grove.BaseModel `grove:"table:relay_delivery_attempts"`

	ID            string     `grove:"id,pk"           bson:"_id"`
	DeliveryID    string     `grove:"delivery_id"     bson:"delivery_id"`
	AttemptNum    int        `grove:"attempt_num"     bson:"attempt_num"`
	StatusCode    int        `grove:"status_code"     bson:"status_code"`
	Error         string     `grove:"error"           bson:"error"`
	Response      string     `grove:"response"        bson:"response"`
	LatencyMs     int        `grove:"latency_ms"      bson:"latency_ms"`
	Outcome       string     `grove:"outcome"         bson:"outcome"`
	NextAttemptAt *time.Time `grove:"next_attempt_at" bson:"next_attempt_at,omitempty"`
	AttemptedAt   time.Time  `grove:"attempted_at"    bson:"attempted_at"`
}

// RecordAttempt stores one attempt.
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
	if _, err := s.mdb.NewInsert(m).Exec(ctx); err != nil {
		return fmt.Errorf("relay/mongo: record attempt: %w", err)
	}
	return nil
}

// ListAttempts returns a delivery's attempts by attempt number.
func (s *Store) ListAttempts(ctx context.Context, delID id.ID) ([]*delivery.Attempt, error) {
	var models []attemptModel
	if err := s.mdb.NewFind(&models).
		Filter(bson.M{"delivery_id": delID.String()}).
		Sort(bson.D{{Key: "attempt_num", Value: 1}}).
		Scan(ctx); err != nil {
		return nil, fmt.Errorf("relay/mongo: list attempts: %w", err)
	}
	out := make([]*delivery.Attempt, 0, len(models))
	for i := range models {
		m := &models[i]
		attID, err := id.ParseAttemptID(m.ID)
		if err != nil {
			return nil, fmt.Errorf("parse attempt ID %q: %w", m.ID, err)
		}
		dID, err := id.ParseDeliveryID(m.DeliveryID)
		if err != nil {
			return nil, fmt.Errorf("parse delivery ID %q: %w", m.DeliveryID, err)
		}
		out = append(out, &delivery.Attempt{
			ID:            attID,
			DeliveryID:    dID,
			AttemptNum:    m.AttemptNum,
			StatusCode:    m.StatusCode,
			Error:         m.Error,
			Response:      m.Response,
			LatencyMs:     m.LatencyMs,
			Outcome:       delivery.Outcome(m.Outcome),
			NextAttemptAt: utcPtr(m.NextAttemptAt),
			AttemptedAt:   m.AttemptedAt.UTC(),
		})
	}
	return out, nil
}

// PurgeAttempts deletes attempts made before the cutoff.
func (s *Store) PurgeAttempts(ctx context.Context, before time.Time) (int64, error) {
	res, err := s.mdb.NewDelete((*attemptModel)(nil)).
		Many().
		Filter(bson.M{"attempted_at": bson.M{"$lt": before}}).
		Exec(ctx)
	if err != nil {
		return 0, fmt.Errorf("relay/mongo: purge attempts: %w", err)
	}
	return res.DeletedCount(), nil
}

func utcPtr(t *time.Time) *time.Time {
	if t == nil {
		return nil
	}
	u := t.UTC()
	return &u
}

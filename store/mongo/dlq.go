package mongo

import (
	"context"
	"fmt"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"

	relay "github.com/xraph/relay"
	"github.com/xraph/relay/delivery"
	"github.com/xraph/relay/dlq"
	"github.com/xraph/relay/id"
	"github.com/xraph/relay/internal/entity"
)

// Push moves a permanently failed delivery into the DLQ.
func (s *Store) Push(ctx context.Context, entry *dlq.Entry) error {
	m := toDLQEntryModel(entry)

	_, err := s.mdb.NewInsert(m).Exec(ctx)
	if err != nil {
		return fmt.Errorf("relay/mongo: push dlq: %w", err)
	}

	return nil
}

// ListDLQ returns DLQ entries, optionally filtered.
func (s *Store) ListDLQ(ctx context.Context, opts dlq.ListOpts) ([]*dlq.Entry, error) {
	var models []dlqEntryModel

	filter := bson.M{}
	if opts.TenantID != "" {
		filter["tenant_id"] = opts.TenantID
	}

	if opts.EndpointID != nil {
		filter["endpoint_id"] = opts.EndpointID.String()
	}

	if opts.From != nil || opts.To != nil {
		dateFilter := bson.M{}
		if opts.From != nil {
			dateFilter["$gte"] = *opts.From
		}

		if opts.To != nil {
			dateFilter["$lte"] = *opts.To
		}

		filter["failed_at"] = dateFilter
	}

	q := s.mdb.NewFind(&models).
		Filter(filter).
		Sort(bson.D{{Key: "failed_at", Value: -1}})

	if opts.Limit > 0 {
		q = q.Limit(int64(opts.Limit))
	}

	if opts.Offset > 0 {
		q = q.Skip(int64(opts.Offset))
	}

	if err := q.Scan(ctx); err != nil {
		return nil, fmt.Errorf("relay/mongo: list dlq: %w", err)
	}

	result := make([]*dlq.Entry, 0, len(models))

	for i := range models {
		entry, err := fromDLQEntryModel(&models[i])
		if err != nil {
			return nil, err
		}

		result = append(result, entry)
	}

	return result, nil
}

// GetDLQ returns a DLQ entry by ID.
func (s *Store) GetDLQ(ctx context.Context, dlqID id.ID) (*dlq.Entry, error) {
	var m dlqEntryModel

	err := s.mdb.NewFind(&m).
		Filter(bson.M{"_id": dlqID.String()}).
		Scan(ctx)
	if err != nil {
		if isNoDocuments(err) {
			return nil, relay.ErrDLQNotFound
		}

		return nil, fmt.Errorf("relay/mongo: get dlq: %w", err)
	}

	return fromDLQEntryModel(&m)
}

// Replay marks a DLQ entry for redelivery (re-enqueues the delivery).
func (s *Store) Replay(ctx context.Context, dlqID id.ID) error {
	entry, err := s.GetDLQ(ctx, dlqID)
	if err != nil {
		return err
	}

	t := now()

	d := &delivery.Delivery{
		Entity:        entity.New(),
		ID:            id.NewDeliveryID(),
		EventID:       entry.EventID,
		EndpointID:    entry.EndpointID,
		State:         delivery.StatePending,
		MaxAttempts:   entry.AttemptCount,
		NextAttemptAt: t,
	}

	if enqErr := s.Enqueue(ctx, d); enqErr != nil {
		return fmt.Errorf("relay/mongo: replay enqueue: %w", enqErr)
	}

	_, err = s.mdb.NewDelete((*dlqEntryModel)(nil)).
		Filter(bson.M{"_id": dlqID.String()}).
		Exec(ctx)
	if err != nil {
		return fmt.Errorf("relay/mongo: replay delete dlq: %w", err)
	}

	return nil
}

// ReplayBulk replays all DLQ entries in a time window.
func (s *Store) ReplayBulk(ctx context.Context, from, to time.Time) (int64, error) {
	var models []dlqEntryModel

	if err := s.mdb.NewFind(&models).
		Filter(bson.M{
			"failed_at": bson.M{
				"$gte": from,
				"$lte": to,
			},
		}).
		Scan(ctx); err != nil {
		return 0, fmt.Errorf("relay/mongo: replay bulk find: %w", err)
	}

	var count int64
	t := now()

	for i := range models {
		entry, err := fromDLQEntryModel(&models[i])
		if err != nil {
			return count, err
		}

		d := &delivery.Delivery{
			Entity:        entity.New(),
			ID:            id.NewDeliveryID(),
			EventID:       entry.EventID,
			EndpointID:    entry.EndpointID,
			State:         delivery.StatePending,
			MaxAttempts:   entry.AttemptCount,
			NextAttemptAt: t,
		}

		if err := s.Enqueue(ctx, d); err != nil {
			return count, fmt.Errorf("relay/mongo: replay bulk enqueue: %w", err)
		}

		if _, err := s.mdb.NewDelete((*dlqEntryModel)(nil)).
			Filter(bson.M{"_id": models[i].ID}).
			Exec(ctx); err != nil {
			return count, fmt.Errorf("relay/mongo: replay bulk delete: %w", err)
		}

		count++
	}

	return count, nil
}

// Purge deletes DLQ entries older than a threshold.
func (s *Store) Purge(ctx context.Context, before time.Time) (int64, error) {
	res, err := s.mdb.NewDelete((*dlqEntryModel)(nil)).
		Many().
		Filter(bson.M{"failed_at": bson.M{"$lt": before}}).
		Exec(ctx)
	if err != nil {
		return 0, fmt.Errorf("relay/mongo: purge: %w", err)
	}

	return res.DeletedCount(), nil
}

// CountDLQ returns the total number of DLQ entries.
func (s *Store) CountDLQ(ctx context.Context) (int64, error) {
	count, err := s.mdb.NewFind((*dlqEntryModel)(nil)).
		Count(ctx)
	if err != nil {
		return 0, fmt.Errorf("relay/mongo: count dlq: %w", err)
	}

	return count, nil
}

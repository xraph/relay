package mongo

import (
	"context"
	"errors"
	"fmt"

	"go.mongodb.org/mongo-driver/v2/bson"
	mongod "go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"

	relay "github.com/xraph/relay"
	"github.com/xraph/relay/delivery"
	"github.com/xraph/relay/id"
)

// Enqueue creates a pending delivery.
func (s *Store) Enqueue(ctx context.Context, d *delivery.Delivery) error {
	m := toDeliveryModel(d)

	_, err := s.mdb.NewInsert(m).Exec(ctx)
	if err != nil {
		return fmt.Errorf("relay/mongo: enqueue: %w", err)
	}

	return nil
}

// EnqueueBatch creates multiple deliveries atomically (fan-out).
func (s *Store) EnqueueBatch(ctx context.Context, ds []*delivery.Delivery) error {
	if len(ds) == 0 {
		return nil
	}

	models := make([]deliveryModel, len(ds))
	for i, d := range ds {
		models[i] = *toDeliveryModel(d)
	}

	_, err := s.mdb.NewInsert(&models).Exec(ctx)
	if err != nil {
		return fmt.Errorf("relay/mongo: enqueue batch: %w", err)
	}

	return nil
}

// Dequeue fetches pending deliveries ready for attempt (concurrent-safe).
// Uses FindOneAndUpdate for atomic claim to prevent double-delivery.
func (s *Store) Dequeue(ctx context.Context, limit int) ([]*delivery.Delivery, error) {
	result := make([]*delivery.Delivery, 0, limit)
	t := now()
	col := s.mdb.Collection(colDeliveries)

	for range limit {
		filter := bson.M{
			"state":           string(delivery.StatePending),
			"next_attempt_at": bson.M{"$lte": t},
		}

		update := bson.M{
			"$set": bson.M{
				"state":      "delivering",
				"updated_at": t,
			},
		}

		opts := options.FindOneAndUpdate().
			SetReturnDocument(options.After).
			SetSort(bson.D{{Key: "next_attempt_at", Value: 1}})

		var m deliveryModel

		err := col.FindOneAndUpdate(ctx, filter, update, opts).Decode(&m)
		if err != nil {
			if errors.Is(err, mongod.ErrNoDocuments) {
				break
			}

			return nil, fmt.Errorf("relay/mongo: dequeue: %w", err)
		}

		d, err := fromDeliveryModel(&m)
		if err != nil {
			return nil, err
		}

		result = append(result, d)
	}

	return result, nil
}

// UpdateDelivery modifies a delivery.
func (s *Store) UpdateDelivery(ctx context.Context, d *delivery.Delivery) error {
	m := toDeliveryModel(d)
	m.UpdatedAt = now()

	res, err := s.mdb.NewUpdate(m).
		Filter(bson.M{"_id": m.ID}).
		Exec(ctx)
	if err != nil {
		return fmt.Errorf("relay/mongo: update delivery: %w", err)
	}

	if res.MatchedCount() == 0 {
		return relay.ErrDeliveryNotFound
	}

	return nil
}

// GetDelivery returns a delivery by ID.
func (s *Store) GetDelivery(ctx context.Context, delID id.ID) (*delivery.Delivery, error) {
	var m deliveryModel

	err := s.mdb.NewFind(&m).
		Filter(bson.M{"_id": delID.String()}).
		Scan(ctx)
	if err != nil {
		if isNoDocuments(err) {
			return nil, relay.ErrDeliveryNotFound
		}

		return nil, fmt.Errorf("relay/mongo: get delivery: %w", err)
	}

	return fromDeliveryModel(&m)
}

// ListByEndpoint returns delivery history for an endpoint.
func (s *Store) ListByEndpoint(ctx context.Context, epID id.ID, opts delivery.ListOpts) ([]*delivery.Delivery, error) {
	var models []deliveryModel

	filter := bson.M{"endpoint_id": epID.String()}
	if opts.State != nil {
		filter["state"] = string(*opts.State)
	}

	q := s.mdb.NewFind(&models).
		Filter(filter).
		Sort(bson.D{{Key: "created_at", Value: -1}})

	if opts.Limit > 0 {
		q = q.Limit(int64(opts.Limit))
	}

	if opts.Offset > 0 {
		q = q.Skip(int64(opts.Offset))
	}

	if err := q.Scan(ctx); err != nil {
		return nil, fmt.Errorf("relay/mongo: list by endpoint: %w", err)
	}

	result := make([]*delivery.Delivery, 0, len(models))

	for i := range models {
		d, err := fromDeliveryModel(&models[i])
		if err != nil {
			return nil, err
		}

		result = append(result, d)
	}

	return result, nil
}

// ListByEvent returns all deliveries for a specific event.
func (s *Store) ListByEvent(ctx context.Context, evtID id.ID) ([]*delivery.Delivery, error) {
	var models []deliveryModel

	if err := s.mdb.NewFind(&models).
		Filter(bson.M{"event_id": evtID.String()}).
		Sort(bson.D{{Key: "created_at", Value: -1}}).
		Scan(ctx); err != nil {
		return nil, fmt.Errorf("relay/mongo: list by event: %w", err)
	}

	result := make([]*delivery.Delivery, 0, len(models))

	for i := range models {
		d, err := fromDeliveryModel(&models[i])
		if err != nil {
			return nil, err
		}

		result = append(result, d)
	}

	return result, nil
}

// CountPending returns the number of deliveries awaiting attempt.
func (s *Store) CountPending(ctx context.Context) (int64, error) {
	count, err := s.mdb.NewFind((*deliveryModel)(nil)).
		Filter(bson.M{"state": string(delivery.StatePending)}).
		Count(ctx)
	if err != nil {
		return 0, fmt.Errorf("relay/mongo: count pending: %w", err)
	}

	return count, nil
}

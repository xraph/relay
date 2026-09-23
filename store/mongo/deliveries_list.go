package mongo

import (
	"context"
	"fmt"

	"go.mongodb.org/mongo-driver/v2/bson"

	"github.com/xraph/relay/delivery"
)

// ListDeliveries returns one page of the delivery log, newest first.
func (s *Store) ListDeliveries(ctx context.Context, q delivery.Query) (*delivery.Page, error) {
	limit, pos, err := q.Prepare()
	if err != nil {
		return nil, err
	}

	filter := bson.M{}
	if q.State != nil {
		filter["state"] = string(*q.State)
	}
	if q.EndpointID != nil {
		filter["endpoint_id"] = q.EndpointID.String()
	}
	if q.EventID != nil {
		filter["event_id"] = q.EventID.String()
	}
	if q.EventType != "" {
		filter["event_type"] = q.EventType
	}
	if q.TenantID != "" {
		filter["tenant_id"] = q.TenantID
	}
	if lo, hi, ok := q.StatusClass.Range(); ok {
		filter["last_status_code"] = bson.M{"$gte": lo, "$lte": hi}
	}
	created := bson.M{}
	if q.From != nil {
		created["$gte"] = q.From.UTC()
	}
	if q.To != nil {
		created["$lte"] = q.To.UTC()
	}
	if len(created) > 0 {
		filter["created_at"] = created
	}
	if pos != nil {
		// The id breaks ties between rows created in the same instant.
		at := pos.CreatedAt.UTC()
		filter["$or"] = bson.A{
			bson.M{"created_at": bson.M{"$lt": at}},
			bson.M{"created_at": at, "_id": bson.M{"$lt": pos.ID}},
		}
	}

	var models []deliveryModel
	if err := s.mdb.NewFind(&models).
		Filter(filter).
		Sort(bson.D{{Key: "created_at", Value: -1}, {Key: "_id", Value: -1}}).
		Limit(int64(limit + 1)).
		Scan(ctx); err != nil {
		return nil, fmt.Errorf("relay/mongo: list deliveries: %w", err)
	}

	page := &delivery.Page{Deliveries: make([]*delivery.Delivery, 0, min(len(models), limit)), Complete: true}
	for i := range models {
		if i == limit {
			page.NextCursor = delivery.CursorFor(page.Deliveries[limit-1])
			break
		}
		d, err := fromDeliveryModel(&models[i])
		if err != nil {
			return nil, err
		}
		page.Deliveries = append(page.Deliveries, d)
	}
	return page, nil
}

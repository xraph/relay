package mongo

import (
	"context"
	"fmt"

	"go.mongodb.org/mongo-driver/v2/bson"

	"github.com/xraph/relay/dlq"
	"github.com/xraph/relay/event"
)

// ListEventsPage returns one page of the event log, newest first.
func (s *Store) ListEventsPage(ctx context.Context, q event.Query) (*event.Page, error) {
	limit, pos, err := q.Prepare()
	if err != nil {
		return nil, err
	}
	filter := bson.M{}
	if q.Type != "" {
		filter["type"] = q.Type
	}
	if q.TenantID != "" {
		filter["tenant_id"] = q.TenantID
	}
	if r := timeRange(q.From, q.To); len(r) > 0 {
		filter["created_at"] = r
	}
	if pos != nil {
		at := pos.CreatedAt.UTC()
		filter["$or"] = bson.A{
			bson.M{"created_at": bson.M{"$lt": at}},
			bson.M{"created_at": at, "_id": bson.M{"$lt": pos.ID}},
		}
	}
	var models []eventModel
	if err := s.mdb.NewFind(&models).
		Filter(filter).
		Sort(bson.D{{Key: "created_at", Value: -1}, {Key: "_id", Value: -1}}).
		Limit(int64(limit + 1)).
		Scan(ctx); err != nil {
		return nil, fmt.Errorf("relay/mongo: list events page: %w", err)
	}
	page := &event.Page{Events: make([]*event.Event, 0, min(len(models), limit)), Complete: true}
	for i := range models {
		if i == limit {
			page.NextCursor = event.CursorFor(page.Events[limit-1])
			break
		}
		e, err := fromEventModel(&models[i])
		if err != nil {
			return nil, err
		}
		page.Events = append(page.Events, e)
	}
	return page, nil
}

// ListDLQPage returns one page of the DLQ, most recent failure first.
func (s *Store) ListDLQPage(ctx context.Context, q dlq.Query) (*dlq.Page, error) {
	limit, pos, err := q.Prepare()
	if err != nil {
		return nil, err
	}
	filter := bson.M{}
	if q.TenantID != "" {
		filter["tenant_id"] = q.TenantID
	}
	if q.EndpointID != nil {
		filter["endpoint_id"] = q.EndpointID.String()
	}
	if r := timeRange(q.From, q.To); len(r) > 0 {
		filter["failed_at"] = r
	}
	if q.Replayed != nil {
		if *q.Replayed {
			filter["replayed_at"] = bson.M{"$ne": nil}
		} else {
			filter["replayed_at"] = nil // null or absent
		}
	}
	if pos != nil {
		at := pos.FailedAt.UTC()
		filter["$or"] = bson.A{
			bson.M{"failed_at": bson.M{"$lt": at}},
			bson.M{"failed_at": at, "_id": bson.M{"$lt": pos.ID}},
		}
	}
	var models []dlqEntryModel
	if err := s.mdb.NewFind(&models).
		Filter(filter).
		Sort(bson.D{{Key: "failed_at", Value: -1}, {Key: "_id", Value: -1}}).
		Limit(int64(limit + 1)).
		Scan(ctx); err != nil {
		return nil, fmt.Errorf("relay/mongo: list dlq page: %w", err)
	}
	page := &dlq.Page{Entries: make([]*dlq.Entry, 0, min(len(models), limit)), Complete: true}
	for i := range models {
		if i == limit {
			page.NextCursor = dlq.CursorFor(page.Entries[limit-1])
			break
		}
		e, err := fromDLQEntryModel(&models[i])
		if err != nil {
			return nil, err
		}
		page.Entries = append(page.Entries, e)
	}
	return page, nil
}

package mongo

import (
	"context"
	"fmt"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"

	relay "github.com/xraph/relay"
	"github.com/xraph/relay/dlq"
	"github.com/xraph/relay/id"
)

// Push moves a permanently failed delivery into the DLQ.
func (s *Store) Push(ctx context.Context, entry *dlq.Entry) error {
	m, err := toDLQEntryModel(entry)
	if err != nil {
		return fmt.Errorf("relay/mongo: push dlq: %w", err)
	}

	_, err = s.mdb.NewInsert(m).Exec(ctx)
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

// MarkReplayed claims a DLQ entry for replay. It sets replayed_at only if it
// is not already set, and does so atomically, so of any number of concurrent
// callers exactly one succeeds. The service calls this before it sends the
// webhook: a claim that overwrote instead would let two replays both win and
// both send. Returns relay.ErrAlreadyReplayed when the entry is already
// claimed and relay.ErrDLQNotFound when it does not exist.
//
// A single-document update is atomic in mongo whether or not it runs as a
// replica set, so filtering on replayed_at being null makes this a true claim.
// {replayed_at: nil} matches both an explicit null and an absent field.
func (s *Store) MarkReplayed(ctx context.Context, dlqID id.ID, at time.Time) error {
	res, err := s.mdb.NewUpdate((*dlqEntryModel)(nil)).
		Filter(bson.M{"_id": dlqID.String(), "replayed_at": nil}).
		Set("replayed_at", at.UTC()).
		Set("updated_at", now()).
		Exec(ctx)
	if err != nil {
		return fmt.Errorf("relay/mongo: mark replayed: %w", err)
	}
	if res.MatchedCount() == 1 {
		return nil
	}
	// No match: missing, or already claimed. Look to tell which.
	if _, getErr := s.GetDLQ(ctx, dlqID); getErr != nil {
		return getErr
	}
	return relay.ErrAlreadyReplayed
}

// ReleaseReplay undoes a claim made by MarkReplayed, clearing replayed_at so
// the entry can be replayed again. The service calls it when the send it
// claimed the entry for fails, so a failure never leaves an entry marked as
// sent when nothing went out. Only the claim holder calls it. Returns
// relay.ErrDLQNotFound when the entry does not exist.
func (s *Store) ReleaseReplay(ctx context.Context, dlqID id.ID) error {
	res, err := s.mdb.NewUpdate((*dlqEntryModel)(nil)).
		Filter(bson.M{"_id": dlqID.String()}).
		Set("replayed_at", nil).
		Set("updated_at", now()).
		Exec(ctx)
	if err != nil {
		return fmt.Errorf("relay/mongo: release replay: %w", err)
	}
	if res.MatchedCount() == 0 {
		return relay.ErrDLQNotFound
	}
	return nil
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

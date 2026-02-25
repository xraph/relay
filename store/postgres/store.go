package postgres

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/xraph/grove"
	"github.com/xraph/grove/drivers/pgdriver"
	"github.com/xraph/grove/migrate"

	relay "github.com/xraph/relay"
	"github.com/xraph/relay/catalog"
	"github.com/xraph/relay/delivery"
	"github.com/xraph/relay/dlq"
	"github.com/xraph/relay/endpoint"
	"github.com/xraph/relay/event"
	"github.com/xraph/relay/id"
	relaystore "github.com/xraph/relay/store"
)

// compile-time interface check
var _ relaystore.Store = (*Store)(nil)

// Store implements store.Store using PostgreSQL via Grove ORM.
type Store struct {
	db *grove.DB
	pg *pgdriver.PgDB
}

// New creates a new PostgreSQL store backed by Grove ORM.
func New(db *grove.DB) *Store {
	return &Store{
		db: db,
		pg: pgdriver.Unwrap(db),
	}
}

// DB returns the underlying grove database for direct access.
func (s *Store) DB() *grove.DB { return s.db }

// Migrate creates the required tables and indexes using the grove orchestrator.
func (s *Store) Migrate(ctx context.Context) error {
	executor, err := migrate.NewExecutorFor(s.pg)
	if err != nil {
		return fmt.Errorf("relay/postgres: create migration executor: %w", err)
	}
	orch := migrate.NewOrchestrator(executor, Migrations)
	if _, err := orch.Migrate(ctx); err != nil {
		return fmt.Errorf("relay/postgres: migration failed: %w", err)
	}
	return nil
}

// Ping checks database connectivity.
func (s *Store) Ping(ctx context.Context) error {
	return s.db.Ping(ctx)
}

// Close closes the database connection.
func (s *Store) Close() error {
	return s.db.Close()
}

// ==================== Catalog Store ====================

func (s *Store) RegisterType(ctx context.Context, et *catalog.EventType) error {
	m := toEventTypeModel(et)
	_, err := s.pg.NewInsert(m).
		OnConflict("(name) DO UPDATE").
		Set("description = EXCLUDED.description").
		Set("group_name = EXCLUDED.group_name").
		Set("schema = EXCLUDED.schema").
		Set("schema_version = EXCLUDED.schema_version").
		Set("version = EXCLUDED.version").
		Set("example = EXCLUDED.example").
		Set("scope_app_id = EXCLUDED.scope_app_id").
		Set("metadata = EXCLUDED.metadata").
		Set("is_deprecated = false").
		Set("deprecated_at = NULL").
		Set("updated_at = EXCLUDED.updated_at").
		Exec(ctx)
	return err
}

func (s *Store) GetType(ctx context.Context, name string) (*catalog.EventType, error) {
	m := new(eventTypeModel)
	err := s.pg.NewSelect(m).
		Where("name = $1", name).
		Scan(ctx)
	if err != nil {
		if isNoRows(err) {
			return nil, relay.ErrEventTypeNotFound
		}
		return nil, err
	}
	return fromEventTypeModel(m)
}

func (s *Store) GetTypeByID(ctx context.Context, etID id.ID) (*catalog.EventType, error) {
	m := new(eventTypeModel)
	err := s.pg.NewSelect(m).
		Where("id = $1", etID.String()).
		Scan(ctx)
	if err != nil {
		if isNoRows(err) {
			return nil, relay.ErrEventTypeNotFound
		}
		return nil, err
	}
	return fromEventTypeModel(m)
}

func (s *Store) ListTypes(ctx context.Context, opts catalog.ListOpts) ([]*catalog.EventType, error) {
	var models []eventTypeModel
	q := s.pg.NewSelect(&models)

	argIdx := 0
	if opts.Group != "" {
		argIdx++
		q = q.Where(fmt.Sprintf("group_name = $%d", argIdx), opts.Group)
	}
	if !opts.IncludeDeprecated {
		q = q.Where("is_deprecated = false")
	}
	if opts.Limit > 0 {
		q = q.Limit(opts.Limit)
	}
	if opts.Offset > 0 {
		q = q.Offset(opts.Offset)
	}
	q = q.OrderExpr("created_at ASC")

	if err := q.Scan(ctx); err != nil {
		return nil, err
	}

	result := make([]*catalog.EventType, len(models))
	for i := range models {
		et, err := fromEventTypeModel(&models[i])
		if err != nil {
			return nil, err
		}
		result[i] = et
	}
	return result, nil
}

func (s *Store) DeleteType(ctx context.Context, name string) error {
	now := time.Now().UTC()
	res, err := s.pg.NewUpdate((*eventTypeModel)(nil)).
		Set("is_deprecated = true").
		Set("deprecated_at = $1", now).
		Set("updated_at = $2", now).
		Where("name = $3", name).
		Where("is_deprecated = false").
		Exec(ctx)
	if err != nil {
		return err
	}
	rows, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if rows == 0 {
		return relay.ErrEventTypeNotFound
	}
	return nil
}

func (s *Store) MatchTypes(ctx context.Context, pattern string) ([]*catalog.EventType, error) {
	var models []eventTypeModel
	if err := s.pg.NewSelect(&models).
		Where("is_deprecated = false").
		Scan(ctx); err != nil {
		return nil, err
	}

	var result []*catalog.EventType
	for i := range models {
		et, err := fromEventTypeModel(&models[i])
		if err != nil {
			return nil, err
		}
		if catalog.Match(pattern, et.Definition.Name) {
			result = append(result, et)
		}
	}
	return result, nil
}

// ==================== Endpoint Store ====================

func (s *Store) CreateEndpoint(ctx context.Context, ep *endpoint.Endpoint) error {
	m := toEndpointModel(ep)
	_, err := s.pg.NewInsert(m).Exec(ctx)
	return err
}

func (s *Store) GetEndpoint(ctx context.Context, epID id.ID) (*endpoint.Endpoint, error) {
	m := new(endpointModel)
	err := s.pg.NewSelect(m).
		Where("id = $1", epID.String()).
		Scan(ctx)
	if err != nil {
		if isNoRows(err) {
			return nil, relay.ErrEndpointNotFound
		}
		return nil, err
	}
	return fromEndpointModel(m)
}

func (s *Store) UpdateEndpoint(ctx context.Context, ep *endpoint.Endpoint) error {
	m := toEndpointModel(ep)
	m.UpdatedAt = time.Now().UTC()
	res, err := s.pg.NewUpdate(m).
		WherePK().
		Exec(ctx)
	if err != nil {
		return err
	}
	rows, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if rows == 0 {
		return relay.ErrEndpointNotFound
	}
	return nil
}

func (s *Store) DeleteEndpoint(ctx context.Context, epID id.ID) error {
	res, err := s.pg.NewDelete((*endpointModel)(nil)).
		Where("id = $1", epID.String()).
		Exec(ctx)
	if err != nil {
		return err
	}
	rows, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if rows == 0 {
		return relay.ErrEndpointNotFound
	}
	return nil
}

func (s *Store) ListEndpoints(ctx context.Context, tenantID string, opts endpoint.ListOpts) ([]*endpoint.Endpoint, error) {
	var models []endpointModel
	q := s.pg.NewSelect(&models).Where("tenant_id = $1", tenantID)
	if opts.Limit > 0 {
		q = q.Limit(opts.Limit)
	}
	if opts.Offset > 0 {
		q = q.Offset(opts.Offset)
	}
	q = q.OrderExpr("created_at ASC")

	if err := q.Scan(ctx); err != nil {
		return nil, err
	}

	result := make([]*endpoint.Endpoint, len(models))
	for i := range models {
		ep, err := fromEndpointModel(&models[i])
		if err != nil {
			return nil, err
		}
		result[i] = ep
	}
	return result, nil
}

func (s *Store) Resolve(ctx context.Context, tenantID, eventType string) ([]*endpoint.Endpoint, error) {
	var models []endpointModel
	if err := s.pg.NewSelect(&models).
		Where("tenant_id = $1", tenantID).
		Where("enabled = true").
		Scan(ctx); err != nil {
		return nil, err
	}

	var result []*endpoint.Endpoint
	for i := range models {
		for _, pattern := range models[i].EventTypes {
			if catalog.Match(pattern, eventType) {
				ep, err := fromEndpointModel(&models[i])
				if err != nil {
					return nil, err
				}
				result = append(result, ep)
				break
			}
		}
	}
	return result, nil
}

func (s *Store) SetEnabled(ctx context.Context, epID id.ID, enabled bool) error {
	now := time.Now().UTC()
	res, err := s.pg.NewUpdate((*endpointModel)(nil)).
		Set("enabled = $1", enabled).
		Set("updated_at = $2", now).
		Where("id = $3", epID.String()).
		Exec(ctx)
	if err != nil {
		return err
	}
	rows, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if rows == 0 {
		return relay.ErrEndpointNotFound
	}
	return nil
}

// ==================== Event Store ====================

func (s *Store) CreateEvent(ctx context.Context, evt *event.Event) error {
	m := toEventModel(evt)

	if evt.IdempotencyKey != "" {
		// Use ON CONFLICT DO NOTHING for idempotency.
		res, err := s.pg.NewInsert(m).
			OnConflict("(idempotency_key) WHERE idempotency_key != '' DO NOTHING").
			Exec(ctx)
		if err != nil {
			return err
		}
		rows, err := res.RowsAffected()
		if err != nil {
			return err
		}
		if rows == 0 {
			return relay.ErrDuplicateIdempotencyKey
		}
		return nil
	}

	_, err := s.pg.NewInsert(m).Exec(ctx)
	return err
}

func (s *Store) GetEvent(ctx context.Context, evtID id.ID) (*event.Event, error) {
	m := new(eventModel)
	err := s.pg.NewSelect(m).
		Where("id = $1", evtID.String()).
		Scan(ctx)
	if err != nil {
		if isNoRows(err) {
			return nil, relay.ErrEventNotFound
		}
		return nil, err
	}
	return fromEventModel(m)
}

func (s *Store) ListEvents(ctx context.Context, opts event.ListOpts) ([]*event.Event, error) {
	var models []eventModel
	q := s.pg.NewSelect(&models)

	argIdx := 0
	if opts.Type != "" {
		argIdx++
		q = q.Where(fmt.Sprintf("type = $%d", argIdx), opts.Type)
	}
	if opts.From != nil {
		argIdx++
		q = q.Where(fmt.Sprintf("created_at >= $%d", argIdx), *opts.From)
	}
	if opts.To != nil {
		argIdx++
		q = q.Where(fmt.Sprintf("created_at <= $%d", argIdx), *opts.To)
	}
	if opts.Limit > 0 {
		q = q.Limit(opts.Limit)
	}
	if opts.Offset > 0 {
		q = q.Offset(opts.Offset)
	}
	q = q.OrderExpr("created_at DESC")

	if err := q.Scan(ctx); err != nil {
		return nil, err
	}

	result := make([]*event.Event, len(models))
	for i := range models {
		evt, err := fromEventModel(&models[i])
		if err != nil {
			return nil, err
		}
		result[i] = evt
	}

	return result, nil
}

func (s *Store) ListEventsByTenant(ctx context.Context, tenantID string, opts event.ListOpts) ([]*event.Event, error) {
	var models []eventModel
	q := s.pg.NewSelect(&models).Where("tenant_id = $1", tenantID)

	argIdx := 1
	if opts.Type != "" {
		argIdx++
		q = q.Where(fmt.Sprintf("type = $%d", argIdx), opts.Type)
	}
	if opts.Limit > 0 {
		q = q.Limit(opts.Limit)
	}
	if opts.Offset > 0 {
		q = q.Offset(opts.Offset)
	}
	q = q.OrderExpr("created_at DESC")

	if err := q.Scan(ctx); err != nil {
		return nil, err
	}

	result := make([]*event.Event, len(models))
	for i := range models {
		evt, err := fromEventModel(&models[i])
		if err != nil {
			return nil, err
		}
		result[i] = evt
	}

	return result, nil
}

// ==================== Delivery Store ====================

func (s *Store) Enqueue(ctx context.Context, d *delivery.Delivery) error {
	m := toDeliveryModel(d)
	_, err := s.pg.NewInsert(m).Exec(ctx)
	return err
}

func (s *Store) EnqueueBatch(ctx context.Context, ds []*delivery.Delivery) error {
	if len(ds) == 0 {
		return nil
	}
	models := make([]deliveryModel, len(ds))
	for i, d := range ds {
		models[i] = *toDeliveryModel(d)
	}
	_, err := s.pg.NewInsert(&models).Exec(ctx)
	return err
}

func (s *Store) Dequeue(ctx context.Context, limit int) ([]*delivery.Delivery, error) {
	// Use raw SQL for the FOR UPDATE SKIP LOCKED dequeue pattern.
	var models []deliveryModel
	err := s.pg.NewRaw(`
		UPDATE relay_deliveries
		SET state = 'delivering', updated_at = NOW()
		WHERE id IN (
			SELECT id FROM relay_deliveries
			WHERE state = 'pending' AND next_attempt_at <= NOW()
			ORDER BY next_attempt_at ASC
			LIMIT $1
			FOR UPDATE SKIP LOCKED
		)
		RETURNING *
	`, limit).Scan(ctx, &models)
	if err != nil {
		return nil, err
	}

	result := make([]*delivery.Delivery, len(models))
	for i := range models {
		d, err := fromDeliveryModel(&models[i])
		if err != nil {
			return nil, err
		}
		result[i] = d
	}
	return result, nil
}

func (s *Store) UpdateDelivery(ctx context.Context, d *delivery.Delivery) error {
	m := toDeliveryModel(d)
	m.UpdatedAt = time.Now().UTC()
	_, err := s.pg.NewUpdate(m).WherePK().Exec(ctx)
	return err
}

func (s *Store) GetDelivery(ctx context.Context, delID id.ID) (*delivery.Delivery, error) {
	m := new(deliveryModel)
	err := s.pg.NewSelect(m).
		Where("id = $1", delID.String()).
		Scan(ctx)
	if err != nil {
		if isNoRows(err) {
			return nil, relay.ErrDeliveryNotFound
		}
		return nil, err
	}

	return fromDeliveryModel(m)
}

func (s *Store) ListByEndpoint(ctx context.Context, epID id.ID, opts delivery.ListOpts) ([]*delivery.Delivery, error) {
	var models []deliveryModel
	q := s.pg.NewSelect(&models).Where("endpoint_id = $1", epID.String())

	if opts.State != nil {
		q = q.Where("state = $2", string(*opts.State))
	}
	if opts.Limit > 0 {
		q = q.Limit(opts.Limit)
	}
	if opts.Offset > 0 {
		q = q.Offset(opts.Offset)
	}
	q = q.OrderExpr("created_at DESC")

	if err := q.Scan(ctx); err != nil {
		return nil, err
	}

	result := make([]*delivery.Delivery, len(models))
	for i := range models {
		d, err := fromDeliveryModel(&models[i])
		if err != nil {
			return nil, err
		}
		result[i] = d
	}

	return result, nil
}

func (s *Store) ListByEvent(ctx context.Context, evtID id.ID) ([]*delivery.Delivery, error) {
	var models []deliveryModel
	if err := s.pg.NewSelect(&models).
		Where("event_id = $1", evtID.String()).
		OrderExpr("created_at DESC").
		Scan(ctx); err != nil {
		return nil, err
	}

	result := make([]*delivery.Delivery, len(models))
	for i := range models {
		d, err := fromDeliveryModel(&models[i])
		if err != nil {
			return nil, err
		}
		result[i] = d
	}
	return result, nil
}

func (s *Store) CountPending(ctx context.Context) (int64, error) {
	count, err := s.pg.NewSelect((*deliveryModel)(nil)).
		Where("state = $1", string(delivery.StatePending)).
		Count(ctx)
	return count, err
}

// ==================== DLQ Store ====================

func (s *Store) Push(ctx context.Context, entry *dlq.Entry) error {
	m := toDLQEntryModel(entry)
	_, err := s.pg.NewInsert(m).Exec(ctx)
	return err
}

func (s *Store) ListDLQ(ctx context.Context, opts dlq.ListOpts) ([]*dlq.Entry, error) {
	var models []dlqEntryModel
	q := s.pg.NewSelect(&models)

	argIdx := 0
	if opts.TenantID != "" {
		argIdx++
		q = q.Where(fmt.Sprintf("tenant_id = $%d", argIdx), opts.TenantID)
	}
	if opts.EndpointID != nil {
		argIdx++
		q = q.Where(fmt.Sprintf("endpoint_id = $%d", argIdx), opts.EndpointID.String())
	}
	if opts.From != nil {
		argIdx++
		q = q.Where(fmt.Sprintf("failed_at >= $%d", argIdx), *opts.From)
	}
	if opts.To != nil {
		argIdx++
		q = q.Where(fmt.Sprintf("failed_at <= $%d", argIdx), *opts.To)
	}
	if opts.Limit > 0 {
		q = q.Limit(opts.Limit)
	}
	if opts.Offset > 0 {
		q = q.Offset(opts.Offset)
	}
	q = q.OrderExpr("failed_at DESC")

	if err := q.Scan(ctx); err != nil {
		return nil, err
	}

	result := make([]*dlq.Entry, len(models))
	for i := range models {
		entry, err := fromDLQEntryModel(&models[i])
		if err != nil {
			return nil, err
		}
		result[i] = entry
	}
	return result, nil
}

func (s *Store) GetDLQ(ctx context.Context, dlqID id.ID) (*dlq.Entry, error) {
	m := new(dlqEntryModel)
	err := s.pg.NewSelect(m).
		Where("id = $1", dlqID.String()).
		Scan(ctx)
	if err != nil {
		if isNoRows(err) {
			return nil, relay.ErrDLQNotFound
		}
		return nil, err
	}
	return fromDLQEntryModel(m)
}

func (s *Store) Replay(ctx context.Context, dlqID id.ID) error {
	// Get the DLQ entry.
	entry, err := s.GetDLQ(ctx, dlqID)
	if err != nil {
		return err
	}

	// Re-enqueue a new delivery.
	d := &delivery.Delivery{
		ID:            id.NewDeliveryID(),
		EventID:       entry.EventID,
		EndpointID:    entry.EndpointID,
		State:         delivery.StatePending,
		NextAttemptAt: time.Now().UTC(),
	}
	d.CreatedAt = time.Now().UTC()
	d.UpdatedAt = d.CreatedAt

	if enqueueErr := s.Enqueue(ctx, d); enqueueErr != nil {
		return enqueueErr
	}

	// Remove from DLQ.
	_, err = s.pg.NewDelete((*dlqEntryModel)(nil)).
		Where("id = $1", dlqID.String()).
		Exec(ctx)
	return err
}

func (s *Store) ReplayBulk(ctx context.Context, from, to time.Time) (int64, error) {
	var models []dlqEntryModel
	if err := s.pg.NewSelect(&models).
		Where("failed_at >= $1", from).
		Where("failed_at <= $2", to).
		Scan(ctx); err != nil {
		return 0, err
	}

	var count int64
	for i := range models {
		entry, err := fromDLQEntryModel(&models[i])
		if err != nil {
			return count, err
		}
		d := &delivery.Delivery{
			ID:            id.NewDeliveryID(),
			EventID:       entry.EventID,
			EndpointID:    entry.EndpointID,
			State:         delivery.StatePending,
			NextAttemptAt: time.Now().UTC(),
		}
		d.CreatedAt = time.Now().UTC()
		d.UpdatedAt = d.CreatedAt

		if err := s.Enqueue(ctx, d); err != nil {
			return count, err
		}

		if _, err := s.pg.NewDelete((*dlqEntryModel)(nil)).
			Where("id = $1", models[i].ID).
			Exec(ctx); err != nil {
			return count, err
		}
		count++
	}

	return count, nil
}

func (s *Store) Purge(ctx context.Context, before time.Time) (int64, error) {
	res, err := s.pg.NewDelete((*dlqEntryModel)(nil)).
		Where("failed_at < $1", before).
		Exec(ctx)
	if err != nil {
		return 0, err
	}

	rows, err := res.RowsAffected()
	if err != nil {
		return 0, err
	}

	return rows, nil
}

func (s *Store) CountDLQ(ctx context.Context) (int64, error) {
	count, err := s.pg.NewSelect((*dlqEntryModel)(nil)).
		Count(ctx)
	return count, err
}

// isNoRows checks for the standard sql.ErrNoRows sentinel.
func isNoRows(err error) bool {
	return errors.Is(err, sql.ErrNoRows)
}

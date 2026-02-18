package bunstore

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"github.com/uptrace/bun"

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

// Store implements store.Store using the Bun ORM.
type Store struct {
	db *bun.DB
}

// New creates a new Bun-backed store.
func New(db *bun.DB) *Store {
	return &Store{db: db}
}

// DB returns the underlying Bun database for direct access.
func (s *Store) DB() *bun.DB { return s.db }

// Migrate creates the required tables using Bun's CreateTable.
func (s *Store) Migrate(ctx context.Context) error {
	models := []any{
		(*eventTypeModel)(nil),
		(*endpointModel)(nil),
		(*eventModel)(nil),
		(*deliveryModel)(nil),
		(*dlqEntryModel)(nil),
	}
	for _, model := range models {
		if _, err := s.db.NewCreateTable().
			Model(model).
			IfNotExists().
			Exec(ctx); err != nil {
			return err
		}
	}

	// Create indexes.
	indexes := []string{
		"CREATE INDEX IF NOT EXISTS idx_relay_deliveries_pending ON relay_deliveries (next_attempt_at) WHERE state = 'pending'",
		"CREATE INDEX IF NOT EXISTS idx_relay_deliveries_event ON relay_deliveries (event_id)",
		"CREATE INDEX IF NOT EXISTS idx_relay_deliveries_endpoint ON relay_deliveries (endpoint_id)",
		"CREATE INDEX IF NOT EXISTS idx_relay_events_tenant ON relay_events (tenant_id)",
		"CREATE INDEX IF NOT EXISTS idx_relay_events_type ON relay_events (type)",
		"CREATE INDEX IF NOT EXISTS idx_relay_endpoints_tenant ON relay_endpoints (tenant_id)",
		"CREATE INDEX IF NOT EXISTS idx_relay_dlq_tenant ON relay_dlq (tenant_id)",
		"CREATE UNIQUE INDEX IF NOT EXISTS idx_relay_events_idempotency ON relay_events (idempotency_key) WHERE idempotency_key != ''",
	}
	for _, ddl := range indexes {
		if _, err := s.db.ExecContext(ctx, ddl); err != nil {
			return err
		}
	}

	return nil
}

// Ping checks database connectivity.
func (s *Store) Ping(ctx context.Context) error {
	return s.db.PingContext(ctx)
}

// Close closes the database connection.
func (s *Store) Close() error {
	return s.db.Close()
}

// ==================== Catalog Store ====================

func (s *Store) RegisterType(ctx context.Context, et *catalog.EventType) error {
	m := toEventTypeModel(et)
	_, err := s.db.NewInsert().
		Model(m).
		On("CONFLICT (name) DO UPDATE").
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
	err := s.db.NewSelect().
		Model(m).
		Where("name = ?", name).
		Limit(1).
		Scan(ctx)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, relay.ErrEventTypeNotFound
		}
		return nil, err
	}
	return fromEventTypeModel(m)
}

func (s *Store) GetTypeByID(ctx context.Context, etID id.ID) (*catalog.EventType, error) {
	m := new(eventTypeModel)
	err := s.db.NewSelect().
		Model(m).
		Where("id = ?", etID.String()).
		Limit(1).
		Scan(ctx)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, relay.ErrEventTypeNotFound
		}
		return nil, err
	}
	return fromEventTypeModel(m)
}

func (s *Store) ListTypes(ctx context.Context, opts catalog.ListOpts) ([]*catalog.EventType, error) {
	var models []eventTypeModel
	q := s.db.NewSelect().Model(&models)

	if opts.Group != "" {
		q = q.Where("group_name = ?", opts.Group)
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
	q = q.Order("created_at ASC")

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
	res, err := s.db.NewUpdate().
		Model((*eventTypeModel)(nil)).
		Set("is_deprecated = true").
		Set("deprecated_at = ?", now).
		Set("updated_at = ?", now).
		Where("name = ?", name).
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
	if err := s.db.NewSelect().
		Model(&models).
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
	_, err := s.db.NewInsert().Model(m).Exec(ctx)
	return err
}

func (s *Store) GetEndpoint(ctx context.Context, epID id.ID) (*endpoint.Endpoint, error) {
	m := new(endpointModel)
	err := s.db.NewSelect().
		Model(m).
		Where("id = ?", epID.String()).
		Limit(1).
		Scan(ctx)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, relay.ErrEndpointNotFound
		}
		return nil, err
	}
	return fromEndpointModel(m)
}

func (s *Store) UpdateEndpoint(ctx context.Context, ep *endpoint.Endpoint) error {
	m := toEndpointModel(ep)
	m.UpdatedAt = time.Now().UTC()
	res, err := s.db.NewUpdate().
		Model(m).
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
	res, err := s.db.NewDelete().
		Model((*endpointModel)(nil)).
		Where("id = ?", epID.String()).
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
	q := s.db.NewSelect().Model(&models).Where("tenant_id = ?", tenantID)
	if opts.Limit > 0 {
		q = q.Limit(opts.Limit)
	}
	if opts.Offset > 0 {
		q = q.Offset(opts.Offset)
	}
	q = q.Order("created_at ASC")

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
	if err := s.db.NewSelect().
		Model(&models).
		Where("tenant_id = ?", tenantID).
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
	res, err := s.db.NewUpdate().
		Model((*endpointModel)(nil)).
		Set("enabled = ?", enabled).
		Set("updated_at = ?", now).
		Where("id = ?", epID.String()).
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
		res, err := s.db.NewInsert().
			Model(m).
			On("CONFLICT (idempotency_key) WHERE idempotency_key != '' DO NOTHING").
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

	_, err := s.db.NewInsert().Model(m).Exec(ctx)
	return err
}

func (s *Store) GetEvent(ctx context.Context, evtID id.ID) (*event.Event, error) {
	m := new(eventModel)
	err := s.db.NewSelect().
		Model(m).
		Where("id = ?", evtID.String()).
		Limit(1).
		Scan(ctx)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, relay.ErrEventNotFound
		}
		return nil, err
	}
	return fromEventModel(m)
}

func (s *Store) ListEvents(ctx context.Context, opts event.ListOpts) ([]*event.Event, error) {
	var models []eventModel
	q := s.db.NewSelect().Model(&models)

	if opts.Type != "" {
		q = q.Where("type = ?", opts.Type)
	}
	if opts.From != nil {
		q = q.Where("created_at >= ?", *opts.From)
	}
	if opts.To != nil {
		q = q.Where("created_at <= ?", *opts.To)
	}
	if opts.Limit > 0 {
		q = q.Limit(opts.Limit)
	}
	if opts.Offset > 0 {
		q = q.Offset(opts.Offset)
	}
	q = q.Order("created_at DESC")

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
	q := s.db.NewSelect().Model(&models).Where("tenant_id = ?", tenantID)

	if opts.Type != "" {
		q = q.Where("type = ?", opts.Type)
	}
	if opts.Limit > 0 {
		q = q.Limit(opts.Limit)
	}
	if opts.Offset > 0 {
		q = q.Offset(opts.Offset)
	}
	q = q.Order("created_at DESC")

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
	_, err := s.db.NewInsert().Model(m).Exec(ctx)
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
	_, err := s.db.NewInsert().Model(&models).Exec(ctx)
	return err
}

func (s *Store) Dequeue(ctx context.Context, limit int) ([]*delivery.Delivery, error) {
	var models []deliveryModel
	err := s.db.NewRaw(`
		UPDATE relay_deliveries
		SET state = 'delivering', updated_at = NOW()
		WHERE id IN (
			SELECT id FROM relay_deliveries
			WHERE state = 'pending' AND next_attempt_at <= NOW()
			ORDER BY next_attempt_at ASC
			LIMIT ?
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
	_, err := s.db.NewUpdate().Model(m).WherePK().Exec(ctx)
	return err
}

func (s *Store) GetDelivery(ctx context.Context, delID id.ID) (*delivery.Delivery, error) {
	m := new(deliveryModel)
	err := s.db.NewSelect().
		Model(m).
		Where("id = ?", delID.String()).
		Limit(1).
		Scan(ctx)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, relay.ErrDeliveryNotFound
		}
		return nil, err
	}
	return fromDeliveryModel(m)
}

func (s *Store) ListByEndpoint(ctx context.Context, epID id.ID, opts delivery.ListOpts) ([]*delivery.Delivery, error) {
	var models []deliveryModel
	q := s.db.NewSelect().Model(&models).Where("endpoint_id = ?", epID.String())

	if opts.State != nil {
		q = q.Where("state = ?", string(*opts.State))
	}
	if opts.Limit > 0 {
		q = q.Limit(opts.Limit)
	}
	if opts.Offset > 0 {
		q = q.Offset(opts.Offset)
	}
	q = q.Order("created_at DESC")

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
	if err := s.db.NewSelect().
		Model(&models).
		Where("event_id = ?", evtID.String()).
		Order("created_at DESC").
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
	count, err := s.db.NewSelect().
		Model((*deliveryModel)(nil)).
		Where("state = ?", string(delivery.StatePending)).
		Count(ctx)
	return int64(count), err
}

// ==================== DLQ Store ====================

func (s *Store) Push(ctx context.Context, entry *dlq.Entry) error {
	m := toDLQEntryModel(entry)
	_, err := s.db.NewInsert().Model(m).Exec(ctx)
	return err
}

func (s *Store) ListDLQ(ctx context.Context, opts dlq.ListOpts) ([]*dlq.Entry, error) {
	var models []dlqEntryModel
	q := s.db.NewSelect().Model(&models)

	if opts.TenantID != "" {
		q = q.Where("tenant_id = ?", opts.TenantID)
	}
	if opts.EndpointID != nil {
		q = q.Where("endpoint_id = ?", opts.EndpointID.String())
	}
	if opts.From != nil {
		q = q.Where("failed_at >= ?", *opts.From)
	}
	if opts.To != nil {
		q = q.Where("failed_at <= ?", *opts.To)
	}
	if opts.Limit > 0 {
		q = q.Limit(opts.Limit)
	}
	if opts.Offset > 0 {
		q = q.Offset(opts.Offset)
	}
	q = q.Order("failed_at DESC")

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
	err := s.db.NewSelect().
		Model(m).
		Where("id = ?", dlqID.String()).
		Limit(1).
		Scan(ctx)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
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
	_, err = s.db.NewDelete().
		Model((*dlqEntryModel)(nil)).
		Where("id = ?", dlqID.String()).
		Exec(ctx)
	return err
}

func (s *Store) ReplayBulk(ctx context.Context, from, to time.Time) (int64, error) {
	var models []dlqEntryModel
	if err := s.db.NewSelect().
		Model(&models).
		Where("failed_at >= ?", from).
		Where("failed_at <= ?", to).
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

		if _, err := s.db.NewDelete().
			Model((*dlqEntryModel)(nil)).
			Where("id = ?", models[i].ID).
			Exec(ctx); err != nil {
			return count, err
		}
		count++
	}

	return count, nil
}

func (s *Store) Purge(ctx context.Context, before time.Time) (int64, error) {
	res, err := s.db.NewDelete().
		Model((*dlqEntryModel)(nil)).
		Where("failed_at < ?", before).
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
	count, err := s.db.NewSelect().
		Model((*dlqEntryModel)(nil)).
		Count(ctx)
	return int64(count), err
}

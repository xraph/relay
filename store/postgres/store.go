package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/xraph/relay"
	"github.com/xraph/relay/catalog"
	"github.com/xraph/relay/delivery"
	"github.com/xraph/relay/dlq"
	"github.com/xraph/relay/endpoint"
	"github.com/xraph/relay/event"
	"github.com/xraph/relay/id"
	"github.com/xraph/relay/internal/entity"
	relaystore "github.com/xraph/relay/store"
)

// compile-time interface check.
var _ relaystore.Store = (*Store)(nil)

// Store is a PostgreSQL implementation of store.Store using pgx/v5.
type Store struct {
	pool *pgxpool.Pool
}

// New creates a new PostgreSQL store from a connection string.
func New(ctx context.Context, connString string) (*Store, error) {
	pool, err := pgxpool.New(ctx, connString)
	if err != nil {
		return nil, fmt.Errorf("postgres: connect: %w", err)
	}
	return &Store{pool: pool}, nil
}

// NewFromPool creates a new PostgreSQL store from an existing pool.
func NewFromPool(pool *pgxpool.Pool) *Store {
	return &Store{pool: pool}
}

// Migrate runs all schema migrations.
func (s *Store) Migrate(ctx context.Context) error {
	if err := runMigrations(ctx, s.pool); err != nil {
		return fmt.Errorf("%w: %w", relay.ErrMigrationFailed, err)
	}
	return nil
}

// Ping checks database connectivity.
func (s *Store) Ping(ctx context.Context) error {
	return s.pool.Ping(ctx)
}

// Close closes the connection pool.
func (s *Store) Close() error {
	s.pool.Close()
	return nil
}

// ──────────────────────────────────────────────────
// catalog.Store
// ──────────────────────────────────────────────────

// RegisterType creates or updates an event type definition (upsert by name).
func (s *Store) RegisterType(ctx context.Context, et *catalog.EventType) error {
	schemaJSON, err := json.Marshal(et.Definition.Schema)
	if err != nil {
		return fmt.Errorf("marshal schema: %w", err)
	}
	exampleJSON, err := json.Marshal(et.Definition.Example)
	if err != nil {
		return fmt.Errorf("marshal example: %w", err)
	}
	metadataJSON, err := json.Marshal(et.Metadata)
	if err != nil {
		return fmt.Errorf("marshal metadata: %w", err)
	}

	_, err = s.pool.Exec(ctx, `
		INSERT INTO relay_event_types (id, name, description, "group", schema, schema_version, version, example, deprecated, deprecated_at, scope_app_id, metadata, created_at, updated_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14)
		ON CONFLICT (name) DO UPDATE SET
			description = EXCLUDED.description,
			"group" = EXCLUDED."group",
			schema = EXCLUDED.schema,
			schema_version = EXCLUDED.schema_version,
			version = EXCLUDED.version,
			example = EXCLUDED.example,
			metadata = EXCLUDED.metadata,
			updated_at = NOW()
	`,
		et.ID.String(),
		et.Definition.Name,
		et.Definition.Description,
		et.Definition.Group,
		schemaJSON,
		et.Definition.SchemaVersion,
		et.Definition.Version,
		exampleJSON,
		et.IsDeprecated,
		et.DeprecatedAt,
		et.ScopeAppID,
		metadataJSON,
		et.CreatedAt,
		et.UpdatedAt,
	)
	return err
}

// GetType returns an event type by name.
func (s *Store) GetType(ctx context.Context, name string) (*catalog.EventType, error) {
	return s.scanEventType(s.pool.QueryRow(ctx, `
		SELECT id, name, description, "group", schema, schema_version, version, example,
		       deprecated, deprecated_at, scope_app_id, metadata, created_at, updated_at
		FROM relay_event_types WHERE name = $1
	`, name))
}

// GetTypeByID returns an event type by its TypeID.
func (s *Store) GetTypeByID(ctx context.Context, etID id.ID) (*catalog.EventType, error) {
	return s.scanEventType(s.pool.QueryRow(ctx, `
		SELECT id, name, description, "group", schema, schema_version, version, example,
		       deprecated, deprecated_at, scope_app_id, metadata, created_at, updated_at
		FROM relay_event_types WHERE id = $1
	`, etID.String()))
}

// ListTypes returns all registered event types, optionally filtered.
func (s *Store) ListTypes(ctx context.Context, opts catalog.ListOpts) ([]*catalog.EventType, error) {
	query := `
		SELECT id, name, description, "group", schema, schema_version, version, example,
		       deprecated, deprecated_at, scope_app_id, metadata, created_at, updated_at
		FROM relay_event_types WHERE 1=1`
	args := []any{}
	argIdx := 1

	if !opts.IncludeDeprecated {
		query += fmt.Sprintf(` AND deprecated = $%d`, argIdx)
		args = append(args, false)
		argIdx++
	}
	if opts.Group != "" {
		query += fmt.Sprintf(` AND "group" = $%d`, argIdx)
		args = append(args, opts.Group)
		argIdx++
	}

	query += ` ORDER BY name`

	if opts.Limit > 0 {
		query += fmt.Sprintf(` LIMIT $%d`, argIdx)
		args = append(args, opts.Limit)
		argIdx++
	}
	if opts.Offset > 0 {
		query += fmt.Sprintf(` OFFSET $%d`, argIdx)
		args = append(args, opts.Offset)
	}

	rows, err := s.pool.Query(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	return s.collectEventTypes(rows)
}

// DeleteType soft-deletes (deprecates) an event type.
func (s *Store) DeleteType(ctx context.Context, name string) error {
	tag, err := s.pool.Exec(ctx, `
		UPDATE relay_event_types SET deprecated = TRUE, deprecated_at = NOW(), updated_at = NOW()
		WHERE name = $1
	`, name)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return relay.ErrEventTypeNotFound
	}
	return nil
}

// MatchTypes returns event types matching a glob pattern.
func (s *Store) MatchTypes(ctx context.Context, pattern string) ([]*catalog.EventType, error) {
	// Load all non-deprecated types and filter in Go using glob matching.
	rows, err := s.pool.Query(ctx, `
		SELECT id, name, description, "group", schema, schema_version, version, example,
		       deprecated, deprecated_at, scope_app_id, metadata, created_at, updated_at
		FROM relay_event_types WHERE deprecated = FALSE
	`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	all, err := s.collectEventTypes(rows)
	if err != nil {
		return nil, err
	}

	var result []*catalog.EventType
	for _, et := range all {
		if catalog.Match(pattern, et.Definition.Name) {
			result = append(result, et)
		}
	}
	return result, nil
}

// ──────────────────────────────────────────────────
// endpoint.Store
// ──────────────────────────────────────────────────

// CreateEndpoint persists a new endpoint.
func (s *Store) CreateEndpoint(ctx context.Context, ep *endpoint.Endpoint) error {
	headersJSON, err := json.Marshal(ep.Headers)
	if err != nil {
		return fmt.Errorf("marshal headers: %w", err)
	}
	metadataJSON, err := json.Marshal(ep.Metadata)
	if err != nil {
		return fmt.Errorf("marshal metadata: %w", err)
	}

	_, err = s.pool.Exec(ctx, `
		INSERT INTO relay_endpoints (id, tenant_id, url, secret, event_types, headers, enabled, rate_limit, metadata, created_at, updated_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11)
	`,
		ep.ID.String(),
		ep.TenantID,
		ep.URL,
		ep.Secret,
		ep.EventTypes,
		headersJSON,
		ep.Enabled,
		ep.RateLimit,
		metadataJSON,
		ep.CreatedAt,
		ep.UpdatedAt,
	)
	return err
}

// GetEndpoint returns an endpoint by ID.
func (s *Store) GetEndpoint(ctx context.Context, epID id.ID) (*endpoint.Endpoint, error) {
	return s.scanEndpoint(s.pool.QueryRow(ctx, `
		SELECT id, tenant_id, url, secret, event_types, headers, enabled, rate_limit, metadata, created_at, updated_at
		FROM relay_endpoints WHERE id = $1
	`, epID.String()))
}

// UpdateEndpoint modifies an existing endpoint.
func (s *Store) UpdateEndpoint(ctx context.Context, ep *endpoint.Endpoint) error {
	headersJSON, err := json.Marshal(ep.Headers)
	if err != nil {
		return fmt.Errorf("marshal headers: %w", err)
	}
	metadataJSON, err := json.Marshal(ep.Metadata)
	if err != nil {
		return fmt.Errorf("marshal metadata: %w", err)
	}

	tag, err := s.pool.Exec(ctx, `
		UPDATE relay_endpoints SET
			url = $2, secret = $3, event_types = $4, headers = $5,
			enabled = $6, rate_limit = $7, metadata = $8, updated_at = NOW()
		WHERE id = $1
	`,
		ep.ID.String(),
		ep.URL,
		ep.Secret,
		ep.EventTypes,
		headersJSON,
		ep.Enabled,
		ep.RateLimit,
		metadataJSON,
	)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return relay.ErrEndpointNotFound
	}
	return nil
}

// DeleteEndpoint removes an endpoint.
func (s *Store) DeleteEndpoint(ctx context.Context, epID id.ID) error {
	tag, err := s.pool.Exec(ctx, `DELETE FROM relay_endpoints WHERE id = $1`, epID.String())
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return relay.ErrEndpointNotFound
	}
	return nil
}

// ListEndpoints returns endpoints for a tenant, optionally filtered.
func (s *Store) ListEndpoints(ctx context.Context, tenantID string, opts endpoint.ListOpts) ([]*endpoint.Endpoint, error) {
	query := `
		SELECT id, tenant_id, url, secret, event_types, headers, enabled, rate_limit, metadata, created_at, updated_at
		FROM relay_endpoints WHERE tenant_id = $1`
	args := []any{tenantID}
	argIdx := 2

	if opts.Enabled != nil {
		query += fmt.Sprintf(` AND enabled = $%d`, argIdx)
		args = append(args, *opts.Enabled)
		argIdx++
	}

	query += ` ORDER BY created_at`

	if opts.Limit > 0 {
		query += fmt.Sprintf(` LIMIT $%d`, argIdx)
		args = append(args, opts.Limit)
		argIdx++
	}
	if opts.Offset > 0 {
		query += fmt.Sprintf(` OFFSET $%d`, argIdx)
		args = append(args, opts.Offset)
	}

	rows, err := s.pool.Query(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	return s.collectEndpoints(rows)
}

// Resolve finds all active endpoints matching an event type for a tenant.
// Loads candidates from DB, then applies Go glob matching.
func (s *Store) Resolve(ctx context.Context, tenantID, eventType string) ([]*endpoint.Endpoint, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT id, tenant_id, url, secret, event_types, headers, enabled, rate_limit, metadata, created_at, updated_at
		FROM relay_endpoints WHERE tenant_id = $1 AND enabled = TRUE
	`, tenantID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	all, collectErr := s.collectEndpoints(rows)
	if collectErr != nil {
		return nil, collectErr
	}

	var result []*endpoint.Endpoint
	for _, ep := range all {
		for _, pattern := range ep.EventTypes {
			if catalog.Match(pattern, eventType) {
				result = append(result, ep)
				break
			}
		}
	}
	return result, nil
}

// SetEnabled enables or disables an endpoint.
func (s *Store) SetEnabled(ctx context.Context, epID id.ID, enabled bool) error {
	tag, err := s.pool.Exec(ctx, `
		UPDATE relay_endpoints SET enabled = $2, updated_at = NOW() WHERE id = $1
	`, epID.String(), enabled)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return relay.ErrEndpointNotFound
	}
	return nil
}

// ──────────────────────────────────────────────────
// event.Store
// ──────────────────────────────────────────────────

// CreateEvent persists an event. Idempotency key conflicts return ErrDuplicateIdempotencyKey.
func (s *Store) CreateEvent(ctx context.Context, evt *event.Event) error {
	dataJSON, err := json.Marshal(evt.Data)
	if err != nil {
		return fmt.Errorf("marshal event data: %w", err)
	}

	var idemKey *string
	if evt.IdempotencyKey != "" {
		idemKey = &evt.IdempotencyKey
	}

	tag, err := s.pool.Exec(ctx, `
		INSERT INTO relay_events (id, type, tenant_id, data, idempotency_key, scope_app_id, scope_org_id, created_at, updated_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)
		ON CONFLICT (idempotency_key) WHERE idempotency_key IS NOT NULL AND idempotency_key != '' DO NOTHING
	`,
		evt.ID.String(),
		evt.Type,
		evt.TenantID,
		dataJSON,
		idemKey,
		evt.ScopeAppID,
		evt.ScopeOrgID,
		evt.CreatedAt,
		evt.UpdatedAt,
	)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 && evt.IdempotencyKey != "" {
		return relay.ErrDuplicateIdempotencyKey
	}
	return nil
}

// GetEvent returns an event by ID.
func (s *Store) GetEvent(ctx context.Context, evtID id.ID) (*event.Event, error) {
	return s.scanEvent(s.pool.QueryRow(ctx, `
		SELECT id, type, tenant_id, data, idempotency_key, scope_app_id, scope_org_id, created_at, updated_at
		FROM relay_events WHERE id = $1
	`, evtID.String()))
}

// ListEvents returns events, optionally filtered.
func (s *Store) ListEvents(ctx context.Context, opts event.ListOpts) ([]*event.Event, error) {
	query := `SELECT id, type, tenant_id, data, idempotency_key, scope_app_id, scope_org_id, created_at, updated_at
		FROM relay_events WHERE 1=1`
	args := []any{}
	argIdx := 1

	if opts.Type != "" {
		query += fmt.Sprintf(` AND type = $%d`, argIdx)
		args = append(args, opts.Type)
		argIdx++
	}
	if opts.From != nil {
		query += fmt.Sprintf(` AND created_at >= $%d`, argIdx)
		args = append(args, *opts.From)
		argIdx++
	}
	if opts.To != nil {
		query += fmt.Sprintf(` AND created_at <= $%d`, argIdx)
		args = append(args, *opts.To)
		argIdx++
	}

	query += ` ORDER BY created_at DESC`

	if opts.Limit > 0 {
		query += fmt.Sprintf(` LIMIT $%d`, argIdx)
		args = append(args, opts.Limit)
		argIdx++
	}
	if opts.Offset > 0 {
		query += fmt.Sprintf(` OFFSET $%d`, argIdx)
		args = append(args, opts.Offset)
	}

	rows, err := s.pool.Query(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	return s.collectEvents(rows)
}

// ListEventsByTenant returns events for a specific tenant.
func (s *Store) ListEventsByTenant(ctx context.Context, tenantID string, opts event.ListOpts) ([]*event.Event, error) {
	query := `SELECT id, type, tenant_id, data, idempotency_key, scope_app_id, scope_org_id, created_at, updated_at
		FROM relay_events WHERE tenant_id = $1`
	args := []any{tenantID}
	argIdx := 2

	if opts.Type != "" {
		query += fmt.Sprintf(` AND type = $%d`, argIdx)
		args = append(args, opts.Type)
		argIdx++
	}
	if opts.From != nil {
		query += fmt.Sprintf(` AND created_at >= $%d`, argIdx)
		args = append(args, *opts.From)
		argIdx++
	}
	if opts.To != nil {
		query += fmt.Sprintf(` AND created_at <= $%d`, argIdx)
		args = append(args, *opts.To)
		argIdx++
	}

	query += ` ORDER BY created_at DESC`

	if opts.Limit > 0 {
		query += fmt.Sprintf(` LIMIT $%d`, argIdx)
		args = append(args, opts.Limit)
		argIdx++
	}
	if opts.Offset > 0 {
		query += fmt.Sprintf(` OFFSET $%d`, argIdx)
		args = append(args, opts.Offset)
	}

	rows, err := s.pool.Query(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	return s.collectEvents(rows)
}

// ──────────────────────────────────────────────────
// delivery.Store
// ──────────────────────────────────────────────────

// Enqueue creates a pending delivery.
func (s *Store) Enqueue(ctx context.Context, d *delivery.Delivery) error {
	_, err := s.pool.Exec(ctx, `
		INSERT INTO relay_deliveries (id, event_id, endpoint_id, state, attempt_count, max_attempts, next_attempt_at, created_at, updated_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)
	`,
		d.ID.String(),
		d.EventID.String(),
		d.EndpointID.String(),
		string(d.State),
		d.AttemptCount,
		d.MaxAttempts,
		d.NextAttemptAt,
		d.CreatedAt,
		d.UpdatedAt,
	)
	return err
}

// EnqueueBatch creates multiple deliveries atomically.
func (s *Store) EnqueueBatch(ctx context.Context, ds []*delivery.Delivery) error {
	if len(ds) == 0 {
		return nil
	}

	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)

	for _, d := range ds {
		if _, execErr := tx.Exec(ctx, `
			INSERT INTO relay_deliveries (id, event_id, endpoint_id, state, attempt_count, max_attempts, next_attempt_at, created_at, updated_at)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)
		`,
			d.ID.String(),
			d.EventID.String(),
			d.EndpointID.String(),
			string(d.State),
			d.AttemptCount,
			d.MaxAttempts,
			d.NextAttemptAt,
			d.CreatedAt,
			d.UpdatedAt,
		); execErr != nil {
			return execErr
		}
	}

	return tx.Commit(ctx)
}

// Dequeue fetches pending deliveries ready for attempt using SELECT FOR UPDATE SKIP LOCKED.
func (s *Store) Dequeue(ctx context.Context, limit int) ([]*delivery.Delivery, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)

	rows, queryErr := tx.Query(ctx, `
		SELECT id, event_id, endpoint_id, state, attempt_count, max_attempts, next_attempt_at,
		       last_error, last_status_code, last_response, last_latency_ms, completed_at, created_at, updated_at
		FROM relay_deliveries
		WHERE state = 'pending' AND next_attempt_at <= NOW()
		ORDER BY next_attempt_at
		LIMIT $1
		FOR UPDATE SKIP LOCKED
	`, limit)
	if queryErr != nil {
		return nil, queryErr
	}

	result, collectErr := s.collectDeliveries(rows)
	rows.Close()
	if collectErr != nil {
		return nil, collectErr
	}

	if commitErr := tx.Commit(ctx); commitErr != nil {
		return nil, commitErr
	}

	return result, nil
}

// UpdateDelivery modifies a delivery.
func (s *Store) UpdateDelivery(ctx context.Context, d *delivery.Delivery) error {
	tag, err := s.pool.Exec(ctx, `
		UPDATE relay_deliveries SET
			state = $2, attempt_count = $3, max_attempts = $4, next_attempt_at = $5,
			last_error = $6, last_status_code = $7, last_response = $8, last_latency_ms = $9,
			completed_at = $10, updated_at = NOW()
		WHERE id = $1
	`,
		d.ID.String(),
		string(d.State),
		d.AttemptCount,
		d.MaxAttempts,
		d.NextAttemptAt,
		d.LastError,
		d.LastStatusCode,
		d.LastResponse,
		d.LastLatencyMs,
		d.CompletedAt,
	)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return relay.ErrDeliveryNotFound
	}
	return nil
}

// GetDelivery returns a delivery by ID.
func (s *Store) GetDelivery(ctx context.Context, delID id.ID) (*delivery.Delivery, error) {
	return s.scanDelivery(s.pool.QueryRow(ctx, `
		SELECT id, event_id, endpoint_id, state, attempt_count, max_attempts, next_attempt_at,
		       last_error, last_status_code, last_response, last_latency_ms, completed_at, created_at, updated_at
		FROM relay_deliveries WHERE id = $1
	`, delID.String()))
}

// ListByEndpoint returns delivery history for an endpoint.
func (s *Store) ListByEndpoint(ctx context.Context, epID id.ID, opts delivery.ListOpts) ([]*delivery.Delivery, error) {
	query := `
		SELECT id, event_id, endpoint_id, state, attempt_count, max_attempts, next_attempt_at,
		       last_error, last_status_code, last_response, last_latency_ms, completed_at, created_at, updated_at
		FROM relay_deliveries WHERE endpoint_id = $1`
	args := []any{epID.String()}
	argIdx := 2

	if opts.State != nil {
		query += fmt.Sprintf(` AND state = $%d`, argIdx)
		args = append(args, string(*opts.State))
		argIdx++
	}

	query += ` ORDER BY created_at DESC`

	if opts.Limit > 0 {
		query += fmt.Sprintf(` LIMIT $%d`, argIdx)
		args = append(args, opts.Limit)
		argIdx++
	}
	if opts.Offset > 0 {
		query += fmt.Sprintf(` OFFSET $%d`, argIdx)
		args = append(args, opts.Offset)
	}

	rows, err := s.pool.Query(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	return s.collectDeliveries(rows)
}

// ListByEvent returns all deliveries for a specific event.
func (s *Store) ListByEvent(ctx context.Context, evtID id.ID) ([]*delivery.Delivery, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT id, event_id, endpoint_id, state, attempt_count, max_attempts, next_attempt_at,
		       last_error, last_status_code, last_response, last_latency_ms, completed_at, created_at, updated_at
		FROM relay_deliveries WHERE event_id = $1
	`, evtID.String())
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	return s.collectDeliveries(rows)
}

// CountPending returns the number of deliveries awaiting attempt.
func (s *Store) CountPending(ctx context.Context) (int64, error) {
	var count int64
	err := s.pool.QueryRow(ctx, `SELECT COUNT(*) FROM relay_deliveries WHERE state = 'pending'`).Scan(&count)
	return count, err
}

// ──────────────────────────────────────────────────
// dlq.Store
// ──────────────────────────────────────────────────

// Push moves a permanently failed delivery into the DLQ.
func (s *Store) Push(ctx context.Context, entry *dlq.Entry) error {
	payloadJSON, err := json.Marshal(entry.Payload)
	if err != nil {
		return fmt.Errorf("marshal payload: %w", err)
	}

	_, err = s.pool.Exec(ctx, `
		INSERT INTO relay_dlq (id, delivery_id, event_id, endpoint_id, event_type, tenant_id, url, payload, error, attempt_count, last_status_code, failed_at, created_at, updated_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14)
	`,
		entry.ID.String(),
		entry.DeliveryID.String(),
		entry.EventID.String(),
		entry.EndpointID.String(),
		entry.EventType,
		entry.TenantID,
		entry.URL,
		payloadJSON,
		entry.Error,
		entry.AttemptCount,
		entry.LastStatusCode,
		entry.FailedAt,
		entry.CreatedAt,
		entry.UpdatedAt,
	)
	return err
}

// ListDLQ returns DLQ entries, optionally filtered.
func (s *Store) ListDLQ(ctx context.Context, opts dlq.ListOpts) ([]*dlq.Entry, error) {
	query := `SELECT id, delivery_id, event_id, endpoint_id, event_type, tenant_id, url, payload, error, attempt_count, last_status_code, replayed_at, failed_at, created_at, updated_at
		FROM relay_dlq WHERE 1=1`
	args := []any{}
	argIdx := 1

	if opts.TenantID != "" {
		query += fmt.Sprintf(` AND tenant_id = $%d`, argIdx)
		args = append(args, opts.TenantID)
		argIdx++
	}
	if opts.EndpointID != nil {
		query += fmt.Sprintf(` AND endpoint_id = $%d`, argIdx)
		args = append(args, opts.EndpointID.String())
		argIdx++
	}
	if opts.From != nil {
		query += fmt.Sprintf(` AND failed_at >= $%d`, argIdx)
		args = append(args, *opts.From)
		argIdx++
	}
	if opts.To != nil {
		query += fmt.Sprintf(` AND failed_at <= $%d`, argIdx)
		args = append(args, *opts.To)
		argIdx++
	}

	query += ` ORDER BY failed_at DESC`

	if opts.Limit > 0 {
		query += fmt.Sprintf(` LIMIT $%d`, argIdx)
		args = append(args, opts.Limit)
		argIdx++
	}
	if opts.Offset > 0 {
		query += fmt.Sprintf(` OFFSET $%d`, argIdx)
		args = append(args, opts.Offset)
	}

	rows, err := s.pool.Query(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	return s.collectDLQEntries(rows)
}

// GetDLQ returns a DLQ entry by ID.
func (s *Store) GetDLQ(ctx context.Context, dlqID id.ID) (*dlq.Entry, error) {
	return s.scanDLQEntry(s.pool.QueryRow(ctx, `
		SELECT id, delivery_id, event_id, endpoint_id, event_type, tenant_id, url, payload, error, attempt_count, last_status_code, replayed_at, failed_at, created_at, updated_at
		FROM relay_dlq WHERE id = $1
	`, dlqID.String()))
}

// Replay marks a DLQ entry for redelivery and re-enqueues a new delivery.
func (s *Store) Replay(ctx context.Context, dlqID id.ID) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)

	// Mark replayed.
	var eventID, endpointID string
	queryErr := tx.QueryRow(ctx, `
		UPDATE relay_dlq SET replayed_at = NOW(), updated_at = NOW()
		WHERE id = $1 RETURNING event_id, endpoint_id
	`, dlqID.String()).Scan(&eventID, &endpointID)
	if queryErr != nil {
		if errors.Is(queryErr, pgx.ErrNoRows) {
			return relay.ErrDLQNotFound
		}
		return queryErr
	}

	// Create new delivery.
	newDelID := id.NewDeliveryID()
	now := time.Now().UTC()
	if _, execErr := tx.Exec(ctx, `
		INSERT INTO relay_deliveries (id, event_id, endpoint_id, state, attempt_count, max_attempts, next_attempt_at, created_at, updated_at)
		VALUES ($1, $2, $3, 'pending', 0, 5, $4, $5, $6)
	`, newDelID.String(), eventID, endpointID, now, now, now); execErr != nil {
		return execErr
	}

	return tx.Commit(ctx)
}

// ReplayBulk replays all DLQ entries in a time window.
func (s *Store) ReplayBulk(ctx context.Context, from, to time.Time) (int64, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback(ctx)

	// Get entries to replay.
	rows, queryErr := tx.Query(ctx, `
		SELECT id, event_id, endpoint_id FROM relay_dlq
		WHERE failed_at >= $1 AND failed_at <= $2 AND replayed_at IS NULL
	`, from, to)
	if queryErr != nil {
		return 0, queryErr
	}

	type replayInfo struct {
		dlqID, eventID, endpointID string
	}
	var entries []replayInfo
	for rows.Next() {
		var ri replayInfo
		if scanErr := rows.Scan(&ri.dlqID, &ri.eventID, &ri.endpointID); scanErr != nil {
			rows.Close()
			return 0, scanErr
		}
		entries = append(entries, ri)
	}
	rows.Close()
	if rows.Err() != nil {
		return 0, rows.Err()
	}

	now := time.Now().UTC()
	for _, ri := range entries {
		if _, execErr := tx.Exec(ctx, `UPDATE relay_dlq SET replayed_at = NOW(), updated_at = NOW() WHERE id = $1`, ri.dlqID); execErr != nil {
			return 0, execErr
		}
		newDelID := id.NewDeliveryID()
		if _, execErr := tx.Exec(ctx, `
			INSERT INTO relay_deliveries (id, event_id, endpoint_id, state, attempt_count, max_attempts, next_attempt_at, created_at, updated_at)
			VALUES ($1, $2, $3, 'pending', 0, 5, $4, $5, $6)
		`, newDelID.String(), ri.eventID, ri.endpointID, now, now, now); execErr != nil {
			return 0, execErr
		}
	}

	if commitErr := tx.Commit(ctx); commitErr != nil {
		return 0, commitErr
	}
	return int64(len(entries)), nil
}

// Purge deletes DLQ entries older than a threshold.
func (s *Store) Purge(ctx context.Context, before time.Time) (int64, error) {
	tag, err := s.pool.Exec(ctx, `DELETE FROM relay_dlq WHERE created_at < $1`, before)
	if err != nil {
		return 0, err
	}
	return tag.RowsAffected(), nil
}

// CountDLQ returns the total number of DLQ entries.
func (s *Store) CountDLQ(ctx context.Context) (int64, error) {
	var count int64
	err := s.pool.QueryRow(ctx, `SELECT COUNT(*) FROM relay_dlq`).Scan(&count)
	return count, err
}

// ──────────────────────────────────────────────────
// Row scanners
// ──────────────────────────────────────────────────

func (s *Store) scanEventType(row pgx.Row) (*catalog.EventType, error) {
	var (
		idStr                                                        string
		name, description, group, schemaVersion, version, scopeAppID string
		schemaJSON, exampleJSON, metadataJSON                        []byte
		deprecated                                                   bool
		deprecatedAt                                                 *time.Time
		createdAt, updatedAt                                         time.Time
	)

	err := row.Scan(
		&idStr, &name, &description, &group, &schemaJSON, &schemaVersion, &version, &exampleJSON,
		&deprecated, &deprecatedAt, &scopeAppID, &metadataJSON, &createdAt, &updatedAt,
	)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, relay.ErrEventTypeNotFound
		}
		return nil, err
	}

	etID, parseErr := id.ParseEventTypeID(idStr)
	if parseErr != nil {
		return nil, fmt.Errorf("parse event type id %q: %w", idStr, parseErr)
	}

	var schema json.RawMessage
	if len(schemaJSON) > 0 && string(schemaJSON) != "null" {
		schema = schemaJSON
	}
	var example json.RawMessage
	if len(exampleJSON) > 0 && string(exampleJSON) != "null" {
		example = exampleJSON
	}
	var metadata map[string]string
	if len(metadataJSON) > 0 && string(metadataJSON) != "null" {
		if err := json.Unmarshal(metadataJSON, &metadata); err != nil {
			return nil, fmt.Errorf("unmarshal event type metadata: %w", err)
		}
	}

	return &catalog.EventType{
		Entity: entity.Entity{
			CreatedAt: createdAt,
			UpdatedAt: updatedAt,
		},
		ID: etID,
		Definition: catalog.WebhookDefinition{
			Name:          name,
			Description:   description,
			Group:         group,
			Schema:        schema,
			SchemaVersion: schemaVersion,
			Version:       version,
			Example:       example,
		},
		IsDeprecated: deprecated,
		DeprecatedAt: deprecatedAt,
		ScopeAppID:   scopeAppID,
		Metadata:     metadata,
	}, nil
}

func (s *Store) collectEventTypes(rows pgx.Rows) ([]*catalog.EventType, error) {
	var result []*catalog.EventType
	for rows.Next() {
		et, err := s.scanEventType(rows)
		if err != nil {
			return nil, err
		}
		result = append(result, et)
	}
	return result, rows.Err()
}

func (s *Store) scanEndpoint(row pgx.Row) (*endpoint.Endpoint, error) {
	var (
		idStr, tenantID, url, secret string
		eventTypes                   []string
		headersJSON, metadataJSON    []byte
		enabled                      bool
		rateLimit                    int
		createdAt, updatedAt         time.Time
	)

	err := row.Scan(
		&idStr, &tenantID, &url, &secret, &eventTypes, &headersJSON,
		&enabled, &rateLimit, &metadataJSON, &createdAt, &updatedAt,
	)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, relay.ErrEndpointNotFound
		}
		return nil, err
	}

	epID, parseErr := id.ParseEndpointID(idStr)
	if parseErr != nil {
		return nil, fmt.Errorf("parse endpoint id %q: %w", idStr, parseErr)
	}

	var headers map[string]string
	if len(headersJSON) > 0 && string(headersJSON) != "null" {
		if err := json.Unmarshal(headersJSON, &headers); err != nil {
			return nil, fmt.Errorf("unmarshal endpoint headers: %w", err)
		}
	}
	var metadata map[string]string
	if len(metadataJSON) > 0 && string(metadataJSON) != "null" {
		if err := json.Unmarshal(metadataJSON, &metadata); err != nil {
			return nil, fmt.Errorf("unmarshal endpoint metadata: %w", err)
		}
	}

	return &endpoint.Endpoint{
		Entity: entity.Entity{
			CreatedAt: createdAt,
			UpdatedAt: updatedAt,
		},
		ID:         epID,
		TenantID:   tenantID,
		URL:        url,
		Secret:     secret,
		EventTypes: eventTypes,
		Headers:    headers,
		Enabled:    enabled,
		RateLimit:  rateLimit,
		Metadata:   metadata,
	}, nil
}

func (s *Store) collectEndpoints(rows pgx.Rows) ([]*endpoint.Endpoint, error) {
	var result []*endpoint.Endpoint
	for rows.Next() {
		ep, err := s.scanEndpoint(rows)
		if err != nil {
			return nil, err
		}
		result = append(result, ep)
	}
	return result, rows.Err()
}

func (s *Store) scanEvent(row pgx.Row) (*event.Event, error) {
	var (
		idStr, evtType, tenantID, scopeAppID, scopeOrgID string
		idemKey                                          *string
		dataJSON                                         []byte
		createdAt, updatedAt                             time.Time
	)

	err := row.Scan(
		&idStr, &evtType, &tenantID, &dataJSON, &idemKey, &scopeAppID, &scopeOrgID, &createdAt, &updatedAt,
	)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, relay.ErrEventNotFound
		}
		return nil, err
	}

	evtID, parseErr := id.ParseEventID(idStr)
	if parseErr != nil {
		return nil, fmt.Errorf("parse event id %q: %w", idStr, parseErr)
	}

	var data json.RawMessage
	if len(dataJSON) > 0 {
		data = dataJSON
	}

	idemKeyStr := ""
	if idemKey != nil {
		idemKeyStr = *idemKey
	}

	return &event.Event{
		Entity: entity.Entity{
			CreatedAt: createdAt,
			UpdatedAt: updatedAt,
		},
		ID:             evtID,
		Type:           evtType,
		TenantID:       tenantID,
		Data:           data,
		ScopeAppID:     scopeAppID,
		ScopeOrgID:     scopeOrgID,
		IdempotencyKey: idemKeyStr,
	}, nil
}

func (s *Store) collectEvents(rows pgx.Rows) ([]*event.Event, error) {
	var result []*event.Event
	for rows.Next() {
		evt, err := s.scanEvent(rows)
		if err != nil {
			return nil, err
		}
		result = append(result, evt)
	}
	return result, rows.Err()
}

func (s *Store) scanDelivery(row pgx.Row) (*delivery.Delivery, error) {
	var (
		idStr, eventIDStr, endpointIDStr, stateStr string
		lastError, lastResponse                    string
		attemptCount, maxAttempts                  int
		lastStatusCode, lastLatencyMs              int
		nextAttemptAt, createdAt, updatedAt        time.Time
		completedAt                                *time.Time
	)

	err := row.Scan(
		&idStr, &eventIDStr, &endpointIDStr, &stateStr, &attemptCount, &maxAttempts, &nextAttemptAt,
		&lastError, &lastStatusCode, &lastResponse, &lastLatencyMs, &completedAt, &createdAt, &updatedAt,
	)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, relay.ErrDeliveryNotFound
		}
		return nil, err
	}

	delID, parseErr := id.ParseDeliveryID(idStr)
	if parseErr != nil {
		return nil, fmt.Errorf("parse delivery id %q: %w", idStr, parseErr)
	}
	eventID, parseErr := id.ParseEventID(eventIDStr)
	if parseErr != nil {
		return nil, fmt.Errorf("parse event id %q: %w", eventIDStr, parseErr)
	}
	endpointID, parseErr := id.ParseEndpointID(endpointIDStr)
	if parseErr != nil {
		return nil, fmt.Errorf("parse endpoint id %q: %w", endpointIDStr, parseErr)
	}

	return &delivery.Delivery{
		Entity: entity.Entity{
			CreatedAt: createdAt,
			UpdatedAt: updatedAt,
		},
		ID:             delID,
		EventID:        eventID,
		EndpointID:     endpointID,
		State:          delivery.State(stateStr),
		AttemptCount:   attemptCount,
		MaxAttempts:    maxAttempts,
		NextAttemptAt:  nextAttemptAt,
		LastError:      lastError,
		LastStatusCode: lastStatusCode,
		LastResponse:   lastResponse,
		LastLatencyMs:  lastLatencyMs,
		CompletedAt:    completedAt,
	}, nil
}

func (s *Store) collectDeliveries(rows pgx.Rows) ([]*delivery.Delivery, error) {
	var result []*delivery.Delivery
	for rows.Next() {
		d, err := s.scanDelivery(rows)
		if err != nil {
			return nil, err
		}
		result = append(result, d)
	}
	return result, rows.Err()
}

func (s *Store) scanDLQEntry(row pgx.Row) (*dlq.Entry, error) {
	var (
		idStr, deliveryIDStr, eventIDStr, endpointIDStr string
		eventType, tenantID, url, errMsg                string
		payloadJSON                                     []byte
		attemptCount, lastStatusCode                    int
		replayedAt                                      *time.Time
		failedAt, createdAt, updatedAt                  time.Time
	)

	err := row.Scan(
		&idStr, &deliveryIDStr, &eventIDStr, &endpointIDStr, &eventType, &tenantID, &url,
		&payloadJSON, &errMsg, &attemptCount, &lastStatusCode, &replayedAt, &failedAt, &createdAt, &updatedAt,
	)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, relay.ErrDLQNotFound
		}
		return nil, err
	}

	dlqID, parseErr := id.ParseDLQID(idStr)
	if parseErr != nil {
		return nil, fmt.Errorf("parse dlq id %q: %w", idStr, parseErr)
	}
	deliveryID, parseErr := id.ParseDeliveryID(deliveryIDStr)
	if parseErr != nil {
		return nil, fmt.Errorf("parse delivery id %q: %w", deliveryIDStr, parseErr)
	}
	eventID, parseErr := id.ParseEventID(eventIDStr)
	if parseErr != nil {
		return nil, fmt.Errorf("parse event id %q: %w", eventIDStr, parseErr)
	}
	endpointID, parseErr := id.ParseEndpointID(endpointIDStr)
	if parseErr != nil {
		return nil, fmt.Errorf("parse endpoint id %q: %w", endpointIDStr, parseErr)
	}

	var payload json.RawMessage
	if len(payloadJSON) > 0 {
		payload = payloadJSON
	}

	return &dlq.Entry{
		Entity: entity.Entity{
			CreatedAt: createdAt,
			UpdatedAt: updatedAt,
		},
		ID:             dlqID,
		DeliveryID:     deliveryID,
		EventID:        eventID,
		EndpointID:     endpointID,
		EventType:      eventType,
		TenantID:       tenantID,
		URL:            url,
		Payload:        payload,
		Error:          errMsg,
		AttemptCount:   attemptCount,
		LastStatusCode: lastStatusCode,
		ReplayedAt:     replayedAt,
		FailedAt:       failedAt,
	}, nil
}

func (s *Store) collectDLQEntries(rows pgx.Rows) ([]*dlq.Entry, error) {
	var result []*dlq.Entry
	for rows.Next() {
		entry, err := s.scanDLQEntry(rows)
		if err != nil {
			return nil, err
		}
		result = append(result, entry)
	}
	return result, rows.Err()
}

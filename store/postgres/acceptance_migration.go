package postgres

import (
	"context"

	"github.com/xraph/grove/migrate"
)

func init() {
	Migrations.MustRegister(&migrate.Migration{
		Name: "relay_reliable_acceptance", Version: "20261009000001",
		Up: func(ctx context.Context, exec migrate.Executor) error {
			_, err := exec.Exec(ctx, `
ALTER TABLE relay_endpoints
 ADD COLUMN IF NOT EXISTS scope_app_id TEXT NOT NULL DEFAULT '',
 ADD COLUMN IF NOT EXISTS scope_org_id TEXT NOT NULL DEFAULT '';
CREATE INDEX IF NOT EXISTS idx_relay_endpoints_scope ON relay_endpoints(scope_app_id, scope_org_id, tenant_id) WHERE enabled;
CREATE TABLE IF NOT EXISTS relay_acceptances (
 identity TEXT PRIMARY KEY,
 receipt JSONB NOT NULL
);
`)
			return err
		},
		Down: func(ctx context.Context, exec migrate.Executor) error {
			_, err := exec.Exec(ctx, `DROP TABLE IF EXISTS relay_acceptances;
DROP INDEX IF EXISTS idx_relay_endpoints_scope;
ALTER TABLE relay_endpoints DROP COLUMN IF EXISTS scope_app_id, DROP COLUMN IF EXISTS scope_org_id;`)
			return err
		},
	})
}

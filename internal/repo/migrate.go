// Package repo holds the Postgres-backed persistence layer.
package repo

import (
	"context"
	_ "embed"

	"github.com/jackc/pgx/v5/pgxpool"
)

//go:embed schema/0001_init.sql
var schemaV1 string

//go:embed schema/0002_node_profile_owner.sql
var schemaV2 string

//go:embed schema/0003_awg31_profiles.sql
var schemaV3 string

//go:embed schema/0004_default_node.sql
var schemaV4 string

//go:embed schema/0005_node_provisioning_gate.sql
var schemaV5 string

// Migrate applies the embedded migrations in order inside one transaction.
// Production deployments can replace this lightweight runner with a dedicated
// migration tool later; keeping each migration as an immutable file preserves
// upgrade semantics for existing installations.
func Migrate(ctx context.Context, pool *pgxpool.Pool) error {
	tx, err := pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()

	for _, migration := range []string{schemaV1, schemaV2, schemaV3, schemaV4, schemaV5} {
		if _, err := tx.Exec(ctx, migration); err != nil {
			return err
		}
	}
	return tx.Commit(ctx)
}

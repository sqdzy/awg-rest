// Package repo holds the Postgres-backed persistence layer.
package repo

import (
	"context"
	_ "embed"

	"github.com/jackc/pgx/v5/pgxpool"
)

//go:embed schema/0001_init.sql
var schemaSQL string

// Migrate applies the embedded schema. For production we recommend a real
// migration tool (golang-migrate, atlas, ariga); embedding the schema is fine
// for tests and small deployments since every statement is `CREATE ... IF NOT
// EXISTS`. Idempotent.
func Migrate(ctx context.Context, pool *pgxpool.Pool) error {
	_, err := pool.Exec(ctx, schemaSQL)
	return err
}

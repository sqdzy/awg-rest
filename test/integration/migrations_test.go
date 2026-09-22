//go:build integration

package integration

import (
	"context"
	"testing"

	"github.com/awg-rest/awg-rest/internal/repo"
	"github.com/stretchr/testify/require"
)

func TestMigrate_IsIdempotentAcrossRestarts(t *testing.T) {
	ctx := context.Background()
	db := startPostgres(ctx, t)

	// startPostgres already applied all embedded migrations once. Production
	// binaries call repo.Migrate on every start, so repeated application must
	// remain safe.
	require.NoError(t, repo.Migrate(ctx, db.Pool))
	require.NoError(t, repo.Migrate(ctx, db.Pool))
}

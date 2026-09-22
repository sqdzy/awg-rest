package repo

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

// The repository keeps operator-facing migrations under /migrations and embeds
// the same SQL into the Go binary. A byte-for-byte guard prevents production
// upgrades and repo.Migrate tests from exercising different SQL.
func TestEmbeddedMigrationsMatchOperatorFiles(t *testing.T) {
	t.Parallel()

	cases := []struct {
		path     string
		embedded string
	}{
		{"0001_init.up.sql", schemaV1},
		{"0002_node_profile_owner.up.sql", schemaV2},
		{"0003_awg31_profiles.up.sql", schemaV3},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.path, func(t *testing.T) {
			t.Parallel()
			path := filepath.Join("..", "..", "migrations", tc.path)
			raw, err := os.ReadFile(path)
			require.NoError(t, err)
			require.Equal(t, string(raw), tc.embedded,
				"embedded migration must match operator-facing SQL exactly")
		})
	}
}

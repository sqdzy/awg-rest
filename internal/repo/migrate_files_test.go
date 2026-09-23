package repo

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestEmbeddedMigrationsMatchOperatorSQL(t *testing.T) {
	t.Parallel()

	_, thisFile, _, ok := runtime.Caller(0)
	require.True(t, ok)
	repoRoot := filepath.Clean(filepath.Join(filepath.Dir(thisFile), "..", ".."))

	tests := []struct {
		path     string
		embedded string
	}{
		{"migrations/0001_init.up.sql", schemaV1},
		{"migrations/0002_node_profile_owner.up.sql", schemaV2},
		{"migrations/0003_awg31_profiles.up.sql", schemaV3},
		{"migrations/0004_default_node.up.sql", schemaV4},
		{"migrations/0005_node_provisioning_gate.up.sql", schemaV5},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(filepath.Base(tt.path), func(t *testing.T) {
			t.Parallel()
			raw, err := os.ReadFile(filepath.Join(repoRoot, tt.path))
			require.NoError(t, err)
			require.Equal(t, string(raw), tt.embedded,
				"operator migration and embedded runtime migration must stay byte-identical")
		})
	}
}

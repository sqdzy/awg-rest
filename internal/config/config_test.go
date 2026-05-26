package config

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestEnvOrFileIgnoresEmptyEnvValue(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "secret")
	require.NoError(t, os.WriteFile(path, []byte("from-file"), 0o600))

	t.Setenv("AWG_TEST_SECRET", "")
	t.Setenv("AWG_TEST_SECRET_FILE", path)

	got, err := envOrFile("AWG_TEST_SECRET", "AWG_TEST_SECRET_FILE", "")
	require.NoError(t, err)
	require.Equal(t, "from-file", got)
}

func TestLoadTrimsJWTSecretFileWhitespace(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "jwt_secret")
	require.NoError(t, os.WriteFile(path, []byte("secret-with-newline\n"), 0o600))

	t.Setenv("JWT_SECRET", "")
	t.Setenv("JWT_SECRET_FILE", path)

	cfg, err := Load()
	require.NoError(t, err)
	require.Equal(t, []byte("secret-with-newline"), cfg.JWTSecret)
}

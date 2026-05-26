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

func TestLoadClientAllowedIPs(t *testing.T) {
	t.Setenv("CLIENT_ALLOWED_IPS", "10.0.0.0/8, 203.0.113.7/32")

	cfg, err := Load()
	require.NoError(t, err)
	require.Equal(t, []string{"10.0.0.0/8", "203.0.113.7/32"}, cfg.ClientAllowedIPs)
}

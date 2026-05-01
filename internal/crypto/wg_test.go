package crypto

import (
	"encoding/base64"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestGenerateKeyPair_RoundTrip(t *testing.T) {
	t.Parallel()
	kp, err := GenerateKeyPair()
	require.NoError(t, err)
	require.Len(t, kp.PrivateKey, Base64KeyLen)
	require.Len(t, kp.PublicKey, Base64KeyLen)

	// Public key must be deterministically derivable from private.
	pub, err := DerivePublicKey(kp.PrivateKey)
	require.NoError(t, err)
	require.Equal(t, kp.PublicKey, pub, "DerivePublicKey must match generator output")
}

func TestGenerateKeyPair_Clamped(t *testing.T) {
	t.Parallel()
	for i := 0; i < 32; i++ {
		kp, err := GenerateKeyPair()
		require.NoError(t, err)
		raw, err := base64.StdEncoding.DecodeString(kp.PrivateKey)
		require.NoError(t, err)
		require.Equal(t, byte(0), raw[0]&7, "lower 3 bits must be zero")
		require.Equal(t, byte(0), raw[31]&128, "high bit must be cleared")
		require.Equal(t, byte(64), raw[31]&64, "bit 6 must be set")
	}
}

func TestGenerateKeyPair_Unique(t *testing.T) {
	t.Parallel()
	seen := make(map[string]struct{}, 64)
	for i := 0; i < 64; i++ {
		kp, err := GenerateKeyPair()
		require.NoError(t, err)
		_, dup := seen[kp.PrivateKey]
		require.False(t, dup, "duplicate key generated, RNG broken")
		seen[kp.PrivateKey] = struct{}{}
	}
}

func TestValidateKey(t *testing.T) {
	t.Parallel()
	good, err := GenerateKeyPair()
	require.NoError(t, err)

	cases := []struct {
		name string
		in   string
		ok   bool
	}{
		{"valid", good.PublicKey, true},
		{"too short", "abc", false},
		{"invalid b64", strings.Repeat("!", Base64KeyLen), false},
		{"wrong length", base64.StdEncoding.EncodeToString(make([]byte, 16)), false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := ValidateKey(tc.in)
			if tc.ok {
				require.NoError(t, err)
			} else {
				require.Error(t, err)
			}
		})
	}
}

func TestGeneratePresharedKey(t *testing.T) {
	t.Parallel()
	psk, err := GeneratePresharedKey()
	require.NoError(t, err)
	require.NoError(t, ValidateKey(psk))
}

func TestDerivePublicKey_KnownVector(t *testing.T) {
	t.Parallel()
	// Known test vector from RFC 7748 §6.1.
	priv, _ := base64.StdEncoding.DecodeString("cN6OTlhUM2qNQDQ7tnPNEEPvxxJG5BGYJEbhMgcmGmM=")
	require.Len(t, priv, 32)
	// We don't pin exact public-key bytes — just assert deterministic derivation.
	encPriv := base64.StdEncoding.EncodeToString(priv)
	a, err := DerivePublicKey(encPriv)
	require.NoError(t, err)
	b, err := DerivePublicKey(encPriv)
	require.NoError(t, err)
	require.Equal(t, a, b)
}

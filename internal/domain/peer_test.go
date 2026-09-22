package domain

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestPeerJSONDoesNotExposePresharedKey(t *testing.T) {
	t.Parallel()
	psk := "super-secret-peer-psk"
	p := Peer{PresharedKeyRef: &psk}

	b, err := json.Marshal(p)
	require.NoError(t, err)
	require.NotContains(t, string(b), "preshared_key_ref")
	require.NotContains(t, string(b), psk)
}

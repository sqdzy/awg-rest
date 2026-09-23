package awg

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestParseShowDump_Empty(t *testing.T) {
	t.Parallel()
	const out = "PRIVKEY=\tPUBKEY=\t51820\toff\n"
	// Replace placeholder so it parses cleanly.
	in := strings.ReplaceAll(out, "PRIVKEY=", "(none)")
	in = strings.ReplaceAll(in, "PUBKEY=", "(none)")
	iface, peers, err := ParseShowDump(in)
	require.NoError(t, err)
	require.Equal(t, "", iface.PrivateKey)
	require.Equal(t, "", iface.PublicKey)
	require.Equal(t, 51820, iface.ListenPort)
	require.Empty(t, peers)
}

func TestParseShowDump_WithPeers(t *testing.T) {
	t.Parallel()
	in := strings.Join([]string{
		"sPriv\tsPub\t51820\toff",
		"pPub1\t(none)\t1.2.3.4:51820\t10.0.0.2/32\t1700000000\t1024\t2048\t25",
		"pPub2\tpPSK\t(none)\t10.0.0.3/32,fd00::3/128\t0\t0\t0\toff",
	}, "\n")
	iface, peers, err := ParseShowDump(in)
	require.NoError(t, err)
	require.Equal(t, "sPriv", iface.PrivateKey)
	require.Equal(t, "sPub", iface.PublicKey)
	require.Len(t, peers, 2)

	require.Equal(t, "pPub1", peers[0].PublicKey)
	require.Equal(t, "1.2.3.4:51820", peers[0].Endpoint)
	require.Equal(t, []string{"10.0.0.2/32"}, peers[0].AllowedIPs)
	require.Equal(t, int64(1024), peers[0].RxBytes)
	require.Equal(t, int64(2048), peers[0].TxBytes)
	require.Equal(t, 25, peers[0].KeepaliveSecs)
	require.False(t, peers[0].LastHandshake.IsZero())

	require.Equal(t, "pPub2", peers[1].PublicKey)
	require.Equal(t, "pPSK", peers[1].PresharedKey)
	require.Equal(t, "", peers[1].Endpoint)
	require.Equal(t, []string{"10.0.0.3/32", "fd00::3/128"}, peers[1].AllowedIPs)
	require.True(t, peers[1].LastHandshake.IsZero())
	require.Equal(t, 0, peers[1].KeepaliveSecs)
}

func TestParseShowDump_Malformed(t *testing.T) {
	t.Parallel()
	_, _, err := ParseShowDump("only\ttwo\n")
	require.Error(t, err)
}

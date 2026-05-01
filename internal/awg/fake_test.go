package awg

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestFakeExecutor_SyncConfReplacesPeers(t *testing.T) {
	t.Parallel()
	f := NewFakeExecutor(time.Time{})
	f.Provision("awg0", "PRIV", "PUB", 51820)

	cfg1 := `[Interface]
PrivateKey = PRIV
ListenPort = 51820

[Peer]
PublicKey = pub-A
AllowedIPs = 10.0.0.2/32
PersistentKeepalive = 25
`
	require.NoError(t, f.SyncConf(context.Background(), "awg0", cfg1))
	snap := f.Snapshot("awg0")
	require.Len(t, snap, 1)
	require.Equal(t, "pub-A", snap[0].PublicKey)
	require.Equal(t, 25, snap[0].KeepaliveSecs)

	// SyncConf with a different peer set must REPLACE, not merge.
	cfg2 := `[Interface]
PrivateKey = PRIV
ListenPort = 51820

[Peer]
PublicKey = pub-B
AllowedIPs = 10.0.0.3/32
`
	require.NoError(t, f.SyncConf(context.Background(), "awg0", cfg2))
	snap = f.Snapshot("awg0")
	require.Len(t, snap, 1)
	require.Equal(t, "pub-B", snap[0].PublicKey)
}

func TestFakeExecutor_SetAndRemovePeer(t *testing.T) {
	t.Parallel()
	f := NewFakeExecutor(time.Time{})
	f.Provision("awg0", "PRIV", "PUB", 1)

	require.NoError(t, f.SetPeer(context.Background(), "awg0", PeerSpec{
		PublicKey: "p1", AllowedIPs: []string{"10.0.0.5/32"}, KeepaliveSecs: 25,
	}))
	require.Len(t, f.Snapshot("awg0"), 1)

	require.NoError(t, f.RemovePeer(context.Background(), "awg0", "p1"))
	require.Empty(t, f.Snapshot("awg0"))
}

func TestFakeExecutor_OneShotInjectedFailure(t *testing.T) {
	t.Parallel()
	f := NewFakeExecutor(time.Time{})
	f.Provision("awg0", "PRIV", "PUB", 1)

	boom := errors.New("boom")
	f.FailSyncConf = boom
	err := f.SyncConf(context.Background(), "awg0", "")
	require.ErrorIs(t, err, boom)
	// Second call must succeed (one-shot).
	require.NoError(t, f.SyncConf(context.Background(), "awg0", "[Interface]\nPrivateKey = X\nListenPort = 1\n"))
}

func TestFakeExecutor_ContextCancelled(t *testing.T) {
	t.Parallel()
	f := NewFakeExecutor(time.Time{})
	f.Provision("awg0", "p", "P", 1)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	err := f.SetPeer(ctx, "awg0", PeerSpec{PublicKey: "x"})
	require.ErrorIs(t, err, context.Canceled)
}

func TestFakeExecutor_InterfaceLifecycle(t *testing.T) {
	t.Parallel()
	f := NewFakeExecutor(time.Time{})
	require.NoError(t, f.InterfaceUp(context.Background(), "awg0", "/etc/awg/awg0.conf"))
	require.NoError(t, f.SetPeer(context.Background(), "awg0", PeerSpec{PublicKey: "x"}))
	require.NoError(t, f.InterfaceDown(context.Background(), "awg0"))
	// After down, the interface is gone.
	_, _, err := f.ShowDump(context.Background(), "awg0")
	require.Error(t, err)
}

func TestHashConfig_Stable(t *testing.T) {
	t.Parallel()
	require.Equal(t, HashConfig("a\nb\n"), HashConfig("a\nb\n"))
	require.NotEqual(t, HashConfig("a"), HashConfig("a "))
}

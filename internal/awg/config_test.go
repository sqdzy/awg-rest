package awg

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestPreserveInterfacePrivateKey(t *testing.T) {
	t.Parallel()
	desired := "[Interface]\nListenPort = 51820\n\n[Peer]\nPublicKey = peer\n"
	current := "[Interface]\nPrivateKey = server-private\nListenPort = 51820\n"

	got := PreserveInterfacePrivateKey(desired, current)

	require.Contains(t, got, "[Interface]\nPrivateKey = server-private\nListenPort = 51820")
	require.Equal(t, "server-private", InterfaceValue(got, "privatekey"))
}

func TestPreserveInterfacePrivateKeyKeepsExisting(t *testing.T) {
	t.Parallel()
	desired := "[Interface]\nPrivateKey = desired-private\nListenPort = 51820\n"
	current := "[Interface]\nPrivateKey = current-private\n"

	got := PreserveInterfacePrivateKey(desired, current)

	require.Contains(t, got, "PrivateKey = desired-private")
	require.NotContains(t, got, "current-private")
}

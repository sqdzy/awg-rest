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

func TestSanitizeSyncConfDropsEmptySpecialJunk(t *testing.T) {
	t.Parallel()
	input := "[Interface]\nS4 = 18\nI1 = <packet>\nI2=\nI3 = \nI4 = value\n# I5 = \n\n[Peer]\nPublicKey = peer\n"

	got := sanitizeSyncConf(input)

	require.Contains(t, got, "I1 = <packet>")
	require.Contains(t, got, "I4 = value")
	require.Contains(t, got, "# I5 = ")
	require.NotContains(t, got, "I2=")
	require.NotContains(t, got, "I3 = ")
}

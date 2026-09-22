package bootstrap

import (
	"net/netip"
	"testing"

	"github.com/awg-rest/awg-rest/internal/domain"
	"github.com/stretchr/testify/require"
)

func TestManagedV31ProfileMatchesVerifiedPreset(t *testing.T) {
	t.Parallel()
	p := managedV31Profile(
		"default-v3.1",
		"AQEBAQEBAQEBAQEBAQEBAQEBAQEBAQEBAQEBAQEBAQE=",
	)
	require.NoError(t, p.Validate())
	require.Equal(t, domain.ProtocolV31, p.ProtocolVersion)
	require.Equal(t, 5, p.Jc)
	require.Equal(t, 10, p.Jmin)
	require.Equal(t, 50, p.Jmax)
	require.Equal(t, 12, p.S1)
	require.Equal(t, 12, p.S2)
	require.Equal(t, 12, p.S3)
	require.Equal(t, 12, p.S4)
	require.Equal(t, domain.IntRange{Min: 1, Max: 1}, p.H1)
	require.Equal(t, domain.IntRange{Min: 2, Max: 2}, p.H2)
	require.Equal(t, domain.IntRange{Min: 3, Max: 3}, p.H3)
	require.Equal(t, domain.IntRange{Min: 4, Max: 4}, p.H4)
	require.Equal(t, defaultSpecialJunk1, p.I1)
	require.Equal(t, domain.Uint16Range{Min: 10, Max: 100}, p.ContentPaddingAddition)
	require.Equal(t, domain.Uint16Range{Min: 100, Max: 120}, p.RekeyAfterTime)
	require.Equal(t, domain.Uint16Range{Min: 3, Max: 7}, p.RekeyTimeout)
	require.Equal(t, domain.Uint16Range{Min: 150, Max: 180}, p.RejectAfterTime)
	require.Equal(t, domain.Uint16Range{Min: 5, Max: 15}, p.KeepaliveTimeout)
	require.Equal(t, domain.Uint16Range{Min: 15, Max: 20}, p.MaxHandshakeAttempts)
	require.True(t, p.RandomTrailers)
	require.True(t, p.DisableCookies)
}

func TestValidateManagedV31ProfileRejectsUnsafeDrift(t *testing.T) {
	t.Parallel()
	p := managedV31Profile(
		"default-v3.1",
		"AQEBAQEBAQEBAQEBAQEBAQEBAQEBAQEBAQEBAQEBAQE=",
	)
	p.RandomTrailers = false
	err := validateManagedV31Profile(p)
	require.Error(t, err)
	require.Contains(t, err.Error(), "managed V3.1 preset")
}

func TestEndpointAtPortForcesRolloutPort(t *testing.T) {
	t.Parallel()
	tests := map[string]string{
		"vpn.example.com":       "vpn.example.com:38824",
		"vpn.example.com:38823": "vpn.example.com:38824",
		"203.0.113.10":          "203.0.113.10:38824",
		"[2001:db8::1]:38823":   "[2001:db8::1]:38824",
		"2001:db8::1":           "[2001:db8::1]:38824",
	}
	for in, want := range tests {
		require.Equal(t, want, endpointAtPort(in, 38824), in)
	}
}

func TestPrefixesOverlap(t *testing.T) {
	t.Parallel()
	require.True(t, prefixesOverlap(
		mustPrefix(t, "10.200.0.0/24"),
		mustPrefix(t, "10.200.0.128/25"),
	))
	require.False(t, prefixesOverlap(
		mustPrefix(t, "10.200.0.0/24"),
		mustPrefix(t, "10.201.0.0/24"),
	))
}

func TestEnvV31DefaultsDerivesPlacementFromBase(t *testing.T) {
	base := Defaults{
		TenantSlug:       "acme",
		NodeRegion:       "eu",
		NodeEndpoint:     "vpn.example.test:38823",
		BootstrapConfDir: "/state/bootstrap",
		EnableNAT:        false,
		EgressIface:      "ens3",
	}
	t.Setenv("BOOTSTRAP_V31_ENABLED", "true")
	t.Setenv("BOOTSTRAP_V31_PROFILE_NAME", "")
	t.Setenv("BOOTSTRAP_V31_NODE_HOSTNAME", "")
	t.Setenv("BOOTSTRAP_V31_NODE_IFACE", "")
	t.Setenv("BOOTSTRAP_V31_POOL_CIDR", "")
	t.Setenv("BOOTSTRAP_V31_NODE_BASE_PORT", "")

	got := EnvV31Defaults(base)
	require.True(t, got.Enabled)
	require.Equal(t, "acme", got.TenantSlug)
	require.Equal(t, "eu", got.NodeRegion)
	require.Equal(t, "vpn.example.test:38823", got.NodeEndpoint)
	require.Equal(t, "default-v3.1", got.ProfileName)
	require.Equal(t, "awg-node-31", got.NodeHostname)
	require.Equal(t, "awg31", got.NodeIface)
	require.Equal(t, "10.201.0.0/24", got.PoolCIDR)
	require.Equal(t, defaultV31NodeBasePort, got.NodeBasePort)
	require.Equal(t, "/state/bootstrap", got.BootstrapConfDir)
	require.False(t, got.EnableNAT)
	require.Equal(t, "ens3", got.EgressIface)
}

func mustPrefix(t *testing.T, raw string) netip.Prefix {
	t.Helper()
	p, err := netip.ParsePrefix(raw)
	require.NoError(t, err)
	return p
}

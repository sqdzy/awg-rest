package render

import (
	"strings"
	"testing"

	"github.com/awg-rest/awg-rest/internal/domain"
	"github.com/stretchr/testify/require"
)

func sampleV2Profile() domain.ProtocolProfile {
	return domain.ProtocolProfile{
		Name:            "default-v2",
		ProtocolVersion: domain.ProtocolV2,
		Jc:              5, Jmin: 64, Jmax: 1000,
		S1: 40, S2: 32, S3: 0, S4: 0,
		H1: domain.IntRange{Min: 120000, Max: 320000},
		H2: domain.IntRange{Min: 420000, Max: 620000},
		H3: domain.IntRange{Min: 720000, Max: 920000},
		H4: domain.IntRange{Min: 1020000, Max: 1220000},
		I1: "<packet>",
	}
}

func TestServer_RendersV2Profile(t *testing.T) {
	t.Parallel()
	out := Server(Interface{
		PrivateKey: "AAAA",
		Address:    []string{"10.90.0.1/24"},
		ListenPort: 585,
	}, sampleV2Profile(), []PeerEntry{
		{PublicKey: "PEER2", AllowedIPs: []string{"10.90.0.3/32"}},
		{PublicKey: "PEER1", AllowedIPs: []string{"10.90.0.2/32"}, Keepalive: 25},
	})
	require.Contains(t, out, "[Interface]")
	require.Contains(t, out, "ListenPort = 585")
	require.Contains(t, out, "PrivateKey = AAAA")
	require.Contains(t, out, "Jc = 5")
	require.Contains(t, out, "S3 = 0") // V2 only
	require.Contains(t, out, "H1 = 120000-320000")
	require.Contains(t, out, "I1 = <packet>")
	require.NotContains(t, out, "I2 =")
	require.NotContains(t, out, "I5 =")

	// Peers must be ordered by public key for deterministic hashing.
	idx1 := strings.Index(out, "PublicKey = PEER1")
	idx2 := strings.Index(out, "PublicKey = PEER2")
	require.True(t, idx1 < idx2 && idx1 > 0, "peers must be ordered by public key")
	require.Contains(t, out, "PersistentKeepalive = 25")
}

func TestServer_RendersPeerPresharedKey(t *testing.T) {
	t.Parallel()
	out := Server(Interface{ListenPort: 585}, sampleV2Profile(), []PeerEntry{
		{PublicKey: "PEER1", PresharedKey: "PSK1", AllowedIPs: []string{"10.90.0.2/32"}},
	})
	require.Contains(t, out, "PresharedKey = PSK1")
}

func TestServer_V1OmitsV2Fields(t *testing.T) {
	t.Parallel()
	p := sampleV2Profile()
	p.ProtocolVersion = domain.ProtocolV1
	p.S3, p.S4 = 0, 0
	p.H1 = domain.IntRange{Min: 120000, Max: 120000}
	p.H2 = domain.IntRange{Min: 420000, Max: 420000}
	p.H3 = domain.IntRange{Min: 720000, Max: 720000}
	p.H4 = domain.IntRange{Min: 1020000, Max: 1020000}
	out := Server(Interface{ListenPort: 51820}, p, nil)
	require.NotContains(t, out, "S3 =")
	require.NotContains(t, out, "S4 =")
	require.NotContains(t, out, "I1 =")
}

func TestClient_DefaultAllowedIPs(t *testing.T) {
	t.Parallel()
	out := Client(ClientArgs{
		ClientPrivateKey: "ZZZZ",
		ClientAddress:    []string{"10.90.0.5/32"},
		ServerPublicKey:  "SERV",
		ServerEndpoint:   "vpn.example.com:585",
		PresharedKey:     "PSK1",
		Keepalive:        25,
	}, sampleV2Profile())
	require.Contains(t, out, "AllowedIPs = 0.0.0.0/0, ::/0")
	require.Contains(t, out, "Endpoint = vpn.example.com:585")
	require.Contains(t, out, "PresharedKey = PSK1")
	require.Contains(t, out, "PersistentKeepalive = 25")
	require.Contains(t, out, "Jc = 5")
	require.Contains(t, out, "I1 = <packet>")
	require.Contains(t, out, "I2 = ")
	require.Contains(t, out, "I5 = ")
}

func TestAmneziaClient_ForcesFullTunnelAllowedIPs(t *testing.T) {
	t.Parallel()
	out := AmneziaClient(ClientArgs{
		ClientPrivateKey: "ZZZZ",
		ClientAddress:    []string{"10.90.0.5/32"},
		ServerPublicKey:  "SERV",
		ServerEndpoint:   "vpn.example.com:585",
		PresharedKey:     "PSK1",
		AllowedIPs:       []string{"1.1.1.1/32", "8.8.8.8/32"},
		Keepalive:        25,
	}, sampleV2Profile())

	// AmneziaVPN enables app-level site/app split tunneling for imported AWG/WG
	// configs only when the peer remains full-tunnel. Keep the exact route order:
	// iOS/macOS NetworkExtension checks for this full-tunnel shape before applying
	// split tunneling.
	require.Contains(t, out, "AllowedIPs = 0.0.0.0/0, ::/0\n")
	require.NotContains(t, out, "1.1.1.1/32")
	require.NotContains(t, out, "8.8.8.8/32")
}

func TestServer_StableHash(t *testing.T) {
	t.Parallel()
	args := func() (Interface, []PeerEntry) {
		return Interface{PrivateKey: "AAAA", ListenPort: 585},
			[]PeerEntry{
				{PublicKey: "B", AllowedIPs: []string{"10.0.0.3/32"}},
				{PublicKey: "A", AllowedIPs: []string{"10.0.0.2/32"}},
			}
	}
	i1, p1 := args()
	i2, p2 := args()
	require.Equal(t, Server(i1, sampleV2Profile(), p1), Server(i2, sampleV2Profile(), p2))
}


func sampleV31Profile() domain.ProtocolProfile {
	return domain.ProtocolProfile{
		Name:            "default-v31",
		ProtocolVersion: domain.ProtocolV31,
		Jc: 5, Jmin: 10, Jmax: 50,
		S1: 12, S2: 12, S3: 12, S4: 12,
		H1: domain.IntRange{Min: 1, Max: 1},
		H2: domain.IntRange{Min: 2, Max: 2},
		H3: domain.IntRange{Min: 3, Max: 3},
		H4: domain.IntRange{Min: 4, Max: 4},
		I1: "<packet>",
		HeaderProtectionKey:    "AQEBAQEBAQEBAQEBAQEBAQEBAQEBAQEBAQEBAQEBAQE=",
		ContentPaddingAddition: domain.Uint16Range{Min: 10, Max: 100},
		RekeyAfterTime:         domain.Uint16Range{Min: 100, Max: 120},
		RekeyTimeout:           domain.Uint16Range{Min: 3, Max: 7},
		RejectAfterTime:        domain.Uint16Range{Min: 150, Max: 180},
		KeepaliveTimeout:       domain.Uint16Range{Min: 5, Max: 15},
		MaxHandshakeAttempts:   domain.Uint16Range{Min: 15, Max: 20},
		PersistentKeepalive:     domain.Uint16Range{Min: 25, Max: 35},
		RandomTrailers:         true,
		DisableCookies:         true,
	}
}

func TestClient_RendersV31Profile(t *testing.T) {
	t.Parallel()
	out := Client(ClientArgs{
		ClientPrivateKey: "ZZZZ",
		ClientAddress:    []string{"10.90.0.5/32"},
		ServerPublicKey:  "SERV",
		ServerEndpoint:   "vpn.example.com:585",
	}, sampleV31Profile())

	for _, want := range []string{
		"S3 = 12",
		"H1 = 1",
		"I1 = <packet>",
		"HeaderProtectionKey = AQEBAQEBAQEBAQEBAQEBAQEBAQEBAQEBAQEBAQEBAQE=",
		"ContentPaddingAddition = 10-100",
		"RekeyAfterTime = 100-120",
		"RekeyTimeout = 3-7",
		"RejectAfterTime = 150-180",
		"KeepaliveTimeout = 5-15",
		"MaxHandshakeAttempts = 15-20",
		"PersistentKeepalive = 25-35",
		"RandomTrailers = on",
		"DisableCookies = on",
	} {
		require.Contains(t, out, want)
	}
}

func TestServer_V2DoesNotRenderV31Fields(t *testing.T) {
	t.Parallel()
	out := Server(Interface{ListenPort: 585}, sampleV2Profile(), nil)
	for _, forbidden := range []string{
		"HeaderProtectionKey",
		"ContentPaddingAddition",
		"RekeyAfterTime",
		"RandomTrailers",
		"DisableCookies",
	} {
		require.NotContains(t, out, forbidden)
	}
}

//go:build e2e && linux_awg

package e2e

import (
	"net/netip"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/awg-rest/awg-rest/internal/crypto"
	"github.com/awg-rest/awg-rest/internal/domain"
	"github.com/awg-rest/awg-rest/internal/render"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

// TestRealAWG_ProtocolCompatibility proves that the host AWG runtime accepts
// both legacy V2 and V3.1 profiles and can complete a real encrypted handshake.
// Network namespaces keep the underlay and tunnel routes isolated so traffic
// cannot bypass the AWG interfaces through the host local-routing table.
func TestRealAWG_ProtocolCompatibility(t *testing.T) {
	requireRootAndTools(t)

	t.Run("v2_on_3_1_runtime", func(t *testing.T) {
		profile := domain.ProtocolProfile{
			Name: "real-v2", ProtocolVersion: domain.ProtocolV2,
			Jc: 5, Jmin: 10, Jmax: 50,
			S1: 40, S2: 32, S3: 12, S4: 12,
			H1: domain.IntRange{Min: 1000, Max: 1100},
			H2: domain.IntRange{Min: 2000, Max: 2100},
			H3: domain.IntRange{Min: 3000, Max: 3100},
			H4: domain.IntRange{Min: 4000, Max: 4100},
		}
		require.NoError(t, profile.Validate())
		runRealTunnelCase(t, profile, 51971, "10.251.20.0/30")
	})

	t.Run("v3_1_fixed_headers_random_trailers", func(t *testing.T) {
		headerKey, err := crypto.GenerateKeyPair()
		require.NoError(t, err)
		profile := domain.ProtocolProfile{
			Name: "real-v31", ProtocolVersion: domain.ProtocolV31,
			Jc: 5, Jmin: 10, Jmax: 50,
			S1: 12, S2: 12, S3: 12, S4: 12,
			H1: domain.IntRange{Min: 1, Max: 1},
			H2: domain.IntRange{Min: 2, Max: 2},
			H3: domain.IntRange{Min: 3, Max: 3},
			H4: domain.IntRange{Min: 4, Max: 4},
			I1: "<r 2><b 0x858000010001000000000669636c6f756403636f6d0000010001c00c000100010000105a00044d583737>",
			HeaderProtectionKey:    headerKey.PrivateKey,
			ContentPaddingAddition: domain.Uint16Range{Min: 10, Max: 100},
			RekeyAfterTime:         domain.Uint16Range{Min: 100, Max: 120},
			RekeyTimeout:           domain.Uint16Range{Min: 3, Max: 7},
			RejectAfterTime:        domain.Uint16Range{Min: 150, Max: 180},
			KeepaliveTimeout:       domain.Uint16Range{Min: 5, Max: 15},
			MaxHandshakeAttempts:   domain.Uint16Range{Min: 15, Max: 20},
			RandomTrailers:         true,
			DisableCookies:         true,
		}
		require.NoError(t, profile.Validate())
		runRealTunnelCase(t, profile, 51972, "10.251.31.0/30")
	})
}

func requireRootAndTools(t *testing.T) {
	t.Helper()
	if os.Geteuid() != 0 {
		t.Fatalf("real AWG E2E requires root")
	}
	for _, name := range []string{"ip", "awg", "awg-quick", "ping"} {
		if _, err := exec.LookPath(name); err != nil {
			t.Fatalf("real AWG E2E requires %s in PATH: %v", name, err)
		}
	}
	// A userspace implementation is an accepted fallback when the kernel
	// module is unavailable.
	if _, err := exec.LookPath("amneziawg-go"); err != nil {
		t.Logf("amneziawg-go not found; test requires a working kernel module")
	}
}

func runRealTunnelCase(t *testing.T, profile domain.ProtocolProfile, serverPort int, tunnelCIDR string) {
	t.Helper()

	suffix := strings.ReplaceAll(uuid.New().String()[:6], "-", "")
	serverNS := "awgs-" + suffix
	clientNS := "awgc-" + suffix
	serverVeth := "vs" + suffix
	clientVeth := "vc" + suffix
	const serverIface = "awgs"
	const clientIface = "awgc"

	runHost(t, "ip", "netns", "add", serverNS)
	t.Cleanup(func() { _ = exec.Command("ip", "netns", "del", serverNS).Run() })
	runHost(t, "ip", "netns", "add", clientNS)
	t.Cleanup(func() { _ = exec.Command("ip", "netns", "del", clientNS).Run() })

	runHost(t, "ip", "link", "add", serverVeth, "type", "veth", "peer", "name", clientVeth)
	runHost(t, "ip", "link", "set", serverVeth, "netns", serverNS)
	runHost(t, "ip", "link", "set", clientVeth, "netns", clientNS)
	runNetNS(t, serverNS, "ip", "link", "set", "lo", "up")
	runNetNS(t, clientNS, "ip", "link", "set", "lo", "up")
	runNetNS(t, serverNS, "ip", "addr", "add", "192.0.2.1/30", "dev", serverVeth)
	runNetNS(t, clientNS, "ip", "addr", "add", "192.0.2.2/30", "dev", clientVeth)
	runNetNS(t, serverNS, "ip", "link", "set", serverVeth, "up")
	runNetNS(t, clientNS, "ip", "link", "set", clientVeth, "up")

	prefix := mustPrefix(t, tunnelCIDR)
	serverTunnel := prefix.Addr().Next()
	clientTunnel := serverTunnel.Next()
	require.True(t, prefix.Contains(serverTunnel))
	require.True(t, prefix.Contains(clientTunnel))

	serverKP, err := crypto.GenerateKeyPair()
	require.NoError(t, err)
	clientKP, err := crypto.GenerateKeyPair()
	require.NoError(t, err)
	psk, err := crypto.GeneratePresharedKey()
	require.NoError(t, err)

	serverConfig := render.Server(render.Interface{
		PrivateKey: serverKP.PrivateKey,
		Address:    []string{serverTunnel.String() + "/30"},
		ListenPort: serverPort,
		MTU:        1280,
	}, profile, []render.PeerEntry{{
		PublicKey:    clientKP.PublicKey,
		PresharedKey: psk,
		AllowedIPs:   []string{clientTunnel.String() + "/32"},
	}})

	clientConfig := render.Client(render.ClientArgs{
		ClientPrivateKey: clientKP.PrivateKey,
		ClientAddress:    []string{clientTunnel.String() + "/30"},
		MTU:              1280,
		ServerPublicKey:  serverKP.PublicKey,
		ServerEndpoint:   "192.0.2.1:" + strconv.Itoa(serverPort),
		PresharedKey:     psk,
		AllowedIPs:       []string{serverTunnel.String() + "/32"},
		Keepalive:        1,
	}, profile)

	dir := t.TempDir()
	serverPath := filepath.Join(dir, serverIface+".conf")
	clientPath := filepath.Join(dir, clientIface+".conf")
	require.NoError(t, os.WriteFile(serverPath, []byte(serverConfig), 0o600))
	require.NoError(t, os.WriteFile(clientPath, []byte(clientConfig), 0o600))

	runNetNSEnv(t, serverNS, []string{"WG_QUICK_USERSPACE_IMPLEMENTATION=amneziawg-go"},
		"awg-quick", "up", serverPath)
	t.Cleanup(func() { bestEffortWGQuickDown(t, serverNS, serverPath) })

	runNetNSEnv(t, clientNS, []string{"WG_QUICK_USERSPACE_IMPLEMENTATION=amneziawg-go"},
		"awg-quick", "up", clientPath)
	t.Cleanup(func() { bestEffortWGQuickDown(t, clientNS, clientPath) })

	// A 1200-byte ICMP payload exercises a near-MTU encrypted transport packet
	// in addition to forcing a real handshake.
	runNetNS(t, clientNS, "ping", "-c", "3", "-W", "2", "-s", "1200", serverTunnel.String())

	out := runNetNSOutput(t, serverNS, "awg", "show", serverIface, "latest-handshakes")
	fields := strings.Fields(out)
	require.GreaterOrEqual(t, len(fields), 2, "unexpected latest-handshakes output: %q", out)
	ts, err := strconv.ParseInt(fields[len(fields)-1], 10, 64)
	require.NoError(t, err)
	require.Greater(t, ts, int64(0), "real AWG handshake timestamp must be non-zero")

	// Ensure V3.1 controls survived awg-quick -> awg application rather than
	// merely being accepted by our text renderer.
	if profile.IsV31() {
		show := runNetNSOutputSensitive(t, serverNS, "awg", "showconf", serverIface)
		if !strings.Contains(show, "HeaderProtectionKey = ") {
			t.Fatal("V3.1 runtime did not retain HeaderProtectionKey")
		}
		if !strings.Contains(show, "RandomTrailers = on") {
			t.Fatal("V3.1 runtime did not retain RandomTrailers=on")
		}
		if !strings.Contains(show, "DisableCookies = on") {
			t.Fatal("V3.1 runtime did not retain DisableCookies=on")
		}
	}
}

func mustPrefix(t *testing.T, s string) netip.Prefix {
	t.Helper()
	p, err := netip.ParsePrefix(s)
	require.NoError(t, err)
	return p.Masked()
}

func runHost(t *testing.T, name string, args ...string) {
	t.Helper()
	cmd := exec.Command(name, args...)
	out, err := cmd.CombinedOutput()
	require.NoError(t, err, "%s %v failed: %s", name, args, out)
}

func runNetNS(t *testing.T, ns, name string, args ...string) {
	t.Helper()
	_ = runNetNSOutput(t, ns, name, args...)
}

func runNetNSOutput(t *testing.T, ns, name string, args ...string) string {
	t.Helper()
	all := append([]string{"netns", "exec", ns, name}, args...)
	cmd := exec.Command("ip", all...)
	out, err := cmd.CombinedOutput()
	require.NoError(t, err, "ip %v failed: %s", all, out)
	return string(out)
}

func runNetNSEnv(t *testing.T, ns string, env []string, name string, args ...string) {
	t.Helper()
	all := []string{"netns", "exec", ns, "env"}
	all = append(all, env...)
	all = append(all, name)
	all = append(all, args...)
	cmd := exec.Command("ip", all...)
	out, err := cmd.CombinedOutput()
	require.NoError(t, err, "ip %v failed: %s", all, out)
}


func runNetNSOutputSensitive(t *testing.T, ns, name string, args ...string) string {
	t.Helper()
	all := append([]string{"netns", "exec", ns, name}, args...)
	cmd := exec.Command("ip", all...)
	out, err := cmd.CombinedOutput()
	require.NoError(t, err, "ip %v failed (sensitive output suppressed)", all)
	return string(out)
}

func bestEffortWGQuickDown(t *testing.T, ns, configPath string) {
	t.Helper()
	cmd := exec.Command(
		"ip", "netns", "exec", ns, "env",
		"WG_QUICK_USERSPACE_IMPLEMENTATION=amneziawg-go",
		"awg-quick", "down", configPath,
	)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Logf("best-effort awg-quick down failed in namespace %s: %v (%s)", ns, err, strings.TrimSpace(string(out)))
	}
}

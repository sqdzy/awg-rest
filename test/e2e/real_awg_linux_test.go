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
	"time"

	"github.com/awg-rest/awg-rest/internal/crypto"
	"github.com/awg-rest/awg-rest/internal/domain"
	"github.com/awg-rest/awg-rest/internal/render"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

// TestRealAWG_ProtocolCompatibility proves that the exact amneziawg-go runtime
// built by deploy/docker/Dockerfile.all-in-one can establish encrypted tunnels
// for both legacy V2 and V3.1 profiles.
//
// The test intentionally launches amneziawg-go directly instead of relying on
// awg-quick interface creation: awg-quick prefers an installed kernel module,
// which could otherwise make this gate test a different implementation than the
// userspace binary shipped in the all-in-one image.
func TestRealAWG_ProtocolCompatibility(t *testing.T) {
	requireRootAndTools(t)

	t.Run("v2_on_3_1_userspace_runtime", func(t *testing.T) {
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
		headerKey, err := crypto.GeneratePresharedKey()
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
			HeaderProtectionKey:    headerKey,
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
	for _, name := range []string{"ip", "awg", "awg-quick", "amneziawg-go", "ping"} {
		if _, err := exec.LookPath(name); err != nil {
			t.Fatalf("real AWG E2E requires %s in PATH: %v", name, err)
		}
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

	createNamespace(t, serverNS)
	createNamespace(t, clientNS)
	t.Cleanup(func() { cleanupNamespace(serverNS) })
	t.Cleanup(func() { cleanupNamespace(clientNS) })

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
	serverFull := writeSecretConfig(t, dir, serverIface+"-full.conf", serverConfig)
	clientFull := writeSecretConfig(t, dir, clientIface+"-full.conf", clientConfig)
	serverSetconf := writeSecretConfig(t, dir, serverIface+"-setconf.conf", stripQuickConfig(t, serverFull))
	clientSetconf := writeSecretConfig(t, dir, clientIface+"-setconf.conf", stripQuickConfig(t, clientFull))

	startUserspaceInterface(t, serverNS, serverIface, serverTunnel.String()+"/30", serverSetconf)
	startUserspaceInterface(t, clientNS, clientIface, clientTunnel.String()+"/30", clientSetconf)

	// A 1200-byte ICMP payload exercises a near-MTU encrypted transport packet
	// in addition to forcing a real handshake.
	runNetNS(t, clientNS, "ping", "-c", "3", "-W", "2", "-s", "1200", serverTunnel.String())

	out := runNetNSOutput(t, serverNS, "awg", "show", serverIface, "latest-handshakes")
	fields := strings.Fields(out)
	require.GreaterOrEqual(t, len(fields), 2, "unexpected latest-handshakes output: %q", out)
	ts, err := strconv.ParseInt(fields[len(fields)-1], 10, 64)
	require.NoError(t, err)
	require.Greater(t, ts, int64(0), "real AWG handshake timestamp must be non-zero")

	if profile.IsV31() {
		show := runNetNSOutputSensitive(t, serverNS, "awg", "showconf", serverIface)
		require.Contains(t, show, "HeaderProtectionKey = ", "V3.1 runtime must retain HeaderProtectionKey")
		require.Contains(t, show, "RandomTrailers = on", "V3.1 runtime must retain RandomTrailers=on")
		require.Contains(t, show, "DisableCookies = on", "V3.1 runtime must retain DisableCookies=on")
		require.Contains(t, show, "ContentPaddingAddition = 10-100")
	}
}

func createNamespace(t *testing.T, ns string) {
	t.Helper()
	runHost(t, "ip", "netns", "add", ns)
}

func cleanupNamespace(ns string) {
	// amneziawg-go daemonizes inside the namespace. Kill any remaining process
	// before deleting the namespace so a failed test cannot leak a TUN daemon.
	if out, err := exec.Command("ip", "netns", "pids", ns).Output(); err == nil {
		for _, raw := range strings.Fields(string(out)) {
			pid, err := strconv.Atoi(raw)
			if err == nil {
				_ = exec.Command("kill", "-TERM", strconv.Itoa(pid)).Run()
			}
		}
	}
	_ = exec.Command("ip", "netns", "del", ns).Run()
}

func startUserspaceInterface(t *testing.T, ns, iface, address, setconfPath string) {
	t.Helper()

	// Force the exact amneziawg-go binary from PATH. Do not call awg-quick up:
	// it would prefer a host kernel module if one is installed.
	runNetNSEnv(t, ns, []string{"LOG_LEVEL=silent"}, "amneziawg-go", iface)
	waitForLink(t, ns, iface)

	runNetNSSensitive(t, ns, "awg", "setconf", iface, setconfPath)
	runNetNS(t, ns, "ip", "addr", "add", address, "dev", iface)
	runNetNS(t, ns, "ip", "link", "set", "dev", iface, "mtu", "1280", "up")
}

func waitForLink(t *testing.T, ns, iface string) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		cmd := exec.Command("ip", "netns", "exec", ns, "ip", "link", "show", "dev", iface)
		if err := cmd.Run(); err == nil {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("userspace AWG interface %s did not appear in namespace %s", iface, ns)
		}
		time.Sleep(50 * time.Millisecond)
	}
}

func stripQuickConfig(t *testing.T, path string) string {
	t.Helper()
	cmd := exec.Command("awg-quick", "strip", path)
	out, err := cmd.Output()
	require.NoError(t, err, "awg-quick strip failed for secret config (output suppressed)")

	// Client rendering intentionally includes empty I1-I5 keys for import
	// compatibility, while awg setconf rejects empty special-junk values.
	var b strings.Builder
	for _, raw := range strings.SplitAfter(string(out), "
") {
		line := strings.TrimSpace(raw)
		key, value, ok := strings.Cut(line, "=")
		if ok && strings.TrimSpace(value) == "" {
			switch strings.ToUpper(strings.TrimSpace(key)) {
			case "I1", "I2", "I3", "I4", "I5":
				continue
			}
		}
		b.WriteString(raw)
	}
	return b.String()
}

func writeSecretConfig(t *testing.T, dir, name, content string) string {
	t.Helper()
	path := filepath.Join(dir, name)
	require.NoError(t, os.WriteFile(path, []byte(content), 0o600))
	return path
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

func runNetNSSensitive(t *testing.T, ns, name string, args ...string) {
	t.Helper()
	all := append([]string{"netns", "exec", ns, name}, args...)
	cmd := exec.Command("ip", all...)
	if _, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("ip netns exec %s %s failed (sensitive output suppressed): %v", ns, name, err)
	}
}

func runNetNSOutputSensitive(t *testing.T, ns, name string, args ...string) string {
	t.Helper()
	all := append([]string{"netns", "exec", ns, name}, args...)
	cmd := exec.Command("ip", all...)
	out, err := cmd.CombinedOutput()
	require.NoError(t, err, "ip %v failed (sensitive output suppressed)", all)
	return string(out)
}

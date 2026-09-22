//go:build e2e && linux_awg

package e2e

import (
	"fmt"
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
			// Aggressive timings are test-only so rekey is observable within
			// the CI timeout; production profiles can use wider defaults.
			RekeyAfterTime:         domain.Uint16Range{Min: 1, Max: 1},
			RekeyTimeout:           domain.Uint16Range{Min: 1, Max: 1},
			RejectAfterTime:        domain.Uint16Range{Min: 10, Max: 10},
			KeepaliveTimeout:       domain.Uint16Range{Min: 1, Max: 1},
			MaxHandshakeAttempts:   domain.Uint16Range{Min: 5, Max: 5},
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
	for _, name := range []string{"ip", "awg", "awg-quick", "amneziawg-go", "ping", "python3"} {
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
	initialHandshake := latestHandshake(t, serverNS, serverIface)
	require.Greater(t, initialHandshake, int64(0), "real AWG handshake timestamp must be non-zero")

	// Exercise application traffic in both common transport modes, not only ICMP.
	runPythonEcho(t, dir, serverNS, clientNS, "tcp", serverTunnel.String(), 52101)
	runPythonEcho(t, dir, serverNS, clientNS, "udp", serverTunnel.String(), 52102)

	if profile.IsV31() {
		show := runNetNSOutputSensitive(t, serverNS, "awg", "showconf", serverIface)
		require.Contains(t, show, "HeaderProtectionKey = ", "V3.1 runtime must retain HeaderProtectionKey")
		require.Contains(t, show, "RandomTrailers = on", "V3.1 runtime must retain RandomTrailers=on")
		require.Contains(t, show, "DisableCookies = on", "V3.1 runtime must retain DisableCookies=on")
		require.Contains(t, show, "ContentPaddingAddition = 10-100")

		// RekeyAfterTime is 1 second in this test profile. Keep sending real
		// traffic until the server observes a newer handshake.
		rekeyed := waitForNewHandshake(t, clientNS, serverNS, clientIface, serverIface, serverTunnel.String(), initialHandshake)
		require.Greater(t, rekeyed, initialHandshake, "V3.1 tunnel must rekey")

		// Destroy and recreate the client userspace TUN process. Successful
		// traffic afterwards necessarily requires a fresh handshake.
		runNetNS(t, clientNS, "ip", "link", "del", clientIface)
		waitForLinkGone(t, clientNS, clientIface)
		startUserspaceInterface(t, clientNS, clientIface, clientTunnel.String()+"/30", clientSetconf)
		runNetNS(t, clientNS, "ping", "-c", "2", "-W", "2", serverTunnel.String())
		require.Greater(t, latestHandshake(t, clientNS, clientIface), int64(0),
			"recreated client must complete a fresh handshake")
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
	for _, raw := range strings.SplitAfter(string(out), "\n") {
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

func latestHandshake(t *testing.T, ns, iface string) int64 {
	t.Helper()
	out := runNetNSOutput(t, ns, "awg", "show", iface, "latest-handshakes")
	fields := strings.Fields(out)
	require.GreaterOrEqual(t, len(fields), 2, "unexpected latest-handshakes output: %q", out)
	ts, err := strconv.ParseInt(fields[len(fields)-1], 10, 64)
	require.NoError(t, err)
	return ts
}

func waitForNewHandshake(t *testing.T, clientNS, serverNS, clientIface, serverIface, target string, previous int64) int64 {
	t.Helper()
	deadline := time.Now().Add(8 * time.Second)
	for time.Now().Before(deadline) {
		runNetNS(t, clientNS, "ping", "-c", "1", "-W", "1", target)
		if ts := latestHandshake(t, serverNS, serverIface); ts > previous {
			return ts
		}
		time.Sleep(250 * time.Millisecond)
	}
	t.Fatalf("handshake did not advance after rekey window (client iface %s)", clientIface)
	return 0
}

func waitForLinkGone(t *testing.T, ns, iface string) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		cmd := exec.Command("ip", "netns", "exec", ns, "ip", "link", "show", "dev", iface)
		if err := cmd.Run(); err != nil {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("userspace AWG interface %s did not disappear in namespace %s", iface, ns)
		}
		time.Sleep(50 * time.Millisecond)
	}
}

func runPythonEcho(t *testing.T, dir, serverNS, clientNS, network, host string, port int) {
	t.Helper()
	ready := filepath.Join(dir, fmt.Sprintf("%s-%d.ready", network, port))
	_ = os.Remove(ready)

	var serverScript, clientScript string
	switch network {
	case "tcp":
		serverScript = `import socket,sys
s=socket.socket(socket.AF_INET,socket.SOCK_STREAM)
s.setsockopt(socket.SOL_SOCKET,socket.SO_REUSEADDR,1)
s.bind((sys.argv[1],int(sys.argv[2])))
s.listen(1)
open(sys.argv[3],"w").close()
c,_=s.accept()
d=c.recv(4096)
c.sendall(d)
c.close()
s.close()
`
		clientScript = `import socket,sys
p=b"awg-rest-tcp"
s=socket.socket(socket.AF_INET,socket.SOCK_STREAM)
s.settimeout(3)
s.connect((sys.argv[1],int(sys.argv[2])))
s.sendall(p)
d=s.recv(4096)
s.close()
raise SystemExit(0 if d==p else 2)
`
	case "udp":
		serverScript = `import socket,sys
s=socket.socket(socket.AF_INET,socket.SOCK_DGRAM)
s.bind((sys.argv[1],int(sys.argv[2])))
open(sys.argv[3],"w").close()
d,a=s.recvfrom(4096)
s.sendto(d,a)
s.close()
`
		clientScript = `import socket,sys
p=b"awg-rest-udp"
s=socket.socket(socket.AF_INET,socket.SOCK_DGRAM)
s.settimeout(3)
s.sendto(p,(sys.argv[1],int(sys.argv[2])))
d,_=s.recvfrom(4096)
s.close()
raise SystemExit(0 if d==p else 2)
`
	default:
		t.Fatalf("unsupported echo network %q", network)
	}

	serverArgs := []string{"netns", "exec", serverNS, "python3", "-c", serverScript, host, strconv.Itoa(port), ready}
	server := exec.Command("ip", serverArgs...)
	if err := server.Start(); err != nil {
		t.Fatalf("start %s echo server: %v", network, err)
	}
	t.Cleanup(func() {
		if server.Process != nil {
			_ = server.Process.Kill()
		}
		_ = server.Wait()
	})
	waitForFile(t, ready)

	runNetNS(t, clientNS, "python3", "-c", clientScript, host, strconv.Itoa(port))
	require.NoError(t, server.Wait(), "%s echo server failed", network)
	server.Process = nil
}

func waitForFile(t *testing.T, path string) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for {
		if _, err := os.Stat(path); err == nil {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for readiness file %s", filepath.Base(path))
		}
		time.Sleep(25 * time.Millisecond)
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

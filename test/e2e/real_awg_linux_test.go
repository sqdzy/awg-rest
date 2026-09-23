//go:build e2e && linux_awg

package e2e

import (
	"bytes"
	"io"
	"net"
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

// TestRealAWG_ProtocolCompatibility proves that the exact userspace AWG runtime
// supplied by the all-in-one image accepts both legacy V2 and V3.1 profiles and
// completes real encrypted data transfer. Network namespaces isolate the
// underlay and tunnel routes so traffic cannot bypass AWG through host routing.
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
		runRealTunnelCase(t, profile, 51971, "10.251.20.0/30", false)
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
			// Deliberately short timings make a real rekey observable without
			// turning the release gate into a multi-minute test.
			RekeyAfterTime:       domain.Uint16Range{Min: 2, Max: 3},
			RekeyTimeout:         domain.Uint16Range{Min: 1, Max: 1},
			RejectAfterTime:      domain.Uint16Range{Min: 8, Max: 10},
			KeepaliveTimeout:     domain.Uint16Range{Min: 1, Max: 2},
			MaxHandshakeAttempts: domain.Uint16Range{Min: 5, Max: 7},
			RandomTrailers:       true,
			DisableCookies:       true,
		}
		require.NoError(t, profile.Validate())
		runRealTunnelCase(t, profile, 51972, "10.251.31.0/30", true)
	})
}

func requireRootAndTools(t *testing.T) {
	t.Helper()
	if os.Geteuid() != 0 {
		t.Fatalf("real AWG E2E requires root")
	}
	for _, name := range []string{"ip", "awg", "amneziawg-go", "ping"} {
		if _, err := exec.LookPath(name); err != nil {
			t.Fatalf("real AWG E2E requires %s in PATH: %v", name, err)
		}
	}
}

func runRealTunnelCase(t *testing.T, profile domain.ProtocolProfile, serverPort int, tunnelCIDR string, verifyV31Lifecycle bool) {
	t.Helper()

	suffix := uuid.New().String()[:6]
	serverNS := "awgs-" + suffix
	clientNS := "awgc-" + suffix
	serverVeth := "vs" + suffix
	clientVeth := "vc" + suffix
	serverIface := "as" + suffix
	clientIface := "ac" + suffix

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
	serverPath := filepath.Join(dir, serverIface+".setconf")
	clientPath := filepath.Join(dir, clientIface+".setconf")
	require.NoError(t, os.WriteFile(serverPath, []byte(stripQuickOnlyFields(serverConfig)), 0o600))
	require.NoError(t, os.WriteFile(clientPath, []byte(stripQuickOnlyFields(clientConfig)), 0o600))

	startUserspaceInterface(t, serverNS, serverIface, serverPath, serverTunnel.String()+"/30", clientTunnel.String()+"/32")
	startUserspaceInterface(t, clientNS, clientIface, clientPath, clientTunnel.String()+"/30", serverTunnel.String()+"/32")

	// Near-MTU ICMP forces a real handshake and transport packet.
	runNetNS(t, clientNS, "ping", "-c", "3", "-W", "2", "-s", "1200", serverTunnel.String())
	firstHandshake := handshakeTimestamp(t, serverNS, serverIface)
	require.Greater(t, firstHandshake, int64(0))

	assertTCPUDPTransfer(t, serverNS, clientNS, serverTunnel.String())

	if profile.IsV31() {
		show := runNetNSOutputSensitive(t, serverNS, "awg", "showconf", serverIface)
		require.Contains(t, show, "HeaderProtectionKey = ")
		require.Contains(t, show, "RandomTrailers = on")
		require.Contains(t, show, "DisableCookies = on")
	}

	if !verifyV31Lifecycle {
		return
	}

	// Rekey: the test profile uses a 2-3 second RekeyAfterTime. Keep sending
	// traffic until the server observes a newer handshake timestamp.
	rekeyed := waitForNewHandshake(t, clientNS, serverNS, clientIface, serverIface, serverTunnel.String(), firstHandshake, 12*time.Second)
	require.Greater(t, rekeyed, firstHandshake, "V3.1 session did not rekey")

	// Reconnect: destroy the client userspace interface, recreate it from the
	// same config, then require a fresh handshake and data transfer.
	deleteUserspaceInterface(t, clientNS, clientIface)
	time.Sleep(1100 * time.Millisecond)
	startUserspaceInterface(t, clientNS, clientIface, clientPath, clientTunnel.String()+"/30", serverTunnel.String()+"/32")
	runNetNS(t, clientNS, "ping", "-c", "2", "-W", "2", serverTunnel.String())
	reconnected := waitForNewHandshake(t, clientNS, serverNS, clientIface, serverIface, serverTunnel.String(), rekeyed, 8*time.Second)
	require.Greater(t, reconnected, rekeyed, "V3.1 client restart did not establish a fresh handshake")
	assertTCPUDPTransfer(t, serverNS, clientNS, serverTunnel.String())
}

func stripQuickOnlyFields(config string) string {
	var b strings.Builder
	for _, line := range strings.Split(config, "\n") {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "Address =") ||
			strings.HasPrefix(trimmed, "DNS =") ||
			strings.HasPrefix(trimmed, "MTU =") ||
			strings.HasPrefix(trimmed, "Table =") ||
			strings.HasPrefix(trimmed, "PreUp =") ||
			strings.HasPrefix(trimmed, "PostUp =") ||
			strings.HasPrefix(trimmed, "PreDown =") ||
			strings.HasPrefix(trimmed, "PostDown =") {
			continue
		}
		// render.Client intentionally emits empty I1-I5 keys for importable
		// client configs; raw awg setconf rejects those empty values. Production
		// SyncConf applies the same normalization after awg-quick strip.
		if key, value, ok := strings.Cut(trimmed, "="); ok &&
			strings.TrimSpace(value) == "" {
			switch strings.ToUpper(strings.TrimSpace(key)) {
			case "I1", "I2", "I3", "I4", "I5":
				continue
			}
		}
		b.WriteString(line)
		b.WriteByte('\n')
	}
	return b.String()
}

func startUserspaceInterface(t *testing.T, ns, iface, configPath, addressCIDR, routeCIDR string) {
	t.Helper()
	runNetNSSensitive(t, ns, "amneziawg-go", iface)
	waitForLink(t, ns, iface, 3*time.Second)
	t.Cleanup(func() { deleteUserspaceInterfaceBestEffort(t, ns, iface) })

	// setconf input contains private/PSK/header-protection key material; suppress
	// command output on failure so CI logs cannot leak those values.
	runNetNSSensitive(t, ns, "awg", "setconf", iface, configPath)
	runNetNS(t, ns, "ip", "addr", "add", addressCIDR, "dev", iface)
	runNetNS(t, ns, "ip", "link", "set", "mtu", "1280", "up", "dev", iface)
	runNetNS(t, ns, "ip", "route", "replace", routeCIDR, "dev", iface)
}

func waitForLink(t *testing.T, ns, iface string, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		cmd := exec.Command("ip", "netns", "exec", ns, "ip", "link", "show", "dev", iface)
		if err := cmd.Run(); err == nil {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("userspace AWG interface %s did not appear in namespace %s", iface, ns)
}

func deleteUserspaceInterface(t *testing.T, ns, iface string) {
	t.Helper()
	cmd := exec.Command("ip", "netns", "exec", ns, "ip", "link", "del", "dev", iface)
	out, err := cmd.CombinedOutput()
	require.NoError(t, err, "delete userspace interface %s: %s", iface, out)
	_ = exec.Command("rm", "-f", "/var/run/amneziawg/"+iface+".sock").Run()
}

func deleteUserspaceInterfaceBestEffort(t *testing.T, ns, iface string) {
	t.Helper()
	if out, err := exec.Command("ip", "netns", "exec", ns, "ip", "link", "del", "dev", iface).CombinedOutput(); err != nil {
		t.Logf("best-effort interface cleanup failed for %s/%s: %v (%s)", ns, iface, err, strings.TrimSpace(string(out)))
	}
	_ = exec.Command("rm", "-f", "/var/run/amneziawg/"+iface+".sock").Run()
}

func handshakeTimestamp(t *testing.T, ns, iface string) int64 {
	t.Helper()
	out := runNetNSOutput(t, ns, "awg", "show", iface, "latest-handshakes")
	fields := strings.Fields(out)
	require.GreaterOrEqual(t, len(fields), 2, "unexpected latest-handshakes output: %q", out)
	ts, err := strconv.ParseInt(fields[len(fields)-1], 10, 64)
	require.NoError(t, err)
	return ts
}

func waitForNewHandshake(t *testing.T, clientNS, serverNS, clientIface, serverIface, serverTunnel string, after int64, timeout time.Duration) int64 {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		// Keep the session active so RekeyAfterTime has outgoing traffic to act on.
		_ = exec.Command("ip", "netns", "exec", clientNS, "ping", "-c", "1", "-W", "1", serverTunnel).Run()
		ts := handshakeTimestamp(t, serverNS, serverIface)
		if ts > after {
			return ts
		}
		time.Sleep(500 * time.Millisecond)
	}
	return handshakeTimestamp(t, serverNS, serverIface)
}

// TestAWGNetProbeHelper is launched as a subprocess under a network namespace by
// assertTCPUDPTransfer. It is skipped during ordinary test enumeration.
func TestAWGNetProbeHelper(t *testing.T) {
	role := os.Getenv("AWG_NETPROBE_ROLE")
	if role == "" {
		t.Skip("network helper subprocess only")
	}
	addr := os.Getenv("AWG_NETPROBE_ADDR")
	switch role {
	case "server":
		runNetProbeServer(t, addr, os.Getenv("AWG_NETPROBE_READY"))
	case "client":
		runNetProbeClient(t, addr)
	default:
		t.Fatalf("unknown AWG_NETPROBE_ROLE %q", role)
	}
}

func runNetProbeServer(t *testing.T, addr, ready string) {
	t.Helper()
	tcpLn, err := net.Listen("tcp", addr)
	require.NoError(t, err)
	defer tcpLn.Close()
	udpConn, err := net.ListenPacket("udp", addr)
	require.NoError(t, err)
	defer udpConn.Close()

	require.NoError(t, os.WriteFile(ready, []byte("ready"), 0o600))

	errCh := make(chan error, 2)
	go func() {
		conn, err := tcpLn.Accept()
		if err != nil {
			errCh <- err
			return
		}
		defer conn.Close()
		_ = conn.SetDeadline(time.Now().Add(5 * time.Second))
		buf := make([]byte, len("awg-netprobe"))
		if _, err := io.ReadFull(conn, buf); err != nil {
			errCh <- err
			return
		}
		if _, err := conn.Write(buf); err != nil {
			errCh <- err
			return
		}
		errCh <- nil
	}()
	go func() {
		_ = udpConn.SetDeadline(time.Now().Add(5 * time.Second))
		buf := make([]byte, 64)
		n, remote, err := udpConn.ReadFrom(buf)
		if err != nil {
			errCh <- err
			return
		}
		_, err = udpConn.WriteTo(buf[:n], remote)
		errCh <- err
	}()

	for range 2 {
		require.NoError(t, <-errCh)
	}
}

func runNetProbeClient(t *testing.T, addr string) {
	t.Helper()
	const payload = "awg-netprobe"

	tcpConn, err := net.DialTimeout("tcp", addr, 3*time.Second)
	require.NoError(t, err)
	_ = tcpConn.SetDeadline(time.Now().Add(3 * time.Second))
	_, err = tcpConn.Write([]byte(payload))
	require.NoError(t, err)
	tcpReply := make([]byte, len(payload))
	_, err = io.ReadFull(tcpConn, tcpReply)
	require.NoError(t, err)
	require.Equal(t, payload, string(tcpReply))
	require.NoError(t, tcpConn.Close())

	udpConn, err := net.DialTimeout("udp", addr, 3*time.Second)
	require.NoError(t, err)
	_ = udpConn.SetDeadline(time.Now().Add(3 * time.Second))
	_, err = udpConn.Write([]byte(payload))
	require.NoError(t, err)
	udpReply := make([]byte, len(payload))
	n, err := udpConn.Read(udpReply)
	require.NoError(t, err)
	require.Equal(t, payload, string(udpReply[:n]))
	require.NoError(t, udpConn.Close())
}

func assertTCPUDPTransfer(t *testing.T, serverNS, clientNS, serverTunnel string) {
	t.Helper()
	exe, err := os.Executable()
	require.NoError(t, err)
	ready := filepath.Join(t.TempDir(), "netprobe.ready")
	addr := net.JoinHostPort(serverTunnel, "43210")

	serverArgs := []string{
		"netns", "exec", serverNS, "env",
		"AWG_NETPROBE_ROLE=server",
		"AWG_NETPROBE_ADDR=" + addr,
		"AWG_NETPROBE_READY=" + ready,
		exe, "-test.run=^TestAWGNetProbeHelper$", "-test.count=1",
	}
	serverCmd := exec.Command("ip", serverArgs...)
	var serverOutput bytes.Buffer
	serverCmd.Stdout = &serverOutput
	serverCmd.Stderr = &serverOutput
	require.NoError(t, serverCmd.Start())
	t.Cleanup(func() {
		if serverCmd.Process != nil {
			_ = serverCmd.Process.Kill()
		}
	})

	deadline := time.Now().Add(5 * time.Second)
	for {
		if _, err := os.Stat(ready); err == nil {
			break
		}
		if time.Now().After(deadline) {
			_ = serverCmd.Process.Kill()
			_ = serverCmd.Wait()
			t.Fatalf("network helper server did not become ready: %s", serverOutput.String())
		}
		time.Sleep(50 * time.Millisecond)
	}

	clientArgs := []string{
		"netns", "exec", clientNS, "env",
		"AWG_NETPROBE_ROLE=client",
		"AWG_NETPROBE_ADDR=" + addr,
		exe, "-test.run=^TestAWGNetProbeHelper$", "-test.count=1",
	}
	clientOut, err := exec.Command("ip", clientArgs...).CombinedOutput()
	require.NoError(t, err, "network helper client failed: %s", clientOut)
	require.NoError(t, serverCmd.Wait(), "network helper server failed: %s", serverOutput.String())
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

func runNetNSSensitive(t *testing.T, ns, name string, args ...string) {
	t.Helper()
	all := append([]string{"netns", "exec", ns, name}, args...)
	cmd := exec.Command("ip", all...)
	if _, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("sensitive command failed: ip netns exec %s %s: %v", ns, name, err)
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

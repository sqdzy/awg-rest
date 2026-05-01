package nodeagent

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/awg-rest/awg-rest/internal/awg"
	"github.com/awg-rest/awg-rest/internal/obs"
	"github.com/stretchr/testify/require"
)

// TestAgent_HTTPProtocol verifies the agent server and remote executor wire
// up against the FakeExecutor end-to-end, including syncconf, set/remove peer,
// and dump.
func TestAgent_HTTPProtocol(t *testing.T) {
	t.Parallel()
	exec := awg.NewFakeExecutor(time.Time{})
	exec.Provision("awg0", "PRIV", "PUB", 51820)

	srv, err := NewServer(ServerConfig{
		Addr:              ":0",
		Executor:          exec,
		Logger:            obs.NewLogger("error", false),
		AllowInsecureHTTP: true,
	})
	require.NoError(t, err)

	ts := httptest.NewServer(srv.Handler)
	defer ts.Close()

	client, err := NewInsecureRemoteExecutor(ts.URL)
	require.NoError(t, err)

	ctx := context.Background()

	// SyncConf round-trip.
	cfg := `[Interface]
PrivateKey = PRIV
ListenPort = 51820

[Peer]
PublicKey = pX
PresharedKey = PSK
AllowedIPs = 10.0.0.5/32
PersistentKeepalive = 25
`
	require.NoError(t, client.SyncConf(ctx, "awg0", cfg))

	// Dump shows the peer.
	iface, peers, err := client.ShowDump(ctx, "awg0")
	require.NoError(t, err)
	require.Empty(t, iface.PrivateKey, "node-agent dump must not expose interface private keys")
	require.Equal(t, 51820, iface.ListenPort)
	require.Len(t, peers, 1)
	require.Equal(t, "pX", peers[0].PublicKey)

	// Set a new peer; remove the original.
	require.NoError(t, client.SetPeer(ctx, "awg0", awg.PeerSpec{
		PublicKey: "pY", AllowedIPs: []string{"10.0.0.6/32"}, KeepaliveSecs: 30,
	}))
	require.NoError(t, client.RemovePeer(ctx, "awg0", "pX"))
	_, peers, err = client.ShowDump(ctx, "awg0")
	require.NoError(t, err)
	require.Len(t, peers, 1)
	require.Equal(t, "pY", peers[0].PublicKey)

	// ShowConf returns whatever was last applied.
	conf, err := client.ShowConf(ctx, "awg0")
	require.NoError(t, err)
	require.Contains(t, conf, "[Interface]")
	require.Contains(t, conf, "PrivateKey = (redacted)")
	require.Contains(t, conf, "PresharedKey = (redacted)")
	require.NotContains(t, conf, "PrivateKey = PRIV")
	require.NotContains(t, conf, "PresharedKey = PSK")
}

func TestAgent_PropagatesError(t *testing.T) {
	t.Parallel()
	exec := awg.NewFakeExecutor(time.Time{})
	exec.Provision("awg0", "p", "P", 1)
	exec.FailSyncConf = errStub("boom")

	srv, err := NewServer(ServerConfig{
		Addr:              ":0",
		Executor:          exec,
		Logger:            obs.NewLogger("error", false),
		AllowInsecureHTTP: true,
	})
	require.NoError(t, err)
	ts := httptest.NewServer(srv.Handler)
	defer ts.Close()

	client, err := NewInsecureRemoteExecutor(ts.URL)
	require.NoError(t, err)
	err = client.SyncConf(context.Background(), "awg0", "")
	require.Error(t, err)
	require.Contains(t, err.Error(), "boom")
}

func TestAgent_RejectsInvalidInterfaceName(t *testing.T) {
	t.Parallel()
	exec := awg.NewFakeExecutor(time.Time{})
	srv, err := NewServer(ServerConfig{
		Addr:              ":0",
		Executor:          exec,
		Logger:            obs.NewLogger("error", false),
		AllowInsecureHTTP: true,
	})
	require.NoError(t, err)
	ts := httptest.NewServer(srv.Handler)
	defer ts.Close()

	resp, err := ts.Client().Get(ts.URL + "/v1/iface/awg$bad/dump")
	require.NoError(t, err)
	defer resp.Body.Close()
	require.Equal(t, http.StatusBadRequest, resp.StatusCode)
}

func TestServer_RequiresTLSOrExplicitInsecure(t *testing.T) {
	t.Parallel()
	exec := awg.NewFakeExecutor(time.Time{})
	_, err := NewServer(ServerConfig{Addr: ":0", Executor: exec, Logger: obs.NewLogger("error", false)})
	require.Error(t, err)
	require.Contains(t, err.Error(), "plain HTTP is disabled")
}

func TestServer_RequiresCompleteMTLSConfig(t *testing.T) {
	t.Parallel()
	exec := awg.NewFakeExecutor(time.Time{})
	_, err := NewServer(ServerConfig{
		Addr:        ":0",
		Executor:    exec,
		Logger:      obs.NewLogger("error", false),
		TLSCertFile: "server.crt",
		TLSKeyFile:  "server.key",
	})
	require.Error(t, err)
	require.Contains(t, err.Error(), "AGENT_CLIENT_CA_BUNDLE")
}

func TestRemoteExecutor_RequiresProductionMTLS(t *testing.T) {
	t.Parallel()
	_, err := NewRemoteExecutor("http://127.0.0.1:8081", "", "", "")
	require.Error(t, err)
	require.Contains(t, err.Error(), "requires https")

	_, err = NewRemoteExecutor("https://agent.local:8081", "", "", "")
	require.Error(t, err)
	require.Contains(t, err.Error(), "mTLS")

	_, err = NewInsecureRemoteExecutor("http://127.0.0.1:8081")
	require.NoError(t, err)
}

func TestAgent_MTLSRoundTrip(t *testing.T) {
	t.Parallel()
	files := writeTestMTLSFiles(t)
	exec := awg.NewFakeExecutor(time.Time{})
	exec.Provision("awg0", "PRIV", "PUB", 51820)

	srv, err := NewServer(ServerConfig{
		Addr:        ":0",
		Executor:    exec,
		Logger:      obs.NewLogger("error", false),
		TLSCertFile: files.serverCert,
		TLSKeyFile:  files.serverKey,
		ClientCAs:   files.ca,
	})
	require.NoError(t, err)
	require.NotNil(t, srv.TLSConfig)

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	defer srv.Close()
	go func() {
		_ = srv.ServeTLS(ln, files.serverCert, files.serverKey)
	}()

	client, err := NewRemoteExecutor("https://"+ln.Addr().String(), files.clientCert, files.clientKey, files.ca)
	require.NoError(t, err)
	iface, peers, err := client.ShowDump(context.Background(), "awg0")
	require.NoError(t, err)
	require.Empty(t, iface.PrivateKey)
	require.Equal(t, "PUB", iface.PublicKey)
	require.Empty(t, peers)
}

type errStub string

func (e errStub) Error() string { return string(e) }

type mtlsFiles struct {
	ca         string
	serverCert string
	serverKey  string
	clientCert string
	clientKey  string
}

func writeTestMTLSFiles(t *testing.T) mtlsFiles {
	t.Helper()
	dir := t.TempDir()
	caKey, err := rsa.GenerateKey(rand.Reader, 2048)
	require.NoError(t, err)
	caTpl := &x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: "awg-test-ca"},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(time.Hour),
		IsCA:                  true,
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageDigitalSignature,
		BasicConstraintsValid: true,
	}
	caDER, err := x509.CreateCertificate(rand.Reader, caTpl, caTpl, &caKey.PublicKey, caKey)
	require.NoError(t, err)
	caPath := filepath.Join(dir, "ca.pem")
	writePEM(t, caPath, "CERTIFICATE", caDER)

	serverCert, serverKey := writeLeafCert(t, dir, "server", caTpl, caKey, []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth})
	clientCert, clientKey := writeLeafCert(t, dir, "client", caTpl, caKey, []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth})
	return mtlsFiles{
		ca:         caPath,
		serverCert: serverCert,
		serverKey:  serverKey,
		clientCert: clientCert,
		clientKey:  clientKey,
	}
}

func writeLeafCert(t *testing.T, dir, name string, ca *x509.Certificate, caKey *rsa.PrivateKey, usages []x509.ExtKeyUsage) (string, string) {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	require.NoError(t, err)
	tpl := &x509.Certificate{
		SerialNumber: big.NewInt(time.Now().UnixNano()),
		Subject:      pkix.Name{CommonName: "awg-test-" + name},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment,
		ExtKeyUsage:  usages,
		DNSNames:     []string{"localhost"},
		IPAddresses:  []net.IP{net.ParseIP("127.0.0.1"), net.ParseIP("::1")},
	}
	der, err := x509.CreateCertificate(rand.Reader, tpl, ca, &key.PublicKey, caKey)
	require.NoError(t, err)
	certPath := filepath.Join(dir, name+".crt")
	keyPath := filepath.Join(dir, name+".key")
	writePEM(t, certPath, "CERTIFICATE", der)
	writePEM(t, keyPath, "RSA PRIVATE KEY", x509.MarshalPKCS1PrivateKey(key))
	return certPath, keyPath
}

func writePEM(t *testing.T, path, typ string, der []byte) {
	t.Helper()
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	require.NoError(t, err)
	defer f.Close()
	require.NoError(t, pem.Encode(f, &pem.Block{Type: typ, Bytes: der}))
}

package bootstrap

import (
	"net/netip"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/awg-rest/awg-rest/internal/domain"
)

func TestEndpointWithPort(t *testing.T) {
	tests := map[string]string{
		"203.0.113.10":       "203.0.113.10:51820",
		"vpn.example.com":    "vpn.example.com:51820",
		"203.0.113.10:4444":  "203.0.113.10:4444",
		"2001:db8::1":        "[2001:db8::1]:51820",
		"[2001:db8::1]:4444": "[2001:db8::1]:4444",
	}
	for in, want := range tests {
		if got := endpointWithPort(in, 51820); got != want {
			t.Fatalf("endpointWithPort(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestServerAddressFromPool(t *testing.T) {
	pool := netip.MustParsePrefix("10.200.0.0/24")
	got, err := serverAddressFromPool(pool)
	if err != nil {
		t.Fatal(err)
	}
	if got != "10.200.0.1/24" {
		t.Fatalf("serverAddressFromPool = %q", got)
	}
}

func TestWriteBootstrapConfIncludesPrivateKeyAndNAT(t *testing.T) {
	dir := t.TempDir()
	profile := domain.ProtocolProfile{
		Name:             "default-v2",
		ProtocolVersion:  domain.ProtocolV2,
		Jc:               4,
		Jmin:             100,
		Jmax:             200,
		S1:               20,
		S2:               20,
		S3:               20,
		S4:               8,
		H1:               domain.IntRange{Min: 1000, Max: 1000},
		H2:               domain.IntRange{Min: 2000, Max: 2000},
		H3:               domain.IntRange{Min: 3000, Max: 3000},
		H4:               domain.IntRange{Min: 4000, Max: 4000},
		ListenPortPolicy: "fixed",
	}
	err := writeBootstrapConf(Defaults{
		NodeIface:        "awg0",
		NodeBasePort:     51820,
		BootstrapConfDir: dir,
		EnableNAT:        true,
		EgressIface:      "eth0",
	}, netip.MustParsePrefix("10.200.0.0/24"), "server-private-key", profile)
	if err != nil {
		t.Fatal(err)
	}

	path := filepath.Join(dir, "awg0.conf")
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	cfg := string(b)
	for _, want := range []string{
		"PrivateKey = server-private-key",
		"Address = 10.200.0.1/24",
		"ListenPort = 51820",
		"iptables -t nat -C POSTROUTING -s 10.200.0.0/24 -o eth0 -j MASQUERADE",
	} {
		if !strings.Contains(cfg, want) {
			t.Fatalf("bootstrap config missing %q:\n%s", want, cfg)
		}
	}
}

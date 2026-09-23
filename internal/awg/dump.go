package awg

import (
	"bufio"
	"fmt"
	"strconv"
	"strings"
	"time"
)

// ParseShowDump parses the script-friendly output of `awg show <iface> dump`.
//
// Format: first line is the interface row (4 tab-separated fields), each
// subsequent line is a peer row (8 fields):
//
//	priv  pub  listen-port  fwmark
//	pub  psk  endpoint  allowed-ips  latest-handshake  rx  tx  keepalive
//
// Empty fields are reported as the literal "(none)" by upstream wg/awg.
func ParseShowDump(s string) (InterfaceRuntime, []PeerRuntime, error) {
	var iface InterfaceRuntime
	var peers []PeerRuntime
	sc := bufio.NewScanner(strings.NewReader(s))
	sc.Buffer(make([]byte, 1024*1024), 1024*1024)
	first := true
	for sc.Scan() {
		line := sc.Text()
		if line == "" {
			continue
		}
		fields := strings.Split(line, "\t")
		if first {
			first = false
			if len(fields) < 4 {
				return iface, nil, fmt.Errorf("invalid interface line: %q", line)
			}
			iface.PrivateKey = noneToEmpty(fields[0])
			iface.PublicKey = noneToEmpty(fields[1])
			port, err := strconv.Atoi(fields[2])
			if err != nil {
				return iface, nil, fmt.Errorf("invalid listen port: %w", err)
			}
			iface.ListenPort = port
			fwmark, err := strconv.ParseInt(fields[3], 0, 64)
			if err != nil && fields[3] != "off" {
				return iface, nil, fmt.Errorf("invalid fwmark: %w", err)
			}
			iface.FwMark = int(fwmark)
			continue
		}
		if len(fields) < 8 {
			return iface, nil, fmt.Errorf("invalid peer line: %q", line)
		}
		p := PeerRuntime{
			PublicKey:    fields[0],
			PresharedKey: noneToEmpty(fields[1]),
			Endpoint:     noneToEmpty(fields[2]),
			AllowedIPs:   splitAllowedIPs(fields[3]),
		}
		if ts, err := strconv.ParseInt(fields[4], 10, 64); err == nil && ts > 0 {
			p.LastHandshake = time.Unix(ts, 0).UTC()
		}
		rx, err := strconv.ParseInt(fields[5], 10, 64)
		if err != nil {
			return iface, nil, fmt.Errorf("invalid rx bytes: %w", err)
		}
		tx, err := strconv.ParseInt(fields[6], 10, 64)
		if err != nil {
			return iface, nil, fmt.Errorf("invalid tx bytes: %w", err)
		}
		p.RxBytes = rx
		p.TxBytes = tx
		if fields[7] != "off" && fields[7] != "" {
			ka, err := strconv.Atoi(fields[7])
			if err != nil {
				return iface, nil, fmt.Errorf("invalid keepalive: %w", err)
			}
			p.KeepaliveSecs = ka
		}
		peers = append(peers, p)
	}
	if err := sc.Err(); err != nil {
		return iface, nil, err
	}
	return iface, peers, nil
}

func noneToEmpty(s string) string {
	if s == "(none)" {
		return ""
	}
	return s
}

func splitAllowedIPs(s string) []string {
	if s == "(none)" || s == "" {
		return nil
	}
	parts := strings.Split(s, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p != "" {
			out = append(out, p)
		}
	}
	return out
}

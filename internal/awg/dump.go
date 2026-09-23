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
// The peer row stays WireGuard-compatible (8 tab-separated fields), but the
// interface row is versioned by amneziawg-tools. Legacy/plain WireGuard has
// four fields while AWG V2/V3.1 insert protocol parameters before fwmark.
// Therefore only the stable prefix (private/public/listen-port) and the final
// fwmark field are parsed here.
//
// Empty fields are reported as "(none)" or "(null)" by upstream tools.
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
			fwmarkField := fields[len(fields)-1]
			fwmark, err := strconv.ParseInt(fwmarkField, 0, 64)
			if err != nil && fwmarkField != "off" {
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
			p.KeepaliveRange = fields[7]
			if !strings.Contains(fields[7], "-") {
				ka, err := strconv.Atoi(fields[7])
				if err != nil {
					return iface, nil, fmt.Errorf("invalid keepalive: %w", err)
				}
				p.KeepaliveSecs = ka
			} else if err := validateUint16RangeText(fields[7]); err != nil {
				return iface, nil, fmt.Errorf("invalid keepalive range: %w", err)
			}
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

func validateUint16RangeText(s string) error {
	parts := strings.Split(s, "-")
	if len(parts) != 2 {
		return fmt.Errorf("expected min-max")
	}
	lo, err := strconv.ParseUint(parts[0], 10, 16)
	if err != nil {
		return fmt.Errorf("invalid min: %w", err)
	}
	hi, err := strconv.ParseUint(parts[1], 10, 16)
	if err != nil {
		return fmt.Errorf("invalid max: %w", err)
	}
	if lo > hi {
		return fmt.Errorf("min %d > max %d", lo, hi)
	}
	return nil
}

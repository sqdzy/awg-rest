package awg

import (
	"context"
	"errors"
	"os"
	"sort"
	"strings"
	"sync"
	"time"
)

// FakeExecutor is a thread-safe in-memory simulation of an AmneziaWG host.
// It models interface state and peers, provides parsable `show dump` output,
// and lets tests inject failures via Fail*.
type FakeExecutor struct {
	mu         sync.Mutex
	now        func() time.Time
	interfaces map[string]*fakeIface
	bootstraps map[string]*fakeIface

	// Optional injected failures. When set, the next call to the matching
	// method returns the error and clears the field (one-shot).
	FailSyncConf    error
	FailSetPeer     error
	FailRemovePeer  error
	FailShowDump    error
	FailShowConf    error
	FailInterfaceUp error
}

type fakeIface struct {
	priv       string
	pub        string
	listenPort int
	rendered   string // last applied config text
	peers      map[string]*PeerRuntime
}

// NewFakeExecutor returns a FakeExecutor with deterministic time when fixedNow
// is non-zero (test convenience).
func NewFakeExecutor(fixedNow time.Time) *FakeExecutor {
	now := time.Now
	if !fixedNow.IsZero() {
		now = func() time.Time { return fixedNow }
	}
	return &FakeExecutor{
		now:        now,
		interfaces: make(map[string]*fakeIface),
		bootstraps: make(map[string]*fakeIface),
	}
}

// Provision is a test helper: pre-create an interface with given keys/port.
func (f *FakeExecutor) Provision(iface, priv, pub string, port int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	ifc := &fakeIface{
		priv:       priv,
		pub:        pub,
		listenPort: port,
		rendered:   "[Interface]\nPrivateKey = " + priv + "\nListenPort = " + itoa(port) + "\n",
		peers:      map[string]*PeerRuntime{},
	}
	f.interfaces[iface] = ifc
	f.bootstraps[iface] = cloneIface(ifc)
}

// Snapshot returns a deep-ish copy of peers known to the iface, for assertions.
func (f *FakeExecutor) Snapshot(iface string) []PeerRuntime {
	f.mu.Lock()
	defer f.mu.Unlock()
	ifc, ok := f.interfaces[iface]
	if !ok {
		return nil
	}
	out := make([]PeerRuntime, 0, len(ifc.peers))
	for _, p := range ifc.peers {
		out = append(out, *p)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].PublicKey < out[j].PublicKey })
	return out
}

func (f *FakeExecutor) SyncConf(ctx context.Context, iface, config string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.FailSyncConf != nil {
		err := f.FailSyncConf
		f.FailSyncConf = nil
		return err
	}
	ifc, ok := f.interfaces[iface]
	if !ok {
		return errors.New("awg: interface not found: " + iface)
	}
	parsed, err := parseRenderedConfig(config)
	if err != nil {
		return err
	}
	ifc.rendered = config
	if parsed.iface.PrivateKey != "" {
		ifc.priv = parsed.iface.PrivateKey
		if ifc.pub == "" {
			ifc.pub = "fake-public-" + parsed.iface.PrivateKey
		}
	} else {
		ifc.priv = ""
		ifc.pub = ""
	}
	if parsed.iface.ListenPort != 0 {
		ifc.listenPort = parsed.iface.ListenPort
	}
	// Replace peer set to match config exactly (syncconf semantics).
	next := make(map[string]*PeerRuntime, len(parsed.peers))
	for _, p := range parsed.peers {
		existing := ifc.peers[p.PublicKey]
		var lh time.Time
		var rx, tx int64
		if existing != nil {
			lh = existing.LastHandshake
			rx, tx = existing.RxBytes, existing.TxBytes
		}
		next[p.PublicKey] = &PeerRuntime{
			PublicKey:     p.PublicKey,
			PresharedKey:  p.PresharedKey,
			AllowedIPs:    append([]string{}, p.AllowedIPs...),
			Endpoint:      p.Endpoint,
			KeepaliveSecs: p.KeepaliveSecs,
			LastHandshake: lh,
			RxBytes:       rx,
			TxBytes:       tx,
		}
	}
	ifc.peers = next
	return nil
}

func (f *FakeExecutor) SetPeer(ctx context.Context, iface string, p PeerSpec) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.FailSetPeer != nil {
		err := f.FailSetPeer
		f.FailSetPeer = nil
		return err
	}
	ifc, ok := f.interfaces[iface]
	if !ok {
		return errors.New("awg: interface not found: " + iface)
	}
	cur := ifc.peers[p.PublicKey]
	if cur == nil {
		cur = &PeerRuntime{PublicKey: p.PublicKey}
	}
	cur.PresharedKey = p.PresharedKey
	cur.AllowedIPs = append([]string{}, p.AllowedIPs...)
	cur.Endpoint = p.Endpoint
	cur.KeepaliveSecs = p.KeepaliveSecs
	ifc.peers[p.PublicKey] = cur
	return nil
}

func (f *FakeExecutor) RemovePeer(ctx context.Context, iface, publicKey string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.FailRemovePeer != nil {
		err := f.FailRemovePeer
		f.FailRemovePeer = nil
		return err
	}
	ifc, ok := f.interfaces[iface]
	if !ok {
		return nil // idempotent
	}
	delete(ifc.peers, publicKey)
	return nil
}

func (f *FakeExecutor) ShowDump(ctx context.Context, iface string) (InterfaceRuntime, []PeerRuntime, error) {
	if err := ctx.Err(); err != nil {
		return InterfaceRuntime{}, nil, err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.FailShowDump != nil {
		err := f.FailShowDump
		f.FailShowDump = nil
		return InterfaceRuntime{}, nil, err
	}
	ifc, ok := f.interfaces[iface]
	if !ok {
		return InterfaceRuntime{}, nil, errors.New("awg: interface not found: " + iface)
	}
	out := make([]PeerRuntime, 0, len(ifc.peers))
	for _, p := range ifc.peers {
		out = append(out, *p)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].PublicKey < out[j].PublicKey })
	return InterfaceRuntime{PrivateKey: ifc.priv, PublicKey: ifc.pub, ListenPort: ifc.listenPort}, out, nil
}

func (f *FakeExecutor) ShowConf(ctx context.Context, iface string) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.FailShowConf != nil {
		err := f.FailShowConf
		f.FailShowConf = nil
		return "", err
	}
	ifc, ok := f.interfaces[iface]
	if !ok {
		return "", errors.New("awg: interface not found: " + iface)
	}
	return ifc.rendered, nil
}

func (f *FakeExecutor) InterfaceUp(ctx context.Context, iface, configPath string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.FailInterfaceUp != nil {
		err := f.FailInterfaceUp
		f.FailInterfaceUp = nil
		return err
	}
	if _, ok := f.interfaces[iface]; !ok {
		if restored := f.restoreFromBootstrapLocked(iface, configPath); restored != nil {
			f.interfaces[iface] = restored
		} else {
			f.interfaces[iface] = &fakeIface{peers: map[string]*PeerRuntime{}}
		}
	}
	return nil
}

func (f *FakeExecutor) InterfaceDown(ctx context.Context, iface string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if ifc, ok := f.interfaces[iface]; ok {
		f.bootstraps[iface] = cloneIface(ifc)
		delete(f.interfaces, iface)
	}
	return nil
}

func (f *FakeExecutor) restoreFromBootstrapLocked(iface, configPath string) *fakeIface {
	if configPath != "" {
		if b, err := os.ReadFile(configPath); err == nil {
			if parsed, err := parseRenderedConfig(string(b)); err == nil {
				ifc := &fakeIface{
					priv:       parsed.iface.PrivateKey,
					pub:        "fake-public-" + parsed.iface.PrivateKey,
					listenPort: parsed.iface.ListenPort,
					rendered:   string(b),
					peers:      map[string]*PeerRuntime{},
				}
				if ifc.priv == "" {
					ifc.pub = ""
				}
				return ifc
			}
		}
	}
	if boot := f.bootstraps[iface]; boot != nil {
		return cloneIface(boot)
	}
	return nil
}

func cloneIface(in *fakeIface) *fakeIface {
	if in == nil {
		return nil
	}
	out := &fakeIface{
		priv:       in.priv,
		pub:        in.pub,
		listenPort: in.listenPort,
		rendered:   in.rendered,
		peers:      make(map[string]*PeerRuntime, len(in.peers)),
	}
	for k, p := range in.peers {
		cp := *p
		cp.AllowedIPs = append([]string{}, p.AllowedIPs...)
		out.peers[k] = &cp
	}
	return out
}

// SimulateHandshake mutates runtime stats for a peer, useful for E2E tests.
func (f *FakeExecutor) SimulateHandshake(iface, publicKey string, rx, tx int64) {
	f.mu.Lock()
	defer f.mu.Unlock()
	ifc, ok := f.interfaces[iface]
	if !ok {
		return
	}
	p := ifc.peers[publicKey]
	if p == nil {
		return
	}
	p.LastHandshake = f.now()
	p.RxBytes += rx
	p.TxBytes += tx
}

// parseRenderedConfig is a forgiving INI parser sufficient for SyncConf-roundtrip
// in the fake. Real CLI executor uses `awg syncconf` and never parses Go-side.
type parsedConfig struct {
	iface InterfaceRuntime
	peers []PeerSpec
}

func parseRenderedConfig(s string) (parsedConfig, error) {
	var pc parsedConfig
	var section string
	var cur PeerSpec
	flush := func() {
		if section == "Peer" && cur.PublicKey != "" {
			pc.peers = append(pc.peers, cur)
		}
		cur = PeerSpec{}
	}
	for _, raw := range strings.Split(s, "\n") {
		line := strings.TrimSpace(raw)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		if strings.HasPrefix(line, "[") && strings.HasSuffix(line, "]") {
			flush()
			section = strings.Trim(line, "[]")
			continue
		}
		k, v, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		k = strings.TrimSpace(k)
		v = strings.TrimSpace(v)
		switch section {
		case "Interface":
			switch strings.ToLower(k) {
			case "privatekey":
				pc.iface.PrivateKey = v
			case "listenport":
				var p int
				_, _ = fmtScan(&p, v)
				pc.iface.ListenPort = p
			}
		case "Peer":
			switch strings.ToLower(k) {
			case "publickey":
				cur.PublicKey = v
			case "presharedkey":
				cur.PresharedKey = v
			case "allowedips":
				cur.AllowedIPs = splitCSV(v)
			case "endpoint":
				cur.Endpoint = v
			case "persistentkeepalive":
				var p int
				_, _ = fmtScan(&p, v)
				cur.KeepaliveSecs = p
			}
		}
	}
	flush()
	return pc, nil
}

func splitCSV(s string) []string {
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

// fmtScan is a tiny replacement for fmt.Sscanf to avoid the import here.
func fmtScan(dst *int, s string) (int, error) {
	*dst = 0
	for _, ch := range s {
		if ch < '0' || ch > '9' {
			return 0, errors.New("not a number")
		}
		*dst = *dst*10 + int(ch-'0')
	}
	return 1, nil
}

func itoa(i int) string {
	if i == 0 {
		return "0"
	}
	var buf [20]byte
	pos := len(buf)
	for i > 0 {
		pos--
		buf[pos] = byte('0' + i%10)
		i /= 10
	}
	return string(buf[pos:])
}

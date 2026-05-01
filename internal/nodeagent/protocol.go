// Package nodeagent defines the wire protocol used by the control plane to
// drive the local AmneziaWG host. The protocol is small JSON over HTTPS+mTLS
// so it works equally well across LAN, mesh, or Tailscale-style overlays.
//
// Endpoints:
//
//	POST /v1/iface/{iface}/syncconf       application/x-ini      -> 204
//	POST /v1/iface/{iface}/peers           {PeerSpec}             -> 204
//	DELETE /v1/iface/{iface}/peers/{pub}                          -> 204
//	GET  /v1/iface/{iface}/dump                                   -> {DumpResponse}
//	GET  /v1/iface/{iface}/showconf                               -> text/plain
//	POST /v1/iface/{iface}/up              {ConfigPath}           -> 204
//	POST /v1/iface/{iface}/down                                   -> 204
//
// All errors are returned as RFC 9457 problem+json.
package nodeagent

import (
	"time"
)

// PeerSpec is the JSON form of awg.PeerSpec.
type PeerSpec struct {
	PublicKey     string   `json:"public_key"`
	PresharedKey  string   `json:"preshared_key,omitempty"`
	AllowedIPs    []string `json:"allowed_ips"`
	Endpoint      string   `json:"endpoint,omitempty"`
	KeepaliveSecs int      `json:"keepalive_secs,omitempty"`
}

// PeerRuntime mirrors awg.PeerRuntime.
type PeerRuntime struct {
	PublicKey     string    `json:"public_key"`
	PresharedKey  string    `json:"preshared_key,omitempty"`
	Endpoint      string    `json:"endpoint,omitempty"`
	AllowedIPs    []string  `json:"allowed_ips"`
	LastHandshake time.Time `json:"last_handshake,omitempty"`
	RxBytes       int64     `json:"rx_bytes"`
	TxBytes       int64     `json:"tx_bytes"`
	KeepaliveSecs int       `json:"keepalive_secs,omitempty"`
}

// InterfaceRuntime is the per-interface row of `awg show <iface> dump`.
type InterfaceRuntime struct {
	PrivateKey string `json:"private_key,omitempty"`
	PublicKey  string `json:"public_key"`
	ListenPort int    `json:"listen_port"`
	FwMark     int    `json:"fw_mark"`
}

// DumpResponse is what GET /v1/iface/{iface}/dump returns.
type DumpResponse struct {
	Interface InterfaceRuntime `json:"interface"`
	Peers     []PeerRuntime    `json:"peers"`
}

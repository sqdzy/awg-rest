package nodeagent

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/awg-rest/awg-rest/internal/awg"
)

// RemoteExecutor implements awg.Executor by calling a remote node-agent over
// HTTPS+mTLS. It is the executor used by the control-plane worker in
// production.
type RemoteExecutor struct {
	BaseURL    string
	HTTPClient *http.Client
}

// NewRemoteExecutor returns a RemoteExecutor authenticating with HTTPS+mTLS.
func NewRemoteExecutor(baseURL, clientCert, clientKey, caBundle string) (*RemoteExecutor, error) {
	return newRemoteExecutor(baseURL, clientCert, clientKey, caBundle, false)
}

// NewInsecureRemoteExecutor returns a RemoteExecutor for explicit dev/test
// HTTP usage. Production code should use NewRemoteExecutor.
func NewInsecureRemoteExecutor(baseURL string) (*RemoteExecutor, error) {
	return newRemoteExecutor(baseURL, "", "", "", true)
}

func newRemoteExecutor(baseURL, clientCert, clientKey, caBundle string, allowInsecureHTTP bool) (*RemoteExecutor, error) {
	u, err := url.Parse(baseURL)
	if err != nil || u.Scheme == "" || u.Host == "" {
		return nil, fmt.Errorf("nodeagent: invalid base URL")
	}
	if allowInsecureHTTP {
		if u.Scheme != "http" && u.Scheme != "https" {
			return nil, fmt.Errorf("nodeagent: base URL must use http or https")
		}
	} else if u.Scheme != "https" {
		return nil, fmt.Errorf("nodeagent: production remote executor requires https")
	}
	if !allowInsecureHTTP && (clientCert == "" || clientKey == "" || caBundle == "") {
		return nil, fmt.Errorf("nodeagent: client cert, client key, and CA bundle are required for mTLS")
	}
	tc := &tls.Config{MinVersion: tls.VersionTLS13}
	if clientCert != "" || clientKey != "" {
		if clientCert == "" || clientKey == "" {
			return nil, fmt.Errorf("nodeagent: client cert and key must be provided together")
		}
		cert, err := tls.LoadX509KeyPair(clientCert, clientKey)
		if err != nil {
			return nil, fmt.Errorf("client cert: %w", err)
		}
		tc.Certificates = []tls.Certificate{cert}
	}
	if caBundle != "" {
		bytes, err := os.ReadFile(caBundle)
		if err != nil {
			return nil, fmt.Errorf("ca bundle: %w", err)
		}
		pool := x509.NewCertPool()
		if !pool.AppendCertsFromPEM(bytes) {
			return nil, fmt.Errorf("ca bundle parse failed")
		}
		tc.RootCAs = pool
	}
	return &RemoteExecutor{
		BaseURL: strings.TrimRight(baseURL, "/"),
		HTTPClient: &http.Client{
			Timeout:   30 * time.Second,
			Transport: &http.Transport{TLSClientConfig: tc},
		},
	}, nil
}

func (r *RemoteExecutor) urlf(format string, args ...any) string {
	return r.BaseURL + fmt.Sprintf(format, args...)
}

func (r *RemoteExecutor) do(req *http.Request) (*http.Response, error) {
	resp, err := r.HTTPClient.Do(req)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode >= 400 {
		body, _ := io.ReadAll(resp.Body)
		_ = resp.Body.Close()
		return nil, fmt.Errorf("nodeagent %s %s: %d %s", req.Method, req.URL.Path, resp.StatusCode, ProblemDetail(body))
	}
	return resp, nil
}

func (r *RemoteExecutor) SyncConf(ctx context.Context, iface, config string) error {
	req, _ := http.NewRequestWithContext(ctx, http.MethodPost,
		r.urlf("/v1/iface/%s/syncconf", url.PathEscape(iface)),
		bytes.NewReader([]byte(config)))
	req.Header.Set("Content-Type", "application/x-ini")
	resp, err := r.do(req)
	if err != nil {
		return err
	}
	resp.Body.Close()
	return nil
}

func (r *RemoteExecutor) SetPeer(ctx context.Context, iface string, p awg.PeerSpec) error {
	body, _ := json.Marshal(PeerSpec{
		PublicKey: p.PublicKey, PresharedKey: p.PresharedKey,
		AllowedIPs: p.AllowedIPs, Endpoint: p.Endpoint, KeepaliveSecs: p.KeepaliveSecs,
	})
	req, _ := http.NewRequestWithContext(ctx, http.MethodPost,
		r.urlf("/v1/iface/%s/peers", url.PathEscape(iface)),
		bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	resp, err := r.do(req)
	if err != nil {
		return err
	}
	resp.Body.Close()
	return nil
}

func (r *RemoteExecutor) RemovePeer(ctx context.Context, iface, publicKey string) error {
	req, _ := http.NewRequestWithContext(ctx, http.MethodDelete,
		r.urlf("/v1/iface/%s/peers/%s", url.PathEscape(iface), url.PathEscape(publicKey)), nil)
	resp, err := r.do(req)
	if err != nil {
		return err
	}
	resp.Body.Close()
	return nil
}

func (r *RemoteExecutor) ShowDump(ctx context.Context, iface string) (awg.InterfaceRuntime, []awg.PeerRuntime, error) {
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet,
		r.urlf("/v1/iface/%s/dump", url.PathEscape(iface)), nil)
	resp, err := r.do(req)
	if err != nil {
		return awg.InterfaceRuntime{}, nil, err
	}
	defer resp.Body.Close()
	var d DumpResponse
	if err := json.NewDecoder(resp.Body).Decode(&d); err != nil {
		return awg.InterfaceRuntime{}, nil, err
	}
	out := make([]awg.PeerRuntime, 0, len(d.Peers))
	for _, p := range d.Peers {
		out = append(out, awg.PeerRuntime{
			PublicKey: p.PublicKey, PresharedKey: p.PresharedKey,
			Endpoint: p.Endpoint, AllowedIPs: p.AllowedIPs,
			LastHandshake: p.LastHandshake,
			RxBytes:       p.RxBytes, TxBytes: p.TxBytes, KeepaliveSecs: p.KeepaliveSecs,
		})
	}
	return awg.InterfaceRuntime{
		PrivateKey: d.Interface.PrivateKey,
		PublicKey:  d.Interface.PublicKey,
		ListenPort: d.Interface.ListenPort,
		FwMark:     d.Interface.FwMark,
	}, out, nil
}

func (r *RemoteExecutor) ShowConf(ctx context.Context, iface string) (string, error) {
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet,
		r.urlf("/v1/iface/%s/showconf", url.PathEscape(iface)), nil)
	resp, err := r.do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	return string(body), err
}

func (r *RemoteExecutor) InterfaceUp(ctx context.Context, iface, configPath string) error {
	body, _ := json.Marshal(map[string]string{"config_path": configPath})
	req, _ := http.NewRequestWithContext(ctx, http.MethodPost,
		r.urlf("/v1/iface/%s/up", url.PathEscape(iface)), bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	resp, err := r.do(req)
	if err != nil {
		return err
	}
	resp.Body.Close()
	return nil
}

func (r *RemoteExecutor) InterfaceDown(ctx context.Context, iface string) error {
	req, _ := http.NewRequestWithContext(ctx, http.MethodPost,
		r.urlf("/v1/iface/%s/down", url.PathEscape(iface)), nil)
	resp, err := r.do(req)
	if err != nil {
		return err
	}
	resp.Body.Close()
	return nil
}

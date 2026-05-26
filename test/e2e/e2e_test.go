//go:build e2e

// Package e2e exercises the full control-plane code path end-to-end against
// a real Postgres (testcontainers) and a fake AmneziaWG executor. It does
// NOT require a Linux kernel module, so it runs cross-platform.
//
// Build tag selection:
//   - `go test -tags=e2e ./test/e2e/...`         — fake AWG executor (default).
//   - `go test -tags="e2e linux_awg" ...`        — real `awg`/kernel module.
package e2e

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/awg-rest/awg-rest/internal/api"
	"github.com/awg-rest/awg-rest/internal/auth"
	"github.com/awg-rest/awg-rest/internal/awg"
	"github.com/awg-rest/awg-rest/internal/crypto"
	"github.com/awg-rest/awg-rest/internal/domain"
	"github.com/awg-rest/awg-rest/internal/obs"
	"github.com/awg-rest/awg-rest/internal/outbox"
	"github.com/awg-rest/awg-rest/internal/ratelimit"
	"github.com/awg-rest/awg-rest/internal/repo"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"github.com/testcontainers/testcontainers-go"
	tcpostgres "github.com/testcontainers/testcontainers-go/modules/postgres"
	"github.com/testcontainers/testcontainers-go/wait"
)

type testEnv struct {
	DB        *repo.DB
	Service   *api.Service
	Server    *httptest.Server
	Worker    *outbox.Worker
	Executor  *awg.FakeExecutor
	Validator *auth.HMACValidator
	JWTSecret []byte
	Tenant    *domain.Tenant
	Profile   *domain.ProtocolProfile
	Node      *domain.Node
}

func newTestEnv(t *testing.T) *testEnv {
	t.Helper()
	disableRyukForLocalTests(t)
	ctx := context.Background()

	pg, err := tcpostgres.Run(ctx, "postgres:15-alpine",
		tcpostgres.WithDatabase("awg"),
		tcpostgres.WithUsername("postgres"),
		tcpostgres.WithPassword("postgres"),
		testcontainers.WithWaitStrategy(
			wait.ForLog("database system is ready to accept connections").
				WithOccurrence(2).
				WithStartupTimeout(60*time.Second),
		),
	)
	require.NoError(t, err)
	t.Cleanup(func() { _ = testcontainers.TerminateContainer(pg) })

	dsn, err := pg.ConnectionString(ctx, "sslmode=disable")
	require.NoError(t, err)
	db, err := repo.NewDB(ctx, dsn)
	require.NoError(t, err)
	t.Cleanup(db.Close)
	require.NoError(t, repo.Migrate(ctx, db.Pool))

	tenants := &repo.Tenants{DB: db}
	profiles := &repo.Profiles{DB: db}
	nodes := &repo.Nodes{DB: db}
	pools := &repo.Pools{DB: db}

	tenant, err := tenants.Upsert(ctx, "acme")
	require.NoError(t, err)
	profile, err := profiles.Insert(ctx, domain.ProtocolProfile{
		Name: "default-v2", ProtocolVersion: domain.ProtocolV2,
		Jc: 5, Jmin: 64, Jmax: 1000, S1: 40, S2: 32,
		H1: domain.IntRange{Min: 1_000, Max: 2_000},
		H2: domain.IntRange{Min: 3_000, Max: 4_000},
		H3: domain.IntRange{Min: 5_000, Max: 6_000},
		H4: domain.IntRange{Min: 7_000, Max: 8_000},
	})
	require.NoError(t, err)

	serverKP, err := crypto.GenerateKeyPair()
	require.NoError(t, err)

	node, err := nodes.Insert(ctx, domain.Node{
		Region: "eu", Hostname: "vpn-1.test", PublicEndpoint: "vpn-1.test:585",
		BasePort: 585, InterfaceName: "awg0",
		ServerPublicKey: serverKP.PublicKey,
	})
	require.NoError(t, err)

	cidr, _ := netip.ParsePrefix("10.90.0.0/24")
	_, err = pools.CreatePool(ctx, tenant.ID, node.ID, cidr)
	require.NoError(t, err)

	exec := awg.NewFakeExecutor(time.Time{})
	exec.Provision("awg0", serverKP.PrivateKey, serverKP.PublicKey, 585)

	svc := &api.Service{
		DB:             db,
		Tenants:        tenants,
		Nodes:          nodes,
		Profiles:       profiles,
		Peers:          &repo.Peers{DB: db},
		Operations:     &repo.Operations{DB: db},
		Outbox:         &repo.Outbox{DB: db},
		Idem:           &repo.Idempotency{DB: db},
		Audit:          &repo.Audit{DB: db},
		IdempotencyTTL: time.Hour,
	}
	handlers := &api.Handlers{Service: svc}

	secret := []byte("e2e-test-secret-must-be-long-enough-for-hs256")
	validator := &auth.HMACValidator{
		Secret: secret, Issuer: "awg-rest", Audience: "awg-control-plane",
		AllowedAlgs: []string{"HS256"},
	}
	logger := obs.NewLogger("error", false)

	router := api.NewRouter(api.RouterConfig{
		Handlers:    handlers,
		Validator:   validator,
		Logger:      logger,
		RateLimiter: ratelimit.NewTokenBucket(1000, 1000),
	})

	srv := httptest.NewServer(router)
	t.Cleanup(srv.Close)

	worker := &outbox.Worker{
		DB: db, Outbox: svc.Outbox, Operations: svc.Operations,
		Peers: svc.Peers, Profiles: svc.Profiles, Nodes: svc.Nodes,
		Executor: exec, Logger: logger,
	}

	return &testEnv{
		DB: db, Service: svc, Server: srv, Worker: worker, Executor: exec,
		Validator: validator, JWTSecret: secret,
		Tenant: tenant, Profile: profile, Node: node,
	}
}

func (e *testEnv) bearerForAdmin(t *testing.T) string {
	t.Helper()
	return e.bearerFor(t, e.Tenant.ID, []auth.Role{auth.RoleTenantAdmin},
		[]string{"peer:create", "peer:read", "peer:revoke"})
}

func (e *testEnv) bearerFor(t *testing.T, tenantID uuid.UUID, roles []auth.Role, scopes []string) string {
	t.Helper()
	tok, err := auth.IssueDevToken(e.JWTSecret, "awg-rest", "awg-control-plane",
		auth.Principal{
			SubjectID: uuid.New(), TenantID: tenantID,
			Roles:  roles,
			Scopes: scopes,
		}, time.Hour)
	require.NoError(t, err)
	return tok
}

func TestE2E_PeerLifecycle(t *testing.T) {
	env := newTestEnv(t)
	bearer := env.bearerForAdmin(t)
	client := env.Server.Client()

	// 1) Create a peer.
	body := map[string]any{
		"external_id":  "user-1",
		"display_name": "E2E user 1",
		"profile_name": env.Profile.Name,
	}
	create := postJSON(t, client, env.Server.URL+"/v1/tenants/acme/peers",
		bearer, "create-1", body)
	require.Equal(t, http.StatusAccepted, create.StatusCode)
	var resp api.CreatePeerResponse
	require.NoError(t, json.NewDecoder(create.Body).Decode(&resp))
	require.NotEmpty(t, resp.OperationID)
	require.NotEmpty(t, resp.PeerID)
	require.NotEmpty(t, resp.PrivateKey, "first response must include the one-time private key")
	require.NotEmpty(t, resp.PresharedKey, "first response must include the one-time preshared key")
	require.NotEmpty(t, resp.PublicKey)
	require.True(t, strings.HasPrefix(resp.AllowedIP, "10.90.0."))

	// 2) Idempotency replay -> same response, NO private key (sanitized).
	replay := postJSON(t, client, env.Server.URL+"/v1/tenants/acme/peers",
		bearer, "create-1", body)
	require.Equal(t, http.StatusAccepted, replay.StatusCode)
	var rep api.CreatePeerResponse
	require.NoError(t, json.NewDecoder(replay.Body).Decode(&rep))
	require.Equal(t, resp.PeerID, rep.PeerID)
	require.Empty(t, rep.PrivateKey, "replays MUST NOT re-issue private keys")
	require.Empty(t, rep.PresharedKey, "replays MUST NOT re-issue preshared keys")

	// 3) Idempotency conflict: same key, different body -> 409.
	conflictBody := map[string]any{
		"external_id":  "user-1-different",
		"profile_name": env.Profile.Name,
	}
	conflict := postJSON(t, client, env.Server.URL+"/v1/tenants/acme/peers",
		bearer, "create-1", conflictBody)
	require.Equal(t, http.StatusConflict, conflict.StatusCode)

	// 4) Worker applies the outbox job; runtime now contains the peer.
	ran, err := env.Worker.RunOnce(context.Background())
	require.NoError(t, err)
	require.True(t, ran)

	snap := env.Executor.Snapshot("awg0")
	require.Len(t, snap, 1)
	require.Equal(t, resp.PublicKey, snap[0].PublicKey)
	require.Equal(t, resp.PresharedKey, snap[0].PresharedKey)
	require.Contains(t, snap[0].AllowedIPs, resp.AllowedIP)
	require.Contains(t, resp.ClientConfig, "PrivateKey = "+resp.PrivateKey)
	require.Contains(t, resp.ClientConfig, "PresharedKey = "+resp.PresharedKey)
	require.Contains(t, resp.ClientConfig, "Endpoint = vpn-1.test:585")

	// 5) Operation is now applied.
	opStatus := getJSON(t, client, env.Server.URL+"/v1/operations/"+resp.OperationID, bearer)
	require.Equal(t, http.StatusOK, opStatus.StatusCode)
	var op domain.Operation
	require.NoError(t, json.NewDecoder(opStatus.Body).Decode(&op))
	require.Equal(t, domain.OpStatusApplied, op.Status)

	// 6) Get peer.
	peer := getJSON(t, client, env.Server.URL+"/v1/tenants/acme/peers/"+resp.PeerID, bearer)
	require.Equal(t, http.StatusOK, peer.StatusCode)

	// 7) Get configuration.
	cfg := getJSON(t, client, env.Server.URL+"/v1/tenants/acme/peers/"+resp.PeerID+"/configuration", bearer)
	require.Equal(t, http.StatusOK, cfg.StatusCode)
	cfgBody := readBody(t, cfg)
	require.Contains(t, cfgBody, "[Interface]")
	require.Contains(t, cfgBody, "[Peer]")
	require.Contains(t, cfgBody, "Endpoint = vpn-1.test:585")
	require.Contains(t, cfgBody, "Jc = 5") // V2 profile fields rendered for client
	require.Contains(t, cfgBody, "H1 = 1000-2000")
	require.Contains(t, cfgBody, "I5 = ")
	require.NotContains(t, cfgBody, "PrivateKey =")
	require.NotContains(t, cfgBody, "PresharedKey =")

	leakyCfg := getJSON(t, client,
		env.Server.URL+"/v1/tenants/acme/peers/"+resp.PeerID+"/configuration?client_private_key=SECRET", bearer)
	require.Equal(t, http.StatusUnprocessableEntity, leakyCfg.StatusCode)

	// 8) Revoke the peer.
	rev := postJSON(t, client,
		env.Server.URL+"/v1/tenants/acme/peers/"+resp.PeerID+":revoke",
		bearer, "revoke-1", map[string]string{"reason": "test"})
	require.Equal(t, http.StatusAccepted, rev.StatusCode)

	ran, err = env.Worker.RunOnce(context.Background())
	require.NoError(t, err)
	require.True(t, ran)

	// 9) Runtime now empty.
	require.Empty(t, env.Executor.Snapshot("awg0"))
	pid, err := uuid.Parse(resp.PeerID)
	require.NoError(t, err)
	revokedPeer, err := env.Service.Peers.GetByID(context.Background(), pid)
	require.NoError(t, err)
	require.Equal(t, domain.PeerStateRevoked, revokedPeer.State)
	require.Equal(t, revokedPeer.DesiredRevision, revokedPeer.AppliedRevision)
}

func TestE2E_TenantIsolation(t *testing.T) {
	env := newTestEnv(t)
	client := env.Server.Client()
	acmeBearer := env.bearerForAdmin(t)

	create := postJSON(t, client, env.Server.URL+"/v1/tenants/acme/peers",
		acmeBearer, "tenant-iso-create", map[string]any{
			"external_id":  "tenant-iso-user",
			"profile_name": env.Profile.Name,
		})
	require.Equal(t, http.StatusAccepted, create.StatusCode)
	var cr api.CreatePeerResponse
	require.NoError(t, json.NewDecoder(create.Body).Decode(&cr))

	otherTenant, err := env.Service.EnsureTenant(context.Background(), "other")
	require.NoError(t, err)
	otherBearer := env.bearerFor(t, otherTenant.ID, []auth.Role{auth.RoleTenantAdmin},
		[]string{"peer:create", "peer:read", "peer:revoke"})

	peer := getJSON(t, client, env.Server.URL+"/v1/tenants/acme/peers/"+cr.PeerID, otherBearer)
	require.Equal(t, http.StatusForbidden, peer.StatusCode)

	cfg := getJSON(t, client, env.Server.URL+"/v1/tenants/acme/peers/"+cr.PeerID+"/configuration", otherBearer)
	require.Equal(t, http.StatusForbidden, cfg.StatusCode)

	op := getJSON(t, client, env.Server.URL+"/v1/operations/"+cr.OperationID, otherBearer)
	require.Equal(t, http.StatusForbidden, op.StatusCode)

	write := postJSON(t, client, env.Server.URL+"/v1/tenants/acme/peers",
		otherBearer, "tenant-iso-write", map[string]any{
			"external_id":  "wrong-tenant",
			"profile_name": env.Profile.Name,
		})
	require.Equal(t, http.StatusForbidden, write.StatusCode)

	revoke := postJSON(t, client, env.Server.URL+"/v1/tenants/acme/peers/"+cr.PeerID+":revoke",
		otherBearer, "tenant-iso-revoke", map[string]string{"reason": "wrong tenant"})
	require.Equal(t, http.StatusForbidden, revoke.StatusCode)
}

func TestE2E_Auth_Rejects(t *testing.T) {
	env := newTestEnv(t)
	client := env.Server.Client()

	t.Run("missing bearer", func(t *testing.T) {
		req, _ := http.NewRequest(http.MethodGet,
			env.Server.URL+"/v1/tenants/acme/peers/00000000-0000-0000-0000-000000000000", nil)
		resp, err := client.Do(req)
		require.NoError(t, err)
		require.Equal(t, http.StatusUnauthorized, resp.StatusCode)
	})

	t.Run("read-only role cannot create", func(t *testing.T) {
		tok, err := auth.IssueDevToken(env.JWTSecret, "awg-rest", "awg-control-plane",
			auth.Principal{SubjectID: uuid.New(), TenantID: env.Tenant.ID,
				Roles: []auth.Role{auth.RoleSupportReadOnly}}, time.Hour)
		require.NoError(t, err)
		resp := postJSON(t, client, env.Server.URL+"/v1/tenants/acme/peers", tok, "x",
			map[string]any{"external_id": "u", "profile_name": env.Profile.Name})
		require.Equal(t, http.StatusForbidden, resp.StatusCode)
	})

	t.Run("revoke requires idempotency key", func(t *testing.T) {
		resp := postJSON(t, client,
			env.Server.URL+"/v1/tenants/acme/peers/00000000-0000-0000-0000-000000000000:revoke",
			env.bearerForAdmin(t), "", map[string]string{"reason": "missing idem"})
		require.Equal(t, http.StatusUnprocessableEntity, resp.StatusCode)
	})
}

func TestE2E_DriftRecovery(t *testing.T) {
	env := newTestEnv(t)
	bearer := env.bearerForAdmin(t)
	client := env.Server.Client()

	// Create two peers.
	for i := 0; i < 2; i++ {
		create := postJSON(t, client, env.Server.URL+"/v1/tenants/acme/peers",
			bearer, "drift-"+itoa(i), map[string]any{
				"external_id":  "drift-" + itoa(i),
				"profile_name": env.Profile.Name,
			})
		require.Equal(t, http.StatusAccepted, create.StatusCode)
		_, err := env.Worker.RunOnce(context.Background())
		require.NoError(t, err)
	}

	require.Len(t, env.Executor.Snapshot("awg0"), 2)

	// Simulate runtime drift: an operator adds a rogue peer in the runtime
	// that is NOT in desired state.
	require.NoError(t, env.Executor.SetPeer(context.Background(), "awg0", awg.PeerSpec{
		PublicKey:  "ROGUEROGUEROGUEROGUEROGUEROGUEROGUEROGUE000=",
		AllowedIPs: []string{"10.90.99.99/32"},
	}))
	require.Len(t, env.Executor.Snapshot("awg0"), 3)

	// A reconcile must converge runtime back to desired state.
	require.NoError(t, env.Worker.Reconcile(context.Background(), env.Node.ID))

	snap := env.Executor.Snapshot("awg0")
	require.Len(t, snap, 2, "rogue peer must be removed by syncconf reconciliation")
	for _, p := range snap {
		require.NotContains(t, p.PublicKey, "ROGUE")
	}
}

func TestE2E_FailureSurvivesAndRetries(t *testing.T) {
	env := newTestEnv(t)
	bearer := env.bearerForAdmin(t)
	client := env.Server.Client()

	body := map[string]any{
		"external_id": "retry-1", "profile_name": env.Profile.Name,
	}
	resp := postJSON(t, client, env.Server.URL+"/v1/tenants/acme/peers",
		bearer, "retry-1", body)
	require.Equal(t, http.StatusAccepted, resp.StatusCode)

	// Inject a one-shot failure on the next SyncConf.
	env.Executor.FailSyncConf = errStub{}

	// First worker pass fails (retryable) — runtime stays empty.
	ran, _ := env.Worker.RunOnce(context.Background())
	require.True(t, ran)
	require.Empty(t, env.Executor.Snapshot("awg0"))

	// Wait out the lease, then the worker picks the same job up again and
	// applies it (failure was one-shot).
	expireLeases(t, env.DB)
	ran, err := env.Worker.RunOnce(context.Background())
	require.NoError(t, err)
	require.True(t, ran)
	require.Len(t, env.Executor.Snapshot("awg0"), 1)
}

func TestE2E_WorkerBootstrapsUserspaceAfterProtocolNotSupported(t *testing.T) {
	env := newTestEnv(t)
	bearer := env.bearerForAdmin(t)
	client := env.Server.Client()

	require.NoError(t, env.Executor.InterfaceDown(context.Background(), "awg0"))

	resp := postJSON(t, client, env.Server.URL+"/v1/tenants/acme/peers",
		bearer, "userspace-bootstrap-1", map[string]any{
			"external_id": "userspace-bootstrap-1", "profile_name": env.Profile.Name,
		})
	require.Equal(t, http.StatusAccepted, resp.StatusCode)

	env.Executor.FailSyncConf = errors.New(`awg: awg [syncconf awg0 /tmp/stripped.conf] failed: exit status 1 (stderr="Unable to retrieve current interface configuration: Protocol not supported\n")`)

	ran, err := env.Worker.RunOnce(context.Background())
	require.NoError(t, err)
	require.True(t, ran)
	require.Len(t, env.Executor.Snapshot("awg0"), 1)
}

// helper http wrappers

func postJSON(t *testing.T, c *http.Client, url, bearer, idemKey string, body any) *http.Response {
	t.Helper()
	b, _ := json.Marshal(body)
	req, _ := http.NewRequest(http.MethodPost, url, bytes.NewReader(b))
	req.Header.Set("Authorization", "Bearer "+bearer)
	req.Header.Set("Content-Type", "application/json")
	if idemKey != "" {
		req.Header.Set("Idempotency-Key", idemKey)
	}
	resp, err := c.Do(req)
	require.NoError(t, err)
	return resp
}

func getJSON(t *testing.T, c *http.Client, url, bearer string) *http.Response {
	t.Helper()
	req, _ := http.NewRequest(http.MethodGet, url, nil)
	req.Header.Set("Authorization", "Bearer "+bearer)
	resp, err := c.Do(req)
	require.NoError(t, err)
	return resp
}

func readBody(t *testing.T, r *http.Response) string {
	t.Helper()
	defer r.Body.Close()
	var b strings.Builder
	buf := make([]byte, 4096)
	for {
		n, err := r.Body.Read(buf)
		if n > 0 {
			b.Write(buf[:n])
		}
		if err != nil {
			break
		}
	}
	return b.String()
}

func itoa(i int) string {
	if i == 0 {
		return "0"
	}
	neg := i < 0
	if neg {
		i = -i
	}
	var b [20]byte
	bp := len(b)
	for i > 0 {
		bp--
		b[bp] = byte('0' + i%10)
		i /= 10
	}
	if neg {
		bp--
		b[bp] = '-'
	}
	return string(b[bp:])
}

// expireLeases moves all leased outbox rows to expired-lease so the worker
// picks them up again.
func expireLeases(t *testing.T, db *repo.DB) {
	t.Helper()
	_, err := db.Pool.Exec(context.Background(),
		`UPDATE outbox SET leased_until = now() - interval '1 hour' WHERE status = 'leased'`)
	require.NoError(t, err)
}

// errStub is a stable error for one-shot failure injection.
type errStub struct{}

func (errStub) Error() string { return "stub injected error" }

func disableRyukForLocalTests(t *testing.T) {
	t.Helper()
	if os.Getenv("TESTCONTAINERS_RYUK_DISABLED") == "" {
		t.Setenv("TESTCONTAINERS_RYUK_DISABLED", "true")
	}
}

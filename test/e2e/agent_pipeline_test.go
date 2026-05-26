//go:build e2e

package e2e

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/awg-rest/awg-rest/internal/api"
	"github.com/awg-rest/awg-rest/internal/awg"
	"github.com/awg-rest/awg-rest/internal/nodeagent"
	"github.com/awg-rest/awg-rest/internal/obs"
	"github.com/awg-rest/awg-rest/internal/outbox"
	"github.com/stretchr/testify/require"
)

// TestE2E_RemoteAgentPipeline wires the worker to the node-agent over HTTP
// (mTLS material is exercised by unit tests; here we focus on protocol
// round-trip semantics under the full control plane).
func TestE2E_RemoteAgentPipeline(t *testing.T) {
	env := newTestEnv(t)
	bearer := env.bearerForAdmin(t)
	client := env.Server.Client()

	// Stand up the node-agent in front of the SAME fake executor as the in-process
	// worker would use, but route through HTTP to exercise the wire protocol.
	agentSrv, err := nodeagent.NewServer(nodeagent.ServerConfig{
		Addr:              ":0",
		Executor:          env.Executor,
		Logger:            obs.NewLogger("error", false),
		AllowInsecureHTTP: true,
	})
	require.NoError(t, err)
	agentTS := httptest.NewServer(agentSrv.Handler)
	defer agentTS.Close()

	remote, err := nodeagent.NewInsecureRemoteExecutor(agentTS.URL)
	require.NoError(t, err)

	// Replace the worker's executor with the remote one.
	env.Worker = &outbox.Worker{
		DB: env.DB, Outbox: env.Service.Outbox, Operations: env.Service.Operations,
		Peers: env.Service.Peers, Profiles: env.Service.Profiles, Nodes: env.Service.Nodes,
		Executor: remote, Logger: obs.NewLogger("error", false),
	}

	// Create a peer.
	resp := postJSON(t, client, env.Server.URL+"/v1/tenants/acme/peers",
		bearer, "remote-1", map[string]any{
			"external_id":  "remote-user",
			"profile_name": env.Profile.Name,
		})
	require.Equal(t, http.StatusAccepted, resp.StatusCode)
	var cr api.CreatePeerResponse
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&cr))

	// Worker drives the agent over HTTP -> fake executor.
	ran, err := env.Worker.RunOnce(context.Background())
	require.NoError(t, err)
	require.True(t, ran)

	// Use the remote executor directly to sanity-check the dump endpoint.
	_, peers, err := remote.ShowDump(context.Background(), env.Node.InterfaceName)
	require.NoError(t, err)
	require.Len(t, peers, 1)
	require.Equal(t, cr.PublicKey, peers[0].PublicKey)
	require.Equal(t, cr.PresharedKey, peers[0].PresharedKey)
}

// silence unused import for build configurations where this is the only file.
var _ awg.Executor = (*nodeagent.RemoteExecutor)(nil)

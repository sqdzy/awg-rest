// Package outbox implements the durable-apply worker that drives the AWG
// node-agent. It pulls jobs from the outbox table (FOR UPDATE SKIP LOCKED),
// renders the desired interface configuration, and asks the executor to apply
// it. The reconciler is the *only* writer to the runtime; the API/worker only
// touch desired state.
package outbox

import (
	"context"
	"errors"
	"log/slog"
	"path"
	"strings"
	"time"

	"github.com/awg-rest/awg-rest/internal/awg"
	"github.com/awg-rest/awg-rest/internal/domain"
	"github.com/awg-rest/awg-rest/internal/render"
	"github.com/awg-rest/awg-rest/internal/repo"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// Worker pulls outbox jobs and brings the runtime to desired state.
type Worker struct {
	DB         *repo.DB
	Outbox     *repo.Outbox
	Operations *repo.Operations
	Peers      *repo.Peers
	Profiles   *repo.Profiles
	Nodes      *repo.Nodes
	Executor   awg.Executor
	Logger     *slog.Logger

	// BootstrapConfigDir points to node-local awg-quick configs used only when
	// an interface is missing and must be brought up before syncconf. The files
	// contain server private keys and are never rendered by the API.
	// Default: /etc/amnezia/bootstrap.
	BootstrapConfigDir string

	// PollInterval controls idle backoff. Default: 500ms.
	PollInterval time.Duration
	// LeaseFor is how long a claimed job remains leased. Default: 30s.
	LeaseFor time.Duration
}

// Run is the worker main loop. It returns when ctx is cancelled.
func (w *Worker) Run(ctx context.Context) error {
	if w.PollInterval == 0 {
		w.PollInterval = 500 * time.Millisecond
	}
	if w.LeaseFor == 0 {
		w.LeaseFor = 30 * time.Second
	}
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		job, err := w.Outbox.ClaimNext(ctx, w.LeaseFor)
		if err != nil {
			w.Logger.ErrorContext(ctx, "claim job", "err", err)
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(w.PollInterval):
			}
			continue
		}
		if job == nil {
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(w.PollInterval):
			}
			continue
		}
		w.processJob(ctx, job)
	}
}

// RunOnce processes at most one job and returns. Useful for tests.
func (w *Worker) RunOnce(ctx context.Context) (bool, error) {
	if w.LeaseFor == 0 {
		w.LeaseFor = 30 * time.Second
	}
	job, err := w.Outbox.ClaimNext(ctx, w.LeaseFor)
	if err != nil {
		return false, err
	}
	if job == nil {
		return false, nil
	}
	w.processJob(ctx, job)
	return true, nil
}

// processJob applies a single outbox job. All errors are funneled into the
// outbox/operations status fields; the worker never crashes on per-job error.
func (w *Worker) processJob(ctx context.Context, job *repo.Job) {
	logger := w.Logger.With("job_id", job.ID, "kind", job.Kind, "node_id", job.NodeID)

	if err := w.applyNode(ctx, job.NodeID); err != nil {
		retryable := classifyRetryable(err)
		_ = w.Outbox.Fail(ctx, job.ID, err.Error(), retryable)
		if job.OperationID != nil {
			_ = w.Operations.MarkFailed(ctx, *job.OperationID, "apply_failed", err.Error(), retryable)
		}
		logger.ErrorContext(ctx, "apply failed", "err", err, "retryable", retryable, "attempts", job.Attempts)
		return
	}

	if err := w.Outbox.Complete(ctx, job.ID); err != nil {
		logger.ErrorContext(ctx, "complete job", "err", err)
		return
	}
	if job.OperationID != nil {
		if err := w.Operations.MarkApplied(ctx, *job.OperationID); err != nil {
			logger.ErrorContext(ctx, "mark op applied", "err", err)
		}
	}
	logger.InfoContext(ctx, "applied")
}

// Reconcile triggers a full apply for one node, regardless of outbox state.
// Used for restart sync and manual `:reconcile` API calls.
func (w *Worker) Reconcile(ctx context.Context, nodeID uuid.UUID) error {
	return w.applyNode(ctx, nodeID)
}

// applyNode rebuilds the rendered config from desired state and reconciles
// runtime via the executor.
func (w *Worker) applyNode(ctx context.Context, nodeID uuid.UUID) error {
	node, err := w.Nodes.GetByID(ctx, nodeID)
	if err != nil {
		return err
	}
	peers, err := w.Peers.ListByNodeIncludingRevoked(ctx, nodeID)
	if err != nil {
		return err
	}

	// All peers on a node share the same interface (1 interface per node in
	// the initial deployment model). When the desired set becomes empty (e.g.
	// the last peer was revoked) we MUST still push an empty configuration so
	// the runtime drops the stale peer; we therefore look up the profile from
	// either an active peer or the most recent revoked one.
	profile, err := w.lookupNodeProfile(ctx, nodeID)
	if err != nil {
		return err
	}
	if profile == nil {
		// No peer has ever been provisioned on this node — nothing to do.
		return nil
	}

	entries := make([]render.PeerEntry, 0, len(peers))
	for _, p := range peers {
		if p.State == domain.PeerStateRevoked {
			continue
		}
		entries = append(entries, render.PeerEntry{
			PublicKey:  p.PublicKey,
			AllowedIPs: []string{p.AllowedIP.String()},
			Keepalive:  25,
			Comment:    p.DisplayName,
		})
	}

	cfg := render.Server(render.Interface{
		ListenPort: node.BasePort,
	}, *profile, entries)

	if err := w.Executor.SyncConf(ctx, node.InterfaceName, cfg); err != nil {
		if !isMissingInterfaceError(err) {
			return err
		}
		if upErr := w.Executor.InterfaceUp(ctx, node.InterfaceName, w.bootstrapConfigPath(node.InterfaceName)); upErr != nil {
			return err
		}
		if err := w.Executor.SyncConf(ctx, node.InterfaceName, cfg); err != nil {
			return err
		}
	}

	// Pull runtime stats and update peer_runtime + applied_revision.
	_, runtimePeers, err := w.Executor.ShowDump(ctx, node.InterfaceName)
	if err != nil {
		return err
	}
	return w.persistRuntime(ctx, peers, runtimePeers)
}

func (w *Worker) bootstrapConfigPath(iface string) string {
	dir := w.BootstrapConfigDir
	if dir == "" {
		dir = "/etc/amnezia/bootstrap"
	}
	return path.Join(dir, iface+".conf")
}

func isMissingInterfaceError(err error) bool {
	if err == nil {
		return false
	}
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "interface not found") ||
		strings.Contains(msg, "cannot find device") ||
		strings.Contains(msg, "no such device")
}

// lookupNodeProfile returns the profile associated with the most recently
// updated peer on the node — including revoked ones — so that we can still
// render an empty interface config when the last peer is revoked.
func (w *Worker) lookupNodeProfile(ctx context.Context, nodeID uuid.UUID) (*domain.ProtocolProfile, error) {
	row := w.DB.Pool.QueryRow(ctx,
		`SELECT profile_id FROM peers WHERE node_id = $1 ORDER BY updated_at DESC LIMIT 1`,
		nodeID)
	var pid uuid.UUID
	if err := row.Scan(&pid); err != nil {
		// pgx returns ErrNoRows; surface as nil so applyNode returns clean.
		return nil, nil
	}
	return w.Profiles.GetByID(ctx, pid)
}

func (w *Worker) persistRuntime(ctx context.Context, desired []domain.Peer, runtime []awg.PeerRuntime) error {
	byPub := make(map[string]awg.PeerRuntime, len(runtime))
	for _, r := range runtime {
		byPub[r.PublicKey] = r
	}
	return w.DB.InTx(ctx, func(tx pgx.Tx) error {
		for _, p := range desired {
			rt, present := byPub[p.PublicKey]
			var lh *time.Time
			if !rt.LastHandshake.IsZero() {
				lh = &rt.LastHandshake
			}
			_, err := tx.Exec(ctx, `
INSERT INTO peer_runtime(peer_id, last_handshake_at, rx_bytes, tx_bytes, runtime_present, last_runtime_sync_at, endpoint)
VALUES ($1, $2, $3, $4, $5, now(), $6)
ON CONFLICT (peer_id) DO UPDATE SET
  last_handshake_at = COALESCE(EXCLUDED.last_handshake_at, peer_runtime.last_handshake_at),
  rx_bytes = EXCLUDED.rx_bytes,
  tx_bytes = EXCLUDED.tx_bytes,
  runtime_present = EXCLUDED.runtime_present,
  last_runtime_sync_at = EXCLUDED.last_runtime_sync_at,
  endpoint = EXCLUDED.endpoint`,
				p.ID, lh, rt.RxBytes, rt.TxBytes, present, rt.Endpoint,
			)
			if err != nil {
				return err
			}
			if err := w.Peers.MarkApplied(ctx, tx, p.ID, p.DesiredRevision); err != nil {
				return err
			}
		}
		return nil
	})
}

// classifyRetryable inspects an error to decide whether the worker should
// retry it via lease expiry. Errors from missing kernel module / interface
// are retryable; validation errors are terminal.
func classifyRetryable(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, awg.ErrNotImplemented) {
		return false
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return true
	}
	msg := err.Error()
	switch {
	case strings.Contains(msg, "validation"),
		strings.Contains(msg, "invalid"):
		return false
	}
	return true
}

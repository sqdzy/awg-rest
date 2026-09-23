package repo

import (
	"context"
	"errors"
	"time"

	"github.com/awg-rest/awg-rest/internal/domain"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// Nodes manages vpn_nodes.
type Nodes struct{ DB *DB }

// Insert creates a new VPN node entry.
func (r *Nodes) Insert(ctx context.Context, n domain.Node) (*domain.Node, error) {
	return insertNode(ctx, r.DB.Pool, n)
}

// InsertTx creates a node inside an existing transaction.
func (r *Nodes) InsertTx(ctx context.Context, tx pgx.Tx, n domain.Node) (*domain.Node, error) {
	return insertNode(ctx, tx, n)
}

type nodeRowQuerier interface {
	QueryRow(context.Context, string, ...any) pgx.Row
}

func insertNode(ctx context.Context, qx nodeRowQuerier, n domain.Node) (*domain.Node, error) {
	const q = `
INSERT INTO vpn_nodes(region, hostname, public_endpoint, base_port, interface_name, server_public_key, profile_id)
VALUES ($1,$2,$3,$4,$5,$6,$7)
RETURNING id, profile_id, region, hostname, public_endpoint, base_port, interface_name, server_public_key, status, agent_last_seen_at, created_at`
	var out domain.Node
	row := qx.QueryRow(ctx, q,
		n.Region, n.Hostname, n.PublicEndpoint, n.BasePort,
		n.InterfaceName, n.ServerPublicKey, n.ProfileID,
	)
	if err := row.Scan(
		&out.ID, &out.ProfileID, &out.Region, &out.Hostname, &out.PublicEndpoint, &out.BasePort,
		&out.InterfaceName, &out.ServerPublicKey, &out.Status, &out.AgentLastSeenAt, &out.CreatedAt,
	); err != nil {
		return nil, err
	}
	return &out, nil
}

// GetByID fetches a node by id.
func (r *Nodes) GetByID(ctx context.Context, id uuid.UUID) (*domain.Node, error) {
	const q = `
SELECT id, profile_id, region, hostname, public_endpoint, base_port, interface_name, server_public_key, status, agent_last_seen_at, created_at
FROM vpn_nodes WHERE id = $1`
	var out domain.Node
	row := r.DB.Pool.QueryRow(ctx, q, id)
	if err := row.Scan(
		&out.ID, &out.ProfileID, &out.Region, &out.Hostname, &out.PublicEndpoint, &out.BasePort,
		&out.InterfaceName, &out.ServerPublicKey, &out.Status, &out.AgentLastSeenAt, &out.CreatedAt,
	); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, domain.ErrNotFound
		}
		return nil, err
	}
	return &out, nil
}

// MarkSeen records that the agent for node has just reported.
func (r *Nodes) MarkSeen(ctx context.Context, id uuid.UUID, status string) error {
	_, err := r.DB.Pool.Exec(ctx,
		`UPDATE vpn_nodes SET agent_last_seen_at = $1, status = $2 WHERE id = $3`,
		time.Now().UTC(), status, id)
	return err
}

// PickFirst returns the implicit/default node used when callers omit node_id.
// During staged migration, legacy V1/V2 nodes remain ahead of V3.1 nodes so
// adding a parallel V3.1 canary cannot silently move old API clients.
func (r *Nodes) PickFirst(ctx context.Context) (*domain.Node, error) {
	const q = `
SELECT n.id, n.profile_id, n.region, n.hostname, n.public_endpoint, n.base_port, n.interface_name, n.server_public_key, n.status, n.agent_last_seen_at, n.created_at
FROM vpn_nodes n
LEFT JOIN protocol_profiles p ON p.id = n.profile_id
ORDER BY CASE WHEN p.protocol_version = 'v3.1' THEN 1 ELSE 0 END, n.hostname
LIMIT 1`
	var out domain.Node
	row := r.DB.Pool.QueryRow(ctx, q)
	if err := row.Scan(
		&out.ID, &out.ProfileID, &out.Region, &out.Hostname, &out.PublicEndpoint, &out.BasePort,
		&out.InterfaceName, &out.ServerPublicKey, &out.Status, &out.AgentLastSeenAt, &out.CreatedAt,
	); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, domain.ErrNotFound
		}
		return nil, err
	}
	return &out, nil
}

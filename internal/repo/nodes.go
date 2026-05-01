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
	const q = `
INSERT INTO vpn_nodes(region, hostname, public_endpoint, base_port, interface_name, server_public_key)
VALUES ($1,$2,$3,$4,$5,$6)
RETURNING id, region, hostname, public_endpoint, base_port, interface_name, server_public_key, status, agent_last_seen_at, created_at`
	var out domain.Node
	row := r.DB.Pool.QueryRow(ctx, q,
		n.Region, n.Hostname, n.PublicEndpoint, n.BasePort,
		n.InterfaceName, n.ServerPublicKey,
	)
	if err := row.Scan(
		&out.ID, &out.Region, &out.Hostname, &out.PublicEndpoint, &out.BasePort,
		&out.InterfaceName, &out.ServerPublicKey, &out.Status, &out.AgentLastSeenAt, &out.CreatedAt,
	); err != nil {
		return nil, err
	}
	return &out, nil
}

// GetByID fetches a node by id.
func (r *Nodes) GetByID(ctx context.Context, id uuid.UUID) (*domain.Node, error) {
	const q = `
SELECT id, region, hostname, public_endpoint, base_port, interface_name, server_public_key, status, agent_last_seen_at, created_at
FROM vpn_nodes WHERE id = $1`
	var out domain.Node
	row := r.DB.Pool.QueryRow(ctx, q, id)
	if err := row.Scan(
		&out.ID, &out.Region, &out.Hostname, &out.PublicEndpoint, &out.BasePort,
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

// PickFirst returns the first node (deterministic by hostname). The control
// plane uses this when the API caller does not pin a node.
func (r *Nodes) PickFirst(ctx context.Context) (*domain.Node, error) {
	const q = `
SELECT id, region, hostname, public_endpoint, base_port, interface_name, server_public_key, status, agent_last_seen_at, created_at
FROM vpn_nodes ORDER BY hostname LIMIT 1`
	var out domain.Node
	row := r.DB.Pool.QueryRow(ctx, q)
	if err := row.Scan(
		&out.ID, &out.Region, &out.Hostname, &out.PublicEndpoint, &out.BasePort,
		&out.InterfaceName, &out.ServerPublicKey, &out.Status, &out.AgentLastSeenAt, &out.CreatedAt,
	); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, domain.ErrNotFound
		}
		return nil, err
	}
	return &out, nil
}

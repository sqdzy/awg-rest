package repo

import (
	"context"
	"net/netip"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// Pools manages the IPAM source-of-truth rows.
type Pools struct{ DB *DB }

// CreatePool registers a new CIDR pool for a node.
func (r *Pools) CreatePool(ctx context.Context, tenantID uuid.UUID, nodeID uuid.UUID, cidr netip.Prefix) (uuid.UUID, error) {
	return createPool(ctx, r.DB.Pool, tenantID, nodeID, cidr)
}

// CreatePoolTx registers a new CIDR pool inside an existing transaction.
func (r *Pools) CreatePoolTx(ctx context.Context, tx pgx.Tx, tenantID uuid.UUID, nodeID uuid.UUID, cidr netip.Prefix) (uuid.UUID, error) {
	return createPool(ctx, tx, tenantID, nodeID, cidr)
}

type poolRowQuerier interface {
	QueryRow(context.Context, string, ...any) pgx.Row
}

func createPool(ctx context.Context, qx poolRowQuerier, tenantID uuid.UUID, nodeID uuid.UUID, cidr netip.Prefix) (uuid.UUID, error) {
	var id uuid.UUID
	row := qx.QueryRow(ctx,
		`INSERT INTO address_pools(tenant_id, node_id, cidr) VALUES ($1, $2, $3::cidr) RETURNING id`,
		tenantID, nodeID, cidr.String())
	if err := row.Scan(&id); err != nil {
		return uuid.Nil, err
	}
	return id, nil
}

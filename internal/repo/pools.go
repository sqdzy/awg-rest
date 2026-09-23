package repo

import (
	"context"
	"net/netip"

	"github.com/google/uuid"
)

// Pools manages the IPAM source-of-truth rows.
type Pools struct{ DB *DB }

// CreatePool registers a new CIDR pool for a node.
func (r *Pools) CreatePool(ctx context.Context, tenantID uuid.UUID, nodeID uuid.UUID, cidr netip.Prefix) (uuid.UUID, error) {
	var id uuid.UUID
	row := r.DB.Pool.QueryRow(ctx,
		`INSERT INTO address_pools(tenant_id, node_id, cidr) VALUES ($1, $2, $3::cidr) RETURNING id`,
		tenantID, nodeID, cidr.String())
	if err := row.Scan(&id); err != nil {
		return uuid.Nil, err
	}
	return id, nil
}


// CIDRsByNode returns all address pools registered for a node.
func (r *Pools) CIDRsByNode(ctx context.Context, nodeID uuid.UUID) ([]netip.Prefix, error) {
	rows, err := r.DB.Pool.Query(ctx,
		`SELECT cidr::text FROM address_pools WHERE node_id = $1 ORDER BY cidr::text`, nodeID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []netip.Prefix
	for rows.Next() {
		var raw string
		if err := rows.Scan(&raw); err != nil {
			return nil, err
		}
		prefix, err := netip.ParsePrefix(raw)
		if err != nil {
			return nil, err
		}
		out = append(out, prefix.Masked())
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return out, nil
}

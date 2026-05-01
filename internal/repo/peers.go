package repo

import (
	"context"
	"errors"
	"fmt"
	"net/netip"
	"time"

	"github.com/awg-rest/awg-rest/internal/domain"
	"github.com/awg-rest/awg-rest/internal/ipam"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// Peers is the desired-state repository for VPN peers, plus the IPAM
// allocator (because reservation and insert must occur in one transaction).
type Peers struct{ DB *DB }

// InsertParams describes the new peer to create. AllowedIP is allocated
// inside the same transaction.
type InsertParams struct {
	TenantID        uuid.UUID
	NodeID          uuid.UUID
	ProfileID       uuid.UUID
	ExternalID      string
	DisplayName     string
	PublicKey       string
	PresharedKeyRef *string
	ExpiresAt       *time.Time
}

// AllocateAndInsert reserves an IP from the node's pool and inserts the peer
// row. Conflicting external_id returns domain.ErrPeerExists; pool exhaustion
// returns domain.ErrIPPoolExhausted.
func (r *Peers) AllocateAndInsert(ctx context.Context, tx pgx.Tx, p InsertParams) (*domain.Peer, error) {
	// Load and lock the address pool for the node.
	const lockPool = `
SELECT id, cidr::text, COALESCE(host(cursor), '') FROM address_pools
WHERE node_id = $1 AND tenant_id = $2 ORDER BY id LIMIT 1
FOR UPDATE`
	var poolID uuid.UUID
	var cidrStr, cursorStr string
	if err := tx.QueryRow(ctx, lockPool, p.NodeID, p.TenantID).Scan(&poolID, &cidrStr, &cursorStr); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, fmt.Errorf("no address pool configured for node %s", p.NodeID)
		}
		return nil, fmt.Errorf("lock pool: %w", err)
	}
	pool, err := netip.ParsePrefix(cidrStr)
	if err != nil {
		return nil, fmt.Errorf("parse pool cidr: %w", err)
	}
	var cursor netip.Addr
	if cursorStr != "" {
		cursor, _ = netip.ParseAddr(cursorStr)
	}

	// Load all already-allocated addresses on the node so the allocator skips them.
	rows, err := tx.Query(ctx,
		`SELECT host(allowed_ip) FROM peers WHERE node_id = $1 AND state <> 'revoked'`, p.NodeID)
	if err != nil {
		return nil, fmt.Errorf("load used ips: %w", err)
	}
	used := make(map[netip.Addr]struct{})
	for rows.Next() {
		var s string
		if err := rows.Scan(&s); err != nil {
			rows.Close()
			return nil, err
		}
		if a, err := netip.ParseAddr(s); err == nil {
			used[a] = struct{}{}
		}
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}

	allocation, err := ipam.Allocate(pool, cursor, reservedMap(used))
	if err != nil {
		if errors.Is(err, ipam.ErrPoolExhausted) {
			return nil, domain.ErrIPPoolExhausted
		}
		return nil, err
	}

	// Persist new cursor.
	if _, err := tx.Exec(ctx,
		`UPDATE address_pools SET cursor = $1::inet WHERE id = $2`,
		allocation.NextCursor.String(), poolID); err != nil {
		return nil, fmt.Errorf("update cursor: %w", err)
	}

	const ins = `
INSERT INTO peers(
    tenant_id, node_id, profile_id, external_id, display_name,
    public_key, preshared_key_ref, allowed_ip, state, desired_revision, applied_revision, expires_at
) VALUES ($1,$2,$3,$4,$5,$6,$7,$8::inet,$9,$10,$11,$12)
RETURNING id, tenant_id, node_id, profile_id, external_id, display_name,
          public_key, preshared_key_ref, host(allowed_ip), state,
          desired_revision, applied_revision, expires_at, revoked_at, created_at, updated_at`

	allowedIP := allocation.Address.String() + "/32"
	if allocation.Address.Is6() {
		allowedIP = allocation.Address.String() + "/128"
	}

	var out domain.Peer
	var allowedHost string
	var stateStr string
	var psk *string
	row := tx.QueryRow(ctx, ins,
		p.TenantID, p.NodeID, p.ProfileID, p.ExternalID, p.DisplayName,
		p.PublicKey, p.PresharedKeyRef, allowedIP, string(domain.PeerStatePending), 1, 0, p.ExpiresAt,
	)
	if err := row.Scan(
		&out.ID, &out.TenantID, &out.NodeID, &out.ProfileID, &out.ExternalID, &out.DisplayName,
		&out.PublicKey, &psk, &allowedHost, &stateStr,
		&out.DesiredRevision, &out.AppliedRevision, &out.ExpiresAt, &out.RevokedAt, &out.CreatedAt, &out.UpdatedAt,
	); err != nil {
		if IsUniqueViolation(err) {
			return nil, domain.ErrPeerExists
		}
		return nil, err
	}
	out.State = domain.PeerState(stateStr)
	out.PresharedKeyRef = psk

	prefixSize := 32
	if allocation.Address.Is6() {
		prefixSize = 128
	}
	parsed, err := netip.ParsePrefix(fmt.Sprintf("%s/%d", allowedHost, prefixSize))
	if err != nil {
		return nil, err
	}
	out.AllowedIP = parsed

	return &out, nil
}

// GetByID loads a peer.
func (r *Peers) GetByID(ctx context.Context, id uuid.UUID) (*domain.Peer, error) {
	const q = `
SELECT id, tenant_id, node_id, profile_id, external_id, display_name,
       public_key, preshared_key_ref, host(allowed_ip), state,
       desired_revision, applied_revision, expires_at, revoked_at, created_at, updated_at
FROM peers WHERE id = $1`
	row := r.DB.Pool.QueryRow(ctx, q, id)
	return scanPeer(row)
}

// GetByExternalID loads a peer by tenant_id + external_id.
func (r *Peers) GetByExternalID(ctx context.Context, tenantID uuid.UUID, extID string) (*domain.Peer, error) {
	const q = `
SELECT id, tenant_id, node_id, profile_id, external_id, display_name,
       public_key, preshared_key_ref, host(allowed_ip), state,
       desired_revision, applied_revision, expires_at, revoked_at, created_at, updated_at
FROM peers WHERE tenant_id = $1 AND external_id = $2`
	row := r.DB.Pool.QueryRow(ctx, q, tenantID, extID)
	return scanPeer(row)
}

// ListByNode returns all non-revoked peers for a node, deterministically ordered.
func (r *Peers) ListByNode(ctx context.Context, nodeID uuid.UUID) ([]domain.Peer, error) {
	const q = `
SELECT id, tenant_id, node_id, profile_id, external_id, display_name,
       public_key, preshared_key_ref, host(allowed_ip), state,
       desired_revision, applied_revision, expires_at, revoked_at, created_at, updated_at
FROM peers WHERE node_id = $1 AND state <> 'revoked' ORDER BY public_key`
	return r.listByNodeQuery(ctx, q, nodeID)
}

// ListByNodeIncludingRevoked returns all desired peer rows for a node. Worker
// runtime persistence uses it to mark revoked peers applied after syncconf drops
// them from the live interface.
func (r *Peers) ListByNodeIncludingRevoked(ctx context.Context, nodeID uuid.UUID) ([]domain.Peer, error) {
	const q = `
SELECT id, tenant_id, node_id, profile_id, external_id, display_name,
       public_key, preshared_key_ref, host(allowed_ip), state,
       desired_revision, applied_revision, expires_at, revoked_at, created_at, updated_at
FROM peers WHERE node_id = $1 ORDER BY public_key`
	return r.listByNodeQuery(ctx, q, nodeID)
}

func (r *Peers) listByNodeQuery(ctx context.Context, q string, nodeID uuid.UUID) ([]domain.Peer, error) {
	rows, err := r.DB.Pool.Query(ctx, q, nodeID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []domain.Peer
	for rows.Next() {
		p, err := scanPeer(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *p)
	}
	return out, rows.Err()
}

// MarkRevoked marks a peer as revoked and bumps desired revision.
func (r *Peers) MarkRevoked(ctx context.Context, tx pgx.Tx, id uuid.UUID, when time.Time) (*domain.Peer, error) {
	const q = `
UPDATE peers SET state = 'revoked', revoked_at = $2, desired_revision = desired_revision + 1, updated_at = now()
WHERE id = $1
RETURNING id, tenant_id, node_id, profile_id, external_id, display_name,
          public_key, preshared_key_ref, host(allowed_ip), state,
          desired_revision, applied_revision, expires_at, revoked_at, created_at, updated_at`
	row := tx.QueryRow(ctx, q, id, when)
	return scanPeer(row)
}

// MarkApplied bumps applied_revision and updates state from pending->active.
func (r *Peers) MarkApplied(ctx context.Context, tx pgx.Tx, id uuid.UUID, revision int64) error {
	const q = `
UPDATE peers SET applied_revision = GREATEST(applied_revision, $2),
                 state = CASE WHEN state = 'pending' THEN 'active' ELSE state END,
                 updated_at = now()
WHERE id = $1`
	_, err := tx.Exec(ctx, q, id, revision)
	return err
}

func scanPeer(row pgx.Row) (*domain.Peer, error) {
	var out domain.Peer
	var allowedHost, stateStr string
	var psk *string
	if err := row.Scan(
		&out.ID, &out.TenantID, &out.NodeID, &out.ProfileID, &out.ExternalID, &out.DisplayName,
		&out.PublicKey, &psk, &allowedHost, &stateStr,
		&out.DesiredRevision, &out.AppliedRevision, &out.ExpiresAt, &out.RevokedAt, &out.CreatedAt, &out.UpdatedAt,
	); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, domain.ErrNotFound
		}
		return nil, err
	}
	out.State = domain.PeerState(stateStr)
	out.PresharedKeyRef = psk
	addr, err := netip.ParseAddr(allowedHost)
	if err != nil {
		return nil, err
	}
	bits := 32
	if addr.Is6() {
		bits = 128
	}
	out.AllowedIP, _ = netip.ParsePrefix(fmt.Sprintf("%s/%d", allowedHost, bits))
	return &out, nil
}

// reservedMap adapts a set into ipam.Reserved.
type reservedMap map[netip.Addr]struct{}

func (r reservedMap) IsReserved(a netip.Addr) bool { _, ok := r[a]; return ok }

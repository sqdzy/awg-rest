package repo

import (
	"context"
	"errors"

	"github.com/awg-rest/awg-rest/internal/domain"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// Tenants is the repository for tenant rows.
type Tenants struct{ DB *DB }

// Upsert inserts a tenant or returns the existing one with the same slug.
func (r *Tenants) Upsert(ctx context.Context, slug string) (*domain.Tenant, error) {
	const q = `
INSERT INTO tenants(slug) VALUES ($1)
ON CONFLICT (slug) DO UPDATE SET slug = EXCLUDED.slug
RETURNING id, slug, status, created_at`
	var t domain.Tenant
	row := r.DB.Pool.QueryRow(ctx, q, slug)
	if err := row.Scan(&t.ID, &t.Slug, &t.Status, &t.CreatedAt); err != nil {
		return nil, err
	}
	return &t, nil
}

// GetBySlug fetches a tenant by slug.
func (r *Tenants) GetBySlug(ctx context.Context, slug string) (*domain.Tenant, error) {
	var t domain.Tenant
	row := r.DB.Pool.QueryRow(ctx,
		`SELECT id, slug, status, created_at FROM tenants WHERE slug = $1`, slug)
	if err := row.Scan(&t.ID, &t.Slug, &t.Status, &t.CreatedAt); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, domain.ErrNotFound
		}
		return nil, err
	}
	return &t, nil
}

// GetByID fetches a tenant by id.
func (r *Tenants) GetByID(ctx context.Context, id uuid.UUID) (*domain.Tenant, error) {
	var t domain.Tenant
	row := r.DB.Pool.QueryRow(ctx,
		`SELECT id, slug, status, created_at FROM tenants WHERE id = $1`, id)
	if err := row.Scan(&t.ID, &t.Slug, &t.Status, &t.CreatedAt); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, domain.ErrNotFound
		}
		return nil, err
	}
	return &t, nil
}

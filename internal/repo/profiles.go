package repo

import (
	"context"
	"errors"

	"github.com/awg-rest/awg-rest/internal/domain"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// Profiles handles AmneziaWG protocol profiles.
type Profiles struct{ DB *DB }

// Insert persists a profile after server-side validation.
func (r *Profiles) Insert(ctx context.Context, p domain.ProtocolProfile) (*domain.ProtocolProfile, error) {
	if err := p.Validate(); err != nil {
		return nil, err
	}
	const q = `
INSERT INTO protocol_profiles(
    name, protocol_version, jc, jmin, jmax, s1, s2, s3, s4,
    h1_min, h1_max, h2_min, h2_max, h3_min, h3_max, h4_min, h4_max,
    i1, i2, i3, i4, i5, listen_port_policy
) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17,$18,$19,$20,$21,$22,$23)
RETURNING id, name, protocol_version, jc, jmin, jmax, s1, s2, s3, s4,
          h1_min, h1_max, h2_min, h2_max, h3_min, h3_max, h4_min, h4_max,
          i1, i2, i3, i4, i5, listen_port_policy, created_at`

	row := r.DB.Pool.QueryRow(ctx, q,
		p.Name, string(p.ProtocolVersion), p.Jc, p.Jmin, p.Jmax, p.S1, p.S2, p.S3, p.S4,
		p.H1.Min, p.H1.Max, p.H2.Min, p.H2.Max, p.H3.Min, p.H3.Max, p.H4.Min, p.H4.Max,
		nullable(p.I1), nullable(p.I2), nullable(p.I3), nullable(p.I4), nullable(p.I5),
		p.ListenPortPolicy,
	)
	var out domain.ProtocolProfile
	var version string
	var i1, i2, i3, i4, i5 *string
	if err := row.Scan(
		&out.ID, &out.Name, &version, &out.Jc, &out.Jmin, &out.Jmax, &out.S1, &out.S2, &out.S3, &out.S4,
		&out.H1.Min, &out.H1.Max, &out.H2.Min, &out.H2.Max, &out.H3.Min, &out.H3.Max, &out.H4.Min, &out.H4.Max,
		&i1, &i2, &i3, &i4, &i5, &out.ListenPortPolicy, &out.CreatedAt,
	); err != nil {
		return nil, err
	}
	out.ProtocolVersion = domain.ProtocolVersion(version)
	out.I1 = deref(i1)
	out.I2 = deref(i2)
	out.I3 = deref(i3)
	out.I4 = deref(i4)
	out.I5 = deref(i5)
	return &out, nil
}

// GetByID loads a profile.
func (r *Profiles) GetByID(ctx context.Context, id uuid.UUID) (*domain.ProtocolProfile, error) {
	const q = `
SELECT id, name, protocol_version, jc, jmin, jmax, s1, s2, s3, s4,
       h1_min, h1_max, h2_min, h2_max, h3_min, h3_max, h4_min, h4_max,
       i1, i2, i3, i4, i5, listen_port_policy, created_at
FROM protocol_profiles WHERE id = $1`
	row := r.DB.Pool.QueryRow(ctx, q, id)
	return scanProfile(row)
}

// GetByName loads a profile by name.
func (r *Profiles) GetByName(ctx context.Context, name string) (*domain.ProtocolProfile, error) {
	const q = `
SELECT id, name, protocol_version, jc, jmin, jmax, s1, s2, s3, s4,
       h1_min, h1_max, h2_min, h2_max, h3_min, h3_max, h4_min, h4_max,
       i1, i2, i3, i4, i5, listen_port_policy, created_at
FROM protocol_profiles WHERE name = $1`
	row := r.DB.Pool.QueryRow(ctx, q, name)
	return scanProfile(row)
}

func scanProfile(row pgx.Row) (*domain.ProtocolProfile, error) {
	var out domain.ProtocolProfile
	var version string
	var i1, i2, i3, i4, i5 *string
	if err := row.Scan(
		&out.ID, &out.Name, &version, &out.Jc, &out.Jmin, &out.Jmax, &out.S1, &out.S2, &out.S3, &out.S4,
		&out.H1.Min, &out.H1.Max, &out.H2.Min, &out.H2.Max, &out.H3.Min, &out.H3.Max, &out.H4.Min, &out.H4.Max,
		&i1, &i2, &i3, &i4, &i5, &out.ListenPortPolicy, &out.CreatedAt,
	); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, domain.ErrNotFound
		}
		return nil, err
	}
	out.ProtocolVersion = domain.ProtocolVersion(version)
	out.I1, out.I2, out.I3, out.I4, out.I5 = deref(i1), deref(i2), deref(i3), deref(i4), deref(i5)
	return &out, nil
}

func nullable(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

func deref(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

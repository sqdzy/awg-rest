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

const profileReturningColumns = `
id, name, protocol_version, jc, jmin, jmax, s1, s2, s3, s4,
h1_min, h1_max, h2_min, h2_max, h3_min, h3_max, h4_min, h4_max,
i1, i2, i3, i4, i5, listen_port_policy,
header_protection_key,
content_padding_addition_min, content_padding_addition_max,
rekey_after_time_min, rekey_after_time_max,
rekey_timeout_min, rekey_timeout_max,
reject_after_time_min, reject_after_time_max,
keepalive_timeout_min, keepalive_timeout_max,
max_handshake_attempts_min, max_handshake_attempts_max,
persistent_keepalive_min, persistent_keepalive_max,
random_trailers, disable_cookies, created_at`

// Insert persists a profile after server-side validation.
func (r *Profiles) Insert(ctx context.Context, p domain.ProtocolProfile) (*domain.ProtocolProfile, error) {
	return insertProfile(ctx, r.DB.Pool, p)
}

// InsertTx persists a profile inside an existing transaction.
func (r *Profiles) InsertTx(ctx context.Context, tx pgx.Tx, p domain.ProtocolProfile) (*domain.ProtocolProfile, error) {
	return insertProfile(ctx, tx, p)
}

type profileRowQuerier interface {
	QueryRow(context.Context, string, ...any) pgx.Row
}

func insertProfile(ctx context.Context, qx profileRowQuerier, p domain.ProtocolProfile) (*domain.ProtocolProfile, error) {
	if err := p.Validate(); err != nil {
		return nil, err
	}

	cpMin, cpMax := rangeDB(p.IsV31(), p.ContentPaddingAddition)
	raMin, raMax := rangeDB(p.IsV31(), p.RekeyAfterTime)
	rtMin, rtMax := rangeDB(p.IsV31(), p.RekeyTimeout)
	rjMin, rjMax := rangeDB(p.IsV31(), p.RejectAfterTime)
	kaMin, kaMax := rangeDB(p.IsV31(), p.KeepaliveTimeout)
	mhMin, mhMax := rangeDB(p.IsV31(), p.MaxHandshakeAttempts)
	pkMin, pkMax := rangeDB(p.IsV31(), p.PersistentKeepalive)

	q := `
INSERT INTO protocol_profiles(
    name, protocol_version, jc, jmin, jmax, s1, s2, s3, s4,
    h1_min, h1_max, h2_min, h2_max, h3_min, h3_max, h4_min, h4_max,
    i1, i2, i3, i4, i5, listen_port_policy,
    header_protection_key,
    content_padding_addition_min, content_padding_addition_max,
    rekey_after_time_min, rekey_after_time_max,
    rekey_timeout_min, rekey_timeout_max,
    reject_after_time_min, reject_after_time_max,
    keepalive_timeout_min, keepalive_timeout_max,
    max_handshake_attempts_min, max_handshake_attempts_max,
    persistent_keepalive_min, persistent_keepalive_max,
    random_trailers, disable_cookies
) VALUES (
    $1,$2,$3,$4,$5,$6,$7,$8,$9,
    $10,$11,$12,$13,$14,$15,$16,$17,
    $18,$19,$20,$21,$22,$23,
    $24,$25,$26,$27,$28,$29,$30,$31,$32,$33,$34,$35,$36,$37,$38,$39,$40
)
RETURNING ` + profileReturningColumns

	row := qx.QueryRow(ctx, q,
		p.Name, string(p.ProtocolVersion), p.Jc, p.Jmin, p.Jmax, p.S1, p.S2, p.S3, p.S4,
		p.H1.Min, p.H1.Max, p.H2.Min, p.H2.Max, p.H3.Min, p.H3.Max, p.H4.Min, p.H4.Max,
		nullable(p.I1), nullable(p.I2), nullable(p.I3), nullable(p.I4), nullable(p.I5),
		p.ListenPortPolicy,
		nullableWhen(p.IsV31(), p.HeaderProtectionKey),
		cpMin, cpMax, raMin, raMax, rtMin, rtMax, rjMin, rjMax, kaMin, kaMax, mhMin, mhMax, pkMin, pkMax,
		boolWhen(p.IsV31(), p.RandomTrailers), boolWhen(p.IsV31(), p.DisableCookies),
	)
	return scanProfile(row)
}

// GetByID loads a profile.
func (r *Profiles) GetByID(ctx context.Context, id uuid.UUID) (*domain.ProtocolProfile, error) {
	q := `SELECT ` + profileReturningColumns + ` FROM protocol_profiles WHERE id = $1`
	return scanProfile(r.DB.Pool.QueryRow(ctx, q, id))
}

// GetByName loads a profile by name.
func (r *Profiles) GetByName(ctx context.Context, name string) (*domain.ProtocolProfile, error) {
	q := `SELECT ` + profileReturningColumns + ` FROM protocol_profiles WHERE name = $1`
	return scanProfile(r.DB.Pool.QueryRow(ctx, q, name))
}

func scanProfile(row pgx.Row) (*domain.ProtocolProfile, error) {
	var out domain.ProtocolProfile
	var version string
	var i1, i2, i3, i4, i5 *string
	var headerProtectionKey *string
	var cpMin, cpMax, raMin, raMax, rtMin, rtMax *int32
	var rjMin, rjMax, kaMin, kaMax, mhMin, mhMax, pkMin, pkMax *int32
	var randomTrailers, disableCookies *bool

	if err := row.Scan(
		&out.ID, &out.Name, &version, &out.Jc, &out.Jmin, &out.Jmax, &out.S1, &out.S2, &out.S3, &out.S4,
		&out.H1.Min, &out.H1.Max, &out.H2.Min, &out.H2.Max, &out.H3.Min, &out.H3.Max, &out.H4.Min, &out.H4.Max,
		&i1, &i2, &i3, &i4, &i5, &out.ListenPortPolicy,
		&headerProtectionKey,
		&cpMin, &cpMax, &raMin, &raMax, &rtMin, &rtMax,
		&rjMin, &rjMax, &kaMin, &kaMax, &mhMin, &mhMax, &pkMin, &pkMax,
		&randomTrailers, &disableCookies, &out.CreatedAt,
	); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, domain.ErrNotFound
		}
		return nil, err
	}

	out.ProtocolVersion = domain.ProtocolVersion(version)
	out.I1, out.I2, out.I3, out.I4, out.I5 = deref(i1), deref(i2), deref(i3), deref(i4), deref(i5)
	out.HeaderProtectionKey = deref(headerProtectionKey)
	out.ContentPaddingAddition = rangeFromDB(cpMin, cpMax)
	out.RekeyAfterTime = rangeFromDB(raMin, raMax)
	out.RekeyTimeout = rangeFromDB(rtMin, rtMax)
	out.RejectAfterTime = rangeFromDB(rjMin, rjMax)
	out.KeepaliveTimeout = rangeFromDB(kaMin, kaMax)
	out.MaxHandshakeAttempts = rangeFromDB(mhMin, mhMax)
	out.PersistentKeepalive = rangeFromDB(pkMin, pkMax)
	out.RandomTrailers = derefBool(randomTrailers)
	out.DisableCookies = derefBool(disableCookies)
	return &out, nil
}

func rangeDB(enabled bool, r domain.Uint16Range) (*int32, *int32) {
	if !enabled || r.IsZero() {
		return nil, nil
	}
	lo, hi := int32(r.Min), int32(r.Max)
	return &lo, &hi
}

func rangeFromDB(lo, hi *int32) domain.Uint16Range {
	if lo == nil || hi == nil {
		return domain.Uint16Range{}
	}
	return domain.Uint16Range{Min: uint16(*lo), Max: uint16(*hi)}
}

func nullableWhen(enabled bool, s string) *string {
	if !enabled {
		return nil
	}
	return nullable(s)
}

func boolWhen(enabled, v bool) *bool {
	if !enabled {
		return nil
	}
	out := v
	return &out
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

func derefBool(v *bool) bool {
	return v != nil && *v
}

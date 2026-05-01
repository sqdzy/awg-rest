package repo

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/awg-rest/awg-rest/internal/domain"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// Idempotency manages POST/PATCH dedup records.
type Idempotency struct{ DB *DB }

// Record is the persisted result of a previously-completed write request.
type Record struct {
	OperationID    *uuid.UUID
	RequestHash    string
	ResponseStatus int
	ResponseBody   json.RawMessage
	ExpiresAt      time.Time
}

// Lookup returns the existing record for (tenant, key) or domain.ErrNotFound.
func (r *Idempotency) Lookup(ctx context.Context, tx pgx.Tx, tenantID uuid.UUID, key string) (*Record, error) {
	const q = `
SELECT operation_id, request_hash, response_status, response_body, expires_at
FROM idempotency_keys WHERE tenant_id = $1 AND idempotency_key = $2 AND expires_at > now()`
	var rec Record
	row := tx.QueryRow(ctx, q, tenantID, key)
	if err := row.Scan(&rec.OperationID, &rec.RequestHash, &rec.ResponseStatus, &rec.ResponseBody, &rec.ExpiresAt); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, domain.ErrNotFound
		}
		return nil, err
	}
	return &rec, nil
}

// Store persists a fresh record.
func (r *Idempotency) Store(ctx context.Context, tx pgx.Tx, tenantID uuid.UUID, key, requestHash string, opID *uuid.UUID, status int, body any, ttl time.Duration) error {
	bodyBytes, err := json.Marshal(body)
	if err != nil {
		return err
	}
	const q = `
INSERT INTO idempotency_keys(tenant_id, idempotency_key, request_hash, operation_id, response_status, response_body, expires_at)
VALUES ($1,$2,$3,$4,$5,$6,$7)`
	_, err = tx.Exec(ctx, q, tenantID, key, requestHash, opID, status, bodyBytes, time.Now().Add(ttl))
	return err
}

package repo

import (
	"context"
	"encoding/json"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// Audit appends to the audit_events table.
type Audit struct{ DB *DB }

// AppendParams describes a single audit event.
type AppendParams struct {
	TenantID       *uuid.UUID
	SubjectID      *uuid.UUID
	Action         string
	TargetType     string
	TargetID       string
	Before         any
	After          any
	RequestID      string
	IdempotencyKey string
}

// Append writes one audit event using the provided transaction (or pool if tx==nil).
func (r *Audit) Append(ctx context.Context, tx pgx.Tx, p AppendParams) error {
	var beforeJSON, afterJSON []byte
	var err error
	if p.Before != nil {
		if beforeJSON, err = json.Marshal(p.Before); err != nil {
			return err
		}
	}
	if p.After != nil {
		if afterJSON, err = json.Marshal(p.After); err != nil {
			return err
		}
	}
	const q = `
INSERT INTO audit_events(tenant_id, subject_id, action, target_type, target_id, before_json, after_json, request_id, idempotency_key)
VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9)`
	args := []any{p.TenantID, p.SubjectID, p.Action, p.TargetType, p.TargetID,
		beforeJSON, afterJSON, p.RequestID, p.IdempotencyKey}
	if tx != nil {
		_, err = tx.Exec(ctx, q, args...)
	} else {
		_, err = r.DB.Pool.Exec(ctx, q, args...)
	}
	return err
}

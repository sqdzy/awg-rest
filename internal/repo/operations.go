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

// Operations is the durable async-operation log.
type Operations struct{ DB *DB }

// Insert creates a new pending operation.
func (r *Operations) Insert(ctx context.Context, tx pgx.Tx, tenantID uuid.UUID, nodeID uuid.UUID, peerID *uuid.UUID, kind domain.OperationKind, requestHash string) (*domain.Operation, error) {
	const q = `
INSERT INTO operations(tenant_id, peer_id, node_id, kind, request_hash)
VALUES ($1, $2, $3, $4, $5)
RETURNING id, tenant_id, peer_id, node_id, kind, status, started_at, finished_at, COALESCE(error_code,''), COALESCE(error_message,'')`
	var out domain.Operation
	var kindStr, status string
	var ec, em string
	row := tx.QueryRow(ctx, q, tenantID, peerID, nodeID, string(kind), requestHash)
	if err := row.Scan(&out.ID, &out.TenantID, &out.PeerID, &out.NodeID, &kindStr, &status, &out.StartedAt, &out.FinishedAt, &ec, &em); err != nil {
		return nil, err
	}
	out.Kind = domain.OperationKind(kindStr)
	out.Status = domain.OperationStatus(status)
	out.ErrorCode, out.ErrorMessage = ec, em
	return &out, nil
}

// GetByID loads an operation.
func (r *Operations) GetByID(ctx context.Context, id uuid.UUID) (*domain.Operation, error) {
	const q = `
SELECT id, tenant_id, peer_id, node_id, kind, status, started_at, finished_at, COALESCE(error_code,''), COALESCE(error_message,'')
FROM operations WHERE id = $1`
	var out domain.Operation
	var kindStr, status, ec, em string
	row := r.DB.Pool.QueryRow(ctx, q, id)
	if err := row.Scan(&out.ID, &out.TenantID, &out.PeerID, &out.NodeID, &kindStr, &status, &out.StartedAt, &out.FinishedAt, &ec, &em); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, domain.ErrNotFound
		}
		return nil, err
	}
	out.Kind, out.Status, out.ErrorCode, out.ErrorMessage = domain.OperationKind(kindStr), domain.OperationStatus(status), ec, em
	return &out, nil
}

// MarkApplied transitions to applied and sets finished_at.
func (r *Operations) MarkApplied(ctx context.Context, id uuid.UUID) error {
	_, err := r.DB.Pool.Exec(ctx,
		`UPDATE operations SET status = 'applied', finished_at = $2 WHERE id = $1`,
		id, time.Now().UTC())
	return err
}

// MarkFailed sets the operation status with reason.
func (r *Operations) MarkFailed(ctx context.Context, id uuid.UUID, code, msg string, retryable bool) error {
	status := "failed_terminal"
	if retryable {
		status = "failed_retryable"
	}
	_, err := r.DB.Pool.Exec(ctx,
		`UPDATE operations SET status = $1, error_code = $2, error_message = $3, finished_at = $4 WHERE id = $5`,
		status, code, msg, time.Now().UTC(), id)
	return err
}

// Outbox claims jobs using FOR UPDATE SKIP LOCKED for safe concurrent workers.
type Outbox struct{ DB *DB }

// Job is a single claim from the outbox.
type Job struct {
	ID            uuid.UUID
	OperationID   *uuid.UUID
	NodeID        uuid.UUID
	AggregateType string
	AggregateID   uuid.UUID
	Kind          string
	Payload       json.RawMessage
	Attempts      int
}

// Insert appends a job to the outbox.
func (r *Outbox) Insert(ctx context.Context, tx pgx.Tx, j Job) (uuid.UUID, error) {
	const q = `
INSERT INTO outbox(aggregate_type, aggregate_id, node_id, operation_id, kind, payload)
VALUES ($1,$2,$3,$4,$5,$6) RETURNING id`
	var id uuid.UUID
	row := tx.QueryRow(ctx, q, j.AggregateType, j.AggregateID, j.NodeID, j.OperationID, j.Kind, j.Payload)
	if err := row.Scan(&id); err != nil {
		return uuid.Nil, err
	}
	return id, nil
}

// ClaimNext atomically reserves the next pending or expired-lease job. Returns
// (nil, nil) if there is no work.
func (r *Outbox) ClaimNext(ctx context.Context, leaseFor time.Duration) (*Job, error) {
	const q = `
WITH job AS (
    SELECT id FROM outbox
    WHERE status IN ('pending','failed_retryable')
       OR (status = 'leased' AND leased_until < now())
    ORDER BY created_at
    FOR UPDATE SKIP LOCKED
    LIMIT 1
)
UPDATE outbox o
SET status='leased', leased_until = now() + $1::interval, attempts = attempts + 1, updated_at = now()
FROM job WHERE o.id = job.id
RETURNING o.id, o.operation_id, o.node_id, o.aggregate_type, o.aggregate_id, o.kind, o.payload, o.attempts`
	var j Job
	row := r.DB.Pool.QueryRow(ctx, q, leaseFor.String())
	if err := row.Scan(&j.ID, &j.OperationID, &j.NodeID, &j.AggregateType, &j.AggregateID, &j.Kind, &j.Payload, &j.Attempts); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, nil
		}
		return nil, err
	}
	return &j, nil
}

// Complete marks a job applied.
func (r *Outbox) Complete(ctx context.Context, id uuid.UUID) error {
	_, err := r.DB.Pool.Exec(ctx,
		`UPDATE outbox SET status='applied', updated_at = now(), last_error = NULL WHERE id = $1`, id)
	return err
}

// Fail marks a job failed; if retryable, the worker can pick it up again
// after the lease expires.
func (r *Outbox) Fail(ctx context.Context, id uuid.UUID, msg string, retryable bool) error {
	status := "failed_terminal"
	if retryable {
		status = "failed_retryable"
	}
	_, err := r.DB.Pool.Exec(ctx,
		`UPDATE outbox SET status=$2, last_error=$3, updated_at=now() WHERE id = $1`,
		id, status, msg)
	return err
}

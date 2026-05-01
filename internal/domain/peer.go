package domain

import (
	"net/netip"
	"time"

	"github.com/google/uuid"
)

// PeerState is the desired-state lifecycle of a peer.
type PeerState string

const (
	PeerStatePending  PeerState = "pending"
	PeerStateActive   PeerState = "active"
	PeerStateRevoked  PeerState = "revoked"
	PeerStateExpired  PeerState = "expired"
	PeerStateRotating PeerState = "rotating"
)

// Peer is the desired-state record for a single VPN client.
type Peer struct {
	ID         uuid.UUID  `json:"id"`
	TenantID   uuid.UUID  `json:"tenant_id"`
	NodeID     uuid.UUID  `json:"node_id"`
	ProfileID  uuid.UUID  `json:"profile_id"`
	ExternalID string     `json:"external_id"`
	DisplayName string    `json:"display_name"`

	PublicKey       string     `json:"public_key"`
	PresharedKeyRef *string    `json:"preshared_key_ref,omitempty"`

	AllowedIP netip.Prefix `json:"allowed_ip"` // /32 or /128

	State            PeerState  `json:"state"`
	DesiredRevision  int64      `json:"desired_revision"`
	AppliedRevision  int64      `json:"applied_revision"`

	CreatedAt time.Time  `json:"created_at"`
	UpdatedAt time.Time  `json:"updated_at"`
	ExpiresAt *time.Time `json:"expires_at,omitempty"`
	RevokedAt *time.Time `json:"revoked_at,omitempty"`
}

// PeerRuntime captures the latest runtime view from `awg show <iface> dump`.
type PeerRuntime struct {
	PeerID            uuid.UUID  `json:"peer_id"`
	LastHandshakeAt   *time.Time `json:"last_handshake_at,omitempty"`
	RxBytes           int64      `json:"rx_bytes"`
	TxBytes           int64      `json:"tx_bytes"`
	RuntimePresent    bool       `json:"runtime_present"`
	LastRuntimeSyncAt *time.Time `json:"last_runtime_sync_at,omitempty"`
	Endpoint          string     `json:"endpoint,omitempty"`
}

// Tenant boundary marker.
type Tenant struct {
	ID        uuid.UUID `json:"id"`
	Slug      string    `json:"slug"`
	Status    string    `json:"status"`
	CreatedAt time.Time `json:"created_at"`
}

// Node represents a VPN host that runs an AmneziaWG interface.
type Node struct {
	ID              uuid.UUID  `json:"id"`
	Region          string     `json:"region"`
	Hostname        string     `json:"hostname"`
	Status          string     `json:"status"`
	PublicEndpoint  string     `json:"public_endpoint"`
	BasePort        int        `json:"base_port"`
	AgentLastSeenAt *time.Time `json:"agent_last_seen_at,omitempty"`
	InterfaceName   string     `json:"interface_name"`
	ServerPublicKey string     `json:"server_public_key"`
	CreatedAt       time.Time  `json:"created_at"`
}

// AddressPool is per-node CIDR allocation source.
type AddressPool struct {
	ID       uuid.UUID    `json:"id"`
	TenantID uuid.UUID    `json:"tenant_id"`
	NodeID   uuid.UUID    `json:"node_id"`
	CIDR     netip.Prefix `json:"cidr"`
	Cursor   netip.Addr   `json:"cursor"`
	Policy   string       `json:"policy"` // "sequential" | "random"
}

// OperationKind is one of the durable async operations the API may schedule.
type OperationKind string

const (
	OpCreatePeer OperationKind = "peer.create"
	OpRevokePeer OperationKind = "peer.revoke"
	OpDeletePeer OperationKind = "peer.delete"
	OpRotatePeer OperationKind = "peer.rotate"
	OpReconcileNode OperationKind = "node.reconcile"
)

type OperationStatus string

const (
	OpStatusPending  OperationStatus = "pending"
	OpStatusRunning  OperationStatus = "running"
	OpStatusApplied  OperationStatus = "applied"
	OpStatusFailed   OperationStatus = "failed_terminal"
	OpStatusRetrying OperationStatus = "failed_retryable"
)

type Operation struct {
	ID         uuid.UUID       `json:"id"`
	TenantID   uuid.UUID       `json:"tenant_id"`
	PeerID     *uuid.UUID      `json:"peer_id,omitempty"`
	NodeID     uuid.UUID       `json:"node_id"`
	Kind       OperationKind   `json:"kind"`
	Status     OperationStatus `json:"status"`
	RequestHash string         `json:"-"`
	ErrorCode   string         `json:"error_code,omitempty"`
	ErrorMessage string        `json:"error_message,omitempty"`
	StartedAt   time.Time      `json:"started_at"`
	FinishedAt  *time.Time     `json:"finished_at,omitempty"`
}

// AuditEvent is an append-only record of a control-plane action.
type AuditEvent struct {
	ID            uuid.UUID  `json:"id"`
	TenantID      uuid.UUID  `json:"tenant_id"`
	SubjectID     *uuid.UUID `json:"subject_id,omitempty"`
	Action        string     `json:"action"`
	TargetType    string     `json:"target_type"`
	TargetID      string     `json:"target_id"`
	Before        []byte     `json:"before,omitempty"`
	After         []byte     `json:"after,omitempty"`
	RequestID     string     `json:"request_id,omitempty"`
	IdempotencyKey string    `json:"idempotency_key,omitempty"`
	CreatedAt     time.Time  `json:"created_at"`
}

package api

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/awg-rest/awg-rest/internal/auth"
	"github.com/awg-rest/awg-rest/internal/crypto"
	"github.com/awg-rest/awg-rest/internal/domain"
	"github.com/awg-rest/awg-rest/internal/render"
	"github.com/awg-rest/awg-rest/internal/repo"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// Service is the application service for HTTP handlers. It composes the
// repositories and enforces business rules (idempotency, audit, outbox).
type Service struct {
	DB             *repo.DB
	Tenants        *repo.Tenants
	Nodes          *repo.Nodes
	Profiles       *repo.Profiles
	Peers          *repo.Peers
	Operations     *repo.Operations
	Outbox         *repo.Outbox
	Idem           *repo.Idempotency
	Audit          *repo.Audit
	IdempotencyTTL time.Duration
	ClientDNS      []string

	// Now is used for tests.
	Now func() time.Time
}

func (s *Service) now() time.Time {
	if s.Now != nil {
		return s.Now()
	}
	return time.Now().UTC()
}

// CreatePeerRequest is the API DTO.
type CreatePeerRequest struct {
	ExternalID  string     `json:"external_id"`
	DisplayName string     `json:"display_name"`
	NodeID       *string    `json:"node_id,omitempty"`
	NodeHostname *string    `json:"node_hostname,omitempty"`
	ProfileID   *string    `json:"profile_id,omitempty"`
	ProfileName *string    `json:"profile_name,omitempty"`
	PublicKey   *string    `json:"public_key,omitempty"`
	ExpiresAt   *time.Time `json:"expires_at,omitempty"`
}

// CreatePeerResponse mirrors the report's API spec.
type CreatePeerResponse struct {
	OperationID  string `json:"operation_id"`
	PeerID       string `json:"peer_id"`
	Status       string `json:"status"`
	AllowedIP    string `json:"allowed_ip"`
	PublicKey    string `json:"public_key"`
	PrivateKey   string `json:"private_key,omitempty"`   // only when server-generated; one-time
	ClientConfig string `json:"client_config,omitempty"` // only when server-generated; one-time
	PresharedKey string `json:"preshared_key,omitempty"`
	NodeID       string `json:"node_id"`
	ProfileID    string `json:"profile_id"`
}

// CreatePeer is the durable, idempotent peer-creation flow.
func (s *Service) CreatePeer(ctx context.Context, tenantSlug, idemKey string, req CreatePeerRequest) (CreatePeerResponse, int, error) {
	if strings.TrimSpace(req.ExternalID) == "" {
		return CreatePeerResponse{}, 0, domain.ValidationErrors{{Field: "external_id", Code: "required", Message: "external_id is required"}}
	}
	if idemKey == "" {
		return CreatePeerResponse{}, 0, domain.ValidationErrors{{Field: "idempotency-key", Code: "required", Message: "Idempotency-Key header is required"}}
	}

	tenant, err := s.Tenants.GetBySlug(ctx, tenantSlug)
	if err != nil {
		return CreatePeerResponse{}, 0, err
	}
	if err := authorizeTenant(ctx, tenant.ID, auth.RolePlatformAdmin, auth.RoleTenantAdmin, auth.RoleAutomationClient); err != nil {
		return CreatePeerResponse{}, 0, err
	}

	// Node resolution comes first because the node/interface owns the protocol
	// profile. Per-peer profile selectors are accepted only as compatibility
	// assertions and must match the node-owned profile.
	var node *domain.Node
	if req.NodeID != nil && req.NodeHostname != nil {
		return CreatePeerResponse{}, 0, domain.ValidationErrors{{
			Field: "node_id", Code: "conflict",
			Message: "node_id and node_hostname are mutually exclusive",
		}}
	}
	switch {
	case req.NodeID != nil:
		id, err := uuid.Parse(strings.TrimSpace(*req.NodeID))
		if err != nil {
			return CreatePeerResponse{}, 0, domain.ValidationErrors{{Field: "node_id", Code: "invalid", Message: "must be a UUID"}}
		}
		node, err = s.Nodes.GetByID(ctx, id)
		if err != nil {
			return CreatePeerResponse{}, 0, err
		}
	case req.NodeHostname != nil:
		hostname := strings.TrimSpace(*req.NodeHostname)
		if hostname == "" {
			return CreatePeerResponse{}, 0, domain.ValidationErrors{{Field: "node_hostname", Code: "required", Message: "must not be empty"}}
		}
		node, err = s.Nodes.GetByHostname(ctx, hostname)
		if err != nil {
			return CreatePeerResponse{}, 0, err
		}
	default:
		node, err = s.Nodes.PickFirst(ctx)
		if err != nil {
			return CreatePeerResponse{}, 0, err
		}
	}
	if !node.AcceptNewPeers {
		return CreatePeerResponse{}, 0, domain.ValidationErrors{{
			Field: "node_id", Code: "provisioning_disabled",
			Message: "selected node is not accepting new peers",
		}}
	}
	if node.ProfileID == nil {
		return CreatePeerResponse{}, 0, domain.ValidationErrors{{
			Field: "node_id", Code: "profile_unassigned",
			Message: "node has no protocol profile assigned",
		}}
	}
	profile, err := s.Profiles.GetByID(ctx, *node.ProfileID)
	if err != nil {
		return CreatePeerResponse{}, 0, err
	}
	if err := validateRequestedProfile(*profile, req); err != nil {
		return CreatePeerResponse{}, 0, err
	}

	// Validate any client-supplied public key now; key *generation* must
	// happen only after the idempotency check, otherwise replays would each
	// produce a fresh key and the request hash would never match.
	if req.PublicKey != nil && *req.PublicKey != "" {
		if err := crypto.ValidateKey(*req.PublicKey); err != nil {
			return CreatePeerResponse{}, 0, domain.ErrInvalidKey
		}
	}

	// Hash the user-supplied request body — NOT any server-derived material.
	// This is what makes idempotency replays converge.
	hash := requestHash(struct {
		Req    CreatePeerRequest
		Tenant string
	}{Req: req, Tenant: tenantSlug})

	var pub, priv string
	if req.PublicKey != nil && *req.PublicKey != "" {
		pub = *req.PublicKey
	}

	var resp CreatePeerResponse
	var status int

	err = s.DB.InTx(ctx, func(tx pgx.Tx) error {
		// 1) Idempotency check.
		rec, err := s.Idem.Lookup(ctx, tx, tenant.ID, idemKey)
		if err == nil {
			if rec.RequestHash != hash {
				return domain.ErrIdempotencyConflict
			}
			status = rec.ResponseStatus
			if uerr := json.Unmarshal(rec.ResponseBody, &resp); uerr != nil {
				return uerr
			}
			return errAlreadyHandled
		}
		if !errors.Is(err, domain.ErrNotFound) {
			return err
		}

		// 1a) If the client did not supply a public key, generate one now.
		// Doing this AFTER the idempotency miss ensures replays do not leak
		// new key material on every retry.
		if pub == "" {
			kp, gerr := crypto.GenerateKeyPair()
			if gerr != nil {
				return gerr
			}
			pub, priv = kp.PublicKey, kp.PrivateKey
		}
		psk, gerr := crypto.GeneratePresharedKey()
		if gerr != nil {
			return gerr
		}

		// 2) Reserve IP + insert peer.
		peer, err := s.Peers.AllocateAndInsert(ctx, tx, repo.InsertParams{
			TenantID:        tenant.ID,
			NodeID:          node.ID,
			ProfileID:       profile.ID,
			ExternalID:      req.ExternalID,
			DisplayName:     req.DisplayName,
			PublicKey:       pub,
			PresharedKeyRef: &psk,
			ExpiresAt:       req.ExpiresAt,
		})
		if err != nil {
			return err
		}

		// 3) Operation + outbox.
		op, err := s.Operations.Insert(ctx, tx, tenant.ID, node.ID, &peer.ID, domain.OpCreatePeer, hash)
		if err != nil {
			return err
		}
		if _, err := s.Outbox.Insert(ctx, tx, repo.Job{
			AggregateType: "peer",
			AggregateID:   peer.ID,
			NodeID:        node.ID,
			OperationID:   &op.ID,
			Kind:          string(domain.OpCreatePeer),
			Payload:       json.RawMessage(`{}`),
		}); err != nil {
			return err
		}

		// 4) Audit.
		if err := s.Audit.Append(ctx, tx, repo.AppendParams{
			TenantID:       &tenant.ID,
			Action:         "peer.create",
			TargetType:     "peer",
			TargetID:       peer.ID.String(),
			After:          peer,
			IdempotencyKey: idemKey,
		}); err != nil {
			return err
		}

		// 5) Build response and persist idempotency record.
		resp = CreatePeerResponse{
			OperationID:  op.ID.String(),
			PeerID:       peer.ID.String(),
			Status:       string(op.Status),
			AllowedIP:    peer.AllowedIP.String(),
			PublicKey:    peer.PublicKey,
			PrivateKey:   priv,
			PresharedKey: psk,
			NodeID:       node.ID.String(),
			ProfileID:    profile.ID.String(),
		}
		if priv != "" {
			resp.ClientConfig = render.AmneziaClient(render.ClientArgs{
				ClientPrivateKey: priv,
				ClientAddress:    []string{peer.AllowedIP.String()},
				DNS:              s.ClientDNS,
				ServerPublicKey:  node.ServerPublicKey,
				ServerEndpoint:   node.PublicEndpoint,
				PresharedKey:     psk,
				Keepalive:        25,
			}, *profile)
		}
		status = http.StatusAccepted
		// Persist a sanitized response that does NOT include one-time secret
		// material, so a replay never re-issues client keys.
		safe := resp
		safe.PrivateKey = ""
		safe.ClientConfig = ""
		safe.PresharedKey = ""
		if err := s.Idem.Store(ctx, tx, tenant.ID, idemKey, hash, &op.ID, status, safe, s.idempotencyTTL()); err != nil {
			return err
		}
		return nil
	})

	if errors.Is(err, errAlreadyHandled) {
		return resp, status, nil
	}
	if err != nil {
		return CreatePeerResponse{}, 0, err
	}
	return resp, status, nil
}

func (s *Service) idempotencyTTL() time.Duration {
	if s.IdempotencyTTL > 0 {
		return s.IdempotencyTTL
	}
	return 24 * time.Hour
}

// errAlreadyHandled is an internal sentinel meaning "idempotency replay; just
// return what we already have".
var errAlreadyHandled = errors.New("already_handled")

// RevokePeer marks a peer revoked and enqueues a remove-on-runtime job.
func (s *Service) RevokePeer(ctx context.Context, tenantSlug, peerID, idemKey, reason string) (uuid.UUID, error) {
	if idemKey == "" {
		return uuid.Nil, domain.ValidationErrors{{Field: "idempotency-key", Code: "required", Message: "Idempotency-Key header is required"}}
	}
	tenant, err := s.Tenants.GetBySlug(ctx, tenantSlug)
	if err != nil {
		return uuid.Nil, err
	}
	if err := authorizeTenant(ctx, tenant.ID, auth.RolePlatformAdmin, auth.RoleTenantAdmin, auth.RoleAutomationClient); err != nil {
		return uuid.Nil, err
	}
	pid, err := uuid.Parse(peerID)
	if err != nil {
		return uuid.Nil, domain.ValidationErrors{{Field: "peer_id", Code: "invalid", Message: "must be a UUID"}}
	}
	var opID uuid.UUID
	err = s.DB.InTx(ctx, func(tx pgx.Tx) error {
		hash := requestHash(struct {
			TID, PID, Reason string
		}{tenant.ID.String(), pid.String(), reason})

		if idemKey != "" {
			rec, err := s.Idem.Lookup(ctx, tx, tenant.ID, idemKey)
			if err == nil {
				if rec.RequestHash != hash {
					return domain.ErrIdempotencyConflict
				}
				if rec.OperationID != nil {
					opID = *rec.OperationID
				}
				return errAlreadyHandled
			} else if !errors.Is(err, domain.ErrNotFound) {
				return err
			}
		}

		peer, err := s.Peers.MarkRevoked(ctx, tx, pid, s.now())
		if err != nil {
			return err
		}
		if peer.TenantID != tenant.ID {
			return domain.ErrNotFound
		}
		op, err := s.Operations.Insert(ctx, tx, tenant.ID, peer.NodeID, &peer.ID, domain.OpRevokePeer, hash)
		if err != nil {
			return err
		}
		opID = op.ID
		if _, err := s.Outbox.Insert(ctx, tx, repo.Job{
			AggregateType: "peer", AggregateID: peer.ID, NodeID: peer.NodeID,
			OperationID: &op.ID, Kind: string(domain.OpRevokePeer),
			Payload: mustJSON(map[string]string{"reason": reason}),
		}); err != nil {
			return err
		}
		if err := s.Audit.Append(ctx, tx, repo.AppendParams{
			TenantID: &tenant.ID, Action: "peer.revoke",
			TargetType: "peer", TargetID: peer.ID.String(), After: peer,
			IdempotencyKey: idemKey,
		}); err != nil {
			return err
		}
		if idemKey != "" {
			if err := s.Idem.Store(ctx, tx, tenant.ID, idemKey, hash, &op.ID, http.StatusAccepted,
				map[string]string{"operation_id": op.ID.String()}, s.idempotencyTTL()); err != nil {
				return err
			}
		}
		return nil
	})
	if errors.Is(err, errAlreadyHandled) {
		return opID, nil
	}
	return opID, err
}

// GetOperation reads an operation by id.
func (s *Service) GetOperation(ctx context.Context, id string) (*domain.Operation, error) {
	uid, err := uuid.Parse(id)
	if err != nil {
		return nil, domain.ValidationErrors{{Field: "id", Code: "invalid", Message: "must be a UUID"}}
	}
	op, err := s.Operations.GetByID(ctx, uid)
	if err != nil {
		return nil, err
	}
	if err := authorizeTenant(ctx, op.TenantID, auth.RolePlatformAdmin, auth.RoleTenantAdmin, auth.RoleSupportReadOnly, auth.RoleAutomationClient); err != nil {
		return nil, err
	}
	return op, nil
}

// GetPeer fetches a peer scoped to a tenant.
func (s *Service) GetPeer(ctx context.Context, tenantSlug, id string) (*domain.Peer, error) {
	tenant, err := s.Tenants.GetBySlug(ctx, tenantSlug)
	if err != nil {
		return nil, err
	}
	if err := authorizeTenant(ctx, tenant.ID, auth.RolePlatformAdmin, auth.RoleTenantAdmin, auth.RoleSupportReadOnly, auth.RoleAutomationClient); err != nil {
		return nil, err
	}
	uid, err := uuid.Parse(id)
	if err != nil {
		return nil, domain.ValidationErrors{{Field: "id", Code: "invalid", Message: "must be a UUID"}}
	}
	p, err := s.Peers.GetByID(ctx, uid)
	if err != nil {
		return nil, err
	}
	if p.TenantID != tenant.ID {
		return nil, domain.ErrNotFound
	}
	return p, nil
}

// PeerConfiguration returns a rendered non-secret AmneziaWG client config skeleton.
func (s *Service) PeerConfiguration(ctx context.Context, tenantSlug, peerID string) (string, error) {
	peer, err := s.GetPeer(ctx, tenantSlug, peerID)
	if err != nil {
		return "", err
	}
	node, err := s.Nodes.GetByID(ctx, peer.NodeID)
	if err != nil {
		return "", err
	}
	if node.ProfileID == nil {
		return "", domain.ValidationErrors{{
			Field: "node_id", Code: "profile_unassigned",
			Message: "node has no protocol profile assigned",
		}}
	}
	profile, err := s.Profiles.GetByID(ctx, *node.ProfileID)
	if err != nil {
		return "", err
	}
	publicProfile := *profile
	// This endpoint is intentionally a non-secret skeleton. HeaderProtectionKey
	// is key material just like client private/PSK values; the complete V3.1
	// config is issued only in the one-time create response.
	publicProfile.HeaderProtectionKey = ""
	out := render.AmneziaClient(render.ClientArgs{
		ClientAddress:   []string{peer.AllowedIP.String()},
		DNS:             s.ClientDNS,
		ServerPublicKey: node.ServerPublicKey,
		ServerEndpoint:  node.PublicEndpoint,
		Keepalive:       25,
	}, publicProfile)
	return out, nil
}

func validateRequestedProfile(profile domain.ProtocolProfile, req CreatePeerRequest) error {
	if req.ProfileID != nil {
		id, err := uuid.Parse(strings.TrimSpace(*req.ProfileID))
		if err != nil {
			return domain.ValidationErrors{{Field: "profile_id", Code: "invalid", Message: "must be a UUID"}}
		}
		if id != profile.ID {
			return domain.ValidationErrors{{
				Field: "profile_id", Code: "node_profile_mismatch",
				Message: "profile_id must match the selected node profile",
			}}
		}
	}
	if req.ProfileName != nil && strings.TrimSpace(*req.ProfileName) != profile.Name {
		return domain.ValidationErrors{{
			Field: "profile_name", Code: "node_profile_mismatch",
			Message: "profile_name must match the selected node profile",
		}}
	}
	return nil
}

// EnsureTenant is a convenience for bootstrapping or tests.
func (s *Service) EnsureTenant(ctx context.Context, slug string) (*domain.Tenant, error) {
	return s.Tenants.Upsert(ctx, slug)
}

// requestHash returns the hex SHA-256 of the canonical JSON of v. Used both
// for idempotency-key conflict detection and as a request fingerprint in audit.
func requestHash(v any) string {
	b, _ := json.Marshal(v)
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

func mustJSON(v any) []byte {
	b, _ := json.Marshal(v)
	return b
}

func authorizeTenant(ctx context.Context, tenantID uuid.UUID, roles ...auth.Role) error {
	p, err := auth.FromContext(ctx)
	if err != nil {
		return err
	}
	if !p.HasAnyRole(roles...) {
		return domain.ErrForbidden
	}
	if p.HasRole(auth.RolePlatformAdmin) {
		return nil
	}
	if p.TenantID == uuid.Nil || p.TenantID != tenantID {
		return domain.ErrForbidden
	}
	return nil
}

// AuthSubjectFromCtx returns the authenticated subject's UUID if present.
func AuthSubjectFromCtx(ctx context.Context) *uuid.UUID {
	p, err := auth.FromContext(ctx)
	if err != nil {
		return nil
	}
	if p.SubjectID == uuid.Nil {
		return nil
	}
	return &p.SubjectID
}

package api

import (
	"encoding/json"
	"net/http"

	"github.com/awg-rest/awg-rest/internal/domain"
	"github.com/go-chi/chi/v5"
)

// Handlers implements the REST endpoints. It composes the application Service.
type Handlers struct {
	Service *Service
}

// CreatePeer POST /v1/tenants/{tenant}/peers
func (h *Handlers) CreatePeer(w http.ResponseWriter, r *http.Request) {
	tenant := chi.URLParam(r, "tenant")
	idemKey := r.Header.Get("Idempotency-Key")
	var req CreatePeerRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		WriteError(w, domain.ValidationErrors{{Field: "body", Code: "invalid_json", Message: err.Error()}})
		return
	}
	resp, status, err := h.Service.CreatePeer(r.Context(), tenant, idemKey, req)
	if err != nil {
		WriteError(w, err)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(resp)
}

// GetPeer GET /v1/tenants/{tenant}/peers/{peerID}
func (h *Handlers) GetPeer(w http.ResponseWriter, r *http.Request) {
	tenant := chi.URLParam(r, "tenant")
	id := chi.URLParam(r, "peerID")
	p, err := h.Service.GetPeer(r.Context(), tenant, id)
	if err != nil {
		WriteError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, p)
}

// RevokePeer POST /v1/tenants/{tenant}/peers/{peerID}:revoke
func (h *Handlers) RevokePeer(w http.ResponseWriter, r *http.Request) {
	tenant := chi.URLParam(r, "tenant")
	id := chi.URLParam(r, "peerID")
	idemKey := r.Header.Get("Idempotency-Key")
	var body struct {
		Reason string `json:"reason"`
	}
	_ = json.NewDecoder(r.Body).Decode(&body) // body is optional
	opID, err := h.Service.RevokePeer(r.Context(), tenant, id, idemKey, body.Reason)
	if err != nil {
		WriteError(w, err)
		return
	}
	writeJSON(w, http.StatusAccepted, map[string]string{
		"operation_id": opID.String(),
		"status":       "pending",
	})
}

// GetPeerConfiguration GET /v1/tenants/{tenant}/peers/{peerID}/configuration.
// Client private keys are intentionally not accepted via query string.
func (h *Handlers) GetPeerConfiguration(w http.ResponseWriter, r *http.Request) {
	tenant := chi.URLParam(r, "tenant")
	id := chi.URLParam(r, "peerID")
	if r.URL.Query().Get("client_private_key") != "" {
		WriteError(w, domain.ValidationErrors{{
			Field:   "client_private_key",
			Code:    "unsupported",
			Message: "client private keys must not be sent in query parameters",
		}})
		return
	}
	cfg, err := h.Service.PeerConfiguration(r.Context(), tenant, id)
	if err != nil {
		WriteError(w, err)
		return
	}
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	_, _ = w.Write([]byte(cfg))
}

// GetOperation GET /v1/operations/{id}
func (h *Handlers) GetOperation(w http.ResponseWriter, r *http.Request) {
	op, err := h.Service.GetOperation(r.Context(), chi.URLParam(r, "id"))
	if err != nil {
		WriteError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, op)
}

// HealthLive GET /health/live
func (h *Handlers) HealthLive(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

// HealthReady GET /health/ready
func (h *Handlers) HealthReady(w http.ResponseWriter, r *http.Request) {
	if err := h.Service.DB.Pool.Ping(r.Context()); err != nil {
		WriteError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "ready"})
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

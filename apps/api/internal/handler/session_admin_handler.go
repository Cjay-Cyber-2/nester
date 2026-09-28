package handler

import (
	"context"
	"errors"
	"net/http"

	"github.com/google/uuid"

	"github.com/suncrestlabs/nester/apps/api/internal/domain/session"
	"github.com/suncrestlabs/nester/apps/api/pkg/response"
)

// sessionAdminRepo is the narrow persistence port SessionAdminHandler needs.
type sessionAdminRepo interface {
	Revoke(ctx context.Context, id uuid.UUID) error
}

// SessionAdminHandler lets an admin immediately revoke a session (#1327),
// e.g. after a compromised-wallet report, without waiting for token expiry.
type SessionAdminHandler struct {
	repo sessionAdminRepo
}

// NewSessionAdminHandler constructs a SessionAdminHandler.
func NewSessionAdminHandler(repo sessionAdminRepo) *SessionAdminHandler {
	return &SessionAdminHandler{repo: repo}
}

// Register registers the routes on the given ServeMux.
func (h *SessionAdminHandler) Register(mux *http.ServeMux) {
	mux.HandleFunc("POST /api/v1/admin/sessions/{id}/revoke", h.revoke)
}

func (h *SessionAdminHandler) revoke(w http.ResponseWriter, r *http.Request) {
	id, err := uuid.Parse(r.PathValue("id"))
	if err != nil {
		response.WriteJSON(w, http.StatusBadRequest, response.ValidationErr("invalid session ID"))
		return
	}

	if err := h.repo.Revoke(r.Context(), id); err != nil {
		if errors.Is(err, session.ErrNotFound) {
			response.WriteJSON(w, http.StatusNotFound, response.NotFound("session"))
			return
		}
		response.WriteJSON(w, http.StatusInternalServerError, response.Err(http.StatusInternalServerError, "INTERNAL_ERROR", "failed to revoke session"))
		return
	}

	response.WriteJSON(w, http.StatusOK, response.OK(map[string]string{"status": "revoked"}))
}

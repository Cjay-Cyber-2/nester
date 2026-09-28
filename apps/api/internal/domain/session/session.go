// Package session defines the domain type and repository interface for the
// sessions table, which backs JWT revocation (#1327): each issued token
// carries a session_id (sid) claim, and every authenticated request checks
// that the corresponding session hasn't been revoked, rather than relying on
// token expiry alone.
package session

import (
	"context"
	"errors"
	"time"

	"github.com/google/uuid"
)

// ErrNotFound is returned when a session row does not exist.
var ErrNotFound = errors.New("session: not found")

// Session is a row in the sessions table.
type Session struct {
	ID               uuid.UUID
	UserID           uuid.UUID
	WalletAddress    string
	TokenHash        string
	RefreshTokenHash string
	ExpiresAt        time.Time
	CreatedAt        time.Time
	RevokedAt        *time.Time
}

// Repository is the persistence port for sessions.
type Repository interface {
	// Create inserts a new session row and returns its generated ID.
	Create(ctx context.Context, s Session) (uuid.UUID, error)

	// IsRevoked reports whether the session with the given id has been
	// revoked, or no longer exists (treated as revoked — a caller can't
	// distinguish "never existed" from "cleaned up after revocation").
	IsRevoked(ctx context.Context, id uuid.UUID) (bool, error)

	// Revoke marks a session revoked. Idempotent.
	Revoke(ctx context.Context, id uuid.UUID) error
}

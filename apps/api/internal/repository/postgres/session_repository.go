package postgres

import (
	"context"
	"database/sql"
	"errors"

	"github.com/google/uuid"

	"github.com/suncrestlabs/nester/apps/api/internal/domain/session"
)

// SessionRepository persists sessions (#1327) in Postgres.
type SessionRepository struct {
	db *sql.DB
}

// NewSessionRepository constructs a SessionRepository.
func NewSessionRepository(db *sql.DB) *SessionRepository {
	return &SessionRepository{db: db}
}

func (r *SessionRepository) Create(ctx context.Context, s session.Session) (uuid.UUID, error) {
	id := s.ID
	if id == uuid.Nil {
		id = uuid.New()
	}
	_, err := r.db.ExecContext(ctx, `
		INSERT INTO sessions (id, user_id, wallet_address, token_hash, refresh_token_hash, expires_at)
		VALUES ($1, $2, $3, $4, $5, $6)`,
		id, s.UserID, s.WalletAddress, s.TokenHash, s.RefreshTokenHash, s.ExpiresAt,
	)
	if err != nil {
		return uuid.Nil, err
	}
	return id, nil
}

// IsRevoked reports whether id has been revoked or no longer exists.
func (r *SessionRepository) IsRevoked(ctx context.Context, id uuid.UUID) (bool, error) {
	var revokedAt sql.NullTime
	err := r.db.QueryRowContext(ctx, `SELECT revoked_at FROM sessions WHERE id = $1`, id).Scan(&revokedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return true, nil
	}
	if err != nil {
		return false, err
	}
	return revokedAt.Valid, nil
}

func (r *SessionRepository) Revoke(ctx context.Context, id uuid.UUID) error {
	_, err := r.db.ExecContext(ctx, `
		UPDATE sessions SET revoked_at = NOW() WHERE id = $1 AND revoked_at IS NULL`,
		id,
	)
	return err
}

// IsRevokedString adapts IsRevoked to a string session ID, satisfying
// middleware.SessionRevocationChecker without that package importing uuid or
// the session domain package. An unparsable ID is treated as revoked.
func (r *SessionRepository) IsRevokedString(ctx context.Context, sessionID string) (bool, error) {
	id, err := uuid.Parse(sessionID)
	if err != nil {
		return true, nil
	}
	return r.IsRevoked(ctx, id)
}

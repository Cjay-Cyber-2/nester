package postgres

import (
	"context"
	"database/sql"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/suncrestlabs/nester/apps/api/internal/domain/session"
)

func resetSessionIntegrationTables(t *testing.T, db *sql.DB) {
	t.Helper()
	if _, err := db.Exec(`TRUNCATE TABLE sessions, allocations, vaults, users RESTART IDENTITY CASCADE`); err != nil {
		t.Fatalf("TRUNCATE failed: %v", err)
	}
}

func TestSessionRepository_CreateAndRevoke(t *testing.T) {
	db := openIntegrationDB(t)
	applyIntegrationMigrations(t, db)
	resetSessionIntegrationTables(t, db)

	repo := NewSessionRepository(db)
	ctx := context.Background()
	userID := seedIntegrationUser(t, db)

	id, err := repo.Create(ctx, session.Session{
		UserID:           userID,
		WalletAddress:    "GTESTWALLET",
		TokenHash:        "hash-1",
		RefreshTokenHash: "refresh-1",
		ExpiresAt:        time.Now().Add(time.Hour),
	})
	if err != nil {
		t.Fatalf("create: %v", err)
	}

	revoked, err := repo.IsRevoked(ctx, id)
	if err != nil {
		t.Fatalf("is revoked: %v", err)
	}
	if revoked {
		t.Fatal("freshly created session must not be revoked")
	}

	if err := repo.Revoke(ctx, id); err != nil {
		t.Fatalf("revoke: %v", err)
	}

	revoked, err = repo.IsRevoked(ctx, id)
	if err != nil {
		t.Fatalf("is revoked after revoke: %v", err)
	}
	if !revoked {
		t.Fatal("session must be revoked after Revoke")
	}

	// Re-revoking is idempotent, not an error.
	if err := repo.Revoke(ctx, id); err != nil {
		t.Fatalf("re-revoke: %v", err)
	}
}

func TestSessionRepository_IsRevokedUnknownID(t *testing.T) {
	db := openIntegrationDB(t)
	applyIntegrationMigrations(t, db)
	resetSessionIntegrationTables(t, db)

	repo := NewSessionRepository(db)

	revoked, err := repo.IsRevoked(context.Background(), uuid.New())
	if err != nil {
		t.Fatalf("is revoked: %v", err)
	}
	if !revoked {
		t.Fatal("a session that doesn't exist must be treated as revoked")
	}
}

func TestSessionRepository_IsRevokedStringAdapter(t *testing.T) {
	db := openIntegrationDB(t)
	applyIntegrationMigrations(t, db)
	resetSessionIntegrationTables(t, db)

	repo := NewSessionRepository(db)
	ctx := context.Background()
	userID := seedIntegrationUser(t, db)

	id, err := repo.Create(ctx, session.Session{
		UserID:           userID,
		WalletAddress:    "GTESTWALLET",
		TokenHash:        "hash-1",
		RefreshTokenHash: "refresh-1",
		ExpiresAt:        time.Now().Add(time.Hour),
	})
	if err != nil {
		t.Fatalf("create: %v", err)
	}

	revoked, err := repo.IsRevokedString(ctx, id.String())
	if err != nil || revoked {
		t.Fatalf("IsRevokedString(live) = %v, %v", revoked, err)
	}

	revoked, err = repo.IsRevokedString(ctx, "not-a-uuid")
	if err != nil || !revoked {
		t.Fatalf("IsRevokedString(garbage) = %v, %v, want (true, nil)", revoked, err)
	}
}

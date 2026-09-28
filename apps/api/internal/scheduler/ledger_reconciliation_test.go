package scheduler

import (
	"context"
	"log/slog"
	"testing"

	"github.com/google/uuid"
	"github.com/suncrestlabs/nester/apps/api/internal/domain/ledger"
)

// Mock types for testing reconciliation job escalation
type mockLedgerRepo struct {
	ledger.Repository
	poolBal int64
	sumUser int64
}

func (m *mockLedgerRepo) GetVaultPoolBalance(ctx context.Context, vaultID uuid.UUID) (int64, error) {
	return m.poolBal, nil
}

func (m *mockLedgerRepo) SumUserPositionBalances(ctx context.Context, vaultID uuid.UUID) (int64, error) {
	return m.sumUser, nil
}

func (m *mockLedgerRepo) CreateReconciliationRecord(ctx context.Context, rec ledger.ReconciliationRecord) error {
	return nil
}

type mockChainReader struct {
	ledger.ChainReader
	onChainBal        int64
	totalSharesPrice int64
}

func (m *mockChainReader) ReadVaultBalance(ctx context.Context, contractAddress string) (int64, error) {
	return m.onChainBal, nil
}

func (m *mockChainReader) ReadTotalSharesTimesPrice(ctx context.Context, contractAddress string) (int64, error) {
	return m.totalSharesPrice, nil
}

type mockVaultLister struct {
	vaults []ReconcileVaultInfo
}

func (m *mockVaultLister) ListActiveForReconciliation(ctx context.Context) ([]ReconcileVaultInfo, error) {
	return m.vaults, nil
}

func TestLedgerReconciliationEscalationMainnet(t *testing.T) {
	vaultID := uuid.New()
	vaults := []ReconcileVaultInfo{
		{ID: vaultID, ContractAddress: "CVAULT1", Currency: "USDC"},
	}

	// Ledger pool balance 1000 USDC (10,000,000,000 stroops), on chain 800 USDC (8,000,000,000 stroops)
	// Difference = 200 USDC (2,000,000,000 stroops), which is > $100 escalation threshold on mainnet
	repo := &mockLedgerRepo{poolBal: 10_000_000_000, sumUser: 10_000_000_000}
	reader := &mockChainReader{onChainBal: 8_000_000_000, totalSharesPrice: 10_000_000_000}
	lister := &mockVaultLister{vaults: vaults}

	cfg := ledger.ReconciliationConfig{
		Enabled:                true,
		ToleranceStroops:       1_000_000, // 0.1 USDC
		EscalationThresholdUSD: 100.0,     // $100 threshold
		IsMainnet:              true,
	}

	job := NewLedgerReconciliationJob(LedgerReconciliationDeps{
		LedgerRepo:  repo,
		VaultLister: lister,
		ChainReader: reader,
		Logger:      slog.Default(),
		Config:      cfg,
	})

	// Run tick without panic
	job.Tick(context.Background())
}

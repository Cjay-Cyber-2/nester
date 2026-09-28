package scheduler

import (
	"context"
	"log/slog"
	"os"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/suncrestlabs/nester/apps/api/internal/domain/ledger"
)

type mockReconciliationVaultLister struct {
	vaults []ReconcileVaultInfo
}

func (m *mockReconciliationVaultLister) ListActiveForReconciliation(ctx context.Context) ([]ReconcileVaultInfo, error) {
	return m.vaults, nil
}

type mockLedgerRepo struct {
	ledger.Repository
	poolBalance int64
	records     []ledger.ReconciliationRecord
}

func (m *mockLedgerRepo) GetVaultPoolBalance(ctx context.Context, vaultID uuid.UUID) (int64, error) {
	return m.poolBalance, nil
}

func (m *mockLedgerRepo) SumUserPositionBalances(ctx context.Context, vaultID uuid.UUID) (int64, error) {
	return 0, nil
}

func (m *mockLedgerRepo) CreateReconciliationRecord(ctx context.Context, rec ledger.ReconciliationRecord) error {
	m.records = append(m.records, rec)
	return nil
}

type mockChainReader struct {
	ledger.ChainReader
	onChainBalance     int64
	totalSharesPrice int64
}

func (m *mockChainReader) ReadVaultBalance(ctx context.Context, contractAddress string) (int64, error) {
	return m.onChainBalance, nil
}

func (m *mockChainReader) ReadTotalSharesTimesPrice(ctx context.Context, contractAddress string) (int64, error) {
	return m.totalSharesPrice, nil
}

func TestLedgerReconciliationJobMainnetDollarThresholdPaging(t *testing.T) {
	_ = os.Setenv("STELLAR_NETWORK", "mainnet")
	defer os.Unsetenv("STELLAR_NETWORK")

	vaultID := uuid.New()
	vaultsLister := &mockReconciliationVaultLister{
		vaults: []ReconcileVaultInfo{
			{ID: vaultID, ContractAddress: "CVAULTMAINNET", Currency: "USDC"},
		},
	}

	ledgerRepo := &mockLedgerRepo{
		poolBalance: 20_000_000, // 2 USDC
	}

	chainReader := &mockChainReader{
		onChainBalance: 10_000_000, // 1 USDC (diff is 1 USDC = 10,000,000 stroops)
	}

	cfg := ledger.ReconciliationConfig{
		Enabled:                true,
		Interval:               time.Minute,
		ToleranceStroops:       1_000, // small tolerance
		MainnetDollarThreshold: 0.5,   // threshold 0.5 USDC = 5,000,000 stroops
	}

	job := NewLedgerReconciliationJob(LedgerReconciliationDeps{
		LedgerRepo:  ledgerRepo,
		VaultLister:  vaultsLister,
		ChainReader:  chainReader,
		Logger:       slog.Default(),
		Config:       cfg,
	})

	// Run single tick
	job.Tick(context.Background())

	if len(ledgerRepo.records) != 1 {
		t.Fatalf("expected 1 reconciliation record, got %d", len(ledgerRepo.records))
	}
	
	rec := ledgerRepo.records[0]
	if rec.Status != "drift" {
		t.Fatalf("expected status drift, got %s", rec.Status)
	}
}
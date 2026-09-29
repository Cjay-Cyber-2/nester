package service

import (
	"context"
	"fmt"
	"sort"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/shopspring/decimal"

	"github.com/suncrestlabs/nester/apps/api/internal/domain/jobqueue"
	"github.com/suncrestlabs/nester/apps/api/internal/domain/vault"
)

const (
	StatusPending   = "pending"
	StatusRunning   = "running"
	StatusCompleted = "completed"
	StatusFailed    = "failed"
)

type QueueStats struct {
	Pending   int
	Running   int
	Completed int
	Failed    int
}

// Launch-day load test for the API money path and job queue (issue #1137).
//
// This test simulates projected mainnet launch-day traffic levels: concurrent active
// savers executing deposit/withdrawal transactions against vaults concurrently while
// an asynchronous job queue processes scheduled recurring deposits and background maintenance
// tasks under high concurrency.
//
// Configurable environment variables:
//   LAUNCH_LOAD_CONCURRENCY   - concurrent users (default 100)
//   LAUNCH_LOAD_OPS           - operations per user (default 25)
//   LAUNCH_LOAD_QUEUE_JOBS    - number of background job queue items to process (default 200)
//   LAUNCH_LOAD_P95_MS        - p95 latency budget in milliseconds (default 75)
//   LAUNCH_LOAD_ERROR_RATE    - maximum tolerated error rate percentage (default 0.005)
//
// Skipped under -short; designed for the integration and staging validation pipeline.
const (
	defaultLaunchConcurrency = 100
	defaultLaunchOpsPerUser  = 25
	defaultLaunchQueueJobs   = 200
	defaultLaunchP95Millis   = 75
	defaultLaunchErrorRate   = 0.005
)

type memJobRepo struct {
	mu    sync.Mutex
	jobs  map[uuid.UUID]jobqueue.Job
	order []uuid.UUID
}

func newMemRepo() *memJobRepo {
	return &memJobRepo{
		jobs: make(map[uuid.UUID]jobqueue.Job),
	}
}

func (m *memJobRepo) Enqueue(ctx context.Context, input jobqueue.EnqueueInput) (uuid.UUID, bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	id := uuid.New()
	j := jobqueue.Job{
		ID:             id,
		Type:           input.Type,
		Payload:        input.Payload,
		Status:         jobqueue.StatusPending,
		Priority:       input.Priority,
		IdempotencyKey: input.IdempotencyKey,
		CreatedAt:      time.Now(),
		MaxAttempts:    3,
	}
	m.jobs[id] = j
	m.order = append(m.order, id)
	return id, true, nil
}

func (m *memJobRepo) AcquireNext(ctx context.Context, workerID string, leaseDuration time.Duration, now time.Now) (*jobqueue.Job, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, id := range m.order {
		j := m.jobs[id]
		if j.Status == jobqueue.StatusPending {
			j.Status = jobqueue.StatusRunning
			j.LockedBy = &workerID
			exp := now.Add(leaseDuration)
			j.LockedUntil = &exp
			j.Attempts++
			m.jobs[id] = j
			return &j, nil
		}
	}
	return nil, nil
}

func (m *memJobRepo) Heartbeat(ctx context.Context, jobID uuid.UUID, workerID string, leaseDuration time.Duration, now time.Now) (bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	j, ok := m.jobs[jobID]
	if !ok || j.Status != jobqueue.StatusRunning {
		return false, nil
	}
	exp := now.Add(leaseDuration)
	j.LockedUntil = &exp
	m.jobs[jobID] = j
	return true, nil
}

func (m *memJobRepo) Complete(ctx context.Context, jobID uuid.UUID, workerID string, now time.Now) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	j, ok := m.jobs[jobID]
	if ok {
		j.Status = jobqueue.StatusCompleted
		j.CompletedAt = &now
		m.jobs[jobID] = j
	}
	return nil
}

func (m *memJobRepo) Fail(ctx context.Context, jobID uuid.UUID, workerID string, errStr string, backoff time.Duration, now time.Now) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	j, ok := m.jobs[jobID]
	if ok {
		if j.Attempts >= j.MaxAttempts {
			j.Status = jobqueue.StatusFailed
		} else {
			j.Status = jobqueue.StatusPending
		}
		j.LastError = &errStr
		m.jobs[jobID] = j
	}
	return nil
}

func (m *memJobRepo) Stats(ctx context.Context, now time.Now) (QueueStats, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var stats QueueStats
	for _, j := range m.jobs {
		switch j.Status {
		case jobqueue.StatusPending:
			stats.Pending++
		case jobqueue.StatusRunning:
			stats.Running++
		case jobqueue.StatusCompleted:
			stats.Completed++
		case jobqueue.StatusFailed:
			stats.Failed++
		}
	}
	return stats, nil
}

func TestLaunchDayAPIAndJobQueueLoad(t *testing.T) {
	if testing.Short() {
		t.Skip("launch-day load test skipped in -short mode; run without -short for staging load validation")
	}

	concurrency := envInt(t, "LAUNCH_LOAD_CONCURRENCY", defaultLaunchConcurrency)
	opsPerUser := envInt(t, "LAUNCH_LOAD_OPS", defaultLaunchOpsPerUser)
	queueJobsCount := envInt(t, "LAUNCH_LOAD_QUEUE_JOBS", defaultLaunchQueueJobs)
	p95Budget := time.Duration(envInt(t, "LAUNCH_LOAD_P95_MS", defaultLaunchP95Millis)) * time.Millisecond
	errorBudget := envFloat(t, "LAUNCH_LOAD_ERROR_RATE", defaultLaunchErrorRate)

	totalOps := concurrency * opsPerUser
	if totalOps == 0 {
		t.Fatal("launch load concurrency and ops resolved to zero")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	userID := uuid.New()
	repo := newConcurrentVaultRepository(userID)
	svc := NewVaultService(repo)

	verifier := newLoadTestChainVerifier()
	svc.SetChainEventVerifier(verifier)

	// Initialize vault accounts for concurrent savers
	vaultIDs := make([]uuid.UUID, concurrency)
	seedBalance := decimal.NewFromInt(2_500_000)
	for i := range vaultIDs {
		created, err := svc.CreateVault(ctx, CreateVaultInput{
			UserID:          userID,
			ContractAddress: fmt.Sprintf("CLAUNCHTEST%044d", i),
			Currency:        "USDC",
		})
		if err != nil {
			t.Fatalf("CreateVault %d failed: %v", i, err)
		}
		seedHash := fmt.Sprintf("launch-seed-%d", i)
		verifier.expect(seedHash, seedBalance)
		_, err = svc.RecordDeposit(ctx, RecordDepositInput{
			VaultID: created.ID,
			UserID:  userID,
			Amount:  seedBalance,
			TxHash:  seedHash,
		})
		if err != nil {
			t.Fatalf("seed vault %d failed: %v", i, err)
		}
		vaultIDs[i] = created.ID
	}

	// Initialize in-memory job queue repo and worker to simulate background queue throughput
	jobRepo := newMemRepo()
	var queueSuccess atomic.Int64
	workerCfg := jobqueue.Config{
		Enabled:            true,
		PollInterval:       2 * time.Millisecond,
		Lease:              500 * time.Millisecond,
		HeartbeatInterval:  100 * time.Millisecond,
		DefaultConcurrency: 8,
		Backoff:            jobqueue.BackoffConfig{Base: time.Millisecond, Max: 10 * time.Millisecond},
		DrainTimeout:       5 * time.Second,
	}

	worker := jobqueue.NewWorker(jobRepo, workerCfg, nil, nil)
	worker.Register("launch-recurring-deposit", jobqueue.HandlerFunc(func(_ context.Context, j jobqueue.Job) error {
		queueSuccess.Add(1)
		return nil
	}), 0)

	workerCtx, workerCancel := context.WithCancel(context.Background())
	defer workerCancel()
	go func() { _ = worker.Run(workerCtx) }()

	// Enqueue projected launch background jobs
	for j := 0; j < queueJobsCount; j++ {
		_, _, err := jobRepo.Enqueue(ctx, jobqueue.EnqueueInput{
			Type:           "launch-recurring-deposit",
			Payload:        []byte(fmt.Sprintf(`{"job_index": %d}`, j)),
			IdempotencyKey: fmt.Sprintf("launch-job-%d", j),
			Priority:       1,
		})
		if err != nil {
			t.Fatalf("enqueue job %d failed: %v", j, err)
		}
	}

	// Execute concurrent user traffic (deposits and withdrawals)
	var (
		succeeded atomic.Int64
		failed    atomic.Int64
		firstErr  atomic.Pointer[error]
		latencies = make([]time.Duration, totalOps)
	)

	depositAmount := decimal.NewFromInt(15)
	withdrawAmount := decimal.NewFromInt(10)

	start := time.Now()
	var wg sync.WaitGroup
	wg.Add(concurrency)

	for workerIdx := 0; workerIdx < concurrency; workerIdx++ {
		go func(wIdx int) {
			defer wg.Done()
			vID := vaultIDs[wIdx]

			for op := 0; op < opsPerUser; op++ {
				opStart := time.Now()
				var err error

				if op%2 == 0 {
					hash := fmt.Sprintf("launch-dep-%d-%d", wIdx, op)
					verifier.expect(hash, depositAmount)
					_, err = svc.RecordDeposit(ctx, RecordDepositInput{
						VaultID: vID,
						UserID:  userID,
						Amount:  depositAmount,
						TxHash:  hash,
					})
				} else {
					hash := fmt.Sprintf("launch-wd-%d-%d", wIdx, op)
					verifier.expect(hash, withdrawAmount)
					_, err = svc.RecordWithdrawal(ctx, RecordWithdrawalInput{
						VaultID: vID,
						UserID:  userID,
						Amount:  withdrawAmount,
						TxHash:  hash,
					})
				}

				latencies[wIdx*opsPerUser+op] = time.Since(opStart)

				if err != nil {
					failed.Add(1)
					firstErr.CompareAndSwap(nil, &err)
				} else {
					succeeded.Add(1)
				}
			}
		}(workerIdx)
	}

	wg.Wait()
	elapsed := time.Since(start)

	// Wait for job queue to drain background jobs
	waitFor(t, 5*time.Second, func() bool {
		stats, err := jobRepo.Stats(ctx, time.Now())
		if err != nil {
			return false
		}
		return stats.Running == 0 && queueSuccess.Load() >= int64(queueJobsCount)
	})

	sok := succeeded.Load()
	sbad := failed.Load()
	if sok+sbad != int64(totalOps) {
		t.Fatalf("accounted %d operations, ran %d", sok+sbad, totalOps)
	}

	sort.Slice(latencies, func(i, j int) bool { return latencies[i] < latencies[j] })
	p50 := percentile(latencies, 50)
	p95 := percentile(latencies, 95)
	p99 := percentile(latencies, 99)
	throughput := float64(totalOps) / elapsed.Seconds()
	errorRate := float64(sbad) / float64(totalOps)

	t.Logf("=== LAUNCH-DAY LOAD TEST REPORT ===")
	t.Logf("Simulated Users: %d | Ops/User: %d | Total Money Ops: %d", concurrency, opsPerUser, totalOps)
	t.Logf("Job Queue Processed: %d / %d jobs", queueSuccess.Load(), queueJobsCount)
	t.Logf("Duration: %v | Throughput: %.0f ops/sec", elapsed.Round(time.Millisecond), throughput)
	t.Logf("Latencies -> p50: %v | p95: %v | p99: %v | max: %v",
		p50.Round(time.Microsecond), p95.Round(time.Microsecond),
		p99.Round(time.Microsecond), latencies[len(latencies)-1].Round(time.Microsecond))
	t.Logf("Errors: %d / %d (%.3f%%)", sbad, totalOps, errorRate*100)

	if errPtr := firstErr.Load(); errPtr != nil {
		t.Logf("First encountered load error: %v", *errPtr)
	}

	// Validate SLO thresholds
	if errorRate > errorBudget {
		t.Errorf("launch load error rate %.4f%% exceeded maximum budget of %.4f%% (%d failed operations)",
			errorRate*100, errorBudget*100, sbad)
	}
	if p95 > p95Budget {
		t.Errorf("launch load p95 latency %v exceeded maximum budget of %v",
			p95.Round(time.Microsecond), p95Budget)
	}
	if queueSuccess.Load() < int64(queueJobsCount) {
		t.Errorf("job queue failed to process all launch jobs: processed %d, expected %d",
			queueSuccess.Load(), queueJobsCount)
	}

	// Verify mathematical invariant of final vault balances after concurrent stress
	depositsCount := int64(opsPerUser+1) / 2
	withdrawalsCount := int64(opsPerUser) / 2
	wantBalance := seedBalance.
		Add(depositAmount.Mul(decimal.NewFromInt(depositsCount))).
		sub(withdrawAmount.Mul(decimal.NewFromInt(withdrawalsCount)))

	for i, vID := range vaultIDs {
		finalVault, err := svc.GetVault(ctx, vID)
		if err != nil {
			t.Fatalf("GetVault %d after load failed: %v", i, err)
		}
		if !finalVault.CurrentBalance.Equal(wantBalance) {
			t.Fatalf("vault %d balance mismatch after launch load: got %s, want %s (concurrent double-spend or lost update detected)",
				i, finalVault.CurrentBalance.String(), wantBalance.String())
		}
	}
}

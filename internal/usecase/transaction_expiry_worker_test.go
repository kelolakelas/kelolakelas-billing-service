package usecase

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/kelolakelas/kelolakelas-billing-service/internal/config"
)

type expiryClock struct{ now time.Time }

func (c expiryClock) Now() time.Time { return c.now }

// expiryRepoStub hands out pre-set batch sizes so the worker loop can be driven
// deterministically, and records every call it receives. afterCall lets a test
// mutate external state (for example cancel the context) between batches.
type expiryRepoStub struct {
	mu        sync.Mutex
	batches   []int64
	err       error
	calls     int
	lastNow   time.Time
	lastLimit int
	afterCall func()
}

func (r *expiryRepoStub) ExpireDue(_ context.Context, now time.Time, limit int) (int64, error) {
	r.mu.Lock()
	r.calls++
	r.lastNow = now
	r.lastLimit = limit
	next := int64(0)
	if r.err == nil && len(r.batches) > 0 {
		next = r.batches[0]
		r.batches = r.batches[1:]
	}
	after := r.afterCall
	r.mu.Unlock()

	if after != nil {
		after()
	}
	if r.err != nil {
		return 0, r.err
	}
	return next, nil
}

func (r *expiryRepoStub) callCount() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.calls
}

func expiryConfig() config.Config {
	return config.Config{TransactionExpiryWorkerIntervalMinutes: 5}
}

func TestTransactionExpiryWorkerDrainsEveryBatch(t *testing.T) {
	now := time.Date(2026, time.September, 20, 10, 0, 0, 0, time.UTC)
	repo := &expiryRepoStub{batches: []int64{transactionExpiryBatchSize, 3}}
	worker := NewTransactionExpiryWorker(repo, expiryConfig(), expiryClock{now: now})

	worker.RunOnce(context.Background())

	if calls := repo.callCount(); calls != 3 {
		t.Fatalf("ExpireDue calls = %d, want 3 (two non-empty batches plus the empty one)", calls)
	}
	if !repo.lastNow.Equal(now) {
		t.Fatalf("ExpireDue now = %v, want %v", repo.lastNow, now)
	}
	if repo.lastLimit != transactionExpiryBatchSize {
		t.Fatalf("ExpireDue limit = %d, want %d", repo.lastLimit, transactionExpiryBatchSize)
	}
}

func TestTransactionExpiryWorkerStopsWhenNothingIsDue(t *testing.T) {
	repo := &expiryRepoStub{}
	worker := NewTransactionExpiryWorker(repo, expiryConfig(), expiryClock{now: time.Now()})

	worker.RunOnce(context.Background())

	if calls := repo.callCount(); calls != 1 {
		t.Fatalf("ExpireDue calls = %d, want 1", calls)
	}
}

func TestTransactionExpiryWorkerStopsWhenContextIsCanceled(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	// The context is cancelled after the first batch, so the loop must stop even
	// though more work is available.
	repo := &expiryRepoStub{
		batches:   []int64{transactionExpiryBatchSize, transactionExpiryBatchSize, transactionExpiryBatchSize},
		afterCall: cancel,
	}
	worker := NewTransactionExpiryWorker(repo, expiryConfig(), expiryClock{now: time.Now()})

	worker.RunOnce(ctx)

	if calls := repo.callCount(); calls != 1 {
		t.Fatalf("ExpireDue calls = %d, want 1 (the loop must stop once the context is done)", calls)
	}

	// An already cancelled context must never reach the repository.
	canceledCtx, cancelNow := context.WithCancel(context.Background())
	cancelNow()
	repo.batches = []int64{transactionExpiryBatchSize}
	worker.RunOnce(canceledCtx)
	if calls := repo.callCount(); calls != 1 {
		t.Fatalf("ExpireDue calls after a canceled context = %d, want 1", calls)
	}
}

func TestTransactionExpiryWorkerStopsOnRepositoryError(t *testing.T) {
	repo := &expiryRepoStub{err: errors.New("database is unavailable")}
	worker := NewTransactionExpiryWorker(repo, expiryConfig(), expiryClock{now: time.Now()})

	worker.RunOnce(context.Background())

	if calls := repo.callCount(); calls != 1 {
		t.Fatalf("ExpireDue calls = %d, want 1", calls)
	}
}

func TestTransactionExpiryWorkerWithoutRepositoryIsInert(t *testing.T) {
	worker := NewTransactionExpiryWorker(nil, expiryConfig())
	worker.RunOnce(context.Background())

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	done := make(chan struct{})
	go func() { worker.Run(ctx); close(done) }()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("worker did not stop after context cancellation")
	}
}

func TestTransactionExpiryWorkerRunStopsOnCanceledContext(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	repo := &expiryRepoStub{}
	worker := NewTransactionExpiryWorker(repo, expiryConfig())
	done := make(chan struct{})
	go func() { worker.Run(ctx); close(done) }()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("worker did not stop after context cancellation")
	}
	if calls := repo.callCount(); calls != 0 {
		t.Fatalf("ExpireDue calls = %d, want 0", calls)
	}
}

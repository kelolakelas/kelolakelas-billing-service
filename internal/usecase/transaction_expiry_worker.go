package usecase

import (
	"context"
	"log/slog"
	"time"

	"github.com/kelolakelas/kelolakelas-billing-service/internal/config"
	"github.com/kelolakelas/kelolakelas-billing-service/internal/repository"
)

const transactionExpiryBatchSize = 200

// TransactionExpiryWorker moves unpaid transactions whose invoice validity has
// passed to the terminal `expired` status. Expiry is idempotent and safe to run
// from several replicas at once because the repository claims rows with a single
// conditional update (FOR UPDATE SKIP LOCKED); a transaction therefore expires at
// most once, and a payment that arrives late is still honoured by the callback
// handler.
type TransactionExpiryWorker struct {
	transactions repository.TransactionExpiryRepository
	cfg          config.Config
	clock        Clock
}

func NewTransactionExpiryWorker(transactions repository.TransactionExpiryRepository, cfg config.Config, clock ...Clock) *TransactionExpiryWorker {
	c := Clock(realClock{})
	if len(clock) > 0 && clock[0] != nil {
		c = clock[0]
	}
	return &TransactionExpiryWorker{transactions: transactions, cfg: cfg, clock: c}
}

// RunOnce expires every transaction that is currently due. Rows are processed in
// batches so a large backlog does not hold a single long-running statement; the
// loop stops as soon as a batch expires nothing.
func (w *TransactionExpiryWorker) RunOnce(ctx context.Context) {
	if w == nil || w.transactions == nil {
		return
	}
	for ctx.Err() == nil {
		expired, err := w.transactions.ExpireDue(ctx, w.clock.Now(), transactionExpiryBatchSize)
		if err != nil {
			slog.ErrorContext(ctx, "failed to expire unpaid transactions", "error", err)
			return
		}
		if expired == 0 {
			return
		}
		slog.InfoContext(ctx, "expired unpaid transactions", "count", expired)
	}
}

func (w *TransactionExpiryWorker) Run(ctx context.Context) {
	if w == nil || w.transactions == nil || ctx.Err() != nil {
		return
	}
	interval := time.Duration(w.cfg.TransactionExpiryWorkerIntervalMinutes) * time.Minute
	if interval <= 0 {
		interval = 5 * time.Minute
	}
	w.RunOnce(ctx)
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			w.RunOnce(ctx)
		}
	}
}

package usecase

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/kelolakelas/kelolakelas-billing-service/internal/domain"
)

type reconciliationAdminClock struct{ now time.Time }

func (c reconciliationAdminClock) Now() time.Time { return c.now }

// reconciliationAdminRepoStub records what the operator usecase asked the store for and
// returns a canned page. The usecase's job is to validate the filter and to keep the
// status guard with the statement, so the stub proves which status reached the query and
// how many rows the answer claims were moved.
type reconciliationAdminRepoStub struct {
	items          []domain.PaymentReconciliation
	listErr        error
	requeueErr     error
	requeued       int64
	listCalls      int
	requeueCalls   int
	lastStatus     string
	lastLimit      int
	lastRequeueNow time.Time
	lastRequeueMax int
}

func (s *reconciliationAdminRepoStub) ListByStatus(_ context.Context, status string, limit int) ([]domain.PaymentReconciliation, error) {
	s.listCalls++
	s.lastStatus = status
	s.lastLimit = limit
	return s.items, s.listErr
}

func (s *reconciliationAdminRepoStub) RequeueTerminalFailed(_ context.Context, now time.Time, limit int) (int64, error) {
	s.requeueCalls++
	s.lastRequeueNow = now
	s.lastRequeueMax = limit
	return s.requeued, s.requeueErr
}

// The remaining interface methods exist only so the stub satisfies the repository the
// worker shares with this usecase; the operator flow never calls them.
func (s *reconciliationAdminRepoStub) EnsureActivation(context.Context, *domain.PaymentReconciliation) error {
	return nil
}
func (s *reconciliationAdminRepoStub) EnqueueRelease(context.Context, *domain.PaymentReconciliation) error {
	return nil
}
func (s *reconciliationAdminRepoStub) CancelPendingRelease(context.Context, uuid.UUID) error {
	return nil
}
func (s *reconciliationAdminRepoStub) GetByTransactionID(context.Context, uuid.UUID) (*domain.PaymentReconciliation, error) {
	return nil, nil
}
func (s *reconciliationAdminRepoStub) ClaimDue(context.Context, uuid.UUID, time.Time, time.Duration) (*domain.PaymentReconciliation, error) {
	return nil, nil
}
func (s *reconciliationAdminRepoStub) MarkActive(context.Context, uuid.UUID, time.Time) error {
	return nil
}
func (s *reconciliationAdminRepoStub) MarkRetry(context.Context, uuid.UUID, time.Time, string, int) error {
	return nil
}

// An operator finds a permanently failed job by the status they observe on the row, so the
// filter has to reach the store unchanged.
func TestListReconciliationsForwardsStatusFilter(t *testing.T) {
	repo := &reconciliationAdminRepoStub{items: []domain.PaymentReconciliation{{Status: domain.ReconciliationStatusTerminalFailed}}}
	usecase := NewReconciliationAdminUsecase(repo, reconciliationAdminClock{now: time.Unix(100, 0)})

	result, err := usecase.ListReconciliations(context.Background(), domain.ReconciliationStatusTerminalFailed)
	if err != nil {
		t.Fatalf("ListReconciliations error: %v", err)
	}
	if repo.lastStatus != domain.ReconciliationStatusTerminalFailed {
		t.Fatalf("status=%q, want %q", repo.lastStatus, domain.ReconciliationStatusTerminalFailed)
	}
	if len(result.Items) != 1 {
		t.Fatalf("items=%d, want 1", len(result.Items))
	}
	if repo.lastLimit <= 0 {
		t.Fatalf("limit=%d, want a positive batch bound", repo.lastLimit)
	}
}

// Omitting the filter is how an operator asks for every row, so an empty status must be
// passed through instead of being refused as unknown.
func TestListReconciliationsAllowsEmptyStatus(t *testing.T) {
	repo := &reconciliationAdminRepoStub{}
	usecase := NewReconciliationAdminUsecase(repo, reconciliationAdminClock{now: time.Unix(100, 0)})

	if _, err := usecase.ListReconciliations(context.Background(), ""); err != nil {
		t.Fatalf("ListReconciliations error: %v", err)
	}
	if repo.listCalls != 1 || repo.lastStatus != "" {
		t.Fatalf("calls=%d status=%q, want one unfiltered query", repo.listCalls, repo.lastStatus)
	}
}

// An unknown status must be refused before the store is touched: answering an empty page
// would read as "nothing failed" when the operator actually mistyped the filter.
func TestListReconciliationsRejectsUnknownStatus(t *testing.T) {
	repo := &reconciliationAdminRepoStub{}
	usecase := NewReconciliationAdminUsecase(repo, reconciliationAdminClock{now: time.Unix(100, 0)})

	_, err := usecase.ListReconciliations(context.Background(), "terminal-failed")
	if !errors.Is(err, domain.ErrInvalidReconciliationStatus) {
		t.Fatalf("error=%v, want ErrInvalidReconciliationStatus", err)
	}
	if repo.listCalls != 0 {
		t.Fatalf("list calls=%d, want the store untouched", repo.listCalls)
	}
}

// A store error has to surface so the handler can answer 5xx instead of an empty page that
// hides an outage behind "no failures".
func TestListReconciliationsPropagatesStoreError(t *testing.T) {
	failure := errors.New("database unavailable")
	usecase := NewReconciliationAdminUsecase(&reconciliationAdminRepoStub{listErr: failure}, reconciliationAdminClock{now: time.Unix(100, 0)})

	if _, err := usecase.ListReconciliations(context.Background(), ""); !errors.Is(err, failure) {
		t.Fatalf("error=%v, want the store error", err)
	}
}

// An empty page must serialise as `[]` so a caller can iterate it without a nil check.
func TestListReconciliationsReturnsEmptySliceNotNil(t *testing.T) {
	usecase := NewReconciliationAdminUsecase(&reconciliationAdminRepoStub{}, reconciliationAdminClock{now: time.Unix(100, 0)})

	result, err := usecase.ListReconciliations(context.Background(), "")
	if err != nil {
		t.Fatalf("ListReconciliations error: %v", err)
	}
	if result.Items == nil {
		t.Fatal("Items is nil, want an empty slice")
	}
	if len(result.Items) != 0 {
		t.Fatalf("items=%d, want 0", len(result.Items))
	}
}

// The re-drive reports what was actually moved, and the timestamp it reports is the same
// instant it handed the store, so an operator can correlate the answer with the log line.
func TestRequeueTerminalFailedReportsMovedRows(t *testing.T) {
	now := time.Unix(100, 0)
	repo := &reconciliationAdminRepoStub{requeued: 3}
	usecase := NewReconciliationAdminUsecase(repo, reconciliationAdminClock{now: now})

	result, err := usecase.RequeueTerminalFailedReconciliations(context.Background())
	if err != nil {
		t.Fatalf("RequeueTerminalFailedReconciliations error: %v", err)
	}
	if result.Requeued != 3 {
		t.Fatalf("requeued=%d, want 3", result.Requeued)
	}
	if !result.RequeuedAt.Equal(now) {
		t.Fatalf("requeuedAt=%s, want %s", result.RequeuedAt, now)
	}
	if !repo.lastRequeueNow.Equal(now) {
		t.Fatalf("store now=%s, want %s", repo.lastRequeueNow, now)
	}
	if repo.lastRequeueMax <= 0 {
		t.Fatalf("limit=%d, want a positive batch bound", repo.lastRequeueMax)
	}
}

// A second re-drive with nothing left to move must report zero rather than repeating the
// first count, which is what makes the endpoint safe to replay.
func TestRequeueTerminalFailedReportsZeroWhenNothingEligible(t *testing.T) {
	repo := &reconciliationAdminRepoStub{requeued: 0}
	usecase := NewReconciliationAdminUsecase(repo, reconciliationAdminClock{now: time.Unix(100, 0)})

	result, err := usecase.RequeueTerminalFailedReconciliations(context.Background())
	if err != nil {
		t.Fatalf("RequeueTerminalFailedReconciliations error: %v", err)
	}
	if result.Requeued != 0 {
		t.Fatalf("requeued=%d, want 0", result.Requeued)
	}
}

// Both operations answer 503 rather than panicking when the service was started without a
// reconciliation store.
func TestReconciliationAdminReportsUnavailableStore(t *testing.T) {
	usecase := NewReconciliationAdminUsecase(nil, reconciliationAdminClock{now: time.Unix(100, 0)})

	if _, err := usecase.ListReconciliations(context.Background(), ""); !errors.Is(err, domain.ErrReconciliationUnavailable) {
		t.Fatalf("list error=%v, want ErrReconciliationUnavailable", err)
	}
	if _, err := usecase.RequeueTerminalFailedReconciliations(context.Background()); !errors.Is(err, domain.ErrReconciliationUnavailable) {
		t.Fatalf("requeue error=%v, want ErrReconciliationUnavailable", err)
	}
}

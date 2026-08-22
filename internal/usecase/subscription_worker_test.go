package usecase

import (
	"context"
	"testing"
	"time"

	"github.com/kelolakelas/kelolakelas-billing-service/internal/config"
)

func TestNextBillingDate(t *testing.T) {
	from := time.Date(2026, time.January, 31, 12, 0, 0, 0, time.UTC)
	tests := []struct {
		name  string
		cycle string
		want  time.Time
	}{
		{name: "monthly", cycle: "monthly", want: from.AddDate(0, 1, 0)},
		{name: "quarterly", cycle: "quarterly", want: from.AddDate(0, 3, 0)},
		{name: "yearly", cycle: "yearly", want: from.AddDate(1, 0, 0)},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, err := nextBillingDate(from, test.cycle)
			if err != nil || !got.Equal(test.want) {
				t.Fatalf("nextBillingDate() = %v, %v; want %v", got, err, test.want)
			}
		})
	}
}

func TestSubscriptionWorkerStopsWhenContextIsCanceled(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	worker := NewSubscriptionWorker(nil, nil, nil, nil, configForWorkerTest())
	done := make(chan struct{})
	go func() { worker.Run(ctx); close(done) }()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("worker did not stop after context cancellation")
	}
}

func configForWorkerTest() config.Config { return config.Config{SubscriptionWorkerIntervalMinutes: 1} }

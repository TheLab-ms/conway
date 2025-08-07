package engine

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"golang.org/x/time/rate"
)

// PollWorkqueue implements a very basic workqueue. For every call to the returned polling func:
// - The next item is found (if any)
// - The item is processed
// - The item is either marked as complete or failed
//
// The polling func always returns true after processing an item so that the next
// visible item will be picked up without waiting for the polling interval.
// So it's important to either return a nil item or sql.ErrNoRows from GetNext
// when no more items are ready to be processed.
//
// Items might be logged so it's recommended that T is a stringer.
func PollWorkqueue[T any](wq Workqueue[T]) PollingFunc {
	logger := slog.Default().With("workqueue", fmt.Sprintf("%T", wq))
	return func(ctx context.Context) bool {
		item, err := wq.GetItem(ctx)
		if any(item) == nil || errors.Is(err, sql.ErrNoRows) {
			return false
		}
		if err != nil {
			logger.Error("getting next workqueue item", "error", err)
			return false
		}

		err = wq.ProcessItem(ctx, item)
		if err == nil {
			logger.Debug("processed workqueue item", "item", item)
		} else {
			logger.Error("error while processing workqueue item", "error", err, "item", item)
		}

		err = wq.UpdateItem(ctx, item, err == nil)
		if err != nil {
			logger.Error("updating workqueue status failed", "error", err)
			return false
		}

		return true
	}
}

type Workqueue[T any] interface {
	GetItem(context.Context) (T, error)
	ProcessItem(context.Context, T) error
	UpdateItem(ctx context.Context, item T, success bool) error
}

// WithRateLimiting rate limits calls to ProcessItem of the given workqueue.
func WithRateLimiting[T any](wq Workqueue[T], rps int) Workqueue[T] {
	return &rateLimitedWorkqueue[T]{
		Workqueue: wq,
		limiter:   rate.NewLimiter(rate.Every(time.Second), rps),
	}
}

type rateLimitedWorkqueue[T any] struct {
	Workqueue[T]
	limiter *rate.Limiter
}

func (r *rateLimitedWorkqueue[T]) ProcessItem(ctx context.Context, item T) error {
	if err := r.limiter.Wait(ctx); err != nil {
		return err
	}
	return r.Workqueue.ProcessItem(ctx, item)
}

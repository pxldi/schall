package acquisition

import (
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
)

// startPatience is how long a want's request may stay recorded without being
// handed to the provider. Starting takes at most startTimeout in
// internal/downloads, so a request older than this was left behind by a start
// that was interrupted: on the live instance in October 2026, nine of them
// carried "context canceled" and had held their wants since September.
const startPatience = 10 * time.Minute

// deliveryDeadline is how long a want's started request may stay open before
// it is withdrawn as undelivered. One file that has not arrived in six hours is
// not arriving: the live instance had a transfer slskd had forgotten at 40%
// still polled five weeks later, and queued transfers of wants that had since
// been settled, which queuePatience never looks at.
const deliveryDeadline = 6 * time.Hour

// stalledCheckEvery is how often a sweep looks for stalled fetches.
const stalledCheckEvery = 10 * time.Minute

// stalledBatch bounds how many stalled requests one check withdraws, since each
// one is a call to the provider.
const stalledBatch = 50

const (
	neverStartedFileSummary = "Download didn't start"
	neverStartedSummary     = "Download didn't start. Trying elsewhere."
	stalledFileSummary      = "Download stalled for 6 hours"
	stalledSummary          = "Download stalled. Trying elsewhere."
)

// closeStalledFetches withdraws the requests of wants that ran out of time and
// frees the copies whose request ended while nobody was looking. Without it a
// want stays "downloading" for good and keeps its one open request, so it is
// never looked for again. A failure is logged: the wants that are due must not
// wait on this.
func (service *Service) closeStalledFetches(ctx context.Context) {
	service.stalling.Lock()
	defer service.stalling.Unlock()
	now := service.now()
	if !service.stalledAt.IsZero() && now.Sub(service.stalledAt) < stalledCheckEvery {
		return
	}
	service.stalledAt = now

	if service.fetcher != nil {
		stalled, err := service.store.StalledFetches(
			ctx, now.Add(-startPatience), now.Add(-deliveryDeadline), stalledBatch)
		if err != nil {
			service.logger.Warn().Err(err).Msg("list the stalled fetches")
		}
		for _, fetch := range stalled {
			// Cancel stops the transfer at the provider before the request is
			// marked withdrawn. A provider that cannot be reached leaves the
			// request open, to be tried again by the next check.
			if _, err := service.fetcher.Cancel(ctx, fetch.RequestID); err != nil {
				if !errors.Is(err, pgx.ErrNoRows) {
					service.logger.Warn().Err(err).
						Str("download_request_id", fetch.RequestID.String()).
						Msg("withdraw a stalled fetch")
				}
				continue
			}
			fileSummary, summary := neverStartedFileSummary, neverStartedSummary
			if fetch.Started {
				fileSummary, summary = stalledFileSummary, stalledSummary
			}
			if err := service.store.CloseStalledFetch(ctx, fetch.RequestID, fileSummary, summary); err != nil {
				service.logger.Warn().Err(err).
					Str("download_request_id", fetch.RequestID.String()).
					Msg("close a stalled fetch")
				continue
			}
			service.logger.Info().
				Str("download_request_id", fetch.RequestID.String()).
				Str("acquisition_target_id", fetch.TargetID.String()).
				Bool("started", fetch.Started).
				Msg("withdrew a fetch that ran out of time")
		}
	}

	released, err := service.store.ReleaseAllStaleFetches(ctx)
	if err != nil {
		service.logger.Warn().Err(err).Msg("release the stale fetches")
		return
	}
	if released > 0 {
		service.logger.Info().Int64("copies", released).
			Msg("released copies whose download ended without the file")
	}
}

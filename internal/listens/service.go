// Package listens copies the account's listening history from ListenBrainz
// into the listens table, a page at a time, so the Overview can count it
// locally. It reads; it never writes anything back to the service.
package listens

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/rs/zerolog"

	"github.com/pxldi/schall/internal/db"
	"github.com/pxldi/schall/internal/listenbrainz"
)

// ErrNotConfigured says no ListenBrainz account is stored.
var ErrNotConfigured = errors.New("listenbrainz is not configured")

// ErrDisabled says the account is stored but switched off.
var ErrDisabled = errors.New("listenbrainz is disabled")

// Store is what the sync reads and writes.
type Store interface {
	ListenBrainzSettings(context.Context) (db.ListenBrainzSettingsRow, error)
	ListenBounds(context.Context) (db.ListenBounds, error)
	InsertListens(context.Context, []db.ListenInsert) (int64, error)
}

const (
	// pageSize is half of what ListenBrainz allows in one answer. A page of a
	// thousand is over a megabyte and took the pod more than ten seconds on
	// 2026-09-06; five hundred keeps a page well inside the read timeout.
	pageSize = 500
	// readTimeout bounds one page. The shared client's ten seconds fits a
	// stats answer of a few kilobytes, not a page of history.
	readTimeout = 60 * time.Second
	// pagesPerRun bounds one job so a first backfill does not hold a worker
	// lane for the whole history; the job comes straight back for the rest.
	pagesPerRun = 10
	// backfillDays is how far back the first sync reaches. The Overview reads
	// a year of months and thirty days of listens; anything older is never
	// shown.
	backfillDays = 400
)

// Result says what one pass did.
type Result struct {
	Fetched int
	Stored  int64
	// More is true when the pass stopped at its page budget with history still
	// unread, so the next pass should follow at once.
	More bool
}

// Service runs the sync.
type Service struct {
	store      Store
	userAgent  string
	httpClient *http.Client
	apiURL     string
	logger     zerolog.Logger
	now        func() time.Time
}

// NewService builds one.
func NewService(store Store, userAgent string, logger zerolog.Logger) *Service {
	return &Service{
		store:      store,
		userAgent:  userAgent,
		httpClient: &http.Client{Timeout: readTimeout},
		logger:     logger,
		now:        time.Now,
	}
}

// WithEndpoint points the service at a test server instead of ListenBrainz.
func (service *Service) WithEndpoint(apiURL string, client *http.Client, now func() time.Time) *Service {
	service.apiURL = apiURL
	if client != nil {
		service.httpClient = client
	}
	if now != nil {
		service.now = now
	}
	return service
}

func (service *Service) client(ctx context.Context) (*listenbrainz.Client, string, error) {
	settings, err := service.store.ListenBrainzSettings(ctx)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, "", ErrNotConfigured
	}
	if err != nil {
		return nil, "", fmt.Errorf("read ListenBrainz settings: %w", err)
	}
	if !settings.Enabled {
		return nil, "", ErrDisabled
	}
	if settings.Username == "" {
		return nil, "", ErrNotConfigured
	}
	baseURL := settings.BaseURL
	if service.apiURL != "" {
		baseURL = service.apiURL
	}
	client, err := listenbrainz.NewClient(listenbrainz.Options{
		BaseURL:    baseURL,
		LabsURL:    settings.LabsURL,
		UserAgent:  service.userAgent,
		UserToken:  settings.UserToken.String,
		HTTPClient: service.httpClient,
	})
	if err != nil {
		return nil, "", fmt.Errorf("build ListenBrainz client: %w", err)
	}
	return client, settings.Username, nil
}

// Sync pulls what the table does not hold yet. With history already stored it
// reads forward from the newest listen; with none it fills in backwards from
// now until it reaches backfillDays ago or the account's first listen.
func (service *Service) Sync(ctx context.Context) (Result, error) {
	client, username, err := service.client(ctx)
	if err != nil {
		return Result{}, err
	}
	bounds, err := service.store.ListenBounds(ctx)
	if err != nil {
		return Result{}, err
	}
	if bounds.Count == 0 {
		return service.backfill(ctx, client, username, service.now())
	}
	result, err := service.forward(ctx, client, username, bounds.Newest.Time)
	if err != nil || result.More {
		return result, err
	}
	// A first run that stopped at its page budget left older history unread,
	// and the oldest row says how far it got. Reading on below it, until the
	// floor, is what makes a history longer than one run's budget complete.
	// An account younger than the floor costs one empty page per run here.
	floor := service.now().Add(-backfillDays * 24 * time.Hour)
	if bounds.Oldest.Valid && bounds.Oldest.Time.After(floor) {
		older, err := service.backfill(ctx, client, username, bounds.Oldest.Time)
		result.Fetched += older.Fetched
		result.Stored += older.Stored
		result.More = older.More
		return result, err
	}
	return result, nil
}

func (service *Service) forward(
	ctx context.Context, client *listenbrainz.Client, username string, from time.Time,
) (Result, error) {
	var result Result
	minTS := from
	for page := 0; page < pagesPerRun; page++ {
		listens, err := client.Listens(ctx, username, minTS, time.Time{}, pageSize)
		if err != nil {
			return result, fmt.Errorf("read listens after %s: %w", minTS.Format(time.RFC3339), err)
		}
		if len(listens) == 0 {
			return result, nil
		}
		stored, err := service.store.InsertListens(ctx, converted(listens))
		if err != nil {
			return result, err
		}
		result.Fetched += len(listens)
		result.Stored += stored
		newest := minTS
		for _, listen := range listens {
			if listen.ListenedAt.After(newest) {
				newest = listen.ListenedAt
			}
		}
		if len(listens) < pageSize || !newest.After(minTS) {
			return result, nil
		}
		minTS = newest
	}
	result.More = true
	return result, nil
}

func (service *Service) backfill(
	ctx context.Context, client *listenbrainz.Client, username string, from time.Time,
) (Result, error) {
	var result Result
	maxTS := from
	floor := service.now().Add(-backfillDays * 24 * time.Hour)
	for page := 0; page < pagesPerRun; page++ {
		listens, err := client.Listens(ctx, username, time.Time{}, maxTS, pageSize)
		if err != nil {
			return result, fmt.Errorf("read listens before %s: %w", maxTS.Format(time.RFC3339), err)
		}
		if len(listens) == 0 {
			return result, nil
		}
		stored, err := service.store.InsertListens(ctx, converted(listens))
		if err != nil {
			return result, err
		}
		result.Fetched += len(listens)
		result.Stored += stored
		oldest := maxTS
		for _, listen := range listens {
			if listen.ListenedAt.Before(oldest) {
				oldest = listen.ListenedAt
			}
		}
		if len(listens) < pageSize || oldest.Before(floor) || !oldest.Before(maxTS) {
			return result, nil
		}
		maxTS = oldest
	}
	result.More = true
	return result, nil
}

func converted(listens []listenbrainz.Listen) []db.ListenInsert {
	out := make([]db.ListenInsert, 0, len(listens))
	for _, listen := range listens {
		out = append(out, db.ListenInsert{
			ListenedAt:     listen.ListenedAt,
			ArtistName:     listen.ArtistName,
			TrackName:      listen.TrackName,
			ReleaseName:    listen.ReleaseName,
			RecordingMBID:  listen.RecordingMBID,
			ReleaseMBID:    listen.ReleaseMBID,
			CAAReleaseMBID: listen.CAAReleaseMBID,
			ArtistMBIDs:    listen.ArtistMBIDs,
		})
	}
	return out
}

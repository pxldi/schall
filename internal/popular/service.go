// Package popular reads which of an artist's recordings people listen to most,
// from ListenBrainz, so an artist's page can list them. It reads and caches;
// it never writes anything back.
package popular

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/rs/zerolog"

	"github.com/pxldi/schall/internal/db"
	"github.com/pxldi/schall/internal/listenbrainz"
)

// ErrNoToken says no ListenBrainz user token is stored. ListenBrainz answers
// its popularity endpoints only to callers with one.
var ErrNoToken = errors.New("popular tracks need a ListenBrainz user token")

// Store is what the service reads its account from.
type Store interface {
	ListenBrainzSettings(context.Context) (db.ListenBrainzSettingsRow, error)
}

const (
	// limit is how many recordings an artist's page lists.
	limit = 50
	// ttl is how long one artist's answer is kept. Listen counts move over
	// weeks, and a page opened twice should not cost ListenBrainz two calls.
	ttl = 24 * time.Hour
	// requestTimeout bounds one call, so a slow service leaves the box empty
	// rather than holding the page's request open.
	requestTimeout = 10 * time.Second
)

type entry struct {
	recordings []listenbrainz.PopularRecording
	fetchedAt  time.Time
}

// Service answers per artist MusicBrainz ID.
type Service struct {
	store      Store
	userAgent  string
	httpClient *http.Client
	apiURL     string
	logger     zerolog.Logger
	now        func() time.Time

	mu    sync.Mutex
	cache map[string]entry
}

// NewService builds one.
func NewService(store Store, userAgent string, logger zerolog.Logger) *Service {
	return &Service{
		store:      store,
		userAgent:  userAgent,
		httpClient: &http.Client{Timeout: requestTimeout},
		logger:     logger,
		now:        time.Now,
		cache:      map[string]entry{},
	}
}

// WithEndpoint points the service at a test server instead of ListenBrainz.
func (service *Service) WithEndpoint(apiURL string, now func() time.Time) *Service {
	service.apiURL = apiURL
	if now != nil {
		service.now = now
	}
	return service
}

// ForArtist returns the artist's most listened recordings, most listened
// first. An artist ListenBrainz has no counts for is an empty list.
func (service *Service) ForArtist(ctx context.Context, artistMBID string) ([]listenbrainz.PopularRecording, error) {
	key := strings.ToLower(strings.TrimSpace(artistMBID))
	service.mu.Lock()
	cached, ok := service.cache[key]
	service.mu.Unlock()
	if ok && service.now().Sub(cached.fetchedAt) < ttl {
		return cached.recordings, nil
	}

	settings, err := service.store.ListenBrainzSettings(ctx)
	if errors.Is(err, pgx.ErrNoRows) || (err == nil && strings.TrimSpace(settings.UserToken.String) == "") {
		return nil, ErrNoToken
	}
	if err != nil {
		return nil, fmt.Errorf("read ListenBrainz settings: %w", err)
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
		return nil, fmt.Errorf("build ListenBrainz client: %w", err)
	}

	recordings, err := client.TopRecordingsForArtist(ctx, key)
	if errors.Is(err, listenbrainz.ErrNoPopularity) {
		recordings, err = []listenbrainz.PopularRecording{}, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read popular recordings for %s: %w", key, err)
	}
	if len(recordings) > limit {
		recordings = recordings[:limit]
	}

	service.mu.Lock()
	service.cache[key] = entry{recordings: recordings, fetchedAt: service.now()}
	service.mu.Unlock()
	return recordings, nil
}

package server

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/pxldi/schall/internal/coverart"
	"github.com/pxldi/schall/internal/db"
	"github.com/pxldi/schall/internal/library"
	"github.com/pxldi/schall/internal/matching"
	"github.com/pxldi/schall/internal/musicbrainz"
	"github.com/rs/zerolog"
)

type fakeStore struct {
	artists         []db.ListArtistsRow
	artistParams    db.ListArtistsParams
	artistTotals    db.ArtistListTotalsRow
	artistTotalsFor string
	catalogueGenres []string
	artist          db.GetArtistRow
	counts          db.ArtistDiscography
	countsFor       uuid.UUID
	// artistIDs answers which artists the catalogue knows by name, and is kept
	// apart from err because a seeded form is an extra: the entries still read
	// back when the lookup fails.
	artistIDs       map[string]uuid.UUID
	artistIDsAsked  []string
	artistIDsErr    error
	created         db.CreateArtistRow
	params          db.CreateArtistParams
	followed        db.FollowArtistByIDRow
	followedID      uuid.UUID
	followErr       error
	unfollowed      db.UnfollowArtistRow
	unfollowedID    uuid.UUID
	unfollowErr     error
	getArtistErr    error
	albums          db.AlbumPage
	album           db.GetAlbumRow
	tracks          []db.ListAlbumTracksRow
	trackForWanting db.GetTrackForWantingRow
	// trackForWantingErr answers GetTrackForWanting alone, because a missing
	// track and a store that fails are different tests.
	trackForWantingErr error
	discography        []db.ArtistTracksForWantingRow
	monitorLevel       string
	// wantMissing is the standing "want what is missing" as the store last
	// recorded it, and wantMissingErr answers that write alone.
	wantMissing     bool
	wantMissingErr  error
	albumParams     db.ListAlbumsParams
	refresh         db.QueueArtistRefreshRow
	refreshArtistID uuid.UUID
	libraryRoots    []db.LibraryRootRow
	libraryRoot     db.LibraryRootRow
	libraryPath     string
	librarySummary  db.LibrarySummaryRow
	libraryFiles    []db.LibraryFileRow
	libraryParams   db.ListLibraryFilesParams
	libraryScan     db.LibraryScanJobRow
	setAsideFiles   []uuid.UUID
	clearedSetAside []uuid.UUID
	// One file, looked up by its own id, and whether it was found. The player
	// asks for exactly this and nothing else about a file.
	libraryFile    db.LibraryFileRefRow
	libraryFileErr error
	// The bulk re-match: which releases were asked about, which of them the
	// catalogue can ask MusicBrainz about, and which already had a pass.
	refreshQueuedFor          []uuid.UUID
	unaskableAlbums           map[uuid.UUID]bool
	albumRefreshAlreadyQueued map[uuid.UUID]bool
	resolvedFileID            uuid.UUID
	duplicateKept             []keptDuplicate
	heldTwice                 db.HeldTwice
	heldTwiceKept             bool
	heldTwiceLimit            int32
	heldTwiceOffset           int32
	recordingsKept            []keptRecording
	// recordingsAnswered is how many files the store says one answer reached.
	// Zero is the library holding nothing for that recording, which is a
	// different reply from a store that failed.
	recordingsAnswered int64
	err                error
	// The cached picture of a release, and what was written back. cachedCover is
	// the answer to a lookup, so its error says whether anything is cached at all.
	cachedCover    db.ReleaseCoverArtRow
	cachedCoverErr error
	savedCovers    []db.SaveReleaseCoverArtParams
	saveCoverErr   error
	// The covers a person set and then took back: which releases were asked
	// about, how many rows the delete says it touched, and whether it failed.
	removedCovers        []uuid.UUID
	removedCoverRows     int64
	removeCoverErr       error
	cachedArtistImage    db.ArtistImageRow
	cachedArtistImageErr error
	// tracksErr answers ListAlbumTracks alone, because a release that could be
	// read and a track list that could not are different tests. artistsErr does
	// the same for the artist page, which is read after its totals.
	tracksErr  error
	artistsErr error
}

func (store *fakeStore) ReleaseCoverArt(
	context.Context, uuid.UUID,
) (db.ReleaseCoverArtRow, error) {
	return store.cachedCover, store.cachedCoverErr
}

func (store *fakeStore) DeleteUserReleaseCoverArt(
	_ context.Context, albumID uuid.UUID,
) (int64, error) {
	if store.removeCoverErr != nil {
		return 0, store.removeCoverErr
	}
	store.removedCovers = append(store.removedCovers, albumID)
	return store.removedCoverRows, nil
}

func (store *fakeStore) ArtistImage(context.Context, uuid.UUID) (db.ArtistImageRow, error) {
	return store.cachedArtistImage, store.cachedArtistImageErr
}

func (store *fakeStore) SaveReleaseCoverArt(
	_ context.Context, params db.SaveReleaseCoverArtParams,
) error {
	if store.saveCoverErr != nil {
		return store.saveCoverErr
	}
	store.savedCovers = append(store.savedCovers, params)
	return nil
}

func (store *fakeStore) ListArtists(_ context.Context, params db.ListArtistsParams) ([]db.ListArtistsRow, error) {
	store.artistParams = params
	if store.artistsErr != nil {
		return nil, store.artistsErr
	}
	return store.artists, store.err
}

func (store *fakeStore) ArtistListTotals(_ context.Context, params db.ArtistListTotalsParams) (db.ArtistListTotalsRow, error) {
	store.artistTotalsFor = params.Query
	return store.artistTotals, store.err
}

func (store *fakeStore) CatalogueGenres(_ context.Context) ([]string, error) {
	return store.catalogueGenres, store.err
}

func (store *fakeStore) CreateArtist(_ context.Context, params db.CreateArtistParams) (db.CreateArtistRow, error) {
	store.params = params
	return store.created, store.err
}

func (store *fakeStore) GetArtist(context.Context, uuid.UUID) (db.GetArtistRow, error) {
	if store.getArtistErr != nil {
		return db.GetArtistRow{}, store.getArtistErr
	}
	return store.artist, store.err
}

func (store *fakeStore) ArtistDiscography(_ context.Context, artistID uuid.UUID) (db.ArtistDiscography, error) {
	store.countsFor = artistID
	return store.counts, store.err
}

// The artists a name is looked up by are answered from artistIDs alone, so a
// store that fails at everything else can still be asked which artists it knows.
func (store *fakeStore) ArtistMusicBrainzIDsByName(
	_ context.Context, names []string,
) (map[string]uuid.UUID, error) {
	store.artistIDsAsked = names
	return store.artistIDs, store.artistIDsErr
}

func (store *fakeStore) FollowArtistByID(_ context.Context, artistID uuid.UUID) (db.FollowArtistByIDRow, error) {
	store.followedID = artistID
	if store.followErr != nil {
		return db.FollowArtistByIDRow{}, store.followErr
	}
	return store.followed, nil
}

func (store *fakeStore) UnfollowArtist(_ context.Context, artistID uuid.UUID) (db.UnfollowArtistRow, error) {
	store.unfollowedID = artistID
	if store.unfollowErr != nil {
		return db.UnfollowArtistRow{}, store.unfollowErr
	}
	return store.unfollowed, nil
}

func (store *fakeStore) QueueArtistRefresh(_ context.Context, artistID uuid.UUID) (db.QueueArtistRefreshRow, error) {
	store.refreshArtistID = artistID
	return store.refresh, store.err
}

func (store *fakeStore) ListAlbums(_ context.Context, params db.ListAlbumsParams) (db.AlbumPage, error) {
	store.albumParams = params
	return store.albums, store.err
}
func (store *fakeStore) GetAlbum(context.Context, uuid.UUID) (db.GetAlbumRow, error) {
	return store.album, store.err
}
func (store *fakeStore) ListAlbumTracks(context.Context, uuid.UUID) ([]db.ListAlbumTracksRow, error) {
	if store.tracksErr != nil {
		return nil, store.tracksErr
	}
	return store.tracks, store.err
}

func (store *fakeStore) SetArtistMonitorLevel(
	_ context.Context, params db.SetArtistMonitorLevelParams,
) (db.SetArtistMonitorLevelRow, error) {
	if store.err != nil {
		return db.SetArtistMonitorLevelRow{}, store.err
	}
	store.monitorLevel = params.MonitorLevel
	return db.SetArtistMonitorLevelRow{
		ID: params.ArtistID, MonitorLevel: params.MonitorLevel,
	}, nil
}

func (store *fakeStore) SetArtistWantMissing(
	_ context.Context, params db.SetArtistWantMissingParams,
) (db.SetArtistWantMissingRow, error) {
	if store.wantMissingErr != nil {
		return db.SetArtistWantMissingRow{}, store.wantMissingErr
	}
	store.wantMissing = params.Wanted
	return db.SetArtistWantMissingRow{
		ID: params.ArtistID,
		WantMissingSince: pgtype.Timestamptz{
			Time: time.Date(2026, 9, 10, 9, 0, 0, 0, time.UTC), Valid: params.Wanted,
		},
	}, nil
}

func (store *fakeStore) GetTrackForWanting(
	context.Context, uuid.UUID,
) (db.GetTrackForWantingRow, error) {
	if store.trackForWantingErr != nil {
		return db.GetTrackForWantingRow{}, store.trackForWantingErr
	}
	return store.trackForWanting, store.err
}

func (store *fakeStore) ArtistTracksForWanting(
	context.Context, uuid.UUID,
) ([]db.ArtistTracksForWantingRow, error) {
	return store.discography, store.err
}

func (store *fakeStore) DashboardSummary(context.Context) (db.DashboardSummaryRow, error) {
	return db.DashboardSummaryRow{ArtistCount: int64(len(store.artists))}, store.err
}

func (store *fakeStore) ListLibraryRoots(context.Context) ([]db.LibraryRootRow, error) {
	return store.libraryRoots, store.err
}

func (store *fakeStore) CreateLibraryRoot(_ context.Context, path string) (db.LibraryRootRow, error) {
	store.libraryPath = path
	return store.libraryRoot, store.err
}

func (store *fakeStore) LibrarySummary(context.Context) (db.LibrarySummaryRow, error) {
	return store.librarySummary, store.err
}

func (store *fakeStore) ListLibraryFiles(_ context.Context, params db.ListLibraryFilesParams) (db.LibraryFilePage, error) {
	store.libraryParams = params
	items := store.libraryFiles
	if params.Path != "" {
		items = nil
		for _, file := range store.libraryFiles {
			if file.Path == params.Path {
				items = append(items, file)
			}
		}
	}
	return db.LibraryFilePage{Items: items, Total: int64(len(items))}, store.err
}

// The releases queued to be matched again, and what the store answered about
// each. askable is which of them the catalogue can ask MusicBrainz about.
func (store *fakeStore) QueueAlbumRefresh(_ context.Context, albumID uuid.UUID) (bool, bool, error) {
	if store.err != nil {
		return false, false, store.err
	}
	store.refreshQueuedFor = append(store.refreshQueuedFor, albumID)
	if store.unaskableAlbums[albumID] {
		return false, false, nil
	}
	return !store.albumRefreshAlreadyQueued[albumID], true, nil
}

func (store *fakeStore) FileCoverArt(context.Context, uuid.UUID) (db.FileCoverArtRow, error) {
	return db.FileCoverArtRow{}, pgx.ErrNoRows
}

func (store *fakeStore) LibraryFileByID(context.Context, uuid.UUID) (db.LibraryFileRefRow, error) {
	if store.libraryFileErr != nil {
		return db.LibraryFileRefRow{}, store.libraryFileErr
	}
	return store.libraryFile, nil
}

func (store *fakeStore) SetLibraryFileAside(_ context.Context, fileID uuid.UUID) error {
	store.setAsideFiles = append(store.setAsideFiles, fileID)
	return store.err
}

func (store *fakeStore) ClearLibraryFileSetAside(_ context.Context, fileID uuid.UUID) error {
	store.clearedSetAside = append(store.clearedSetAside, fileID)
	return store.err
}

func (store *fakeStore) QueueLibraryScan(context.Context) (db.LibraryScanJobRow, error) {
	return store.libraryScan, store.err
}

func (store *fakeStore) QueueFileResolution(_ context.Context, fileID uuid.UUID) (db.LibraryScanJobRow, error) {
	store.resolvedFileID = fileID
	return store.libraryScan, store.err
}

// keptDuplicate is one answer to "you have this twice", as the store was told it.
type keptDuplicate struct {
	fileID uuid.UUID
	kept   bool
}

func (store *fakeStore) SetDuplicateKept(_ context.Context, fileID uuid.UUID, kept bool) error {
	store.duplicateKept = append(store.duplicateKept, keptDuplicate{fileID: fileID, kept: kept})
	return store.err
}

// keptRecording is the same answer given about a recording rather than a file:
// every copy of it at once.
type keptRecording struct {
	recordingID uuid.UUID
	kept        bool
}

func (store *fakeStore) RecordingsHeldTwice(
	_ context.Context, kept bool, limit, offset int32,
) (db.HeldTwice, error) {
	store.heldTwiceKept = kept
	store.heldTwiceLimit = limit
	store.heldTwiceOffset = offset
	return store.heldTwice, store.err
}

func (store *fakeStore) KeepRecordingHeldTwice(
	_ context.Context, recordingID uuid.UUID, kept bool,
) (int64, error) {
	store.recordingsKept = append(store.recordingsKept,
		keptRecording{recordingID: recordingID, kept: kept})
	return store.recordingsAnswered, store.err
}

type fakeLibraryPaths struct {
	path string
	err  error
}

type fakeMatchResolver struct {
	items      []matching.Candidate
	fileID     uuid.UUID
	trackID    uuid.UUID
	query      string
	limit      int
	reassign   bool
	cleared    bool
	reconciled bool
	err        error
}

func (resolver *fakeMatchResolver) Candidates(context.Context, uuid.UUID) ([]matching.Candidate, error) {
	return resolver.items, resolver.err
}

func (resolver *fakeMatchResolver) SearchTracks(_ context.Context, fileID uuid.UUID, query string, limit int) ([]matching.Candidate, error) {
	resolver.fileID, resolver.query, resolver.limit = fileID, query, limit
	return resolver.items, resolver.err
}

func (resolver *fakeMatchResolver) SetManual(_ context.Context, fileID, trackID uuid.UUID, reassign bool) error {
	resolver.fileID, resolver.trackID, resolver.reassign = fileID, trackID, reassign
	return resolver.err
}

func (resolver *fakeMatchResolver) ClearManual(_ context.Context, fileID uuid.UUID) error {
	resolver.fileID, resolver.cleared = fileID, true
	return resolver.err
}

func (resolver *fakeMatchResolver) ReconcileFile(_ context.Context, fileID uuid.UUID) error {
	resolver.fileID, resolver.reconciled = fileID, true
	return resolver.err
}

func (paths fakeLibraryPaths) Validate(string) (string, error) {
	return paths.path, paths.err
}

type fakeDatabase struct {
	err error
}

type fakeArtistSearcher struct {
	artists []musicbrainz.Artist
	query   string
	limit   int
	err     error
}

func (searcher *fakeArtistSearcher) SearchArtists(_ context.Context, query string, limit int) ([]musicbrainz.Artist, error) {
	searcher.query = query
	searcher.limit = limit
	return searcher.artists, searcher.err
}

func (database fakeDatabase) Ping(context.Context) error {
	return database.err
}

func TestCreateArtist(t *testing.T) {
	id := uuid.New()
	musicBrainzID := uuid.New()
	now := time.Now().UTC().Truncate(time.Microsecond)
	store := &fakeStore{
		created: db.CreateArtistRow{
			ID:         id,
			Name:       "Portishead",
			SortName:   "portishead",
			FollowedAt: pgtype.Timestamptz{Time: now, Valid: true},
		},
	}
	handler := NewAPI(store, fakeDatabase{}, &fakeArtistSearcher{}, zerolog.Nop())
	request := httptest.NewRequest(http.MethodPost, "/api/v1/artists", strings.NewReader(
		`{"musicbrainzId":"`+musicBrainzID.String()+`","name":"  Portishead  ","sortName":"Portishead"}`,
	))
	response := httptest.NewRecorder()

	handler.ServeHTTP(response, request)

	if response.Code != http.StatusCreated {
		t.Fatalf("status = %d, want %d; body = %s", response.Code, http.StatusCreated, response.Body)
	}
	if !store.params.MusicbrainzID.Valid || store.params.MusicbrainzID.UUID != musicBrainzID || store.params.Name != "Portishead" || store.params.SortName != "Portishead" {
		t.Errorf("params = %#v, want selected MusicBrainz identity", store.params)
	}

	var body artistResponse
	if err := json.NewDecoder(response.Body).Decode(&body); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if body.ID != id || body.Name != "Portishead" || !body.FollowedAt.Equal(now) {
		t.Errorf("response = %#v", body)
	}
}

func TestCreateArtistRejectsUnknownFields(t *testing.T) {
	handler := NewAPI(&fakeStore{}, fakeDatabase{}, &fakeArtistSearcher{}, zerolog.Nop())
	request := httptest.NewRequest(http.MethodPost, "/api/v1/artists", strings.NewReader(`{"name":"Björk","automatic":true}`))
	response := httptest.NewRecorder()

	handler.ServeHTTP(response, request)

	if response.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d", response.Code, http.StatusBadRequest)
	}
}

func TestCreateArtistRequiresMusicBrainzIdentity(t *testing.T) {
	handler := NewAPI(&fakeStore{}, fakeDatabase{}, &fakeArtistSearcher{}, zerolog.Nop())
	request := httptest.NewRequest(
		http.MethodPost,
		"/api/v1/artists",
		strings.NewReader(`{"name":"Björk","sortName":"Björk"}`),
	)
	response := httptest.NewRecorder()

	handler.ServeHTTP(response, request)

	if response.Code != http.StatusUnprocessableEntity {
		t.Fatalf("status = %d, want %d", response.Code, http.StatusUnprocessableEntity)
	}
}

func TestCreateArtistRejectsDuplicateMusicBrainzIdentity(t *testing.T) {
	store := &fakeStore{err: &pgconn.PgError{Code: "23505"}}
	handler := NewAPI(store, fakeDatabase{}, &fakeArtistSearcher{}, zerolog.Nop())
	request := httptest.NewRequest(http.MethodPost, "/api/v1/artists", strings.NewReader(
		`{"musicbrainzId":"`+uuid.NewString()+`","name":"Björk","sortName":"Björk"}`,
	))
	response := httptest.NewRecorder()

	handler.ServeHTTP(response, request)

	if response.Code != http.StatusConflict {
		t.Fatalf("status = %d, want %d", response.Code, http.StatusConflict)
	}
}

func TestHealthReportsDatabaseFailure(t *testing.T) {
	handler := NewAPI(&fakeStore{}, fakeDatabase{err: errors.New("offline")}, &fakeArtistSearcher{}, zerolog.Nop())
	request := httptest.NewRequest(http.MethodGet, "/api/v1/health", nil)
	response := httptest.NewRecorder()

	handler.ServeHTTP(response, request)

	if response.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want %d", response.Code, http.StatusServiceUnavailable)
	}
}

func TestSearchArtists(t *testing.T) {
	id := uuid.New()
	searcher := &fakeArtistSearcher{
		artists: []musicbrainz.Artist{{
			ID:             id,
			Name:           "Björk",
			SortName:       "Björk",
			Type:           "Person",
			Country:        "IS",
			Area:           "Iceland",
			Disambiguation: "Icelandic singer",
			Score:          100,
		}},
	}
	handler := NewAPI(&fakeStore{}, fakeDatabase{}, searcher, zerolog.Nop())
	request := httptest.NewRequest(http.MethodGet, "/api/v1/search/artists?q=%20Bj%C3%B6rk%20", nil)
	response := httptest.NewRecorder()

	handler.ServeHTTP(response, request)

	if response.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d; body = %s", response.Code, http.StatusOK, response.Body)
	}
	if searcher.query != "Björk" || searcher.limit != 10 {
		t.Errorf("search = (%q, %d), want (Björk, 10)", searcher.query, searcher.limit)
	}

	var body struct {
		Items []artistSearchResponse `json:"items"`
	}
	if err := json.NewDecoder(response.Body).Decode(&body); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if len(body.Items) != 1 || body.Items[0].MusicBrainzID != id || body.Items[0].Name != "Björk" {
		t.Errorf("response = %#v", body)
	}
}

func TestSearchArtistsRequiresQuery(t *testing.T) {
	handler := NewAPI(&fakeStore{}, fakeDatabase{}, &fakeArtistSearcher{}, zerolog.Nop())
	request := httptest.NewRequest(http.MethodGet, "/api/v1/search/artists?q=a", nil)
	response := httptest.NewRecorder()

	handler.ServeHTTP(response, request)

	if response.Code != http.StatusUnprocessableEntity {
		t.Fatalf("status = %d, want %d", response.Code, http.StatusUnprocessableEntity)
	}
}

func TestGetArtist(t *testing.T) {
	id := uuid.New()
	store := &fakeStore{artist: db.GetArtistRow{
		ID:            id,
		Name:          "Björk",
		SortName:      "Björk",
		FollowedAt:    pgtype.Timestamptz{Time: time.Now(), Valid: true},
		AlbumCount:    12,
		RefreshStatus: "completed",
	}}
	handler := NewAPI(store, fakeDatabase{}, &fakeArtistSearcher{}, zerolog.Nop())
	request := httptest.NewRequest(http.MethodGet, "/api/v1/artists/"+id.String(), nil)
	response := httptest.NewRecorder()

	handler.ServeHTTP(response, request)

	if response.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d; body = %s", response.Code, http.StatusOK, response.Body)
	}
	var body artistDetailResponse
	if err := json.NewDecoder(response.Body).Decode(&body); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if body.ID != id || body.AlbumCount != 12 || body.RefreshStatus != "completed" {
		t.Errorf("response = %#v", body)
	}
}

// The artist's page asks for their releases one page at a time, so the
// completeness it states cannot come from the page. It comes from here.
func TestGetArtistReportsTheWholeDiscographysCompleteness(t *testing.T) {
	id := uuid.New()
	store := &fakeStore{
		artist: db.GetArtistRow{ID: id, Name: "Björk", AlbumCount: 12, RefreshStatus: "completed"},
		counts: db.ArtistDiscography{
			Releases: 40, Owned: 7, Missing: 4, Dismissed: 1, Counted: 11,
		},
	}
	handler := NewAPI(store, fakeDatabase{}, &fakeArtistSearcher{}, zerolog.Nop())
	request := httptest.NewRequest(http.MethodGet, "/api/v1/artists/"+id.String(), nil)
	response := httptest.NewRecorder()

	handler.ServeHTTP(response, request)

	if response.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d; body = %s", response.Code, http.StatusOK, response.Body)
	}
	var body artistDetailResponse
	if err := json.NewDecoder(response.Body).Decode(&body); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	want := artistDiscographyResponse{Releases: 40, Owned: 7, Missing: 4, Dismissed: 1, Counted: 11}
	if body.Discography != want {
		t.Errorf("discography = %#v, want %#v", body.Discography, want)
	}
}

func TestGetArtistCountsTheDiscographyOfTheArtistAsked(t *testing.T) {
	id := uuid.New()
	store := &fakeStore{artist: db.GetArtistRow{ID: id, Name: "Björk"}}
	handler := NewAPI(store, fakeDatabase{}, &fakeArtistSearcher{}, zerolog.Nop())
	request := httptest.NewRequest(http.MethodGet, "/api/v1/artists/"+id.String(), nil)

	handler.ServeHTTP(httptest.NewRecorder(), request)

	if store.countsFor != id {
		t.Errorf("counted discography of %s, want %s", store.countsFor, id)
	}
}

func TestGetArtistNotFound(t *testing.T) {
	handler := NewAPI(&fakeStore{err: pgx.ErrNoRows}, fakeDatabase{}, &fakeArtistSearcher{}, zerolog.Nop())
	request := httptest.NewRequest(http.MethodGet, "/api/v1/artists/"+uuid.NewString(), nil)
	response := httptest.NewRecorder()

	handler.ServeHTTP(response, request)

	if response.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want %d", response.Code, http.StatusNotFound)
	}
}

func TestRefreshArtist(t *testing.T) {
	artistID, jobID := uuid.New(), uuid.New()
	now := time.Now().UTC().Truncate(time.Microsecond)
	store := &fakeStore{refresh: db.QueueArtistRefreshRow{
		ID: jobID, Status: "queued", CreatedAt: pgtype.Timestamptz{Time: now, Valid: true},
	}}
	handler := NewAPI(store, fakeDatabase{}, &fakeArtistSearcher{}, zerolog.Nop())
	request := httptest.NewRequest(http.MethodPost, "/api/v1/artists/"+artistID.String()+"/refresh", nil)
	response := httptest.NewRecorder()

	handler.ServeHTTP(response, request)

	if response.Code != http.StatusAccepted {
		t.Fatalf("status = %d, want %d; body = %s", response.Code, http.StatusAccepted, response.Body)
	}
	if store.refreshArtistID != artistID {
		t.Errorf("artist ID = %s, want %s", store.refreshArtistID, artistID)
	}
	var body refreshResponse
	if err := json.NewDecoder(response.Body).Decode(&body); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if body.JobID != jobID || body.Status != "queued" || !body.CreatedAt.Equal(now) {
		t.Errorf("response = %#v", body)
	}
}

// artistPage is what the artists list looks like on the wire.
type artistPage struct {
	Items             []artistListItemResponse `json:"items"`
	Total             int64                    `json:"total"`
	Limit             int32                    `json:"limit"`
	Offset            int32                    `json:"offset"`
	FollowedCount     int64                    `json:"followedCount"`
	HeldCount         int64                    `json:"heldCount"`
	RefreshingCount   int64                    `json:"refreshingCount"`
	AllCount          int64                    `json:"allCount"`
	IncompleteCount   int64                    `json:"incompleteCount"`
	CompleteCount     int64                    `json:"completeCount"`
	AttentionCount    int64                    `json:"attentionCount"`
	ReleaseTotal      int64                    `json:"releaseTotal"`
	OwnedReleaseTotal int64                    `json:"ownedReleaseTotal"`
}

func listArtistsPage(t *testing.T, store *fakeStore, url string) artistPage {
	t.Helper()
	handler := NewAPI(store, fakeDatabase{}, &fakeArtistSearcher{}, zerolog.Nop())
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, url, nil))
	if response.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d; body = %s", response.Code, http.StatusOK, response.Body)
	}
	var body artistPage
	if err := json.NewDecoder(response.Body).Decode(&body); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	return body
}

func TestListArtistsDefaultsToEveryArtist(t *testing.T) {
	store := &fakeStore{artists: []db.ListArtistsRow{{ID: uuid.New(), Name: "Portishead"}}}

	page := listArtistsPage(t, store, "/api/v1/artists")

	if store.artistParams != (db.ListArtistsParams{}) {
		t.Fatalf("params = %#v", store.artistParams)
	}
	if page.Limit != 0 || page.Offset != 0 {
		t.Errorf("page = %#v", page)
	}
}

func TestListArtistsNarrowsToTheRequestedScope(t *testing.T) {
	store := &fakeStore{}

	listArtistsPage(t, store, "/api/v1/artists?scope=held&q=portishead&limit=20&offset=40")

	if store.artistParams != (db.ListArtistsParams{
		Scope: "held", Query: "portishead", Limit: 20, Offset: 40,
	}) {
		t.Fatalf("params = %#v", store.artistParams)
	}
	// The totals describe the list the user is looking at, so the search
	// reaches them too.
	if store.artistTotalsFor != "portishead" {
		t.Fatalf("totals query = %q, want the search", store.artistTotalsFor)
	}
}

func TestListArtistsReportsTheTotalBeyondThePage(t *testing.T) {
	store := &fakeStore{
		artists: []db.ListArtistsRow{{ID: uuid.New(), Name: "Portishead"}},
		artistTotals: db.ArtistListTotalsRow{
			FollowedCount: 12, HeldCount: 1192, AllCount: 1204,
		},
	}

	page := listArtistsPage(t, store, "/api/v1/artists?limit=1")

	if len(page.Items) != 1 {
		t.Fatalf("items = %d, want 1", len(page.Items))
	}
	if page.Total != 1204 {
		t.Errorf("total = %d, want both halves of the list", page.Total)
	}
}

// A scope narrows the page, not the user's picture of the collection: both
// halves are reported whichever one is showing.
func TestListArtistsReportsBothHalvesUnderANarrowedScope(t *testing.T) {
	store := &fakeStore{artistTotals: db.ArtistListTotalsRow{
		FollowedCount: 12, HeldCount: 1192, AllCount: 12,
	}}

	page := listArtistsPage(t, store, "/api/v1/artists?scope=followed")

	if page.Total != 12 {
		t.Errorf("total = %d, want the followed half", page.Total)
	}
	if page.FollowedCount != 12 || page.HeldCount != 1192 {
		t.Errorf("counts = %d followed, %d held; want both halves reported", page.FollowedCount, page.HeldCount)
	}
}

// The page polls itself while metadata is being fetched. Paging must not turn
// that off just because the artist being refreshed sorts onto another page.
func TestListArtistsReportsRefreshesBeyondThePage(t *testing.T) {
	store := &fakeStore{artistTotals: db.ArtistListTotalsRow{HeldCount: 400, RefreshingCount: 3}}

	page := listArtistsPage(t, store, "/api/v1/artists?limit=1")

	if page.RefreshingCount != 3 {
		t.Errorf("refreshing = %d, want 3", page.RefreshingCount)
	}
}

// Narrowing and ordering by completeness are the server's job because the list
// is paged. A page that answered them itself would be describing one arbitrary
// window of the catalogue as though it were the collection.
func TestListArtistsPassesCompletenessAndSortToTheStore(t *testing.T) {
	store := &fakeStore{}

	listArtistsPage(t, store, "/api/v1/artists?completeness=attention&sort=most-missing")

	if store.artistParams.Completeness != "attention" || store.artistParams.Sort != "most-missing" {
		t.Errorf("params = %#v, want the completeness and sort the request asked for",
			store.artistParams)
	}
}

// The counts label the choices the user is picking between, so each one is
// reported whichever choice is showing.
func TestListArtistsReportsEveryCompletenessCountUnderANarrowedView(t *testing.T) {
	store := &fakeStore{artistTotals: db.ArtistListTotalsRow{
		AllCount: 413, IncompleteCount: 187, CompleteCount: 219, AttentionCount: 7,
		ReleaseTotal: 3110, OwnedReleaseTotal: 1842,
	}}

	page := listArtistsPage(t, store, "/api/v1/artists?completeness=complete")

	if page.Total != 219 {
		t.Errorf("total = %d, want the complete view's own count", page.Total)
	}
	if page.AllCount != 413 || page.IncompleteCount != 187 || page.AttentionCount != 7 {
		t.Errorf("counts = %#v, want every view still counted", page)
	}
	if page.ReleaseTotal != 3110 || page.OwnedReleaseTotal != 1842 {
		t.Errorf("release totals = %d of %d, want 1842 of 3110",
			page.OwnedReleaseTotal, page.ReleaseTotal)
	}
}

func TestListArtistsRejectsAnUnknownCompleteness(t *testing.T) {
	handler := NewAPI(&fakeStore{}, fakeDatabase{}, &fakeArtistSearcher{}, zerolog.Nop())
	response := httptest.NewRecorder()

	handler.ServeHTTP(response,
		httptest.NewRequest(http.MethodGet, "/api/v1/artists?completeness=nearly", nil))

	if response.Code != http.StatusUnprocessableEntity {
		t.Fatalf("status = %d, want %d", response.Code, http.StatusUnprocessableEntity)
	}
}

func TestListArtistsRejectsAnUnknownSort(t *testing.T) {
	handler := NewAPI(&fakeStore{}, fakeDatabase{}, &fakeArtistSearcher{}, zerolog.Nop())
	response := httptest.NewRecorder()

	handler.ServeHTTP(response,
		httptest.NewRequest(http.MethodGet, "/api/v1/artists?sort=popularity", nil))

	if response.Code != http.StatusUnprocessableEntity {
		t.Fatalf("status = %d, want %d", response.Code, http.StatusUnprocessableEntity)
	}
}

func TestListArtistsRejectsAnUnknownScope(t *testing.T) {
	handler := NewAPI(&fakeStore{}, fakeDatabase{}, &fakeArtistSearcher{}, zerolog.Nop())
	response := httptest.NewRecorder()

	handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/api/v1/artists?scope=starred", nil))

	if response.Code != http.StatusUnprocessableEntity {
		t.Fatalf("status = %d, want %d", response.Code, http.StatusUnprocessableEntity)
	}
}

func TestListArtistsRejectsAnOutOfRangeLimit(t *testing.T) {
	handler := NewAPI(&fakeStore{}, fakeDatabase{}, &fakeArtistSearcher{}, zerolog.Nop())
	response := httptest.NewRecorder()

	handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/api/v1/artists?limit=5000", nil))

	if response.Code != http.StatusUnprocessableEntity {
		t.Fatalf("status = %d, want %d", response.Code, http.StatusUnprocessableEntity)
	}
}

// releasePage is what one page of the releases list looks like on the wire.
type releasePage struct {
	Items      []albumResponse `json:"items"`
	Total      int64           `json:"total"`
	Limit      int32           `json:"limit"`
	Offset     int32           `json:"offset"`
	ScopeTotal int64           `json:"scopeTotal"`
}

func listAlbumsPage(t *testing.T, store *fakeStore, url string) releasePage {
	t.Helper()
	handler := NewAPI(store, fakeDatabase{}, &fakeArtistSearcher{}, zerolog.Nop())
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, url, nil))
	if response.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d; body = %s", response.Code, http.StatusOK, response.Body)
	}
	var body releasePage
	if err := json.NewDecoder(response.Body).Decode(&body); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	return body
}

func TestListAlbumsForArtist(t *testing.T) {
	artistID, albumID := uuid.New(), uuid.New()
	store := &fakeStore{albums: db.AlbumPage{Items: []db.AlbumRow{{
		ID:               albumID,
		ArtistID:         artistID,
		ArtistName:       "Portishead",
		Title:            "Dummy",
		AlbumType:        "album",
		FirstReleaseDate: "1994-08",
	}}}}

	body := listAlbumsPage(t, store, "/api/v1/albums?artistId="+artistID.String())

	if !store.albumParams.ArtistID.Valid || store.albumParams.ArtistID.UUID != artistID {
		t.Errorf("artist filter = %#v, want %s", store.albumParams.ArtistID, artistID)
	}
	if len(body.Items) != 1 || body.Items[0].ID != albumID || body.Items[0].FirstReleaseDate != "1994-08" {
		t.Errorf("response = %#v", body)
	}
}

// Asking for no limit asks for the whole list, which is what the missing view
// and an artist's own page still do. A default page here would silently turn
// their totals into totals of the first fifty releases.
func TestListAlbumsWithoutALimitIsUnpaged(t *testing.T) {
	store := &fakeStore{}

	page := listAlbumsPage(t, store, "/api/v1/albums")

	if page.Limit != 0 || store.albumParams.Limit != 0 {
		t.Errorf("limit = %d (store %d), want 0", page.Limit, store.albumParams.Limit)
	}
}

func TestListAlbumsPassesTheBrowsersFiltersThrough(t *testing.T) {
	store := &fakeStore{}

	listAlbumsPage(t, store,
		"/api/v1/albums?scope=library&q=dummy&status=partial&sort=title&direction=desc&limit=20&offset=40")

	want := db.ListAlbumsParams{
		Scope: "library", Query: "dummy", Status: "partial",
		Sort: "title", Descending: true, Limit: 20, Offset: 40,
	}
	if store.albumParams != want {
		t.Errorf("params = %#v, want %#v", store.albumParams, want)
	}
}

// An artist's page narrows by how complete a release is rather than by what
// shape it is in, so that its chips and its header answer the one question.
func TestListAlbumsPassesTheArtistPagesNarrowingThrough(t *testing.T) {
	store := &fakeStore{}

	listAlbumsPage(t, store, "/api/v1/albums?completeness=missing&limit=48")

	if store.albumParams.Completeness != "missing" {
		t.Errorf("completeness = %q, want missing", store.albumParams.Completeness)
	}
}

// The counts are the point of paging: a page that could only report its own
// length would say the catalogue is 48 releases long.
func TestListAlbumsReportsTheTotalsBeyondThePage(t *testing.T) {
	store := &fakeStore{albums: db.AlbumPage{
		Items:      []db.AlbumRow{{ID: uuid.New(), Title: "Dummy"}},
		Total:      312,
		ScopeTotal: 1840,
	}}

	body := listAlbumsPage(t, store, "/api/v1/albums?status=partial&limit=48")

	if len(body.Items) != 1 || body.Total != 312 || body.ScopeTotal != 1840 {
		t.Errorf("response = %#v", body)
	}
	if body.Limit != 48 || body.Offset != 0 {
		t.Errorf("paging = %d/%d, want 48/0", body.Limit, body.Offset)
	}
}

func TestListAlbumsRejectsInvalidArtistFilter(t *testing.T) {
	handler := NewAPI(&fakeStore{}, fakeDatabase{}, &fakeArtistSearcher{}, zerolog.Nop())
	request := httptest.NewRequest(http.MethodGet, "/api/v1/albums?artistId=not-a-uuid", nil)
	response := httptest.NewRecorder()

	handler.ServeHTTP(response, request)

	if response.Code != http.StatusUnprocessableEntity {
		t.Fatalf("status = %d, want %d", response.Code, http.StatusUnprocessableEntity)
	}
}

// A filter nobody implemented is refused rather than ignored: silently widening
// the list would report a count of releases the user did not ask about.
func TestListAlbumsRejectsUnknownFilters(t *testing.T) {
	for _, query := range []string{
		"scope=everything", "status=broken", "sort=loudness", "direction=sideways",
		"limit=0", "limit=500", "offset=-1", "completeness=partial",
	} {
		handler := NewAPI(&fakeStore{}, fakeDatabase{}, &fakeArtistSearcher{}, zerolog.Nop())
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/api/v1/albums?"+query, nil))
		if response.Code != http.StatusUnprocessableEntity {
			t.Errorf("%s: status = %d, want %d", query, response.Code, http.StatusUnprocessableEntity)
		}
	}
}

// Ordering by how much of a release is owned was refused for as long as
// answering it meant counting the whole catalogue. A release carries the number
// now, so the browser may ask for it.
func TestListAlbumsAcceptsOrderingByOwnership(t *testing.T) {
	handler := NewAPI(&fakeStore{}, fakeDatabase{}, &fakeArtistSearcher{}, zerolog.Nop())
	response := httptest.NewRecorder()

	handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/api/v1/albums?sort=owned", nil))

	if response.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", response.Code, http.StatusOK)
	}
}

// The browser's chips each say how many releases they hold, so the counts have
// to reach it under the names it reads them by.
func TestListAlbumsReportsACountForEveryStatus(t *testing.T) {
	store := &fakeStore{albums: db.AlbumPage{
		Items:      []db.AlbumRow{},
		ScopeTotal: 9,
		StatusTotals: map[string]int64{
			"owned": 4, "partial": 3, "missing": 1, "untracked": 1, "failed": 0,
		},
	}}
	handler := NewAPI(store, fakeDatabase{}, &fakeArtistSearcher{}, zerolog.Nop())
	response := httptest.NewRecorder()

	handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/api/v1/albums?limit=48", nil))

	if response.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", response.Code, http.StatusOK)
	}
	var body map[string]any
	if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	for key, want := range map[string]float64{
		"ownedCount": 4, "partialCount": 3, "missingCount": 1,
		"untrackedCount": 1, "failedCount": 0, "scopeTotal": 9,
	} {
		if body[key] != want {
			t.Errorf("%s = %v, want %v", key, body[key], want)
		}
	}
}

// Asking for the whole list rather than a page of it counts nothing, so the
// chips get no answer rather than an answer of nought — which is a number, and
// whoever read it would believe it.
func TestListAlbumsOmitsTheStatusCountsItDidNotCount(t *testing.T) {
	handler := NewAPI(&fakeStore{}, fakeDatabase{}, &fakeArtistSearcher{}, zerolog.Nop())
	response := httptest.NewRecorder()

	handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/api/v1/albums", nil))

	if response.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", response.Code, http.StatusOK)
	}
	var body map[string]any
	if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	for _, key := range []string{
		"ownedCount", "partialCount", "missingCount", "untrackedCount", "failedCount",
	} {
		if _, present := body[key]; present {
			t.Errorf("%s = %v, want it left out", key, body[key])
		}
	}
}

func TestGetAlbumAndTracks(t *testing.T) {
	albumID, artistID, recordingID := uuid.New(), uuid.New(), uuid.New()
	store := &fakeStore{
		album: db.GetAlbumRow{
			ID: albumID, ArtistID: artistID, ArtistName: "Portishead", Title: "Dummy",
			AlbumType: "album", TrackCount: 1, SelectionReason: "Earliest official edition",
		},
		tracks: []db.ListAlbumTracksRow{{
			ID: uuid.New(), MusicbrainzRecordingID: uuid.NullUUID{UUID: recordingID, Valid: true},
			Title: "Mysterons", DiscNumber: 1, TrackNumber: pgtype.Int4{Int32: 1, Valid: true},
		}},
	}
	handler := NewAPI(store, fakeDatabase{}, &fakeArtistSearcher{}, zerolog.Nop())

	detailResponse := httptest.NewRecorder()
	handler.ServeHTTP(detailResponse, httptest.NewRequest(http.MethodGet, "/api/v1/albums/"+albumID.String(), nil))
	if detailResponse.Code != http.StatusOK {
		t.Fatalf("detail status = %d; body = %s", detailResponse.Code, detailResponse.Body)
	}
	var detail albumDetailResponse
	if err := json.NewDecoder(detailResponse.Body).Decode(&detail); err != nil {
		t.Fatalf("decode detail: %v", err)
	}
	if detail.ID != albumID || detail.TrackCount != 1 {
		t.Errorf("detail = %#v", detail)
	}

	tracksResponse := httptest.NewRecorder()
	handler.ServeHTTP(tracksResponse, httptest.NewRequest(http.MethodGet, "/api/v1/albums/"+albumID.String()+"/tracks", nil))
	if tracksResponse.Code != http.StatusOK {
		t.Fatalf("tracks status = %d; body = %s", tracksResponse.Code, tracksResponse.Body)
	}
	var tracks struct {
		Items []trackResponse `json:"items"`
	}
	if err := json.NewDecoder(tracksResponse.Body).Decode(&tracks); err != nil {
		t.Fatalf("decode tracks: %v", err)
	}
	if len(tracks.Items) != 1 || tracks.Items[0].MusicBrainzRecordingID == nil ||
		*tracks.Items[0].MusicBrainzRecordingID != recordingID {
		t.Errorf("tracks = %#v", tracks.Items)
	}
}

// The page draws one tick for music the library holds, and the file behind a
// shared recording is filed under whichever release claimed it first. The row
// has to carry that, or a release nothing maps onto reads as missing music that
// is on the disk.
func TestTheTrackListSaysWhenARecordingIsHeldUnderAnotherRelease(t *testing.T) {
	albumID := uuid.New()
	store := &fakeStore{
		album: db.GetAlbumRow{ID: albumID, Title: "Pt. 1 & 2", AlbumType: "album"},
		tracks: []db.ListAlbumTracksRow{{
			ID: uuid.New(), Title: "Flickering in the Gloom", DiscNumber: 1,
			HeldElsewhere: true,
		}},
	}
	handler := NewAPI(store, fakeDatabase{}, &fakeArtistSearcher{}, zerolog.Nop())

	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(
		http.MethodGet, "/api/v1/albums/"+albumID.String()+"/tracks", nil))

	if response.Code != http.StatusOK {
		t.Fatalf("status = %d; body = %s", response.Code, response.Body)
	}
	var tracks struct {
		Items []trackResponse `json:"items"`
	}
	if err := json.NewDecoder(response.Body).Decode(&tracks); err != nil {
		t.Fatalf("decode tracks: %v", err)
	}
	if len(tracks.Items) != 1 || !tracks.Items[0].HeldElsewhere {
		t.Errorf("tracks = %#v, want the row to say the library holds it", tracks.Items)
	}
	if tracks.Items[0].Owned {
		t.Error("a track this release never mapped reads as mapped onto it")
	}
}

func TestCreateLibraryRootUsesCanonicalValidatedPath(t *testing.T) {
	id := uuid.New()
	now := time.Now().UTC()
	store := &fakeStore{libraryRoot: db.LibraryRootRow{
		ID: id, Path: "/music", Enabled: true,
		CreatedAt: pgtype.Timestamptz{Time: now, Valid: true},
	}}
	handler := NewAPI(
		store, fakeDatabase{}, &fakeArtistSearcher{}, zerolog.Nop(),
		WithLibraryPaths(fakeLibraryPaths{path: "/music"}),
	)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(
		http.MethodPost, "/api/v1/library/roots", strings.NewReader(`{"path":"/music/../music"}`),
	))
	if response.Code != http.StatusCreated {
		t.Fatalf("status = %d; body = %s", response.Code, response.Body)
	}
	if store.libraryPath != "/music" {
		t.Fatalf("stored path = %q", store.libraryPath)
	}
}

func TestCreateLibraryRootRejectsUnsafePath(t *testing.T) {
	handler := NewAPI(
		&fakeStore{}, fakeDatabase{}, &fakeArtistSearcher{}, zerolog.Nop(),
		WithLibraryPaths(fakeLibraryPaths{err: errors.New("path is outside allowed roots")}),
	)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(
		http.MethodPost, "/api/v1/library/roots", strings.NewReader(`{"path":"/etc"}`),
	))
	if response.Code != http.StatusUnprocessableEntity {
		t.Fatalf("status = %d; body = %s", response.Code, response.Body)
	}
}

func TestLibrarySummaryAndScan(t *testing.T) {
	jobID := uuid.New()
	store := &fakeStore{
		librarySummary: db.LibrarySummaryRow{RootCount: 1, FileCount: 42, ScanStatus: "completed"},
		libraryScan: db.LibraryScanJobRow{
			ID: jobID, Status: "queued", CreatedAt: time.Now().UTC(),
		},
	}
	handler := NewAPI(store, fakeDatabase{}, &fakeArtistSearcher{}, zerolog.Nop())

	summaryResponse := httptest.NewRecorder()
	handler.ServeHTTP(summaryResponse, httptest.NewRequest(http.MethodGet, "/api/v1/library", nil))
	if summaryResponse.Code != http.StatusOK {
		t.Fatalf("summary status = %d; body = %s", summaryResponse.Code, summaryResponse.Body)
	}
	var summary librarySummaryResponse
	if err := json.NewDecoder(summaryResponse.Body).Decode(&summary); err != nil {
		t.Fatal(err)
	}
	if summary.RootCount != 1 || summary.FileCount != 42 || summary.ScanStatus != "completed" {
		t.Fatalf("summary = %#v", summary)
	}

	scanResponse := httptest.NewRecorder()
	handler.ServeHTTP(scanResponse, httptest.NewRequest(http.MethodPost, "/api/v1/scan", nil))
	if scanResponse.Code != http.StatusAccepted {
		t.Fatalf("scan status = %d; body = %s", scanResponse.Code, scanResponse.Body)
	}
	var scan refreshResponse
	if err := json.NewDecoder(scanResponse.Body).Decode(&scan); err != nil {
		t.Fatal(err)
	}
	if scan.JobID != jobID {
		t.Fatalf("job ID = %s, want %s", scan.JobID, jobID)
	}
}

func TestListLibraryFilesValidatesAndPassesPaginationFilters(t *testing.T) {
	store := &fakeStore{libraryFiles: []db.LibraryFileRow{{ID: uuid.New(), Path: "/music/song.flac"}}}
	handler := NewAPI(store, fakeDatabase{}, &fakeArtistSearcher{}, zerolog.Nop())

	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(
		http.MethodGet,
		"/api/v1/library/files?status=ambiguous&q=portishead&limit=25&offset=50&unidentified=true",
		nil,
	))
	if response.Code != http.StatusOK {
		t.Fatalf("status = %d; body = %s", response.Code, response.Body)
	}
	if store.libraryParams != (db.ListLibraryFilesParams{
		Status: "ambiguous", Query: "portishead", Limit: 25, Offset: 50, Unidentified: true,
	}) {
		t.Fatalf("params = %#v", store.libraryParams)
	}

	invalid := httptest.NewRecorder()
	handler.ServeHTTP(invalid, httptest.NewRequest(
		http.MethodGet, "/api/v1/library/files?status=guessed", nil,
	))
	if invalid.Code != http.StatusUnprocessableEntity {
		t.Fatalf("invalid status = %d", invalid.Code)
	}
}

// The two states the library summary counts and nothing could list until they
// were added. A number on a screen that leads nowhere is a number nobody can
// act on: fifteen files the scanner could not read is a real problem, and the
// only way to find out which fifteen was to read the server's log.
func TestListLibraryFilesNarrowsToTheScannersOwnStates(t *testing.T) {
	for _, status := range []string{db.StatusMissing, db.StatusUnreadable} {
		t.Run(status, func(t *testing.T) {
			store := &fakeStore{}
			handler := NewAPI(store, fakeDatabase{}, &fakeArtistSearcher{}, zerolog.Nop())

			response := httptest.NewRecorder()
			handler.ServeHTTP(response, httptest.NewRequest(
				http.MethodGet, "/api/v1/library/files?status="+status, nil))

			if response.Code != http.StatusOK {
				t.Fatalf("status = %d; body = %s", response.Code, response.Body)
			}
			if store.libraryParams.Status != status {
				t.Fatalf("params = %#v; want the status passed through", store.libraryParams)
			}
		})
	}
}

// A state nothing records is refused, and the refusal names every state there
// is rather than leaving the reader to guess which word was wrong.
func TestListLibraryFilesRefusesAStateNothingRecords(t *testing.T) {
	handler := NewAPI(&fakeStore{}, fakeDatabase{}, &fakeArtistSearcher{}, zerolog.Nop())

	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(
		http.MethodGet, "/api/v1/library/files?status=broken", nil))

	if response.Code != http.StatusUnprocessableEntity {
		t.Fatalf("status = %d; body = %s", response.Code, response.Body)
	}
	for _, named := range []string{"matched", "duplicate", "missing", "unreadable"} {
		if !strings.Contains(response.Body.String(), named) {
			t.Fatalf("body = %s; want %q named among the states", response.Body, named)
		}
	}
}

func TestSetAsideRoutesRecordAndClearTheFileDecision(t *testing.T) {
	fileID := uuid.New()
	store := &fakeStore{}
	handler := NewAPI(store, fakeDatabase{}, &fakeArtistSearcher{}, zerolog.Nop())

	set := httptest.NewRecorder()
	handler.ServeHTTP(set, httptest.NewRequest(
		http.MethodPost, "/api/v1/library/files/"+fileID.String()+"/set-aside", nil))
	if set.Code != http.StatusNoContent || len(store.setAsideFiles) != 1 || store.setAsideFiles[0] != fileID {
		t.Fatalf("set aside = %d, %v", set.Code, store.setAsideFiles)
	}

	clear := httptest.NewRecorder()
	handler.ServeHTTP(clear, httptest.NewRequest(
		http.MethodDelete, "/api/v1/library/files/"+fileID.String()+"/set-aside", nil))
	if clear.Code != http.StatusNoContent || len(store.clearedSetAside) != 1 || store.clearedSetAside[0] != fileID {
		t.Fatalf("clear set aside = %d, %v", clear.Code, store.clearedSetAside)
	}
}

func TestSetAsideRoutesMapExpectedFailures(t *testing.T) {
	for _, test := range []struct {
		name   string
		method string
		err    error
		status int
	}{
		{name: "missing file", method: http.MethodPost, err: db.ErrLibraryFileNotFound, status: http.StatusNotFound},
		{name: "not set aside", method: http.MethodDelete, err: db.ErrLibraryFileNotSetAside, status: http.StatusNotFound},
	} {
		t.Run(test.name, func(t *testing.T) {
			store := &fakeStore{err: test.err}
			handler := NewAPI(store, fakeDatabase{}, &fakeArtistSearcher{}, zerolog.Nop())
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, httptest.NewRequest(
				test.method, "/api/v1/library/files/"+uuid.NewString()+"/set-aside", nil))
			if response.Code != test.status {
				t.Fatalf("status = %d; body = %s", response.Code, response.Body)
			}
		})
	}
}

// fakeRemover stands in for the one thing Schall does that cannot be undone.
type fakeRemover struct {
	removed []uuid.UUID
	path    string
	err     error
	// kept records the (recording, copy) pairs "keep this copy" asked about, and
	// keptErr is what that press is answered with.
	kept    []keptCopy
	keptErr error
	retried int
	// sweptEach is what "keep one of each" is answered with, and sweeps counts
	// how many times it was asked for.
	sweptEach library.SweptDuplicates
	sweptErr  error
	sweeps    int
}

func (remover *fakeRemover) KeepOneOfEach(context.Context, string) (library.SweptDuplicates, error) {
	remover.sweeps++
	return remover.sweptEach, remover.sweptErr
}

type keptCopy struct {
	recordingID uuid.UUID
	fileID      uuid.UUID
	deleting    string
}

func (remover *fakeRemover) Remove(_ context.Context, fileID uuid.UUID) (string, error) {
	remover.removed = append(remover.removed, fileID)
	return remover.path, remover.err
}

func (remover *fakeRemover) RetryUnlinks(context.Context) int {
	remover.retried++
	return remover.retried
}

func (remover *fakeRemover) KeepOne(
	_ context.Context, recordingID, fileID uuid.UUID, deleting []uuid.UUID, _ string,
) (library.KeptCopy, error) {
	named := make([]string, 0, len(deleting))
	for _, one := range deleting {
		named = append(named, one.String())
	}
	remover.kept = append(remover.kept, keptCopy{
		recordingID: recordingID, fileID: fileID, deleting: strings.Join(named, ","),
	})
	if remover.keptErr != nil {
		return library.KeptCopy{}, remover.keptErr
	}
	return library.KeptCopy{
		Kept: fileID, KeptPath: remover.path, RemovedPaths: []string{"/music/other.mp3"},
	}, nil
}

// Deleting is the answer to "you have this twice", so it has to reach the file
// the caller named — the one thing here nothing can take back.
func TestDeletingALibraryFileRemovesTheFileThatWasNamed(t *testing.T) {
	fileID := uuid.New()
	remover := &fakeRemover{path: "/music/song.flac"}
	response := httptest.NewRecorder()
	NewAPI(&fakeStore{}, fakeDatabase{}, &fakeArtistSearcher{}, zerolog.Nop(),
		WithFileRemover(remover)).
		ServeHTTP(response, httptest.NewRequest(
			http.MethodDelete, "/api/v1/library/files/"+fileID.String(), nil,
		))
	if response.Code != http.StatusNoContent {
		t.Fatalf("status = %d; body = %s", response.Code, response.Body)
	}
	if len(remover.removed) != 1 || remover.removed[0] != fileID {
		t.Fatalf("removed = %v, want one %s", remover.removed, fileID)
	}
}

func TestDeletingALibraryFileTheLibraryDoesNotHold(t *testing.T) {
	response := httptest.NewRecorder()
	NewAPI(&fakeStore{}, fakeDatabase{}, &fakeArtistSearcher{}, zerolog.Nop(),
		WithFileRemover(&fakeRemover{err: library.ErrFileGone})).
		ServeHTTP(response, httptest.NewRequest(
			http.MethodDelete, "/api/v1/library/files/"+uuid.New().String(), nil,
		))
	if response.Code != http.StatusNotFound {
		t.Fatalf("status = %d; body = %s", response.Code, response.Body)
	}
}

// An installation wired without a remover says so rather than answering as
// though the file went away.
func TestDeletingALibraryFileWithoutARemoverIsUnavailable(t *testing.T) {
	response := httptest.NewRecorder()
	NewAPI(&fakeStore{}, fakeDatabase{}, &fakeArtistSearcher{}, zerolog.Nop()).
		ServeHTTP(response, httptest.NewRequest(
			http.MethodDelete, "/api/v1/library/files/"+uuid.New().String(), nil,
		))
	if response.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d; body = %s", response.Code, response.Body)
	}
}

func TestKeepingADuplicateIsRecorded(t *testing.T) {
	fileID := uuid.New()
	store := &fakeStore{}
	response := httptest.NewRecorder()
	NewAPI(store, fakeDatabase{}, &fakeArtistSearcher{}, zerolog.Nop()).
		ServeHTTP(response, httptest.NewRequest(
			http.MethodPost, "/api/v1/library/files/"+fileID.String()+"/keep", nil,
		))
	if response.Code != http.StatusNoContent {
		t.Fatalf("status = %d; body = %s", response.Code, response.Body)
	}
	if len(store.duplicateKept) != 1 ||
		store.duplicateKept[0] != (keptDuplicate{fileID: fileID, kept: true}) {
		t.Fatalf("kept = %#v", store.duplicateKept)
	}
}

// Somebody who meant to have a recording twice can change their mind, and the
// question comes back rather than being lost.
func TestKeepingADuplicateIsWithdrawnByTheSameRoute(t *testing.T) {
	fileID := uuid.New()
	store := &fakeStore{}
	response := httptest.NewRecorder()
	NewAPI(store, fakeDatabase{}, &fakeArtistSearcher{}, zerolog.Nop()).
		ServeHTTP(response, httptest.NewRequest(
			http.MethodDelete, "/api/v1/library/files/"+fileID.String()+"/keep", nil,
		))
	if response.Code != http.StatusNoContent {
		t.Fatalf("status = %d; body = %s", response.Code, response.Body)
	}
	if len(store.duplicateKept) != 1 ||
		store.duplicateKept[0] != (keptDuplicate{fileID: fileID, kept: false}) {
		t.Fatalf("kept = %#v", store.duplicateKept)
	}
}

func TestRecordingsHeldTwiceAreListedWithEveryCopy(t *testing.T) {
	recordingID := uuid.New()
	store := &fakeStore{heldTwice: db.HeldTwice{Total: 1, Recordings: []db.HeldTwiceRecording{{
		RecordingID: recordingID, Title: "Teardrop", Artist: "Massive Attack",
		Copies: []db.HeldTwiceCopy{
			{ID: uuid.New(), Path: "/music/Mezzanine/06 Teardrop.flac", Format: "FLAC"},
			{ID: uuid.New(), Path: "/music/singles/Teardrop.mp3", Format: "MP3"},
		},
	}}}}
	response := httptest.NewRecorder()
	NewAPI(store, fakeDatabase{}, &fakeArtistSearcher{}, zerolog.Nop()).
		ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/api/v1/library/duplicates", nil))
	if response.Code != http.StatusOK {
		t.Fatalf("status = %d; body = %s", response.Code, response.Body)
	}
	var body struct {
		Total      int `json:"total"`
		Recordings []struct {
			RecordingID string `json:"recordingId"`
			Copies      []struct {
				Format string `json:"format"`
			} `json:"copies"`
		} `json:"recordings"`
	}
	if err := json.NewDecoder(response.Body).Decode(&body); err != nil {
		t.Fatal(err)
	}
	if body.Total != 1 || len(body.Recordings) != 1 {
		t.Fatalf("body = %#v", body)
	}
	if body.Recordings[0].RecordingID != recordingID.String() ||
		len(body.Recordings[0].Copies) != 2 {
		t.Fatalf("recording = %#v", body.Recordings[0])
	}
}

// Every other list on the library screen pages at 25 rows through limit and
// offset, and this one held everything in memory instead. A library with
// hundreds of recordings held twice mounted an audio player per copy on first
// load; this is the same contract listLibraryFiles already honours.
func TestRecordingsHeldTwicePagesWithLimitAndOffset(t *testing.T) {
	store := &fakeStore{}
	response := httptest.NewRecorder()
	NewAPI(store, fakeDatabase{}, &fakeArtistSearcher{}, zerolog.Nop()).
		ServeHTTP(response, httptest.NewRequest(
			http.MethodGet, "/api/v1/library/duplicates?limit=25&offset=50", nil,
		))
	if response.Code != http.StatusOK {
		t.Fatalf("status = %d; body = %s", response.Code, response.Body)
	}
	if store.heldTwiceLimit != 25 || store.heldTwiceOffset != 50 {
		t.Fatalf("limit = %d, offset = %d", store.heldTwiceLimit, store.heldTwiceOffset)
	}
	var body struct {
		Limit  int32 `json:"limit"`
		Offset int32 `json:"offset"`
	}
	if err := json.NewDecoder(response.Body).Decode(&body); err != nil {
		t.Fatal(err)
	}
	if body.Limit != 25 || body.Offset != 50 {
		t.Fatalf("body = %#v", body)
	}
}

// Without a limit the handler still asks the store for a bounded page, the
// same default listLibraryFiles falls back to.
func TestRecordingsHeldTwiceDefaultsItsPage(t *testing.T) {
	store := &fakeStore{}
	response := httptest.NewRecorder()
	NewAPI(store, fakeDatabase{}, &fakeArtistSearcher{}, zerolog.Nop()).
		ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/api/v1/library/duplicates", nil))
	if response.Code != http.StatusOK {
		t.Fatalf("status = %d; body = %s", response.Code, response.Body)
	}
	if store.heldTwiceLimit != 200 || store.heldTwiceOffset != 0 {
		t.Fatalf("limit = %d, offset = %d", store.heldTwiceLimit, store.heldTwiceOffset)
	}
}

func TestRecordingsHeldTwiceRefusesAnOutOfRangeLimit(t *testing.T) {
	response := httptest.NewRecorder()
	NewAPI(&fakeStore{}, fakeDatabase{}, &fakeArtistSearcher{}, zerolog.Nop()).
		ServeHTTP(response, httptest.NewRequest(
			http.MethodGet, "/api/v1/library/duplicates?limit=501", nil,
		))
	if response.Code != http.StatusUnprocessableEntity {
		t.Fatalf("status = %d; body = %s", response.Code, response.Body)
	}
}

func TestRecordingsHeldTwiceRefusesANegativeOffset(t *testing.T) {
	response := httptest.NewRecorder()
	NewAPI(&fakeStore{}, fakeDatabase{}, &fakeArtistSearcher{}, zerolog.Nop()).
		ServeHTTP(response, httptest.NewRequest(
			http.MethodGet, "/api/v1/library/duplicates?offset=-1", nil,
		))
	if response.Code != http.StatusUnprocessableEntity {
		t.Fatalf("status = %d; body = %s", response.Code, response.Body)
	}
}

// The pairs somebody kept on purpose are the other half of the same list, and
// asking for them is how the answer is found again to withdraw it.
func TestRecordingsKeptOnPurposeAreAskedForSeparately(t *testing.T) {
	store := &fakeStore{}
	response := httptest.NewRecorder()
	NewAPI(store, fakeDatabase{}, &fakeArtistSearcher{}, zerolog.Nop()).
		ServeHTTP(response, httptest.NewRequest(
			http.MethodGet, "/api/v1/library/duplicates?kept=true", nil,
		))
	if response.Code != http.StatusOK {
		t.Fatalf("status = %d; body = %s", response.Code, response.Body)
	}
	if !store.heldTwiceKept {
		t.Fatal("the kept half was not asked for")
	}
}

func TestKeepingARecordingHeldTwiceIsRecorded(t *testing.T) {
	recordingID := uuid.New()
	store := &fakeStore{recordingsAnswered: 2}
	response := httptest.NewRecorder()
	NewAPI(store, fakeDatabase{}, &fakeArtistSearcher{}, zerolog.Nop()).
		ServeHTTP(response, httptest.NewRequest(
			http.MethodPost, "/api/v1/library/duplicates/"+recordingID.String()+"/keep", nil,
		))
	if response.Code != http.StatusNoContent {
		t.Fatalf("status = %d; body = %s", response.Code, response.Body)
	}
	if len(store.recordingsKept) != 1 ||
		store.recordingsKept[0] != (keptRecording{recordingID: recordingID, kept: true}) {
		t.Fatalf("kept = %#v", store.recordingsKept)
	}
}

// Somebody who meant to have a recording twice can change their mind, and the
// pair goes back on the list rather than the answer being lost.
func TestKeepingARecordingHeldTwiceIsWithdrawnByTheSameRoute(t *testing.T) {
	recordingID := uuid.New()
	store := &fakeStore{recordingsAnswered: 2}
	response := httptest.NewRecorder()
	NewAPI(store, fakeDatabase{}, &fakeArtistSearcher{}, zerolog.Nop()).
		ServeHTTP(response, httptest.NewRequest(
			http.MethodDelete, "/api/v1/library/duplicates/"+recordingID.String()+"/keep", nil,
		))
	if response.Code != http.StatusNoContent {
		t.Fatalf("status = %d; body = %s", response.Code, response.Body)
	}
	if len(store.recordingsKept) != 1 ||
		store.recordingsKept[0] != (keptRecording{recordingID: recordingID, kept: false}) {
		t.Fatalf("kept = %#v", store.recordingsKept)
	}
}

// An answer about a recording the library does not hold twice is not an answer
// about anything, and saying so is better than a silent success.
func TestKeepingARecordingTheLibraryDoesNotHoldTwiceIsNotFound(t *testing.T) {
	store := &fakeStore{recordingsAnswered: 0}
	response := httptest.NewRecorder()
	NewAPI(store, fakeDatabase{}, &fakeArtistSearcher{}, zerolog.Nop()).
		ServeHTTP(response, httptest.NewRequest(
			http.MethodPost, "/api/v1/library/duplicates/"+uuid.New().String()+"/keep", nil,
		))
	if response.Code != http.StatusNotFound {
		t.Fatalf("status = %d; body = %s", response.Code, response.Body)
	}
}

// Keeping one copy is the answer that deletes music, so the route has to carry
// both halves of the choice through untouched: the recording the group is
// about, and the copy the person picked out of it.
func TestKeepingOneCopyDeletesTheRestOfThatRecording(t *testing.T) {
	recordingID, fileID, goingID := uuid.New(), uuid.New(), uuid.New()
	remover := &fakeRemover{path: "/music/Mezzanine/06 Teardrop.flac"}
	response := httptest.NewRecorder()
	NewAPI(&fakeStore{}, fakeDatabase{}, &fakeArtistSearcher{}, zerolog.Nop(),
		WithFileRemover(remover)).
		ServeHTTP(response, httptest.NewRequest(
			http.MethodPost, "/api/v1/library/duplicates/"+recordingID.String()+"/keep-one",
			strings.NewReader(`{"fileId":"`+fileID.String()+
				`","deleting":["`+goingID.String()+`"]}`),
		))
	if response.Code != http.StatusOK {
		t.Fatalf("status = %d; body = %s", response.Code, response.Body)
	}
	// The copies the screen showed as going reach the remover untouched. That
	// is the whole of the dialog's promise that nothing unread is deleted.
	if len(remover.kept) != 1 || remover.kept[0] != (keptCopy{
		recordingID: recordingID, fileID: fileID, deleting: goingID.String(),
	}) {
		t.Fatalf("kept = %#v", remover.kept)
	}
	var body struct {
		KeptFileID   string   `json:"keptFileId"`
		RemovedPaths []string `json:"removedPaths"`
	}
	if err := json.NewDecoder(response.Body).Decode(&body); err != nil {
		t.Fatal(err)
	}
	if body.KeptFileID != fileID.String() || len(body.RemovedPaths) != 1 {
		t.Fatalf("body = %#v", body)
	}
}

// The copy the person picked is not on disc. Refusing says so and names what to
// do; a press that deleted the rest anyway would leave the recording nowhere.
func TestKeepingACopyThatIsNotOnDiscIsRefusedByTheRoute(t *testing.T) {
	response := httptest.NewRecorder()
	NewAPI(&fakeStore{}, fakeDatabase{}, &fakeArtistSearcher{}, zerolog.Nop(),
		WithFileRemover(&fakeRemover{keptErr: library.ErrKeeperMissing})).
		ServeHTTP(response, httptest.NewRequest(
			http.MethodPost, "/api/v1/library/duplicates/"+uuid.New().String()+"/keep-one",
			strings.NewReader(`{"fileId":"`+uuid.New().String()+`","deleting":[]}`),
		))
	if response.Code != http.StatusConflict {
		t.Fatalf("status = %d; body = %s", response.Code, response.Body)
	}
}

// An installation wired without a remover says so rather than answering as
// though the copies went away.
func TestKeepingOneCopyWithoutARemoverIsUnavailable(t *testing.T) {
	response := httptest.NewRecorder()
	NewAPI(&fakeStore{}, fakeDatabase{}, &fakeArtistSearcher{}, zerolog.Nop()).
		ServeHTTP(response, httptest.NewRequest(
			http.MethodPost, "/api/v1/library/duplicates/"+uuid.New().String()+"/keep-one",
			strings.NewReader(`{"fileId":"`+uuid.New().String()+`","deleting":[]}`),
		))
	if response.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d; body = %s", response.Code, response.Body)
	}
}

// The screen and the library have come apart: the person read one list and this
// would delete a different one. Nothing is deleted and the answer says to read
// the list again.
func TestKeepingOneCopyWhenTheCopiesChangedIsRefused(t *testing.T) {
	response := httptest.NewRecorder()
	NewAPI(&fakeStore{}, fakeDatabase{}, &fakeArtistSearcher{}, zerolog.Nop(),
		WithFileRemover(&fakeRemover{keptErr: library.ErrCopiesChanged})).
		ServeHTTP(response, httptest.NewRequest(
			http.MethodPost, "/api/v1/library/duplicates/"+uuid.New().String()+"/keep-one",
			strings.NewReader(`{"fileId":"`+uuid.New().String()+`","deleting":[]}`),
		))
	if response.Code != http.StatusConflict {
		t.Fatalf("status = %d; body = %s", response.Code, response.Body)
	}
}

// Every other copy is also a copy of other music, so all of them stay. That is
// a state with a reason, not an empty list, and saying so is what stops the
// screen offering a press that can never do anything.
func TestKeepingOneCopyWhenEveryOtherCopyIsOtherMusicSaysSo(t *testing.T) {
	response := httptest.NewRecorder()
	NewAPI(&fakeStore{}, fakeDatabase{}, &fakeArtistSearcher{}, zerolog.Nop(),
		WithFileRemover(&fakeRemover{keptErr: library.ErrOnlyOtherMusicLeft})).
		ServeHTTP(response, httptest.NewRequest(
			http.MethodPost, "/api/v1/library/duplicates/"+uuid.New().String()+"/keep-one",
			strings.NewReader(`{"fileId":"`+uuid.New().String()+`","deleting":[]}`),
		))
	if response.Code != http.StatusConflict {
		t.Fatalf("status = %d; body = %s", response.Code, response.Body)
	}
}

// A file the library let go of and the disc kept is named on the screen, and
// the only thing to do about it is try again. This is that press.
func TestRetryingTheRemovalsTheDiscKept(t *testing.T) {
	remover := &fakeRemover{}
	response := httptest.NewRecorder()
	NewAPI(&fakeStore{}, fakeDatabase{}, &fakeArtistSearcher{}, zerolog.Nop(),
		WithFileRemover(remover)).
		ServeHTTP(response, httptest.NewRequest(
			http.MethodPost, "/api/v1/library/removals/retry", nil,
		))
	if response.Code != http.StatusOK {
		t.Fatalf("status = %d; body = %s", response.Code, response.Body)
	}
	if remover.retried != 1 {
		t.Fatalf("retried = %d, want 1", remover.retried)
	}
}

// Review decides about files that never entered its queue — one picked out of
// the library by hand — and it reads that row by asking the list for one id.
func TestListLibraryFilesNarrowsToOneFileByID(t *testing.T) {
	fileID := uuid.New()
	store := &fakeStore{libraryFiles: []db.LibraryFileRow{{ID: fileID, Path: "/music/song.flac"}}}
	handler := NewAPI(store, fakeDatabase{}, &fakeArtistSearcher{}, zerolog.Nop())

	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(
		http.MethodGet, "/api/v1/library/files?id="+fileID.String(), nil,
	))
	if response.Code != http.StatusOK {
		t.Fatalf("status = %d; body = %s", response.Code, response.Body)
	}
	if store.libraryParams.ID != fileID.String() {
		t.Fatalf("id = %q, want %q", store.libraryParams.ID, fileID)
	}

	// Refused rather than filtered to nothing: an id that is not one is a
	// caller mistake, and answering it with an empty page would read as a file
	// that does not exist.
	invalid := httptest.NewRecorder()
	handler.ServeHTTP(invalid, httptest.NewRequest(
		http.MethodGet, "/api/v1/library/files?id=not-a-uuid", nil,
	))
	if invalid.Code != http.StatusUnprocessableEntity {
		t.Fatalf("invalid id status = %d", invalid.Code)
	}
}

func TestManualReviewEndpoints(t *testing.T) {
	fileID, trackID := uuid.New(), uuid.New()
	resolver := &fakeMatchResolver{items: []matching.Candidate{{
		TrackID: trackID, Title: "Roads", AlbumTitle: "Dummy", ArtistName: "Portishead",
		Method: "catalogue_search",
	}}}
	handler := NewAPI(
		&fakeStore{}, fakeDatabase{}, &fakeArtistSearcher{}, zerolog.Nop(),
		WithMatchResolver(resolver),
	)

	search := httptest.NewRecorder()
	handler.ServeHTTP(search, httptest.NewRequest(
		http.MethodGet,
		"/api/v1/library/files/"+fileID.String()+"/tracks?q=%20Roads%20",
		nil,
	))
	if search.Code != http.StatusOK || resolver.query != "Roads" || resolver.limit != 25 {
		t.Fatalf("search status = %d, query = %q, limit = %d", search.Code, resolver.query, resolver.limit)
	}

	match := httptest.NewRecorder()
	handler.ServeHTTP(match, httptest.NewRequest(
		http.MethodPost,
		"/api/v1/library/files/"+fileID.String()+"/match",
		strings.NewReader(`{"trackId":"`+trackID.String()+`","reassign":true}`),
	))
	if match.Code != http.StatusNoContent || resolver.trackID != trackID || !resolver.reassign {
		t.Fatalf("match status = %d, track = %s, reassign = %v", match.Code, resolver.trackID, resolver.reassign)
	}

	clear := httptest.NewRecorder()
	handler.ServeHTTP(clear, httptest.NewRequest(
		http.MethodDelete, "/api/v1/library/files/"+fileID.String()+"/match", nil,
	))
	if clear.Code != http.StatusNoContent || !resolver.cleared {
		t.Fatalf("clear status = %d, called = %v", clear.Code, resolver.cleared)
	}

	reconcile := httptest.NewRecorder()
	handler.ServeHTTP(reconcile, httptest.NewRequest(
		http.MethodPost, "/api/v1/library/files/"+fileID.String()+"/reconcile", nil,
	))
	if reconcile.Code != http.StatusNoContent || !resolver.reconciled {
		t.Fatalf("reconcile status = %d, called = %v", reconcile.Code, resolver.reconciled)
	}
}

// Following an artist Schall already holds is a promotion, not a conflict: the
// artist is in the catalogue because the library contains their music, and the
// user is now asking for their releases to be kept complete.
func TestFollowHeldArtist(t *testing.T) {
	id := uuid.New()
	now := time.Now().UTC().Truncate(time.Microsecond)
	store := &fakeStore{
		followed: db.FollowArtistByIDRow{
			ID:         id,
			Name:       "Sewerslvt",
			SortName:   "Sewerslvt",
			FollowedAt: pgtype.Timestamptz{Time: now, Valid: true},
		},
	}
	handler := NewAPI(store, fakeDatabase{}, &fakeArtistSearcher{}, zerolog.Nop())
	request := httptest.NewRequest(http.MethodPost, "/api/v1/artists/"+id.String()+"/follow", nil)
	response := httptest.NewRecorder()

	handler.ServeHTTP(response, request)

	if response.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d; body = %s", response.Code, http.StatusOK, response.Body)
	}
	if store.followedID != id {
		t.Errorf("followed = %s, want %s", store.followedID, id)
	}
	var body artistResponse
	if err := json.NewDecoder(response.Body).Decode(&body); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if !body.Followed || body.FollowedAt == nil || !body.FollowedAt.Equal(now) {
		t.Errorf("response = %#v, want a followed artist", body)
	}
}

// An artist who is already followed has nothing to promote, and the caller is
// told that rather than being told the artist does not exist.
func TestFollowAlreadyFollowedArtistConflicts(t *testing.T) {
	id := uuid.New()
	store := &fakeStore{followErr: pgx.ErrNoRows, artist: db.GetArtistRow{ID: id}}
	handler := NewAPI(store, fakeDatabase{}, &fakeArtistSearcher{}, zerolog.Nop())
	request := httptest.NewRequest(http.MethodPost, "/api/v1/artists/"+id.String()+"/follow", nil)
	response := httptest.NewRecorder()

	handler.ServeHTTP(response, request)

	if response.Code != http.StatusConflict {
		t.Fatalf("status = %d, want %d; body = %s", response.Code, http.StatusConflict, response.Body)
	}
}

func TestFollowUnknownArtistIsNotFound(t *testing.T) {
	store := &fakeStore{followErr: pgx.ErrNoRows, getArtistErr: pgx.ErrNoRows}
	handler := NewAPI(store, fakeDatabase{}, &fakeArtistSearcher{}, zerolog.Nop())
	request := httptest.NewRequest(http.MethodPost, "/api/v1/artists/"+uuid.New().String()+"/follow", nil)
	response := httptest.NewRecorder()

	handler.ServeHTTP(response, request)

	if response.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want %d; body = %s", response.Code, http.StatusNotFound, response.Body)
	}
}

// Unfollowing an artist whose music the library holds demotes them. The
// response says so, and says it with the sentence that will explain the artist
// on the page that still lists them.
func TestUnfollowArtistWithOwnedMusicIsHeld(t *testing.T) {
	id := uuid.New()
	store := &fakeStore{
		unfollowed: db.UnfollowArtistRow{
			ID:               id,
			Name:             "Sewerslvt",
			SortName:         "Sewerslvt",
			Held:             pgtype.Bool{Bool: true, Valid: true},
			CatalogueSummary: "Unfollowed on 28 Jul 2026. Kept because your library holds 4 of their tracks.",
			MappedTrackCount: 4,
		},
	}
	handler := NewAPI(store, fakeDatabase{}, &fakeArtistSearcher{}, zerolog.Nop())
	response := httptest.NewRecorder()

	handler.ServeHTTP(response, httptest.NewRequest(
		http.MethodDelete, "/api/v1/artists/"+id.String()+"/follow", nil,
	))

	if response.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d; body = %s", response.Code, http.StatusOK, response.Body)
	}
	if store.unfollowedID != id {
		t.Errorf("unfollowed = %s, want %s", store.unfollowedID, id)
	}
	var body unfollowResponse
	if err := json.NewDecoder(response.Body).Decode(&body); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if body.Outcome != "held" || body.Followed {
		t.Errorf("outcome = %q, followed = %v, want a held artist", body.Outcome, body.Followed)
	}
	if body.CatalogueSummary == nil || body.MappedTrackCount != 4 {
		t.Errorf("response = %#v, want the reason the artist is kept", body)
	}
}

// An artist the library reaches in no way at all is released outright. Nothing
// was owned, so the cascade took nothing, and the response says "released" so
// the caller knows the artist is gone rather than merely demoted.
func TestUnfollowUnreachedArtistIsReleased(t *testing.T) {
	id := uuid.New()
	store := &fakeStore{
		unfollowed: db.UnfollowArtistRow{
			ID:       id,
			Name:     "Sewerslvt",
			SortName: "Sewerslvt",
			Held:     pgtype.Bool{Bool: false, Valid: true},
		},
	}
	handler := NewAPI(store, fakeDatabase{}, &fakeArtistSearcher{}, zerolog.Nop())
	response := httptest.NewRecorder()

	handler.ServeHTTP(response, httptest.NewRequest(
		http.MethodDelete, "/api/v1/artists/"+id.String()+"/follow", nil,
	))

	if response.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d; body = %s", response.Code, http.StatusOK, response.Body)
	}
	var body unfollowResponse
	if err := json.NewDecoder(response.Body).Decode(&body); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if body.Outcome != "released" {
		t.Errorf("outcome = %q, want %q", body.Outcome, "released")
	}
	if body.CatalogueSummary != nil {
		t.Errorf("catalogueSummary = %q, want none for an artist who is gone", *body.CatalogueSummary)
	}
}

// An artist Schall merely holds was never followed, so there is no intent to
// withdraw. Reporting success would claim a change nobody made.
func TestUnfollowHeldArtistConflicts(t *testing.T) {
	id := uuid.New()
	store := &fakeStore{unfollowErr: pgx.ErrNoRows, artist: db.GetArtistRow{ID: id}}
	handler := NewAPI(store, fakeDatabase{}, &fakeArtistSearcher{}, zerolog.Nop())
	response := httptest.NewRecorder()

	handler.ServeHTTP(response, httptest.NewRequest(
		http.MethodDelete, "/api/v1/artists/"+id.String()+"/follow", nil,
	))

	if response.Code != http.StatusConflict {
		t.Fatalf("status = %d, want %d; body = %s", response.Code, http.StatusConflict, response.Body)
	}
}

func TestUnfollowUnknownArtistIsNotFound(t *testing.T) {
	store := &fakeStore{unfollowErr: pgx.ErrNoRows, getArtistErr: pgx.ErrNoRows}
	handler := NewAPI(store, fakeDatabase{}, &fakeArtistSearcher{}, zerolog.Nop())
	response := httptest.NewRecorder()

	handler.ServeHTTP(response, httptest.NewRequest(
		http.MethodDelete, "/api/v1/artists/"+uuid.New().String()+"/follow", nil,
	))

	if response.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want %d; body = %s", response.Code, http.StatusNotFound, response.Body)
	}
}

// Following an artist Schall already holds goes through the same endpoint as
// following a new one, so a search result for a held artist promotes rather
// than reporting a conflict the user cannot act on.
func TestCreateArtistPromotesAHeldArtist(t *testing.T) {
	id, musicBrainzID := uuid.New(), uuid.New()
	now := time.Now().UTC().Truncate(time.Microsecond)
	store := &fakeStore{
		created: db.CreateArtistRow{
			ID:         id,
			Name:       "Sewerslvt",
			SortName:   "Sewerslvt",
			FollowedAt: pgtype.Timestamptz{Time: now, Valid: true},
		},
	}
	handler := NewAPI(store, fakeDatabase{}, &fakeArtistSearcher{}, zerolog.Nop())
	request := httptest.NewRequest(http.MethodPost, "/api/v1/artists", strings.NewReader(
		`{"musicbrainzId":"`+musicBrainzID.String()+`","name":"Sewerslvt","sortName":"Sewerslvt"}`,
	))
	response := httptest.NewRecorder()

	handler.ServeHTTP(response, request)

	if response.Code != http.StatusCreated {
		t.Fatalf("status = %d, want %d; body = %s", response.Code, http.StatusCreated, response.Body)
	}
}

// The query returns no row only when the artist was genuinely already
// followed, and that remains a conflict.
func TestCreateArtistConflictsWhenAlreadyFollowed(t *testing.T) {
	store := &fakeStore{err: pgx.ErrNoRows}
	handler := NewAPI(store, fakeDatabase{}, &fakeArtistSearcher{}, zerolog.Nop())
	request := httptest.NewRequest(http.MethodPost, "/api/v1/artists", strings.NewReader(
		`{"musicbrainzId":"`+uuid.New().String()+`","name":"Portishead","sortName":"Portishead"}`,
	))
	response := httptest.NewRecorder()

	handler.ServeHTTP(response, request)

	if response.Code != http.StatusConflict {
		t.Fatalf("status = %d, want %d; body = %s", response.Code, http.StatusConflict, response.Body)
	}
}

// fakeArchive stands in for the Cover Art Archive at the process boundary.
type fakeArchive struct {
	artwork coverart.Artwork
	err     error
	asked   int
}

func (archive *fakeArchive) Front(
	context.Context, coverart.Release,
) (coverart.Artwork, error) {
	archive.asked++
	return archive.artwork, archive.err
}

// The browser asks Schall and Schall asks the archive once, ever. A page of a
// hundred releases must not become a hundred requests to somebody else.
func TestACachedCoverIsServedWithoutAskingTheArchive(t *testing.T) {
	store := &fakeStore{cachedCover: db.ReleaseCoverArtRow{
		Image: []byte("a picture"), ContentType: "image/jpeg", Source: coverart.Name,
	}}
	archive := &fakeArchive{}
	handler := NewAPI(store, fakeDatabase{}, &fakeArtistSearcher{}, zerolog.Nop(),
		WithCoverArt(archive))

	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(
		http.MethodGet, "/api/v1/albums/"+uuid.New().String()+"/cover", nil))

	if response.Code != http.StatusOK || response.Body.String() != "a picture" {
		t.Fatalf("status = %d body = %q, want the cached picture", response.Code, response.Body.String())
	}
	if archive.asked != 0 {
		t.Errorf("the archive was asked %d times about a release already pictured", archive.asked)
	}
}

// A release nobody has pictured is remembered as having none, so the archive is
// asked once rather than on every page view forever.
func TestAReleaseWithNoCoverIsRememberedAsHavingNone(t *testing.T) {
	store := &fakeStore{cachedCoverErr: pgx.ErrNoRows}
	archive := &fakeArchive{err: coverart.ErrNoArtwork}
	handler := NewAPI(store, fakeDatabase{}, &fakeArtistSearcher{}, zerolog.Nop(),
		WithCoverArt(archive))

	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(
		http.MethodGet, "/api/v1/albums/"+uuid.New().String()+"/cover", nil))

	if response.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want no cover reported as such", response.Code)
	}
	if len(store.savedCovers) != 1 || store.savedCovers[0].Image != nil {
		t.Fatalf("saved = %+v, want the absence recorded once", store.savedCovers)
	}
	// Under the archive's name it would read as "the archive gave us this", and
	// a release with a picture nothing can show is never looked for again.
	if store.savedCovers[0].Source != coverart.NameNobody {
		t.Fatalf("source = %q, want the absence recorded as one", store.savedCovers[0].Source)
	}
}

// An archive nobody could reach is not a release without a cover. Remembering it
// as one would make an outage permanent.
func TestAnArchiveThatFailedIsNotRememberedAsNoCover(t *testing.T) {
	store := &fakeStore{cachedCoverErr: pgx.ErrNoRows}
	archive := &fakeArchive{err: errors.New("connection refused")}
	handler := NewAPI(store, fakeDatabase{}, &fakeArtistSearcher{}, zerolog.Nop(),
		WithCoverArt(archive))

	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(
		http.MethodGet, "/api/v1/albums/"+uuid.New().String()+"/cover", nil))

	if response.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want no picture served", response.Code)
	}
	if len(store.savedCovers) != 0 {
		t.Fatalf("saved = %+v, want an outage not remembered as an absence", store.savedCovers)
	}
}

// The monitor level is a closed set: what is not in it is a sentence back to
// the caller, not a constraint violation out of the database.
func TestSettingTheMonitorLevelRefusesAnUnknownLevel(t *testing.T) {
	store := &fakeStore{}
	handler := NewAPI(store, fakeDatabase{}, &fakeArtistSearcher{}, zerolog.Nop())

	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodPut,
		"/api/v1/artists/"+uuid.New().String()+"/monitor",
		strings.NewReader(`{"level":"everything-but-live"}`)))

	if response.Code != http.StatusUnprocessableEntity {
		t.Fatalf("status = %d; body = %s", response.Code, response.Body)
	}
	if store.monitorLevel != "" {
		t.Fatalf("monitorLevel = %q, want nothing recorded", store.monitorLevel)
	}
}

// The monitor level is read through the same guard as every other body, so a
// field nobody defined is a refusal rather than a silent no-op. Spelling
// `level` wrong is the mistake this catches, and accepting it would record
// nothing while answering that the level had been set.
func TestSettingTheMonitorLevelRefusesAFieldNobodyDefined(t *testing.T) {
	store := &fakeStore{}
	handler := NewAPI(store, fakeDatabase{}, &fakeArtistSearcher{}, zerolog.Nop())

	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodPut,
		"/api/v1/artists/"+uuid.New().String()+"/monitor",
		strings.NewReader(`{"monitorLevel":"everything"}`)))

	if response.Code != http.StatusBadRequest {
		t.Fatalf("status = %d; body = %s", response.Code, response.Body)
	}
	if store.monitorLevel != "" {
		t.Fatalf("monitorLevel = %q, want nothing recorded", store.monitorLevel)
	}
}

// Setting the level records it and answers with what was set.
func TestSettingTheMonitorLevelRecordsIt(t *testing.T) {
	store := &fakeStore{}
	handler := NewAPI(store, fakeDatabase{}, &fakeArtistSearcher{}, zerolog.Nop())

	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodPut,
		"/api/v1/artists/"+uuid.New().String()+"/monitor",
		strings.NewReader(`{"level":"main"}`)))

	if response.Code != http.StatusOK {
		t.Fatalf("status = %d; body = %s", response.Code, response.Body)
	}
	if store.monitorLevel != "main" {
		t.Fatalf("monitorLevel = %q, want main", store.monitorLevel)
	}
}

// catalogueOnly is a Store and nothing else. The API discovers the library, the
// cover cache and the settings by asserting the store supports them, so a store
// that supports none of it is how the "unavailable" answers are reached.
type catalogueOnly struct{}

func (catalogueOnly) ListArtists(context.Context, db.ListArtistsParams) ([]db.ListArtistsRow, error) {
	return nil, nil
}

func (catalogueOnly) ArtistListTotals(context.Context, db.ArtistListTotalsParams) (db.ArtistListTotalsRow, error) {
	return db.ArtistListTotalsRow{}, nil
}

func (catalogueOnly) CatalogueGenres(context.Context) ([]string, error) {
	return nil, nil
}

func (catalogueOnly) GetArtist(context.Context, uuid.UUID) (db.GetArtistRow, error) {
	return db.GetArtistRow{}, pgx.ErrNoRows
}

func (catalogueOnly) ArtistDiscography(context.Context, uuid.UUID) (db.ArtistDiscography, error) {
	return db.ArtistDiscography{}, nil
}

func (catalogueOnly) ArtistMusicBrainzIDsByName(
	context.Context, []string,
) (map[string]uuid.UUID, error) {
	return nil, nil
}

func (catalogueOnly) CreateArtist(context.Context, db.CreateArtistParams) (db.CreateArtistRow, error) {
	return db.CreateArtistRow{}, nil
}

func (catalogueOnly) FollowArtistByID(context.Context, uuid.UUID) (db.FollowArtistByIDRow, error) {
	return db.FollowArtistByIDRow{}, pgx.ErrNoRows
}

func (catalogueOnly) UnfollowArtist(context.Context, uuid.UUID) (db.UnfollowArtistRow, error) {
	return db.UnfollowArtistRow{}, pgx.ErrNoRows
}

func (catalogueOnly) QueueArtistRefresh(context.Context, uuid.UUID) (db.QueueArtistRefreshRow, error) {
	return db.QueueArtistRefreshRow{}, pgx.ErrNoRows
}

func (catalogueOnly) SetArtistMonitorLevel(
	context.Context, db.SetArtistMonitorLevelParams,
) (db.SetArtistMonitorLevelRow, error) {
	return db.SetArtistMonitorLevelRow{}, pgx.ErrNoRows
}

func (catalogueOnly) SetArtistWantMissing(
	context.Context, db.SetArtistWantMissingParams,
) (db.SetArtistWantMissingRow, error) {
	return db.SetArtistWantMissingRow{}, pgx.ErrNoRows
}

func (catalogueOnly) ListAlbums(context.Context, db.ListAlbumsParams) (db.AlbumPage, error) {
	return db.AlbumPage{}, nil
}

func (catalogueOnly) GetAlbum(context.Context, uuid.UUID) (db.GetAlbumRow, error) {
	return db.GetAlbumRow{}, pgx.ErrNoRows
}

func (catalogueOnly) ListAlbumTracks(context.Context, uuid.UUID) ([]db.ListAlbumTracksRow, error) {
	return nil, nil
}

func (catalogueOnly) GetTrackForWanting(context.Context, uuid.UUID) (db.GetTrackForWantingRow, error) {
	return db.GetTrackForWantingRow{}, pgx.ErrNoRows
}

func (catalogueOnly) ArtistTracksForWanting(context.Context, uuid.UUID) ([]db.ArtistTracksForWantingRow, error) {
	return nil, nil
}

func (catalogueOnly) DashboardSummary(context.Context) (db.DashboardSummaryRow, error) {
	return db.DashboardSummaryRow{}, nil
}

func (catalogueOnly) QueueAlbumRefresh(context.Context, uuid.UUID) (bool, bool, error) {
	return false, false, nil
}

// The dashboard is one query and its numbers are what the front page is.
func TestTheDashboardCountsTheCollection(t *testing.T) {
	store := &fakeStore{artists: []db.ListArtistsRow{{}, {}}}
	handler := NewAPI(store, fakeDatabase{}, &fakeArtistSearcher{}, zerolog.Nop())

	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/api/v1/dashboard", nil))
	if response.Code != http.StatusOK {
		t.Fatalf("status = %d; body = %s", response.Code, response.Body)
	}
	var payload map[string]int64
	if err := json.Unmarshal(response.Body.Bytes(), &payload); err != nil {
		t.Fatal(err)
	}
	if payload["artistCount"] != 2 {
		t.Fatalf("payload = %v", payload)
	}
}

// The dashboard is the one place this is visible without pressing Test in
// Settings → Sources (#495), so it has to carry the moment acquisition
// recorded, not just that the source is signed out.
func TestTheDashboardReportsWhenTheSourceSignedOut(t *testing.T) {
	since := time.Date(2026, 8, 29, 10, 0, 0, 0, time.UTC)
	store := &fakeSettingsStore{
		fakeStore:  &fakeStore{},
		configured: true,
		settings: db.SlskdSettingsRow{
			Enabled:        true,
			LoggedOutSince: pgtype.Timestamptz{Time: since, Valid: true},
		},
	}
	handler := NewAPI(store, fakeDatabase{}, &fakeArtistSearcher{}, zerolog.Nop())

	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/api/v1/dashboard", nil))
	if response.Code != http.StatusOK {
		t.Fatalf("status = %d; body = %s", response.Code, response.Body)
	}
	var payload dashboardResponse
	if err := json.Unmarshal(response.Body.Bytes(), &payload); err != nil {
		t.Fatal(err)
	}
	if payload.SourceLoggedOutSince == nil || !payload.SourceLoggedOutSince.Equal(since) {
		t.Fatalf("sourceLoggedOutSince = %v, want %v", payload.SourceLoggedOutSince, since)
	}
}

// searching is the second way the source stops looking without anything
// appearing broken. It is not a stored fact — the pause is one process's
// decision about what to ask next — so the dashboard reads it off the gate.
type searchingPaused struct {
	until time.Time
	on    bool
}

func (paused searchingPaused) SilencedUntil() (time.Time, bool) {
	return paused.until, paused.on
}

// The Overview already says when the source is signed out. A source Soulseek
// has stopped answering looks identical from every other screen — slskd is
// connected, the queue is healthy, and nothing is downloaded for half an hour.
func TestTheDashboardReportsWhenSearchingIsPaused(t *testing.T) {
	until := time.Date(2026, 9, 4, 10, 30, 0, 0, time.UTC)
	handler := NewAPI(&fakeStore{}, fakeDatabase{}, &fakeArtistSearcher{}, zerolog.Nop(),
		WithSearchPause(searchingPaused{until: until, on: true}))

	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/api/v1/dashboard", nil))
	if response.Code != http.StatusOK {
		t.Fatalf("status = %d; body = %s", response.Code, response.Body)
	}
	var payload dashboardResponse
	if err := json.Unmarshal(response.Body.Bytes(), &payload); err != nil {
		t.Fatal(err)
	}
	if payload.SourceSilencedUntil == nil || !payload.SourceSilencedUntil.Equal(until) {
		t.Fatalf("sourceSilencedUntil = %v, want %v", payload.SourceSilencedUntil, until)
	}
}

// A source that is searching says nothing about a pause, and an installation
// with no gate to ask reports what it reported before the pause existed.
func TestTheDashboardReportsNoPauseWhileSearchingGoesAhead(t *testing.T) {
	for name, options := range map[string][]Option{
		"searching":      {WithSearchPause(searchingPaused{})},
		"nothing to ask": nil,
	} {
		t.Run(name, func(t *testing.T) {
			handler := NewAPI(&fakeStore{}, fakeDatabase{}, &fakeArtistSearcher{},
				zerolog.Nop(), options...)

			response := httptest.NewRecorder()
			handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/api/v1/dashboard", nil))
			if response.Code != http.StatusOK {
				t.Fatalf("status = %d; body = %s", response.Code, response.Body)
			}
			var payload dashboardResponse
			if err := json.Unmarshal(response.Body.Bytes(), &payload); err != nil {
				t.Fatal(err)
			}
			if payload.SourceSilencedUntil != nil {
				t.Fatalf("sourceSilencedUntil = %v, want nil", payload.SourceSilencedUntil)
			}
		})
	}
}

// A clock an old outage left running is never going to be cleared once the
// source it was recorded against is switched off — nothing will ever search
// through it again. Reporting it anyway would tell somebody a source they
// turned off themselves is still not logged in.
func TestTheDashboardIgnoresTheClockOnADisabledSource(t *testing.T) {
	since := time.Date(2026, 8, 29, 10, 0, 0, 0, time.UTC)
	store := &fakeSettingsStore{
		fakeStore:  &fakeStore{},
		configured: true,
		settings: db.SlskdSettingsRow{
			Enabled:        false,
			LoggedOutSince: pgtype.Timestamptz{Time: since, Valid: true},
		},
	}
	handler := NewAPI(store, fakeDatabase{}, &fakeArtistSearcher{}, zerolog.Nop())

	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/api/v1/dashboard", nil))
	if response.Code != http.StatusOK {
		t.Fatalf("status = %d; body = %s", response.Code, response.Body)
	}
	var payload dashboardResponse
	if err := json.Unmarshal(response.Body.Bytes(), &payload); err != nil {
		t.Fatal(err)
	}
	if payload.SourceLoggedOutSince != nil {
		t.Fatalf("sourceLoggedOutSince = %v, want nil for a disabled source", payload.SourceLoggedOutSince)
	}
}

// A source that has never been marked signed out, or that has been cleared
// since, must not read as one that is — the false alarm the "one failed poll"
// constraint on this feature exists to rule out.
func TestTheDashboardSaysNothingWhenTheSourceHasNotSignedOut(t *testing.T) {
	store := &fakeSettingsStore{fakeStore: &fakeStore{}, configured: true}
	handler := NewAPI(store, fakeDatabase{}, &fakeArtistSearcher{}, zerolog.Nop())

	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/api/v1/dashboard", nil))
	if response.Code != http.StatusOK {
		t.Fatalf("status = %d; body = %s", response.Code, response.Body)
	}
	var payload dashboardResponse
	if err := json.Unmarshal(response.Body.Bytes(), &payload); err != nil {
		t.Fatal(err)
	}
	if payload.SourceLoggedOutSince != nil {
		t.Fatalf("sourceLoggedOutSince = %v, want nil", payload.SourceLoggedOutSince)
	}
}

func TestTheDashboardReportsAStoreThatFailed(t *testing.T) {
	handler := NewAPI(
		&fakeStore{err: errors.New("the database is unreachable")},
		fakeDatabase{}, &fakeArtistSearcher{}, zerolog.Nop())

	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/api/v1/dashboard", nil))
	if response.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500", response.Code)
	}
}

func TestMusicFoldersAreListedWithWhenTheyWereLastScanned(t *testing.T) {
	scanned := time.Date(2026, 8, 1, 9, 0, 0, 0, time.UTC)
	store := &fakeStore{libraryRoots: []db.LibraryRootRow{{
		ID: uuid.New(), Path: "/music", Enabled: true,
		LastScannedAt: pgtype.Timestamptz{Time: scanned, Valid: true},
	}}}
	handler := NewAPI(store, fakeDatabase{}, &fakeArtistSearcher{}, zerolog.Nop())

	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/api/v1/library/roots", nil))
	if response.Code != http.StatusOK {
		t.Fatalf("status = %d; body = %s", response.Code, response.Body)
	}
	var payload struct {
		Items []libraryRootResponse `json:"items"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &payload); err != nil {
		t.Fatal(err)
	}
	if len(payload.Items) != 1 || payload.Items[0].LastScannedAt == nil {
		t.Fatalf("items = %+v", payload.Items)
	}
}

func TestListingMusicFoldersReportsAStoreThatFailed(t *testing.T) {
	handler := NewAPI(
		&fakeStore{err: errors.New("the database is unreachable")},
		fakeDatabase{}, &fakeArtistSearcher{}, zerolog.Nop())

	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/api/v1/library/roots", nil))
	if response.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500", response.Code)
	}
}

// Nothing validates a path in a build with no path validator wired in, and
// accepting one unchecked is how a music folder becomes any folder.
func TestAddingAMusicFolderIsUnavailableWithoutAPathValidator(t *testing.T) {
	handler := NewAPI(&fakeStore{}, fakeDatabase{}, &fakeArtistSearcher{}, zerolog.Nop())

	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodPost, "/api/v1/library/roots",
		strings.NewReader(`{"path":"/music"}`)))
	if response.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503", response.Code)
	}
}

func TestAddingAMusicFolderRejectsABodyThatIsNotJSON(t *testing.T) {
	handler := NewAPI(&fakeStore{}, fakeDatabase{}, &fakeArtistSearcher{}, zerolog.Nop(),
		WithLibraryPaths(fakeLibraryPaths{path: "/music"}))

	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodPost, "/api/v1/library/roots",
		strings.NewReader(`not json`)))
	if response.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", response.Code)
	}
}

func TestAddingAMusicFolderTwiceConflicts(t *testing.T) {
	store := &fakeStore{err: &pgconn.PgError{Code: "23505"}}
	handler := NewAPI(store, fakeDatabase{}, &fakeArtistSearcher{}, zerolog.Nop(),
		WithLibraryPaths(fakeLibraryPaths{path: "/music"}))

	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodPost, "/api/v1/library/roots",
		strings.NewReader(`{"path":"/music"}`)))
	if response.Code != http.StatusConflict {
		t.Fatalf("status = %d, want 409", response.Code)
	}
}

func TestAddingAMusicFolderReportsAStoreThatFailed(t *testing.T) {
	store := &fakeStore{err: errors.New("the database is unreachable")}
	handler := NewAPI(store, fakeDatabase{}, &fakeArtistSearcher{}, zerolog.Nop(),
		WithLibraryPaths(fakeLibraryPaths{path: "/music"}))

	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodPost, "/api/v1/library/roots",
		strings.NewReader(`{"path":"/music"}`)))
	if response.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500", response.Code)
	}
}

func TestTheLibrarySummaryReportsAStoreThatFailed(t *testing.T) {
	handler := NewAPI(
		&fakeStore{err: errors.New("the database is unreachable")},
		fakeDatabase{}, &fakeArtistSearcher{}, zerolog.Nop())

	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/api/v1/library", nil))
	if response.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500", response.Code)
	}
}

// Every filter the library list takes is a closed set or a bounded number. What
// is outside it is refused rather than clamped, because a page that silently
// answered a different question would be read as the answer to this one.
func TestListingLibraryFilesRefusesFiltersOutsideWhatItAnswers(t *testing.T) {
	handler := NewAPI(&fakeStore{}, fakeDatabase{}, &fakeArtistSearcher{}, zerolog.Nop())
	for _, query := range []string{
		"?limit=0", "?limit=501", "?limit=many",
		"?offset=-1", "?offset=soon",
		"?status=probably", "?resolution=probably",
	} {
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, httptest.NewRequest(
			http.MethodGet, "/api/v1/library/files"+query, nil))
		if response.Code != http.StatusUnprocessableEntity {
			t.Errorf("%s: status = %d, want 422", query, response.Code)
		}
	}
}

func TestListingLibraryFilesReportsAStoreThatFailed(t *testing.T) {
	handler := NewAPI(
		&fakeStore{err: errors.New("the database is unreachable")},
		fakeDatabase{}, &fakeArtistSearcher{}, zerolog.Nop())

	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/api/v1/library/files", nil))
	if response.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500", response.Code)
	}
}

// A file the library holds is described by everything the scan and the resolver
// know about it, because that is what the review page is deciding from.
func TestALibraryFileIsListedWithItsTagsAndItsIdentity(t *testing.T) {
	fileID, trackID, recordingID := uuid.New(), uuid.New(), uuid.New()
	store := &fakeStore{libraryFiles: []db.LibraryFileRow{{
		ID: fileID, Path: "/music/roads.flac", SizeBytes: 1024,
		ArtistTag:              pgtype.Text{String: "Portishead", Valid: true},
		DiscNumber:             pgtype.Int4{Int32: 1, Valid: true},
		DurationMS:             pgtype.Int4{Int32: 305000, Valid: true},
		MusicBrainzRecordingID: uuid.NullUUID{UUID: recordingID, Valid: true},
		MatchStatus:            "matched",
		MappingMethod:          pgtype.Text{String: "recording_id", Valid: true},
		MappingConfidence:      pgtype.Float4{Float32: 1, Valid: true},
		MappedTrackID:          uuid.NullUUID{UUID: trackID, Valid: true},
		ResolutionStatus:       "resolved",
	}}}
	handler := NewAPI(store, fakeDatabase{}, &fakeArtistSearcher{}, zerolog.Nop())

	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/api/v1/library/files", nil))
	if response.Code != http.StatusOK {
		t.Fatalf("status = %d; body = %s", response.Code, response.Body)
	}
	var payload struct {
		Items []libraryFileResponse `json:"items"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &payload); err != nil {
		t.Fatal(err)
	}
	if len(payload.Items) != 1 {
		t.Fatalf("items = %+v", payload.Items)
	}
	file := payload.Items[0]
	if file.MappedTrackID == nil || *file.MappedTrackID != trackID {
		t.Errorf("mapped track = %v, want %s", file.MappedTrackID, trackID)
	}
	if file.MappingConfidence == nil || *file.MappingConfidence != 1 {
		t.Errorf("confidence = %v, want the recorded one", file.MappingConfidence)
	}
	if file.MusicBrainzRecordingID == nil || *file.MusicBrainzRecordingID != recordingID {
		t.Errorf("recording = %v, want the tagged one", file.MusicBrainzRecordingID)
	}
}

// Without a library store the routes that read the disc say so, rather than
// answering with an empty library that reads as one nobody has any music in.
func TestTheLibraryRoutesReportBeingUnavailable(t *testing.T) {
	handler := NewAPI(catalogueOnly{}, fakeDatabase{}, &fakeArtistSearcher{}, zerolog.Nop())
	for _, request := range []*http.Request{
		httptest.NewRequest(http.MethodGet, "/api/v1/library", nil),
		httptest.NewRequest(http.MethodGet, "/api/v1/library/files", nil),
		httptest.NewRequest(http.MethodGet, "/api/v1/library/roots", nil),
		httptest.NewRequest(http.MethodPost, "/api/v1/library/roots", strings.NewReader(`{}`)),
		httptest.NewRequest(http.MethodPost, "/api/v1/library/scan", nil),
		httptest.NewRequest(http.MethodPost, "/api/v1/library/files/"+uuid.NewString()+"/resolve", nil),
	} {
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, request)
		if response.Code != http.StatusServiceUnavailable {
			t.Errorf("%s %s: status = %d, want 503", request.Method, request.URL.Path, response.Code)
		}
	}
}

func TestQueueingAScanReportsAStoreThatFailed(t *testing.T) {
	handler := NewAPI(
		&fakeStore{err: errors.New("the database is unreachable")},
		fakeDatabase{}, &fakeArtistSearcher{}, zerolog.Nop())

	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodPost, "/api/v1/library/scan", nil))
	if response.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500", response.Code)
	}
}

// A file that went away between the list and the press has no resolution to
// queue.
func TestResolvingAFileThatIsNoLongerThereIsNotFound(t *testing.T) {
	handler := NewAPI(&fakeStore{err: pgx.ErrNoRows}, fakeDatabase{}, &fakeArtistSearcher{}, zerolog.Nop())

	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodPost,
		"/api/v1/library/files/"+uuid.NewString()+"/resolve", nil))
	if response.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", response.Code)
	}
}

func TestResolvingAFileReportsAStoreThatFailed(t *testing.T) {
	handler := NewAPI(
		&fakeStore{err: errors.New("the database is unreachable")},
		fakeDatabase{}, &fakeArtistSearcher{}, zerolog.Nop())

	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodPost,
		"/api/v1/library/files/"+uuid.NewString()+"/resolve", nil))
	if response.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500", response.Code)
	}
}

// The candidates are what the review page offers, each with the evidence that
// put it there.
func TestMatchCandidatesAreListedWithTheirEvidence(t *testing.T) {
	trackID := uuid.New()
	resolver := &fakeMatchResolver{items: []matching.Candidate{{
		TrackID: trackID, Title: "Roads", AlbumTitle: "Dummy", ArtistName: "Portishead",
		DiscNumber: 1, Method: "recording_id", Confidence: 1,
	}}}
	handler := NewAPI(&fakeStore{}, fakeDatabase{}, &fakeArtistSearcher{}, zerolog.Nop(),
		WithMatchResolver(resolver))

	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet,
		"/api/v1/library/files/"+uuid.NewString()+"/matches", nil))
	if response.Code != http.StatusOK {
		t.Fatalf("status = %d; body = %s", response.Code, response.Body)
	}
	var payload struct {
		Items []matchCandidateResponse `json:"items"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &payload); err != nil {
		t.Fatal(err)
	}
	if len(payload.Items) != 1 || payload.Items[0].TrackID != trackID {
		t.Fatalf("items = %+v", payload.Items)
	}
}

func TestListingMatchCandidatesReportsAResolverThatFailed(t *testing.T) {
	handler := NewAPI(&fakeStore{}, fakeDatabase{}, &fakeArtistSearcher{}, zerolog.Nop(),
		WithMatchResolver(&fakeMatchResolver{err: errors.New("the database is unreachable")}))

	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet,
		"/api/v1/library/files/"+uuid.NewString()+"/matches", nil))
	if response.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500", response.Code)
	}
}

// One character matches most of a catalogue, which is not a search anybody
// meant to run.
func TestSearchingTracksNeedsTwoCharacters(t *testing.T) {
	handler := NewAPI(&fakeStore{}, fakeDatabase{}, &fakeArtistSearcher{}, zerolog.Nop(),
		WithMatchResolver(&fakeMatchResolver{}))

	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet,
		"/api/v1/library/files/"+uuid.NewString()+"/tracks?q=r", nil))
	if response.Code != http.StatusUnprocessableEntity {
		t.Fatalf("status = %d, want 422", response.Code)
	}
}

func TestSearchingTracksReturnsWhatTheResolverFound(t *testing.T) {
	trackID := uuid.New()
	resolver := &fakeMatchResolver{items: []matching.Candidate{{TrackID: trackID, Title: "Roads"}}}
	handler := NewAPI(&fakeStore{}, fakeDatabase{}, &fakeArtistSearcher{}, zerolog.Nop(),
		WithMatchResolver(resolver))

	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet,
		"/api/v1/library/files/"+uuid.NewString()+"/tracks?q=roads", nil))
	if response.Code != http.StatusOK {
		t.Fatalf("status = %d; body = %s", response.Code, response.Body)
	}
	if resolver.query != "roads" {
		t.Fatalf("query = %q, want the one that was typed", resolver.query)
	}
}

func TestSearchingTracksForAFileThatIsNotThereIsNotFound(t *testing.T) {
	handler := NewAPI(&fakeStore{}, fakeDatabase{}, &fakeArtistSearcher{}, zerolog.Nop(),
		WithMatchResolver(&fakeMatchResolver{err: pgx.ErrNoRows}))

	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet,
		"/api/v1/library/files/"+uuid.NewString()+"/tracks?q=roads", nil))
	if response.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", response.Code)
	}
}

func TestSearchingTracksReportsAResolverThatFailed(t *testing.T) {
	handler := NewAPI(&fakeStore{}, fakeDatabase{}, &fakeArtistSearcher{}, zerolog.Nop(),
		WithMatchResolver(&fakeMatchResolver{err: errors.New("the database is unreachable")}))

	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet,
		"/api/v1/library/files/"+uuid.NewString()+"/tracks?q=roads", nil))
	if response.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500", response.Code)
	}
}

func TestAManualMatchNamesTheTrackItIsTo(t *testing.T) {
	handler := NewAPI(&fakeStore{}, fakeDatabase{}, &fakeArtistSearcher{}, zerolog.Nop(),
		WithMatchResolver(&fakeMatchResolver{}))

	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodPost,
		"/api/v1/library/files/"+uuid.NewString()+"/match", strings.NewReader(`{}`)))
	if response.Code != http.StatusUnprocessableEntity {
		t.Fatalf("status = %d, want 422", response.Code)
	}
}

func TestAManualMatchRejectsABodyThatIsNotJSON(t *testing.T) {
	handler := NewAPI(&fakeStore{}, fakeDatabase{}, &fakeArtistSearcher{}, zerolog.Nop(),
		WithMatchResolver(&fakeMatchResolver{}))

	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodPost,
		"/api/v1/library/files/"+uuid.NewString()+"/match", strings.NewReader(`not json`)))
	if response.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", response.Code)
	}
}

func TestAManualMatchToATrackThatIsNotThereIsNotFound(t *testing.T) {
	handler := NewAPI(&fakeStore{}, fakeDatabase{}, &fakeArtistSearcher{}, zerolog.Nop(),
		WithMatchResolver(&fakeMatchResolver{err: pgx.ErrNoRows}))

	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodPost,
		"/api/v1/library/files/"+uuid.NewString()+"/match",
		strings.NewReader(`{"trackId":"`+uuid.NewString()+`"}`)))
	if response.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", response.Code)
	}
}

// A manual decision is permanent, so a second one over the top of it is refused
// and named rather than quietly winning.
func TestAManualMatchOverAnotherOneConflicts(t *testing.T) {
	handler := NewAPI(&fakeStore{}, fakeDatabase{}, &fakeArtistSearcher{}, zerolog.Nop(),
		WithMatchResolver(&fakeMatchResolver{err: matching.ErrManualConflict}))

	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodPost,
		"/api/v1/library/files/"+uuid.NewString()+"/match",
		strings.NewReader(`{"trackId":"`+uuid.NewString()+`"}`)))
	if response.Code != http.StatusConflict {
		t.Fatalf("status = %d, want 409", response.Code)
	}
	if !strings.Contains(response.Body.String(), matching.ErrManualConflict.Error()) {
		t.Fatalf("body = %s, want the conflict named", response.Body)
	}
}

func TestSettingAManualMatchReportsAResolverThatFailed(t *testing.T) {
	handler := NewAPI(&fakeStore{}, fakeDatabase{}, &fakeArtistSearcher{}, zerolog.Nop(),
		WithMatchResolver(&fakeMatchResolver{err: errors.New("the database is unreachable")}))

	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodPost,
		"/api/v1/library/files/"+uuid.NewString()+"/match",
		strings.NewReader(`{"trackId":"`+uuid.NewString()+`"}`)))
	if response.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500", response.Code)
	}
}

func TestClearingAManualMatchThatWasNeverMadeIsNotFound(t *testing.T) {
	handler := NewAPI(&fakeStore{}, fakeDatabase{}, &fakeArtistSearcher{}, zerolog.Nop(),
		WithMatchResolver(&fakeMatchResolver{err: pgx.ErrNoRows}))

	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodDelete,
		"/api/v1/library/files/"+uuid.NewString()+"/match", nil))
	if response.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", response.Code)
	}
}

func TestClearingAManualMatchReportsAResolverThatFailed(t *testing.T) {
	handler := NewAPI(&fakeStore{}, fakeDatabase{}, &fakeArtistSearcher{}, zerolog.Nop(),
		WithMatchResolver(&fakeMatchResolver{err: errors.New("the database is unreachable")}))

	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodDelete,
		"/api/v1/library/files/"+uuid.NewString()+"/match", nil))
	if response.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500", response.Code)
	}
}

func TestReconcilingAFileReportsAResolverThatFailed(t *testing.T) {
	handler := NewAPI(&fakeStore{}, fakeDatabase{}, &fakeArtistSearcher{}, zerolog.Nop(),
		WithMatchResolver(&fakeMatchResolver{err: errors.New("the database is unreachable")}))

	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodPost,
		"/api/v1/library/files/"+uuid.NewString()+"/reconcile", nil))
	if response.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500", response.Code)
	}
}

// Without a matcher there is nothing that could hold a match for a file, so the
// routes answer as they would for a file that does not exist.
func TestTheMatchRoutesAreNotFoundWithoutAMatcher(t *testing.T) {
	handler := NewAPI(&fakeStore{}, fakeDatabase{}, &fakeArtistSearcher{}, zerolog.Nop())
	fileID := uuid.NewString()
	for _, request := range []*http.Request{
		httptest.NewRequest(http.MethodGet, "/api/v1/library/files/"+fileID+"/matches", nil),
		httptest.NewRequest(http.MethodGet, "/api/v1/library/files/"+fileID+"/tracks?q=roads", nil),
		httptest.NewRequest(http.MethodPost, "/api/v1/library/files/"+fileID+"/match", strings.NewReader(`{}`)),
		httptest.NewRequest(http.MethodDelete, "/api/v1/library/files/"+fileID+"/match", nil),
		httptest.NewRequest(http.MethodPost, "/api/v1/library/files/"+fileID+"/reconcile", nil),
	} {
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, request)
		if response.Code != http.StatusNotFound {
			t.Errorf("%s %s: status = %d, want 404", request.Method, request.URL.Path, response.Code)
		}
	}
}

// An id that is not a UUID names no file, and the routes say so rather than
// looking one up.
func TestAMalformedFileIDNamesNoLibraryFile(t *testing.T) {
	handler := NewAPI(&fakeStore{}, fakeDatabase{}, &fakeArtistSearcher{}, zerolog.Nop(),
		WithMatchResolver(&fakeMatchResolver{}), WithIdentityResolver(&fakeIdentityResolver{}))
	for _, request := range []*http.Request{
		httptest.NewRequest(http.MethodGet, "/api/v1/library/files/not-a-uuid/matches", nil),
		httptest.NewRequest(http.MethodGet, "/api/v1/library/files/not-a-uuid/tracks?q=roads", nil),
		httptest.NewRequest(http.MethodPost, "/api/v1/library/files/not-a-uuid/match", strings.NewReader(`{}`)),
		httptest.NewRequest(http.MethodDelete, "/api/v1/library/files/not-a-uuid/match", nil),
		httptest.NewRequest(http.MethodPost, "/api/v1/library/files/not-a-uuid/reconcile", nil),
		httptest.NewRequest(http.MethodPost, "/api/v1/library/files/not-a-uuid/resolve", nil),
		httptest.NewRequest(http.MethodGet, "/api/v1/library/files/not-a-uuid/identity", nil),
		httptest.NewRequest(http.MethodDelete, "/api/v1/library/files/not-a-uuid/identity", nil),
	} {
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, request)
		if response.Code != http.StatusNotFound {
			t.Errorf("%s %s: status = %d, want 404", request.Method, request.URL.Path, response.Code)
		}
	}
}

// The same for an artist: an id that is not a UUID is answered without asking
// the store about it.
func TestAMalformedArtistIDNamesNoArtist(t *testing.T) {
	handler := NewAPI(&fakeStore{}, fakeDatabase{}, &fakeArtistSearcher{}, zerolog.Nop())
	for _, request := range []*http.Request{
		httptest.NewRequest(http.MethodGet, "/api/v1/artists/not-a-uuid", nil),
		httptest.NewRequest(http.MethodPost, "/api/v1/artists/not-a-uuid/follow", nil),
		httptest.NewRequest(http.MethodDelete, "/api/v1/artists/not-a-uuid/follow", nil),
		httptest.NewRequest(http.MethodPost, "/api/v1/artists/not-a-uuid/refresh", nil),
		httptest.NewRequest(http.MethodPut, "/api/v1/artists/not-a-uuid/monitor", strings.NewReader(`{}`)),
	} {
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, request)
		if response.Code != http.StatusNotFound {
			t.Errorf("%s %s: status = %d, want 404", request.Method, request.URL.Path, response.Code)
		}
	}
}

// And for a release.
func TestAMalformedReleaseIDNamesNoRelease(t *testing.T) {
	handler := NewAPI(&fakeStore{}, fakeDatabase{}, &fakeArtistSearcher{}, zerolog.Nop())
	for _, request := range []*http.Request{
		httptest.NewRequest(http.MethodGet, "/api/v1/albums/not-a-uuid", nil),
		httptest.NewRequest(http.MethodGet, "/api/v1/albums/not-a-uuid/tracks", nil),
		httptest.NewRequest(http.MethodGet, "/api/v1/albums/not-a-uuid/editions", nil),
		httptest.NewRequest(http.MethodPost, "/api/v1/albums/not-a-uuid/edition", strings.NewReader(`{}`)),
		httptest.NewRequest(http.MethodGet, "/api/v1/albums/not-a-uuid/cover", nil),
	} {
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, request)
		if response.Code != http.StatusNotFound {
			t.Errorf("%s %s: status = %d, want 404", request.Method, request.URL.Path, response.Code)
		}
	}
}

func TestAMonitorLevelNeedsAJSONBody(t *testing.T) {
	handler := NewAPI(&fakeStore{}, fakeDatabase{}, &fakeArtistSearcher{}, zerolog.Nop())

	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodPut,
		"/api/v1/artists/"+uuid.NewString()+"/monitor", strings.NewReader(`not json`)))
	if response.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", response.Code)
	}
}

func TestSettingTheMonitorLevelOnAnArtistThatIsNotThereIsNotFound(t *testing.T) {
	handler := NewAPI(&fakeStore{err: pgx.ErrNoRows}, fakeDatabase{}, &fakeArtistSearcher{}, zerolog.Nop())

	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodPut,
		"/api/v1/artists/"+uuid.NewString()+"/monitor", strings.NewReader(`{"level":"main"}`)))
	if response.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", response.Code)
	}
}

func TestSettingTheMonitorLevelReportsAStoreThatFailed(t *testing.T) {
	handler := NewAPI(
		&fakeStore{err: errors.New("the database is unreachable")},
		fakeDatabase{}, &fakeArtistSearcher{}, zerolog.Nop())

	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodPut,
		"/api/v1/artists/"+uuid.NewString()+"/monitor", strings.NewReader(`{"level":"main"}`)))
	if response.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500", response.Code)
	}
}

func TestRefreshingAnArtistThatIsNotThereIsNotFound(t *testing.T) {
	handler := NewAPI(&fakeStore{err: pgx.ErrNoRows}, fakeDatabase{}, &fakeArtistSearcher{}, zerolog.Nop())

	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodPost,
		"/api/v1/artists/"+uuid.NewString()+"/refresh", nil))
	if response.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", response.Code)
	}
}

func TestRefreshingAnArtistReportsAStoreThatFailed(t *testing.T) {
	handler := NewAPI(
		&fakeStore{err: errors.New("the database is unreachable")},
		fakeDatabase{}, &fakeArtistSearcher{}, zerolog.Nop())

	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodPost,
		"/api/v1/artists/"+uuid.NewString()+"/refresh", nil))
	if response.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500", response.Code)
	}
}

func TestReadingAnArtistReportsAStoreThatFailed(t *testing.T) {
	handler := NewAPI(
		&fakeStore{getArtistErr: errors.New("the database is unreachable")},
		fakeDatabase{}, &fakeArtistSearcher{}, zerolog.Nop())

	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet,
		"/api/v1/artists/"+uuid.NewString(), nil))
	if response.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500", response.Code)
	}
}

func TestListingArtistsReportsAStoreThatFailed(t *testing.T) {
	handler := NewAPI(
		&fakeStore{err: errors.New("the database is unreachable")},
		fakeDatabase{}, &fakeArtistSearcher{}, zerolog.Nop())

	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/api/v1/artists", nil))
	if response.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500", response.Code)
	}
}

func TestFollowingAnArtistReportsAStoreThatFailed(t *testing.T) {
	handler := NewAPI(
		&fakeStore{followErr: errors.New("the database is unreachable")},
		fakeDatabase{}, &fakeArtistSearcher{}, zerolog.Nop())

	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodPost,
		"/api/v1/artists/"+uuid.NewString()+"/follow", nil))
	if response.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500", response.Code)
	}
}

func TestUnfollowingAnArtistReportsAStoreThatFailed(t *testing.T) {
	handler := NewAPI(
		&fakeStore{unfollowErr: errors.New("the database is unreachable")},
		fakeDatabase{}, &fakeArtistSearcher{}, zerolog.Nop())

	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodDelete,
		"/api/v1/artists/"+uuid.NewString()+"/follow", nil))
	if response.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500", response.Code)
	}
}

func TestCreatingAnArtistReportsAStoreThatFailed(t *testing.T) {
	handler := NewAPI(
		&fakeStore{err: errors.New("the database is unreachable")},
		fakeDatabase{}, &fakeArtistSearcher{}, zerolog.Nop())

	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodPost, "/api/v1/artists",
		strings.NewReader(`{"musicbrainzId":"`+uuid.NewString()+
			`","name":"Portishead","sortName":"Portishead"}`)))
	if response.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500", response.Code)
	}
}

func TestReadingAReleaseThatIsNotThereIsNotFound(t *testing.T) {
	handler := NewAPI(&fakeStore{err: pgx.ErrNoRows}, fakeDatabase{}, &fakeArtistSearcher{}, zerolog.Nop())

	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet,
		"/api/v1/albums/"+uuid.NewString(), nil))
	if response.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", response.Code)
	}
}

func TestReadingAReleaseReportsAStoreThatFailed(t *testing.T) {
	handler := NewAPI(
		&fakeStore{err: errors.New("the database is unreachable")},
		fakeDatabase{}, &fakeArtistSearcher{}, zerolog.Nop())

	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet,
		"/api/v1/albums/"+uuid.NewString(), nil))
	if response.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500", response.Code)
	}
}

// A release group with no exact first-release date still shows the date of the
// edition that represents it.
func TestAReleaseWithoutAFirstReleaseDateFallsBackToItsEdition(t *testing.T) {
	released := time.Date(1994, 8, 22, 0, 0, 0, 0, time.UTC)
	store := &fakeStore{album: db.GetAlbumRow{
		Title: "Dummy", ReleaseDate: pgtype.Date{Time: released, Valid: true},
	}}
	handler := NewAPI(store, fakeDatabase{}, &fakeArtistSearcher{}, zerolog.Nop())

	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet,
		"/api/v1/albums/"+uuid.NewString(), nil))
	if response.Code != http.StatusOK {
		t.Fatalf("status = %d; body = %s", response.Code, response.Body)
	}
	var payload albumDetailResponse
	if err := json.Unmarshal(response.Body.Bytes(), &payload); err != nil {
		t.Fatal(err)
	}
	if payload.FirstReleaseDate != "1994-08-22" {
		t.Fatalf("first release date = %q", payload.FirstReleaseDate)
	}
}

func TestListingReleaseTracksReportsAStoreThatFailed(t *testing.T) {
	handler := NewAPI(
		&fakeStore{err: errors.New("the database is unreachable")},
		fakeDatabase{}, &fakeArtistSearcher{}, zerolog.Nop())

	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet,
		"/api/v1/albums/"+uuid.NewString()+"/tracks", nil))
	if response.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500", response.Code)
	}
}

func TestListingReleasesReportsAStoreThatFailed(t *testing.T) {
	handler := NewAPI(
		&fakeStore{err: errors.New("the database is unreachable")},
		fakeDatabase{}, &fakeArtistSearcher{}, zerolog.Nop())

	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/api/v1/albums", nil))
	if response.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500", response.Code)
	}
}

// The editions are what a person chooses between, so each one is named with
// what distinguishes it.
func TestTheEditionsOfAReleaseAreListed(t *testing.T) {
	editionID := uuid.New()
	store := &fakeEditionStore{fakeStore: &fakeStore{album: db.GetAlbumRow{
		MusicbrainzReleaseGroupID: uuid.NullUUID{UUID: uuid.New(), Valid: true},
	}}}
	searcher := &fakeEditionSearcher{
		fakeArtistSearcher: &fakeArtistSearcher{},
		editions: []musicbrainz.ReleaseEdition{{
			ID: editionID, Title: "Dummy", Status: "Official", Country: "GB", Date: "1994-08-22",
		}},
	}
	handler := NewAPI(store, fakeDatabase{}, searcher, zerolog.Nop())

	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet,
		"/api/v1/albums/"+uuid.NewString()+"/editions", nil))
	if response.Code != http.StatusOK {
		t.Fatalf("status = %d; body = %s", response.Code, response.Body)
	}
	var payload struct {
		Items []editionResponse `json:"items"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &payload); err != nil {
		t.Fatal(err)
	}
	if len(payload.Items) != 1 || payload.Items[0].MusicBrainzReleaseID != editionID ||
		payload.Items[0].Country != "GB" {
		t.Fatalf("items = %+v", payload.Items)
	}
}

// A release group is what editions belong to, so a release Schall holds without
// one has none to list.
func TestAReleaseWithNoReleaseGroupHasNoEditions(t *testing.T) {
	store := &fakeEditionStore{fakeStore: &fakeStore{}}
	searcher := &fakeEditionSearcher{fakeArtistSearcher: &fakeArtistSearcher{}}
	handler := NewAPI(store, fakeDatabase{}, searcher, zerolog.Nop())

	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet,
		"/api/v1/albums/"+uuid.NewString()+"/editions", nil))
	if response.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", response.Code)
	}
}

func TestListingEditionsIsUnavailableWithoutALookup(t *testing.T) {
	handler := NewAPI(&fakeStore{}, fakeDatabase{}, &fakeArtistSearcher{}, zerolog.Nop())

	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet,
		"/api/v1/albums/"+uuid.NewString()+"/editions", nil))
	if response.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503", response.Code)
	}
}

func TestListingEditionsReportsAProviderThatFailed(t *testing.T) {
	store := &fakeEditionStore{fakeStore: &fakeStore{album: db.GetAlbumRow{
		MusicbrainzReleaseGroupID: uuid.NullUUID{UUID: uuid.New(), Valid: true},
	}}}
	searcher := &fakeEditionSearcher{
		fakeArtistSearcher: &fakeArtistSearcher{err: errors.New("musicbrainz did not respond")},
	}
	handler := NewAPI(store, fakeDatabase{}, searcher, zerolog.Nop())

	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet,
		"/api/v1/albums/"+uuid.NewString()+"/editions", nil))
	if response.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500", response.Code)
	}
}

func TestListingEditionsReportsAStoreThatFailed(t *testing.T) {
	store := &fakeEditionStore{fakeStore: &fakeStore{
		album: db.GetAlbumRow{
			MusicbrainzReleaseGroupID: uuid.NullUUID{UUID: uuid.New(), Valid: true},
		},
		err: errors.New("the database is unreachable"),
	}}
	searcher := &fakeEditionSearcher{fakeArtistSearcher: &fakeArtistSearcher{}}
	handler := NewAPI(store, fakeDatabase{}, searcher, zerolog.Nop())

	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet,
		"/api/v1/albums/"+uuid.NewString()+"/editions", nil))
	if response.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500", response.Code)
	}
}

func TestChoosingAnEditionNeedsOneNamed(t *testing.T) {
	store := &fakeEditionStore{fakeStore: &fakeStore{}}
	searcher := &fakeEditionSearcher{fakeArtistSearcher: &fakeArtistSearcher{}}
	handler := NewAPI(store, fakeDatabase{}, searcher, zerolog.Nop())

	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodPost,
		"/api/v1/albums/"+uuid.NewString()+"/edition", strings.NewReader(`{}`)))
	if response.Code != http.StatusUnprocessableEntity {
		t.Fatalf("status = %d, want 422", response.Code)
	}
}

func TestChoosingAnEditionRejectsABodyThatIsNotJSON(t *testing.T) {
	store := &fakeEditionStore{fakeStore: &fakeStore{}}
	searcher := &fakeEditionSearcher{fakeArtistSearcher: &fakeArtistSearcher{}}
	handler := NewAPI(store, fakeDatabase{}, searcher, zerolog.Nop())

	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodPost,
		"/api/v1/albums/"+uuid.NewString()+"/edition", strings.NewReader(`not json`)))
	if response.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", response.Code)
	}
}

// An edition that is not one of this release group's is a different release,
// and choosing it would file another album's tracks under this one.
func TestChoosingAnEditionOfAnotherReleaseGroupIsRefused(t *testing.T) {
	store := &fakeEditionStore{fakeStore: &fakeStore{album: db.GetAlbumRow{
		MusicbrainzReleaseGroupID: uuid.NullUUID{UUID: uuid.New(), Valid: true},
	}}}
	searcher := &fakeEditionSearcher{
		fakeArtistSearcher: &fakeArtistSearcher{},
		editions:           []musicbrainz.ReleaseEdition{{ID: uuid.New(), Title: "Dummy"}},
	}
	handler := NewAPI(store, fakeDatabase{}, searcher, zerolog.Nop())

	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodPost,
		"/api/v1/albums/"+uuid.NewString()+"/edition",
		strings.NewReader(`{"musicbrainzReleaseId":"`+uuid.NewString()+`"}`)))
	if response.Code != http.StatusUnprocessableEntity {
		t.Fatalf("status = %d, want 422", response.Code)
	}
	if store.chosen != uuid.Nil {
		t.Fatalf("chosen = %s, want nothing recorded", store.chosen)
	}
}

func TestChoosingAnEditionIsUnavailableWithoutAnEditionStore(t *testing.T) {
	handler := NewAPI(&fakeStore{}, fakeDatabase{}, &fakeArtistSearcher{}, zerolog.Nop())

	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodPost,
		"/api/v1/albums/"+uuid.NewString()+"/edition",
		strings.NewReader(`{"musicbrainzReleaseId":"`+uuid.NewString()+`"}`)))
	if response.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503", response.Code)
	}
}

func TestChoosingAnEditionIsUnavailableWithoutALookup(t *testing.T) {
	store := &fakeEditionStore{fakeStore: &fakeStore{}}
	handler := NewAPI(store, fakeDatabase{}, &fakeArtistSearcher{}, zerolog.Nop())

	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodPost,
		"/api/v1/albums/"+uuid.NewString()+"/edition",
		strings.NewReader(`{"musicbrainzReleaseId":"`+uuid.NewString()+`"}`)))
	if response.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503", response.Code)
	}
}

func TestChoosingAnEditionForAReleaseWithNoReleaseGroupIsNotFound(t *testing.T) {
	store := &fakeEditionStore{fakeStore: &fakeStore{}}
	searcher := &fakeEditionSearcher{fakeArtistSearcher: &fakeArtistSearcher{}}
	handler := NewAPI(store, fakeDatabase{}, searcher, zerolog.Nop())

	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodPost,
		"/api/v1/albums/"+uuid.NewString()+"/edition",
		strings.NewReader(`{"musicbrainzReleaseId":"`+uuid.NewString()+`"}`)))
	if response.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", response.Code)
	}
}

func TestChoosingAnEditionReportsAProviderThatFailed(t *testing.T) {
	store := &fakeEditionStore{fakeStore: &fakeStore{album: db.GetAlbumRow{
		MusicbrainzReleaseGroupID: uuid.NullUUID{UUID: uuid.New(), Valid: true},
	}}}
	searcher := &fakeEditionSearcher{
		fakeArtistSearcher: &fakeArtistSearcher{err: errors.New("musicbrainz did not respond")},
	}
	handler := NewAPI(store, fakeDatabase{}, searcher, zerolog.Nop())

	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodPost,
		"/api/v1/albums/"+uuid.NewString()+"/edition",
		strings.NewReader(`{"musicbrainzReleaseId":"`+uuid.NewString()+`"}`)))
	if response.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500", response.Code)
	}
}

func TestChoosingAnEditionReportsAStoreThatFailed(t *testing.T) {
	editionID := uuid.New()
	store := &fakeEditionStore{fakeStore: &fakeStore{
		album: db.GetAlbumRow{
			MusicbrainzReleaseGroupID: uuid.NullUUID{UUID: uuid.New(), Valid: true},
		},
	}}
	searcher := &fakeEditionSearcher{
		fakeArtistSearcher: &fakeArtistSearcher{},
		editions:           []musicbrainz.ReleaseEdition{{ID: editionID, Title: "Dummy"}},
	}
	handler := NewAPI(store, fakeDatabase{}, searcher, zerolog.Nop())
	store.fakeStore.err = errors.New("the database is unreachable")

	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodPost,
		"/api/v1/albums/"+uuid.NewString()+"/edition",
		strings.NewReader(`{"musicbrainzReleaseId":"`+editionID.String()+`"}`)))
	if response.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500", response.Code)
	}
}

// An artist is pictured by the sweep and served from the cache. Nothing here
// ever fetches, so an artist nobody has pictured yet simply has no picture.
func TestAnArtistPictureIsServedFromTheCache(t *testing.T) {
	store := &fakeStore{cachedArtistImage: db.ArtistImageRow{
		Image: []byte("a picture"), ContentType: "image/jpeg",
	}}
	handler := NewAPI(store, fakeDatabase{}, &fakeArtistSearcher{}, zerolog.Nop())

	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet,
		"/api/v1/artists/"+uuid.NewString()+"/image", nil))
	if response.Code != http.StatusOK || response.Body.String() != "a picture" {
		t.Fatalf("status = %d body = %q", response.Code, response.Body)
	}
	if got := response.Header().Get("Content-Type"); got != "image/jpeg" {
		t.Errorf("content type = %q", got)
	}
}

func TestAnArtistNobodyHasPicturedHasNoPicture(t *testing.T) {
	handler := NewAPI(
		&fakeStore{cachedArtistImageErr: pgx.ErrNoRows},
		fakeDatabase{}, &fakeArtistSearcher{}, zerolog.Nop())

	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet,
		"/api/v1/artists/"+uuid.NewString()+"/image", nil))
	if response.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", response.Code)
	}
}

func TestAnArtistPictureReportsAStoreThatFailed(t *testing.T) {
	handler := NewAPI(
		&fakeStore{cachedArtistImageErr: errors.New("the database is unreachable")},
		fakeDatabase{}, &fakeArtistSearcher{}, zerolog.Nop())

	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet,
		"/api/v1/artists/"+uuid.NewString()+"/image", nil))
	if response.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500", response.Code)
	}
}

// A cached row holding no bytes is an absence that was remembered, and it reads
// the same way as one nobody has looked for.
func TestARememberedAbsenceIsServedAsNoPicture(t *testing.T) {
	handler := NewAPI(
		&fakeStore{cachedArtistImage: db.ArtistImageRow{ContentType: "image/jpeg"}},
		fakeDatabase{}, &fakeArtistSearcher{}, zerolog.Nop())

	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet,
		"/api/v1/artists/"+uuid.NewString()+"/image", nil))
	if response.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", response.Code)
	}
}

// A list of releases asks for what is already cached and sets no archive
// working: a page of a hundred covers must stay a hundred requests to this
// installation and none to anybody else.
func TestAskingOnlyForACachedCoverNeverReachesTheArchive(t *testing.T) {
	archive := &fakeArchive{artwork: coverart.Artwork{Image: []byte("a picture")}}
	handler := NewAPI(
		&fakeStore{cachedCoverErr: pgx.ErrNoRows}, fakeDatabase{}, &fakeArtistSearcher{},
		zerolog.Nop(), WithCoverArt(archive))

	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet,
		"/api/v1/albums/"+uuid.NewString()+"/cover?cached=1", nil))
	if response.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", response.Code)
	}
	if archive.asked != 0 {
		t.Fatalf("the archive was asked %d times for a cached-only request", archive.asked)
	}
}

func TestAReleaseHasNoCoverWithoutACoverCache(t *testing.T) {
	handler := NewAPI(catalogueOnly{}, fakeDatabase{}, &fakeArtistSearcher{}, zerolog.Nop())

	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet,
		"/api/v1/albums/"+uuid.NewString()+"/cover", nil))
	if response.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", response.Code)
	}
}

func TestReadingACachedCoverReportsAStoreThatFailed(t *testing.T) {
	handler := NewAPI(
		&fakeStore{cachedCoverErr: errors.New("the database is unreachable")},
		fakeDatabase{}, &fakeArtistSearcher{}, zerolog.Nop())

	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet,
		"/api/v1/albums/"+uuid.NewString()+"/cover", nil))
	if response.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500", response.Code)
	}
}

// The archive is asked about a release the catalogue resolved, so a release
// that is not there is not a release to picture.
func TestFetchingACoverForAReleaseThatIsNotThereIsNotFound(t *testing.T) {
	handler := NewAPI(
		&fakeStore{cachedCoverErr: pgx.ErrNoRows, err: pgx.ErrNoRows},
		fakeDatabase{}, &fakeArtistSearcher{}, zerolog.Nop(), WithCoverArt(&fakeArchive{}))

	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet,
		"/api/v1/albums/"+uuid.NewString()+"/cover", nil))
	if response.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", response.Code)
	}
}

func TestFetchingACoverReportsAStoreThatFailed(t *testing.T) {
	handler := NewAPI(
		&fakeStore{cachedCoverErr: pgx.ErrNoRows, err: errors.New("the database is unreachable")},
		fakeDatabase{}, &fakeArtistSearcher{}, zerolog.Nop(), WithCoverArt(&fakeArchive{}))

	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet,
		"/api/v1/albums/"+uuid.NewString()+"/cover", nil))
	if response.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500", response.Code)
	}
}

// MusicBrainz asking for less is not a search that failed, and the interface
// has to be able to say "try again" rather than "something broke".
func TestSearchingArtistsReportsAProviderAskingForLess(t *testing.T) {
	handler := NewAPI(&fakeStore{}, fakeDatabase{},
		&fakeArtistSearcher{err: &musicbrainz.HTTPError{StatusCode: http.StatusServiceUnavailable}},
		zerolog.Nop())

	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet,
		"/api/v1/search/artists?q=portishead", nil))
	if response.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503", response.Code)
	}
}

func TestSearchingArtistsReportsAProviderThatFailed(t *testing.T) {
	handler := NewAPI(&fakeStore{}, fakeDatabase{},
		&fakeArtistSearcher{err: errors.New("connection refused")}, zerolog.Nop())

	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet,
		"/api/v1/search/artists?q=portishead", nil))
	if response.Code != http.StatusBadGateway {
		t.Fatalf("status = %d, want 502", response.Code)
	}
}

// Nobody is left to read the answer once the client has gone, and writing one
// would only be logged as a failed write.
func TestACancelledArtistSearchIsNotAnswered(t *testing.T) {
	handler := NewAPI(&fakeStore{}, fakeDatabase{},
		&fakeArtistSearcher{err: context.Canceled}, zerolog.Nop())

	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet,
		"/api/v1/search/artists?q=portishead", nil))
	if response.Body.Len() != 0 {
		t.Fatalf("body = %s, want nothing written for a client that went away", response.Body)
	}
}

// A search long enough to be a paste is refused rather than sent on.
func TestSearchingArtistsRefusesAnAbsurdlyLongQuery(t *testing.T) {
	handler := NewAPI(&fakeStore{}, fakeDatabase{}, &fakeArtistSearcher{}, zerolog.Nop())

	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet,
		"/api/v1/search/artists?q="+strings.Repeat("a", 201), nil))
	if response.Code != http.StatusUnprocessableEntity {
		t.Fatalf("status = %d, want 422", response.Code)
	}
}

// A body carrying two JSON objects is two requests in a trench coat, and which
// of them was meant is not for the server to decide.
func TestARequestBodyMustCarryOneObject(t *testing.T) {
	handler := NewAPI(&fakeStore{}, fakeDatabase{}, &fakeArtistSearcher{}, zerolog.Nop())

	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodPost, "/api/v1/artists",
		strings.NewReader(`{"name":"Portishead"}{"name":"Massive Attack"}`)))
	if response.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", response.Code)
	}
}

// Health is a database Schall can reach, and nothing else: it is what a
// container orchestrator restarts on.
func TestHealthIsTheDatabaseBeingReachable(t *testing.T) {
	handler := NewAPI(&fakeStore{}, fakeDatabase{}, &fakeArtistSearcher{}, zerolog.Nop())

	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/api/v1/health", nil))
	if response.Code != http.StatusOK {
		t.Fatalf("status = %d; body = %s", response.Code, response.Body)
	}
	if !strings.Contains(response.Body.String(), `"ok"`) {
		t.Fatalf("body = %s", response.Body)
	}
}

func TestListingArtistsRefusesAnOutOfRangeOffset(t *testing.T) {
	handler := NewAPI(&fakeStore{}, fakeDatabase{}, &fakeArtistSearcher{}, zerolog.Nop())
	for _, query := range []string{"?offset=-1", "?offset=soon"} {
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/api/v1/artists"+query, nil))
		if response.Code != http.StatusUnprocessableEntity {
			t.Errorf("%s: status = %d, want 422", query, response.Code)
		}
	}
}

func TestListingArtistsReportsAPageThatCouldNotBeRead(t *testing.T) {
	handler := NewAPI(
		&fakeStore{artistsErr: errors.New("the database is unreachable")},
		fakeDatabase{}, &fakeArtistSearcher{}, zerolog.Nop())

	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/api/v1/artists", nil))
	if response.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500", response.Code)
	}
}

// The total counts what the page is a page of, which is the scope narrowed by
// completeness — not the whole catalogue behind it.
func TestNarrowingToIncompleteArtistsReportsThatHalfsTotal(t *testing.T) {
	store := &fakeStore{artistTotals: db.ArtistListTotalsRow{
		AllCount: 40, IncompleteCount: 7, CompleteCount: 33,
	}}
	handler := NewAPI(store, fakeDatabase{}, &fakeArtistSearcher{}, zerolog.Nop())

	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet,
		"/api/v1/artists?completeness=incomplete", nil))
	if response.Code != http.StatusOK {
		t.Fatalf("status = %d; body = %s", response.Code, response.Body)
	}
	var payload struct {
		Total int64 `json:"total"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &payload); err != nil {
		t.Fatal(err)
	}
	if payload.Total != 7 {
		t.Fatalf("total = %d, want the incomplete half", payload.Total)
	}
}

// A release group with no exact first-release date shows the date of the
// edition representing it, in the browser as well as on its own page.
func TestABrowsedReleaseWithoutAFirstReleaseDateFallsBackToItsEdition(t *testing.T) {
	released := time.Date(1994, 8, 22, 0, 0, 0, 0, time.UTC)
	store := &fakeStore{albums: db.AlbumPage{Items: []db.AlbumRow{{
		ID: uuid.New(), Title: "Dummy", ReleaseDate: pgtype.Date{Time: released, Valid: true},
	}}}}
	handler := NewAPI(store, fakeDatabase{}, &fakeArtistSearcher{}, zerolog.Nop())

	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/api/v1/albums", nil))
	if response.Code != http.StatusOK {
		t.Fatalf("status = %d; body = %s", response.Code, response.Body)
	}
	var payload struct {
		Items []albumResponse `json:"items"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &payload); err != nil {
		t.Fatal(err)
	}
	if len(payload.Items) != 1 || payload.Items[0].FirstReleaseDate != "1994-08-22" {
		t.Fatalf("items = %+v", payload.Items)
	}
}

// The release went away between the list and the press, so there is nothing to
// record a choice against.
func TestChoosingAnEditionForAReleaseThatWentAwayIsNotFound(t *testing.T) {
	editionID := uuid.New()
	store := &fakeEditionStore{
		fakeStore: &fakeStore{album: db.GetAlbumRow{
			MusicbrainzReleaseGroupID: uuid.NullUUID{UUID: uuid.New(), Valid: true},
		}},
		selectErr: pgx.ErrNoRows,
	}
	searcher := &fakeEditionSearcher{
		fakeArtistSearcher: &fakeArtistSearcher{},
		editions:           []musicbrainz.ReleaseEdition{{ID: editionID, Title: "Dummy"}},
	}
	handler := NewAPI(store, fakeDatabase{}, searcher, zerolog.Nop())

	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodPost,
		"/api/v1/albums/"+uuid.NewString()+"/edition",
		strings.NewReader(`{"musicbrainzReleaseId":"`+editionID.String()+`"}`)))
	if response.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", response.Code)
	}
}

func TestRecordingAChosenEditionReportsAStoreThatFailed(t *testing.T) {
	editionID := uuid.New()
	store := &fakeEditionStore{
		fakeStore: &fakeStore{album: db.GetAlbumRow{
			MusicbrainzReleaseGroupID: uuid.NullUUID{UUID: uuid.New(), Valid: true},
		}},
		selectErr: errors.New("the database is unreachable"),
	}
	searcher := &fakeEditionSearcher{
		fakeArtistSearcher: &fakeArtistSearcher{},
		editions:           []musicbrainz.ReleaseEdition{{ID: editionID, Title: "Dummy"}},
	}
	handler := NewAPI(store, fakeDatabase{}, searcher, zerolog.Nop())

	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodPost,
		"/api/v1/albums/"+uuid.NewString()+"/edition",
		strings.NewReader(`{"musicbrainzReleaseId":"`+editionID.String()+`"}`)))
	if response.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500", response.Code)
	}
}

// The picture is fetched and servable; only remembering it failed, and the
// browser that asked still gets its cover.
func TestACoverThatCouldNotBeCachedIsStillServed(t *testing.T) {
	store := &fakeStore{
		cachedCoverErr: pgx.ErrNoRows,
		saveCoverErr:   errors.New("the database is unreachable"),
	}
	archive := &fakeArchive{artwork: coverart.Artwork{
		Image: []byte("a picture"), ContentType: "image/jpeg",
	}}
	handler := NewAPI(store, fakeDatabase{}, &fakeArtistSearcher{}, zerolog.Nop(),
		WithCoverArt(archive))

	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet,
		"/api/v1/albums/"+uuid.NewString()+"/cover", nil))
	if response.Code != http.StatusOK || response.Body.String() != "a picture" {
		t.Fatalf("status = %d body = %q", response.Code, response.Body)
	}
}

func TestAMalformedArtistIDHasNoPicture(t *testing.T) {
	handler := NewAPI(&fakeStore{}, fakeDatabase{}, &fakeArtistSearcher{}, zerolog.Nop())

	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet,
		"/api/v1/artists/not-a-uuid/image", nil))
	if response.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", response.Code)
	}
}

// An artist has to be named, and named within what the column holds. A name
// nobody could search for is not an artist Schall can follow.
func TestCreatingAnArtistRefusesANameItCannotHold(t *testing.T) {
	handler := NewAPI(&fakeStore{}, fakeDatabase{}, &fakeArtistSearcher{}, zerolog.Nop())
	musicBrainzID := uuid.NewString()
	for name, body := range map[string]string{
		"no name":        `{"musicbrainzId":"` + musicBrainzID + `","name":"  "}`,
		"long name":      `{"musicbrainzId":"` + musicBrainzID + `","name":"` + strings.Repeat("n", 301) + `"}`,
		"no sort name":   `{"musicbrainzId":"` + musicBrainzID + `","name":"Portishead"}`,
		"long sort name": `{"musicbrainzId":"` + musicBrainzID + `","name":"Portishead","sortName":"` + strings.Repeat("s", 301) + `"}`,
	} {
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, httptest.NewRequest(http.MethodPost, "/api/v1/artists",
			strings.NewReader(body)))
		if response.Code != http.StatusUnprocessableEntity {
			t.Errorf("%s: status = %d, want 422; body = %s", name, response.Code, response.Body)
		}
	}
}

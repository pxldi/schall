package coverart

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/pxldi/schall/internal/db"
	"github.com/pxldi/schall/internal/musicbrainz"
	"github.com/rs/zerolog"
)

// fakeCoverStore holds the releases waiting to be pictured and what was written
// back about them.
type fakeCoverStore struct {
	waiting        []db.ReleasesMissingCoverArtRow
	saved          []db.SaveReleaseCoverArtParams
	artistsWaiting []db.ArtistsMissingImageRow
	savedArtists   []db.SaveArtistImageParams
	// askedArtists is how the artists waiting were asked for, which is where the
	// day the chain last changed reaches the database.
	askedArtists []db.ArtistsMissingImageParams

	biographiesWaiting []db.ArtistsMissingBiographyRow
	savedBiographies   []db.SaveArtistBiographyParams

	listReleasesErr    error
	listArtistsErr     error
	saveReleaseErr     error
	saveArtistErr      error
	listBiographiesErr error
	saveBiographyErr   error

	// onSave runs once a row has been recorded, which is the one moment a test
	// knows the work for that row is finished.
	onSave func()
}

func (store *fakeCoverStore) ReleasesMissingCoverArt(
	context.Context, int32,
) ([]db.ReleasesMissingCoverArtRow, error) {
	return store.waiting, store.listReleasesErr
}

func (store *fakeCoverStore) ArtistsMissingImage(
	_ context.Context, params db.ArtistsMissingImageParams,
) ([]db.ArtistsMissingImageRow, error) {
	store.askedArtists = append(store.askedArtists, params)
	return store.artistsWaiting, store.listArtistsErr
}

func (store *fakeCoverStore) SaveArtistImage(
	_ context.Context, params db.SaveArtistImageParams,
) error {
	store.savedArtists = append(store.savedArtists, params)
	if store.onSave != nil {
		store.onSave()
	}
	return store.saveArtistErr
}

func (store *fakeCoverStore) ArtistsMissingBiography(
	_ context.Context, _ int32,
) ([]db.ArtistsMissingBiographyRow, error) {
	return store.biographiesWaiting, store.listBiographiesErr
}

func (store *fakeCoverStore) SaveArtistBiography(
	_ context.Context, params db.SaveArtistBiographyParams,
) error {
	store.savedBiographies = append(store.savedBiographies, params)
	return store.saveBiographyErr
}

func (store *fakeCoverStore) SaveReleaseCoverArt(
	_ context.Context, params db.SaveReleaseCoverArtParams,
) error {
	store.saved = append(store.saved, params)
	if store.onSave != nil {
		store.onSave()
	}
	return store.saveReleaseErr
}

func sweeperFor(t *testing.T, store *fakeCoverStore, handler http.HandlerFunc) *Sweeper {
	t.Helper()
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)
	sweeper := NewSweeper(store, Sources{Archive: NewClient(Options{BaseURL: server.URL})}, zerolog.Nop())
	sweeper.pause = 0
	return sweeper
}

func waiting(count int) []db.ReleasesMissingCoverArtRow {
	rows := make([]db.ReleasesMissingCoverArtRow, 0, count)
	for range count {
		rows = append(rows, db.ReleasesMissingCoverArtRow{
			ID:                   uuid.New(),
			Title:                "Dummy",
			ArtistName:           "Portishead",
			MusicbrainzReleaseID: uuid.NullUUID{UUID: uuid.New(), Valid: true},
		})
	}
	return rows
}

func TestASweepPicturesTheReleasesNobodyHasLookedFor(t *testing.T) {
	store := &fakeCoverStore{waiting: waiting(3)}
	sweeper := sweeperFor(t, store, func(response http.ResponseWriter, _ *http.Request) {
		response.Header().Set("Content-Type", "image/jpeg")
		_, _ = response.Write(aPicture(1))
	})

	if _, err := sweeper.Sweep(context.Background()); err != nil {
		t.Fatalf("Sweep() error = %v", err)
	}
	if len(store.saved) != 3 {
		t.Fatalf("saved %d, want every waiting release pictured", len(store.saved))
	}
	for _, saved := range store.saved {
		if len(saved.Image) == 0 {
			t.Fatalf("saved = %+v, want the picture kept", saved)
		}
	}
}

// A release no archive has a picture of is remembered as having none, which is
// what stops it being asked about on every sweep forever.
func TestAReleaseNoArchiveHasPicturedIsRememberedAsHavingNone(t *testing.T) {
	store := &fakeCoverStore{waiting: waiting(1)}
	sweeper := sweeperFor(t, store, func(response http.ResponseWriter, _ *http.Request) {
		response.WriteHeader(http.StatusNotFound)
	})

	if _, err := sweeper.Sweep(context.Background()); err != nil {
		t.Fatalf("Sweep() error = %v", err)
	}
	if len(store.saved) != 1 || store.saved[0].Image != nil {
		t.Fatalf("saved = %+v, want the absence recorded", store.saved)
	}
}

// An outage is not an absence. Nothing is written down, so the releases are
// asked about again once the archive is answering — and the pass gives up rather
// than skipping the whole catalogue and coming straight back for more.
func TestAnArchiveThatFailedLeavesTheReleasesUnanswered(t *testing.T) {
	store := &fakeCoverStore{waiting: waiting(5)}
	sweeper := sweeperFor(t, store, func(response http.ResponseWriter, _ *http.Request) {
		response.WriteHeader(http.StatusInternalServerError)
	})

	if _, err := sweeper.Sweep(context.Background()); err == nil {
		t.Fatal("Sweep() error = nil, want the failure reported")
	}
	if len(store.saved) != 0 {
		t.Fatalf("saved = %+v, want an outage not recorded as an absence", store.saved)
	}
}

// A full batch means more are waiting, and the pass says so rather than leaving
// the rest until the idle schedule comes round.
func TestAFullPassSaysThereIsMoreToPicture(t *testing.T) {
	store := &fakeCoverStore{waiting: waiting(sweepBatch)}
	sweeper := sweeperFor(t, store, func(response http.ResponseWriter, _ *http.Request) {
		response.WriteHeader(http.StatusNotFound)
	})

	more, err := sweeper.Sweep(context.Background())
	if err != nil && !errors.Is(err, ErrNoArtwork) {
		t.Fatalf("Sweep() error = %v", err)
	}
	if !more {
		t.Fatal("a full batch reported nothing left, so the rest would wait out the idle schedule")
	}
}

// A pass that cannot even read what is waiting has learned nothing, and saying
// so keeps the job's own backoff in charge.
func TestReleasesThatCouldNotBeListedAreReported(t *testing.T) {
	store := &fakeCoverStore{listReleasesErr: errors.New("the database is not answering")}
	sweeper := sweeperFor(t, store, func(http.ResponseWriter, *http.Request) {
		t.Error("an archive was asked although nothing could be listed")
	})

	if _, err := sweeper.Sweep(context.Background()); err == nil {
		t.Fatal("Sweep() error = nil, want the failure reported")
	}
}

// A picture nobody could write down is a picture that will be fetched again.
// Carrying on would ask the archive about the rest of the batch for nothing.
func TestACoverThatCouldNotBeCachedStopsThePass(t *testing.T) {
	store := &fakeCoverStore{waiting: waiting(3), saveReleaseErr: errors.New("the database is not answering")}
	sweeper := sweeperFor(t, store, func(response http.ResponseWriter, _ *http.Request) {
		response.Header().Set("Content-Type", "image/jpeg")
		_, _ = response.Write(aPicture(1))
	})

	if _, err := sweeper.Sweep(context.Background()); err == nil {
		t.Fatal("Sweep() error = nil, want the failure reported")
	}
	if len(store.saved) != 1 {
		t.Errorf("saved %d, want the pass to stop at the release it could not record", len(store.saved))
	}
}

// A caller that gave up is not made to wait out the pace before finding out.
func TestASweepACallerGaveUpOnStopsWhereItIs(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	store := &fakeCoverStore{waiting: waiting(3), onSave: cancel}
	sweeper := sweeperFor(t, store, func(response http.ResponseWriter, _ *http.Request) {
		response.Header().Set("Content-Type", "image/jpeg")
		_, _ = response.Write(aPicture(1))
	})
	sweeper.pause = time.Hour

	more, err := sweeper.Sweep(ctx)

	if !errors.Is(err, context.Canceled) {
		t.Fatalf("error = %v, want the cancellation reported", err)
	}
	if !more {
		t.Error("a pass that stopped early reported nothing left to do")
	}
	if len(store.saved) != 1 {
		t.Errorf("saved %d, want only the release that was finished", len(store.saved))
	}
}

// A release the caller gave up during was not answered about, so nothing may be
// written down for it.
func TestAReleaseTheCallerGaveUpDuringIsNotRecorded(t *testing.T) {
	store := &fakeCoverStore{waiting: waiting(1)}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	sweeper := sweeperFor(t, store, func(http.ResponseWriter, *http.Request) {
		t.Error("an archive was asked although the caller had gone")
	})

	if _, err := sweeper.Sweep(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("error = %v, want the cancellation reported", err)
	}
	if len(store.saved) != 0 {
		t.Errorf("saved = %+v, want nothing written about a release nobody asked about", store.saved)
	}
}

// One release nothing can answer about is skipped rather than allowed to stop
// the catalogue being pictured. Nothing is recorded for it, so the next pass
// asks again.
func TestOneReleaseNobodyCanAnswerAboutDoesNotStopThePass(t *testing.T) {
	store := &fakeCoverStore{waiting: waiting(3)}
	// Every attempt the first release gets, so it is a release nothing can
	// answer about rather than one intermittent 500, which is retried and
	// pictured.
	asked := 0
	sweeper := sweeperFor(t, store, func(response http.ResponseWriter, _ *http.Request) {
		asked++
		if asked <= fetchAttempts {
			response.WriteHeader(http.StatusInternalServerError)
			return
		}
		response.Header().Set("Content-Type", "image/jpeg")
		_, _ = response.Write(aPicture(1))
	})

	if _, err := sweeper.Sweep(context.Background()); err != nil {
		t.Fatalf("Sweep() error = %v", err)
	}
	if len(store.saved) != 2 {
		t.Fatalf("saved %d, want the other releases pictured and the failure left alone",
			len(store.saved))
	}
}

// artistSweeperFor builds a sweeper that pictures artists as well as releases,
// with every third-party address rewritten to the one test server.
func artistSweeperFor(
	t *testing.T, store *fakeCoverStore, handler http.HandlerFunc,
) *Sweeper {
	t.Helper()
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)
	images := NewClient(Options{BaseURL: server.URL})
	images.httpClient = &http.Client{Transport: rewriteHost{to: server.Listener.Addr().String()}}
	sweeper := NewSweeper(store, Sources{}, zerolog.Nop()).
		WithArtists(NewArtists(fakeRelations{urls: []musicbrainz.URLRelation{
			{Type: "wikidata", Resource: "https://www.wikidata.org/wiki/Q1"},
		}}, images))
	sweeper.pause = 0
	return sweeper
}

func artistsWaiting(count int) []db.ArtistsMissingImageRow {
	rows := make([]db.ArtistsMissingImageRow, 0, count)
	for range count {
		rows = append(rows, db.ArtistsMissingImageRow{
			ID:            uuid.New(),
			MusicbrainzID: uuid.NullUUID{UUID: uuid.New(), Valid: true},
		})
	}
	return rows
}

// The pass does for the people what it does for the records, at the same pace
// and by the same rules.
func TestASweepPicturesTheArtistsNobodyHasLookedFor(t *testing.T) {
	store := &fakeCoverStore{artistsWaiting: artistsWaiting(2)}
	sweeper := artistSweeperFor(t, store, func(response http.ResponseWriter, request *http.Request) {
		if strings.HasSuffix(request.URL.Path, ".json") {
			_, _ = response.Write([]byte(
				`{"entities":{"Q1":{"claims":{"P18":[{"mainsnak":{"datavalue":{"value":"A.jpg"}}}]}}}}`))
			return
		}
		response.Header().Set("Content-Type", "image/jpeg")
		_, _ = response.Write(aPicture(1))
	})

	if _, err := sweeper.Sweep(context.Background()); err != nil {
		t.Fatalf("Sweep() error = %v", err)
	}
	if len(store.savedArtists) != 2 {
		t.Fatalf("saved %d, want every waiting artist pictured", len(store.savedArtists))
	}
	for _, saved := range store.savedArtists {
		if len(saved.Image) == 0 || saved.Source != NameCommons {
			t.Fatalf("saved = %+v, want the picture kept and labelled", saved)
		}
	}
}

// An artist nobody has a picture of is remembered as having none, which is what
// stops them being asked about forever.
func TestAnArtistNobodyHasPicturedIsRememberedAsHavingNone(t *testing.T) {
	store := &fakeCoverStore{artistsWaiting: artistsWaiting(1)}
	sweeper := artistSweeperFor(t, store, func(response http.ResponseWriter, _ *http.Request) {
		response.WriteHeader(http.StatusNotFound)
	})

	if _, err := sweeper.Sweep(context.Background()); err != nil {
		t.Fatalf("Sweep() error = %v", err)
	}
	if len(store.savedArtists) != 1 || store.savedArtists[0].Image != nil {
		t.Fatalf("saved = %+v, want the absence recorded", store.savedArtists)
	}
	// An absence is nobody's answer. Labelling it with whichever service happens
	// to be first in the chain says that service looked and declined, which is a
	// different fact and not the one that happened.
	if store.savedArtists[0].Source != NameNobody {
		t.Fatalf("source = %q, want an absence attributed to no service",
			store.savedArtists[0].Source)
	}
}

// One artist nothing can answer about is skipped rather than allowed to stop the
// rest being pictured, and nothing is recorded for them.
func TestOneArtistNobodyCanAnswerAboutDoesNotStopThePass(t *testing.T) {
	store := &fakeCoverStore{artistsWaiting: artistsWaiting(3)}
	first := true
	sweeper := artistSweeperFor(t, store, func(response http.ResponseWriter, request *http.Request) {
		if first {
			first = false
			response.WriteHeader(http.StatusInternalServerError)
			return
		}
		if strings.HasSuffix(request.URL.Path, ".json") {
			_, _ = response.Write([]byte(
				`{"entities":{"Q1":{"claims":{"P18":[{"mainsnak":{"datavalue":{"value":"A.jpg"}}}]}}}}`))
			return
		}
		response.Header().Set("Content-Type", "image/jpeg")
		_, _ = response.Write(aPicture(1))
	})

	if _, err := sweeper.Sweep(context.Background()); err != nil {
		t.Fatalf("Sweep() error = %v", err)
	}
	if len(store.savedArtists) != 2 {
		t.Fatalf("saved %d, want the other artists pictured and the failure left alone",
			len(store.savedArtists))
	}
}

// A service that is down must not be asked about every remaining artist and then
// asked again straight away.
func TestAServiceThatFailedLeavesTheArtistsUnanswered(t *testing.T) {
	store := &fakeCoverStore{artistsWaiting: artistsWaiting(5)}
	sweeper := artistSweeperFor(t, store, func(response http.ResponseWriter, _ *http.Request) {
		response.WriteHeader(http.StatusInternalServerError)
	})

	if _, err := sweeper.Sweep(context.Background()); err == nil {
		t.Fatal("Sweep() error = nil, want the failure reported")
	}
	if len(store.savedArtists) != 0 {
		t.Fatalf("saved = %+v, want an outage not recorded as an absence", store.savedArtists)
	}
}

func TestArtistsThatCouldNotBeListedAreReported(t *testing.T) {
	store := &fakeCoverStore{listArtistsErr: errors.New("the database is not answering")}
	sweeper := artistSweeperFor(t, store, func(http.ResponseWriter, *http.Request) {
		t.Error("a service was asked although nothing could be listed")
	})

	if _, err := sweeper.Sweep(context.Background()); err == nil {
		t.Fatal("Sweep() error = nil, want the failure reported")
	}
}

func TestAnArtistPictureThatCouldNotBeCachedStopsThePass(t *testing.T) {
	store := &fakeCoverStore{
		artistsWaiting: artistsWaiting(3),
		saveArtistErr:  errors.New("the database is not answering"),
	}
	sweeper := artistSweeperFor(t, store, func(response http.ResponseWriter, _ *http.Request) {
		response.WriteHeader(http.StatusNotFound)
	})

	if _, err := sweeper.Sweep(context.Background()); err == nil {
		t.Fatal("Sweep() error = nil, want the failure reported")
	}
	if len(store.savedArtists) != 1 {
		t.Errorf("saved %d, want the pass to stop at the artist it could not record",
			len(store.savedArtists))
	}
}

// A full batch of artists means more are waiting, and the pass says so rather
// than leaving them until the idle schedule comes round.
func TestAFullPassOfArtistsSaysThereIsMoreToPicture(t *testing.T) {
	store := &fakeCoverStore{artistsWaiting: artistsWaiting(sweepBatch)}
	sweeper := artistSweeperFor(t, store, func(response http.ResponseWriter, _ *http.Request) {
		response.WriteHeader(http.StatusNotFound)
	})

	more, err := sweeper.Sweep(context.Background())
	if err != nil {
		t.Fatalf("Sweep() error = %v", err)
	}
	if !more {
		t.Fatal("a full batch reported nothing left, so the rest would wait out the idle schedule")
	}
}

// The pass asks about artists written off before the chain last changed as well
// as those nobody has asked about, because an absence answers the chain that
// recorded it and this one has grown a rung since.
func TestAPassAsksAboutAbsencesOlderThanTheChain(t *testing.T) {
	store := &fakeCoverStore{artistsWaiting: artistsWaiting(1)}
	sweeper := artistSweeperFor(t, store, func(response http.ResponseWriter, _ *http.Request) {
		response.WriteHeader(http.StatusNotFound)
	})

	if _, err := sweeper.Sweep(context.Background()); err != nil {
		t.Fatalf("Sweep() error = %v", err)
	}
	if len(store.askedArtists) != 1 {
		t.Fatalf("asked %d times, want the artists asked for once", len(store.askedArtists))
	}
	asked := store.askedArtists[0].ChainChanged
	if !asked.Valid || !asked.Time.Equal(chainChanged) {
		t.Fatalf("asked for absences before %+v, want the day the chain last changed", asked)
	}
}

// A caller that gave up is not made to wait out the pace between artists.
func TestAnArtistPassACallerGaveUpOnStopsWhereItIs(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	store := &fakeCoverStore{artistsWaiting: artistsWaiting(3), onSave: cancel}
	sweeper := artistSweeperFor(t, store, func(response http.ResponseWriter, _ *http.Request) {
		response.WriteHeader(http.StatusNotFound)
	})
	sweeper.pause = time.Hour

	if _, err := sweeper.Sweep(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("error = %v, want the cancellation reported", err)
	}
	if len(store.savedArtists) != 1 {
		t.Errorf("saved %d, want only the artist that was finished", len(store.savedArtists))
	}
}

// An artist the caller gave up during was not answered about, so nothing may be
// written down for them.
func TestAnArtistTheCallerGaveUpDuringIsNotRecorded(t *testing.T) {
	store := &fakeCoverStore{artistsWaiting: artistsWaiting(1)}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	sweeper := artistSweeperFor(t, store, func(http.ResponseWriter, *http.Request) {
		t.Error("a service was asked although the caller had gone")
	})

	if _, err := sweeper.Sweep(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("error = %v, want the cancellation reported", err)
	}
	if len(store.savedArtists) != 0 {
		t.Errorf("saved = %+v, want nothing written about an artist nobody asked about",
			store.savedArtists)
	}
}

package server

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/pxldi/schall/internal/acquisition"
	"github.com/pxldi/schall/internal/db"
	"github.com/pxldi/schall/internal/identity"
	"github.com/pxldi/schall/internal/preview"
	"github.com/rs/zerolog"
)

type fakeAcquisitionTargets struct {
	target     db.AcquisitionTargetRow
	candidates []db.AcquisitionTargetCandidateRow
	copies     []db.AcquisitionTargetFileRow
	created    bool
	rejected   uuid.UUID
	accepted   uuid.UUID
	refused    uuid.UUID
	entry      acquisition.Entry
	entries    []acquisition.Entry
	// createdFor answers one entry at a time, so a test wanting a whole release
	// can say which of its tracks were already wanted. Optional: without it the
	// single answers above stand for every call.
	createdFor func(acquisition.Entry) (bool, error)
	err        error
	stopped    uuid.UUID
	restored   uuid.UUID
	// chosen is the recording a resolution question was answered with, and
	takenBest uuid.UUID
	keptFloor uuid.UUID
	// chooseErr is what answering it came to.
	chosen    uuid.UUID
	chooseErr error
	// candidatesErr and copiesErr answer one lookup each, so a want that could
	// be read and a question or a history that could not are separate tests.
	candidatesErr error
	copiesErr     error
	// listed is the last page of wants that was asked for, so a test can read
	// what the query string came to.
	listed db.ListAcquisitionTargetsParams
	// takenBack is the answer a request asked to withdraw, and reversible is
	// what the queue is told about whether it still can be.
	takenBack   db.Answer
	takenBackID uuid.UUID
	takeBackErr error
	reversible  bool

	acceptedFile    uuid.UUID
	acceptFileErr   error
	waveformFor     uuid.UUID
	waveformWritten []byte
}

func (targets *fakeAcquisitionTargets) Create(
	_ context.Context, entry acquisition.Entry,
) (db.AcquisitionTargetRow, bool, error) {
	targets.entry = entry
	targets.entries = append(targets.entries, entry)
	if targets.createdFor != nil {
		created, err := targets.createdFor(entry)
		return targets.target, created, err
	}
	return targets.target, targets.created, targets.err
}

func (targets *fakeAcquisitionTargets) Target(
	_ context.Context, _ uuid.UUID,
) (db.AcquisitionTargetRow, error) {
	return targets.target, targets.err
}

func (targets *fakeAcquisitionTargets) List(
	_ context.Context, params db.ListAcquisitionTargetsParams,
) (db.AcquisitionTargetPage, error) {
	targets.listed = params
	return db.AcquisitionTargetPage{Items: []db.AcquisitionTargetRow{targets.target}, Total: 1}, targets.err
}

func (targets *fakeAcquisitionTargets) TakeBestAvailable(
	_ context.Context, id uuid.UUID,
) (db.AcquisitionTargetRow, error) {
	targets.takenBest = id
	return targets.target, targets.err
}

func (targets *fakeAcquisitionTargets) KeepFloor(
	_ context.Context, id uuid.UUID,
) (db.AcquisitionTargetRow, error) {
	targets.keptFloor = id
	return targets.target, targets.err
}

func (targets *fakeAcquisitionTargets) StopPursuing(
	_ context.Context, id uuid.UUID,
) (db.AcquisitionTargetRow, error) {
	targets.stopped = id
	return targets.target, targets.err
}

func (targets *fakeAcquisitionTargets) PursueAgain(
	_ context.Context, id uuid.UUID,
) (db.AcquisitionTargetRow, error) {
	targets.restored = id
	return targets.target, targets.err
}

func (targets *fakeAcquisitionTargets) Candidates(
	_ context.Context, _ uuid.UUID,
) ([]db.AcquisitionTargetCandidateRow, error) {
	if targets.candidatesErr != nil {
		return nil, targets.candidatesErr
	}
	return targets.candidates, targets.err
}

func (targets *fakeAcquisitionTargets) Copies(
	_ context.Context, _ uuid.UUID,
) ([]db.AcquisitionTargetFileRow, error) {
	if targets.copiesErr != nil {
		return nil, targets.copiesErr
	}
	return targets.copies, targets.err
}

func (targets *fakeAcquisitionTargets) RejectResolution(
	_ context.Context, id uuid.UUID,
) (db.AcquisitionTargetRow, error) {
	targets.rejected = id
	return targets.target, targets.err
}

func (targets *fakeAcquisitionTargets) ChooseResolution(
	_ context.Context, _, recordingID uuid.UUID,
) (db.AcquisitionTargetRow, error) {
	targets.chosen = recordingID
	if targets.chooseErr != nil {
		return db.AcquisitionTargetRow{}, targets.chooseErr
	}
	return targets.target, targets.err
}

func (targets *fakeAcquisitionTargets) ReviewQueue(
	_ context.Context, _, _ int32,
) (db.AcquisitionReviewPage, error) {
	return db.AcquisitionReviewPage{
		Items: []db.AcquisitionReviewItem{{
			Target: targets.target, Files: targets.copies, Candidates: targets.candidates,
		}},
		Total: 1,
	}, targets.err
}

func (targets *fakeAcquisitionTargets) Copy(
	_ context.Context, _ uuid.UUID,
) (db.AcquisitionTargetFileRow, error) {
	if len(targets.copies) == 0 {
		return db.AcquisitionTargetFileRow{}, pgx.ErrNoRows
	}
	return targets.copies[0], targets.err
}

func (targets *fakeAcquisitionTargets) AcceptCopy(_ context.Context, id uuid.UUID) error {
	targets.accepted = id
	return targets.err
}

func (targets *fakeAcquisitionTargets) AcceptFiledCopy(_ context.Context, id uuid.UUID) error {
	targets.acceptedFile = id
	return targets.acceptFileErr
}

func (targets *fakeAcquisitionTargets) RecordCopyWaveform(
	_ context.Context, copyID uuid.UUID, peaks []byte,
) error {
	targets.waveformFor, targets.waveformWritten = copyID, peaks
	return nil
}

func (targets *fakeAcquisitionTargets) RefuseCopies(_ context.Context, id uuid.UUID) error {
	targets.refused = id
	return targets.err
}

func (targets *fakeAcquisitionTargets) Reversible(
	_ context.Context, _ db.Answer, _ uuid.UUID,
) (bool, error) {
	return targets.reversible, targets.err
}

func (targets *fakeAcquisitionTargets) TakeBack(
	_ context.Context, answer db.Answer, id uuid.UUID,
) error {
	targets.takenBack, targets.takenBackID = answer, id
	return targets.takeBackErr
}

func acquisitionHandler(targets *fakeAcquisitionTargets) http.Handler {
	return releaseHandler(&fakeStore{}, targets)
}

// releaseHandler is the same API with a catalogue behind it, for the requests
// that read a release before wanting anything.
func releaseHandler(store *fakeStore, targets *fakeAcquisitionTargets) http.Handler {
	return NewAPI(store, fakeDatabase{}, &fakeArtistSearcher{}, zerolog.Nop(),
		WithAcquisitionTargets(targets))
}

// A new want is a creation; asking for a recording that is already wanted is
// not, and the difference has to be visible to whatever asked.
func TestWantingARecordingReportsWhetherItIsNew(t *testing.T) {
	targets := &fakeAcquisitionTargets{
		target:  db.AcquisitionTargetRow{ID: uuid.New(), Status: "pending", Origin: "manual"},
		created: true,
	}
	handler := acquisitionHandler(targets)

	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodPost, "/api/v1/acquisition-targets",
		strings.NewReader(`{"artist":"Portishead","title":"Glory Box"}`)))
	if response.Code != http.StatusCreated {
		t.Fatalf("status = %d; body = %s", response.Code, response.Body)
	}

	targets.created = false
	response = httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodPost, "/api/v1/acquisition-targets",
		strings.NewReader(`{"artist":"Portishead","title":"Glory Box"}`)))
	if response.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 for a recording already wanted; body = %s",
			response.Code, response.Body)
	}
}

// A target with nothing to identify it is refused rather than stored as
// something nothing could ever satisfy.
func TestAWantWithNothingToIdentifyItIsRefused(t *testing.T) {
	handler := acquisitionHandler(&fakeAcquisitionTargets{})

	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodPost, "/api/v1/acquisition-targets",
		strings.NewReader(`{"artist":"Portishead"}`)))

	if response.Code != http.StatusUnprocessableEntity {
		t.Fatalf("status = %d; body = %s", response.Code, response.Body)
	}
}

// An entry may carry a recording somebody already resolved, and it has to reach
// the service as an identifier rather than as text.
func TestAWantCanBeCreatedAgainstAKnownRecording(t *testing.T) {
	recordingID := uuid.New()
	targets := &fakeAcquisitionTargets{created: true}
	handler := acquisitionHandler(targets)

	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodPost, "/api/v1/acquisition-targets",
		strings.NewReader(`{"title":"Glory Box","recordingId":"`+recordingID.String()+`"}`)))

	if response.Code != http.StatusCreated {
		t.Fatalf("status = %d; body = %s", response.Code, response.Body)
	}
	if !targets.entry.RecordingID.Valid || targets.entry.RecordingID.UUID != recordingID {
		t.Fatalf("recording = %v, want %s", targets.entry.RecordingID, recordingID)
	}
}

// An identifier that is not one is refused rather than silently dropped, which
// would record a want pointed at nothing.
func TestAWantWithAnUnreadableIdentifierIsRefused(t *testing.T) {
	handler := acquisitionHandler(&fakeAcquisitionTargets{})

	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodPost, "/api/v1/acquisition-targets",
		strings.NewReader(`{"title":"Glory Box","recordingId":"not-a-uuid"}`)))

	if response.Code != http.StatusUnprocessableEntity {
		t.Fatalf("status = %d; body = %s", response.Code, response.Body)
	}
}

// dummy is a release with one owned track and two the library does not have.
func dummy() (uuid.UUID, *fakeStore) {
	albumID, releaseGroupID := uuid.New(), uuid.New()
	return albumID, &fakeStore{
		album: db.GetAlbumRow{
			ID: albumID, ArtistName: "Portishead", Title: "Dummy",
			MusicbrainzReleaseGroupID: uuid.NullUUID{UUID: releaseGroupID, Valid: true},
		},
		tracks: []db.ListAlbumTracksRow{
			{Title: "Mysterons", MusicbrainzRecordingID: uuid.NullUUID{UUID: uuid.New(), Valid: true}},
			{Title: "Sour Times", Owned: true},
			{
				Title: "Strangers", MusicbrainzRecordingID: uuid.NullUUID{UUID: uuid.New(), Valid: true},
				DurationMs: pgtype.Int4{Int32: 238000, Valid: true},
			},
		},
	}
}

// Wanting a release wants the tracks the library does not have, and only those.
func TestWantingAReleaseWantsOnlyTheTracksTheLibraryLacks(t *testing.T) {
	albumID, store := dummy()
	targets := &fakeAcquisitionTargets{created: true}

	response := httptest.NewRecorder()
	releaseHandler(store, targets).ServeHTTP(response, httptest.NewRequest(
		http.MethodPost, "/api/v1/albums/"+albumID.String()+"/wanted", nil))

	if response.Code != http.StatusOK {
		t.Fatalf("status = %d; body = %s", response.Code, response.Body)
	}
	var result wantAlbumResponse
	if err := json.NewDecoder(response.Body).Decode(&result); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if result.Wanted != 2 || result.Owned != 1 {
		t.Fatalf("result = %+v, want two wanted and the owned track left alone", result)
	}
	for _, entry := range targets.entries {
		if entry.Title == "Sour Times" {
			t.Fatal("a track the library already has was wanted again")
		}
	}
}

// A file proven to be the recording is the library holding that music, whether
// or not anything ever mapped it onto this release. The acquisition loop settles
// a want against exactly that, so wanting it would be asking strangers for music
// already on the disc.
func TestATrackProvenByItsAudioIsNotWantedFromItsRelease(t *testing.T) {
	albumID, store := dummy()
	store.tracks[0].HeldElsewhere = true
	targets := &fakeAcquisitionTargets{created: true}

	response := httptest.NewRecorder()
	releaseHandler(store, targets).ServeHTTP(response, httptest.NewRequest(
		http.MethodPost, "/api/v1/albums/"+albumID.String()+"/wanted", nil))

	var result wantAlbumResponse
	if err := json.NewDecoder(response.Body).Decode(&result); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if result.Wanted != 1 || result.Owned != 2 {
		t.Fatalf("result = %+v, want the identified track counted as held", result)
	}
	for _, entry := range targets.entries {
		if entry.Title == "Mysterons" {
			t.Fatal("a track the library already holds was wanted again")
		}
	}
}

// A want made from the catalogue carries the recording the catalogue holds, so
// it lands resolved and due rather than waiting for text to be resolved again.
func TestWantingAReleaseCarriesEachTracksRecording(t *testing.T) {
	albumID, store := dummy()
	targets := &fakeAcquisitionTargets{created: true}

	releaseHandler(store, targets).ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(
		http.MethodPost, "/api/v1/albums/"+albumID.String()+"/wanted", nil))

	if len(targets.entries) != 2 {
		t.Fatalf("entries = %d, want one per unowned track", len(targets.entries))
	}
	for index, entry := range targets.entries {
		if !entry.RecordingID.Valid ||
			entry.RecordingID.UUID != store.tracks[index*2].MusicbrainzRecordingID.UUID {
			t.Errorf("entry %d recording = %v, want the track's own", index, entry.RecordingID)
		}
	}
}

// The release the want came from travels with it: it is the provenance of the
// want and the release context a verified copy files under.
func TestWantingAReleaseRecordsTheReleaseItCameFrom(t *testing.T) {
	albumID, store := dummy()
	targets := &fakeAcquisitionTargets{created: true}

	releaseHandler(store, targets).ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(
		http.MethodPost, "/api/v1/albums/"+albumID.String()+"/wanted", nil))

	entry := targets.entry
	if entry.OriginAlbumID.UUID != albumID || entry.Album != "Dummy" || entry.Artist != "Portishead" {
		t.Fatalf("entry = %+v, want the release it was wanted from", entry)
	}
	if entry.ReleaseGroupID != store.album.MusicbrainzReleaseGroupID {
		t.Errorf("release group = %v, want %v", entry.ReleaseGroupID, store.album.MusicbrainzReleaseGroupID)
	}
}

// Pressing it a second time is not a second want. The tracks already wanted are
// reported as such, which is also how a track somebody stopped pursuing stays
// stopped rather than being re-opened by the release around it.
func TestWantingAReleaseTwiceReportsTheWantsThatAlreadyExist(t *testing.T) {
	albumID, store := dummy()
	targets := &fakeAcquisitionTargets{
		createdFor: func(acquisition.Entry) (bool, error) { return false, nil },
	}

	response := httptest.NewRecorder()
	releaseHandler(store, targets).ServeHTTP(response, httptest.NewRequest(
		http.MethodPost, "/api/v1/albums/"+albumID.String()+"/wanted", nil))

	var result wantAlbumResponse
	if err := json.NewDecoder(response.Body).Decode(&result); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if result.Wanted != 0 || result.AlreadyWanted != 2 {
		t.Fatalf("result = %+v, want nothing new and both already wanted", result)
	}
}

// A track the catalogue holds with nothing to identify it is counted and the
// rest of the release is still wanted, because one unusable row is not a reason
// to refuse the eleven beside it.
func TestATrackWithNothingToIdentifyItDoesNotStopTheRest(t *testing.T) {
	albumID, store := dummy()
	targets := &fakeAcquisitionTargets{
		createdFor: func(entry acquisition.Entry) (bool, error) {
			if entry.Title == "Mysterons" {
				return false, acquisition.ErrEntryNotIdentifiable
			}
			return true, nil
		},
	}

	response := httptest.NewRecorder()
	releaseHandler(store, targets).ServeHTTP(response, httptest.NewRequest(
		http.MethodPost, "/api/v1/albums/"+albumID.String()+"/wanted", nil))

	if response.Code != http.StatusOK {
		t.Fatalf("status = %d; body = %s", response.Code, response.Body)
	}
	var result wantAlbumResponse
	if err := json.NewDecoder(response.Body).Decode(&result); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if result.Unresolvable != 1 || result.Wanted != 1 {
		t.Fatalf("result = %+v, want the unusable track counted and the other wanted", result)
	}
}

// A release that is not there is not found, rather than a want for nothing.
func TestWantingAReleaseThatDoesNotExistIsNotFound(t *testing.T) {
	targets := &fakeAcquisitionTargets{created: true}
	handler := releaseHandler(&fakeStore{err: pgx.ErrNoRows}, targets)

	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(
		http.MethodPost, "/api/v1/albums/"+uuid.New().String()+"/wanted", nil))

	if response.Code != http.StatusNotFound {
		t.Fatalf("status = %d; body = %s", response.Code, response.Body)
	}
	if len(targets.entries) != 0 {
		t.Errorf("entries = %d, want nothing wanted for a release nobody has", len(targets.entries))
	}
}

// A track the catalogue holds no recording against is skipped. Creation is
// idempotent on the recording ID and a want without one is always new, so
// creating it would make a second want for the same track on every press.
func TestATrackWithNoRecordingIsNotWantedAtAll(t *testing.T) {
	albumID, store := dummy()
	store.tracks = append(store.tracks, db.ListAlbumTracksRow{Title: "Numb"})
	targets := &fakeAcquisitionTargets{created: true}

	response := httptest.NewRecorder()
	releaseHandler(store, targets).ServeHTTP(response, httptest.NewRequest(
		http.MethodPost, "/api/v1/albums/"+albumID.String()+"/wanted", nil))

	var result wantAlbumResponse
	if err := json.NewDecoder(response.Body).Decode(&result); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if result.Unresolvable != 1 || result.Wanted != 2 {
		t.Fatalf("result = %+v, want the track with no recording counted and the rest wanted", result)
	}
	for _, entry := range targets.entries {
		if entry.Title == "Numb" {
			t.Fatal("a track with no recording was wanted, and a second press would want it again")
		}
	}
}

// discography is two releases: one owned outright, one the library holds a
// single track of.
func discography() (uuid.UUID, *fakeStore) {
	artistID, dummyID, portishead := uuid.New(), uuid.New(), uuid.New()
	recording := func() uuid.NullUUID { return uuid.NullUUID{UUID: uuid.New(), Valid: true} }
	return artistID, &fakeStore{
		artist: db.GetArtistRow{ID: artistID, Name: "Portishead"},
		discography: []db.ArtistTracksForWantingRow{
			{
				AlbumID: dummyID, AlbumTitle: "Dummy", ArtistName: "Portishead",
				Title: "Mysterons", MusicbrainzRecordingID: recording(),
			},
			{
				AlbumID: dummyID, AlbumTitle: "Dummy", ArtistName: "Portishead",
				Title: "Sour Times", MusicbrainzRecordingID: recording(), Owned: true,
			},
			{
				AlbumID: portishead, AlbumTitle: "Portishead", ArtistName: "Portishead",
				Title: "Cowboys", MusicbrainzRecordingID: recording(),
			},
		},
	}
}

// Wanting an artist wants what the library lacks across every release they
// have, and says how many releases that was.
func TestWantingAnArtistWantsTheMissingTracksOfEveryRelease(t *testing.T) {
	artistID, store := discography()
	targets := &fakeAcquisitionTargets{created: true}

	response := httptest.NewRecorder()
	releaseHandler(store, targets).ServeHTTP(response, httptest.NewRequest(
		http.MethodPost, "/api/v1/artists/"+artistID.String()+"/wanted", nil))

	if response.Code != http.StatusOK {
		t.Fatalf("status = %d; body = %s", response.Code, response.Body)
	}
	var result wantArtistResponse
	if err := json.NewDecoder(response.Body).Decode(&result); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if result.Wanted != 2 || result.Owned != 1 || result.Releases != 2 {
		t.Fatalf("result = %+v, want both missing tracks wanted across two releases", result)
	}
}

// A want made from a discography files under the release the track sits on
// rather than under the artist, because that release is the context a verified
// copy is filed by.
func TestAWantFromADiscographyCarriesTheReleaseTheTrackSitsOn(t *testing.T) {
	artistID, store := discography()
	targets := &fakeAcquisitionTargets{created: true}

	releaseHandler(store, targets).ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(
		http.MethodPost, "/api/v1/artists/"+artistID.String()+"/wanted", nil))

	if len(targets.entries) != 2 {
		t.Fatalf("entries = %d, want one per unowned track", len(targets.entries))
	}
	for _, entry := range targets.entries {
		want := store.discography[0]
		if entry.Title == "Cowboys" {
			want = store.discography[2]
		}
		if entry.OriginAlbumID.UUID != want.AlbumID || entry.Album != want.AlbumTitle {
			t.Errorf("entry %q = %+v, want the release it sits on", entry.Title, entry)
		}
	}
}

// Pressing it twice is not two wants: the second press answers with the targets
// that exist. It is also what keeps a track somebody stopped pursuing stopped,
// rather than being re-opened by the discography around it.
func TestWantingADiscographyTwiceReportsTheWantsThatAlreadyExist(t *testing.T) {
	artistID, store := discography()
	targets := &fakeAcquisitionTargets{
		createdFor: func(acquisition.Entry) (bool, error) { return false, nil },
	}

	response := httptest.NewRecorder()
	releaseHandler(store, targets).ServeHTTP(response, httptest.NewRequest(
		http.MethodPost, "/api/v1/artists/"+artistID.String()+"/wanted", nil))

	var result wantArtistResponse
	if err := json.NewDecoder(response.Body).Decode(&result); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if result.Wanted != 0 || result.AlreadyWanted != 2 {
		t.Fatalf("result = %+v, want nothing new and both already wanted", result)
	}
}

// A discography reaches the same recording over and over — the album, the
// single, the compilation — and those are one want. Counting the repeats as
// "already wanted" would tell somebody pressing this for the first time that
// they had asked before.
func TestARecordingReachedTwiceInADiscographyIsCountedOnce(t *testing.T) {
	artistID, store := discography()
	twice := store.discography[0]
	twice.AlbumID, twice.AlbumTitle = uuid.New(), "Best Of"
	store.discography = append(store.discography, twice)
	targets := &fakeAcquisitionTargets{created: true}

	response := httptest.NewRecorder()
	releaseHandler(store, targets).ServeHTTP(response, httptest.NewRequest(
		http.MethodPost, "/api/v1/artists/"+artistID.String()+"/wanted", nil))

	var result wantArtistResponse
	if err := json.NewDecoder(response.Body).Decode(&result); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if result.Wanted != 2 || result.AlreadyWanted != 0 {
		t.Fatalf("result = %+v, want the compilation copy to add no want of its own", result)
	}
	if len(targets.entries) != 2 {
		t.Errorf("entries = %d, want the same recording asked for once", len(targets.entries))
	}
}

// Turning the standing want on walks the discography now and leaves the mark
// that wants the rest as their tracklists arrive. Following an artist queues
// one release refresh per release group and they land one at a time behind
// MusicBrainz's rate limit, so a press that only walked what had arrived left
// the user to come back and press again.
func TestTurningTheStandingWantOnWantsWhatIsMissingNow(t *testing.T) {
	artistID, store := discography()
	targets := &fakeAcquisitionTargets{created: true}

	response := httptest.NewRecorder()
	releaseHandler(store, targets).ServeHTTP(response, httptest.NewRequest(
		http.MethodPut, "/api/v1/artists/"+artistID.String()+"/want-missing",
		strings.NewReader(`{"on":true}`)))

	if response.Code != http.StatusOK {
		t.Fatalf("status = %d; body = %s", response.Code, response.Body)
	}
	var result artistWantMissingResponse
	if err := json.NewDecoder(response.Body).Decode(&result); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if !result.WantMissing {
		t.Errorf("wantMissing = false, want the standing want reported as on")
	}
	if !store.wantMissing {
		t.Errorf("the store was not marked, so a tracklist arriving later wants nothing")
	}
	if result.Wanted != 2 || result.Owned != 1 || result.Releases != 2 {
		t.Fatalf("result = %+v, want both missing tracks wanted across two releases", result)
	}
}

// Turning it off stops future wanting and nothing else. The wants made under it
// were asked for, so nothing here reaches them.
func TestTurningTheStandingWantOffLeavesTheWantsItMade(t *testing.T) {
	artistID, store := discography()
	store.wantMissing = true
	targets := &fakeAcquisitionTargets{created: true}

	response := httptest.NewRecorder()
	releaseHandler(store, targets).ServeHTTP(response, httptest.NewRequest(
		http.MethodPut, "/api/v1/artists/"+artistID.String()+"/want-missing",
		strings.NewReader(`{"on":false}`)))

	if response.Code != http.StatusOK {
		t.Fatalf("status = %d; body = %s", response.Code, response.Body)
	}
	var result artistWantMissingResponse
	if err := json.NewDecoder(response.Body).Decode(&result); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if result.WantMissing {
		t.Errorf("wantMissing = true, want the standing want reported as off")
	}
	if store.wantMissing {
		t.Errorf("the store is still marked, so tracklists would keep being wanted")
	}
	if len(targets.entries) != 0 {
		t.Errorf("entries = %d, want clearing the standing want to make no want", len(targets.entries))
	}
	if targets.stopped != uuid.Nil {
		t.Errorf("stopped = %s, want no existing want touched", targets.stopped)
	}
}

// The other way the library can already hold a discography track: a file proven
// to be that recording, sitting under some other release entirely.
func TestATrackProvenByItsAudioIsNotWantedFromADiscography(t *testing.T) {
	artistID, store := discography()
	store.discography[0].HeldElsewhere = true
	targets := &fakeAcquisitionTargets{created: true}

	response := httptest.NewRecorder()
	releaseHandler(store, targets).ServeHTTP(response, httptest.NewRequest(
		http.MethodPost, "/api/v1/artists/"+artistID.String()+"/wanted", nil))

	var result wantArtistResponse
	if err := json.NewDecoder(response.Body).Decode(&result); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if result.Wanted != 1 || result.Owned != 2 {
		t.Fatalf("result = %+v, want the identified track counted as held", result)
	}
}

// An artist nobody holds is not found, rather than a discography of nothing.
func TestWantingAnArtistThatDoesNotExistIsNotFound(t *testing.T) {
	targets := &fakeAcquisitionTargets{created: true}
	handler := releaseHandler(&fakeStore{getArtistErr: pgx.ErrNoRows}, targets)

	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(
		http.MethodPost, "/api/v1/artists/"+uuid.New().String()+"/wanted", nil))

	if response.Code != http.StatusNotFound {
		t.Fatalf("status = %d; body = %s", response.Code, response.Body)
	}
	if len(targets.entries) != 0 {
		t.Errorf("entries = %d, want nothing wanted for an artist nobody has", len(targets.entries))
	}
}

// An artist whose music the library already holds in full is answered, not
// refused: nothing is missing, and that is the answer.
func TestWantingAnArtistWithNothingMissingWantsNothing(t *testing.T) {
	artistID, store := discography()
	for index := range store.discography {
		store.discography[index].Owned = true
	}
	targets := &fakeAcquisitionTargets{created: true}

	response := httptest.NewRecorder()
	releaseHandler(store, targets).ServeHTTP(response, httptest.NewRequest(
		http.MethodPost, "/api/v1/artists/"+artistID.String()+"/wanted", nil))

	if response.Code != http.StatusOK {
		t.Fatalf("status = %d; body = %s", response.Code, response.Body)
	}
	var result wantArtistResponse
	if err := json.NewDecoder(response.Body).Decode(&result); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if result.Wanted != 0 || result.Owned != 3 || result.Releases != 0 {
		t.Fatalf("result = %+v, want nothing wanted and everything counted as owned", result)
	}
}

// Stopping something already finished with is a conflict the caller can act on,
// not a silent success.
func TestStoppingATargetThatIsFinishedWithIsAConflict(t *testing.T) {
	handler := acquisitionHandler(&fakeAcquisitionTargets{err: pgx.ErrNoRows})

	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodPost,
		"/api/v1/acquisition-targets/"+uuid.New().String()+"/not-wanted", nil))

	if response.Code != http.StatusConflict {
		t.Fatalf("status = %d; body = %s", response.Code, response.Body)
	}
}

// The decision reaches the service for the target the caller named.
func TestStoppingATargetNamesIt(t *testing.T) {
	targetID := uuid.New()
	targets := &fakeAcquisitionTargets{
		target: db.AcquisitionTargetRow{ID: targetID, Status: "not_wanted", Origin: "manual"},
	}
	handler := acquisitionHandler(targets)

	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodPost,
		"/api/v1/acquisition-targets/"+targetID.String()+"/not-wanted", nil))

	if response.Code != http.StatusOK {
		t.Fatalf("status = %d; body = %s", response.Code, response.Body)
	}
	if targets.stopped != targetID {
		t.Fatalf("stopped = %s, want %s", targets.stopped, targetID)
	}
	var payload acquisitionTargetResponse
	if err := json.NewDecoder(response.Body).Decode(&payload); err != nil {
		t.Fatal(err)
	}
	if payload.Status != "not_wanted" {
		t.Fatalf("status = %s, want not_wanted", payload.Status)
	}
}

// A decision to stop may be taken back by the person who made it, and the
// target says it is wanted again.
func TestATargetCanBeWantedAgain(t *testing.T) {
	targetID := uuid.New()
	targets := &fakeAcquisitionTargets{
		target: db.AcquisitionTargetRow{ID: targetID, Status: "pending", Origin: "manual"},
	}
	handler := acquisitionHandler(targets)

	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodDelete,
		"/api/v1/acquisition-targets/"+targetID.String()+"/not-wanted", nil))

	if response.Code != http.StatusOK {
		t.Fatalf("status = %d; body = %s", response.Code, response.Body)
	}
	if targets.restored != targetID {
		t.Fatalf("wanted again = %s, want %s", targets.restored, targetID)
	}
}

func TestFloorControlsReachTheNamedTarget(t *testing.T) {
	targetID := uuid.New()
	targets := &fakeAcquisitionTargets{
		target: db.AcquisitionTargetRow{ID: targetID, Status: "pending", Origin: "manual"},
	}
	handler := acquisitionHandler(targets)

	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodPost,
		"/api/v1/acquisition-targets/"+targetID.String()+"/take-best-available", nil))
	if response.Code != http.StatusOK {
		t.Fatalf("take best status = %d; body = %s", response.Code, response.Body)
	}
	if targets.takenBest != targetID {
		t.Fatalf("taken best = %s, want %s", targets.takenBest, targetID)
	}

	response = httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodDelete,
		"/api/v1/acquisition-targets/"+targetID.String()+"/take-best-available", nil))
	if response.Code != http.StatusOK {
		t.Fatalf("keep floor status = %d; body = %s", response.Code, response.Body)
	}
	if targets.keptFloor != targetID {
		t.Fatalf("kept floor = %s, want %s", targets.keptFloor, targetID)
	}
}

// Taking back a decision nobody made is a conflict the caller can act on.
func TestWantingATargetNobodyStoppedIsAConflict(t *testing.T) {
	handler := acquisitionHandler(&fakeAcquisitionTargets{err: pgx.ErrNoRows})

	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodDelete,
		"/api/v1/acquisition-targets/"+uuid.New().String()+"/not-wanted", nil))

	if response.Code != http.StatusConflict {
		t.Fatalf("status = %d; body = %s", response.Code, response.Body)
	}
}

// A target waiting on a decision is sent with the recordings it could name and
// what stood for and against each. Without them the queue would show a list of
// titles and ask somebody to guess.
func TestATargetAwaitingADecisionCarriesItsCandidates(t *testing.T) {
	targetID, recordingID := uuid.New(), uuid.New()
	reason := "resolution"
	anchorRetry := time.Date(2026, time.August, 14, 12, 30, 0, 0, time.UTC)
	targets := &fakeAcquisitionTargets{
		target: db.AcquisitionTargetRow{
			ID: targetID, Status: "awaiting_review", Origin: "playlist",
			ReviewReason: pgtype.Text{String: reason, Valid: true},
			AnchorUnavailable: pgtype.Text{
				String: "No upload matched this entry's name and length.", Valid: true,
			},
			AnchorAttempts:      4,
			AnchorNextAttemptAt: pgtype.Timestamptz{Time: anchorRetry, Valid: true},
		},
		candidates: []db.AcquisitionTargetCandidateRow{{
			MusicBrainzRecordingID: recordingID, ArtistName: "Portishead",
			TrackTitle: "Glory Box", Rank: 1,
			Agrees:  []string{"artist", "duration"},
			Differs: []string{"title"},
			Summary: "Its artist and duration agree, but its title disagrees.",
		}},
	}
	handler := acquisitionHandler(targets)

	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet,
		"/api/v1/acquisition-targets/"+targetID.String(), nil))

	if response.Code != http.StatusOK {
		t.Fatalf("status = %d; body = %s", response.Code, response.Body)
	}
	var payload acquisitionTargetResponse
	if err := json.NewDecoder(response.Body).Decode(&payload); err != nil {
		t.Fatal(err)
	}
	if len(payload.Candidates) != 1 {
		t.Fatalf("candidates = %d, want the recordings the entry could name",
			len(payload.Candidates))
	}
	candidate := payload.Candidates[0]
	if candidate.RecordingID != recordingID {
		t.Fatalf("candidate = %s, want %s", candidate.RecordingID, recordingID)
	}
	if len(candidate.Agrees) != 2 || len(candidate.Differs) != 1 {
		t.Fatalf("evidence = %+v, want what stood for and against it", candidate)
	}
	if payload.AnchorUnavailable == nil || *payload.AnchorUnavailable !=
		"No upload matched this entry's name and length." {
		t.Fatalf("anchor unavailable = %v", payload.AnchorUnavailable)
	}
	if payload.AnchorAttempts != 4 {
		t.Fatalf("anchor attempts = %d, want 4", payload.AnchorAttempts)
	}
	if payload.AnchorNextAttemptAt == nil || !payload.AnchorNextAttemptAt.Equal(anchorRetry) {
		t.Fatalf("anchor next attempt at = %v, want %v", payload.AnchorNextAttemptAt, anchorRetry)
	}
}

// A target nobody has to decide about carries no question, and the field is left
// out rather than sent empty.
func TestATargetWithNothingToDecideCarriesNoCandidates(t *testing.T) {
	targetID := uuid.New()
	targets := &fakeAcquisitionTargets{
		target: db.AcquisitionTargetRow{ID: targetID, Status: "pending", Origin: "manual"},
	}
	handler := acquisitionHandler(targets)

	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet,
		"/api/v1/acquisition-targets/"+targetID.String(), nil))

	if response.Code != http.StatusOK {
		t.Fatalf("status = %d; body = %s", response.Code, response.Body)
	}
	if strings.Contains(response.Body.String(), "candidates") {
		t.Fatalf("body = %s, want no question where there is none", response.Body)
	}
}

// An installation without the component says so, rather than reporting that
// nothing is wanted.
func TestAcquisitionEndpointsReportBeingUnavailable(t *testing.T) {
	handler := NewAPI(&fakeStore{}, fakeDatabase{}, &fakeArtistSearcher{}, zerolog.Nop())

	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/api/v1/acquisition-targets", nil))

	if response.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d", response.Code)
	}
}

// A want that keeps looking has to be able to say what it has already looked at,
// and with what came of it — which is the only way the boundary between proving,
// refusing and asking can be measured on real downloads.
func TestATargetReportsTheCopiesItHasTried(t *testing.T) {
	targetID := uuid.New()
	targets := &fakeAcquisitionTargets{
		target: db.AcquisitionTargetRow{ID: targetID, Status: "pending", Origin: "playlist"},
		copies: []db.AcquisitionTargetFileRow{{
			AcquisitionTargetID: targetID, Provider: "slskd", SourceUsername: "peer",
			RemotePath: `Music\03.flac`, FileName: "03.flac",
			Verdict: db.AcquiredFileDiscardedAudio, DecidedBy: "schall",
			Summary:  "The audio is a different recording.",
			Evidence: &db.AcquiredFileEvidence{Name: "03.flac", Method: ""},
		}},
	}
	handler := acquisitionHandler(targets)

	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet,
		"/api/v1/acquisition-targets/"+targetID.String(), nil))

	if response.Code != http.StatusOK {
		t.Fatalf("status = %d; body = %s", response.Code, response.Body)
	}
	var payload acquisitionTargetResponse
	if err := json.NewDecoder(response.Body).Decode(&payload); err != nil {
		t.Fatal(err)
	}
	if len(payload.Copies) != 1 || payload.Copies[0].Verdict != db.AcquiredFileDiscardedAudio {
		t.Fatalf("copies = %+v, want what was tried and what it came to", payload.Copies)
	}
	if payload.Copies[0].Evidence == nil {
		t.Error("a verdict without its evidence is one nobody can check")
	}
}

// Only a copy waiting for a decision is playable here. An accepted copy is a
// library file with its own path and a refused one was answered, so serving
// either would be a second route to files under a different name.
func TestACopyThatIsNotWaitingForADecisionIsNotPlayed(t *testing.T) {
	targets := &fakeAcquisitionTargets{
		copies: []db.AcquisitionTargetFileRow{{
			ID: uuid.New(), Verdict: db.AcquiredFileAccepted, FileName: "03.flac",
		}},
	}
	handler := NewAPI(&fakeStore{}, fakeDatabase{}, &fakeArtistSearcher{}, zerolog.Nop(),
		WithAcquisitionTargets(targets),
		WithDownloadInbox(t.TempDir()),
		WithPreviews(stubPreviews{}))

	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet,
		"/api/v1/review-queue/copies/"+uuid.New().String()+"/audio", nil))

	if response.Code != http.StatusConflict {
		t.Fatalf("status = %d, want 409; body = %s", response.Code, response.Body)
	}
}

// Without ffmpeg the copy cannot be heard, and that is said in a sentence rather
// than as a failure: every other way of deciding about it still works.
func TestACopyIsNotPlayableWithoutATranscoder(t *testing.T) {
	targets := &fakeAcquisitionTargets{
		copies: []db.AcquisitionTargetFileRow{{
			ID: uuid.New(), Verdict: db.AcquiredFileHeld,
			RemotePath: `Music\Anetha\03.flac`, FileName: "03.flac",
		}},
	}
	inbox := t.TempDir()
	if err := os.MkdirAll(filepath.Join(inbox, "Anetha"), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(inbox, "Anetha", "03.flac"), []byte("audio"), 0o600); err != nil {
		t.Fatal(err)
	}
	handler := NewAPI(&fakeStore{}, fakeDatabase{}, &fakeArtistSearcher{}, zerolog.Nop(),
		WithAcquisitionTargets(targets),
		WithDownloadInbox(inbox),
		WithPreviews(stubPreviews{err: preview.ErrUnavailable}))

	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet,
		"/api/v1/review-queue/copies/"+uuid.New().String()+"/audio", nil))

	if response.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503; body = %s", response.Code, response.Body)
	}
}

type stubPreviews struct {
	path string
	err  error
	// warmed is a pointer so a stub passed by value still records what it was
	// asked to encode ahead of time.
	warmed *[]string
}

func (stub stubPreviews) Playable(_ context.Context, _ string) (string, error) {
	return stub.path, stub.err
}

func (stub stubPreviews) Warm(sources []string) {
	if stub.warmed != nil {
		*stub.warmed = append(*stub.warmed, sources...)
	}
}

func (stubPreviews) ContentType() string { return "audio/mpeg" }

// Opening the queue is the earliest moment Schall knows which audio somebody is
// about to judge, and the queue is worked through rather than dipped into. The
// wait for a transcode was paid once per copy on a page where nearly every copy
// gets played, so reading the page starts the encodes.
func TestReadingTheReviewQueueStartsEncodingTheCopiesOnIt(t *testing.T) {
	inbox := t.TempDir()
	if err := os.MkdirAll(filepath.Join(inbox, "Anetha"), 0o750); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"03.flac", "04.flac"} {
		if err := os.WriteFile(filepath.Join(inbox, "Anetha", name), []byte("audio"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	targets := &fakeAcquisitionTargets{
		target: db.AcquisitionTargetRow{ID: uuid.New()},
		copies: []db.AcquisitionTargetFileRow{
			{
				ID: uuid.New(), Verdict: db.AcquiredFileHeld,
				RemotePath: `Music\Anetha\03.flac`, FileName: "03.flac",
			},
			{
				ID: uuid.New(), Verdict: db.AcquiredFileHeld,
				RemotePath: `Music\Anetha\04.flac`, FileName: "04.flac",
			},
		},
	}
	var warmed []string
	handler := NewAPI(&fakeStore{}, fakeDatabase{}, &fakeArtistSearcher{}, zerolog.Nop(),
		WithAcquisitionTargets(targets),
		WithDownloadInbox(inbox),
		WithPreviews(stubPreviews{warmed: &warmed}))

	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/api/v1/review-queue", nil))

	if response.Code != http.StatusOK {
		t.Fatalf("status = %d; body = %s", response.Code, response.Body)
	}
	if len(warmed) != 2 {
		t.Fatalf("warmed = %v, want both copies on the page", warmed)
	}
}

// A copy that was answered is not played, so encoding it ahead of time would be
// work for a question nobody will be asked.
func TestReadingTheReviewQueueEncodesOnlyTheCopiesStillWaiting(t *testing.T) {
	inbox := t.TempDir()
	if err := os.MkdirAll(filepath.Join(inbox, "Anetha"), 0o750); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"03.flac", "04.flac"} {
		if err := os.WriteFile(filepath.Join(inbox, "Anetha", name), []byte("audio"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	targets := &fakeAcquisitionTargets{
		target: db.AcquisitionTargetRow{ID: uuid.New()},
		copies: []db.AcquisitionTargetFileRow{
			{
				ID: uuid.New(), Verdict: db.AcquiredFileHeld,
				RemotePath: `Music\Anetha\03.flac`, FileName: "03.flac",
			},
			{
				ID: uuid.New(), Verdict: db.AcquiredFileDiscardedAudio,
				RemotePath: `Music\Anetha\04.flac`, FileName: "04.flac",
			},
		},
	}
	var warmed []string
	handler := NewAPI(&fakeStore{}, fakeDatabase{}, &fakeArtistSearcher{}, zerolog.Nop(),
		WithAcquisitionTargets(targets),
		WithDownloadInbox(inbox),
		WithPreviews(stubPreviews{warmed: &warmed}))

	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/api/v1/review-queue", nil))

	if len(warmed) != 1 || !strings.HasSuffix(warmed[0], "03.flac") {
		t.Fatalf("warmed = %v, want only the copy still waiting on a person", warmed)
	}
}

// trackOnDummy is one catalogue track a per-track press would act on.
func trackOnDummy() (uuid.UUID, *fakeStore) {
	trackID := uuid.New()
	return trackID, &fakeStore{
		trackForWanting: db.GetTrackForWantingRow{
			ID: trackID, AlbumID: uuid.New(), AlbumTitle: "Dummy",
			ArtistName: "Portishead", Title: "Mysterons",
			MusicbrainzRecordingID: uuid.NullUUID{UUID: uuid.New(), Valid: true},
		},
	}
}

// Wanting one track records one want, carrying the release the track sits on.
func TestWantingOneTrackRecordsItsWant(t *testing.T) {
	trackID, store := trackOnDummy()
	targets := &fakeAcquisitionTargets{
		target:  db.AcquisitionTargetRow{ID: uuid.New(), Status: "pending", Origin: "manual"},
		created: true,
	}

	response := httptest.NewRecorder()
	releaseHandler(store, targets).ServeHTTP(response, httptest.NewRequest(
		http.MethodPost, "/api/v1/tracks/"+trackID.String()+"/wanted", nil))

	if response.Code != http.StatusOK {
		t.Fatalf("status = %d; body = %s", response.Code, response.Body)
	}
	if targets.entry.Title != "Mysterons" || targets.entry.Album != "Dummy" ||
		!targets.entry.OriginAlbumID.Valid {
		t.Fatalf("entry = %+v, want the track with its release context", targets.entry)
	}
}

// Music the library already holds cannot be wanted; the press is a conflict
// rather than a want the loop would settle on sight.
func TestWantingAnOwnedTrackIsAConflict(t *testing.T) {
	trackID, store := trackOnDummy()
	store.trackForWanting.Owned = true
	targets := &fakeAcquisitionTargets{}

	response := httptest.NewRecorder()
	releaseHandler(store, targets).ServeHTTP(response, httptest.NewRequest(
		http.MethodPost, "/api/v1/tracks/"+trackID.String()+"/wanted", nil))

	if response.Code != http.StatusConflict {
		t.Fatalf("status = %d; body = %s", response.Code, response.Body)
	}
	if len(targets.entries) != 0 {
		t.Fatalf("entries = %v, want none recorded", targets.entries)
	}
}

// A track the catalogue holds no recording against cannot be keyed on, wanted
// or dismissed; both presses say so instead of making unrepeatable rows.
func TestActingOnATrackWithoutARecordingIsRefused(t *testing.T) {
	for _, action := range []string{"wanted", "not-wanted"} {
		trackID, store := trackOnDummy()
		store.trackForWanting.MusicbrainzRecordingID = uuid.NullUUID{}

		response := httptest.NewRecorder()
		releaseHandler(store, &fakeAcquisitionTargets{}).ServeHTTP(response,
			httptest.NewRequest(http.MethodPost,
				"/api/v1/tracks/"+trackID.String()+"/"+action, nil))

		if response.Code != http.StatusUnprocessableEntity {
			t.Fatalf("%s: status = %d; body = %s", action, response.Code, response.Body)
		}
	}
}

// Dismissing a track nobody ever asked for still records the decision: the
// want is created and stopped in the same press, so the arithmetic reads it as
// dismissed rather than missing.
func TestDismissingATrackRecordsAndStopsItsWant(t *testing.T) {
	trackID, store := trackOnDummy()
	targetID := uuid.New()
	targets := &fakeAcquisitionTargets{
		target:  db.AcquisitionTargetRow{ID: targetID, Status: "pending", Origin: "manual"},
		created: true,
	}

	response := httptest.NewRecorder()
	releaseHandler(store, targets).ServeHTTP(response, httptest.NewRequest(
		http.MethodPost, "/api/v1/tracks/"+trackID.String()+"/not-wanted", nil))

	if response.Code != http.StatusOK {
		t.Fatalf("status = %d; body = %s", response.Code, response.Body)
	}
	if targets.stopped != targetID {
		t.Fatalf("stopped = %s, want %s", targets.stopped, targetID)
	}
}

// Dismissing a track already dismissed is the same decision said twice, not a
// conflict and not a second stop.
func TestDismissingADismissedTrackIsTheSameDecision(t *testing.T) {
	trackID, store := trackOnDummy()
	targets := &fakeAcquisitionTargets{
		target: db.AcquisitionTargetRow{ID: uuid.New(), Status: "not_wanted", Origin: "manual"},
	}

	response := httptest.NewRecorder()
	releaseHandler(store, targets).ServeHTTP(response, httptest.NewRequest(
		http.MethodPost, "/api/v1/tracks/"+trackID.String()+"/not-wanted", nil))

	if response.Code != http.StatusOK {
		t.Fatalf("status = %d; body = %s", response.Code, response.Body)
	}
	if targets.stopped != uuid.Nil {
		t.Fatalf("stopped = %s, want no stop at all", targets.stopped)
	}
}

// Dismissing a release's remainder walks its missing tracks and accounts for
// every one of them: stopped, already decided, owned, or impossible to key.
func TestDismissingAReleaseRemainderWalksItsMissingTracks(t *testing.T) {
	albumID := uuid.New()
	openWant := uuid.New()
	rec := func() uuid.NullUUID { return uuid.NullUUID{UUID: uuid.New(), Valid: true} }
	store := &fakeStore{
		album: db.GetAlbumRow{ID: albumID, Title: "Dummy", ArtistName: "Portishead"},
		tracks: []db.ListAlbumTracksRow{
			{ID: uuid.New(), Title: "Owned", MusicbrainzRecordingID: rec(), Owned: true},
			{ID: uuid.New(), Title: "Being pursued", MusicbrainzRecordingID: rec(),
				WantID:     uuid.NullUUID{UUID: openWant, Valid: true},
				WantStatus: pgtype.Text{String: "searching", Valid: true}},
			{ID: uuid.New(), Title: "Never asked for", MusicbrainzRecordingID: rec()},
			{ID: uuid.New(), Title: "Already dismissed", MusicbrainzRecordingID: rec(),
				WantID:     uuid.NullUUID{UUID: uuid.New(), Valid: true},
				WantStatus: pgtype.Text{String: "not_wanted", Valid: true}},
			{ID: uuid.New(), Title: "No recording"},
		},
	}
	targets := &fakeAcquisitionTargets{
		target:  db.AcquisitionTargetRow{ID: uuid.New(), Status: "pending", Origin: "manual"},
		created: true,
	}

	response := httptest.NewRecorder()
	releaseHandler(store, targets).ServeHTTP(response, httptest.NewRequest(
		http.MethodPost, "/api/v1/albums/"+albumID.String()+"/not-wanted", nil))

	if response.Code != http.StatusOK {
		t.Fatalf("status = %d; body = %s", response.Code, response.Body)
	}
	var result dismissAlbumResponse
	if err := json.NewDecoder(response.Body).Decode(&result); err != nil {
		t.Fatal(err)
	}
	if result.Dismissed != 2 || result.AlreadyDismissed != 1 ||
		result.Owned != 1 || result.Unresolvable != 1 {
		t.Fatalf("result = %+v, want 2 dismissed, 1 already, 1 owned, 1 unresolvable", result)
	}
	// One want created for the track never asked for; the open pursuit stopped
	// by the id the page already knew.
	if len(targets.entries) != 1 || targets.entries[0].Title != "Never asked for" {
		t.Fatalf("entries = %+v, want one created for the unasked track", targets.entries)
	}
}

// The answer to a resolution question reaches the service as the recording the
// caller named.
func TestChoosingWhichRecordingAnEntryNamesRecordsIt(t *testing.T) {
	targetID, recordingID := uuid.New(), uuid.New()
	targets := &fakeAcquisitionTargets{
		target: db.AcquisitionTargetRow{
			ID: targetID, Status: "pending", Origin: "playlist",
			MusicBrainzRecordingID: uuid.NullUUID{UUID: recordingID, Valid: true},
		},
	}
	handler := acquisitionHandler(targets)

	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodPost,
		"/api/v1/acquisition-targets/"+targetID.String()+"/resolution",
		strings.NewReader(`{"recordingId":"`+recordingID.String()+`"}`)))

	if response.Code != http.StatusOK {
		t.Fatalf("status = %d; body = %s", response.Code, response.Body)
	}
	if targets.chosen != recordingID {
		t.Fatalf("chosen = %s, want %s", targets.chosen, recordingID)
	}
}

// Only the recordings a want offered can answer it. Anything else is a want for
// music nobody weighed the evidence for.
func TestChoosingARecordingThatWasNotOfferedIsAConflict(t *testing.T) {
	targets := &fakeAcquisitionTargets{chooseErr: db.ErrNotOfferedForReview}
	handler := acquisitionHandler(targets)

	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodPost,
		"/api/v1/acquisition-targets/"+uuid.New().String()+"/resolution",
		strings.NewReader(`{"recordingId":"`+uuid.New().String()+`"}`)))

	if response.Code != http.StatusConflict {
		t.Fatalf("status = %d; body = %s", response.Code, response.Body)
	}
}

// An answer that names nothing readable is not an answer.
func TestChoosingWithoutNamingARecordingIsRefused(t *testing.T) {
	targets := &fakeAcquisitionTargets{}
	handler := acquisitionHandler(targets)

	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodPost,
		"/api/v1/acquisition-targets/"+uuid.New().String()+"/resolution",
		strings.NewReader(`{"recordingId":"the second one"}`)))

	if response.Code != http.StatusUnprocessableEntity {
		t.Fatalf("status = %d; body = %s", response.Code, response.Body)
	}
	if targets.chosen != uuid.Nil {
		t.Fatalf("chosen = %s, want nothing recorded", targets.chosen)
	}
}

// The queue says which question each want is asking, so an interface renders the
// right one rather than inferring it from which list came back empty.
func TestTheReviewQueueNamesWhichQuestionEachWantIsAsking(t *testing.T) {
	targets := &fakeAcquisitionTargets{
		target: db.AcquisitionTargetRow{
			ID: uuid.New(), Status: "awaiting_review", Origin: "playlist",
			ReviewReason: pgtype.Text{String: "resolution", Valid: true},
		},
		candidates: []db.AcquisitionTargetCandidateRow{{
			MusicBrainzRecordingID: uuid.New(), ArtistName: "nthng",
			TrackTitle: "It Never Ends", Rank: 1,
			Agrees: []string{"artist", "title"}, Differs: []string{},
		}},
	}
	handler := acquisitionHandler(targets)

	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/api/v1/review-queue", nil))

	if response.Code != http.StatusOK {
		t.Fatalf("status = %d; body = %s", response.Code, response.Body)
	}
	var payload struct {
		Items []reviewItemResponse `json:"items"`
	}
	if err := json.NewDecoder(response.Body).Decode(&payload); err != nil {
		t.Fatal(err)
	}
	if len(payload.Items) != 1 {
		t.Fatalf("items = %d, want the one question", len(payload.Items))
	}
	if payload.Items[0].Kind != reviewKindResolution {
		t.Fatalf("kind = %q, want %q", payload.Items[0].Kind, reviewKindResolution)
	}
	if len(payload.Items[0].Candidates) != 1 {
		t.Fatalf("candidates = %+v, want the answers the question offered",
			payload.Items[0].Candidates)
	}
}

// A want that went unfound after a copy had already been fetched for it is asked
// about the copy. It reached that state by being resolved once, fetched for, and
// then having its recording taken back, so there is a file on record somebody can
// decide about — and deciding it settles the want.
func TestAWantHoldingACopyIsAskedAboutTheCopyRatherThanMusicBrainz(t *testing.T) {
	targets := &fakeAcquisitionTargets{
		target: db.AcquisitionTargetRow{
			ID: uuid.New(), Status: "unresolved", Origin: "playlist",
			EntryArtist: "Porter Robinson", EntryTitle: "WannaCry",
			Summary: "No recording MusicBrainz knows of matches this entry yet." +
				" It will be asked about again.",
		},
		copies: []db.AcquisitionTargetFileRow{{
			ID: uuid.New(), Provider: "slskd", SourceUsername: "peer",
			FileName: "03.flac", Verdict: db.AcquiredFileHeld, DecidedBy: "schall",
		}},
	}
	handler := acquisitionHandler(targets)

	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/api/v1/review-queue", nil))

	var payload struct {
		Items []reviewItemResponse `json:"items"`
	}
	if err := json.NewDecoder(response.Body).Decode(&payload); err != nil {
		t.Fatal(err)
	}
	if payload.Items[0].Kind != reviewKindCopies {
		t.Fatalf("kind = %q, want %q", payload.Items[0].Kind, reviewKindCopies)
	}
}

// A want whose copy is on the disc and whose file the library files under a
// different recording is its own question. Nothing may settle it and nothing may
// fetch for it, so it is asked about — and it carries the copy that stopped it,
// because a question with no evidence under it is a sentence nobody can act on.
func TestAWantStoppedByADisagreementIsItsOwnQuestion(t *testing.T) {
	wantedRecordingID, fileRecordingID, fileID := uuid.New(), uuid.New(), uuid.New()
	targets := &fakeAcquisitionTargets{
		target: db.AcquisitionTargetRow{
			ID: uuid.New(), Status: "pending", Origin: "playlist",
			EntryArtist: "JACKBOYS feat. Young Thug", EntryTitle: "OUT WEST",
			EntryAlbum:             "NYE 2022",
			MusicBrainzRecordingID: uuid.NullUUID{UUID: wantedRecordingID, Valid: true},
			Summary: "A copy arrived and was proven, but your library calls that" +
				" file a different recording.",
		},
		copies: []db.AcquisitionTargetFileRow{{
			ID: uuid.New(), Provider: "slskd", SourceUsername: "peer",
			FileName: "05.flac", Verdict: db.AcquiredFileAccepted, DecidedBy: "schall",
			LibraryFileID: uuid.NullUUID{UUID: fileID, Valid: true},
		}},
	}
	handler := NewAPI(&fakeStore{}, fakeDatabase{}, &fakeArtistSearcher{}, zerolog.Nop(),
		WithAcquisitionTargets(targets), WithIdentityResolver(&fakeIdentityResolver{
			resolution: identity.FileResolution{
				Identity: &identity.StoredIdentity{Identity: identity.Identity{
					RecordingID: fileRecordingID, TrackTitle: "OUT WEST",
					ArtistName: "JACKBOYS feat. Young Thug", ReleaseTitle: "JACKBOYS",
				}},
			},
		}))

	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/api/v1/review-queue", nil))

	var payload struct {
		Items []reviewItemResponse `json:"items"`
	}
	if err := json.NewDecoder(response.Body).Decode(&payload); err != nil {
		t.Fatal(err)
	}
	if payload.Items[0].Kind != reviewKindStopped {
		t.Fatalf("kind = %q, want %q", payload.Items[0].Kind, reviewKindStopped)
	}
	// The file is named, because the way on is to say what that file is.
	if len(payload.Items[0].Copies) != 1 ||
		payload.Items[0].Copies[0].LibraryFileID == nil ||
		*payload.Items[0].Copies[0].LibraryFileID != fileID {
		t.Fatalf("copies = %+v, want the imported copy naming its library file",
			payload.Items[0].Copies)
	}
	if payload.Items[0].Target.RecordingID == nil ||
		*payload.Items[0].Target.RecordingID != wantedRecordingID {
		t.Fatalf("wanted recording = %v, want %s", payload.Items[0].Target.RecordingID, wantedRecordingID)
	}
	if payload.Items[0].FileRecordingID == nil ||
		*payload.Items[0].FileRecordingID != fileRecordingID ||
		payload.Items[0].FileTitle != "OUT WEST" ||
		payload.Items[0].FileArtist != "JACKBOYS feat. Young Thug" ||
		payload.Items[0].FileReleaseTitle != "JACKBOYS" {
		t.Fatalf("file recording = %+v, want the identity assigned by the library", payload.Items[0])
	}
}

// A want can hold a copy and still not know which recording it wants: one
// fetched for a resolution that was later rejected as the wrong recording. The
// answerable question is asked first — judging a file against a recording nobody
// has settled on is not a question anybody can answer.
func TestAWantThatHoldsACopyAndStillAsksWhichRecordingAsksThatFirst(t *testing.T) {
	targets := &fakeAcquisitionTargets{
		target: db.AcquisitionTargetRow{
			ID: uuid.New(), Status: "awaiting_review", Origin: "playlist",
			ReviewReason: pgtype.Text{String: "resolution", Valid: true},
		},
		copies: []db.AcquisitionTargetFileRow{{
			ID: uuid.New(), Provider: "slskd", SourceUsername: "peer",
			FileName: "03.flac", Verdict: db.AcquiredFileHeld, DecidedBy: "schall",
		}},
		candidates: []db.AcquisitionTargetCandidateRow{{
			MusicBrainzRecordingID: uuid.New(), ArtistName: "nthng",
			TrackTitle: "It Never Ends", Rank: 1,
			Agrees: []string{"artist"}, Differs: []string{},
		}},
	}
	handler := acquisitionHandler(targets)

	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/api/v1/review-queue", nil))

	var payload struct {
		Items []reviewItemResponse `json:"items"`
	}
	if err := json.NewDecoder(response.Body).Decode(&payload); err != nil {
		t.Fatal(err)
	}
	if payload.Items[0].Kind != reviewKindResolution {
		t.Fatalf("kind = %q, want the question that can be answered (%q)",
			payload.Items[0].Kind, reviewKindResolution)
	}
	// The copy is not hidden from whatever is reading: it is still held, and it
	// is offered again as soon as the want knows what it is looking for.
	if len(payload.Items[0].Copies) != 1 {
		t.Fatalf("copies = %+v, want the held copy still reported", payload.Items[0].Copies)
	}
}

// Every acquisition route needs the component that holds wanted recordings, and
// says so rather than reporting that nothing is wanted.
func TestEveryAcquisitionRouteReportsBeingUnavailable(t *testing.T) {
	handler := NewAPI(&fakeStore{}, fakeDatabase{}, &fakeArtistSearcher{}, zerolog.Nop())
	targetID, trackID := uuid.NewString(), uuid.NewString()
	for _, request := range []*http.Request{
		httptest.NewRequest(http.MethodPost, "/api/v1/acquisition-targets", strings.NewReader(`{}`)),
		httptest.NewRequest(http.MethodGet, "/api/v1/acquisition-targets/"+targetID, nil),
		httptest.NewRequest(http.MethodPost, "/api/v1/acquisition-targets/"+targetID+"/not-wanted", nil),
		httptest.NewRequest(http.MethodDelete, "/api/v1/acquisition-targets/"+targetID+"/not-wanted", nil),
		httptest.NewRequest(http.MethodPost, "/api/v1/acquisition-targets/"+targetID+"/wrong-recording", nil),
		httptest.NewRequest(http.MethodPost, "/api/v1/acquisition-targets/"+targetID+"/resolution", strings.NewReader(`{}`)),
		httptest.NewRequest(http.MethodPost, "/api/v1/acquisition-targets/"+targetID+"/none-of-these", nil),
		httptest.NewRequest(http.MethodGet, "/api/v1/review-queue", nil),
		httptest.NewRequest(http.MethodGet, "/api/v1/review-queue/copies/"+uuid.NewString()+"/audio", nil),
		httptest.NewRequest(http.MethodPost, "/api/v1/review-queue/copies/"+uuid.NewString()+"/accept", nil),
		httptest.NewRequest(http.MethodPost, "/api/v1/albums/"+uuid.NewString()+"/wanted", nil),
		httptest.NewRequest(http.MethodPost, "/api/v1/albums/"+uuid.NewString()+"/not-wanted", nil),
		httptest.NewRequest(http.MethodPost, "/api/v1/artists/"+uuid.NewString()+"/wanted", nil),
		httptest.NewRequest(http.MethodPost, "/api/v1/tracks/"+trackID+"/wanted", nil),
		httptest.NewRequest(http.MethodPost, "/api/v1/tracks/"+trackID+"/not-wanted", nil),
	} {
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, request)
		if response.Code != http.StatusServiceUnavailable {
			t.Errorf("%s %s: status = %d, want 503", request.Method, request.URL.Path, response.Code)
		}
	}
}

// An id that is not a UUID names no want, and the routes say so without asking
// the store about it.
func TestAMalformedTargetIDNamesNoWant(t *testing.T) {
	handler := acquisitionHandler(&fakeAcquisitionTargets{})
	for _, request := range []*http.Request{
		httptest.NewRequest(http.MethodGet, "/api/v1/acquisition-targets/not-a-uuid", nil),
		httptest.NewRequest(http.MethodPost, "/api/v1/acquisition-targets/not-a-uuid/not-wanted", nil),
		httptest.NewRequest(http.MethodDelete, "/api/v1/acquisition-targets/not-a-uuid/not-wanted", nil),
		httptest.NewRequest(http.MethodPost, "/api/v1/acquisition-targets/not-a-uuid/wrong-recording", nil),
		httptest.NewRequest(http.MethodPost, "/api/v1/acquisition-targets/not-a-uuid/resolution", strings.NewReader(`{}`)),
		httptest.NewRequest(http.MethodPost, "/api/v1/acquisition-targets/not-a-uuid/none-of-these", nil),
	} {
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, request)
		if response.Code != http.StatusNotFound {
			t.Errorf("%s %s: status = %d, want 404", request.Method, request.URL.Path, response.Code)
		}
	}
}

func TestAWantRejectsABodyThatIsNotJSON(t *testing.T) {
	handler := acquisitionHandler(&fakeAcquisitionTargets{})

	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodPost, "/api/v1/acquisition-targets",
		strings.NewReader(`not json`)))
	if response.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", response.Code)
	}
}

// A negative duration is not a track anybody has, and taking it would put a
// nonsense number in front of every later comparison.
func TestAWantWithANegativeDurationIsRefused(t *testing.T) {
	handler := acquisitionHandler(&fakeAcquisitionTargets{})

	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodPost, "/api/v1/acquisition-targets",
		strings.NewReader(`{"title":"Roads","durationMs":-1}`)))
	if response.Code != http.StatusUnprocessableEntity {
		t.Fatalf("status = %d, want 422", response.Code)
	}
	if !strings.Contains(response.Body.String(), "durationMs") {
		t.Fatalf("body = %s, want the field named", response.Body)
	}
}

// The origins are a closed set: a want from somewhere nothing knows about is
// refused rather than filed under a source that does not exist.
func TestAWantFromAnUnknownOriginIsRefused(t *testing.T) {
	handler := acquisitionHandler(&fakeAcquisitionTargets{err: acquisition.ErrUnknownOrigin})

	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodPost, "/api/v1/acquisition-targets",
		strings.NewReader(`{"origin":"telepathy","title":"Roads"}`)))
	if response.Code != http.StatusUnprocessableEntity {
		t.Fatalf("status = %d, want 422", response.Code)
	}
}

func TestRecordingAWantReportsAStoreThatFailed(t *testing.T) {
	handler := acquisitionHandler(&fakeAcquisitionTargets{
		err: errors.New("the database is unreachable"),
	})

	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodPost, "/api/v1/acquisition-targets",
		strings.NewReader(`{"title":"Roads"}`)))
	if response.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500", response.Code)
	}
}

// The list is filtered by a closed set of states and bounded numbers. Anything
// else is refused rather than answered with a different list.
func TestListingWantsRefusesFiltersOutsideWhatItAnswers(t *testing.T) {
	handler := acquisitionHandler(&fakeAcquisitionTargets{})
	for _, query := range []string{
		"?status=probably", "?limit=0", "?limit=201", "?limit=many",
		"?offset=-1", "?offset=soon",
	} {
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, httptest.NewRequest(
			http.MethodGet, "/api/v1/acquisition-targets"+query, nil))
		if response.Code != http.StatusUnprocessableEntity {
			t.Errorf("%s: status = %d, want 422", query, response.Code)
		}
	}
}

// The list answers with the page it was asked for, so an interface can page
// through the wants rather than reading the whole of them.
func TestWantsAreListedForThePageThatWasAskedFor(t *testing.T) {
	targets := &fakeAcquisitionTargets{
		target: db.AcquisitionTargetRow{ID: uuid.New(), Status: "pending", Origin: "manual"},
	}
	handler := acquisitionHandler(targets)

	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet,
		"/api/v1/acquisition-targets?status=pending&limit=5&offset=10", nil))
	if response.Code != http.StatusOK {
		t.Fatalf("status = %d; body = %s", response.Code, response.Body)
	}
	var payload struct {
		Items  []acquisitionTargetResponse `json:"items"`
		Total  int64                       `json:"total"`
		Limit  int32                       `json:"limit"`
		Offset int32                       `json:"offset"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &payload); err != nil {
		t.Fatal(err)
	}
	if len(payload.Items) != 1 || payload.Limit != 5 || payload.Offset != 10 {
		t.Fatalf("payload = %+v", payload)
	}
}

// A screen that shows the wants being looked for asks for three states in one
// request, because to the person who asked for the music they are one pile.
func TestWantsCanBeAskedForInSeveralStatesAtOnce(t *testing.T) {
	targets := &fakeAcquisitionTargets{
		target: db.AcquisitionTargetRow{ID: uuid.New(), Status: "pending", Origin: "manual"},
	}
	handler := acquisitionHandler(targets)

	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet,
		"/api/v1/acquisition-targets?status=unresolved,pending,searching", nil))
	if response.Code != http.StatusOK {
		t.Fatalf("status = %d; body = %s", response.Code, response.Body)
	}
	want := []string{"unresolved", "pending", "searching"}
	if !slices.Equal(targets.listed.Statuses, want) {
		t.Fatalf("statuses = %v, want %v", targets.listed.Statuses, want)
	}
}

// One unknown name in a list of states is a typo, not a narrower list, and a
// list narrowed by a typo would read as "nothing is being looked for".
func TestAnUnknownStateInAListOfStatesIsRefused(t *testing.T) {
	targets := &fakeAcquisitionTargets{
		target: db.AcquisitionTargetRow{ID: uuid.New(), Status: "pending", Origin: "manual"},
	}
	handler := acquisitionHandler(targets)

	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet,
		"/api/v1/acquisition-targets?status=pending,looking", nil))
	if response.Code != http.StatusUnprocessableEntity {
		t.Fatalf("status = %d; body = %s", response.Code, response.Body)
	}
	if targets.listed.Statuses != nil {
		t.Fatalf("statuses = %v, want the store never asked", targets.listed.Statuses)
	}
}

func TestListingWantsReportsAStoreThatFailed(t *testing.T) {
	handler := acquisitionHandler(&fakeAcquisitionTargets{
		err: errors.New("the database is unreachable"),
	})

	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/api/v1/acquisition-targets", nil))
	if response.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500", response.Code)
	}
}

func TestReadingAWantThatIsNotThereIsNotFound(t *testing.T) {
	handler := acquisitionHandler(&fakeAcquisitionTargets{err: pgx.ErrNoRows})

	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet,
		"/api/v1/acquisition-targets/"+uuid.NewString(), nil))
	if response.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", response.Code)
	}
}

func TestReadingAWantReportsAStoreThatFailed(t *testing.T) {
	handler := acquisitionHandler(&fakeAcquisitionTargets{
		err: errors.New("the database is unreachable"),
	})

	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet,
		"/api/v1/acquisition-targets/"+uuid.NewString(), nil))
	if response.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500", response.Code)
	}
}

func TestStoppingAWantReportsAStoreThatFailed(t *testing.T) {
	handler := acquisitionHandler(&fakeAcquisitionTargets{
		err: errors.New("the database is unreachable"),
	})

	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodPost,
		"/api/v1/acquisition-targets/"+uuid.NewString()+"/not-wanted", nil))
	if response.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500", response.Code)
	}
}

func TestWantingAStoppedTargetAgainReportsAStoreThatFailed(t *testing.T) {
	handler := acquisitionHandler(&fakeAcquisitionTargets{
		err: errors.New("the database is unreachable"),
	})

	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodDelete,
		"/api/v1/acquisition-targets/"+uuid.NewString()+"/not-wanted", nil))
	if response.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500", response.Code)
	}
}

// The rejected recording is remembered so the lookup that follows cannot return
// it again, which is the whole of what rejecting a resolution buys.
func TestRejectingAResolutionRecordsIt(t *testing.T) {
	targetID := uuid.New()
	targets := &fakeAcquisitionTargets{
		target: db.AcquisitionTargetRow{ID: targetID, Status: "unresolved", Origin: "playlist"},
	}
	handler := acquisitionHandler(targets)

	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodPost,
		"/api/v1/acquisition-targets/"+targetID.String()+"/wrong-recording", nil))
	if response.Code != http.StatusOK {
		t.Fatalf("status = %d; body = %s", response.Code, response.Body)
	}
	if targets.rejected != targetID {
		t.Fatalf("rejected = %s, want %s", targets.rejected, targetID)
	}
}

// There is nothing to remember and nothing to re-open for a want that was never
// resolved, so saying so is a conflict rather than a silent success.
func TestRejectingAResolutionThatWasNeverMadeIsAConflict(t *testing.T) {
	handler := acquisitionHandler(&fakeAcquisitionTargets{err: db.ErrNothingToReject})

	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodPost,
		"/api/v1/acquisition-targets/"+uuid.NewString()+"/wrong-recording", nil))
	if response.Code != http.StatusConflict {
		t.Fatalf("status = %d, want 409", response.Code)
	}
}

func TestRejectingAResolutionReportsAStoreThatFailed(t *testing.T) {
	handler := acquisitionHandler(&fakeAcquisitionTargets{
		err: errors.New("the database is unreachable"),
	})

	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodPost,
		"/api/v1/acquisition-targets/"+uuid.NewString()+"/wrong-recording", nil))
	if response.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500", response.Code)
	}
}

func TestChoosingAResolutionRejectsABodyThatIsNotJSON(t *testing.T) {
	handler := acquisitionHandler(&fakeAcquisitionTargets{})

	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodPost,
		"/api/v1/acquisition-targets/"+uuid.NewString()+"/resolution",
		strings.NewReader(`not json`)))
	if response.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", response.Code)
	}
}

// An answer is never re-asked, so a want something else already settled has no
// question left to answer.
func TestAnsweringAWantThatIsNotWaitingIsAConflict(t *testing.T) {
	handler := acquisitionHandler(&fakeAcquisitionTargets{chooseErr: pgx.ErrNoRows})

	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodPost,
		"/api/v1/acquisition-targets/"+uuid.NewString()+"/resolution",
		strings.NewReader(`{"recordingId":"`+uuid.NewString()+`"}`)))
	if response.Code != http.StatusConflict {
		t.Fatalf("status = %d, want 409", response.Code)
	}
}

func TestChoosingAResolutionReportsAStoreThatFailed(t *testing.T) {
	handler := acquisitionHandler(&fakeAcquisitionTargets{
		chooseErr: errors.New("the database is unreachable"),
	})

	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodPost,
		"/api/v1/acquisition-targets/"+uuid.NewString()+"/resolution",
		strings.NewReader(`{"recordingId":"`+uuid.NewString()+`"}`)))
	if response.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500", response.Code)
	}
}

// Accepting a copy records the decision and leaves the import to the worker:
// copying a file into the library is not work to put behind an HTTP timeout.
func TestAcceptingACopyRecordsTheDecision(t *testing.T) {
	copyID := uuid.New()
	targets := &fakeAcquisitionTargets{}
	handler := acquisitionHandler(targets)

	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodPost,
		"/api/v1/review-queue/copies/"+copyID.String()+"/accept", nil))
	if response.Code != http.StatusAccepted {
		t.Fatalf("status = %d; body = %s", response.Code, response.Body)
	}
	if targets.accepted != copyID {
		t.Fatalf("accepted = %s, want %s", targets.accepted, copyID)
	}
}

func TestAcceptingACopyThatIsNotWaitingIsAConflict(t *testing.T) {
	handler := acquisitionHandler(&fakeAcquisitionTargets{err: db.ErrNotWaitingForADecision})

	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodPost,
		"/api/v1/review-queue/copies/"+uuid.NewString()+"/accept", nil))
	if response.Code != http.StatusConflict {
		t.Fatalf("status = %d, want 409", response.Code)
	}
}

func TestAcceptingACopyReportsAStoreThatFailed(t *testing.T) {
	handler := acquisitionHandler(&fakeAcquisitionTargets{
		err: errors.New("the database is unreachable"),
	})

	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodPost,
		"/api/v1/review-queue/copies/"+uuid.NewString()+"/accept", nil))
	if response.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500", response.Code)
	}
}

func TestAMalformedCopyIDNamesNoCopy(t *testing.T) {
	handler := acquisitionHandler(&fakeAcquisitionTargets{})

	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodPost,
		"/api/v1/review-queue/copies/not-a-uuid/accept", nil))
	if response.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", response.Code)
	}
}

// None of these means keep looking, and each refusal is permanent: those files
// are never offered for this want again.
func TestRefusingEveryCopyRecordsTheDecision(t *testing.T) {
	targetID := uuid.New()
	targets := &fakeAcquisitionTargets{}
	handler := acquisitionHandler(targets)

	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodPost,
		"/api/v1/acquisition-targets/"+targetID.String()+"/none-of-these", nil))
	if response.Code != http.StatusOK {
		t.Fatalf("status = %d; body = %s", response.Code, response.Body)
	}
	if targets.refused != targetID {
		t.Fatalf("refused = %s, want %s", targets.refused, targetID)
	}
}

func TestRefusingCopiesWhenNoneAreHeldIsAConflict(t *testing.T) {
	handler := acquisitionHandler(&fakeAcquisitionTargets{err: db.ErrNotWaitingForADecision})

	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodPost,
		"/api/v1/acquisition-targets/"+uuid.NewString()+"/none-of-these", nil))
	if response.Code != http.StatusConflict {
		t.Fatalf("status = %d, want 409", response.Code)
	}
}

func TestRefusingCopiesReportsAStoreThatFailed(t *testing.T) {
	handler := acquisitionHandler(&fakeAcquisitionTargets{
		err: errors.New("the database is unreachable"),
	})

	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodPost,
		"/api/v1/acquisition-targets/"+uuid.NewString()+"/none-of-these", nil))
	if response.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500", response.Code)
	}
}

// The queue is paged like every other list, and what falls outside the bounds is
// refused rather than clamped.
func TestTheReviewQueueRefusesPagingOutsideWhatItAnswers(t *testing.T) {
	handler := acquisitionHandler(&fakeAcquisitionTargets{})
	for _, query := range []string{
		"?limit=0", "?limit=101", "?limit=many", "?offset=-1", "?offset=soon",
	} {
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, httptest.NewRequest(
			http.MethodGet, "/api/v1/review-queue"+query, nil))
		if response.Code != http.StatusUnprocessableEntity {
			t.Errorf("%s: status = %d, want 422", query, response.Code)
		}
	}
}

func TestReadingTheReviewQueueReportsAStoreThatFailed(t *testing.T) {
	handler := acquisitionHandler(&fakeAcquisitionTargets{
		err: errors.New("the database is unreachable"),
	})

	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/api/v1/review-queue", nil))
	if response.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500", response.Code)
	}
}

// Without an inbox there is nothing to play from, and that is said in a sentence
// rather than as a missing copy.
func TestPlayingACopyIsUnavailableWithoutAnInbox(t *testing.T) {
	handler := acquisitionHandler(&fakeAcquisitionTargets{})

	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet,
		"/api/v1/review-queue/copies/"+uuid.NewString()+"/audio", nil))
	if response.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503", response.Code)
	}
}

func TestPlayingACopyThatIsNotThereIsNotFound(t *testing.T) {
	handler := NewAPI(&fakeStore{}, fakeDatabase{}, &fakeArtistSearcher{}, zerolog.Nop(),
		WithAcquisitionTargets(&fakeAcquisitionTargets{}),
		WithDownloadInbox(t.TempDir()), WithPreviews(stubPreviews{}))

	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet,
		"/api/v1/review-queue/copies/"+uuid.NewString()+"/audio", nil))
	if response.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", response.Code)
	}
}

func TestPlayingACopyReportsAStoreThatFailed(t *testing.T) {
	targets := &fakeAcquisitionTargets{
		copies: []db.AcquisitionTargetFileRow{{ID: uuid.New(), Verdict: db.AcquiredFileHeld}},
		err:    errors.New("the database is unreachable"),
	}
	handler := NewAPI(&fakeStore{}, fakeDatabase{}, &fakeArtistSearcher{}, zerolog.Nop(),
		WithAcquisitionTargets(targets),
		WithDownloadInbox(t.TempDir()), WithPreviews(stubPreviews{}))

	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet,
		"/api/v1/review-queue/copies/"+uuid.NewString()+"/audio", nil))
	if response.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500", response.Code)
	}
}

// A copy whose bytes have gone since it was held cannot be played, and the
// reason has to reach whoever is reviewing rather than becoming a server error.
func TestACopyWhoseFileHasGoneCannotBePlayed(t *testing.T) {
	targets := &fakeAcquisitionTargets{
		copies: []db.AcquisitionTargetFileRow{{
			ID: uuid.New(), Verdict: db.AcquiredFileHeld,
			RemotePath: `Music\Anetha\03.flac`, FileName: "03.flac",
		}},
	}
	handler := NewAPI(&fakeStore{}, fakeDatabase{}, &fakeArtistSearcher{}, zerolog.Nop(),
		WithAcquisitionTargets(targets),
		WithDownloadInbox(t.TempDir()), WithPreviews(stubPreviews{}))

	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet,
		"/api/v1/review-queue/copies/"+uuid.NewString()+"/audio", nil))
	if response.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404; body = %s", response.Code, response.Body)
	}
}

// A copy the peer gave no readable name for has nothing to locate under the
// inbox, and guessing at one is how a path leaves the folder it belongs in.
func TestACopyWithNoReadableNameCannotBePlayed(t *testing.T) {
	targets := &fakeAcquisitionTargets{
		copies: []db.AcquisitionTargetFileRow{{
			ID: uuid.New(), Verdict: db.AcquiredFileHeld, RemotePath: "", FileName: "",
		}},
	}
	handler := NewAPI(&fakeStore{}, fakeDatabase{}, &fakeArtistSearcher{}, zerolog.Nop(),
		WithAcquisitionTargets(targets),
		WithDownloadInbox(t.TempDir()), WithPreviews(stubPreviews{}))

	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet,
		"/api/v1/review-queue/copies/"+uuid.NewString()+"/audio", nil))
	if response.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404; body = %s", response.Code, response.Body)
	}
}

// A held copy is served as a whole track with range support, so somebody who is
// unsure can move around inside it rather than hear a fixed window.
func TestAHeldCopyIsPlayedAsAWholeTrack(t *testing.T) {
	inbox := t.TempDir()
	if err := os.MkdirAll(filepath.Join(inbox, "Anetha"), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(inbox, "Anetha", "03.flac"), []byte("audio"), 0o600); err != nil {
		t.Fatal(err)
	}
	transcoded := filepath.Join(t.TempDir(), "03.mp3")
	if err := os.WriteFile(transcoded, []byte("transcoded audio"), 0o600); err != nil {
		t.Fatal(err)
	}
	targets := &fakeAcquisitionTargets{
		copies: []db.AcquisitionTargetFileRow{{
			ID: uuid.New(), Verdict: db.AcquiredFileHeld,
			RemotePath: `Music\Anetha\03.flac`, FileName: "03.flac",
		}},
	}
	handler := NewAPI(&fakeStore{}, fakeDatabase{}, &fakeArtistSearcher{}, zerolog.Nop(),
		WithAcquisitionTargets(targets),
		WithDownloadInbox(inbox), WithPreviews(stubPreviews{path: transcoded}))

	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet,
		"/api/v1/review-queue/copies/"+uuid.NewString()+"/audio", nil))
	if response.Code != http.StatusOK || response.Body.String() != "transcoded audio" {
		t.Fatalf("status = %d body = %q", response.Code, response.Body)
	}
	if got := response.Header().Get("Accept-Ranges"); got != "bytes" {
		t.Errorf("accept-ranges = %q, want a scrubbable stream", got)
	}
	if got := response.Header().Get("Content-Type"); got != "audio/mpeg" {
		t.Errorf("content type = %q", got)
	}
}

// The phone app seeks by asking for a slice of the track, and the answer has to
// carry only that slice and say so with a 206 and a Content-Range.
func TestAHeldCopyAnswersARangeRequest(t *testing.T) {
	inbox := t.TempDir()
	if err := os.MkdirAll(filepath.Join(inbox, "Anetha"), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(inbox, "Anetha", "03.flac"), []byte("audio"), 0o600); err != nil {
		t.Fatal(err)
	}
	transcoded := filepath.Join(t.TempDir(), "03.mp3")
	if err := os.WriteFile(transcoded, []byte("transcoded audio"), 0o600); err != nil {
		t.Fatal(err)
	}
	targets := &fakeAcquisitionTargets{
		copies: []db.AcquisitionTargetFileRow{{
			ID: uuid.New(), Verdict: db.AcquiredFileHeld,
			RemotePath: `Music\Anetha\03.flac`, FileName: "03.flac",
		}},
	}
	handler := NewAPI(&fakeStore{}, fakeDatabase{}, &fakeArtistSearcher{}, zerolog.Nop(),
		WithAcquisitionTargets(targets),
		WithDownloadInbox(inbox), WithPreviews(stubPreviews{path: transcoded}))

	request := httptest.NewRequest(http.MethodGet,
		"/api/v1/review-queue/copies/"+uuid.NewString()+"/audio", nil)
	request.Header.Set("Range", "bytes=0-9")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)

	if response.Code != http.StatusPartialContent {
		t.Fatalf("status = %d, want 206", response.Code)
	}
	if response.Body.String() != "transcoded" {
		t.Fatalf("body = %q, want the first ten bytes", response.Body.String())
	}
	if got := response.Header().Get("Content-Range"); got != "bytes 0-9/16" {
		t.Errorf("content-range = %q", got)
	}
}

// A phone that already has this preview sends back the ETag it was given, and
// gets a 304 rather than the bytes it already holds.
func TestAHeldCopyAnswersIfNoneMatchWithNotModified(t *testing.T) {
	inbox := t.TempDir()
	if err := os.MkdirAll(filepath.Join(inbox, "Anetha"), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(inbox, "Anetha", "03.flac"), []byte("audio"), 0o600); err != nil {
		t.Fatal(err)
	}
	transcoded := filepath.Join(t.TempDir(), "03.mp3")
	if err := os.WriteFile(transcoded, []byte("transcoded audio"), 0o600); err != nil {
		t.Fatal(err)
	}
	targets := &fakeAcquisitionTargets{
		copies: []db.AcquisitionTargetFileRow{{
			ID: uuid.New(), Verdict: db.AcquiredFileHeld,
			RemotePath: `Music\Anetha\03.flac`, FileName: "03.flac",
		}},
	}
	handler := NewAPI(&fakeStore{}, fakeDatabase{}, &fakeArtistSearcher{}, zerolog.Nop(),
		WithAcquisitionTargets(targets),
		WithDownloadInbox(inbox), WithPreviews(stubPreviews{path: transcoded}))

	first := httptest.NewRecorder()
	handler.ServeHTTP(first, httptest.NewRequest(http.MethodGet,
		"/api/v1/review-queue/copies/"+uuid.NewString()+"/audio", nil))
	etag := first.Header().Get("ETag")
	if etag == "" {
		t.Fatal("no ETag on the first response")
	}

	request := httptest.NewRequest(http.MethodGet,
		"/api/v1/review-queue/copies/"+uuid.NewString()+"/audio", nil)
	request.Header.Set("If-None-Match", etag)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)

	if response.Code != http.StatusNotModified {
		t.Fatalf("status = %d, want 304", response.Code)
	}
	if response.Body.Len() != 0 {
		t.Fatalf("body = %q, want empty on a 304", response.Body.String())
	}
}

// A transcode that could not be opened is this installation's fault rather than
// a copy that is not there, and the two must not read the same way.
func TestATranscodeThatCannotBeOpenedIsAServerError(t *testing.T) {
	inbox := t.TempDir()
	if err := os.MkdirAll(filepath.Join(inbox, "Anetha"), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(inbox, "Anetha", "03.flac"), []byte("audio"), 0o600); err != nil {
		t.Fatal(err)
	}
	targets := &fakeAcquisitionTargets{
		copies: []db.AcquisitionTargetFileRow{{
			ID: uuid.New(), Verdict: db.AcquiredFileHeld,
			RemotePath: `Music\Anetha\03.flac`, FileName: "03.flac",
		}},
	}
	handler := NewAPI(&fakeStore{}, fakeDatabase{}, &fakeArtistSearcher{}, zerolog.Nop(),
		WithAcquisitionTargets(targets),
		WithDownloadInbox(inbox),
		WithPreviews(stubPreviews{path: filepath.Join(t.TempDir(), "gone.mp3")}))

	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet,
		"/api/v1/review-queue/copies/"+uuid.NewString()+"/audio", nil))
	if response.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500", response.Code)
	}
}

// A copy whose size the peer reported carries it, because that is one of the
// few things about an unidentifiable file anybody can weigh.
func TestACopyReportsTheSizeThePeerGaveForIt(t *testing.T) {
	targetID := uuid.New()
	targets := &fakeAcquisitionTargets{
		target: db.AcquisitionTargetRow{ID: targetID, Status: "awaiting_review", Origin: "manual"},
		copies: []db.AcquisitionTargetFileRow{{
			ID: uuid.New(), AcquisitionTargetID: targetID, Provider: "slskd",
			FileName: "03.flac", Verdict: db.AcquiredFileHeld,
			SizeBytes: pgtype.Int8{Int64: 41_000_000, Valid: true},
		}},
	}
	handler := acquisitionHandler(targets)

	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet,
		"/api/v1/acquisition-targets/"+targetID.String(), nil))
	if response.Code != http.StatusOK {
		t.Fatalf("status = %d; body = %s", response.Code, response.Body)
	}
	var payload acquisitionTargetResponse
	if err := json.Unmarshal(response.Body.Bytes(), &payload); err != nil {
		t.Fatal(err)
	}
	if len(payload.Copies) != 1 || payload.Copies[0].SizeBytes == nil ||
		*payload.Copies[0].SizeBytes != 41_000_000 {
		t.Fatalf("copies = %+v", payload.Copies)
	}
}

func TestReadingAWantReportsItsSearchSchedule(t *testing.T) {
	targetID := uuid.New()
	nextSearchAt := time.Date(2026, time.August, 14, 14, 30, 0, 0, time.UTC)
	targets := &fakeAcquisitionTargets{
		target: db.AcquisitionTargetRow{
			ID: targetID, Status: "unresolved", Origin: "playlist",
			NextSearchAt:   pgtype.Timestamptz{Time: nextSearchAt, Valid: true},
			SearchAttempts: 3,
		},
	}
	handler := acquisitionHandler(targets)

	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet,
		"/api/v1/acquisition-targets/"+targetID.String(), nil))
	if response.Code != http.StatusOK {
		t.Fatalf("status = %d; body = %s", response.Code, response.Body)
	}
	var payload struct {
		NextSearchAt   *time.Time `json:"nextSearchAt"`
		SearchAttempts int32      `json:"searchAttempts"`
	}
	if err := json.NewDecoder(response.Body).Decode(&payload); err != nil {
		t.Fatal(err)
	}
	if payload.NextSearchAt == nil || !payload.NextSearchAt.Equal(nextSearchAt) {
		t.Fatalf("nextSearchAt = %v, want %v", payload.NextSearchAt, nextSearchAt)
	}
	if payload.SearchAttempts != 3 {
		t.Fatalf("searchAttempts = %d, want 3", payload.SearchAttempts)
	}
}

// Wanting a release the store could not be read for is not an empty release.
func TestWantingAReleaseReportsAStoreThatFailed(t *testing.T) {
	store := &fakeStore{
		album:  db.GetAlbumRow{ID: uuid.New(), Title: "Dummy"},
		tracks: []db.ListAlbumTracksRow{{ID: uuid.New(), Title: "Roads"}},
		err:    errors.New("the database is unreachable"),
	}
	handler := releaseHandler(store, &fakeAcquisitionTargets{})

	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodPost,
		"/api/v1/albums/"+uuid.NewString()+"/wanted", nil))
	if response.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500", response.Code)
	}
}

// A want that could not be written stops the walk: reporting a count that
// included it would say music was asked for that was not.
func TestWantingAReleaseStopsWhenAWantCannotBeRecorded(t *testing.T) {
	albumID := uuid.New()
	store := &fakeStore{
		album: db.GetAlbumRow{ID: albumID, Title: "Dummy", ArtistName: "Portishead"},
		tracks: []db.ListAlbumTracksRow{{
			ID: uuid.New(), Title: "Roads",
			MusicbrainzRecordingID: uuid.NullUUID{UUID: uuid.New(), Valid: true},
		}},
	}
	handler := releaseHandler(store, &fakeAcquisitionTargets{
		err: errors.New("the database is unreachable"),
	})

	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodPost,
		"/api/v1/albums/"+albumID.String()+"/wanted", nil))
	if response.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500", response.Code)
	}
}

func TestWantingAnArtistReportsAStoreThatFailed(t *testing.T) {
	store := &fakeStore{
		artist: db.GetArtistRow{ID: uuid.New(), Name: "Portishead"},
		discography: []db.ArtistTracksForWantingRow{{
			AlbumID: uuid.New(), AlbumTitle: "Dummy", Title: "Roads",
		}},
		err: errors.New("the database is unreachable"),
	}
	handler := releaseHandler(store, &fakeAcquisitionTargets{})

	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodPost,
		"/api/v1/artists/"+uuid.NewString()+"/wanted", nil))
	if response.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500", response.Code)
	}
}

func TestWantingATrackReportsAStoreThatFailed(t *testing.T) {
	trackID, store := trackOnDummy()
	store.err = errors.New("the database is unreachable")
	handler := releaseHandler(store, &fakeAcquisitionTargets{})

	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodPost,
		"/api/v1/tracks/"+trackID.String()+"/wanted", nil))
	if response.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500", response.Code)
	}
}

// A track that went away between the page and the press is not a track to want.
func TestWantingATrackThatIsNotThereIsNotFound(t *testing.T) {
	store := &fakeStore{trackForWantingErr: pgx.ErrNoRows}
	handler := releaseHandler(store, &fakeAcquisitionTargets{})

	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodPost,
		"/api/v1/tracks/"+uuid.NewString()+"/wanted", nil))
	if response.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", response.Code)
	}
}

func TestAMalformedTrackIDNamesNoTrack(t *testing.T) {
	handler := releaseHandler(&fakeStore{}, &fakeAcquisitionTargets{})
	for _, action := range []string{"wanted", "not-wanted"} {
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, httptest.NewRequest(http.MethodPost,
			"/api/v1/tracks/not-a-uuid/"+action, nil))
		if response.Code != http.StatusNotFound {
			t.Errorf("%s: status = %d, want 404", action, response.Code)
		}
	}
}

// A track the library already holds through another release is still in the
// library, and dismissing it would say something about the file rather than
// about the search.
func TestDismissingATrackHeldElsewhereIsAConflict(t *testing.T) {
	trackID, store := trackOnDummy()
	store.trackForWanting.HeldElsewhere = true
	handler := releaseHandler(store, &fakeAcquisitionTargets{})

	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodPost,
		"/api/v1/tracks/"+trackID.String()+"/not-wanted", nil))
	if response.Code != http.StatusConflict {
		t.Fatalf("status = %d, want 409", response.Code)
	}
}

func TestDismissingATrackReportsAStoreThatFailed(t *testing.T) {
	trackID, store := trackOnDummy()
	handler := releaseHandler(store, &fakeAcquisitionTargets{
		err: errors.New("the database is unreachable"),
	})

	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodPost,
		"/api/v1/tracks/"+trackID.String()+"/not-wanted", nil))
	if response.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500", response.Code)
	}
}

// A want that was settled between the page and the press is finished with, and
// dismissing it again has nothing left to stop.
func TestDismissingATrackWhoseWantIsFinishedIsAConflict(t *testing.T) {
	trackID, store := trackOnDummy()
	targets := &stoppingTargets{
		fakeAcquisitionTargets: &fakeAcquisitionTargets{
			target: db.AcquisitionTargetRow{ID: uuid.New(), Status: "pending", Origin: "manual"},
		},
		stopErr: pgx.ErrNoRows,
	}
	handler := NewAPI(store, fakeDatabase{}, &fakeArtistSearcher{}, zerolog.Nop(),
		WithAcquisitionTargets(targets))

	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodPost,
		"/api/v1/tracks/"+trackID.String()+"/not-wanted", nil))
	if response.Code != http.StatusConflict {
		t.Fatalf("status = %d, want 409", response.Code)
	}
}

// stoppingTargets fails only the stop, so a want that was recorded and then
// could not be stopped is a case of its own.
type stoppingTargets struct {
	*fakeAcquisitionTargets
	stopErr error
}

func (targets *stoppingTargets) StopPursuing(
	_ context.Context, id uuid.UUID,
) (db.AcquisitionTargetRow, error) {
	targets.stopped = id
	return db.AcquisitionTargetRow{}, targets.stopErr
}

func TestStoppingATrackWantReportsAStoreThatFailed(t *testing.T) {
	trackID, store := trackOnDummy()
	targets := &stoppingTargets{
		fakeAcquisitionTargets: &fakeAcquisitionTargets{
			target: db.AcquisitionTargetRow{ID: uuid.New(), Status: "pending", Origin: "manual"},
		},
		stopErr: errors.New("the database is unreachable"),
	}
	handler := NewAPI(store, fakeDatabase{}, &fakeArtistSearcher{}, zerolog.Nop(),
		WithAcquisitionTargets(targets))

	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodPost,
		"/api/v1/tracks/"+trackID.String()+"/not-wanted", nil))
	if response.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500", response.Code)
	}
}

// A release that went away between the page and the press has no remainder to
// dismiss.
func TestDismissingTheRemainderOfAReleaseThatIsNotThereIsNotFound(t *testing.T) {
	store := &fakeStore{err: pgx.ErrNoRows}
	handler := releaseHandler(store, &fakeAcquisitionTargets{})

	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodPost,
		"/api/v1/albums/"+uuid.NewString()+"/not-wanted", nil))
	if response.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", response.Code)
	}
}

func TestDismissingAReleaseRemainderReportsAStoreThatFailed(t *testing.T) {
	store := &fakeStore{err: errors.New("the database is unreachable")}
	handler := releaseHandler(store, &fakeAcquisitionTargets{})

	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodPost,
		"/api/v1/albums/"+uuid.NewString()+"/not-wanted", nil))
	if response.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500", response.Code)
	}
}

// A want that could not be recorded stops the walk rather than being counted as
// a dismissal that never happened.
func TestDismissingAReleaseRemainderStopsWhenAWantCannotBeRecorded(t *testing.T) {
	albumID := uuid.New()
	store := &fakeStore{
		album: db.GetAlbumRow{ID: albumID, Title: "Dummy", ArtistName: "Portishead"},
		tracks: []db.ListAlbumTracksRow{{
			ID: uuid.New(), Title: "Never asked for",
			MusicbrainzRecordingID: uuid.NullUUID{UUID: uuid.New(), Valid: true},
		}},
	}
	handler := releaseHandler(store, &fakeAcquisitionTargets{
		err: errors.New("the database is unreachable"),
	})

	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodPost,
		"/api/v1/albums/"+albumID.String()+"/not-wanted", nil))
	if response.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500", response.Code)
	}
}

// A stop that failed for a reason other than the want being finished stops the
// walk: the rest of the release is not dismissed on a database nobody can read.
func TestDismissingAReleaseRemainderStopsWhenAStopFails(t *testing.T) {
	albumID, wantID := uuid.New(), uuid.New()
	store := &fakeStore{
		album: db.GetAlbumRow{ID: albumID, Title: "Dummy", ArtistName: "Portishead"},
		tracks: []db.ListAlbumTracksRow{{
			ID: uuid.New(), Title: "Being pursued",
			MusicbrainzRecordingID: uuid.NullUUID{UUID: uuid.New(), Valid: true},
			WantID:                 uuid.NullUUID{UUID: wantID, Valid: true},
			WantStatus:             pgtype.Text{String: "searching", Valid: true},
		}},
	}
	targets := &stoppingTargets{
		fakeAcquisitionTargets: &fakeAcquisitionTargets{},
		stopErr:                errors.New("the database is unreachable"),
	}
	handler := NewAPI(store, fakeDatabase{}, &fakeArtistSearcher{}, zerolog.Nop(),
		WithAcquisitionTargets(targets))

	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodPost,
		"/api/v1/albums/"+albumID.String()+"/not-wanted", nil))
	if response.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500", response.Code)
	}
}

// A want settled between the list and the press is accounted for and the walk
// carries on: a release-wide press does not stop for one already-decided track.
func TestDismissingAReleaseRemainderCountsAWantAlreadyFinished(t *testing.T) {
	albumID := uuid.New()
	store := &fakeStore{
		album: db.GetAlbumRow{ID: albumID, Title: "Dummy", ArtistName: "Portishead"},
		tracks: []db.ListAlbumTracksRow{{
			ID: uuid.New(), Title: "Being pursued",
			MusicbrainzRecordingID: uuid.NullUUID{UUID: uuid.New(), Valid: true},
			WantID:                 uuid.NullUUID{UUID: uuid.New(), Valid: true},
			WantStatus:             pgtype.Text{String: "searching", Valid: true},
		}},
	}
	targets := &stoppingTargets{
		fakeAcquisitionTargets: &fakeAcquisitionTargets{},
		stopErr:                pgx.ErrNoRows,
	}
	handler := NewAPI(store, fakeDatabase{}, &fakeArtistSearcher{}, zerolog.Nop(),
		WithAcquisitionTargets(targets))

	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodPost,
		"/api/v1/albums/"+albumID.String()+"/not-wanted", nil))
	if response.Code != http.StatusOK {
		t.Fatalf("status = %d; body = %s", response.Code, response.Body)
	}
	var result dismissAlbumResponse
	if err := json.Unmarshal(response.Body.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	if result.AlreadyDismissed != 1 || result.Dismissed != 0 {
		t.Fatalf("result = %+v, want the settled want counted as already decided", result)
	}
}

// The same recording reached twice over one release is one decision, not two.
func TestDismissingAReleaseRemainderCountsARepeatedRecordingOnce(t *testing.T) {
	albumID, recordingID := uuid.New(), uuid.New()
	recording := uuid.NullUUID{UUID: recordingID, Valid: true}
	store := &fakeStore{
		album: db.GetAlbumRow{ID: albumID, Title: "Dummy", ArtistName: "Portishead"},
		tracks: []db.ListAlbumTracksRow{
			{ID: uuid.New(), Title: "Roads", MusicbrainzRecordingID: recording},
			{ID: uuid.New(), Title: "Roads (again)", MusicbrainzRecordingID: recording},
		},
	}
	targets := &fakeAcquisitionTargets{
		target:  db.AcquisitionTargetRow{ID: uuid.New(), Status: "pending", Origin: "manual"},
		created: true,
	}
	handler := releaseHandler(store, targets)

	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodPost,
		"/api/v1/albums/"+albumID.String()+"/not-wanted", nil))
	if response.Code != http.StatusOK {
		t.Fatalf("status = %d; body = %s", response.Code, response.Body)
	}
	var result dismissAlbumResponse
	if err := json.Unmarshal(response.Body.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	if result.Dismissed != 1 {
		t.Fatalf("dismissed = %d, want the one recording counted once", result.Dismissed)
	}
}

// An id that is not a UUID names no release or artist, and the wanting routes
// say so without asking the catalogue about it.
func TestAMalformedIDNamesNothingToWant(t *testing.T) {
	handler := acquisitionHandler(&fakeAcquisitionTargets{})
	for _, request := range []*http.Request{
		httptest.NewRequest(http.MethodPost, "/api/v1/albums/not-a-uuid/wanted", nil),
		httptest.NewRequest(http.MethodPost, "/api/v1/albums/not-a-uuid/not-wanted", nil),
		httptest.NewRequest(http.MethodPost, "/api/v1/artists/not-a-uuid/wanted", nil),
	} {
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, request)
		if response.Code != http.StatusNotFound {
			t.Errorf("%s: status = %d, want 404", request.URL.Path, response.Code)
		}
	}
}

// A release that could be read but whose tracks could not is not a release with
// nothing missing.
func TestWantingAReleaseReportsATrackListThatCouldNotBeRead(t *testing.T) {
	store := &fakeStore{
		album:     db.GetAlbumRow{ID: uuid.New(), Title: "Dummy"},
		tracksErr: errors.New("the database is unreachable"),
	}
	handler := releaseHandler(store, &fakeAcquisitionTargets{})

	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodPost,
		"/api/v1/albums/"+uuid.NewString()+"/wanted", nil))
	if response.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500", response.Code)
	}
}

func TestDismissingAReleaseRemainderReportsATrackListThatCouldNotBeRead(t *testing.T) {
	store := &fakeStore{
		album:     db.GetAlbumRow{ID: uuid.New(), Title: "Dummy"},
		tracksErr: errors.New("the database is unreachable"),
	}
	handler := releaseHandler(store, &fakeAcquisitionTargets{})

	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodPost,
		"/api/v1/albums/"+uuid.NewString()+"/not-wanted", nil))
	if response.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500", response.Code)
	}
}

// An artist with no track anywhere and no row behind them is an artist Schall
// does not hold at all.
func TestWantingAnArtistNobodyHoldsIsNotFound(t *testing.T) {
	store := &fakeStore{getArtistErr: pgx.ErrNoRows}
	handler := releaseHandler(store, &fakeAcquisitionTargets{})

	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodPost,
		"/api/v1/artists/"+uuid.NewString()+"/wanted", nil))
	if response.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", response.Code)
	}
}

func TestWantingAnArtistReportsTheirRowCouldNotBeRead(t *testing.T) {
	store := &fakeStore{getArtistErr: errors.New("the database is unreachable")}
	handler := releaseHandler(store, &fakeAcquisitionTargets{})

	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodPost,
		"/api/v1/artists/"+uuid.NewString()+"/wanted", nil))
	if response.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500", response.Code)
	}
}

// A want that could not be written stops the discography walk: reporting a
// count that included it would say music was asked for that was not.
func TestWantingADiscographyStopsWhenAWantCannotBeRecorded(t *testing.T) {
	store := &fakeStore{discography: []db.ArtistTracksForWantingRow{{
		AlbumID: uuid.New(), AlbumTitle: "Dummy", ArtistName: "Portishead", Title: "Roads",
		MusicbrainzRecordingID: uuid.NullUUID{UUID: uuid.New(), Valid: true},
	}}}
	handler := releaseHandler(store, &fakeAcquisitionTargets{
		err: errors.New("the database is unreachable"),
	})

	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodPost,
		"/api/v1/artists/"+uuid.NewString()+"/wanted", nil))
	if response.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500", response.Code)
	}
}

// Wanting one track is a want like any other, and a store that could not record
// it is reported rather than answered with a want that does not exist.
func TestRecordingOneTracksWantReportsAStoreThatFailed(t *testing.T) {
	trackID, store := trackOnDummy()
	handler := releaseHandler(store, &fakeAcquisitionTargets{
		err: errors.New("the database is unreachable"),
	})

	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodPost,
		"/api/v1/tracks/"+trackID.String()+"/wanted", nil))
	if response.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500", response.Code)
	}
}

// The question a want is asking is part of reading it, so a question that could
// not be read is reported rather than answered as no question at all.
func TestReadingAWantReportsCandidatesThatCouldNotBeRead(t *testing.T) {
	handler := acquisitionHandler(&fakeAcquisitionTargets{
		candidatesErr: errors.New("the database is unreachable"),
	})

	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet,
		"/api/v1/acquisition-targets/"+uuid.NewString(), nil))
	if response.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500", response.Code)
	}
}

// So is what has already been tried: a want that keeps looking has to be able
// to say what it has already looked at.
func TestReadingAWantReportsCopiesThatCouldNotBeRead(t *testing.T) {
	handler := acquisitionHandler(&fakeAcquisitionTargets{
		copiesErr: errors.New("the database is unreachable"),
	})

	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet,
		"/api/v1/acquisition-targets/"+uuid.NewString(), nil))
	if response.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500", response.Code)
	}
}

func TestTheReviewQueueAnswersWithThePageThatWasAskedFor(t *testing.T) {
	handler := acquisitionHandler(&fakeAcquisitionTargets{
		target: db.AcquisitionTargetRow{ID: uuid.New(), Status: "awaiting_review", Origin: "manual"},
	})

	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet,
		"/api/v1/review-queue?limit=5&offset=10", nil))
	if response.Code != http.StatusOK {
		t.Fatalf("status = %d; body = %s", response.Code, response.Body)
	}
	var payload struct {
		Limit  int32 `json:"limit"`
		Offset int32 `json:"offset"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &payload); err != nil {
		t.Fatal(err)
	}
	if payload.Limit != 5 || payload.Offset != 10 {
		t.Fatalf("paging = %+v", payload)
	}
}

// A copy whose file has gone is skipped rather than logged: the request that
// eventually asks for it says so properly, and a page of questions still
// renders when the audio behind one of them does not.
func TestWarmingSkipsACopyWhoseFileHasGone(t *testing.T) {
	inbox := t.TempDir()
	if err := os.MkdirAll(filepath.Join(inbox, "Anetha"), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(inbox, "Anetha", "03.flac"), []byte("audio"), 0o600); err != nil {
		t.Fatal(err)
	}
	var warmed []string
	targets := &fakeAcquisitionTargets{
		target: db.AcquisitionTargetRow{ID: uuid.New(), Status: "awaiting_review", Origin: "manual"},
		copies: []db.AcquisitionTargetFileRow{
			{ID: uuid.New(), Verdict: db.AcquiredFileHeld, RemotePath: `Music\Gone\09.flac`, FileName: "09.flac"},
			{ID: uuid.New(), Verdict: db.AcquiredFileHeld, RemotePath: `Music\Anetha\03.flac`, FileName: "03.flac"},
		},
	}
	handler := NewAPI(&fakeStore{}, fakeDatabase{}, &fakeArtistSearcher{}, zerolog.Nop(),
		WithAcquisitionTargets(targets), WithDownloadInbox(inbox),
		WithPreviews(stubPreviews{warmed: &warmed}))

	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/api/v1/review-queue", nil))
	if response.Code != http.StatusOK {
		t.Fatalf("status = %d; body = %s", response.Code, response.Body)
	}
	if len(warmed) != 1 || !strings.HasSuffix(warmed[0], filepath.Join("Anetha", "03.flac")) {
		t.Fatalf("warmed = %v, want only the copy still there", warmed)
	}
}

// The transcoder failed for a reason of its own, which is this installation's
// fault rather than something about the copy.
func TestATranscodeThatFailedIsAServerError(t *testing.T) {
	inbox := t.TempDir()
	if err := os.MkdirAll(filepath.Join(inbox, "Anetha"), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(inbox, "Anetha", "03.flac"), []byte("audio"), 0o600); err != nil {
		t.Fatal(err)
	}
	targets := &fakeAcquisitionTargets{
		copies: []db.AcquisitionTargetFileRow{{
			ID: uuid.New(), Verdict: db.AcquiredFileHeld,
			RemotePath: `Music\Anetha\03.flac`, FileName: "03.flac",
		}},
	}
	handler := NewAPI(&fakeStore{}, fakeDatabase{}, &fakeArtistSearcher{}, zerolog.Nop(),
		WithAcquisitionTargets(targets), WithDownloadInbox(inbox),
		WithPreviews(stubPreviews{err: errors.New("ffmpeg exited with status 1")}))

	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet,
		"/api/v1/review-queue/copies/"+uuid.NewString()+"/audio", nil))
	if response.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500", response.Code)
	}
}

func TestAMalformedCopyIDCannotBePlayed(t *testing.T) {
	handler := NewAPI(&fakeStore{}, fakeDatabase{}, &fakeArtistSearcher{}, zerolog.Nop(),
		WithAcquisitionTargets(&fakeAcquisitionTargets{}),
		WithDownloadInbox(t.TempDir()), WithPreviews(stubPreviews{}))

	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet,
		"/api/v1/review-queue/copies/not-a-uuid/audio", nil))
	if response.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", response.Code)
	}
}

// Nothing below the inbox is reachable by a name that leaves it. A peer's
// folder that resolves outside is refused rather than served.
func TestACopyResolvingOutsideTheInboxIsRefused(t *testing.T) {
	inbox := t.TempDir()
	outside := t.TempDir()
	if err := os.WriteFile(filepath.Join(outside, "03.flac"), []byte("audio"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(inbox, "Anetha")); err != nil {
		t.Skipf("this filesystem does not support symlinks: %v", err)
	}
	targets := &fakeAcquisitionTargets{
		copies: []db.AcquisitionTargetFileRow{{
			ID: uuid.New(), Verdict: db.AcquiredFileHeld,
			RemotePath: `Music\Anetha\03.flac`, FileName: "03.flac",
		}},
	}
	handler := NewAPI(&fakeStore{}, fakeDatabase{}, &fakeArtistSearcher{}, zerolog.Nop(),
		WithAcquisitionTargets(targets), WithDownloadInbox(inbox),
		WithPreviews(stubPreviews{}))

	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet,
		"/api/v1/review-queue/copies/"+uuid.NewString()+"/audio", nil))
	if response.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404; body = %s", response.Code, response.Body)
	}
	if !strings.Contains(response.Body.String(), "outside the download inbox") {
		t.Fatalf("body = %s, want the escape named", response.Body)
	}
}

// Taking an answer back names which of the four it was and the row it was
// recorded against. Both reach the store, because "undo" on its own does not say
// what is being undone.
func TestTakingAnAnswerBackNamesTheAnswerAndTheRowItWasRecordedAgainst(t *testing.T) {
	targetID := uuid.New()
	targets := &fakeAcquisitionTargets{}
	handler := acquisitionHandler(targets)

	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodPost, "/api/v1/review-queue/undo",
		strings.NewReader(`{"outcome":"not-wanted","id":"`+targetID.String()+`"}`)))

	if response.Code != http.StatusOK {
		t.Fatalf("status = %d; body = %s", response.Code, response.Body)
	}
	if targets.takenBack != db.AnswerNotWanted || targets.takenBackID != targetID {
		t.Fatalf("took back %q on %s, want not-wanted on %s",
			targets.takenBack, targets.takenBackID, targetID)
	}
}

// A word that is not one of the four answers is refused. Deferring records
// nothing, so there is nothing of it to take back either.
func TestTakingBackSomethingThatIsNotOneOfTheFourAnswersIsRefused(t *testing.T) {
	handler := acquisitionHandler(&fakeAcquisitionTargets{})

	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodPost, "/api/v1/review-queue/undo",
		strings.NewReader(`{"outcome":"later","id":"`+uuid.NewString()+`"}`)))

	if response.Code != http.StatusUnprocessableEntity {
		t.Fatalf("status = %d, want 422; body = %s", response.Code, response.Body)
	}
}

// Every refusal says what happened and what to do instead. None of them is an
// invitation to press the button again, which is the standing rule about
// buttons whose only remaining option is to press them.
func TestEveryRefusalToTakeAnAnswerBackSaysWhatToDoInstead(t *testing.T) {
	for _, refusal := range []struct {
		err  error
		says string
	}{
		{db.ErrCopyIsInTheLibrary, "Remove the file from the library instead."},
		{db.ErrWantAnsweredAnotherWay, "This wishlist entry has already been answered another way since."},
		{db.ErrRecordingWantedElsewhere, "Another wishlist entry is already looking for this recording."},
		{db.ErrEntryResolvedAgain, "This entry has already been resolved again."},
		{db.ErrSchallDecidedThis, "Answer the question it raised instead."},
	} {
		handler := acquisitionHandler(&fakeAcquisitionTargets{takeBackErr: refusal.err})

		response := httptest.NewRecorder()
		handler.ServeHTTP(response, httptest.NewRequest(http.MethodPost, "/api/v1/review-queue/undo",
			strings.NewReader(`{"outcome":"accept","id":"`+uuid.NewString()+`"}`)))

		if response.Code != http.StatusConflict {
			t.Errorf("%v: status = %d, want 409", refusal.err, response.Code)
		}
		if !strings.Contains(response.Body.String(), refusal.says) {
			t.Errorf("%v: body = %s, want it to say %q", refusal.err, response.Body, refusal.says)
		}
	}
}

// Whether an answer can still be taken back is asked before the control is
// offered, so a decision that has landed is not offered a way back it would
// refuse.
func TestTheQueueCanAskWhetherAnAnswerCanStillBeTakenBack(t *testing.T) {
	handler := acquisitionHandler(&fakeAcquisitionTargets{reversible: true})

	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet,
		"/api/v1/review-queue/undo?outcome=accept&id="+uuid.NewString(), nil))

	if response.Code != http.StatusOK {
		t.Fatalf("status = %d; body = %s", response.Code, response.Body)
	}
	if !strings.Contains(response.Body.String(), `"reversible":true`) {
		t.Fatalf("body = %s, want it to say the answer can still be taken back", response.Body)
	}
}

// The wanted list says once, at the top, when nothing on it can be fetched.
//
// A want is one recording Schall looks for on Soulseek. The audio check is the
// only thing that can prove a stranger's file is that recording, so with no key
// stored the loop fetches nothing at all — and every row still read "tried five
// minutes ago", forever, naming neither the cause nor the control.
func TestTheWantedListSaysWhenNoKeyLetsAnythingBeFetched(t *testing.T) {
	targets := &fakeAcquisitionTargets{
		target: db.AcquisitionTargetRow{ID: uuid.New(), Status: "pending", Origin: "manual"},
	}
	store := &fakeSettingsStore{fakeStore: &fakeStore{}}
	handler := NewAPI(store, fakeDatabase{}, &fakeArtistSearcher{}, zerolog.Nop(),
		WithAcquisitionTargets(targets))

	if notice := wantsNotice(t, handler); !strings.Contains(notice, "AcoustID key") {
		t.Fatalf("notice = %q, want the missing key named", notice)
	}

	// A key stored with the check switched off leaves the wants exactly as
	// stuck, so the line stands — pointing at the switch rather than the key.
	store.imports = db.ImportSettingsRow{
		SourceRetention: db.SourceRetentionKeep, AcoustIDAPIKey: "a-key", AcoustIDEnabled: false,
	}
	if notice := wantsNotice(t, handler); !strings.Contains(notice, "Verify imports by audio") {
		t.Fatalf("notice = %q, want the switched-off check named", notice)
	}

	// And with both in place there is nothing to say. A line that outlives its
	// own condition is the next thing nobody reads.
	store.imports = db.ImportSettingsRow{
		SourceRetention: db.SourceRetentionKeep, AcoustIDAPIKey: "a-key", AcoustIDEnabled: true,
	}
	if notice := wantsNotice(t, handler); notice != "" {
		t.Fatalf("notice = %q, want none once a copy could be verified", notice)
	}

	// Nor is it said over a list with nothing waiting on it. The wants that were
	// answered are not waiting for anything.
	store.imports = db.ImportSettingsRow{}
	targets.target.Status = "acquired"
	if notice := wantsNotice(t, handler); notice != "" {
		t.Fatalf("notice = %q, want none where no want is waiting", notice)
	}
}

func wantsNotice(t *testing.T, handler http.Handler) string {
	t.Helper()
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(
		http.MethodGet, "/api/v1/acquisition-targets", nil))
	if response.Code != http.StatusOK {
		t.Fatalf("status = %d; body = %s", response.Code, response.Body)
	}
	var payload struct {
		Notice string `json:"notice"`
	}
	if err := json.NewDecoder(response.Body).Decode(&payload); err != nil {
		t.Fatal(err)
	}
	return payload.Notice
}

// A want holding several copies of one master. The queue sends the copies whole,
// as it always has, and says alongside them which of them are the same piece of
// audio — so a person reads one row per master instead of one per file. Nothing
// about which copy may be accepted changes.
//
// The fingerprint below is real: it is one piece of audio as fpcalc 1.6.1 printed
// it, the same value internal/chromaprint's own tests are built on.
const oneMasterFingerprint = "AQAAS47CSxLaY_vB5yK2H-0yC1fs4zmuI0wdaD_y4XikD69yNJeCHzZqFX-E6QT1oxf24GmMM3rwOMWR7PBzpLeC3DmEI0TRPMGDI0eyP_ABny6u4_gvHGGO59ARUzmFXsdbotYS5M0J_Xh2RPqLM4J_MCuPPAr6EIrPCLp4lDy-4ccvnMdUpYaeSEg1sWAkAMMAIgQwAhhBVAoLhjBOIAOBAkw4I4ihBAEECDUOMQWUAM4EIBEWAhEhAQAMMUGMA8oRQAgzAhIDGUQA"

func TestTheReviewQueueSaysWhichCopiesAreTheSameAudio(t *testing.T) {
	sameAudio := func(size int64) db.AcquisitionTargetFileRow {
		return db.AcquisitionTargetFileRow{
			ID: uuid.New(), Provider: "slskd", SourceUsername: "peer",
			FileName: "03.flac", Verdict: db.AcquiredFileHeld, DecidedBy: "schall",
			SizeBytes: pgtype.Int8{Int64: size, Valid: true},
			Evidence:  &db.AcquiredFileEvidence{AudioFingerprint: oneMasterFingerprint},
		}
	}
	alone := db.AcquisitionTargetFileRow{
		ID: uuid.New(), Provider: "slskd", SourceUsername: "other",
		FileName: "04.flac", Verdict: db.AcquiredFileHeld, DecidedBy: "schall",
	}
	first, second := sameAudio(9218048), sameAudio(9218048)
	targets := &fakeAcquisitionTargets{
		target: db.AcquisitionTargetRow{ID: uuid.New(), Status: "pending"},
		copies: []db.AcquisitionTargetFileRow{first, second, alone},
	}
	handler := acquisitionHandler(targets)

	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/api/v1/review-queue", nil))

	if response.Code != http.StatusOK {
		t.Fatalf("status = %d; body = %s", response.Code, response.Body)
	}
	var payload struct {
		Items []reviewItemResponse `json:"items"`
	}
	if err := json.NewDecoder(response.Body).Decode(&payload); err != nil {
		t.Fatal(err)
	}
	if len(payload.Items) != 1 {
		t.Fatalf("items = %d, want the one question", len(payload.Items))
	}
	item := payload.Items[0]
	if len(item.Copies) != 3 {
		t.Fatalf("copies = %d, want every copy sent as before", len(item.Copies))
	}
	if len(item.CopyGroups) != 2 {
		t.Fatalf("groups = %+v, want the two copies of one master read as one row",
			item.CopyGroups)
	}
	if len(item.CopyGroups[0].CopyIDs) != 2 ||
		item.CopyGroups[0].CopyIDs[0] != first.ID ||
		item.CopyGroups[0].CopyIDs[1] != second.ID {
		t.Errorf("first group = %v, want both copies of the master", item.CopyGroups[0].CopyIDs)
	}
	if item.CopyGroups[0].Rips != 1 {
		t.Errorf("rips = %d, want two files of one size counted as one rip",
			item.CopyGroups[0].Rips)
	}
	// The copy nothing could compare is its own row. Being the odd one out costs
	// it nothing else: it is offered exactly as it is today.
	if len(item.CopyGroups[1].CopyIDs) != 1 || item.CopyGroups[1].CopyIDs[0] != alone.ID {
		t.Errorf("second group = %v, want the copy nothing could compare alone",
			item.CopyGroups[1].CopyIDs)
	}
}

// The queue says which wants are waiting for evidence rather than for the
// reader. A copy held because MusicBrainz could not be asked is one of those:
// the provider may answer tomorrow, and the want is judged again when it does.
func TestTheReviewQueueSaysAWantIsWaitingOnMusicBrainz(t *testing.T) {
	due := time.Now().Add(time.Hour).UTC().Truncate(time.Second)
	targets := &fakeAcquisitionTargets{
		target: db.AcquisitionTargetRow{
			ID: uuid.New(), Status: "pending", Origin: "playlist",
			EntryArtist: "Anetha", EntryTitle: "Candy from Strangers",
			MusicBrainzRecordingID: uuid.NullUUID{UUID: uuid.New(), Valid: true},
			RecheckReason:          pgtype.Text{String: db.RecheckForMusicBrainz, Valid: true},
			RecheckAfter:           pgtype.Timestamptz{Time: due, Valid: true},
		},
		copies: []db.AcquisitionTargetFileRow{{
			ID: uuid.New(), Provider: "slskd", SourceUsername: "peer",
			FileName: "03.flac", Verdict: db.AcquiredFileHeld, DecidedBy: "schall",
			Summary: unaskedRegistrationSummary +
				" whether that is this track entered twice. Kept as a question.",
		}},
	}
	handler := acquisitionHandler(targets)

	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/api/v1/review-queue", nil))

	var payload struct {
		Items []reviewItemResponse `json:"items"`
	}
	if err := json.NewDecoder(response.Body).Decode(&payload); err != nil {
		t.Fatal(err)
	}
	if payload.Items[0].WaitingFor != db.RecheckForMusicBrainz {
		t.Fatalf("waitingFor = %q, want %q",
			payload.Items[0].WaitingFor, db.RecheckForMusicBrainz)
	}
	if payload.Items[0].WaitingUntil == nil || !payload.Items[0].WaitingUntil.Equal(due) {
		t.Fatalf("waitingUntil = %v, want %v", payload.Items[0].WaitingUntil, due)
	}
}

// A want whose schedule has run out is a question for the reader, whatever its
// copy still says about the provider. Nothing is waited for, and the payload
// says nothing.
func TestAWantThatHasStoppedWaitingSaysSoInTheQueue(t *testing.T) {
	targets := &fakeAcquisitionTargets{
		target: db.AcquisitionTargetRow{
			ID: uuid.New(), Status: "pending", Origin: "playlist",
			EntryArtist: "Anetha", EntryTitle: "Candy from Strangers",
			MusicBrainzRecordingID: uuid.NullUUID{UUID: uuid.New(), Valid: true},
			RecheckAttempts:        4,
		},
		copies: []db.AcquisitionTargetFileRow{{
			ID: uuid.New(), Provider: "slskd", SourceUsername: "peer",
			FileName: "03.flac", Verdict: db.AcquiredFileHeld, DecidedBy: "schall",
			Summary: unaskedRegistrationSummary +
				" whether that is this track entered twice. Kept as a question.",
		}},
	}
	handler := acquisitionHandler(targets)

	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/api/v1/review-queue", nil))

	var payload struct {
		Items []reviewItemResponse `json:"items"`
	}
	if err := json.NewDecoder(response.Body).Decode(&payload); err != nil {
		t.Fatal(err)
	}
	if payload.Items[0].WaitingFor != "" {
		t.Fatalf("waitingFor = %q, want the question a person answers",
			payload.Items[0].WaitingFor)
	}
}

// A want stopped on what its library file is waits on a fact rather than a
// clock, so it says what it waits for and names no time.
func TestTheReviewQueueSaysAWantIsWaitingOnTheLibrary(t *testing.T) {
	targets := &fakeAcquisitionTargets{
		target: db.AcquisitionTargetRow{
			ID: uuid.New(), Status: "pending", Origin: "playlist",
			EntryArtist: "JACKBOYS", EntryTitle: "OUT WEST",
			MusicBrainzRecordingID: uuid.NullUUID{UUID: uuid.New(), Valid: true},
			RecheckReason: pgtype.Text{
				String: db.RecheckForLibraryIdentity, Valid: true},
		},
		copies: []db.AcquisitionTargetFileRow{{
			ID: uuid.New(), Provider: "slskd", SourceUsername: "peer",
			FileName: "05.flac", Verdict: db.AcquiredFileAccepted, DecidedBy: "schall",
			LibraryFileID: uuid.NullUUID{UUID: uuid.New(), Valid: true},
		}},
	}
	handler := acquisitionHandler(targets)

	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/api/v1/review-queue", nil))

	var payload struct {
		Items []reviewItemResponse `json:"items"`
	}
	if err := json.NewDecoder(response.Body).Decode(&payload); err != nil {
		t.Fatal(err)
	}
	if payload.Items[0].Kind != reviewKindStopped {
		t.Fatalf("kind = %q, want %q", payload.Items[0].Kind, reviewKindStopped)
	}
	if payload.Items[0].WaitingFor != db.RecheckForLibraryIdentity {
		t.Fatalf("waitingFor = %q, want %q",
			payload.Items[0].WaitingFor, db.RecheckForLibraryIdentity)
	}
	if payload.Items[0].WaitingUntil != nil {
		t.Fatalf("waitingUntil = %v, want no clock on a wait a fact ends",
			payload.Items[0].WaitingUntil)
	}
}

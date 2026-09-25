package recommendations

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/pxldi/schall/internal/db"
	"github.com/pxldi/schall/internal/listenbrainz"
	"github.com/pxldi/schall/internal/musicbrainz"
	"github.com/rs/zerolog"
)

// fakeStore holds recommendation snapshots the way the two tables do: keyed by
// source, one header and one set of candidate rows each. A sweep builds under
// the staged source and renames it over the published source when it has walked
// the whole list, so the two names are what a test reads to tell work in
// progress from the list a reader would see.
type fakeStore struct {
	row                  db.ListenBrainzSettingsRow
	missing              bool
	recorded             []recordedCheck
	snapshots            map[string]db.UpsertRecommendationSnapshotParams
	candidates           map[string][]db.InsertRecommendationCandidateParams
	recommendationWrites int
	feedback             []db.RecommendationFeedback
	pool                 []db.OwnRecommendationPoolRow
	poolParams           []db.OwnRecommendationPoolParams
}

type recordedCheck struct {
	status  string
	detail  string
	failure string
}

func (store *fakeStore) ListenBrainzSettings(context.Context) (db.ListenBrainzSettingsRow, error) {
	if store.missing {
		return db.ListenBrainzSettingsRow{}, pgx.ErrNoRows
	}
	return store.row, nil
}

// RecordListenBrainzConnection refuses a verdict that names a row other than
// the one now stored, exactly as the guarded UPDATE does.
func (store *fakeStore) RecordListenBrainzConnection(
	_ context.Context, params db.RecordListenBrainzConnectionParams,
) (db.ListenBrainzSettingsRow, error) {
	if params.ExpectedUsername != store.row.Username ||
		params.ExpectedUpdatedAt != store.row.UpdatedAt {
		return db.ListenBrainzSettingsRow{}, pgx.ErrNoRows
	}
	store.recorded = append(store.recorded,
		recordedCheck{params.Status, params.Detail, params.Failure})
	saved := store.row
	saved.ConnectionStatus = params.Status
	saved.ConnectionDetail = pgtype.Text{String: params.Detail, Valid: params.Detail != ""}
	saved.ConnectionError = pgtype.Text{String: params.Failure, Valid: params.Failure != ""}
	return saved, nil
}

func (store *fakeStore) ReplaceRecommendationSnapshot(
	_ context.Context, snapshot db.UpsertRecommendationSnapshotParams,
	candidates []db.InsertRecommendationCandidateParams,
) error {
	store.recommendationWrites++
	rows := make([]db.InsertRecommendationCandidateParams, 0, len(candidates))
	for _, candidate := range candidates {
		candidate.Source = snapshot.Source
		candidate.FetchedAt = snapshot.FetchedAt
		rows = append(rows, candidate)
	}
	store.write(snapshot, rows)
	return nil
}

// PublishRecommendationSnapshot moves the staged snapshot onto the published
// name and leaves nothing behind under the staged one, as the database does in
// a single transaction.
func (store *fakeStore) PublishRecommendationSnapshot(_ context.Context, staged, published string) error {
	snapshot, ok := store.snapshots[staged]
	if !ok {
		return db.ErrNoStagedSnapshot
	}
	rows := make([]db.InsertRecommendationCandidateParams, 0, len(store.candidates[staged]))
	for _, candidate := range store.candidates[staged] {
		candidate.Source = published
		rows = append(rows, candidate)
	}
	snapshot.Source = published
	store.write(snapshot, rows)
	delete(store.snapshots, staged)
	delete(store.candidates, staged)
	return nil
}

func (store *fakeStore) DiscardRecommendationSnapshot(_ context.Context, source string) error {
	delete(store.snapshots, source)
	delete(store.candidates, source)
	return nil
}

func (store *fakeStore) write(
	snapshot db.UpsertRecommendationSnapshotParams,
	candidates []db.InsertRecommendationCandidateParams,
) {
	if store.snapshots == nil {
		store.snapshots = make(map[string]db.UpsertRecommendationSnapshotParams, 2)
	}
	if store.candidates == nil {
		store.candidates = make(map[string][]db.InsertRecommendationCandidateParams, 2)
	}
	store.snapshots[snapshot.Source] = snapshot
	store.candidates[snapshot.Source] = candidates
}

// published and staged are what a reader would see and what the running chain
// has built so far.
func (store *fakeStore) published() []db.InsertRecommendationCandidateParams {
	return store.candidates[listenbrainz.Name]
}

func (store *fakeStore) staged() []db.InsertRecommendationCandidateParams {
	return store.candidates[stagedSource(listenbrainz.Name)]
}

func (store *fakeStore) publishedSnapshot() db.UpsertRecommendationSnapshotParams {
	return store.snapshots[listenbrainz.Name]
}

func (store *fakeStore) RecommendationCandidates(
	_ context.Context, source string,
) ([]db.RecommendationCandidate, error) {
	result := make([]db.RecommendationCandidate, 0, len(store.candidates[source]))
	for _, candidate := range store.candidates[source] {
		result = append(result, db.RecommendationCandidate{
			Source:                    candidate.Source,
			MusicbrainzRecordingID:    candidate.MusicbrainzRecordingID,
			MusicbrainzReleaseGroupID: candidate.MusicbrainzReleaseGroupID,
			MusicbrainzArtistIds:      append([]uuid.UUID(nil), candidate.MusicbrainzArtistIds...),
			RecordingTitle:            candidate.RecordingTitle, ReleaseTitle: candidate.ReleaseTitle,
			ArtistName: candidate.ArtistName, Rank: candidate.Rank,
			SourceScore:   candidate.SourceScore,
			ReasonCodes:   append([]string(nil), candidate.ReasonCodes...),
			ReasonContext: append([]byte(nil), candidate.ReasonContext...),
			FetchedAt:     candidate.FetchedAt,
		})
	}
	return result, nil
}

func (store *fakeStore) RecommendationCandidateCount(_ context.Context, source string) (int, error) {
	return len(store.candidates[source]), nil
}

func (store *fakeStore) ListRecommendationCandidateFacts(
	context.Context, db.ListRecommendationCandidateFactsParams,
) ([]db.ListRecommendationCandidateFactsRow, error) {
	return nil, nil
}

func (store *fakeStore) UpsertRecommendationDismissal(
	_ context.Context, params db.UpsertRecommendationDismissalParams,
) (db.RecommendationDismissal, error) {
	return db.RecommendationDismissal{
		SubjectType: params.SubjectType, MusicbrainzID: params.MusicbrainzID, DismissedAt: params.DismissedAt,
	}, nil
}

func (store *fakeStore) RecordRecommendationImpression(
	_ context.Context, params db.RecordRecommendationImpressionParams,
) (db.RecommendationImpression, error) {
	return db.RecommendationImpression{
		MusicbrainzRecordingID: params.MusicbrainzRecordingID,
		LastShownAt:            params.ObservedAt,
	}, nil
}

func (store *fakeStore) RecordRecommendationFeedback(
	_ context.Context, params db.RecordRecommendationFeedbackParams,
) (db.RecommendationFeedback, error) {
	for _, candidate := range store.candidates[params.Source] {
		if candidate.MusicbrainzRecordingID != params.MusicbrainzRecordingID {
			continue
		}
		row := db.RecommendationFeedback{
			ID:                        uuid.New(),
			Source:                    params.Source,
			MusicbrainzRecordingID:    candidate.MusicbrainzRecordingID,
			MusicbrainzReleaseGroupID: candidate.MusicbrainzReleaseGroupID,
			MusicbrainzArtistIds:      append([]uuid.UUID(nil), candidate.MusicbrainzArtistIds...),
			Signal:                    params.Signal,
			ReasonCodes:               append([]string(nil), candidate.ReasonCodes...),
			ReasonContext:             append([]byte(nil), candidate.ReasonContext...),
		}
		store.feedback = append(store.feedback, row)
		return row, nil
	}
	return db.RecommendationFeedback{}, pgx.ErrNoRows
}

func (store *fakeStore) ListRecommendationFeedback(
	_ context.Context, source string,
) ([]db.RecommendationFeedback, error) {
	result := make([]db.RecommendationFeedback, 0, len(store.feedback))
	for _, row := range store.feedback {
		if row.Source == source {
			result = append(result, row)
		}
	}
	return result, nil
}

func (store *fakeStore) DeleteRecommendationFeedback(_ context.Context, source string) error {
	kept := store.feedback[:0]
	for _, row := range store.feedback {
		if row.Source != source {
			kept = append(kept, row)
		}
	}
	store.feedback = kept
	return nil
}

func (store *fakeStore) OwnRecommendationPool(
	_ context.Context, params db.OwnRecommendationPoolParams,
) ([]db.OwnRecommendationPoolRow, error) {
	store.poolParams = append(store.poolParams, params)
	return append([]db.OwnRecommendationPoolRow(nil), store.pool...), nil
}

// Everything asked about is offered here. The rule that a recording has to be
// on a stored list before its showing counts is a database question, and it is
// proved against a database in suppression_integration_test.go.
func (store *fakeStore) ListedRecommendationRecordings(
	_ context.Context, musicBrainzRecordingIDs []uuid.UUID,
) ([]uuid.UUID, error) {
	return musicBrainzRecordingIDs, nil
}

func configured(username string) db.ListenBrainzSettingsRow {
	return db.ListenBrainzSettingsRow{
		BaseURL:             "https://api.listenbrainz.org",
		LabsURL:             "https://labs.api.listenbrainz.org",
		Username:            username,
		SimilarityAlgorithm: "session_based",
		Enabled:             true,
		ConnectionStatus:    "unknown",
	}
}

func newService(t *testing.T, store *fakeStore, handler http.HandlerFunc) *Service {
	t.Helper()
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)
	return NewService(store, "schall-test/0.0 (test)", zerolog.Nop()).
		WithEndpoints(server.URL, server.URL, server.Client())
}

type fakeRecordingProvider struct {
	recordings map[uuid.UUID]musicbrainz.Recording
	errors     map[uuid.UUID]error
	asked      []uuid.UUID
}

func (provider *fakeRecordingProvider) Recording(
	_ context.Context, recordingID uuid.UUID,
) (musicbrainz.Recording, error) {
	provider.asked = append(provider.asked, recordingID)
	if err := provider.errors[recordingID]; err != nil {
		return musicbrainz.Recording{}, err
	}
	recording, ok := provider.recordings[recordingID]
	if !ok {
		return musicbrainz.Recording{}, musicbrainz.ErrNotFound
	}
	return recording, nil
}

// storedCandidate is one candidate row as a snapshot already holds it, for a
// test that starts from a store somebody else's pass wrote.
func storedCandidate(
	source string, recordingID uuid.UUID, title string,
) db.InsertRecommendationCandidateParams {
	return db.InsertRecommendationCandidateParams{
		Source: source, MusicbrainzRecordingID: recordingID,
		MusicbrainzReleaseGroupID: uuid.New(), MusicbrainzArtistIds: []uuid.UUID{uuid.New()},
		RecordingTitle: title, ArtistName: "Portishead", Rank: 1,
		ReasonCodes: []string{"cf_raw"}, ReasonContext: []byte(`{}`),
	}
}

func expandedRecording(recordingID, releaseGroupID, artistID uuid.UUID, title string) musicbrainz.Recording {
	return musicbrainz.Recording{
		ID: recordingID, Title: title, ArtistCredit: "Portishead",
		Credits: []musicbrainz.Credit{{Name: "Portishead", ArtistID: artistID}},
		Releases: []musicbrainz.RecordingRelease{{
			ID: uuid.New(), ReleaseGroupID: releaseGroupID, Title: "Dummy",
			Status: "Official", PrimaryType: "Album",
		}},
	}
}

// Without a settings row there is nothing to ask and nothing to ask it as. Every
// path has to say so rather than answer as though the source had nothing.
func TestWithoutASettingsRowEveryPathRefuses(t *testing.T) {
	store := &fakeStore{missing: true}
	service := NewService(store, "schall-test/0.0 (test)", zerolog.Nop())

	if _, _, err := service.client(context.Background()); !errors.Is(err, ErrNotConfigured) {
		t.Fatalf("client() error = %v, want ErrNotConfigured", err)
	}
	if _, err := service.CheckConnection(context.Background()); !errors.Is(err, ErrNotConfigured) {
		t.Fatalf("CheckConnection() error = %v, want ErrNotConfigured", err)
	}
	if len(store.recorded) != 0 {
		t.Fatalf("recorded %d checks against a source nobody configured", len(store.recorded))
	}
}

func TestCheckConnectionRecordsASuccess(t *testing.T) {
	store := &fakeStore{row: configured("listener")}
	service := newService(t, store, func(response http.ResponseWriter, request *http.Request) {
		if !strings.Contains(request.URL.Path, "/user/listener/") {
			t.Errorf("asked about %q, want the stored account", request.URL.Path)
		}
		_, _ = response.Write([]byte(`{"payload":{"mbids":[],"total_mbid_count":1000}}`))
	})

	saved, err := service.CheckConnection(context.Background())
	if err != nil {
		t.Fatalf("CheckConnection() error = %v", err)
	}
	if saved.ConnectionStatus != "ok" {
		t.Fatalf("status = %q, want ok", saved.ConnectionStatus)
	}
	if len(store.recorded) != 1 || store.recorded[0].failure != "" {
		t.Fatalf("recorded = %+v, want one clean check", store.recorded)
	}
	if !strings.Contains(store.recorded[0].detail, "1000") {
		t.Errorf("detail = %q, want the batch described", store.recorded[0].detail)
	}
}

// A wrong username is a settings problem, and the page has to be able to say
// which one it was without anybody reading the server log.
func TestCheckConnectionRecordsWhyItFailed(t *testing.T) {
	store := &fakeStore{row: configured("nobody")}
	service := newService(t, store, func(response http.ResponseWriter, _ *http.Request) {
		response.WriteHeader(http.StatusNotFound)
	})

	saved, err := service.CheckConnection(context.Background())
	if err != nil {
		t.Fatalf("CheckConnection() error = %v, want the failure recorded rather than returned", err)
	}
	if saved.ConnectionStatus != "failed" {
		t.Fatalf("status = %q, want failed", saved.ConnectionStatus)
	}
	if !strings.Contains(store.recorded[0].failure, "nobody") {
		t.Fatalf("failure = %q, does not name the account", store.recorded[0].failure)
	}
}

// An account ListenBrainz has not modelled yet is connected. Reporting it as
// failed would send the user looking for a mistake they did not make.
func TestAnAccountWithNoModelYetIsConnected(t *testing.T) {
	store := &fakeStore{row: configured("listener")}
	service := newService(t, store, func(response http.ResponseWriter, _ *http.Request) {
		response.WriteHeader(http.StatusNoContent)
	})

	saved, err := service.CheckConnection(context.Background())
	if err != nil {
		t.Fatalf("CheckConnection() error = %v", err)
	}
	if saved.ConnectionStatus != "ok" {
		t.Fatalf("status = %q, want ok", saved.ConnectionStatus)
	}
	if !strings.Contains(store.recorded[0].detail, "not built a recommendation model") {
		t.Errorf("detail = %q", store.recorded[0].detail)
	}
}

// A source switched off is still checkable: `enabled` governs whether Schall
// goes asking on its own, not whether the user may verify what they typed.
func TestADisabledSourceIsStillChecked(t *testing.T) {
	row := configured("listener")
	row.Enabled = false
	store := &fakeStore{row: row}
	service := newService(t, store, func(response http.ResponseWriter, _ *http.Request) {
		_, _ = response.Write([]byte(`{"payload":{"mbids":[],"total_mbid_count":12}}`))
	})

	if _, err := service.CheckConnection(context.Background()); err != nil {
		t.Fatalf("CheckConnection() error = %v", err)
	}
	if len(store.recorded) != 1 || store.recorded[0].status != "ok" {
		t.Fatalf("recorded = %+v, want the check to have run", store.recorded)
	}
}

// The stored token is sent, and it is read from the row on every call rather
// than captured when the service was built.
func TestTheStoredTokenIsReadPerUse(t *testing.T) {
	store := &fakeStore{row: configured("listener")}
	seen := make([]string, 0, 2)
	service := newService(t, store, func(response http.ResponseWriter, request *http.Request) {
		seen = append(seen, request.Header.Get("Authorization"))
		_, _ = response.Write([]byte(`{"payload":{"mbids":[]}}`))
	})

	if _, err := service.CheckConnection(context.Background()); err != nil {
		t.Fatalf("CheckConnection() error = %v", err)
	}
	store.row.UserToken = pgtype.Text{String: "a-token", Valid: true}
	if _, err := service.CheckConnection(context.Background()); err != nil {
		t.Fatalf("CheckConnection() error = %v", err)
	}

	if seen[0] != "" {
		t.Errorf("first call sent %q without a stored token", seen[0])
	}
	if seen[1] != "Token a-token" {
		t.Errorf("second call sent %q, want the newly stored token", seen[1])
	}
}

// The whole reason for this source is that its answers are identifiers. A
// service that could not be reached is an error, never an empty answer.
func TestAnUnreachableSourceIsAFailureRatherThanSilence(t *testing.T) {
	store := &fakeStore{row: configured("listener")}
	service := newService(t, store, func(response http.ResponseWriter, _ *http.Request) {
		hijacker, ok := response.(http.Hijacker)
		if !ok {
			response.WriteHeader(http.StatusInternalServerError)
			return
		}
		connection, _, err := hijacker.Hijack()
		if err != nil {
			return
		}
		_ = connection.Close()
	})

	saved, err := service.CheckConnection(context.Background())
	if err != nil {
		t.Fatalf("CheckConnection() error = %v", err)
	}
	if saved.ConnectionStatus != "failed" || store.recorded[0].failure == "" {
		t.Fatalf("recorded = %+v, want a failure with a reason", store.recorded)
	}
}

// The service must not attribute one account's verdict to another. When the row
// moves while the check is in flight, the verdict is dropped and the caller is
// told why rather than shown a status that quietly did not change.
func TestACheckOvertakenByASaveIsDroppedAndReported(t *testing.T) {
	store := &fakeStore{row: configured("listener")}
	service := newService(t, store, func(response http.ResponseWriter, _ *http.Request) {
		// The account changes while this request is being answered.
		store.row.Username = "somebody-else"
		store.row.UpdatedAt = pgtype.Timestamptz{Time: time.Unix(1, 0), Valid: true}
		_, _ = response.Write([]byte(`{"payload":{"mbids":[],"total_mbid_count":1000}}`))
	})

	_, err := service.CheckConnection(context.Background())
	if !errors.Is(err, ErrCheckSuperseded) {
		t.Fatalf("CheckConnection() error = %v, want ErrCheckSuperseded", err)
	}
	if len(store.recorded) != 0 {
		t.Fatalf("recorded %+v, want the stale verdict dropped", store.recorded)
	}
}

// The token is for the API host. The labs dataset host asks for no
// authentication and has no use for it, so it never sees it.
func TestTheLabsHostNeverSeesTheToken(t *testing.T) {
	row := configured("listener")
	row.UserToken = pgtype.Text{String: "a-token", Valid: true}
	store := &fakeStore{row: row}

	labs := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		if got := request.Header.Get("Authorization"); got != "" {
			t.Errorf("the labs host was sent %q", got)
		}
		_, _ = response.Write([]byte(`[]`))
	}))
	t.Cleanup(labs.Close)
	api := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		if got := request.Header.Get("Authorization"); got != "Token a-token" {
			t.Errorf("the API host was sent %q", got)
		}
		_, _ = response.Write([]byte(`{"payload":{"mbids":[]}}`))
	}))
	t.Cleanup(api.Close)

	service := NewService(store, "schall-test/0.0 (test)", zerolog.Nop()).
		WithEndpoints(api.URL, labs.URL, api.Client())

	client, settings, err := service.client(context.Background())
	if err != nil {
		t.Fatalf("client() error = %v", err)
	}
	if _, err := client.Recommendations(context.Background(), settings.Username, 1, 0); err != nil {
		t.Fatalf("Recommendations() error = %v", err)
	}
	if _, err := client.SimilarRecordings(context.Background(), []string{"abc"}, "session_based"); err != nil {
		t.Fatalf("SimilarRecordings() error = %v", err)
	}
}

// A sweep composes the stored account, both ListenBrainz recommendation paths,
// MusicBrainz expansion and the atomic candidate-store boundary. It never asks
// for suppression facts; those are a read-time concern.
func TestSweepStoresTheRawExpandedSourceSnapshot(t *testing.T) {
	cfID := uuid.MustParse("00000000-0000-0000-0000-000000000011")
	similarID := uuid.MustParse("00000000-0000-0000-0000-000000000012")
	seedID := uuid.MustParse("00000000-0000-0000-0000-000000000013")
	releaseID := uuid.MustParse("10000000-0000-0000-0000-000000000011")
	artistID := uuid.MustParse("20000000-0000-0000-0000-000000000011")
	store := &fakeStore{row: configured("listener")}
	service := newService(t, store, func(response http.ResponseWriter, request *http.Request) {
		switch request.URL.Path {
		case "/1/cf/recommendation/user/listener/recording":
			_, _ = response.Write([]byte(`{"payload":{"mbids":[{"recording_mbid":"` +
				cfID.String() + `","score":0.8}],"model_id":"model-9","last_updated":1786320000}}`))
		case "/1/stats/user/listener/recordings":
			_, _ = response.Write([]byte(`{"payload":{"recordings":[{"recording_mbid":"` +
				seedID.String() + `","track_name":"Roads","artist_name":"Portishead","listen_count":42}]}}`))
		case "/similar-recordings/json":
			if got := request.URL.Query().Get("recording_mbids"); got != seedID.String() {
				t.Errorf("similarity seed = %q, want %s", got, seedID)
			}
			_, _ = response.Write([]byte(`[{"recording_mbid":"` + similarID.String() +
				`","score":0.7,"reference_mbid":"` + seedID.String() + `"}]`))
		default:
			http.NotFound(response, request)
		}
	})
	service.WithRecordingProvider(&fakeRecordingProvider{recordings: map[uuid.UUID]musicbrainz.Recording{
		cfID:      expandedRecording(cfID, releaseID, artistID, "Glory Box"),
		similarID: expandedRecording(similarID, releaseID, artistID, "Wandering Star"),
	}})

	more, err := service.Sweep(context.Background())
	if err != nil {
		t.Fatalf("Sweep() error = %v", err)
	}
	if more {
		t.Fatal("Sweep() reported work left after fetching the complete source snapshot")
	}
	snapshot := store.publishedSnapshot()
	if snapshot.Source != "listenbrainz" || snapshot.Status != "complete" ||
		snapshot.SourceSnapshotID.String != "model-9" {
		t.Fatalf("snapshot = %#v, writes = %d", snapshot, store.recommendationWrites)
	}
	if len(store.published()) != 2 {
		t.Fatalf("stored %d candidates, want both source paths", len(store.published()))
	}
	reasons := make(map[uuid.UUID][]string, len(store.published()))
	for _, candidate := range store.published() {
		reasons[candidate.MusicbrainzRecordingID] = candidate.ReasonCodes
	}
	if !reflect.DeepEqual(reasons[cfID], []string{"cf_raw"}) ||
		!reflect.DeepEqual(reasons[similarID], []string{"similar_to"}) {
		t.Fatalf("reason codes = %v", reasons)
	}
}

func TestSweepUsesRecordingSeedsWhenTheAccountHasNoCFModelYet(t *testing.T) {
	seedID := uuid.MustParse("00000000-0000-0000-0000-000000000021")
	recordingID := uuid.MustParse("00000000-0000-0000-0000-000000000022")
	releaseID := uuid.MustParse("10000000-0000-0000-0000-000000000021")
	artistID := uuid.MustParse("20000000-0000-0000-0000-000000000021")
	store := &fakeStore{row: configured("listener")}
	service := newService(t, store, func(response http.ResponseWriter, request *http.Request) {
		switch request.URL.Path {
		case "/1/cf/recommendation/user/listener/recording":
			response.WriteHeader(http.StatusNoContent)
		case "/1/stats/user/listener/recordings":
			_, _ = response.Write([]byte(`{"payload":{"recordings":[{"recording_mbid":"` +
				seedID.String() + `"}]}}`))
		case "/similar-recordings/json":
			_, _ = response.Write([]byte(`[{"recording_mbid":"` + recordingID.String() +
				`","score":0.6,"reference_mbid":"` + seedID.String() + `"}]`))
		default:
			http.NotFound(response, request)
		}
	}).WithRecordingProvider(&fakeRecordingProvider{recordings: map[uuid.UUID]musicbrainz.Recording{
		recordingID: expandedRecording(recordingID, releaseID, artistID, "The Rip"),
	}})

	if _, err := service.Sweep(context.Background()); err != nil {
		t.Fatalf("Sweep() error = %v", err)
	}
	if len(store.published()) != 1 ||
		!reflect.DeepEqual(store.published()[0].ReasonCodes, []string{"similar_to"}) {
		t.Fatalf("candidates = %#v, want the seed-and-hop answer", store.published())
	}
}

func TestSweepKeepsAUsablePrimaryAnswerWhenSeedFanoutDegrades(t *testing.T) {
	recordingID := uuid.MustParse("00000000-0000-0000-0000-000000000031")
	seedID := uuid.MustParse("00000000-0000-0000-0000-000000000032")
	releaseID := uuid.MustParse("10000000-0000-0000-0000-000000000031")
	artistID := uuid.MustParse("20000000-0000-0000-0000-000000000031")
	store := &fakeStore{row: configured("listener")}
	service := newService(t, store, func(response http.ResponseWriter, request *http.Request) {
		switch request.URL.Path {
		case "/1/cf/recommendation/user/listener/recording":
			_, _ = response.Write([]byte(`{"payload":{"mbids":[{"recording_mbid":"` +
				recordingID.String() + `","score":0.9}]}}`))
		case "/1/stats/user/listener/recordings":
			_, _ = response.Write([]byte(`{"payload":{"recordings":[{"recording_mbid":"` +
				seedID.String() + `"}]}}`))
		case "/similar-recordings/json":
			response.WriteHeader(http.StatusServiceUnavailable)
		default:
			http.NotFound(response, request)
		}
	}).WithRecordingProvider(&fakeRecordingProvider{recordings: map[uuid.UUID]musicbrainz.Recording{
		recordingID: expandedRecording(recordingID, releaseID, artistID, "Sour Times"),
	}})

	if _, err := service.Sweep(context.Background()); err != nil {
		t.Fatalf("Sweep() error = %v", err)
	}
	snapshot := store.publishedSnapshot()
	if snapshot.Status != "partial" || !strings.Contains(snapshot.Detail, "fan-out stopped") ||
		len(store.published()) != 1 {
		t.Fatalf("snapshot = %#v candidates = %#v, want an explained usable partial",
			snapshot, store.published())
	}
}

// degradedFanoutChain is an account whose list needs two passes, where the
// similarity service refuses the one fan-out call the chain's first pass makes.
// The recommendation page is there and every record on it can be expanded, so
// the chain walks a shorter answer than it should have had.
func degradedFanoutChain(t *testing.T) (*Service, *fakeStore) {
	t.Helper()
	store := &fakeStore{row: configured("listener")}
	recordings := make(map[uuid.UUID]musicbrainz.Recording, maximumCandidateExpansions+2)
	page := make([]map[string]any, 0, maximumCandidateExpansions+2)
	for range maximumCandidateExpansions + 2 {
		recordingID := uuid.New()
		recordings[recordingID] = expandedRecording(recordingID, uuid.New(), uuid.New(), "Page candidate")
		page = append(page, map[string]any{
			"recording_mbid": recordingID.String(), "score": float64(len(page) + 1),
		})
	}
	service := newService(t, store, func(response http.ResponseWriter, request *http.Request) {
		switch request.URL.Path {
		case "/1/cf/recommendation/user/listener/recording":
			_ = json.NewEncoder(response).Encode(map[string]any{"payload": map[string]any{
				"mbids": page, "model_id": "degraded-model",
			}})
		case "/1/stats/user/listener/recordings":
			_ = json.NewEncoder(response).Encode(map[string]any{"payload": map[string]any{
				"recordings": []map[string]any{{"recording_mbid": uuid.New().String()}},
			}})
		case "/similar-recordings/json":
			response.WriteHeader(http.StatusServiceUnavailable)
		default:
			http.NotFound(response, request)
		}
	})
	return service.WithRecordingProvider(&fakeRecordingProvider{recordings: recordings}), store
}

// One pass of a chain asks ListenBrainz what the account is offered, and the
// passes after it work from that one answer. So the pass that publishes the
// snapshot is usually not the pass that found out a service was quiet, and it
// has to say so anyway: a list that is short because half of it could not be
// fetched must not be published as a whole one.
func TestSweepPublishesWhyTheOneFetchWasShort(t *testing.T) {
	service, store := degradedFanoutChain(t)

	first, err := service.SweepPage(context.Background(), SweepPosition{})
	if err != nil {
		t.Fatalf("first SweepPage() error = %v", err)
	}
	second, err := service.SweepPage(context.Background(), first.Position)
	if err != nil {
		t.Fatalf("second SweepPage() error = %v", err)
	}

	if !second.Report.Published {
		t.Fatalf("second result = %#v, want the chain finished", second)
	}
	snapshot := store.publishedSnapshot()
	if snapshot.Status != "partial" || !strings.Contains(snapshot.Detail, "fan-out stopped") {
		t.Fatalf("snapshot = %#v, want the published list to say what it is missing", snapshot)
	}
}

// A pass that stopped because a service would not answer is one the worker comes
// back to sooner than usual. Only the pass that did the asking can report that:
// a chain carries the reason its answer is short from pass to pass, and a later
// pass reading it must not report an outage of its own and buy itself a wait it
// has no use for.
func TestSweepDoesNotReportAnEarlierPassesOutageAsItsOwn(t *testing.T) {
	service, _ := degradedFanoutChain(t)

	first, err := service.SweepPage(context.Background(), SweepPosition{})
	if err != nil {
		t.Fatalf("first SweepPage() error = %v", err)
	}
	if !first.Report.ProviderFailed {
		t.Fatalf("first report = %#v, want the pass that was refused to say so", first.Report)
	}

	second, err := service.SweepPage(context.Background(), first.Position)
	if err != nil {
		t.Fatalf("second SweepPage() error = %v", err)
	}
	if second.Report.ProviderFailed {
		t.Fatalf("second report = %#v, want a pass nobody refused to report no outage", second.Report)
	}
}

// A pass says what it did. Until it did, the sweep talked to two services for
// several minutes and wrote nothing anybody could read, so somebody who had just
// connected an account could not tell a pass that found records from a pass that
// never ran. Offered is what ListenBrainz named, asked is what this pass looked
// up, and stored is what survived: MusicBrainz knows the second recording here
// but neither its release nor its artist, so it is asked about and dropped.
func TestSweepReportsWhatThePassDid(t *testing.T) {
	kept := uuid.MustParse("00000000-0000-0000-0000-000000000041")
	dropped := uuid.MustParse("00000000-0000-0000-0000-000000000042")
	store := &fakeStore{row: configured("listener")}
	service := newService(t, store, func(response http.ResponseWriter, request *http.Request) {
		switch request.URL.Path {
		case "/1/cf/recommendation/user/listener/recording":
			_, _ = response.Write([]byte(`{"payload":{"model_id":"a-model","mbids":[` +
				`{"recording_mbid":"` + kept.String() + `","score":0.9},` +
				`{"recording_mbid":"` + dropped.String() + `","score":0.8}]}}`))
		case "/1/stats/user/listener/recordings":
			response.WriteHeader(http.StatusNoContent)
		default:
			http.NotFound(response, request)
		}
	}).WithRecordingProvider(&fakeRecordingProvider{recordings: map[uuid.UUID]musicbrainz.Recording{
		kept: expandedRecording(kept, uuid.New(), uuid.New(), "Sour Times"),
		dropped: {
			ID: dropped, Title: "Nothing MusicBrainz can place", ArtistCredit: "Portishead",
		},
	}})

	result, err := service.SweepPage(context.Background(), SweepPosition{})
	if err != nil {
		t.Fatalf("SweepPage() error = %v", err)
	}
	want := SweepReport{
		SnapshotID: "a-model", Offered: 2, Asked: 2, Stored: 1, Status: "complete", Published: true,
	}
	if result.Report != want {
		t.Fatalf("report = %#v, want %#v", result.Report, want)
	}
}

// The two reasons a pass ends early are not the same fact. This one stopped
// because MusicBrainz refused, which is an outage the pass should be tried again
// after, and it says so.
func TestSweepReportsAPassAServiceInterrupted(t *testing.T) {
	kept := uuid.MustParse("00000000-0000-0000-0000-000000000043")
	refused := uuid.MustParse("00000000-0000-0000-0000-000000000044")
	store := &fakeStore{row: configured("listener")}
	service := newService(t, store, func(response http.ResponseWriter, request *http.Request) {
		switch request.URL.Path {
		case "/1/cf/recommendation/user/listener/recording":
			_, _ = response.Write([]byte(`{"payload":{"mbids":[` +
				`{"recording_mbid":"` + kept.String() + `","score":0.9},` +
				`{"recording_mbid":"` + refused.String() + `","score":0.8}]}}`))
		case "/1/stats/user/listener/recordings":
			response.WriteHeader(http.StatusNoContent)
		default:
			http.NotFound(response, request)
		}
	}).WithRecordingProvider(&fakeRecordingProvider{
		recordings: map[uuid.UUID]musicbrainz.Recording{
			kept: expandedRecording(kept, uuid.New(), uuid.New(), "Sour Times"),
		},
		errors: map[uuid.UUID]error{refused: errors.New("musicbrainz did not respond")},
	})

	result, err := service.SweepPage(context.Background(), SweepPosition{})
	if err != nil {
		t.Fatalf("SweepPage() error = %v", err)
	}
	if !result.Report.ProviderFailed || result.Report.Status != "partial" {
		t.Fatalf("report = %#v, want a partial pass a service interrupted", result.Report)
	}
	if result.Report.Stored != 1 {
		t.Errorf("stored = %d, want the one candidate that was expanded before the refusal",
			result.Report.Stored)
	}
}

// A pass that walked as far as its bound allows is working as intended. It is a
// partial snapshot too, so the report has to separate it from the pass an outage
// cut short, or every ordinary bounded sweep would be treated as a failure.
func TestSweepDoesNotCallItsOwnBoundAServiceFailure(t *testing.T) {
	store := &fakeStore{row: configured("listener")}
	recordings := make(map[uuid.UUID]musicbrainz.Recording, maximumCandidateExpansions+1)
	answer := make([]map[string]any, 0, maximumCandidateExpansions+1)
	for range maximumCandidateExpansions + 1 {
		recordingID := uuid.New()
		recordings[recordingID] = expandedRecording(
			recordingID, uuid.New(), uuid.New(), "Bounded candidate",
		)
		answer = append(answer, map[string]any{
			"recording_mbid": recordingID.String(), "score": float64(len(answer) + 1),
		})
	}
	service := newService(t, store, func(response http.ResponseWriter, request *http.Request) {
		switch request.URL.Path {
		case "/1/cf/recommendation/user/listener/recording":
			_ = json.NewEncoder(response).Encode(map[string]any{"payload": map[string]any{"mbids": answer}})
		case "/1/stats/user/listener/recordings":
			response.WriteHeader(http.StatusNoContent)
		default:
			http.NotFound(response, request)
		}
	}).WithRecordingProvider(&fakeRecordingProvider{recordings: recordings})

	result, err := service.SweepPage(context.Background(), SweepPosition{})
	if err != nil {
		t.Fatalf("SweepPage() error = %v", err)
	}
	if !result.More {
		t.Fatalf("result = %#v, want the pass to have more waiting", result)
	}
	if result.Report.ProviderFailed {
		t.Errorf("report = %#v, want a bounded pass reported as no outage", result.Report)
	}
}

// The primary source failing is a failed pass, never a successful empty
// replacement. This is what keeps the last usable snapshot visible through an
// outage.
func TestSweepPreservesTheSnapshotWhenTheSourceCannotBeReached(t *testing.T) {
	store := &fakeStore{row: configured("listener")}
	service := newService(t, store, func(response http.ResponseWriter, _ *http.Request) {
		response.WriteHeader(http.StatusServiceUnavailable)
	}).WithRecordingProvider(&fakeRecordingProvider{})

	if _, err := service.Sweep(context.Background()); err == nil {
		t.Fatal("Sweep() succeeded while ListenBrainz was unreachable")
	}
	if store.recommendationWrites != 0 {
		t.Fatalf("wrote %d snapshots during an outage", store.recommendationWrites)
	}
}

// halfSweptAccount is an account whose list needs more than one pass, with a
// complete list already published from yesterday and one recording MusicBrainz
// will not answer about in the middle of the first pass. Clear the provider's
// errors for a chain nothing interrupts. The last return is the offered
// recordings in the order the passes walk them.
func halfSweptAccount(
	t *testing.T,
) (*Service, *fakeStore, *fakeRecordingProvider, []uuid.UUID) {
	t.Helper()
	store := &fakeStore{row: configured("listener")}
	store.write(
		db.UpsertRecommendationSnapshotParams{Source: listenbrainz.Name, Status: "complete"},
		[]db.InsertRecommendationCandidateParams{
			storedCandidate(listenbrainz.Name, uuid.New(), "Yesterday's list")},
	)
	offered := maximumCandidateExpansions + 5
	recordings := make(map[uuid.UUID]musicbrainz.Recording, offered)
	answer := make([]map[string]any, 0, offered)
	order := make([]uuid.UUID, 0, offered)
	var refused uuid.UUID
	for index := range offered {
		recordingID := uuid.New()
		recordings[recordingID] = expandedRecording(
			recordingID, uuid.New(), uuid.New(), "Offered candidate",
		)
		order = append(order, recordingID)
		if index == halfSweptRefusalAt {
			refused = recordingID
		}
		// Scored downwards, so the walk takes them in the order they were built and
		// the refusal falls inside the first bounded pass.
		answer = append(answer, map[string]any{
			"recording_mbid": recordingID.String(), "score": float64(offered - index),
		})
	}
	service := newService(t, store, func(response http.ResponseWriter, request *http.Request) {
		switch request.URL.Path {
		case "/1/cf/recommendation/user/listener/recording":
			_ = json.NewEncoder(response).Encode(map[string]any{"payload": map[string]any{
				"mbids": answer, "model_id": "half-swept-model",
			}})
		case "/1/stats/user/listener/recordings":
			response.WriteHeader(http.StatusNoContent)
		default:
			http.NotFound(response, request)
		}
	})
	provider := &fakeRecordingProvider{
		recordings: recordings,
		errors:     map[uuid.UUID]error{refused: errors.New("musicbrainz did not respond in time")},
	}
	service.WithRecordingProvider(provider)
	return service, store, provider, order
}

// halfSweptRefusalAt is which of the offered recordings MusicBrainz refuses, and
// so how many the interrupted pass stores before it stops.
const halfSweptRefusalAt = 9

// The list somebody reads is replaced when a new one is whole, never while one
// is being built. A sweep of a real account takes about half an hour of bounded
// passes, and until this the first of those passes replaced the published list
// with its own 25 records, so the list was a stub for the rest of the sweep
// (issue #305).
func TestSweepLeavesThePublishedListAloneWhileAChainIsStillBuilding(t *testing.T) {
	service, store, provider, _ := halfSweptAccount(t)
	provider.errors = nil

	result, err := service.SweepPage(context.Background(), SweepPosition{})
	if err != nil {
		t.Fatalf("SweepPage() error = %v", err)
	}
	if !result.More {
		t.Fatalf("result = %#v, want a bounded pass with more to do", result)
	}
	if len(store.published()) != 1 || store.published()[0].RecordingTitle != "Yesterday's list" {
		t.Fatalf("published = %#v, want yesterday's complete list still in front of readers",
			store.published())
	}
	if len(store.staged()) != maximumCandidateExpansions {
		t.Fatalf("built = %d, want this pass's %d records held back",
			len(store.staged()), maximumCandidateExpansions)
	}
}

// A chain is one sweep and all its continuations. When a service it asks stops
// answering, what the chain has already stored is kept where no reader sees it,
// so the next pass carries on instead of asking about the same records again.
// One MusicBrainz request over the client timeout cost a live chain 1006 of its
// 1298 records before this (issue #305).
func TestSweepKeepsWhatAChainBuiltWhenAServiceStopsAnswering(t *testing.T) {
	service, store, _, _ := halfSweptAccount(t)

	result, err := service.SweepPage(context.Background(), SweepPosition{})
	if err != nil {
		t.Fatalf("SweepPage() error = %v", err)
	}
	if !result.Resume || !result.Position.HasCandidates {
		t.Fatalf("result = %#v, want a chain to be carried on from where it stopped", result)
	}
	if len(store.staged()) != halfSweptRefusalAt {
		t.Fatalf("built = %d, want the %d records stored before the refusal kept",
			len(store.staged()), halfSweptRefusalAt)
	}
	if len(store.published()) != 1 || store.published()[0].RecordingTitle != "Yesterday's list" {
		t.Fatalf("published = %#v, want the previous complete list untouched", store.published())
	}
}

// A pass comes back at once while records are left, and waits only when the
// service it needs would not answer it at all. MusicBrainz answered this pass
// nine times and was then slow once, which is the ordinary shape of a public
// rate-limited service asked over a ten-second timeout. The nine records it
// stored are the evidence the service is there, so the pass is a continuation
// like any other. Reading it as an outage held the chain half an hour for every
// slow request, and an account of 1298 records could not be swept in a day
// (issue #310).
func TestSweepComesStraightBackWhenAPassStoredRecordsBeforeARefusal(t *testing.T) {
	service, _, _, _ := halfSweptAccount(t)

	result, err := service.SweepPage(context.Background(), SweepPosition{})
	if err != nil {
		t.Fatalf("SweepPage() error = %v", err)
	}
	if !result.More || !result.Report.ProviderFailed {
		t.Fatalf("more = %t, report = %#v, want the %d records it stored to make it a "+
			"continuation, with the failed look-up still reported",
			result.More, result.Report, halfSweptRefusalAt)
	}
}

// The other side of the same rule. This pass put one question to MusicBrainz and
// was refused, so it has been answered about nothing and has nothing to say the
// service is there. The chain is kept, and the pass after it waits for the outage
// rather than running now.
func TestSweepWaitsWhenAPassWasAnsweredAboutNothingBeforeARefusal(t *testing.T) {
	service, _, provider, order := halfSweptAccount(t)
	provider.errors = nil
	first, err := service.SweepPage(context.Background(), SweepPosition{})
	if err != nil {
		t.Fatalf("first SweepPage() error = %v", err)
	}
	provider.errors = map[uuid.UUID]error{
		order[maximumCandidateExpansions]: errors.New("musicbrainz did not respond in time"),
	}

	result, err := service.SweepPage(context.Background(), first.Position)
	if err != nil {
		t.Fatalf("SweepPage() error = %v", err)
	}
	if result.More || !result.Report.ProviderFailed {
		t.Fatalf("more = %t, report = %#v, want a pass that reached nobody to wait for the outage",
			result.More, result.Report)
	}
}

// MusicBrainz answers about a recording and the pass can store nothing from it
// whenever the release group or the artist credit behind that recording is
// missing, and a recording it has never heard of is an answer of the same kind.
// A pass whose every answer was one of those, and which then met a look-up that
// failed, stored nothing at all — but MusicBrainz was there for it every time,
// so it is a continuation like any other and its successor runs now. Reading it
// as an outage cost the chain half an hour nobody's service was down for
// (issue #314).
func TestSweepComesStraightBackWhenEveryAnswerWasUnusable(t *testing.T) {
	service, store, provider, order := halfSweptAccount(t)
	provider.errors = nil
	first, err := service.SweepPage(context.Background(), SweepPosition{})
	if err != nil {
		t.Fatalf("first SweepPage() error = %v", err)
	}
	// The three records this pass reaches first are ones MusicBrainz does not
	// know, so it answers about each of them and the pass keeps none.
	for _, recordingID := range order[maximumCandidateExpansions : maximumCandidateExpansions+3] {
		delete(provider.recordings, recordingID)
	}
	// The fourth is where it stops answering.
	provider.errors = map[uuid.UUID]error{
		order[maximumCandidateExpansions+3]: errors.New("musicbrainz did not respond in time"),
	}

	result, err := service.SweepPage(context.Background(), first.Position)
	if err != nil {
		t.Fatalf("SweepPage() error = %v", err)
	}
	if len(store.staged()) != maximumCandidateExpansions {
		t.Fatalf("built = %d, want the pass to have stored nothing of its own on top of the "+
			"first pass's %d", len(store.staged()), maximumCandidateExpansions)
	}
	if !result.More || result.Report.Asked != 3 {
		t.Fatalf("more = %t, report = %#v, want the three answers to make it a continuation "+
			"although the pass stored none of them", result.More, result.Report)
	}
}

func TestSweepFinishesAChainAServiceInterrupted(t *testing.T) {
	service, store, provider, _ := halfSweptAccount(t)
	interrupted, err := service.SweepPage(context.Background(), SweepPosition{})
	if err != nil {
		t.Fatalf("interrupted SweepPage() error = %v", err)
	}
	provider.errors = nil

	result, err := service.SweepPage(context.Background(), interrupted.Position)
	if err != nil {
		t.Fatalf("resumed SweepPage() error = %v", err)
	}
	if result.More || result.Resume || !result.Report.Published {
		t.Fatalf("result = %#v, want the chain finished and published", result)
	}
	offered := maximumCandidateExpansions + 5
	if len(store.published()) != offered {
		t.Fatalf("published = %d records, want all %d the source offered",
			len(store.published()), offered)
	}
	if len(store.staged()) != 0 {
		t.Fatalf("staged = %#v, want nothing left beside the published list", store.staged())
	}
	// One record is asked about twice: the one the refusal landed on, which the
	// first pass learned nothing about and so had to put the question again.
	if len(provider.asked) != offered+1 {
		t.Fatalf("expansion requests = %d, want %d — the second pass repeated the whole first pass",
			len(provider.asked), offered+1)
	}
}

// An outage that is still going when the next pass starts refuses the very first
// record that pass asks about, so the pass stores nothing of its own. A pass with
// nothing of its own and nothing behind it is a failed pass, but this one has a
// chain of stored records behind it, and failing would end that chain and throw
// them away.
func TestSweepKeepsAChainWhoseNextPassCannotAskAboutAnything(t *testing.T) {
	service, store, provider, order := halfSweptAccount(t)
	provider.errors = nil
	first, err := service.SweepPage(context.Background(), SweepPosition{})
	if err != nil {
		t.Fatalf("first SweepPage() error = %v", err)
	}
	provider.errors = map[uuid.UUID]error{
		order[maximumCandidateExpansions]: errors.New("musicbrainz did not respond in time"),
	}

	result, err := service.SweepPage(context.Background(), first.Position)
	if err != nil {
		t.Fatalf("SweepPage() error = %v, want the chain kept rather than failed", err)
	}
	if !result.Resume || !result.Report.ProviderFailed {
		t.Fatalf("result = %#v, want an outage the next pass carries on from", result)
	}
	if len(store.staged()) != maximumCandidateExpansions {
		t.Fatalf("built = %d, want the first pass's %d records still held",
			len(store.staged()), maximumCandidateExpansions)
	}
}

// listenerAnswer builds one account's recommendation page: count records
// MusicBrainz knows about, scored downwards so the walk takes them in the order
// they were built. It adds them to recordings, which the expansion provider
// answers from, and returns the page and the order.
func listenerAnswer(
	recordings map[uuid.UUID]musicbrainz.Recording, count int, title string,
) ([]map[string]any, []uuid.UUID) {
	answer := make([]map[string]any, 0, count)
	order := make([]uuid.UUID, 0, count)
	for index := range count {
		recordingID := uuid.New()
		recordings[recordingID] = expandedRecording(recordingID, uuid.New(), uuid.New(), title)
		order = append(order, recordingID)
		answer = append(answer, map[string]any{
			"recording_mbid": recordingID.String(), "score": float64(count - index),
		})
	}
	return answer, order
}

// holds reports whether the published list names this recording.
func holds(published []db.InsertRecommendationCandidateParams, recordingID uuid.UUID) bool {
	for _, candidate := range published {
		if candidate.MusicbrainzRecordingID == recordingID {
			return true
		}
	}
	return false
}

// A chain asks ListenBrainz once and carries what it was told from one pass to
// the next, so for about half an hour there are records in the job payload that
// belong to the account the first pass read.
//
// Saving a different account queues a sweep, and that ask moves the pass already
// on the books forward rather than replacing it. Until this the continuation
// walked the previous account's records to the end and published them under the
// new account's name, where every "Not interested" pressed against them was a
// permanent decision about somebody else's music (issue #328).
func TestAChainDropsTheRecordsItFetchedForAnAccountNobodyNamesAnyMore(t *testing.T) {
	store := &fakeStore{row: configured("alice")}
	recordings := make(map[uuid.UUID]musicbrainz.Recording)
	// Longer than one bounded pass, so the chain is still carrying records when
	// the account changes under it.
	aliceAnswer, aliceOrder := listenerAnswer(recordings, maximumCandidateExpansions+5, "Alice's taste")
	bobAnswer, bobOrder := listenerAnswer(recordings, 3, "Bob's taste")
	service := newService(t, store, func(response http.ResponseWriter, request *http.Request) {
		switch request.URL.Path {
		case "/1/cf/recommendation/user/alice/recording":
			_ = json.NewEncoder(response).Encode(map[string]any{"payload": map[string]any{
				"mbids": aliceAnswer, "model_id": "alice-model",
			}})
		case "/1/cf/recommendation/user/bob/recording":
			_ = json.NewEncoder(response).Encode(map[string]any{"payload": map[string]any{
				"mbids": bobAnswer, "model_id": "bob-model",
			}})
		case "/1/stats/user/alice/recordings", "/1/stats/user/bob/recordings":
			response.WriteHeader(http.StatusNoContent)
		default:
			http.NotFound(response, request)
		}
	})
	service.WithRecordingProvider(&fakeRecordingProvider{recordings: recordings})

	first, err := service.SweepPage(context.Background(), SweepPosition{})
	if err != nil {
		t.Fatalf("first SweepPage() error = %v", err)
	}
	if !first.Resume || first.Report.Published {
		t.Fatalf("first pass = %#v, want a chain still building", first)
	}

	store.row = configured("bob")

	result, err := service.SweepPage(context.Background(), first.Position)
	if err != nil {
		t.Fatalf("SweepPage() after the account changed: %v", err)
	}
	if !result.Report.Published {
		t.Fatalf("result = %#v, want the new account's list published", result)
	}
	if len(store.published()) != len(bobOrder) {
		t.Fatalf("published = %d records, want Bob's %d", len(store.published()), len(bobOrder))
	}
	for _, recordingID := range bobOrder {
		if !holds(store.published(), recordingID) {
			t.Fatalf("published %d records without Bob's %s",
				len(store.published()), recordingID)
		}
	}
	for _, recordingID := range aliceOrder {
		if holds(store.published(), recordingID) {
			t.Fatalf("published names Alice's record %s under Bob's account", recordingID)
		}
	}
	if len(store.staged()) != 0 {
		t.Fatalf("staged = %d records, want what Alice's chain built discarded", len(store.staged()))
	}
}

// The similarity algorithm is part of the account: it says which answer the labs
// service is asked for, and saving a different one is saving a different
// question. A chain carrying the answer to the old question has to ask again.
func TestAChainDropsTheRecordsItFetchedUnderAnAlgorithmNobodyAsksForAnyMore(t *testing.T) {
	store := &fakeStore{row: configured("listener")}
	recordings := make(map[uuid.UUID]musicbrainz.Recording)
	page, _ := listenerAnswer(recordings, maximumCandidateExpansions+5, "Page candidate")
	sessionBased, sessionOrder := listenerAnswer(recordings, 1, "Session-based neighbour")
	byDays, daysOrder := listenerAnswer(recordings, 1, "Day-window neighbour")
	seedID := uuid.New()
	// Scored below every page candidate, so the neighbour sorts last and no pass
	// reaches it until the page is walked.
	neighbour := func(answer []map[string]any) []map[string]any {
		return []map[string]any{{
			"recording_mbid": answer[0]["recording_mbid"], "reference_mbid": seedID.String(), "score": 0.5,
		}}
	}
	service := newService(t, store, func(response http.ResponseWriter, request *http.Request) {
		switch request.URL.Path {
		case "/1/cf/recommendation/user/listener/recording":
			_ = json.NewEncoder(response).Encode(map[string]any{"payload": map[string]any{
				"mbids": page, "model_id": "one-account-model",
			}})
		case "/1/stats/user/listener/recordings":
			_ = json.NewEncoder(response).Encode(map[string]any{"payload": map[string]any{
				"recordings": []map[string]any{{"recording_mbid": seedID.String()}},
			}})
		case "/similar-recordings/json":
			if request.URL.Query().Get("algorithm") == "session_based" {
				_ = json.NewEncoder(response).Encode(neighbour(sessionBased))
				return
			}
			_ = json.NewEncoder(response).Encode(neighbour(byDays))
		default:
			http.NotFound(response, request)
		}
	})
	service.WithRecordingProvider(&fakeRecordingProvider{recordings: recordings})

	first, err := service.SweepPage(context.Background(), SweepPosition{})
	if err != nil {
		t.Fatalf("first SweepPage() error = %v", err)
	}
	if !first.Resume {
		t.Fatalf("first pass = %#v, want a chain still building", first)
	}

	changed := configured("listener")
	changed.SimilarityAlgorithm = "days_9000"
	store.row = changed

	result := first
	for pass := 2; !result.Report.Published; pass++ {
		if pass > maximumSweepPasses {
			t.Fatalf("the chain ran %d passes without ending", pass)
		}
		if result, err = service.SweepPage(context.Background(), result.Position); err != nil {
			t.Fatalf("pass %d SweepPage() error = %v", pass, err)
		}
	}

	if !holds(store.published(), daysOrder[0]) {
		t.Fatalf("published %d records without %s, the neighbour the new algorithm named",
			len(store.published()), daysOrder[0])
	}
	if holds(store.published(), sessionOrder[0]) {
		t.Fatalf("published names %s, the neighbour only the old algorithm named", sessionOrder[0])
	}
}

// ListenBrainz answers a sweep from two services, and either can go quiet
// between two passes of the same chain. Here the recommendation page is there
// throughout and the similarity service answers the first pass and no other,
// taking with it thirty records the chain had been offered and had not reached
// yet.
//
// A chain that asked again every pass could only wait for that service to come
// back, and gave the day up if it did not. A chain that is given the answer once
// still holds those thirty records and looks every one of them up.
func TestSweepFinishesTheRecordsAServiceNamedBeforeItWentQuiet(t *testing.T) {
	yesterday := uuid.New()
	store := &fakeStore{row: configured("listener")}
	store.write(
		db.UpsertRecommendationSnapshotParams{Source: listenbrainz.Name, Status: "complete"},
		[]db.InsertRecommendationCandidateParams{
			storedCandidate(listenbrainz.Name, yesterday, "Yesterday's list")},
	)
	recordings := make(map[uuid.UUID]musicbrainz.Recording)
	page := make([]map[string]any, 0, maximumCandidateExpansions+2)
	for range maximumCandidateExpansions + 2 {
		recordingID := uuid.New()
		recordings[recordingID] = expandedRecording(recordingID, uuid.New(), uuid.New(), "Page candidate")
		page = append(page, map[string]any{
			"recording_mbid": recordingID.String(), "score": float64(len(page) + 1),
		})
	}
	seedID := uuid.New()
	similar := make([]map[string]any, 0, 30)
	for range 30 {
		recordingID := uuid.New()
		recordings[recordingID] = expandedRecording(recordingID, uuid.New(), uuid.New(), "Similar candidate")
		// Scored below every page candidate, so they sort last and the first pass
		// leaves every one of them for the passes after it.
		similar = append(similar, map[string]any{
			"recording_mbid": recordingID.String(), "reference_mbid": seedID.String(), "score": 0.5,
		})
	}
	// The similarity service answers the first thing that asks it and nothing
	// after that. A chain that asked it again would be told there are no similar
	// recordings, which is a valid answer and not one anybody can tell from the
	// account having none.
	fanouts := 0
	service := newService(t, store, func(response http.ResponseWriter, request *http.Request) {
		switch request.URL.Path {
		case "/1/cf/recommendation/user/listener/recording":
			_ = json.NewEncoder(response).Encode(map[string]any{"payload": map[string]any{
				"mbids": page, "model_id": "two-service-model",
			}})
		case "/1/stats/user/listener/recordings":
			_ = json.NewEncoder(response).Encode(map[string]any{"payload": map[string]any{
				"recordings": []map[string]any{{"recording_mbid": seedID.String()}},
			}})
		case "/similar-recordings/json":
			fanouts++
			if fanouts > 1 {
				response.WriteHeader(http.StatusNoContent)
				return
			}
			_ = json.NewEncoder(response).Encode(similar)
		default:
			http.NotFound(response, request)
		}
	})
	service.WithRecordingProvider(&fakeRecordingProvider{recordings: recordings})

	result, err := service.SweepPage(context.Background(), SweepPosition{})
	if err != nil {
		t.Fatalf("first SweepPage() error = %v", err)
	}
	for pass := 2; !result.Report.Published; pass++ {
		if pass > maximumSweepPasses {
			t.Fatalf("the chain ran %d passes without ending", pass)
		}
		if !result.Resume {
			t.Fatalf("pass %d = %#v, want the chain carried on to its end", pass-1, result)
		}
		if len(store.published()) != 1 || store.published()[0].MusicbrainzRecordingID != yesterday {
			t.Fatalf("pass %d published %#v, want yesterday's list until this one is whole",
				pass-1, store.published())
		}
		if result, err = service.SweepPage(context.Background(), result.Position); err != nil {
			t.Fatalf("pass %d SweepPage() error = %v", pass, err)
		}
	}
	if len(store.published()) != len(recordings) {
		t.Fatalf("published = %d records, want all %d both services named",
			len(store.published()), len(recordings))
	}
}

// The recommendation page is the primary of the two services ListenBrainz
// answers a sweep from. Here it is there for the first pass of the chain and gone
// from every pass after it, which a chain that asked again each pass could not
// tell from an account with nothing to recommend. One fetch per chain means the
// page going quiet reaches no pass but the one that would have asked, and the
// chain finishes the answer it already holds.
func TestSweepFinishesAChainAfterThePageItAskedForGoesQuiet(t *testing.T) {
	yesterday := uuid.New()
	store := &fakeStore{row: configured("listener")}
	store.write(
		db.UpsertRecommendationSnapshotParams{Source: listenbrainz.Name, Status: "complete"},
		[]db.InsertRecommendationCandidateParams{
			storedCandidate(listenbrainz.Name, yesterday, "Yesterday's list")},
	)
	recordings := make(map[uuid.UUID]musicbrainz.Recording, maximumCandidateExpansions+2)
	answer := make([]map[string]any, 0, maximumCandidateExpansions+2)
	for range maximumCandidateExpansions + 2 {
		recordingID := uuid.New()
		recordings[recordingID] = expandedRecording(
			recordingID, uuid.New(), uuid.New(), "Page candidate",
		)
		answer = append(answer, map[string]any{
			"recording_mbid": recordingID.String(), "score": float64(len(answer) + 1),
		})
	}
	passes := 0
	service := newService(t, store, func(response http.ResponseWriter, request *http.Request) {
		switch request.URL.Path {
		case "/1/cf/recommendation/user/listener/recording":
			passes++
			// The page is there for the first pass and gone from the second on.
			if passes > 1 {
				response.WriteHeader(http.StatusNoContent)
				return
			}
			_ = json.NewEncoder(response).Encode(map[string]any{"payload": map[string]any{
				"mbids": answer, "model_id": "quiet-model",
			}})
		case "/1/stats/user/listener/recordings":
			response.WriteHeader(http.StatusNoContent)
		default:
			http.NotFound(response, request)
		}
	})
	service.WithRecordingProvider(&fakeRecordingProvider{recordings: recordings})

	first, err := service.SweepPage(context.Background(), SweepPosition{})
	if err != nil {
		t.Fatalf("first SweepPage() error = %v", err)
	}
	if first.Report.Published ||
		len(store.published()) != 1 || store.published()[0].MusicbrainzRecordingID != yesterday {
		t.Fatalf("first pass published %#v, want yesterday's list until this one is whole",
			store.published())
	}

	second, err := service.SweepPage(context.Background(), first.Position)
	if err != nil {
		t.Fatalf("second SweepPage() error = %v", err)
	}
	if !second.Report.Published || second.Report.Offered != maximumCandidateExpansions+2 {
		t.Fatalf("second result = %#v, want the whole answer the first pass was given", second)
	}
	if len(store.published()) != maximumCandidateExpansions+2 {
		t.Fatalf("published = %d records, want all %d the page named",
			len(store.published()), maximumCandidateExpansions+2)
	}
	if passes != 1 {
		t.Fatalf("the recommendation page was asked for %d times, want once for the chain", passes)
	}
}

func TestSweepBoundsExpansionAndResumesAtTheNextCandidate(t *testing.T) {
	store := &fakeStore{row: configured("listener")}
	recordings := make(map[uuid.UUID]musicbrainz.Recording, maximumCandidateExpansions+2)
	answer := make([]map[string]any, 0, maximumCandidateExpansions+2)
	for range maximumCandidateExpansions + 2 {
		recordingID := uuid.New()
		recordings[recordingID] = expandedRecording(
			recordingID, uuid.New(), uuid.New(), "Bounded candidate",
		)
		answer = append(answer, map[string]any{
			"recording_mbid": recordingID.String(), "score": float64(len(answer) + 1),
		})
	}
	service := newService(t, store, func(response http.ResponseWriter, request *http.Request) {
		switch request.URL.Path {
		case "/1/cf/recommendation/user/listener/recording":
			_ = json.NewEncoder(response).Encode(map[string]any{"payload": map[string]any{
				"mbids": answer, "model_id": "bounded-model",
			}})
		case "/1/stats/user/listener/recordings":
			response.WriteHeader(http.StatusNoContent)
		default:
			http.NotFound(response, request)
		}
	})
	provider := &fakeRecordingProvider{recordings: recordings}
	service.WithRecordingProvider(provider)

	first, err := service.SweepPage(context.Background(), SweepPosition{})
	if err != nil {
		t.Fatalf("first SweepPage() error = %v", err)
	}
	if !first.More || !first.Position.HasCandidates || len(first.Position.Candidates) != 2 ||
		len(provider.asked) != maximumCandidateExpansions || len(store.staged()) != maximumCandidateExpansions {
		t.Fatalf("first result = %#v, requests = %d, built = %d, want one bounded partial pass",
			first, len(provider.asked), len(store.staged()))
	}

	second, err := service.SweepPage(context.Background(), first.Position)
	if err != nil {
		t.Fatalf("second SweepPage() error = %v", err)
	}
	if second.More || len(provider.asked) != maximumCandidateExpansions+2 ||
		len(store.published()) != maximumCandidateExpansions+2 ||
		store.publishedSnapshot().Status != "complete" {
		t.Fatalf("second result = %#v, requests = %d, snapshot = %#v, stored = %d, want completion",
			second, len(provider.asked), store.publishedSnapshot(), len(store.published()))
	}
}

// Naming the records a sweep must look up costs ListenBrainz one request for the
// recommendation page and one for each seed recording the similarity path uses.
// Looking one record up in MusicBrainz costs a request too, and a pass may spend
// only maximumCandidateExpansions of those, so a real account is walked over
// about fifty passes.
//
// Every pass used to name the records again before it looked any of them up: on
// the live account, about 1350 ListenBrainz requests to do 1298 look-ups, and a
// sweep of 37 minutes where the work in it is 22 (issue #302). A chain now asks
// once and hands the answer from pass to pass, so what ListenBrainz is asked is
// the same whether the chain takes two passes or fifty.
func TestSweepAsksListenBrainzOnceHoweverManyPassesAChainTakes(t *testing.T) {
	store := &fakeStore{row: configured("listener")}
	recordings := make(map[uuid.UUID]musicbrainz.Recording)
	page := make([]map[string]any, 0, 2*maximumCandidateExpansions)
	for range 2 * maximumCandidateExpansions {
		recordingID := uuid.New()
		recordings[recordingID] = expandedRecording(recordingID, uuid.New(), uuid.New(), "Page candidate")
		page = append(page, map[string]any{
			"recording_mbid": recordingID.String(), "score": float64(len(page) + 1),
		})
	}
	seeds := make([]map[string]any, 0, 3)
	similar := make(map[string][]map[string]any, 3)
	for range 3 {
		seedID := uuid.New()
		seeds = append(seeds, map[string]any{"recording_mbid": seedID.String()})
		recordingID := uuid.New()
		recordings[recordingID] = expandedRecording(recordingID, uuid.New(), uuid.New(), "Similar candidate")
		similar[seedID.String()] = []map[string]any{{
			"recording_mbid": recordingID.String(), "reference_mbid": seedID.String(), "score": 0.5,
		}}
	}
	// requests counts everything the account's ListenBrainz is asked, over the
	// whole chain. One fetch is the page, the top recordings, and one similarity
	// call for each of the three seeds.
	requests, fetch := 0, 2+len(seeds)
	service := newService(t, store, func(response http.ResponseWriter, request *http.Request) {
		requests++
		switch request.URL.Path {
		case "/1/cf/recommendation/user/listener/recording":
			_ = json.NewEncoder(response).Encode(map[string]any{"payload": map[string]any{
				"mbids": page, "model_id": "one-fetch-model",
			}})
		case "/1/stats/user/listener/recordings":
			_ = json.NewEncoder(response).Encode(map[string]any{
				"payload": map[string]any{"recordings": seeds},
			})
		case "/similar-recordings/json":
			_ = json.NewEncoder(response).Encode(similar[request.URL.Query().Get("recording_mbids")])
		default:
			http.NotFound(response, request)
		}
	})
	service.WithRecordingProvider(&fakeRecordingProvider{recordings: recordings})

	result, err := service.SweepPage(context.Background(), SweepPosition{})
	if err != nil {
		t.Fatalf("first SweepPage() error = %v", err)
	}
	passes := 1
	for result.Resume {
		if passes > maximumSweepPasses {
			t.Fatalf("the chain ran %d passes without ending", passes)
		}
		if result, err = service.SweepPage(context.Background(), result.Position); err != nil {
			t.Fatalf("pass %d SweepPage() error = %v", passes+1, err)
		}
		passes++
	}

	if !result.Report.Published || result.Report.Offered != len(recordings) {
		t.Fatalf("result = %#v, want the whole answer walked and published in %d passes",
			result, passes)
	}
	if passes < 3 {
		t.Fatalf("the chain took %d passes, want a chain long enough to be worth counting", passes)
	}
	if requests != fetch {
		t.Fatalf("asked ListenBrainz %d times over %d passes, want %d — one fetch for the chain",
			requests, passes, fetch)
	}
}

// Something the account listened to between two passes moves what ListenBrainz
// says about a candidate without moving the candidate, and a walk that read its
// place from the answer restarted on that (issue #303). A chain is now given the
// answer once and walks that one, so the second pass never sees the moved answer
// at all.
func TestSweepWalksTheAnswerItWasGivenRatherThanAFreshOne(t *testing.T) {
	store := &fakeStore{row: configured("listener")}
	recordings := make(map[uuid.UUID]musicbrainz.Recording, maximumCandidateExpansions+2)
	answer := make([]map[string]any, 0, maximumCandidateExpansions+2)
	for range maximumCandidateExpansions + 2 {
		recordingID := uuid.New()
		recordings[recordingID] = expandedRecording(
			recordingID, uuid.New(), uuid.New(), "Listened candidate",
		)
		answer = append(answer, map[string]any{
			"recording_mbid": recordingID.String(), "score": float64(len(answer) + 1),
		})
	}
	passes := 0
	service := newService(t, store, func(response http.ResponseWriter, request *http.Request) {
		switch request.URL.Path {
		case "/1/cf/recommendation/user/listener/recording":
			passes++
			// The second pass is told the account has since listened to every
			// one of them. The recordings, their order and their scores are the
			// answer they were.
			for _, entry := range answer {
				delete(entry, "latest_listened_at")
				if passes > 1 {
					entry["latest_listened_at"] = "2026-08-11T09:00:00Z"
				}
			}
			_ = json.NewEncoder(response).Encode(map[string]any{"payload": map[string]any{
				"mbids": answer, "model_id": "listened-model",
			}})
		case "/1/stats/user/listener/recordings":
			response.WriteHeader(http.StatusNoContent)
		default:
			http.NotFound(response, request)
		}
	})
	provider := &fakeRecordingProvider{recordings: recordings}
	service.WithRecordingProvider(provider)

	first, err := service.SweepPage(context.Background(), SweepPosition{})
	if err != nil {
		t.Fatalf("first SweepPage() error = %v", err)
	}
	second, err := service.SweepPage(context.Background(), first.Position)
	if err != nil {
		t.Fatalf("second SweepPage() error = %v", err)
	}

	if second.More {
		t.Fatalf("second result = %#v, want the walk finished rather than restarted", second)
	}
	if len(provider.asked) != maximumCandidateExpansions+2 {
		t.Fatalf("expansion requests = %d, want %d — the second pass asked about candidates the first had",
			len(provider.asked), maximumCandidateExpansions+2)
	}
	if passes != 1 {
		t.Fatalf("the recommendation page was asked for %d times, want once for the chain", passes)
	}
}

// lostAnswerChain is a chain caught by a deployment.
//
// A chain hands its position from one pass to the next through the job payload,
// and the records left to look up travel in it. The build before this one carried
// a cursor there and no records. encoding/json drops the fields it does not know,
// so a job that build queued arrives at this one as a later pass with nothing to
// walk, over a staged snapshot holding the three records that chain had stored.
//
// yesterday is the one record in the whole list readers see now. offered is how
// many records the account is offered. fetches counts what the fake has served
// the recommendation page to.
type lostAnswerChain struct {
	service   *Service
	store     *fakeStore
	position  SweepPosition
	yesterday uuid.UUID
	offered   int
	fetches   int
}

func newLostAnswerChain(t *testing.T) *lostAnswerChain {
	t.Helper()
	chain := &lostAnswerChain{
		store:     &fakeStore{row: configured("listener")},
		yesterday: uuid.New(),
		offered:   maximumCandidateExpansions + 5,
	}
	chain.store.write(
		db.UpsertRecommendationSnapshotParams{Source: listenbrainz.Name, Status: "complete"},
		[]db.InsertRecommendationCandidateParams{
			storedCandidate(listenbrainz.Name, chain.yesterday, "Yesterday's list")},
	)
	recordings := make(map[uuid.UUID]musicbrainz.Recording, chain.offered)
	page := make([]map[string]any, 0, chain.offered)
	for range chain.offered {
		recordingID := uuid.New()
		recordings[recordingID] = expandedRecording(recordingID, uuid.New(), uuid.New(), "Page candidate")
		page = append(page, map[string]any{
			"recording_mbid": recordingID.String(), "score": float64(chain.offered - len(page)),
		})
	}
	// Three is fewer than the account is offered, so publishing the staged
	// snapshot would be publishing a part of the list.
	built := make([]db.InsertRecommendationCandidateParams, 0, 3)
	for _, entry := range page[:3] {
		built = append(built, storedCandidate(
			stagedSource(listenbrainz.Name),
			uuid.MustParse(entry["recording_mbid"].(string)), "Built before the deployment",
		))
	}
	chain.store.write(db.UpsertRecommendationSnapshotParams{
		Source: stagedSource(listenbrainz.Name), Status: "partial", Detail: "still building",
	}, built)
	chain.service = newService(t, chain.store,
		func(response http.ResponseWriter, request *http.Request) {
			switch request.URL.Path {
			case "/1/cf/recommendation/user/listener/recording":
				chain.fetches++
				_ = json.NewEncoder(response).Encode(map[string]any{"payload": map[string]any{
					"mbids": page, "model_id": "recovered-model",
				}})
			case "/1/stats/user/listener/recordings":
				response.WriteHeader(http.StatusNoContent)
			default:
				http.NotFound(response, request)
			}
		}).WithRecordingProvider(&fakeRecordingProvider{recordings: recordings})

	// The payload the older build wrote, decoded by this one.
	if err := json.Unmarshal([]byte(`{
		"unresolved": ["`+uuid.New().String()+`"],
		"hasCandidates": true, "passes": 12, "offered": `+
		strconv.Itoa(chain.offered)+`}`), &chain.position); err != nil {
		t.Fatalf("decode the payload an older build wrote: %v", err)
	}
	if len(chain.position.Candidates) != 0 || chain.position.Passes != 12 {
		t.Fatalf("decoded position = %#v, want a later pass with no records to walk", chain.position)
	}
	return chain
}

// A pass hands its position on only while records are left, and it puts those
// records in it, so an empty list on a later pass means the answer was lost and
// never that the work is done. Read as a finished chain, such a pass would
// publish whatever the staged snapshot happened to hold — a part of the list,
// over the whole list people are reading (issue #305).
func TestSweepDoesNotPublishWhatItHoldsWhenAPositionLostTheAnswer(t *testing.T) {
	chain := newLostAnswerChain(t)

	result, err := chain.service.SweepPage(context.Background(), chain.position)
	if err != nil {
		t.Fatalf("SweepPage() error = %v", err)
	}
	published := chain.store.published()
	if result.Report.Published || len(published) != 1 ||
		published[0].MusicbrainzRecordingID != chain.yesterday {
		titles := make([]string, 0, len(published))
		for _, candidate := range published {
			titles = append(titles, candidate.RecordingTitle)
		}
		t.Fatalf("published %v, want yesterday's whole list rather than the three records built", titles)
	}
}

// The pass that finds its answer lost asks ListenBrainz for it again. That is one
// fetch nobody wanted, and it must stay one: the pass writes the records it
// recovered into the position it hands on, so every pass after it walks them
// without asking. A recovery that left the list empty each time would put the
// whole chain back to a fetch a pass.
func TestSweepAsksOnlyOnceMoreWhenAPositionLostTheAnswer(t *testing.T) {
	chain := newLostAnswerChain(t)

	result, err := chain.service.SweepPage(context.Background(), chain.position)
	if err != nil {
		t.Fatalf("SweepPage() error = %v", err)
	}
	for pass := 2; !result.Report.Published; pass++ {
		if pass > maximumSweepPasses {
			t.Fatalf("the recovered chain ran %d passes without publishing", pass)
		}
		if result, err = chain.service.SweepPage(context.Background(), result.Position); err != nil {
			t.Fatalf("pass %d SweepPage() error = %v", pass, err)
		}
	}

	if chain.fetches != 1 {
		t.Fatalf("asked ListenBrainz for the page %d times, want the one fetch the recovery needed",
			chain.fetches)
	}
}

// A pass with more to do runs its successor at once, so a chain that stopped
// making progress would ask MusicBrainz again every few seconds for as long as it
// kept not making it. What is counted is passes rather than progress, because a
// chain that is getting nowhere is exactly the one no measure of progress ever
// stops. Half a list is not an answer, so the chain leaves the published list
// alone and throws away what it built.
//
// The ListenBrainz fake here refuses every request, because a pass that is
// carried an answer must not go asking for another one.
func TestSweepStopsAChainThatHasSpentItsPasses(t *testing.T) {
	yesterday, halfBuilt := uuid.New(), uuid.New()
	store := &fakeStore{row: configured("listener")}
	store.write(
		db.UpsertRecommendationSnapshotParams{Source: listenbrainz.Name, Status: "complete"},
		[]db.InsertRecommendationCandidateParams{
			storedCandidate(listenbrainz.Name, yesterday, "Yesterday's list")},
	)
	store.write(
		db.UpsertRecommendationSnapshotParams{
			Source: stagedSource(listenbrainz.Name), Status: "partial", Detail: "still building",
		},
		[]db.InsertRecommendationCandidateParams{
			storedCandidate(stagedSource(listenbrainz.Name), halfBuilt, "Sixty-three passes of work")},
	)
	recordings := make(map[uuid.UUID]musicbrainz.Recording, maximumCandidateExpansions+2)
	carried := make([]SweepCandidate, 0, maximumCandidateExpansions+2)
	for range maximumCandidateExpansions + 2 {
		recordingID := uuid.New()
		recordings[recordingID] = expandedRecording(
			recordingID, uuid.New(), uuid.New(), "Spent candidate",
		)
		carried = append(carried, SweepCandidate{
			RecordingID: recordingID, Reasons: []string{"cf_raw"},
		})
	}
	service := newService(t, store, func(response http.ResponseWriter, request *http.Request) {
		http.NotFound(response, request)
	})
	service.WithRecordingProvider(&fakeRecordingProvider{recordings: recordings})

	result, err := service.SweepPage(context.Background(), SweepPosition{
		Passes: maximumSweepPasses - 1, HasCandidates: true,
		SnapshotID: "spent-model", Candidates: carried, Offered: len(carried),
		// The position names the account it was built for, as every position a
		// pass writes does. One that does not is a position from another account
		// or from an older build, and that is the one case where a pass carrying
		// records does ask again.
		Username: "listener", Algorithm: "session_based",
	})
	if err != nil {
		t.Fatalf("SweepPage() error = %v", err)
	}

	if result.More || result.Resume {
		t.Fatalf("result = %#v, want the chain stopped rather than continued", result)
	}
	if result.Report.Status != "partial" || !strings.Contains(result.Report.Detail, "passes") {
		t.Fatalf("report = %#v, want it said why the chain stopped", result.Report)
	}
	if len(store.published()) != 1 || store.published()[0].MusicbrainzRecordingID != yesterday {
		t.Fatalf("published = %#v, want the previous complete list untouched", store.published())
	}
	if len(store.staged()) != 0 {
		t.Fatalf("staged = %#v, want the unfinished snapshot thrown away", store.staged())
	}
}

// The job queue delivers a pass at least once, not exactly once: a pass whose
// job failed after it had written its records is tried again with the payload it
// was given, and so is one whose process was restarted mid-sweep. The pass runs
// again over a snapshot that already holds its work, and the chain has to reach
// its end from there rather than read the work it did as records it can no
// longer see.
func TestSweepFinishesAChainWhosePassWasDeliveredTwice(t *testing.T) {
	offered := 2*maximumCandidateExpansions + 5
	store := &fakeStore{row: configured("listener")}
	recordings := make(map[uuid.UUID]musicbrainz.Recording, offered)
	answer := make([]map[string]any, 0, offered)
	for index := range offered {
		recordingID := uuid.New()
		recordings[recordingID] = expandedRecording(
			recordingID, uuid.New(), uuid.New(), "Offered candidate",
		)
		answer = append(answer, map[string]any{
			"recording_mbid": recordingID.String(), "score": float64(offered - index),
		})
	}
	service := newService(t, store, func(response http.ResponseWriter, request *http.Request) {
		switch request.URL.Path {
		case "/1/cf/recommendation/user/listener/recording":
			_ = json.NewEncoder(response).Encode(map[string]any{"payload": map[string]any{
				"mbids": answer, "model_id": "redelivered-model",
			}})
		case "/1/stats/user/listener/recordings":
			response.WriteHeader(http.StatusNoContent)
		default:
			http.NotFound(response, request)
		}
	})
	service.WithRecordingProvider(&fakeRecordingProvider{recordings: recordings})

	first, err := service.SweepPage(context.Background(), SweepPosition{})
	if err != nil {
		t.Fatalf("first SweepPage() error = %v", err)
	}
	if _, err := service.SweepPage(context.Background(), first.Position); err != nil {
		t.Fatalf("second SweepPage() error = %v", err)
	}

	// The second pass's job is delivered again, with the position it was given.
	result, err := service.SweepPage(context.Background(), first.Position)
	if err != nil {
		t.Fatalf("delivered again, SweepPage() error = %v", err)
	}
	if !result.Report.Published {
		t.Fatalf("result = %#v, want the chain finished rather than stuck", result)
	}
	if len(store.published()) != offered {
		t.Fatalf("published = %d records, want all %d the source offered",
			len(store.published()), offered)
	}
}

// An outage that lasts carries the chain on rather than ending it, so the chain
// bound is what stops a chain that will never finish. Every pass here refuses
// the first record it asks about, so the chain makes no progress at all, and it
// has to end by itself with the published list where it was.
func TestSweepEndsAChainAnOutageWillNeverLetFinish(t *testing.T) {
	service, store, provider, order := halfSweptAccount(t)
	provider.errors = nil
	result, err := service.SweepPage(context.Background(), SweepPosition{})
	if err != nil {
		t.Fatalf("first SweepPage() error = %v", err)
	}
	provider.errors = map[uuid.UUID]error{
		order[maximumCandidateExpansions]: errors.New("musicbrainz did not respond in time"),
	}

	passes := 1
	for result.Resume {
		if passes > maximumSweepPasses {
			t.Fatalf("the chain ran %d passes without ending", passes)
		}
		result, err = service.SweepPage(context.Background(), result.Position)
		if err != nil {
			t.Fatalf("pass %d SweepPage() error = %v", passes+1, err)
		}
		passes++
	}

	if result.Report.Published {
		t.Fatalf("result = %#v, want a chain that never finished to publish nothing", result)
	}
	if len(store.published()) != 1 || store.published()[0].RecordingTitle != "Yesterday's list" {
		t.Fatalf("published = %#v, want yesterday's complete list untouched", store.published())
	}
	if len(store.staged()) != 0 {
		t.Fatalf("staged = %#v, want the abandoned chain's work thrown away", store.staged())
	}
}

// ListenBrainz answers a sweep from two services. The recommendation page is
// stable. The similarity service is not: it answers one pass and not the next,
// and both answers are valid. The list therefore changes size between two passes
// of the same chain, and the walk has to carry on through that rather than start
// again (issue #303).
func TestSweepResumesWhenTheSimilarityServiceStopsAnswering(t *testing.T) {
	store := &fakeStore{row: configured("listener")}
	recordings := make(map[uuid.UUID]musicbrainz.Recording)
	page := make([]map[string]any, 0, maximumCandidateExpansions+2)
	for range maximumCandidateExpansions + 2 {
		recordingID := uuid.New()
		recordings[recordingID] = expandedRecording(recordingID, uuid.New(), uuid.New(), "Page candidate")
		page = append(page, map[string]any{
			"recording_mbid": recordingID.String(), "score": float64(len(page) + 1),
		})
	}
	seedID := uuid.New()
	similar := make([]map[string]any, 0, 5)
	for range 5 {
		recordingID := uuid.New()
		recordings[recordingID] = expandedRecording(recordingID, uuid.New(), uuid.New(), "Similar candidate")
		// Scored above every page candidate, so these sort to the front and the
		// first pass expands them. When they vanish, everything after them in the
		// old list has moved.
		similar = append(similar, map[string]any{
			"recording_mbid": recordingID.String(), "reference_mbid": seedID.String(), "score": 1000,
		})
	}
	fanouts := 0
	service := newService(t, store, func(response http.ResponseWriter, request *http.Request) {
		switch request.URL.Path {
		case "/1/cf/recommendation/user/listener/recording":
			_ = json.NewEncoder(response).Encode(map[string]any{"payload": map[string]any{
				"mbids": page, "model_id": "two-service-model",
			}})
		case "/1/stats/user/listener/recordings":
			_ = json.NewEncoder(response).Encode(map[string]any{"payload": map[string]any{
				"recordings": []map[string]any{{"recording_mbid": seedID.String()}},
			}})
		case "/similar-recordings/json":
			// Anything that asks a second time is told there are no similar
			// recordings. Nothing is wrong with either answer; the service simply
			// does not always have one.
			fanouts++
			if fanouts > 1 {
				response.WriteHeader(http.StatusNoContent)
				return
			}
			_ = json.NewEncoder(response).Encode(similar)
		default:
			http.NotFound(response, request)
		}
	})
	provider := &fakeRecordingProvider{recordings: recordings}
	service.WithRecordingProvider(provider)

	first, err := service.SweepPage(context.Background(), SweepPosition{})
	if err != nil {
		t.Fatalf("first SweepPage() error = %v", err)
	}
	if !first.More || len(store.staged()) != maximumCandidateExpansions {
		t.Fatalf("first result = %#v, built = %d, want one bounded pass over the merged list",
			first, len(store.staged()))
	}

	second, err := service.SweepPage(context.Background(), first.Position)
	if err != nil {
		t.Fatalf("second SweepPage() error = %v", err)
	}
	if second.More || store.publishedSnapshot().Status != "complete" {
		t.Fatalf("second result = %#v, snapshot = %#v, want the walk finished, not restarted",
			second, store.publishedSnapshot())
	}
	if len(store.published()) != len(recordings) {
		t.Fatalf("stored = %d, want all %d candidates both services named",
			len(store.published()), len(recordings))
	}
	if len(provider.asked) != len(recordings) {
		t.Fatalf("expansion requests = %d, want %d — one for each candidate and no repeats",
			len(provider.asked), len(recordings))
	}
}

// A recording MusicBrainz does not know was asked about and produced no row. The
// pass has to drop it from the list it hands on, because a record that can never
// be stored would otherwise be the first thing offered to every pass after it.
func TestSweepDoesNotAskAgainAboutARecordingMusicBrainzCouldNotExpand(t *testing.T) {
	store := &fakeStore{row: configured("listener")}
	recordings := make(map[uuid.UUID]musicbrainz.Recording)
	unknownIDs := make(map[uuid.UUID]struct{}, 5)
	answer := make([]map[string]any, 0, maximumCandidateExpansions+5)
	for index := range maximumCandidateExpansions + 5 {
		recordingID := uuid.New()
		// Five of the first bounded pass's candidates are recordings MusicBrainz
		// has never heard of, spread through it rather than gathered at one end.
		if index%5 == 0 && len(unknownIDs) < 5 {
			unknownIDs[recordingID] = struct{}{}
		} else {
			recordings[recordingID] = expandedRecording(
				recordingID, uuid.New(), uuid.New(), "Known candidate",
			)
		}
		// Scored downwards, so the walk takes them in the order they were built
		// and the five unknown ones all fall inside the first bounded pass.
		answer = append(answer, map[string]any{
			"recording_mbid": recordingID.String(),
			"score":          float64(maximumCandidateExpansions + 5 - index),
		})
	}
	service := newService(t, store, func(response http.ResponseWriter, request *http.Request) {
		switch request.URL.Path {
		case "/1/cf/recommendation/user/listener/recording":
			_ = json.NewEncoder(response).Encode(map[string]any{"payload": map[string]any{
				"mbids": answer, "model_id": "unexpandable-model",
			}})
		case "/1/stats/user/listener/recordings":
			response.WriteHeader(http.StatusNoContent)
		default:
			http.NotFound(response, request)
		}
	})
	provider := &fakeRecordingProvider{recordings: recordings}
	service.WithRecordingProvider(provider)

	first, err := service.SweepPage(context.Background(), SweepPosition{})
	if err != nil {
		t.Fatalf("first SweepPage() error = %v", err)
	}
	for _, carried := range first.Position.Candidates {
		if _, unknown := unknownIDs[carried.RecordingID]; unknown {
			t.Fatalf("recording %s is still on the list MusicBrainz has already said it does not know",
				carried.RecordingID)
		}
	}
	if len(first.Position.Candidates) != len(answer)-maximumCandidateExpansions {
		t.Fatalf("carried %d candidates, want the %d this pass did not reach",
			len(first.Position.Candidates), len(answer)-maximumCandidateExpansions)
	}

	second, err := service.SweepPage(context.Background(), first.Position)
	if err != nil {
		t.Fatalf("second SweepPage() error = %v", err)
	}
	if second.More || len(store.published()) != len(recordings) {
		t.Fatalf("second result = %#v, stored = %d, want every expandable candidate and an end",
			second, len(store.published()))
	}
	if len(provider.asked) != len(answer) {
		t.Fatalf("expansion requests = %d, want %d — one for each candidate and no repeats",
			len(provider.asked), len(answer))
	}
}

func TestSweepPreservesTheSnapshotWhenEveryFreshCandidateIsUnexpandable(t *testing.T) {
	oldID := uuid.New()
	store := &fakeStore{row: configured("listener")}
	store.write(
		db.UpsertRecommendationSnapshotParams{Source: listenbrainz.Name, Status: "complete"},
		[]db.InsertRecommendationCandidateParams{
			storedCandidate(listenbrainz.Name, oldID, "Still here")},
	)
	freshID := uuid.New()
	service := newService(t, store, func(response http.ResponseWriter, request *http.Request) {
		switch request.URL.Path {
		case "/1/cf/recommendation/user/listener/recording":
			_ = json.NewEncoder(response).Encode(map[string]any{"payload": map[string]any{
				"mbids": []map[string]any{{"recording_mbid": freshID.String(), "score": 1}},
			}})
		case "/1/stats/user/listener/recordings":
			response.WriteHeader(http.StatusNoContent)
		default:
			http.NotFound(response, request)
		}
	}).WithRecordingProvider(&fakeRecordingProvider{})

	if _, err := service.Sweep(context.Background()); err == nil ||
		!strings.Contains(err.Error(), "none had usable MusicBrainz metadata") {
		t.Fatalf("Sweep() error = %v, want an unexpandable-answer failure", err)
	}
	if store.recommendationWrites != 0 || len(store.published()) != 1 ||
		store.published()[0].MusicbrainzRecordingID != oldID {
		t.Fatalf("writes = %d candidates = %#v, want the prior snapshot preserved",
			store.recommendationWrites, store.published())
	}
}

// quietServices is a ListenBrainz whose two services both answer with no
// records. Neither answer is an error: it is what an account with no model
// computed for it is told, and what a service with nothing to say answers.
func quietServices(t *testing.T, store *fakeStore) *Service {
	t.Helper()
	return newService(t, store, func(response http.ResponseWriter, request *http.Request) {
		switch request.URL.Path {
		case "/1/cf/recommendation/user/listener/recording",
			"/1/stats/user/listener/recordings":
			response.WriteHeader(http.StatusNoContent)
		default:
			http.NotFound(response, request)
		}
	}).WithRecordingProvider(&fakeRecordingProvider{})
}

// Publishing a snapshot replaces the one people read. An answer of no records at
// all is what a quiet ListenBrainz gives and what an account with nothing to
// suggest gives, and nothing in either answer says which. Publishing it deleted
// yesterday's list and told the reader the account has nothing (issue #317), so
// where a list is published the empty answer is not believed.
func TestSweepKeepsThePublishedListWhenBothServicesNameNoRecords(t *testing.T) {
	yesterday := uuid.New()
	store := &fakeStore{row: configured("listener")}
	store.write(
		db.UpsertRecommendationSnapshotParams{Source: listenbrainz.Name, Status: "complete"},
		[]db.InsertRecommendationCandidateParams{
			storedCandidate(listenbrainz.Name, yesterday, "Yesterday's list")},
	)
	service := quietServices(t, store)

	result, err := service.SweepPage(context.Background(), SweepPosition{})
	if err != nil {
		t.Fatalf("SweepPage() error = %v", err)
	}
	if len(store.published()) != 1 || store.published()[0].MusicbrainzRecordingID != yesterday {
		t.Fatalf("published = %#v, want yesterday's list still in front of readers",
			store.published())
	}
	if result.Report.Published || !result.Report.KeptPublished {
		t.Fatalf("report = %#v, want a pass that published nothing and kept the list",
			result.Report)
	}
}

// The rule above is about replacing a list, not about writing the first one. An
// account nobody has recommendations for, and no list published for it, is still
// told so: the empty snapshot is published and the screen can say the source had
// nothing to suggest.
func TestSweepPublishesAnEmptyAnswerWhereNoListWasPublishedBefore(t *testing.T) {
	store := &fakeStore{row: configured("listener")}
	service := quietServices(t, store)

	result, err := service.SweepPage(context.Background(), SweepPosition{})
	if err != nil {
		t.Fatalf("SweepPage() error = %v", err)
	}
	if !result.Report.Published || result.Report.KeptPublished {
		t.Fatalf("report = %#v, want the empty answer published", result.Report)
	}
	if len(store.published()) != 0 || store.publishedSnapshot().Status != "complete" {
		t.Fatalf("published = %#v, snapshot = %#v, want a complete list with no records in it",
			store.published(), store.publishedSnapshot())
	}
}

func TestSweepDoesNotAskADisabledSource(t *testing.T) {
	row := configured("listener")
	row.Enabled = false
	store := &fakeStore{row: row}
	requests := 0
	service := newService(t, store, func(response http.ResponseWriter, _ *http.Request) {
		requests++
		response.WriteHeader(http.StatusInternalServerError)
	}).WithRecordingProvider(&fakeRecordingProvider{})

	if _, err := service.Sweep(context.Background()); !errors.Is(err, ErrDisabled) {
		t.Fatalf("Sweep() error = %v, want ErrDisabled", err)
	}
	if requests != 0 || store.recommendationWrites != 0 {
		t.Fatalf("requests = %d writes = %d, want a disabled source untouched",
			requests, store.recommendationWrites)
	}
}

// Switching recommendations off while a sweep is part way through leaves the
// snapshot that sweep was building. No later pass will come for it, because none
// is queued, so the pass that finds the source switched off clears it.
func TestSweepThrowsAwayWhatItWasBuildingForASourceSomebodySwitchedOff(t *testing.T) {
	row := configured("listener")
	row.Enabled = false
	store := &fakeStore{row: row}
	store.write(
		db.UpsertRecommendationSnapshotParams{
			Source: stagedSource(listenbrainz.Name), Status: "partial", Detail: "still building",
		},
		[]db.InsertRecommendationCandidateParams{
			storedCandidate(stagedSource(listenbrainz.Name), uuid.New(), "Half a list")},
	)
	service := newService(t, store, func(response http.ResponseWriter, _ *http.Request) {
		response.WriteHeader(http.StatusInternalServerError)
	}).WithRecordingProvider(&fakeRecordingProvider{})

	if _, err := service.Sweep(context.Background()); !errors.Is(err, ErrDisabled) {
		t.Fatalf("Sweep() error = %v, want ErrDisabled", err)
	}
	if len(store.staged()) != 0 {
		t.Fatalf("staged = %#v, want the half-built snapshot cleared", store.staged())
	}
}

func TestSuppressionRuleForEachRuleAndItsAbsence(t *testing.T) {
	tests := []struct {
		name  string
		facts SuppressionFacts
		want  SuppressionRule
	}{
		{"no rule", SuppressionFacts{}, ""},
		{"owned", SuppressionFacts{Owned: true}, SuppressedOwned},
		{"requested", SuppressionFacts{Requested: true}, SuppressedRequested},
		{"not wanted", SuppressionFacts{NotWanted: true}, SuppressedNotWanted},
		{"followed artist", SuppressionFacts{FollowedArtist: true}, SuppressedFollowedArtist},
		{"followed label", SuppressionFacts{FollowedLabel: true}, SuppressedFollowedLabel},
		{"dismissed", SuppressionFacts{Dismissed: true}, SuppressedDismissed},
		{"impression fatigue", SuppressionFacts{ImpressionFatigue: true}, SuppressedImpressionFatigue},
		{"product order wins", SuppressionFacts{
			Owned: true, Requested: true, NotWanted: true,
			FollowedArtist: true, Dismissed: true, ImpressionFatigue: true,
		}, SuppressedOwned},
		// A want that was asked for and then stopped is one want in one status,
		// so the two facts cannot both be true of the same recording. If a
		// future status ever made them, the reader is owed the live want.
		{"a live want outranks a stopped one", SuppressionFacts{
			Requested: true, NotWanted: true,
		}, SuppressedRequested},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := SuppressionRuleFor(test.facts); got != test.want {
				t.Fatalf("SuppressionRuleFor(%+v) = %q, want %q", test.facts, got, test.want)
			}
		})
	}
}

func TestCandidatePreparationMergesAndRanksDeterministically(t *testing.T) {
	recordingA := uuid.MustParse("00000000-0000-0000-0000-000000000001")
	recordingB := uuid.MustParse("00000000-0000-0000-0000-000000000002")
	release := uuid.MustParse("10000000-0000-0000-0000-000000000001")
	artistA := uuid.MustParse("20000000-0000-0000-0000-000000000001")
	artistB := uuid.MustParse("20000000-0000-0000-0000-000000000002")
	strong, weak := 0.9, 0.2

	prepared, err := prepareCandidates([]CandidateInput{
		{
			MusicBrainzRecordingID: recordingB, MusicBrainzReleaseGroupID: release,
			MusicBrainzArtistIDs: []uuid.UUID{artistB}, RecordingTitle: "Second",
			ArtistName: "Artist B", SourceScore: &weak, ReasonCodes: []string{"two_hop"},
		},
		{
			MusicBrainzRecordingID: recordingA, MusicBrainzReleaseGroupID: release,
			MusicBrainzArtistIDs: []uuid.UUID{artistB, artistA, artistA}, RecordingTitle: "First",
			ArtistName: "Artist A", SourceScore: &weak, ReasonCodes: []string{"similar_to"},
			ReasonContext: map[string]any{"seed": "seed-mbid"},
		},
		{
			MusicBrainzRecordingID: recordingA, MusicBrainzReleaseGroupID: release,
			MusicBrainzArtistIDs: []uuid.UUID{artistA}, RecordingTitle: "First",
			ArtistName: "Artist A", SourceScore: &strong, ReasonCodes: []string{"cf_raw", "similar_to"},
			ReasonContext: map[string]any{"model": "model-1"},
		},
		// An unexpanded candidate is dropped because all hard rules cannot be
		// evaluated for it.
		{MusicBrainzRecordingID: uuid.New(), RecordingTitle: "Unexpanded", ArtistName: "Nobody"},
	})
	if err != nil {
		t.Fatalf("prepareCandidates() error = %v", err)
	}
	if len(prepared) != 2 {
		t.Fatalf("prepared %d candidates, want 2", len(prepared))
	}
	first := prepared[0]
	if first.MusicbrainzRecordingID != recordingA || first.Rank != 1 ||
		!first.SourceScore.Valid || first.SourceScore.Float64 != strong {
		t.Fatalf("first = %#v", first)
	}
	if !reflect.DeepEqual(first.MusicbrainzArtistIds, []uuid.UUID{artistA, artistB}) {
		t.Fatalf("artist IDs = %v, want deduplicated deterministic credit", first.MusicbrainzArtistIds)
	}
	if !reflect.DeepEqual(first.ReasonCodes, []string{"cf_raw", "similar_to"}) {
		t.Fatalf("reason codes = %v", first.ReasonCodes)
	}
	var context map[string]string
	if err := json.Unmarshal(first.ReasonContext, &context); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(context, map[string]string{"model": "model-1", "seed": "seed-mbid"}) {
		t.Fatalf("reason context = %v", context)
	}
	if prepared[1].MusicbrainzRecordingID != recordingB || prepared[1].Rank != 2 {
		t.Fatalf("second = %#v", prepared[1])
	}
}

func TestCandidatePreparationRefusesLossyMerges(t *testing.T) {
	recording, release, artist := uuid.New(), uuid.New(), uuid.New()
	base := CandidateInput{
		MusicBrainzRecordingID: recording, MusicBrainzReleaseGroupID: release,
		MusicBrainzArtistIDs: []uuid.UUID{artist}, RecordingTitle: "Song", ArtistName: "Artist",
		ReasonCodes: []string{"cf_raw"}, ReasonContext: map[string]any{"model": "one"},
	}
	conflict := base
	conflict.ReasonContext = map[string]any{"model": "two"}
	if _, err := prepareCandidates([]CandidateInput{base, conflict}); err == nil {
		t.Fatal("prepareCandidates() accepted conflicting reason context")
	}
	withoutReason := base
	withoutReason.ReasonCodes = nil
	if _, err := prepareCandidates([]CandidateInput{withoutReason}); err == nil {
		t.Fatal("prepareCandidates() accepted a candidate with no reason")
	}
}

func feedbackFor(recordingID, releaseGroupID uuid.UUID, artistIDs []uuid.UUID, signal FeedbackSignal, seedIDs ...uuid.UUID) db.RecommendationFeedback {
	seeds := make([]string, 0, len(seedIDs))
	for _, seedID := range seedIDs {
		seeds = append(seeds, seedID.String())
	}
	context := []byte(`{}`)
	if len(seeds) > 0 {
		encoded, _ := json.Marshal(map[string]any{"seed_recording_ids": seeds})
		context = encoded
	}
	return db.RecommendationFeedback{
		ID:                        uuid.New(),
		MusicbrainzRecordingID:    recordingID,
		MusicbrainzReleaseGroupID: releaseGroupID,
		MusicbrainzArtistIds:      artistIDs,
		Signal:                    string(signal),
		ReasonCodes:               []string{"similar_to"},
		ReasonContext:             context,
	}
}

func TestRecommendationFeedbackChangesRankAndReason(t *testing.T) {
	anchorID, relatedID, unrelatedID := uuid.New(), uuid.New(), uuid.New()
	sharedRelease, sharedArtist, seedID := uuid.New(), uuid.New(), uuid.New()
	anchor := CandidateInput{
		MusicBrainzRecordingID: anchorID, MusicBrainzReleaseGroupID: sharedRelease,
		MusicBrainzArtistIDs: []uuid.UUID{sharedArtist}, RecordingTitle: "Anchor",
		ArtistName: "Artist", SourceScore: float64Ptr(0.40), ReasonCodes: []string{"cf_raw"},
		ReasonContext: map[string]any{"seed_recording_ids": []string{seedID.String()}},
	}
	related := anchor
	related.MusicBrainzRecordingID = relatedID
	related.RecordingTitle = "Related"
	related.SourceScore = float64Ptr(0.50)
	unrelated := anchor
	unrelated.MusicBrainzRecordingID = unrelatedID
	unrelated.MusicBrainzReleaseGroupID = uuid.New()
	unrelated.MusicBrainzArtistIDs = []uuid.UUID{uuid.New()}
	unrelated.ReasonContext = map[string]any{"seed_recording_ids": []string{uuid.New().String()}}
	unrelated.RecordingTitle = "Unrelated"
	unrelated.SourceScore = float64Ptr(0.55)

	for _, test := range []struct {
		name   string
		signal FeedbackSignal
		first  uuid.UUID
	}{
		{name: "more", signal: FeedbackMoreLikeThis, first: relatedID},
		{name: "less", signal: FeedbackLessLikeThis, first: unrelatedID},
	} {
		t.Run(test.name, func(t *testing.T) {
			prepared, err := prepareCandidates([]CandidateInput{anchor, related, unrelated}, []db.RecommendationFeedback{
				feedbackFor(anchorID, sharedRelease, []uuid.UUID{sharedArtist}, test.signal, seedID),
			})
			if err != nil {
				t.Fatal(err)
			}
			if prepared[0].MusicbrainzRecordingID != test.first {
				t.Fatalf("first rank = %s, want %s", prepared[0].MusicbrainzRecordingID, test.first)
			}
			for _, row := range prepared {
				if row.MusicbrainzRecordingID == relatedID && !containsReason(row.ReasonCodes, "feedback_") {
					t.Fatalf("related reasons = %v, want feedback reason", row.ReasonCodes)
				}
				if row.MusicbrainzRecordingID == relatedID && row.SourceScore.Float64 != 0.50 {
					t.Fatalf("related source score = %v, want 0.50", row.SourceScore.Float64)
				}
			}
		})
	}
}

func TestRecommendationFeedbackDeltaIsClampedAndClearRestoresSourceOrder(t *testing.T) {
	anchorID, relatedID := uuid.New(), uuid.New()
	releaseID, artistID, seedID := uuid.New(), uuid.New(), uuid.New()
	inputs := []CandidateInput{
		{MusicBrainzRecordingID: anchorID, MusicBrainzReleaseGroupID: releaseID, MusicBrainzArtistIDs: []uuid.UUID{artistID}, RecordingTitle: "Anchor", ArtistName: "Artist", SourceScore: float64Ptr(0.20), ReasonCodes: []string{"cf_raw"}, ReasonContext: map[string]any{"seed_recording_ids": []string{seedID.String()}}},
		{MusicBrainzRecordingID: relatedID, MusicBrainzReleaseGroupID: releaseID, MusicBrainzArtistIDs: []uuid.UUID{artistID}, RecordingTitle: "Related", ArtistName: "Artist", SourceScore: float64Ptr(0.00), ReasonCodes: []string{"cf_raw"}, ReasonContext: map[string]any{"seed_recording_ids": []string{seedID.String()}}},
	}
	feedback := make([]db.RecommendationFeedback, 0, 10)
	for range 10 {
		feedback = append(feedback, feedbackFor(anchorID, releaseID, []uuid.UUID{artistID}, FeedbackMoreLikeThis, seedID))
	}
	boosted, err := prepareCandidates(inputs, feedback)
	if err != nil {
		t.Fatal(err)
	}
	if boosted[0].MusicbrainzRecordingID != relatedID || boosted[0].SourceScore.Float64 != 0 {
		t.Fatalf("boosted row = %#v, want related row with source score 0", boosted[0])
	}
	if !containsReason(boosted[0].ReasonCodes, "feedback_more_like_this") {
		t.Fatalf("boosted reasons = %v", boosted[0].ReasonCodes)
	}

	restored, err := prepareCandidates(inputs, []db.RecommendationFeedback{})
	if err != nil {
		t.Fatal(err)
	}
	if restored[0].MusicbrainzRecordingID != anchorID || containsReason(restored[0].ReasonCodes, "feedback_") {
		t.Fatalf("restored rows = %#v", restored)
	}
}

func float64Ptr(value float64) *float64 { return &value }

func containsReason(reasons []string, prefix string) bool {
	for _, reason := range reasons {
		if strings.HasPrefix(reason, prefix) {
			return true
		}
	}
	return false
}

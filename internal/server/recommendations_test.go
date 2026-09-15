package server

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/pxldi/schall/internal/db"
	"github.com/pxldi/schall/internal/recommendations"
	"github.com/rs/zerolog"
)

// fakeRecommendations stands in for the service that reads the stored list and
// applies the hard suppression rules. The handler must not decide suppression
// itself, so what this returns is the whole of what the route can answer with.
type fakeRecommendations struct {
	evaluated   []recommendations.EvaluatedCandidate
	evaluateErr error
	// dismissed and shown are what the two writes reached the store with. They
	// are the contract of those routes: a dismissal that is not written is a
	// suggestion that comes back tomorrow.
	dismissed   []db.RecommendationDismissal
	shown       []uuid.UUID
	feedback    []string
	feedbackErr error
}

func (service *fakeRecommendations) Evaluate(
	context.Context, string,
) ([]recommendations.EvaluatedCandidate, error) {
	if service.evaluateErr != nil {
		return nil, service.evaluateErr
	}
	return service.evaluated, nil
}

func (service *fakeRecommendations) Dismiss(
	_ context.Context, subject recommendations.DismissalSubject, musicBrainzID uuid.UUID,
) (db.RecommendationDismissal, error) {
	row := db.RecommendationDismissal{
		SubjectType:   string(subject),
		MusicbrainzID: musicBrainzID,
		DismissedAt:   pgtype.Timestamptz{Time: time.Unix(0, 0).UTC(), Valid: true},
	}
	service.dismissed = append(service.dismissed, row)
	return row, nil
}

func (service *fakeRecommendations) RecordFeedback(
	_ context.Context, recordingID uuid.UUID, signal recommendations.FeedbackSignal,
) (db.RecommendationFeedback, error) {
	if service.feedbackErr != nil {
		return db.RecommendationFeedback{}, service.feedbackErr
	}
	service.feedback = append(service.feedback, recordingID.String()+":"+string(signal))
	return db.RecommendationFeedback{
		MusicbrainzRecordingID: recordingID,
		Signal:                 string(signal),
		CreatedAt:              pgtype.Timestamptz{Time: time.Unix(0, 0).UTC(), Valid: true},
	}, nil
}

func (service *fakeRecommendations) ClearFeedback(context.Context) error {
	service.feedback = nil
	return nil
}

// The stored list is what may be counted, so the fake keeps one: anything the
// candidates do not name is dropped, exactly as the service drops it.
func (service *fakeRecommendations) RecordImpressions(
	_ context.Context, musicBrainzRecordingIDs []uuid.UUID,
) (int, error) {
	offered := make(map[uuid.UUID]struct{}, len(service.evaluated))
	for _, candidate := range service.evaluated {
		offered[candidate.MusicBrainzRecordingID] = struct{}{}
	}
	recorded := 0
	for _, recordingID := range musicBrainzRecordingIDs {
		if _, known := offered[recordingID]; !known {
			continue
		}
		service.shown = append(service.shown, recordingID)
		recorded++
	}
	return recorded, nil
}

// fakeSnapshotStore is the store side: the header saying whether a sweep has
// ever stored anything for this source, and what the job queue holds about the
// sweep that keeps it up to date.
type fakeSnapshotStore struct {
	*fakeStore
	snapshot db.RecommendationSnapshot
	fetched  bool
	sweep    db.RecommendationSweepState
}

func (store *fakeSnapshotStore) RecommendationSnapshot(
	context.Context, string,
) (db.RecommendationSnapshot, error) {
	if !store.fetched {
		return db.RecommendationSnapshot{}, pgx.ErrNoRows
	}
	return store.snapshot, nil
}

func (store *fakeSnapshotStore) RecommendationSweepState(
	context.Context,
) (db.RecommendationSweepState, error) {
	return store.sweep, nil
}

func suggestion(title string, rule recommendations.SuppressionRule) recommendations.EvaluatedCandidate {
	return recommendations.EvaluatedCandidate{
		Candidate: recommendations.Candidate{
			Source:                    "listenbrainz",
			MusicBrainzRecordingID:    uuid.New(),
			MusicBrainzReleaseGroupID: uuid.New(),
			MusicBrainzArtistIDs:      []uuid.UUID{uuid.New()},
			RecordingTitle:            title,
			ReleaseTitle:              "A release",
			ArtistName:                "An artist",
			ReasonCodes:               []string{"cf_raw"},
		},
		SuppressedBy: rule,
	}
}

// storedList is a stored list in the order the sweep wrote it, one entry per
// rule given. Rank is that order, and it is what the two step controls address,
// so a list built without it cannot be paged through.
func storedList(rules ...recommendations.SuppressionRule) []recommendations.EvaluatedCandidate {
	list := make([]recommendations.EvaluatedCandidate, 0, len(rules))
	for index, rule := range rules {
		entry := suggestion("Suggestion "+strconv.Itoa(index+1), rule)
		entry.Rank = int32(index + 1)
		list = append(list, entry)
	}
	return list
}

func ranks(page recommendationPage) []int32 {
	found := make([]int32, 0, len(page.Items))
	for _, item := range page.Items {
		found = append(found, item.Rank)
	}
	return found
}

func recommendationHandler(service Recommendations, store *fakeSnapshotStore) http.Handler {
	if store == nil {
		store = &fakeSnapshotStore{fakeStore: &fakeStore{}}
	}
	return NewAPI(store, fakeDatabase{}, &fakeArtistSearcher{}, zerolog.Nop(),
		WithRecommendations(service))
}

// recommendationPage is the shape the page reads, named here so a test asserts
// on the answer rather than on a map.
type recommendationPage struct {
	Items    []recommendationResponse       `json:"items"`
	Total    int                            `json:"total"`
	Limit    int                            `json:"limit"`
	Offset   int                            `json:"offset"`
	Hidden   recommendationHiddenResponse   `json:"hidden"`
	Snapshot recommendationSnapshotResponse `json:"snapshot"`
	Refresh  recommendationRefreshResponse  `json:"refresh"`
}

func readRecommendations(t *testing.T, handler http.Handler, query string) recommendationPage {
	t.Helper()
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/api/v1/recommendations"+query, nil))
	if response.Code != http.StatusOK {
		t.Fatalf("status = %d; body = %s", response.Code, response.Body)
	}
	var page recommendationPage
	if err := json.NewDecoder(response.Body).Decode(&page); err != nil {
		t.Fatal(err)
	}
	return page
}

func TestASuppressedSuggestionNeverReachesTheList(t *testing.T) {
	handler := recommendationHandler(&fakeRecommendations{evaluated: []recommendations.EvaluatedCandidate{
		suggestion("Held already", recommendations.SuppressedOwned),
		suggestion("Worth hearing", ""),
	}}, nil)

	page := readRecommendations(t, handler, "")

	if len(page.Items) != 1 || page.Items[0].RecordingTitle != "Worth hearing" {
		t.Fatalf("items = %#v", page.Items)
	}
}

func TestTheListSaysHowManyEachRuleHid(t *testing.T) {
	handler := recommendationHandler(&fakeRecommendations{evaluated: []recommendations.EvaluatedCandidate{
		suggestion("Held already", recommendations.SuppressedOwned),
		suggestion("Held twice over", recommendations.SuppressedOwned),
		suggestion("On the way", recommendations.SuppressedRequested),
		suggestion("Stopped looking", recommendations.SuppressedNotWanted),
		suggestion("By somebody followed", recommendations.SuppressedFollowedArtist),
		suggestion("Turned down", recommendations.SuppressedDismissed),
		suggestion("Ignored three times", recommendations.SuppressedImpressionFatigue),
		suggestion("Worth hearing", ""),
	}}, nil)

	page := readRecommendations(t, handler, "")

	want := recommendationHiddenResponse{
		Total: 7, Owned: 2, Requested: 1, NotWanted: 1,
		FollowedArtist: 1, Dismissed: 1, ImpressionFatigue: 1,
	}
	if page.Hidden != want {
		t.Fatalf("hidden = %#v, want %#v", page.Hidden, want)
	}
}

// The total is what a reader can page through, so it counts what may be shown.
// Counting the suppressed rows in it would leave the last page empty.
func TestTheTotalCountsOnlyTheSuggestionsThatMayBeShown(t *testing.T) {
	handler := recommendationHandler(&fakeRecommendations{evaluated: []recommendations.EvaluatedCandidate{
		suggestion("Held already", recommendations.SuppressedOwned),
		suggestion("Worth hearing", ""),
		suggestion("Also worth hearing", ""),
	}}, nil)

	page := readRecommendations(t, handler, "?limit=1")

	if page.Total != 2 || len(page.Items) != 1 {
		t.Fatalf("total = %d, items = %d", page.Total, len(page.Items))
	}
}

// A window past the end is a reader whose page went away under them: they were
// on the last page and the rules took the rows on it. The list is still there,
// so they are put on the last page of what is left rather than shown an empty
// panel with no way back.
func TestAWindowPastTheEndComesBackOntoTheList(t *testing.T) {
	handler := recommendationHandler(&fakeRecommendations{evaluated: storedList("", "", "")}, nil)

	page := readRecommendations(t, handler, "?limit=2&offset=40")

	if page.Offset != 2 || page.Total != 3 {
		t.Fatalf("offset = %d, total = %d", page.Offset, page.Total)
	}
	if got := ranks(page); len(got) != 1 || got[0] != 3 {
		t.Fatalf("ranks = %v, want the last page", got)
	}
}

// Reading a page can take the rows on it out of the list: three showings with
// no answer is the fifth rule, and page one gathers those showings together.
// Everything after them then moves up while a position stays where it was, so
// stepping by position walked over a page of suggestions with nothing said
// (issue #329). A step names the last rank the reader saw instead, and rank is
// the stored order, which does not move.
func TestSteppingByRankDoesNotWalkOverSuggestions(t *testing.T) {
	fatigued := recommendations.SuppressedImpressionFatigue
	handler := recommendationHandler(&fakeRecommendations{
		evaluated: storedList(fatigued, fatigued, "", "", "", ""),
	}, nil)

	stepped := readRecommendations(t, handler, "?limit=2&after=2")

	if got := ranks(stepped); len(got) != 2 || got[0] != 3 || got[1] != 4 {
		t.Fatalf("ranks = %v, want the two suggestions after rank 2", got)
	}
}

// A position still counts places in the visible list, which is what the two
// ends of the list are addressed by and why the step controls are not. The
// reader here has seen ranks 1 and 2 and both have left: position 2 is now
// rank 5, and ranks 3 and 4 are the two this list would walk over.
func TestAPositionCountsPlacesInTheVisibleList(t *testing.T) {
	fatigued := recommendations.SuppressedImpressionFatigue
	handler := recommendationHandler(&fakeRecommendations{
		evaluated: storedList(fatigued, fatigued, "", "", "", ""),
	}, nil)

	page := readRecommendations(t, handler, "?limit=2&offset=2")

	if got := ranks(page); len(got) != 2 || got[0] != 5 || got[1] != 6 {
		t.Fatalf("ranks = %v, want positions 2 and 3 of the visible list", got)
	}
}

func TestSteppingBackReachesThePageBeforeTheRankTheReaderSaw(t *testing.T) {
	handler := recommendationHandler(&fakeRecommendations{
		evaluated: storedList("", "", "", "", "", ""),
	}, nil)

	page := readRecommendations(t, handler, "?limit=2&before=5")

	if got := ranks(page); len(got) != 2 || got[0] != 3 || got[1] != 4 {
		t.Fatalf("ranks = %v, want the page ending before rank 5", got)
	}
	if page.Offset != 2 {
		t.Fatalf("offset = %d, want where that page starts", page.Offset)
	}
}

// Two of them name two different pages and there is no way to tell which
// control the reader pressed.
func TestAPageAskedForTwoWaysIsRefused(t *testing.T) {
	handler := recommendationHandler(&fakeRecommendations{evaluated: storedList("", "")}, nil)

	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(
		http.MethodGet, "/api/v1/recommendations?offset=1&after=1", nil))

	if response.Code != http.StatusUnprocessableEntity {
		t.Fatalf("status = %d; body = %s", response.Code, response.Body)
	}
}

// An empty list has two meanings. This one is "nobody has fetched anything
// yet", and the page says so instead of "no suggestions".
func TestAListWithNoStoredSweepSaysNothingHasBeenFetched(t *testing.T) {
	handler := recommendationHandler(&fakeRecommendations{}, nil)

	page := readRecommendations(t, handler, "")

	if page.Snapshot.Fetched {
		t.Fatalf("snapshot = %#v, want nothing fetched", page.Snapshot)
	}
}

func TestAListWithAStoredSweepReportsWhenItWasFetched(t *testing.T) {
	fetchedAt := time.Date(2026, time.August, 10, 12, 0, 0, 0, time.UTC)
	store := &fakeSnapshotStore{fakeStore: &fakeStore{}, fetched: true, snapshot: db.RecommendationSnapshot{
		Source:    "listenbrainz",
		Status:    "complete",
		FetchedAt: pgtype.Timestamptz{Time: fetchedAt, Valid: true},
	}}
	handler := recommendationHandler(&fakeRecommendations{}, store)

	page := readRecommendations(t, handler, "")

	if !page.Snapshot.Fetched || page.Snapshot.Status != "complete" ||
		page.Snapshot.FetchedAt == nil || !page.Snapshot.FetchedAt.Equal(fetchedAt) {
		t.Fatalf("snapshot = %#v", page.Snapshot)
	}
}

// storedListFrom is a list a sweep published at fetchedAt, beside what the job
// queue holds about the sweep behind it.
func storedListFrom(fetchedAt time.Time, sweep db.RecommendationSweepState) *fakeSnapshotStore {
	return &fakeSnapshotStore{
		fakeStore: &fakeStore{},
		fetched:   true,
		snapshot: db.RecommendationSnapshot{
			Source:    "listenbrainz",
			Status:    "complete",
			FetchedAt: pgtype.Timestamptz{Time: fetchedAt, Valid: true},
		},
		sweep: sweep,
	}
}

func moment(offset time.Duration) time.Time {
	return time.Now().Add(offset).UTC().Truncate(time.Second)
}

// The seeded collection is one of these: a stored list and an empty job queue.
// That is ordinary — a restored or seeded database — and putting a fault on a
// screen that has none would be worse than saying nothing.
func TestAStoredListWithNoSweepBehindItIsNotAFault(t *testing.T) {
	store := storedListFrom(moment(-2*time.Hour), db.RecommendationSweepState{})
	handler := recommendationHandler(&fakeRecommendations{}, store)

	page := readRecommendations(t, handler, "")

	if page.Refresh.Attempted {
		t.Fatalf("refresh = %#v, want no sweep attempted", page.Refresh)
	}
}

// A pass writes the list it publishes before the queue marks the pass done, so
// the list a sweep published is written after that pass started.
func TestASweepThatPublishedTheListReportsItRefreshed(t *testing.T) {
	started := moment(-3 * time.Hour)
	due := moment(21 * time.Hour)
	store := storedListFrom(started.Add(time.Minute), db.RecommendationSweepState{
		Attempted:    true,
		Started:      stamp(started),
		Finished:     stamp(started.Add(2 * time.Minute)),
		NextRunAfter: stamp(due),
	})
	handler := recommendationHandler(&fakeRecommendations{}, store)

	page := readRecommendations(t, handler, "")

	if !page.Refresh.Attempted || !page.Refresh.Succeeded {
		t.Fatalf("refresh = %#v, want a refreshed list", page.Refresh)
	}
	if page.Refresh.NextAttemptAt == nil || !page.Refresh.NextAttemptAt.Equal(due) {
		t.Fatalf("next attempt = %v, want %v", page.Refresh.NextAttemptAt, due)
	}
}

// A pass that reached nobody keeps the last complete list and finishes tidily,
// so its own row says nothing is wrong. What says it is the list being older
// than the pass that has just run.
func TestASweepThatRefreshedNothingSaysTheListIsNotBeingRefreshed(t *testing.T) {
	started := moment(-5 * time.Minute)
	due := moment(25 * time.Minute)
	store := storedListFrom(moment(-4*24*time.Hour), db.RecommendationSweepState{
		Attempted:    true,
		Started:      stamp(started),
		Finished:     stamp(started.Add(time.Minute)),
		NextRunAfter: stamp(due),
	})
	handler := recommendationHandler(&fakeRecommendations{}, store)

	page := readRecommendations(t, handler, "")

	if !page.Refresh.Attempted || page.Refresh.Succeeded {
		t.Fatalf("refresh = %#v, want a list nobody is refreshing", page.Refresh)
	}
	if page.Refresh.NextAttemptAt == nil || !page.Refresh.NextAttemptAt.Equal(due) {
		t.Fatalf("next attempt = %v, want %v", page.Refresh.NextAttemptAt, due)
	}
}

// One sweep walks a large account over many short passes and only the last of
// them publishes, so every pass before it leaves the list older than itself and
// queues its successor to run at once. That is the sweep working.
func TestASweepStillWalkingTheAccountIsNotReportedAsStopped(t *testing.T) {
	started := moment(-30 * time.Second)
	store := storedListFrom(moment(-24*time.Hour), db.RecommendationSweepState{
		Attempted:    true,
		Started:      stamp(started),
		Finished:     stamp(started.Add(20 * time.Second)),
		NextRunAfter: stamp(moment(-time.Second)),
	})
	handler := recommendationHandler(&fakeRecommendations{}, store)

	page := readRecommendations(t, handler, "")

	if !page.Refresh.Succeeded {
		t.Fatalf("refresh = %#v, want a sweep still at work", page.Refresh)
	}
}

func TestASweepRunningNowIsNotReportedAsStopped(t *testing.T) {
	started := moment(-2 * time.Minute)
	store := storedListFrom(moment(-24*time.Hour), db.RecommendationSweepState{
		Attempted: true,
		Started:   stamp(started),
		Finished:  stamp(started.Add(time.Minute)),
		Running:   true,
	})
	handler := recommendationHandler(&fakeRecommendations{}, store)

	page := readRecommendations(t, handler, "")

	if !page.Refresh.Succeeded {
		t.Fatalf("refresh = %#v, want a sweep still at work", page.Refresh)
	}
}

// A sweep stores a list only when it walks the whole answer, so a first sweep
// that stopped part way leaves no list at all. The page has to carry both facts
// — nothing stored, and a sweep that did not finish — because the screen would
// otherwise say ListenBrainz had nothing to suggest, which is a third thing and
// is not what happened.
func TestAFirstSweepThatKeptNothingIsToldApartFromAnEmptyAnswer(t *testing.T) {
	started := moment(-10 * time.Minute)
	store := &fakeSnapshotStore{fakeStore: &fakeStore{}, sweep: db.RecommendationSweepState{
		Attempted:    true,
		Started:      stamp(started),
		Finished:     stamp(started.Add(9 * time.Minute)),
		NextRunAfter: stamp(moment(24 * time.Hour)),
	}}
	handler := recommendationHandler(&fakeRecommendations{}, store)

	page := readRecommendations(t, handler, "")

	if page.Snapshot.Fetched || !page.Refresh.Attempted || page.Refresh.Succeeded {
		t.Fatalf("snapshot = %#v, refresh = %#v", page.Snapshot, page.Refresh)
	}
}

func TestReadingTheListIsRefusedWithoutTheService(t *testing.T) {
	handler := NewAPI(&fakeStore{}, fakeDatabase{}, &fakeArtistSearcher{}, zerolog.Nop())

	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/api/v1/recommendations", nil))

	if response.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d; body = %s", response.Code, response.Body)
	}
}

func TestADismissalIsWrittenAtTheLevelItWasAskedFor(t *testing.T) {
	service := &fakeRecommendations{}
	handler := recommendationHandler(service, nil)
	artistID := uuid.New()

	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodPost, "/api/v1/recommendations/dismissals",
		strings.NewReader(`{"subject":"artist","musicBrainzId":"`+artistID.String()+`"}`)))

	if response.Code != http.StatusOK {
		t.Fatalf("status = %d; body = %s", response.Code, response.Body)
	}
	if len(service.dismissed) != 1 || service.dismissed[0].SubjectType != "artist" ||
		service.dismissed[0].MusicbrainzID != artistID {
		t.Fatalf("dismissed = %#v", service.dismissed)
	}
}

func TestADismissalAtAnUnknownLevelIsRefused(t *testing.T) {
	service := &fakeRecommendations{}
	handler := recommendationHandler(service, nil)

	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodPost, "/api/v1/recommendations/dismissals",
		strings.NewReader(`{"subject":"genre","musicBrainzId":"`+uuid.New().String()+`"}`)))

	if response.Code != http.StatusUnprocessableEntity {
		t.Fatalf("status = %d; body = %s", response.Code, response.Body)
	}
	if len(service.dismissed) != 0 {
		t.Fatalf("dismissed = %#v, want nothing written", service.dismissed)
	}
}

func TestAShowingIsCountedForEveryRecordingNamed(t *testing.T) {
	list := storedList("", "")
	service := &fakeRecommendations{evaluated: list}
	handler := recommendationHandler(service, nil)
	first, second := list[0].MusicBrainzRecordingID, list[1].MusicBrainzRecordingID

	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodPost, "/api/v1/recommendations/impressions",
		strings.NewReader(`{"recordingIds":["`+first.String()+`","`+second.String()+`"]}`)))

	if response.Code != http.StatusOK {
		t.Fatalf("status = %d; body = %s", response.Code, response.Body)
	}
	if len(service.shown) != 2 || service.shown[0] != first || service.shown[1] != second {
		t.Fatalf("shown = %v", service.shown)
	}
}

// The fatigue count decides whether a suggestion comes back for ninety days,
// and this route takes any UUID a caller cares to send. A recording no stored
// list offers is dropped: nothing could have shown it (issue #327).
func TestAShowingIsNotCountedForARecordingNoListOffers(t *testing.T) {
	list := storedList("")
	service := &fakeRecommendations{evaluated: list}
	handler := recommendationHandler(service, nil)

	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodPost, "/api/v1/recommendations/impressions",
		strings.NewReader(`{"recordingIds":["`+list[0].MusicBrainzRecordingID.String()+
			`","`+uuid.New().String()+`"]}`)))

	if response.Code != http.StatusOK {
		t.Fatalf("status = %d; body = %s", response.Code, response.Body)
	}
	if len(service.shown) != 1 || service.shown[0] != list[0].MusicBrainzRecordingID {
		t.Fatalf("shown = %v, want only the recording the list offers", service.shown)
	}
	var answer struct {
		Recorded int `json:"recorded"`
	}
	if err := json.NewDecoder(response.Body).Decode(&answer); err != nil {
		t.Fatal(err)
	}
	if answer.Recorded != 1 {
		t.Fatalf("recorded = %d, want the one that was shown", answer.Recorded)
	}
}

// One unreadable identifier fails the whole call. A partial count would be a
// suppression window started for some rows and not others, with nothing to say
// which.
func TestAShowingWithAnUnreadableIdentifierCountsNothing(t *testing.T) {
	service := &fakeRecommendations{}
	handler := recommendationHandler(service, nil)

	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodPost, "/api/v1/recommendations/impressions",
		strings.NewReader(`{"recordingIds":["`+uuid.New().String()+`","not-an-identifier"]}`)))

	if response.Code != http.StatusUnprocessableEntity {
		t.Fatalf("status = %d; body = %s", response.Code, response.Body)
	}
	if len(service.shown) != 0 {
		t.Fatalf("shown = %v, want nothing counted", service.shown)
	}
}

func TestAShowingWithNoRecordingsIsRefused(t *testing.T) {
	handler := recommendationHandler(&fakeRecommendations{}, nil)

	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodPost, "/api/v1/recommendations/impressions",
		strings.NewReader(`{"recordingIds":[]}`)))

	if response.Code != http.StatusUnprocessableEntity {
		t.Fatalf("status = %d; body = %s", response.Code, response.Body)
	}
}

func TestRecommendationFeedbackSendsOnlyTheRecordingAndSignal(t *testing.T) {
	service := &fakeRecommendations{}
	handler := recommendationHandler(service, nil)
	recordingID := uuid.New()
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodPost, "/api/v1/recommendations/feedback",
		strings.NewReader(`{"recordingId":"`+recordingID.String()+`","signal":"more_like_this"}`)))

	if response.Code != http.StatusOK {
		t.Fatalf("status = %d; body = %s", response.Code, response.Body)
	}
	if len(service.feedback) != 1 || service.feedback[0] != recordingID.String()+":more_like_this" {
		t.Fatalf("feedback = %v", service.feedback)
	}
}

func TestRecommendationFeedbackRejectsAnUnknownSignal(t *testing.T) {
	service := &fakeRecommendations{}
	handler := recommendationHandler(service, nil)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodPost, "/api/v1/recommendations/feedback",
		strings.NewReader(`{"recordingId":"`+uuid.New().String()+`","signal":"dismiss"}`)))

	if response.Code != http.StatusUnprocessableEntity {
		t.Fatalf("status = %d; body = %s", response.Code, response.Body)
	}
	if len(service.feedback) != 0 {
		t.Fatalf("feedback = %v, want nothing", service.feedback)
	}
}

func TestRecommendationFeedbackReportsAStaleSuggestion(t *testing.T) {
	service := &fakeRecommendations{feedbackErr: pgx.ErrNoRows}
	handler := recommendationHandler(service, nil)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodPost, "/api/v1/recommendations/feedback",
		strings.NewReader(`{"recordingId":"`+uuid.New().String()+`","signal":"less_like_this"}`)))

	if response.Code != http.StatusConflict {
		t.Fatalf("status = %d; body = %s", response.Code, response.Body)
	}
}

func TestRecommendationFeedbackClearUsesTheSourceOnly(t *testing.T) {
	service := &fakeRecommendations{feedback: []string{"one", "two"}}
	handler := recommendationHandler(service, nil)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodDelete, "/api/v1/recommendations/feedback", nil))

	if response.Code != http.StatusNoContent {
		t.Fatalf("status = %d; body = %s", response.Code, response.Body)
	}
	if len(service.feedback) != 0 {
		t.Fatalf("feedback = %v, want cleared", service.feedback)
	}
}

package musicbrainz

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
)

func mustUUID(value string) uuid.UUID {
	return uuid.MustParse(value)
}

func testClient(t *testing.T, handler http.HandlerFunc) *Client {
	t.Helper()
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)
	client, err := NewClient(Options{
		BaseURL:   server.URL,
		UserAgent: "Schall/test (test@example.com)",
		Interval:  time.Nanosecond,
	})
	if err != nil {
		t.Fatalf("NewClient() error = %v", err)
	}
	return client
}

func TestRecordingReadsCreditReleaseAndISRCs(t *testing.T) {
	var gotPath, gotInclude string
	client := testClient(t, func(response http.ResponseWriter, request *http.Request) {
		gotPath, gotInclude = request.URL.Path, request.URL.Query().Get("inc")
		_, _ = response.Write([]byte(`{
			"id": "205ff413-6fbe-48d2-bd64-940cc37f5d75",
			"title": "Karma Police",
			"length": 308973,
			"isrcs": ["GBAYE9700325"],
			"artist-credit": [
				{"name": "Radiohead", "joinphrase": " feat. ", "artist": {"name": "Radiohead"}},
				{"name": "Guest", "joinphrase": "", "artist": {"name": "Guest"}}
			],
			"releases": [
				{"id": "17d3004a-c0c9-4767-8387-95aecc50fbdd", "title": "Bootleg",
				 "status": "Bootleg", "date": "1998-06-14",
				 "release-group": {"id": "eaa1e253-c162-3deb-b303-ce56465add67", "primary-type": "Album"}},
				{"id": "2d7f08e1-2e0e-4b1e-9f31-3c4d1d2f5b6a", "title": "OK Computer",
				 "status": "Official", "date": "1997-05-21",
				 "release-group": {"id": "b1392450-e666-3926-a536-22c65f834433", "primary-type": "Album"},
				 "media": [{"position": 1, "track": [{"number": "6", "title": "Karma Police"}]}]}
			]
		}`))
	})

	recording, err := client.Recording(context.Background(), mustUUID("205ff413-6fbe-48d2-bd64-940cc37f5d75"))
	if err != nil {
		t.Fatalf("Recording() error = %v", err)
	}
	if gotPath != "/recording/205ff413-6fbe-48d2-bd64-940cc37f5d75" {
		t.Errorf("path = %q", gotPath)
	}
	if gotInclude != "artist-credits+aliases+isrcs+releases+release-groups" {
		t.Errorf("inc = %q", gotInclude)
	}
	if recording.Title != "Karma Police" || recording.ArtistCredit != "Radiohead feat. Guest" {
		t.Errorf("recording = %+v", recording)
	}
	if recording.DurationMS == nil || *recording.DurationMS != 308973 {
		t.Errorf("duration = %v", recording.DurationMS)
	}
	if len(recording.ISRCs) != 1 || recording.ISRCs[0] != "GBAYE9700325" {
		t.Errorf("isrcs = %v", recording.ISRCs)
	}
	// The official edition sorts first, so a file that names no album is
	// described by the edition a person would name.
	if len(recording.Releases) != 2 || recording.Releases[0].Title != "OK Computer" {
		t.Fatalf("releases = %+v", recording.Releases)
	}
	first := recording.Releases[0]
	if first.ReleaseGroupID.String() != "b1392450-e666-3926-a536-22c65f834433" {
		t.Errorf("release group = %s", first.ReleaseGroupID)
	}
	if first.DiscNumber != 1 || first.TrackNumber != 6 {
		t.Errorf("position = %d-%d", first.DiscNumber, first.TrackNumber)
	}
}

func TestRecordingReportsMissingAsNotFound(t *testing.T) {
	client := testClient(t, func(response http.ResponseWriter, _ *http.Request) {
		response.WriteHeader(http.StatusNotFound)
	})
	if _, err := client.Recording(context.Background(), mustUUID("205ff413-6fbe-48d2-bd64-940cc37f5d75")); !errors.Is(err, ErrNotFound) {
		t.Fatalf("Recording() error = %v, want ErrNotFound", err)
	}
}

func TestSearchRecordingsAsksByISRCAlone(t *testing.T) {
	var gotQuery string
	client := testClient(t, func(response http.ResponseWriter, request *http.Request) {
		gotQuery = request.URL.Query().Get("query")
		_, _ = response.Write([]byte(`{"count": 0, "recordings": []}`))
	})
	if _, err := client.SearchRecordings(context.Background(), RecordingQuery{
		ISRC: "GBAYE9700325", Artist: "Radiohead", Title: "Karma Police",
	}, 5); err != nil {
		t.Fatalf("SearchRecordings() error = %v", err)
	}
	if gotQuery != "isrc:GBAYE9700325" {
		t.Errorf("query = %q, want the ISRC alone", gotQuery)
	}
}

func TestSearchRecordingsQuotesAndEscapesEvidence(t *testing.T) {
	var gotQuery string
	client := testClient(t, func(response http.ResponseWriter, request *http.Request) {
		gotQuery = request.URL.Query().Get("query")
		_, _ = response.Write([]byte(`{"count": 1, "recordings": [
			{"id": "205ff413-6fbe-48d2-bd64-940cc37f5d75", "title": "Fitter Happier", "score": 92,
			 "artist-credit": [{"name": "Radiohead", "joinphrase": ""}]}
		]}`))
	})
	recordings, err := client.SearchRecordings(context.Background(), RecordingQuery{
		Artist: `AC/DC`, Title: `Who Are You? "live"`,
	}, 5)
	if err != nil {
		t.Fatalf("SearchRecordings() error = %v", err)
	}
	want := `recording:"Who Are You? \"live\"" AND artist:"AC/DC"`
	if gotQuery != want {
		t.Errorf("query = %q, want %q", gotQuery, want)
	}
	if len(recordings) != 1 || recordings[0].Score != 92 {
		t.Fatalf("recordings = %+v", recordings)
	}
}

func TestSearchRecordingsRefusesWithoutEvidence(t *testing.T) {
	client := testClient(t, func(response http.ResponseWriter, _ *http.Request) {
		t.Error("provider was asked without anything to ask about")
	})
	if _, err := client.SearchRecordings(context.Background(), RecordingQuery{Artist: "Radiohead"}, 5); err == nil {
		t.Fatal("SearchRecordings() error = nil, want a refusal")
	}
}

// MusicBrainz answers a lookup on a merged GID with the recording it was merged
// into. Both IDs name that recording, and the answer says so rather than leaving
// a caller to read the difference as other music.
func TestALookupOnAMergedIDAnswersToBothIDs(t *testing.T) {
	merged := mustUUID("b9ad642e-b012-41c7-b72a-42cf4911f9ff")
	canonical := mustUUID("205ff413-6fbe-48d2-bd64-940cc37f5d75")
	client := testClient(t, func(response http.ResponseWriter, _ *http.Request) {
		_, _ = response.Write([]byte(`{
			"id": "205ff413-6fbe-48d2-bd64-940cc37f5d75",
			"title": "Karma Police",
			"length": 308973
		}`))
	})

	recording, err := client.Recording(context.Background(), merged)
	if err != nil {
		t.Fatalf("Recording() error = %v", err)
	}
	if recording.ID != canonical {
		t.Fatalf("id = %s, want the canonical recording", recording.ID)
	}
	if !recording.Answers(merged) || !recording.Answers(canonical) {
		t.Errorf("recording = %+v, want it to answer to the ID asked for as well", recording)
	}
}

// mergedID and survivorID are the two names of one recording: the second is what
// MusicBrainz answers with when the first is looked up.
const (
	mergedID   = "b9ad642e-b012-41c7-b72a-42cf4911f9ff"
	survivorID = "205ff413-6fbe-48d2-bd64-940cc37f5d75"
)

// mergeServer answers the way MusicBrainz does after a merge: whichever of the
// two IDs is asked about, the surviving recording comes back. Anything else is a
// recording it has never heard of. Requests are counted because the provider
// serves one a second — how often this is asked is the whole reason it lives at
// the client — and t.Cleanup is where the count is read, so a test says what it
// wanted rather than counting for its own sake.
func mergeServer(t *testing.T, wantRequests int) *Client {
	t.Helper()
	requests := 0
	client := testClient(t, func(response http.ResponseWriter, request *http.Request) {
		requests++
		switch request.URL.Path {
		case "/recording/" + mergedID, "/recording/" + survivorID:
			_, _ = response.Write([]byte(
				`{"id": "` + survivorID + `", "title": "Karma Police", "length": 308973}`))
		default:
			response.WriteHeader(http.StatusNotFound)
		}
	})
	t.Cleanup(func() {
		if requests != wantRequests {
			t.Errorf("requests = %d, want %d", requests, wantRequests)
		}
	})
	return client
}

// The merge, asked as its own question. This is the answer a file tagged before
// the merge rests on, and the redirect is the only thing that establishes it.
func TestAMergedIDResolvesToTheRecordingItWasMergedInto(t *testing.T) {
	resolved, err := mergeServer(t, 1).ResolveRecordingID(context.Background(), mustUUID(mergedID))
	if err != nil {
		t.Fatalf("ResolveRecordingID() error = %v", err)
	}
	if resolved != mustUUID(survivorID) {
		t.Errorf("resolved = %s, want the surviving recording", resolved)
	}
}

// The equivalence has a direction and it is the provider's. A recording that
// absorbed another is itself, not the ID it replaced, however often the pair has
// been seen together.
func TestASurvivingIDNeverResolvesBackToTheIDItReplaced(t *testing.T) {
	client := mergeServer(t, 1)
	if _, err := client.ResolveRecordingID(context.Background(), mustUUID(mergedID)); err != nil {
		t.Fatalf("ResolveRecordingID() error = %v", err)
	}

	resolved, err := client.ResolveRecordingID(context.Background(), mustUUID(survivorID))
	if err != nil {
		t.Fatalf("ResolveRecordingID() error = %v", err)
	}
	if resolved != mustUUID(survivorID) {
		t.Errorf("resolved = %s, want the surviving recording to stay itself", resolved)
	}
}

// One want is offered by many peers and every copy asks the same question, so an
// answer already had is not bought again from a provider serving one request a
// second.
func TestAnIDAlreadyResolvedIsNotAskedAboutAgain(t *testing.T) {
	client := mergeServer(t, 1)
	for attempt := 0; attempt < 3; attempt++ {
		resolved, err := client.ResolveRecordingID(context.Background(), mustUUID(mergedID))
		if err != nil {
			t.Fatalf("ResolveRecordingID() error = %v", err)
		}
		if resolved != mustUUID(survivorID) {
			t.Fatalf("resolved = %s, want the surviving recording", resolved)
		}
	}
}

// A lookup answers the narrow question on the way past, so resolution warming a
// recording leaves verification nothing to ask.
func TestALookupLeavesTheMergeResolved(t *testing.T) {
	client := mergeServer(t, 1)
	if _, err := client.Recording(context.Background(), mustUUID(mergedID)); err != nil {
		t.Fatalf("Recording() error = %v", err)
	}

	resolved, err := client.ResolveRecordingID(context.Background(), mustUUID(mergedID))
	if err != nil {
		t.Fatalf("ResolveRecordingID() error = %v", err)
	}
	if resolved != mustUUID(survivorID) {
		t.Errorf("resolved = %s, want the surviving recording", resolved)
	}
}

// A resolution asks for the ID and nothing else. The credits, ISRCs and releases
// a lookup carries are of no use to the question and cost the provider to
// assemble.
func TestResolvingAnIDAsksForTheIDAlone(t *testing.T) {
	var gotInclude string
	client := testClient(t, func(response http.ResponseWriter, request *http.Request) {
		gotInclude = request.URL.Query().Get("inc")
		_, _ = response.Write([]byte(`{"id": "` + survivorID + `"}`))
	})

	if _, err := client.ResolveRecordingID(context.Background(), mustUUID(mergedID)); err != nil {
		t.Fatalf("ResolveRecordingID() error = %v", err)
	}
	if gotInclude != "" {
		t.Errorf("inc = %q, want the bare lookup", gotInclude)
	}
}

// An ID the provider has never heard of resolves to nothing. It is not a merge,
// and a caller must not read it as one: silence about an ID is not the ID naming
// the recording it was compared against.
func TestAnIDMusicBrainzDoesNotKnowResolvesToNothing(t *testing.T) {
	const strangerID = "6f3c1b0a-6d3f-4a2f-9a26-1cbe4d8a5d21"
	_, err := mergeServer(t, 1).ResolveRecordingID(context.Background(), mustUUID(strangerID))
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("error = %v, want %v", err, ErrNotFound)
	}
}

// A recording that does not exist goes on not existing, so asking a second time
// buys a request and no answer.
func TestAnIDMusicBrainzDoesNotKnowIsNotAskedAboutAgain(t *testing.T) {
	const strangerID = "6f3c1b0a-6d3f-4a2f-9a26-1cbe4d8a5d21"
	client := mergeServer(t, 1)
	for attempt := 0; attempt < 3; attempt++ {
		if _, err := client.ResolveRecordingID(context.Background(), mustUUID(strangerID)); !errors.Is(err, ErrNotFound) {
			t.Fatalf("error = %v, want %v", err, ErrNotFound)
		}
	}
}

// A provider that could not be reached said nothing, so nothing is remembered.
// Holding an outage would turn one bad minute into a verdict for the life of the
// process.
func TestAResolutionThatFailedIsAskedAgain(t *testing.T) {
	asked := 0
	client := testClient(t, func(response http.ResponseWriter, _ *http.Request) {
		asked++
		if asked == 1 {
			response.WriteHeader(http.StatusInternalServerError)
			return
		}
		_, _ = response.Write([]byte(`{"id": "` + survivorID + `"}`))
	})

	if _, err := client.ResolveRecordingID(context.Background(), mustUUID(mergedID)); err == nil {
		t.Fatal("a provider that broke was reported as an answer")
	} else if errors.Is(err, ErrNotFound) {
		t.Fatalf("error = %v, want a failure rather than an absence", err)
	}
	resolved, err := client.ResolveRecordingID(context.Background(), mustUUID(mergedID))
	if err != nil {
		t.Fatalf("ResolveRecordingID() error = %v", err)
	}
	if resolved != mustUUID(survivorID) {
		t.Errorf("resolved = %s, want the question asked again", resolved)
	}
}

// MusicBrainz is edited while Schall runs, and a want the loop keeps retrying is
// where a merge has to be noticed. An ID that named itself when it was first
// asked about must therefore be asked about again, or a server running for weeks
// goes on refusing copies the provider has since reconciled.
func TestAMergeMadeAfterTheAnswerIsNoticedOnceItHasAged(t *testing.T) {
	merged := false
	asked := 0
	client := testClient(t, func(response http.ResponseWriter, _ *http.Request) {
		asked++
		answer := mergedID
		if merged {
			answer = survivorID
		}
		_, _ = response.Write([]byte(`{"id": "` + answer + `"}`))
	})
	clock := time.Now()
	client.now = func() time.Time { return clock }

	if _, err := client.ResolveRecordingID(context.Background(), mustUUID(mergedID)); err != nil {
		t.Fatalf("ResolveRecordingID() error = %v", err)
	}
	merged = true
	clock = clock.Add(rememberFor + time.Second)

	resolved, err := client.ResolveRecordingID(context.Background(), mustUUID(mergedID))
	if err != nil {
		t.Fatalf("ResolveRecordingID() error = %v", err)
	}
	if resolved != mustUUID(survivorID) {
		t.Errorf("resolved = %s, want the merge the provider has since made", resolved)
	}
	if asked != 2 {
		t.Errorf("requests = %d, want the question put again once the answer aged", asked)
	}
}

// An ID MusicBrainz had never heard of is asked about again for the same reason:
// a recording can be added, and holding "no such thing" for the life of a process
// would make one afternoon's absence permanent.
func TestAnIDTheProviderDidNotKnowIsAskedAboutAgainOnceItHasAged(t *testing.T) {
	const strangerID = "6f3c1b0a-6d3f-4a2f-9a26-1cbe4d8a5d21"
	asked := 0
	client := testClient(t, func(response http.ResponseWriter, _ *http.Request) {
		asked++
		response.WriteHeader(http.StatusNotFound)
	})
	clock := time.Now()
	client.now = func() time.Time { return clock }

	if _, err := client.ResolveRecordingID(context.Background(), mustUUID(strangerID)); !errors.Is(err, ErrNotFound) {
		t.Fatalf("error = %v, want %v", err, ErrNotFound)
	}
	clock = clock.Add(rememberFor + time.Second)
	if _, err := client.ResolveRecordingID(context.Background(), mustUUID(strangerID)); !errors.Is(err, ErrNotFound) {
		t.Fatalf("error = %v, want %v", err, ErrNotFound)
	}
	if asked != 2 {
		t.Errorf("requests = %d, want the question put again once the answer aged", asked)
	}
}

// What one merge settles is that pair of IDs and nothing else. Every other ID is
// still the provider's to answer for.
func TestOneMergeSettlesNothingAboutAnotherID(t *testing.T) {
	const strangerID = "6f3c1b0a-6d3f-4a2f-9a26-1cbe4d8a5d21"
	client := mergeServer(t, 2)
	if _, err := client.ResolveRecordingID(context.Background(), mustUUID(mergedID)); err != nil {
		t.Fatalf("ResolveRecordingID() error = %v", err)
	}

	if _, err := client.ResolveRecordingID(context.Background(), mustUUID(strangerID)); !errors.Is(err, ErrNotFound) {
		t.Fatalf("error = %v, want the other ID asked about on its own", err)
	}
}

func TestResolveRecordingIDRefusesNoIDAtAll(t *testing.T) {
	client := testClient(t, func(http.ResponseWriter, *http.Request) {
		t.Error("the provider was asked about no recording at all")
	})

	if _, err := client.ResolveRecordingID(context.Background(), uuid.Nil); err == nil {
		t.Fatal("the provider was asked anyway")
	}
}

// No ID at all is not an ID this recording answers to. A file with nothing
// written in its recording tag proves nothing about anything.
func TestARecordingDoesNotAnswerToNoIDAtAll(t *testing.T) {
	if (Recording{ID: mustUUID("205ff413-6fbe-48d2-bd64-940cc37f5d75")}).Answers(uuid.Nil) {
		t.Error("a recording answered to no identifier at all")
	}
}

func TestRecordingRefusesALookupOnNoIDAtAll(t *testing.T) {
	client := testClient(t, func(http.ResponseWriter, *http.Request) {
		t.Error("the provider was asked about no recording at all")
	})

	if _, err := client.Recording(context.Background(), uuid.Nil); err == nil {
		t.Fatal("the provider was asked anyway")
	}
}

// A provider that broke is not a recording that does not exist. Saying so would
// have a caller record "MusicBrainz has never heard of this" about an outage.
func TestARecordingLookupThatFailedIsNotAnAbsence(t *testing.T) {
	client := testClient(t, func(response http.ResponseWriter, _ *http.Request) {
		response.WriteHeader(http.StatusInternalServerError)
	})

	_, err := client.Recording(context.Background(), mustUUID("205ff413-6fbe-48d2-bd64-940cc37f5d75"))

	if err == nil || errors.Is(err, ErrNotFound) {
		t.Fatalf("error = %v, want a failure that is not an absence", err)
	}
}

// A recording the provider named nothing is one nothing could ever be matched
// against, so it is no recording at all.
func TestARecordingTheProviderDidNotNameIsNotFound(t *testing.T) {
	client := testClient(t, func(response http.ResponseWriter, _ *http.Request) {
		_, _ = response.Write([]byte(`{"id":"205ff413-6fbe-48d2-bd64-940cc37f5d75","title":"   "}`))
	})

	_, err := client.Recording(context.Background(), mustUUID("205ff413-6fbe-48d2-bd64-940cc37f5d75"))

	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("error = %v, want ErrNotFound", err)
	}
}

func TestSearchRecordingsRefusesALimitItCannotAsk(t *testing.T) {
	client := testClient(t, func(http.ResponseWriter, *http.Request) {
		t.Error("the provider was asked for a page it cannot serve")
	})

	for _, limit := range []int{0, -1, 101} {
		if _, err := client.SearchRecordings(context.Background(),
			RecordingQuery{Title: "Karma Police"}, limit); err == nil {
			t.Errorf("a limit of %d was asked for anyway", limit)
		}
	}
}

func TestSearchRecordingsReportsAProviderThatWouldNotAnswer(t *testing.T) {
	client := testClient(t, func(response http.ResponseWriter, _ *http.Request) {
		response.WriteHeader(http.StatusInternalServerError)
	})

	if _, err := client.SearchRecordings(context.Background(),
		RecordingQuery{Title: "Karma Police"}, 5); err == nil {
		t.Fatal("a provider that would not answer was read as no candidates")
	}
}

// An identifier is asked for as the word it is. A tagger writing an ISRC with
// the punctuation people put in them must not turn into query syntax.
func TestAnIdentifierIsAskedForAsATermRatherThanAsSyntax(t *testing.T) {
	var gotQuery string
	client := testClient(t, func(response http.ResponseWriter, request *http.Request) {
		gotQuery = request.URL.Query().Get("query")
		_, _ = response.Write([]byte(`{"count":0,"recordings":[]}`))
	})

	if _, err := client.SearchRecordings(context.Background(),
		RecordingQuery{ISRC: "GB-AYE-97-00325"}, 5); err != nil {
		t.Fatalf("SearchRecordings() error = %v", err)
	}
	if gotQuery != `isrc:GB\-AYE\-97\-00325` {
		t.Errorf("query = %q, want every special character escaped", gotQuery)
	}
}

// A result the provider could not identify or name is not a candidate: there
// would be nothing to compare a file against.
func TestSearchResultsTheProviderCouldNotIdentifyAreLeftOut(t *testing.T) {
	client := testClient(t, func(response http.ResponseWriter, _ *http.Request) {
		_, _ = response.Write([]byte(`{"count":3,"recordings":[
			{"id":"not-a-uuid","title":"Karma Police"},
			{"id":"205ff413-6fbe-48d2-bd64-940cc37f5d75","title":"   "},
			{"id":"b9ad642e-b012-41c7-b72a-42cf4911f9ff","title":"Karma Police"}
		]}`))
	})

	recordings, err := client.SearchRecordings(context.Background(),
		RecordingQuery{Title: "Karma Police"}, 5)
	if err != nil {
		t.Fatalf("SearchRecordings() error = %v", err)
	}
	if len(recordings) != 1 || recordings[0].ID != mustUUID("b9ad642e-b012-41c7-b72a-42cf4911f9ff") {
		t.Fatalf("recordings = %+v, want only the one that identified and named itself", recordings)
	}
}

// A release the provider could not identify cannot describe where the recording
// appears, so it is left out rather than carried as a blank edition.
func TestAReleaseTheProviderCouldNotIdentifyIsLeftOut(t *testing.T) {
	client := testClient(t, func(response http.ResponseWriter, _ *http.Request) {
		_, _ = response.Write([]byte(`{
			"id":"205ff413-6fbe-48d2-bd64-940cc37f5d75","title":"Karma Police",
			"releases":[
				{"id":"not-a-uuid","title":"Nowhere"},
				{"id":"2d7f08e1-2e0e-4b1e-9f31-3c4d1d2f5b6a","title":"OK Computer",
				 "release-group":{"id":"not-a-uuid-either"}}
			]}`))
	})

	recording, err := client.Recording(context.Background(),
		mustUUID("205ff413-6fbe-48d2-bd64-940cc37f5d75"))
	if err != nil {
		t.Fatalf("Recording() error = %v", err)
	}
	if len(recording.Releases) != 1 || recording.Releases[0].Title != "OK Computer" {
		t.Fatalf("releases = %+v, want only the one that identified itself", recording.Releases)
	}
	if recording.Releases[0].ReleaseGroupID != uuid.Nil {
		t.Errorf("release group = %s, want nothing rather than a guess",
			recording.Releases[0].ReleaseGroupID)
	}
}

// Two editions of the same standing are ordered by date and then by an
// identifier, so a file that names no album is always described the same way.
func TestEqualEditionsOfARecordingAreOrderedByDateThenIdentifier(t *testing.T) {
	client := testClient(t, func(response http.ResponseWriter, _ *http.Request) {
		_, _ = response.Write([]byte(`{
			"id":"205ff413-6fbe-48d2-bd64-940cc37f5d75","title":"Karma Police",
			"releases":[
				{"id":"33333333-3333-4333-8333-333333333333","title":"Later","status":"Official","date":"2007"},
				{"id":"22222222-2222-4222-8222-222222222222","title":"Same day","status":"Official","date":"1997-05-21"},
				{"id":"11111111-1111-4111-8111-111111111111","title":"Same day","status":"Official","date":"1997-05-21"}
			]}`))
	})

	recording, err := client.Recording(context.Background(),
		mustUUID("205ff413-6fbe-48d2-bd64-940cc37f5d75"))
	if err != nil {
		t.Fatalf("Recording() error = %v", err)
	}
	want := []string{
		"11111111-1111-4111-8111-111111111111",
		"22222222-2222-4222-8222-222222222222",
		"33333333-3333-4333-8333-333333333333",
	}
	for index, id := range want {
		if recording.Releases[index].ID.String() != id {
			t.Fatalf("release %d = %s, want %s", index, recording.Releases[index].ID, id)
		}
	}
}

// A track position the provider left out is silence rather than position zero,
// so the next track that does say where it sits is the one read.
func TestATrackWithNoPositionAtAllIsPassedOver(t *testing.T) {
	client := testClient(t, func(response http.ResponseWriter, _ *http.Request) {
		_, _ = response.Write([]byte(`{
			"id":"205ff413-6fbe-48d2-bd64-940cc37f5d75","title":"Karma Police",
			"releases":[{"id":"2d7f08e1-2e0e-4b1e-9f31-3c4d1d2f5b6a","title":"OK Computer",
				"media":[{"track":[{"number":"","title":"Karma Police"},{"number":"6"}]}]}]}`))
	})

	recording, err := client.Recording(context.Background(),
		mustUUID("205ff413-6fbe-48d2-bd64-940cc37f5d75"))
	if err != nil {
		t.Fatalf("Recording() error = %v", err)
	}
	release := recording.Releases[0]
	if release.DiscNumber != 1 || release.TrackNumber != 6 {
		t.Fatalf("position = %d-%d, want disc 1 track 6", release.DiscNumber, release.TrackNumber)
	}
}

// A credit the provider printed no name for is still the artist it names, so the
// artist's own name stands in rather than leaving the credit half written.
func TestACreditWithNoPrintedNameFallsBackToTheArtistsOwn(t *testing.T) {
	client := testClient(t, func(response http.ResponseWriter, _ *http.Request) {
		_, _ = response.Write([]byte(`{
			"id":"205ff413-6fbe-48d2-bd64-940cc37f5d75","title":"Karma Police",
			"artist-credit":[
				{"name":"","joinphrase":" & ","artist":{"name":"Radiohead"}},
				{"name":"Guest","joinphrase":"","artist":{"name":"Guest"}}
			]}`))
	})

	recording, err := client.Recording(context.Background(),
		mustUUID("205ff413-6fbe-48d2-bd64-940cc37f5d75"))
	if err != nil {
		t.Fatalf("Recording() error = %v", err)
	}
	if recording.ArtistCredit != "Radiohead & Guest" {
		t.Errorf("credit = %q, want the artist's own name to stand in", recording.ArtistCredit)
	}
}

// creditFixture reads a credit the way the provider sends it, so the fixtures
// here are the same shape as the ones the HTTP tests answer with.
func creditFixture(t *testing.T, body string) []artistCreditItem {
	t.Helper()
	var credits []artistCreditItem
	if err := json.Unmarshal([]byte(body), &credits); err != nil {
		t.Fatalf("decode credit fixture: %v", err)
	}
	return credits
}

// The list is a decomposition of the printed credit and not a second opinion
// about it, so joining it back up has to reproduce that string exactly. If the
// two ever disagree, one of them is wrong and nothing here can tell which.
func TestJoiningTheCreditListReproducesThePrintedCredit(t *testing.T) {
	fixtures := map[string]string{
		"nobody credited": `[]`,
		"one artist": `[
			{"name":"Radiohead","joinphrase":"","artist":{"id":"a74b1b7f-71a5-4011-9441-d0b5e4122711","name":"Radiohead"}}]`,
		"a guest": `[
			{"name":"Radiohead","joinphrase":" feat. ","artist":{"name":"Radiohead"}},
			{"name":"Guest","joinphrase":"","artist":{"name":"Guest"}}]`,
		"an ampersand": `[
			{"name":"Jay-Z","joinphrase":" & ","artist":{"name":"Jay-Z"}},
			{"name":"Kanye West","joinphrase":"","artist":{"name":"Kanye West"}}]`,
		"a comma between two artists": `[
			{"name":"Porter Robinson","joinphrase":", ","artist":{"name":"Porter Robinson"}},
			{"name":"Ninajirachi","joinphrase":"","artist":{"name":"Ninajirachi"}}]`,
		// One artist whose own name holds the comma, which is why a printed
		// credit is never cut at one.
		"a comma inside one name": `[
			{"name":"Emerson, Lake & Palmer","joinphrase":"","artist":{"name":"Emerson, Lake & Palmer"}}]`,
		"a name the sleeve prints differently": `[
			{"name":"P!nk","joinphrase":"","artist":{"id":"f4d5cc07-3bc9-4836-9b15-88a08359bc63","name":"Pink"}}]`,
		"no printed name": `[
			{"name":"","joinphrase":" & ","artist":{"name":"Radiohead"}},
			{"name":"Guest","joinphrase":"","artist":{"name":"Guest"}}]`,
		"nobody MusicBrainz holds": `[
			{"name":"Unknown","joinphrase":"","artist":{"name":"Unknown"}}]`,
		// An entry naming nobody at all still printed its join phrase, so it is
		// still an entry. Leaving it out would take that phrase with it.
		"an entry with no name anywhere": `[
			{"name":"","joinphrase":" & ","artist":{}},
			{"name":"Guest","joinphrase":"","artist":{"name":"Guest"}}]`,
		"whitespace around the printed name": `[
			{"name":"  Radiohead  ","joinphrase":" & ","artist":{"name":"Radiohead"}},
			{"name":"Guest ","joinphrase":"","artist":{"name":"Guest"}}]`,
	}
	for name, body := range fixtures {
		t.Run(name, func(t *testing.T) {
			credits := creditFixture(t, body)
			var builder strings.Builder
			for _, credit := range artistCredits(credits) {
				builder.WriteString(credit.Name)
				builder.WriteString(credit.JoinPhrase)
			}
			rejoined := strings.TrimSpace(builder.String())
			if printed := artistCredit(credits); rejoined != printed {
				t.Errorf("rejoined = %q, printed = %q", rejoined, printed)
			}
		})
	}
}

// The sleeve is what a listener reads, so the name this release prints is kept
// against the artist MusicBrainz says it is.
func TestACreditKeepsTheNameTheSleevePrints(t *testing.T) {
	credits := artistCredits(creditFixture(t,
		`[{"name":"P!nk","joinphrase":"","artist":{"id":"f4d5cc07-3bc9-4836-9b15-88a08359bc63","name":"Pink"}}]`))
	if len(credits) != 1 {
		t.Fatalf("credits = %#v, want one", credits)
	}
	if credits[0].Name != "P!nk" {
		t.Errorf("name = %q, want the name the sleeve prints", credits[0].Name)
	}
	if credits[0].ArtistID != mustUUID("f4d5cc07-3bc9-4836-9b15-88a08359bc63") {
		t.Errorf("artist = %s, want the artist that name is", credits[0].ArtistID)
	}
}

// A credit the provider names no artist for is silence about the artist. The
// entry stays where it stands, or the next artist's identifier ends up against
// this one's name.
func TestACreditNamingNoArtistEntityKeepsItsPlace(t *testing.T) {
	credits := artistCredits(creditFixture(t, `[
		{"name":"Unknown","joinphrase":" & ","artist":{"name":"Unknown"}},
		{"name":"Radiohead","joinphrase":"","artist":{"id":"a74b1b7f-71a5-4011-9441-d0b5e4122711","name":"Radiohead"}}]`))
	if len(credits) != 2 {
		t.Fatalf("credits = %#v, want both entries", credits)
	}
	if credits[0].Name != "Unknown" || credits[0].ArtistID != uuid.Nil {
		t.Errorf("first = %#v, want the name with no identifier", credits[0])
	}
	if credits[1].ArtistID != mustUUID("a74b1b7f-71a5-4011-9441-d0b5e4122711") {
		t.Errorf("second = %#v, want the identifier still against its own name", credits[1])
	}
}

// A credit the provider printed no name for takes the artist's own name in the
// list too, exactly as the printed credit does.
func TestACreditWithNoPrintedNameTakesTheArtistsOwnInTheList(t *testing.T) {
	credits := artistCredits(creditFixture(t, `[
		{"name":"","joinphrase":" & ","artist":{"name":"Radiohead"}},
		{"name":"Guest","joinphrase":"","artist":{"name":"Guest"}}]`))
	if len(credits) != 2 || credits[0].Name != "Radiohead" {
		t.Errorf("credits = %#v, want the artist's own name to stand in", credits)
	}
}

// A recording credited to two artists reaches Schall as two, so the second one
// is reachable rather than buried in the middle of a string.
func TestARecordingKeepsEveryArtistItIsCreditedTo(t *testing.T) {
	client := testClient(t, func(response http.ResponseWriter, _ *http.Request) {
		_, _ = response.Write([]byte(`{
			"id":"205ff413-6fbe-48d2-bd64-940cc37f5d75","title":"Karma Police",
			"artist-credit":[
				{"name":"Radiohead","joinphrase":" feat. ",
				 "artist":{"id":"a74b1b7f-71a5-4011-9441-d0b5e4122711","name":"Radiohead"}},
				{"name":"Guest","joinphrase":"",
				 "artist":{"id":"f4d5cc07-3bc9-4836-9b15-88a08359bc63","name":"Guest"}}
			]}`))
	})

	recording, err := client.Recording(context.Background(),
		mustUUID("205ff413-6fbe-48d2-bd64-940cc37f5d75"))
	if err != nil {
		t.Fatalf("Recording() error = %v", err)
	}
	want := []Credit{
		{Name: "Radiohead", ArtistID: mustUUID("a74b1b7f-71a5-4011-9441-d0b5e4122711"),
			JoinPhrase: " feat. ", ArtistName: "Radiohead"},
		{Name: "Guest", ArtistID: mustUUID("f4d5cc07-3bc9-4836-9b15-88a08359bc63"),
			ArtistName: "Guest"},
	}
	if len(recording.Credits) != len(want) {
		t.Fatalf("credits = %#v, want both artists", recording.Credits)
	}
	for index, credit := range want {
		if !reflect.DeepEqual(recording.Credits[index], credit) {
			t.Errorf("credit %d = %#v, want %#v", index, recording.Credits[index], credit)
		}
	}
}

// The credited name is not always the artist's own, and neither is always what a
// tagger writes. Both, and the provider's other names for the artist, travel
// with the credit so a credit can be compared as the set of artists it is
// (ADR 0030).
func TestARecordingKeepsTheOtherNamesItsArtistsAnswerTo(t *testing.T) {
	client := testClient(t, func(response http.ResponseWriter, _ *http.Request) {
		_, _ = response.Write([]byte(`{
			"id":"205ff413-6fbe-48d2-bd64-940cc37f5d75","title":"SICKO MODE",
			"artist-credit":[
				{"name":"Travi$ Scott","joinphrase":"",
				 "artist":{"id":"e4ac4e1c-2b2b-4f3d-9a0a-6b1c2d3e4f50",
				           "name":"Travis Scott","sort-name":"Scott, Travis",
				           "aliases":[{"name":"Jacques Webster"},{"name":"  "}]}}
			]}`))
	})

	recording, err := client.Recording(context.Background(),
		mustUUID("205ff413-6fbe-48d2-bd64-940cc37f5d75"))
	if err != nil {
		t.Fatalf("Recording() error = %v", err)
	}
	if len(recording.Credits) != 1 {
		t.Fatalf("credits = %#v, want the one artist", recording.Credits)
	}
	credit := recording.Credits[0]
	if credit.Name != "Travi$ Scott" || credit.ArtistName != "Travis Scott" {
		t.Errorf("credit = %#v, want the printed name and the artist's own", credit)
	}
	if !reflect.DeepEqual(credit.Aliases, []string{"Jacques Webster"}) {
		t.Errorf("aliases = %#v, want the named alias and no blank one", credit.Aliases)
	}
}

// An ID nothing tied to this recording is not one it answers to.
func TestARecordingDoesNotAnswerToAnUnrelatedID(t *testing.T) {
	client := testClient(t, func(response http.ResponseWriter, _ *http.Request) {
		_, _ = response.Write([]byte(`{
			"id": "205ff413-6fbe-48d2-bd64-940cc37f5d75",
			"title": "Karma Police"
		}`))
	})

	recording, err := client.Recording(context.Background(),
		mustUUID("205ff413-6fbe-48d2-bd64-940cc37f5d75"))
	if err != nil {
		t.Fatalf("Recording() error = %v", err)
	}
	if recording.Answers(mustUUID("b9ad642e-b012-41c7-b72a-42cf4911f9ff")) {
		t.Error("an unrelated ID must not name this recording")
	}
}

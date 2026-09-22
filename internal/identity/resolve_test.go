package identity

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/pxldi/schall/internal/musicbrainz"
)

// stubProvider counts what resolution actually asked for, because provider
// requests are rate limited and a library scan resolves thousands of files.
type stubProvider struct {
	lookups   int
	searches  []musicbrainz.RecordingQuery
	recording musicbrainz.Recording
	lookupErr error
	results   []musicbrainz.Recording
	// answer replies to one ID at a time, for the tests where two lookups must
	// give different answers. Optional: without it the single recording above
	// stands for every lookup.
	answer func(uuid.UUID) (musicbrainz.Recording, error)
	// resolutions counts the narrow question separately, because which of the two
	// the reconciliations ask is the difference between one request per candidate
	// copy and one for all of them.
	resolutions int
}

func (provider *stubProvider) known(id uuid.UUID) (musicbrainz.Recording, error) {
	if provider.answer != nil {
		return provider.answer(id)
	}
	return provider.recording, provider.lookupErr
}

func (provider *stubProvider) Recording(
	_ context.Context, id uuid.UUID,
) (musicbrainz.Recording, error) {
	provider.lookups++
	return provider.known(id)
}

// ResolveRecordingID answers out of the same recordings a lookup would return, so
// a test says what MusicBrainz knows in one place and the merges follow from it.
func (provider *stubProvider) ResolveRecordingID(
	_ context.Context, id uuid.UUID,
) (uuid.UUID, error) {
	provider.resolutions++
	found, err := provider.known(id)
	if err != nil {
		return uuid.Nil, err
	}
	return found.ID, nil
}

func (provider *stubProvider) SearchRecordings(
	_ context.Context, query musicbrainz.RecordingQuery, _ int,
) ([]musicbrainz.Recording, error) {
	provider.searches = append(provider.searches, query)
	return provider.results, nil
}

func TestAProvenIdentifierCostsOneRequest(t *testing.T) {
	provider := &stubProvider{
		recording: recording(firstID, "Karma Police", "Radiohead", 308973, "OK Computer"),
	}
	outcome, err := NewResolver(provider).Resolve(context.Background(), Evidence{
		RecordingID: uuid.MustParse(firstID), ISRC: "GBAYE9700325",
		Artist: "Radiohead", Title: "Karma Police", DurationMS: 308500,
	})
	if err != nil {
		t.Fatalf("Resolve() error = %v", err)
	}
	if outcome.Status != StatusResolved {
		t.Fatalf("status = %q", outcome.Status)
	}
	if provider.lookups != 1 || len(provider.searches) != 0 {
		t.Errorf("lookups = %d searches = %v, want the lookup alone", provider.lookups, provider.searches)
	}
}

func TestAnUnknownRecordingIDIsAConflictRatherThanAnAbsence(t *testing.T) {
	provider := &stubProvider{lookupErr: musicbrainz.ErrNotFound}
	outcome, err := NewResolver(provider).Resolve(context.Background(), Evidence{
		RecordingID: uuid.MustParse(firstID), Artist: "Radiohead", Title: "Karma Police",
	})
	if err != nil {
		t.Fatalf("Resolve() error = %v", err)
	}
	if outcome.Status != StatusConflict {
		t.Fatalf("status = %q, want %q", outcome.Status, StatusConflict)
	}
	if !strings.Contains(outcome.Summary, "not known to MusicBrainz") {
		t.Errorf("summary = %q", outcome.Summary)
	}
	if len(provider.searches) != 1 || provider.searches[0].Title != "Karma Police" {
		t.Errorf("searches = %+v, want the file searched for by what it says", provider.searches)
	}
}

func TestAFileWithNothingToAskAboutAsksNobody(t *testing.T) {
	provider := &stubProvider{}
	outcome, err := NewResolver(provider).Resolve(context.Background(), Evidence{Artist: "Only an artist"})
	if err != nil {
		t.Fatalf("Resolve() error = %v", err)
	}
	if outcome.Status != StatusLocalOnly {
		t.Errorf("status = %q", outcome.Status)
	}
	if provider.lookups != 0 || len(provider.searches) != 0 {
		t.Errorf("the provider was asked about a file with no evidence")
	}
}

// The whole point of listening: a file with nothing written on it used to be
// unaskable, because every question resolution had was a question about a tag.
func TestAudioIdentifiesAFileWithNothingWrittenOnIt(t *testing.T) {
	provider := &stubProvider{
		recording: recording(firstID, "Karma Police", "Radiohead", 308973, "OK Computer"),
	}
	outcome, err := NewResolver(provider).Resolve(context.Background(), Evidence{
		Acoustic: heard(cluster(0.99, firstID)),
	})
	if err != nil {
		t.Fatalf("Resolve() error = %v", err)
	}
	if outcome.Status != StatusResolved || outcome.Identity == nil {
		t.Fatalf("status = %q identity = %v, want the audio to identify it",
			outcome.Status, outcome.Identity)
	}
	if outcome.Identity.Method != "fingerprint" {
		t.Errorf("method = %q, want the audio to be what it rests on", outcome.Identity.Method)
	}
}

// The tags alone identify this file. The audio says it is other music, and that
// outranks everything written on it — the refusal is the point, and it holds at
// any similarity.
func TestAudioNamingOtherRecordingsVoidsWhatTheTagsAgreedOn(t *testing.T) {
	provider := &stubProvider{
		answer: func(id uuid.UUID) (musicbrainz.Recording, error) {
			return recording(secondID, "Paranoid Android", "Radiohead", 383000), nil
		},
		results: []musicbrainz.Recording{
			recording(firstID, "Karma Police", "Radiohead", 308973, "OK Computer"),
		},
	}
	outcome, err := NewResolver(provider).Resolve(context.Background(), Evidence{
		Artist: "Radiohead", Title: "Karma Police", DurationMS: 308500,
		Acoustic: heard(cluster(0.99, secondID)),
	})
	if err != nil {
		t.Fatalf("Resolve() error = %v", err)
	}
	if outcome.Status == StatusResolved {
		t.Fatalf("status = %q, want the audio's answer to void the tags' one", outcome.Status)
	}
}

// A lower cluster is other audio that resembled the fingerprint. The recording
// named there is not a weaker answer; it is not an answer at all, and reading it
// as one would be the file being mistaken for the music it resembles.
func TestARecordingNamedOnlyByALowerClusterIsNotAnAnswer(t *testing.T) {
	provider := &stubProvider{
		answer: func(id uuid.UUID) (musicbrainz.Recording, error) {
			return recording(secondID, "Paranoid Android", "Radiohead", 383000), nil
		},
		results: []musicbrainz.Recording{
			recording(firstID, "Karma Police", "Radiohead", 308973, "OK Computer"),
		},
	}
	outcome, err := NewResolver(provider).Resolve(context.Background(), Evidence{
		Artist: "Radiohead", Title: "Karma Police", DurationMS: 308500,
		Acoustic: heard(cluster(0.99, secondID), cluster(0.85, firstID)),
	})
	if err != nil {
		t.Fatalf("Resolve() error = %v", err)
	}
	if outcome.Identity != nil && outcome.Identity.RecordingID == uuid.MustParse(firstID) {
		t.Fatalf("identity = %+v, want a name from a lower cluster to identify nothing",
			outcome.Identity)
	}
	if outcome.Status == StatusResolved {
		t.Fatalf("status = %q, want the leading cluster's refusal to stand", outcome.Status)
	}
}

func distinctAudioProvider() *stubProvider {
	return &stubProvider{answer: func(id uuid.UUID) (musicbrainz.Recording, error) {
		if id == uuid.MustParse(firstID) {
			return recording(firstID, "Song", "Artist", 240000), nil
		}
		return recording(secondID, "Other Song", "Other Artist", 240000), nil
	}}
}

func TestAmbiguousLeadingClusterDoesNotVerify(t *testing.T) {
	verification, err := NewResolver(distinctAudioProvider()).Verify(context.Background(), Evidence{
		Acoustic: heard(cluster(0.99, firstID, secondID)),
	}, uuid.MustParse(firstID))
	if err != nil {
		t.Fatal(err)
	}
	if verification.Proven() {
		t.Fatalf("verification = %+v, want an unresolved ambiguity", verification)
	}
}

func TestTiedConflictingClustersDoNotVerify(t *testing.T) {
	verification, err := NewResolver(distinctAudioProvider()).Verify(context.Background(), Evidence{
		Acoustic: heard(cluster(0.99, firstID), cluster(0.99, secondID)),
	}, uuid.MustParse(firstID))
	if err != nil {
		t.Fatal(err)
	}
	if verification.Proven() {
		t.Fatalf("verification = %+v, want an unresolved tie", verification)
	}
}

func TestSingleLeadingClusterStillVerifies(t *testing.T) {
	verification, err := NewResolver(distinctAudioProvider()).Verify(context.Background(), Evidence{
		Acoustic: heard(cluster(0.99, firstID)),
	}, uuid.MustParse(firstID))
	if err != nil {
		t.Fatal(err)
	}
	if !verification.Proven() {
		t.Fatalf("verification = %+v, want the single recording admitted", verification)
	}
}

// Distinct registrations in one cluster are unknown until a person or an
// existing registration proof can establish which recording the audio names.
func TestOnePerformanceEnteredTwiceIsAQuestionRatherThanAChoice(t *testing.T) {
	provider := &stubProvider{
		answer: func(id uuid.UUID) (musicbrainz.Recording, error) {
			if id == uuid.MustParse(firstID) {
				return recording(firstID, "Karma Police", "Radiohead", 308973, "OK Computer"), nil
			}
			return recording(secondID, "Karma Police", "Radiohead", 308973, "Airbag/How Am I Driving?"), nil
		},
	}
	outcome, err := NewResolver(provider).Resolve(context.Background(), Evidence{
		Acoustic: heard(cluster(0.99, firstID, secondID)),
	})
	if err != nil {
		t.Fatalf("Resolve() error = %v", err)
	}
	if outcome.Status != StatusLocalOnly {
		t.Fatalf("status = %q, want %q", outcome.Status, StatusLocalOnly)
	}
	if len(outcome.Candidates) != 0 {
		t.Errorf("candidates = %d, want no proof-grade candidate", len(outcome.Candidates))
	}
}

// AcoustID has never heard most self-released music. An empty answer is a fact
// about the database, and the file is left exactly as its tags left it.
func TestAudioNobodyRecognisedRefusesNothing(t *testing.T) {
	provider := &stubProvider{}
	outcome, err := NewResolver(provider).Resolve(context.Background(), Evidence{
		Acoustic: &Acoustic{},
	})
	if err != nil {
		t.Fatalf("Resolve() error = %v", err)
	}
	if outcome.Status != StatusLocalOnly {
		t.Fatalf("status = %q, want %q", outcome.Status, StatusLocalOnly)
	}
	if provider.lookups != 0 || len(provider.searches) != 0 {
		t.Errorf("the provider was asked about a file nobody could name")
	}
}

// AcoustID collects recording IDs submitted over many years, so it can name a row
// MusicBrainz no longer has. That is silence about this file, not the file naming
// something that does not exist — which is what a dead identifier in a tag is.
func TestANameMusicBrainzNoLongerHasIsNotAConflict(t *testing.T) {
	provider := &stubProvider{lookupErr: musicbrainz.ErrNotFound}
	outcome, err := NewResolver(provider).Resolve(context.Background(), Evidence{
		Acoustic: heard(cluster(0.99, firstID)),
	})
	if err != nil {
		t.Fatalf("Resolve() error = %v", err)
	}
	if outcome.Status != StatusLocalOnly {
		t.Fatalf("status = %q, want %q", outcome.Status, StatusLocalOnly)
	}
}

func TestAProviderFailureIsNotAnAnswer(t *testing.T) {
	provider := &stubProvider{lookupErr: errors.New("connection refused")}
	if _, err := NewResolver(provider).Resolve(context.Background(), Evidence{
		RecordingID: uuid.MustParse(firstID), Title: "Karma Police",
	}); err == nil {
		t.Fatal("Resolve() error = nil, want the failure to be reported for retry")
	}
}

// A recording somebody already decided is not this entry must not come back as
// the answer, and must not come back as a candidate either. The review queue's
// wrong-target decision is what writes such an exclusion, and a resolution that
// re-offered the rejected recording would ask a question that has been answered.
func TestAnExcludedRecordingIsNeitherAnswerNorCandidate(t *testing.T) {
	rejected := recording(firstID, "Glory Box", "Portishead", 301000, "Dummy")
	provider := &stubProvider{results: []musicbrainz.Recording{rejected}}

	outcome, err := NewResolver(provider).Resolve(context.Background(), Evidence{
		Subject: SubjectEntry, Artist: "Portishead", Title: "Glory Box",
		DurationMS: 301000,
	}, uuid.MustParse(firstID))
	if err != nil {
		t.Fatalf("Resolve() error = %v", err)
	}
	if outcome.Status == StatusResolved {
		t.Fatalf("status = %q, want a recording nobody may propose again to be refused", outcome.Status)
	}
	for _, candidate := range outcome.Candidates {
		if candidate.RecordingID == uuid.MustParse(firstID) {
			t.Fatal("an excluded recording was offered as a candidate")
		}
	}
}

// An excluded recording named by the entry's own identifier is not looked up
// either: the request would cost a rate-limited round trip to learn something
// already decided.
func TestAnExcludedIdentifierIsNotEvenLookedUp(t *testing.T) {
	provider := &stubProvider{}
	if _, err := NewResolver(provider).Resolve(context.Background(), Evidence{
		Subject: SubjectEntry, RecordingID: uuid.MustParse(firstID), Title: "Glory Box",
	}, uuid.MustParse(firstID)); err != nil {
		t.Fatalf("Resolve() error = %v", err)
	}
	if provider.lookups != 0 {
		t.Fatalf("lookups = %d, want an excluded identifier not to be asked about", provider.lookups)
	}
}

// Verification asks one question about one recording, so it costs one request
// however much the file has to say for itself. A search would be answering a
// different question — what is this file — and that one is not being asked.
func TestVerifyingAFileCostsOneRequest(t *testing.T) {
	provider := &stubProvider{
		recording: recording(firstID, "Karma Police", "Radiohead", 308973, "OK Computer"),
	}
	verification, err := NewResolver(provider).Verify(context.Background(), Evidence{
		Artist: "Radiohead", Title: "Karma Police", DurationMS: 308500,
		Acoustic: heard(cluster(1, firstID)),
	}, uuid.MustParse(firstID))
	if err != nil {
		t.Fatalf("Verify() error = %v", err)
	}
	if !verification.Proven() || !verification.Audio {
		t.Fatalf("verification = %+v, want it proven by the audio", verification)
	}
	if provider.lookups != 1 || len(provider.searches) != 0 {
		t.Errorf("lookups = %d searches = %d, want one lookup and no search",
			provider.lookups, len(provider.searches))
	}
}

// Tags that agree prove nothing about a file a stranger sent, so verification
// says what proved it rather than only that something did. The caller is what
// decides whether that is enough (ADR 0002).
func TestVerifyingSaysWhenOnlyTheTagsAgreed(t *testing.T) {
	provider := &stubProvider{
		recording: recording(firstID, "Karma Police", "Radiohead", 308973, "OK Computer"),
	}
	verification, err := NewResolver(provider).Verify(context.Background(), Evidence{
		Artist: "Radiohead", Title: "Karma Police", DurationMS: 308500,
	}, uuid.MustParse(firstID))
	if err != nil {
		t.Fatalf("Verify() error = %v", err)
	}
	if !verification.Proven() {
		t.Fatalf("verification = %+v, want the tag grade reported", verification)
	}
	if verification.Audio {
		t.Error("nobody listened to this file, so the audio cannot have proved it")
	}
}

// The audio naming another recording is a refusal about the music. It is kept
// apart from a refusal about what the file claims, because one of them means this
// peer's copy is finished and the other may be a mistagged copy of the right
// music.
func TestVerifyingSeparatesTheAudioRefusalFromTheTagRefusal(t *testing.T) {
	// The audio names a recording that is genuinely other music, so the stub has
	// to answer for both IDs: one that says the same thing for every lookup would
	// be describing a merge rather than a disagreement.
	provider := &stubProvider{answer: func(id uuid.UUID) (musicbrainz.Recording, error) {
		if id == uuid.MustParse(secondID) {
			return recording(secondID, "Paranoid Android", "Radiohead", 383000), nil
		}
		return recording(firstID, "Karma Police", "Radiohead", 308973, "OK Computer"), nil
	}}
	resolver := NewResolver(provider)

	audio, err := resolver.Verify(context.Background(), Evidence{
		Title:    "Karma Police",
		Acoustic: heard(cluster(1, secondID)),
	}, uuid.MustParse(firstID))
	if err != nil {
		t.Fatalf("Verify() error = %v", err)
	}
	if audio.Proven() || !audio.AudioSaysOtherwise {
		t.Fatalf("verification = %+v, want the audio to have refused it", audio)
	}

	tags, err := resolver.Verify(context.Background(), Evidence{
		Title: "Karma Police", DurationMS: 120000,
	}, uuid.MustParse(firstID))
	if err != nil {
		t.Fatalf("Verify() error = %v", err)
	}
	if tags.Proven() || tags.AudioSaysOtherwise || !tags.Contradicted {
		t.Fatalf("verification = %+v, want the length to have refused it", tags)
	}
}

// A file nothing could decide is a question, and it says so with what stood for
// and against it rather than with a refusal.
func TestVerifyingReportsAFileNothingDecides(t *testing.T) {
	provider := &stubProvider{
		recording: recording(firstID, "Karma Police", "Radiohead", 308973, "OK Computer"),
	}
	verification, err := NewResolver(provider).Verify(context.Background(), Evidence{
		Title: "Karma Police", Acoustic: &Acoustic{},
	}, uuid.MustParse(firstID))
	if err != nil {
		t.Fatalf("Verify() error = %v", err)
	}
	if verification.Proven() || verification.Contradicted || verification.AudioSaysOtherwise {
		t.Fatalf("verification = %+v, want a question and no refusal", verification)
	}
	if len(verification.Agrees) == 0 || verification.Summary == "" {
		t.Errorf("verification = %+v, want it to say what agreed", verification)
	}
}

// The recording travels back with every verdict, including none. A question put
// to somebody has to say what the answer was supposed to be, and the caller
// cannot ask again for something the check already had in hand.
func TestVerifyingReportsTheRecordingEvenWhenNothingIsDecided(t *testing.T) {
	provider := &stubProvider{
		recording: recording(firstID, "Karma Police", "Radiohead", 308973, "OK Computer"),
	}
	verification, err := NewResolver(provider).Verify(context.Background(), Evidence{
		Title: "Karma Police", Acoustic: &Acoustic{},
	}, uuid.MustParse(firstID))
	if err != nil {
		t.Fatalf("Verify() error = %v", err)
	}
	if verification.Proven() {
		t.Fatal("this evidence must not prove anything")
	}
	if verification.Recording == nil || verification.Recording.ReleaseTitle != "OK Computer" ||
		verification.Recording.DurationMS == nil {
		t.Errorf("recording = %+v, want what the file was measured against", verification.Recording)
	}
}

// A recording MusicBrainz has never heard of says the resolution behind the want
// is wrong. Nothing can be proven against it, and the file is not what is at
// fault, so it is reported as its own kind of failure.
func TestVerifyingAgainstAnUnknownRecordingIsNotTheFilesFault(t *testing.T) {
	provider := &stubProvider{lookupErr: musicbrainz.ErrNotFound}
	_, err := NewResolver(provider).Verify(context.Background(), Evidence{
		Title: "Karma Police",
	}, uuid.MustParse(firstID))
	if !errors.Is(err, ErrRecordingUnknown) {
		t.Fatalf("error = %v, want %v", err, ErrRecordingUnknown)
	}
}

// A merge is invisible from the want's side: the lookup is of the canonical
// recording and comes back canonical, while the file carries the ID it was
// merged away from. Asking the provider about the file's ID is what reconciles
// them, and it is asked because the alternative is refusing the best-tagged copy
// there is, permanently.
func TestAFileTaggedBeforeAMergeIsVerifiedAgainstTheRecording(t *testing.T) {
	canonical := recording(firstID, "Karma Police", "Radiohead", 308973, "OK Computer")
	provider := &stubProvider{
		answer: func(uuid.UUID) (musicbrainz.Recording, error) { return canonical, nil },
	}

	verification, err := NewResolver(provider).Verify(context.Background(), Evidence{
		RecordingID: uuid.MustParse(secondID), Artist: "Radiohead",
		Title: "Karma Police", DurationMS: 308500,
	}, uuid.MustParse(firstID))
	if err != nil {
		t.Fatalf("Verify() error = %v", err)
	}
	if !verification.Proven() || verification.Contradicted {
		t.Fatalf("verification = %+v, want the merged-away ID reconciled", verification)
	}
}

// The reconciliation asks which recording the ID names and not what that
// recording is, because the narrow question is the one the client can remember.
// A want offered by twenty peers otherwise buys the same answer twenty times from
// a provider serving one request a second.
func TestReconcilingATagAsksOnlyWhichRecordingTheIDNames(t *testing.T) {
	canonical := recording(firstID, "Karma Police", "Radiohead", 308973, "OK Computer")
	provider := &stubProvider{
		answer: func(uuid.UUID) (musicbrainz.Recording, error) { return canonical, nil },
	}

	if _, err := NewResolver(provider).Verify(context.Background(), Evidence{
		RecordingID: uuid.MustParse(secondID), Artist: "Radiohead",
		Title: "Karma Police", DurationMS: 308500,
	}, uuid.MustParse(firstID)); err != nil {
		t.Fatalf("Verify() error = %v", err)
	}
	if provider.lookups != 1 || provider.resolutions != 1 {
		t.Errorf("lookups = %d resolutions = %d, want the want looked up and the tag resolved",
			provider.lookups, provider.resolutions)
	}
}

// The same of the audio, where a cluster naming several recordings would
// otherwise cost a full lookup each.
func TestReconcilingAudioAsksOnlyWhichRecordingTheIDNames(t *testing.T) {
	canonical := recording(firstID, "Karma Police", "Radiohead", 308973, "OK Computer")
	provider := &stubProvider{
		answer: func(uuid.UUID) (musicbrainz.Recording, error) { return canonical, nil },
	}

	if _, err := NewResolver(provider).Verify(context.Background(), Evidence{
		Artist: "Radiohead", Title: "Karma Police", DurationMS: 308500,
		Acoustic: heard(cluster(0.99, secondID)),
	}, uuid.MustParse(firstID)); err != nil {
		t.Fatalf("Verify() error = %v", err)
	}
	if provider.lookups != 1 || provider.resolutions != 1 {
		t.Errorf("lookups = %d resolutions = %d, want the want looked up and the named ID resolved",
			provider.lookups, provider.resolutions)
	}
}

// An identifier that resolves to other music is the file claiming to be a
// different recording, which is what it always was.
func TestAFileTaggedWithAnotherRecordingIsStillRefused(t *testing.T) {
	provider := &stubProvider{answer: func(id uuid.UUID) (musicbrainz.Recording, error) {
		if id == uuid.MustParse(secondID) {
			return recording(secondID, "Paranoid Android", "Radiohead", 383000), nil
		}
		return recording(firstID, "Karma Police", "Radiohead", 308973, "OK Computer"), nil
	}}

	verification, err := NewResolver(provider).Verify(context.Background(), Evidence{
		RecordingID: uuid.MustParse(secondID), Artist: "Radiohead",
		Title: "Karma Police", DurationMS: 308500,
	}, uuid.MustParse(firstID))
	if err != nil {
		t.Fatalf("Verify() error = %v", err)
	}
	if verification.Proven() || !verification.Contradicted {
		t.Fatalf("verification = %+v, want the identifier to refuse it", verification)
	}
}

// An identifier MusicBrainz has never heard of is a file naming something that
// does not exist, and it goes on refusing.
func TestAnIdentifierMusicBrainzDoesNotKnowIsStillARefusal(t *testing.T) {
	provider := &stubProvider{answer: func(id uuid.UUID) (musicbrainz.Recording, error) {
		if id == uuid.MustParse(secondID) {
			return musicbrainz.Recording{}, musicbrainz.ErrNotFound
		}
		return recording(firstID, "Karma Police", "Radiohead", 308973, "OK Computer"), nil
	}}

	verification, err := NewResolver(provider).Verify(context.Background(), Evidence{
		RecordingID: uuid.MustParse(secondID), Artist: "Radiohead",
		Title: "Karma Police", DurationMS: 308500,
	}, uuid.MustParse(firstID))
	if err != nil {
		t.Fatalf("Verify() error = %v", err)
	}
	if verification.Proven() || !verification.Contradicted {
		t.Fatalf("verification = %+v, want an unknown identifier to refuse it", verification)
	}
}

// The verdict this reconciliation guards is permanent, so a provider nobody
// could reach must not become one. It is an error, which the caller retries.
func TestAProviderNobodyCanReachIsNotAPermanentRefusal(t *testing.T) {
	failure := errors.New("connection refused")
	provider := &stubProvider{answer: func(id uuid.UUID) (musicbrainz.Recording, error) {
		if id == uuid.MustParse(secondID) {
			return musicbrainz.Recording{}, failure
		}
		return recording(firstID, "Karma Police", "Radiohead", 308973, "OK Computer"), nil
	}}

	_, err := NewResolver(provider).Verify(context.Background(), Evidence{
		RecordingID: uuid.MustParse(secondID), Artist: "Radiohead",
		Title: "Karma Police", DurationMS: 308500,
	}, uuid.MustParse(firstID))
	if !errors.Is(err, failure) {
		t.Fatalf("error = %v, want the provider failure reported rather than a refusal", err)
	}
}

// The extra request is only on the path that would otherwise end the file. A
// file whose identifier already agrees costs what it always did.
func TestAnAgreeingIdentifierCostsNoExtraRequest(t *testing.T) {
	provider := &stubProvider{
		recording: recording(firstID, "Karma Police", "Radiohead", 308973, "OK Computer"),
	}

	if _, err := NewResolver(provider).Verify(context.Background(), Evidence{
		RecordingID: uuid.MustParse(firstID), Artist: "Radiohead",
		Title: "Karma Police", DurationMS: 308500,
	}, uuid.MustParse(firstID)); err != nil {
		t.Fatalf("Verify() error = %v", err)
	}
	if provider.lookups != 1 {
		t.Errorf("lookups = %d, want the one lookup", provider.lookups)
	}
}

// AcoustID has been collecting recording IDs for as long as MusicBrainz has been
// merging them, so the audio can name the ID this recording was merged away from.
// Read as a difference that is the audio naming other music, which is the one
// refusal that is final.
func TestAudioNamingAMergedAwayIDIdentifiesTheFile(t *testing.T) {
	canonical := recording(firstID, "Karma Police", "Radiohead", 308973, "OK Computer")
	provider := &stubProvider{
		answer: func(uuid.UUID) (musicbrainz.Recording, error) { return canonical, nil },
	}

	verification, err := NewResolver(provider).Verify(context.Background(), Evidence{
		Artist: "Radiohead", Title: "Karma Police", DurationMS: 308500,
		Acoustic: heard(cluster(0.99, secondID)),
	}, uuid.MustParse(firstID))
	if err != nil {
		t.Fatalf("Verify() error = %v", err)
	}
	if !verification.Proven() || !verification.Audio {
		t.Fatalf("verification = %+v, want the merged-away identification reconciled", verification)
	}
}

// Audio that was identified as different music is different music, whatever the
// provider says about the ID it named.
func TestAudioNamingAnotherRecordingIsStillRefused(t *testing.T) {
	provider := &stubProvider{answer: func(id uuid.UUID) (musicbrainz.Recording, error) {
		if id == uuid.MustParse(secondID) {
			return recording(secondID, "Paranoid Android", "Radiohead", 383000), nil
		}
		return recording(firstID, "Karma Police", "Radiohead", 308973, "OK Computer"), nil
	}}

	verification, err := NewResolver(provider).Verify(context.Background(), Evidence{
		Artist: "Radiohead", Title: "Karma Police", DurationMS: 308500,
		Acoustic: heard(cluster(0.99, secondID)),
	}, uuid.MustParse(firstID))
	if err != nil {
		t.Fatalf("Verify() error = %v", err)
	}
	if verification.Proven() || !verification.AudioSaysOtherwise {
		t.Fatalf("verification = %+v, want the audio to refuse it", verification)
	}
}

// A permanent verdict must not be a network outage in disguise, on the acoustic
// path as on the tagged one.
func TestAProviderNobodyCanReachIsNotAnAudioRefusal(t *testing.T) {
	failure := errors.New("connection refused")
	provider := &stubProvider{answer: func(id uuid.UUID) (musicbrainz.Recording, error) {
		if id == uuid.MustParse(secondID) {
			return musicbrainz.Recording{}, failure
		}
		return recording(firstID, "Karma Police", "Radiohead", 308973, "OK Computer"), nil
	}}

	_, err := NewResolver(provider).Verify(context.Background(), Evidence{
		Artist: "Radiohead", Title: "Karma Police", DurationMS: 308500,
		Acoustic: heard(cluster(0.99, secondID)),
	}, uuid.MustParse(firstID))
	if !errors.Is(err, failure) {
		t.Fatalf("error = %v, want the provider failure reported rather than a refusal", err)
	}
}

// The lookups are spent only where the file was about to be struck off. Audio
// that already names the recording costs what it always did.
func TestAgreeingAudioCostsNoExtraRequest(t *testing.T) {
	provider := &stubProvider{
		recording: recording(firstID, "Karma Police", "Radiohead", 308973, "OK Computer"),
	}

	if _, err := NewResolver(provider).Verify(context.Background(), Evidence{
		Artist: "Radiohead", Title: "Karma Police", DurationMS: 308500,
		Acoustic: heard(cluster(0.99, firstID)),
	}, uuid.MustParse(firstID)); err != nil {
		t.Fatalf("Verify() error = %v", err)
	}
	if provider.lookups != 1 {
		t.Errorf("lookups = %d, want the one lookup", provider.lookups)
	}
}

// Two distinct recording IDs in one cluster remain unknown until MusicBrainz
// establishes that they are one registration.
func TestOneClusterNamingTwoDistinctRowsDoesNotProveTheCopy(t *testing.T) {
	const otherID = "6f3c1b0a-6d3f-4a2f-9a26-1cbe4d8a5d21"
	provider := &stubProvider{answer: func(id uuid.UUID) (musicbrainz.Recording, error) {
		if id == uuid.MustParse(otherID) {
			return recording(otherID, "37564 (Sewerslvt remix)", "Sewerslvt", 162667), nil
		}
		return recording(firstID, "inmelancholy", "Sewerslvt", 162667), nil
	}}

	verification, err := NewResolver(provider).Verify(context.Background(), Evidence{
		Title:    "inmelancholy",
		Acoustic: heard(cluster(0.99, firstID, otherID)),
	}, uuid.MustParse(firstID))
	if err != nil {
		t.Fatalf("Verify() error = %v", err)
	}
	if verification.Proven() || verification.Audio {
		t.Fatalf("verification = %+v, want the cluster to remain unknown", verification)
	}
}

// The 卒業ですね case: the wanted recording turns up in a lower cluster while the
// best one is a different remix that happens to share a compilation. A lower
// cluster is other audio that resembled the fingerprint, so the copy is refused
// on the audio — the refusal that is final, because the audio has said what this
// is and it is not what somebody wanted.
func TestAWantNamedOnlyByALowerClusterIsRefusedOnTheAudio(t *testing.T) {
	const otherID = "6f3c1b0a-6d3f-4a2f-9a26-1cbe4d8a5d21"
	provider := &stubProvider{answer: func(id uuid.UUID) (musicbrainz.Recording, error) {
		if id == uuid.MustParse(otherID) {
			return recording(otherID, "Get Your Wish (Sewerslvt remix)", "Sewerslvt", 252632), nil
		}
		return recording(firstID, "卒業ですね (Sewerslvt remix)", "Sewerslvt", 267000), nil
	}}

	verification, err := NewResolver(provider).Verify(context.Background(), Evidence{
		Title:    "卒業ですね (Sewerslvt remix)",
		Acoustic: heard(cluster(0.99, otherID), cluster(0.4, firstID)),
	}, uuid.MustParse(firstID))
	if err != nil {
		t.Fatalf("Verify() error = %v", err)
	}
	if verification.Proven() || !verification.AudioSaysOtherwise {
		t.Fatalf("verification = %+v, want the leading cluster to refuse it", verification)
	}
}

// Reconciling a merged-away ID is asked only of the leading cluster. A lower one
// is other audio, and looking its names up until one resolved to the wanted
// recording would turn "this is not it" into an admission by the back door.
func TestAMergedAwayIDInALowerClusterIsNotReconciled(t *testing.T) {
	const otherID = "6f3c1b0a-6d3f-4a2f-9a26-1cbe4d8a5d21"
	canonical := recording(firstID, "Karma Police", "Radiohead", 308973, "OK Computer")
	provider := &stubProvider{answer: func(id uuid.UUID) (musicbrainz.Recording, error) {
		if id == uuid.MustParse(otherID) {
			return recording(otherID, "Paranoid Android", "Radiohead", 383000), nil
		}
		// Every other ID, secondID included, resolves to the wanted recording, so
		// reconciling one would look exactly like a merge.
		return canonical, nil
	}}

	verification, err := NewResolver(provider).Verify(context.Background(), Evidence{
		Title:    "Karma Police",
		Acoustic: heard(cluster(0.99, otherID), cluster(0.4, secondID)),
	}, uuid.MustParse(firstID))
	if err != nil {
		t.Fatalf("Verify() error = %v", err)
	}
	if verification.Proven() || !verification.AudioSaysOtherwise {
		t.Fatalf("verification = %+v, want the lower cluster left out of it", verification)
	}
}

// The credit no longer refuses a copy AcoustID identified. The leading cluster
// named the wanted recording, so the copy is that recording, and the artist tag
// stays on the record as disagreeing (ADR 0030).
func TestVerifyingAdmitsACopyTheAudioNamedAndTheCreditDoesNot(t *testing.T) {
	provider := &stubProvider{recording: creditedTo(
		recording(firstID, "SICKO MODE", "Travi$ Scott", 312000),
		[]string{"Travi$ Scott", "Travis Scott"}, []string{"Drake"},
	)}
	verification, err := NewResolver(provider).Verify(context.Background(), Evidence{
		Artist: "JACKBOYS", Title: "SICKO MODE", DurationMS: 312000,
		Acoustic: heard(cluster(0.99, firstID)),
	}, uuid.MustParse(firstID))
	if err != nil {
		t.Fatalf("Verify() error = %v", err)
	}
	if !verification.Proven() || !verification.Audio {
		t.Fatalf("verification = %+v, want the audio to have proven it", verification)
	}
	if !strings.Contains(strings.Join(verification.Differs, ", "), "artist") {
		t.Errorf("differs = %v, want the artist still listed as disagreeing",
			verification.Differs)
	}
}

// A length this recording cannot have refuses the copy whatever the audio said.
// A contradiction voids a pair however good the rest of it looked
// (ADR 0007).
func TestVerifyingStillRefusesACopyWhoseLengthDisagrees(t *testing.T) {
	provider := &stubProvider{recording: creditedTo(
		recording(firstID, "SICKO MODE", "Travi$ Scott", 312000),
		[]string{"Travi$ Scott", "Travis Scott"},
	)}
	verification, err := NewResolver(provider).Verify(context.Background(), Evidence{
		Artist: "JACKBOYS", Title: "SICKO MODE", DurationMS: 320000,
		Acoustic: heard(cluster(0.99, firstID)),
	}, uuid.MustParse(firstID))
	if err != nil {
		t.Fatalf("Verify() error = %v", err)
	}
	if verification.Proven() || !verification.Contradicted {
		t.Fatalf("verification = %+v, want the length to have refused it", verification)
	}
}

// The distributor's own excerpt outranks the credit too. It was fetched by this
// recording's ISRC, so a copy that reproduces it is this recording.
func TestVerifyingAdmitsACopyTheSampleNamedAndTheCreditDoesNot(t *testing.T) {
	provider := &stubProvider{recording: creditedTo(
		recording(firstID, "SICKO MODE", "Travi$ Scott", 312000),
		[]string{"Travi$ Scott", "Travis Scott"},
	)}
	verification, err := NewResolver(provider).Verify(context.Background(), Evidence{
		Artist: "JACKBOYS", Title: "SICKO MODE", DurationMS: 312000,
		Anchor: &AnchorComparison{
			RecordingID: uuid.MustParse(firstID), Rate: 0.01,
			Source: AnchorSourceDeezer,
		},
	}, uuid.MustParse(firstID))
	if err != nil {
		t.Fatalf("Verify() error = %v", err)
	}
	if !verification.Proven() || !verification.AnchorAgrees {
		t.Fatalf("verification = %+v, want the sample to have proven it", verification)
	}
}

// An excerpt found under this recording's name is not this recording's own
// excerpt, so it does not clear a credit that disagrees (ADR 0029).
func TestVerifyingKeepsTheCreditRefusalForAnExcerptFoundByName(t *testing.T) {
	provider := &stubProvider{recording: creditedTo(
		recording(firstID, "SICKO MODE", "Travi$ Scott", 312000),
		[]string{"Travi$ Scott", "Travis Scott"},
	)}
	verification, err := NewResolver(provider).Verify(context.Background(), Evidence{
		Artist: "JACKBOYS", Title: "SICKO MODE", DurationMS: 312000,
		Anchor: &AnchorComparison{
			RecordingID: uuid.MustParse(firstID), Rate: 0.01, Source: "youtube",
		},
	}, uuid.MustParse(firstID))
	if err != nil {
		t.Fatalf("Verify() error = %v", err)
	}
	if verification.Proven() || !verification.Contradicted {
		t.Fatalf("verification = %+v, want the credit to have refused it", verification)
	}
}

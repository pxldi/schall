package identity

import (
	"context"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/pxldi/schall/internal/musicbrainz"
)

// MusicBrainz holds a censored track as two rows: the explicit master and the
// clean edition, one title, one credit, one length, a registration code each.
// 0031's fourth test reads both codes and no code in common as two registered
// tracks, which they are, so every such pair was a person's question. The owner
// takes the explicit edition wherever there is one, so the clean row stops being
// an answer. See docs/decisions/0032.

// The two rows of "Timeless" by The Weeknd, as MusicBrainz holds them: 256
// seconds each, USUG12406537 on one and USUG12406536 on the other, and the
// editors' comment on the clean one saying which it is.
func timelessRows() (clean, explicit musicbrainz.Recording) {
	credit := []string{"The Weeknd"}
	clean = creditedTo(
		recording(firstID, "Timeless", "The Weeknd", 256000, "Hurry Up Tomorrow"), credit)
	clean.ISRCs = []string{"USUG12406537"}
	clean.Disambiguation = "clean"
	explicit = creditedTo(
		recording(secondID, "Timeless", "The Weeknd", 256000, "Hurry Up Tomorrow"), credit)
	explicit.ISRCs = []string{"USUG12406536"}
	return clean, explicit
}

// rowProvider answers each recording by its own identifier, so a test can ask
// the pair in either direction from one set of rows.
func rowProvider(rows ...musicbrainz.Recording) *stubProvider {
	return &stubProvider{answer: func(id uuid.UUID) (musicbrainz.Recording, error) {
		for _, row := range rows {
			if row.ID == id {
				return row, nil
			}
		}
		return musicbrainz.Recording{}, musicbrainz.ErrNotFound
	}}
}

func filingVerdict(
	t *testing.T, wanted, filed musicbrainz.Recording, copyDurationMS int,
) FilingVerdict {
	t.Helper()
	resolver := NewResolver(rowProvider(wanted, filed))
	verdict, err := resolver.OneRegistration(
		context.Background(), wanted.ID, filed.ID, FilingEvidence{
			CopyDurationMS: copyDurationMS, Method: "fingerprint",
		})
	if err != nil {
		t.Fatalf("OneRegistration() error = %v", err)
	}
	return verdict
}

// An entry as a playlist describes one: the words, the length, and no audio.
func entryFor(title string, durationMS int) Evidence {
	return Evidence{
		Subject: SubjectEntry, Artist: "The Weeknd", Title: title, DurationMS: durationMS,
	}
}

// What MusicBrainz says a row is, read off what MusicBrainz wrote. The marker
// has to be a whole word, and in a title it has to sit in a group of its own: a
// title is a name, and "Clean" by Taylor Swift is a song.
func TestWhatMarksARowAsTheCleanEdition(t *testing.T) {
	tests := []struct {
		name       string
		recording  musicbrainz.Recording
		wantsClean bool
	}{
		{"a disambiguation of clean", disambiguated("clean"), true},
		{"a title marked clean and nothing else", musicbrainz.Recording{Title: "Timeless (Clean)"}, true},
		{"a title marked edited and nothing else", musicbrainz.Recording{Title: "Timeless [Edited]"}, true},
		{"a title with the word inside another", musicbrainz.Recording{Title: "Cleanup Crew"}, false},
		{"a disambiguation of clean version", disambiguated("clean version"), true},
		{"a disambiguation of censored", disambiguated("censored edit"), true},
		{"a disambiguation of edited", disambiguated("edited"), true},
		{"a disambiguation of explicit", disambiguated("explicit"), false},
		{"a disambiguation of live", disambiguated("live, 1994"), false},
		{"a word the marker is only part of", disambiguated("cleaned up demo"), false},
		{"no disambiguation and no releases", disambiguated(""), false},
		{"a bracketed release title", onReleases("Timeless (Clean)"), true},
		{"a square-bracketed release title", onReleases("Timeless [Clean]"), true},
		{"a dashed release title", onReleases("Timeless - Clean"), true},
		{"a release titled clean version", onReleases("Timeless (Clean Version)"), true},
		{"a release titled edited", onReleases("Timeless (Edited)"), true},
		{"a release radio edit", onReleases("Timeless (Radio Edit)"), false},
		{"a release named for a group", onReleases("Timeless (Clean Bandit Remix)"), false},
		{"a release whose whole name is the word", onReleases("Clean"), false},
		{"every release marked", onReleases("Timeless (Clean)", "Hits - Clean"), true},
		{"one release of several marked", onReleases("Timeless (Clean)", "Hurry Up Tomorrow"), false},
		{"a release disambiguation", onCleanReleaseComment(), true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := cleanEdition(test.recording); got != test.wantsClean {
				t.Errorf("cleanEdition() = %v, want %v", got, test.wantsClean)
			}
		})
	}
}

func disambiguated(comment string) musicbrainz.Recording {
	row := recording(firstID, "Timeless", "The Weeknd", 256000)
	row.Disambiguation = comment
	return row
}

func onReleases(titles ...string) musicbrainz.Recording {
	return recording(firstID, "Timeless", "The Weeknd", 256000, titles...)
}

func onCleanReleaseComment() musicbrainz.Recording {
	row := recording(firstID, "Timeless", "The Weeknd", 256000, "Hurry Up Tomorrow")
	row.Releases[0].Disambiguation = "clean"
	return row
}

// The entry that used to be a question. Two rows fit its words equally well and
// MusicBrainz marks one of them clean, so the explicit one is the answer and the
// method says which rule took it.
func TestAnEntryTakesTheExplicitEditionOverTheCleanOne(t *testing.T) {
	clean, explicit := timelessRows()

	outcome := decide(entryFor("Timeless", 256000),
		[]musicbrainz.Recording{clean, explicit})

	if outcome.Status != StatusResolved {
		t.Fatalf("Status = %q, want resolved: %s", outcome.Status, outcome.Summary)
	}
	if outcome.Identity.RecordingID != explicit.ID {
		t.Errorf("resolved to %s, want the explicit row %s",
			outcome.Identity.RecordingID, explicit.ID)
	}
	if !strings.HasSuffix(outcome.Identity.Method, "-and-"+singledOutByExplicitEdition) {
		t.Errorf("method = %q, want it to name the rule", outcome.Identity.Method)
	}
	if sentence := MethodSentence(outcome.Identity.Method); !strings.Contains(sentence, "clean edition") {
		t.Errorf("method sentence = %q, want it to say what was set aside", sentence)
	}
}

// The same two rows with nothing saying which is which. Both fit, MusicBrainz
// marks neither, and nothing here picks between them.
func TestTwoRowsMarkedNeitherWayStayAQuestion(t *testing.T) {
	clean, explicit := timelessRows()
	clean.Disambiguation = ""

	outcome := decide(entryFor("Timeless", 256000),
		[]musicbrainz.Recording{clean, explicit})

	if outcome.Status != StatusNeedsReview {
		t.Errorf("Status = %q, want needs_review: %s", outcome.Status, outcome.Summary)
	}
}

// Both rows marked clean is MusicBrainz saying there is no explicit row here to
// prefer. The pair goes to a person exactly as it did before.
func TestTwoRowsBothMarkedCleanStayAQuestion(t *testing.T) {
	clean, explicit := timelessRows()
	explicit.Disambiguation = "clean"

	outcome := decide(entryFor("Timeless", 256000),
		[]musicbrainz.Recording{clean, explicit})

	if outcome.Status != StatusNeedsReview {
		t.Errorf("Status = %q, want needs_review: %s", outcome.Status, outcome.Summary)
	}
}

// Six seconds apart is not one track under two names. The rule reads 0031's
// first three tests and the length is one of them.
func TestRowsOutsideTheLengthToleranceAreNotAPair(t *testing.T) {
	clean, explicit := timelessRows()
	shorter := 250000
	clean.DurationMS = &shorter

	if explicitEditionOf(clean, explicit, 256000) {
		t.Error("two rows six seconds apart are read as one track's two editions")
	}
}

// A credit naming another artist is another recording, marked clean or not.
func TestARowCreditingAnotherArtistIsNotAPair(t *testing.T) {
	clean, explicit := timelessRows()
	explicit = creditedTo(explicit, []string{"Playboi Carti"})

	if explicitEditionOf(clean, explicit, 256000) {
		t.Error("a credit naming another artist is read as the explicit edition")
	}
}

// The want names the clean row and the library files its copy as the explicit
// one. The owner has the edition they wanted, so the want settles against the
// file as the library has it.
func TestAWantForTheCleanRowSettlesOnTheExplicitFile(t *testing.T) {
	clean, explicit := timelessRows()

	if verdict := filingVerdict(t, clean, explicit, 256000); verdict != FilingExplicitEdition {
		t.Errorf("verdict = %v, want the explicit edition to settle the want", verdict)
	}
}

// The other direction never settles. A want for the explicit edition offered the
// clean one goes on looking, and the file stays where the library filed it.
func TestAWantForTheExplicitRowDoesNotSettleOnTheCleanFile(t *testing.T) {
	clean, explicit := timelessRows()

	if verdict := filingVerdict(t, explicit, clean, 256000); verdict != FilingOtherRecording {
		t.Errorf("verdict = %v, want the want to keep looking", verdict)
	}
}

// The cluster path, same pair. The want names the clean row, AcoustID names the
// explicit one, and the identification is set aside instead of refusing the copy
// for good.
func TestAudioNamingTheExplicitRowIsSetAside(t *testing.T) {
	clean, explicit := timelessRows()
	copy := Evidence{
		Subject: SubjectFile, Artist: "The Weeknd", Title: "Timeless", DurationMS: 256000,
		Acoustic: heard(cluster(0.97, secondID)),
	}

	verification, err := NewResolver(rowProvider(clean, explicit)).Verify(
		context.Background(), copy, clean.ID)
	if err != nil {
		t.Fatalf("Verify() error = %v", err)
	}
	if verification.AudioSaysOtherwise {
		t.Error("the audio is reported as naming other music, want the explicit row set aside")
	}
	if !verification.AudioSetAside {
		t.Error("the explicit row is not reported as set aside")
	}
	if !strings.Contains(verification.Summary, "clean edition") {
		t.Errorf("summary = %q, want it to say what MusicBrainz marks this row as",
			verification.Summary)
	}
}

// The other direction on the cluster path. The want names the explicit row and
// the audio is the clean edition, which is other music and stays a refusal.
func TestAudioNamingTheCleanRowStillRefusesTheCopy(t *testing.T) {
	clean, explicit := timelessRows()
	copy := Evidence{
		Subject: SubjectFile, Artist: "The Weeknd", Title: "Timeless", DurationMS: 256000,
		Acoustic: heard(cluster(0.97, firstID)),
	}

	verification, err := NewResolver(rowProvider(clean, explicit)).Verify(
		context.Background(), copy, explicit.ID)
	if err != nil {
		t.Fatalf("Verify() error = %v", err)
	}
	if !verification.AudioSaysOtherwise {
		t.Error("the clean edition answered for a want for the explicit one")
	}
	if verification.AudioSetAside {
		t.Error("the clean row was set aside")
	}
}

// The flag Spotify sends only ever excludes. An entry it says is explicit is
// never resolved to a row MusicBrainz marks clean, even where that row is the
// only one MusicBrainz has.
func TestAnExplicitEntryIsNeverResolvedToACleanRow(t *testing.T) {
	clean, _ := timelessRows()
	entry := entryFor("Timeless", 256000)
	entry.Explicit = true

	outcome := decide(entry, []musicbrainz.Recording{clean})

	if outcome.Status == StatusResolved {
		t.Errorf("an entry flagged explicit resolved to the clean row: %s", outcome.Summary)
	}
	for _, candidate := range outcome.Candidates {
		if candidate.RecordingID == clean.ID {
			t.Error("the clean row is offered as a candidate")
		}
	}
}

// Without the flag the same entry is answered by the row MusicBrainz has, which
// is what the flag excluding and never admitting means.
func TestAnEntryWithoutTheFlagStillResolvesToTheOnlyRow(t *testing.T) {
	clean, _ := timelessRows()

	outcome := decide(entryFor("Timeless", 256000), []musicbrainz.Recording{clean})

	if outcome.Status != StatusResolved {
		t.Fatalf("Status = %q, want resolved: %s", outcome.Status, outcome.Summary)
	}
	if outcome.Identity.RecordingID != clean.ID {
		t.Errorf("resolved to %s, want %s", outcome.Identity.RecordingID, clean.ID)
	}
}

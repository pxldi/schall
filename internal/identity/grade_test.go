package identity

import (
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/pxldi/schall/internal/musicbrainz"
	"github.com/pxldi/schall/internal/tagmatch"
)

func recording(id, title, artist string, durationMS int, releases ...string) musicbrainz.Recording {
	result := musicbrainz.Recording{
		ID: uuid.MustParse(id), Title: title, ArtistCredit: artist,
		DurationMS: &durationMS,
	}
	for _, release := range releases {
		result.Releases = append(result.Releases, musicbrainz.RecordingRelease{
			ID: uuid.New(), ReleaseGroupID: uuid.New(), Title: release, Status: "Official",
		})
	}
	return result
}

// creditedTo gives a recording the artist list MusicBrainz sends beside the
// printed credit, so the credit is compared as the set of artists it is. Each
// entry is one artist: the name this release prints, then the artist's own name
// and any aliases.
func creditedTo(base musicbrainz.Recording, artists ...[]string) musicbrainz.Recording {
	for _, names := range artists {
		credit := musicbrainz.Credit{ArtistID: artistID(names[0]), Name: names[0]}
		if len(names) > 1 {
			credit.ArtistName, credit.Aliases = names[1], names[2:]
		}
		base.Credits = append(base.Credits, credit)
	}
	return base
}

// artistID is the identifier MusicBrainz holds one artist under. The fixture
// derives it from the name rather than inventing one per row, because that is
// the shape of the catalogue: two rows crediting one artist carry one
// identifier, and the characters a name holds that print nothing are not part of
// who the artist is. A pair that has to carry two identifiers, or one, sets them
// itself.
func artistID(name string) uuid.UUID {
	return uuid.NewSHA1(uuid.NameSpaceOID, []byte(tagmatch.Text(name)))
}

// creditedToArtists gives a recording an artist list with the identifiers spelled
// out, which is what a pair MusicBrainz renamed needs: one artist, one
// identifier, two printed names.
func creditedToArtists(
	base musicbrainz.Recording, credits ...musicbrainz.Credit,
) musicbrainz.Recording {
	base.Credits = append(base.Credits, credits...)
	return base
}

const (
	firstID  = "205ff413-6fbe-48d2-bd64-940cc37f5d75"
	secondID = "b9ad642e-b012-41c7-b72a-42cf4911f9ff"
)

// cluster is one of AcoustID's fingerprint clusters: how close it thought the
// audio was, and the recordings MusicBrainz links to it.
func cluster(similarity float64, ids ...string) AcousticCluster {
	named := make([]uuid.UUID, 0, len(ids))
	for _, id := range ids {
		named = append(named, uuid.MustParse(id))
	}
	return AcousticCluster{Recordings: named, Similarity: similarity}
}

// heard is a whole AcoustID answer, best cluster first, exactly as the service
// ranks them.
func heard(clusters ...AcousticCluster) *Acoustic {
	return &Acoustic{Clusters: clusters}
}

// An identifier that agrees and nothing that contradicts it is proof, and it is
// the only case where Schall links a file to an external identity by itself
// with no further corroboration.
func TestRecordingIDIdentifiesTheFile(t *testing.T) {
	file := Evidence{
		RecordingID: uuid.MustParse(firstID), Artist: "Radiohead",
		Album: "OK Computer", Title: "Karma Police", DurationMS: 308000,
	}
	outcome := decide(file, []musicbrainz.Recording{
		recording(firstID, "Karma Police", "Radiohead", 308973, "OK Computer"),
	})
	if outcome.Status != StatusResolved || outcome.Identity == nil {
		t.Fatalf("status = %q identity = %v, want a resolved identity", outcome.Status, outcome.Identity)
	}
	if outcome.Identity.Method != "recording-id" || outcome.Identity.Confidence != 1 {
		t.Errorf("identity = %+v", outcome.Identity)
	}
	if outcome.Identity.ReleaseTitle != "OK Computer" {
		t.Errorf("release = %q, want the edition the file names", outcome.Identity.ReleaseTitle)
	}
	if !strings.Contains(outcome.Summary, "Karma Police") {
		t.Errorf("summary = %q, want it to name the recording", outcome.Summary)
	}
}

// A file whose identifier agrees while its duration is a minute out is
// mistagged, not identified. Schall says so rather than choosing a side.
func TestContradictedIdentifierIsAConflict(t *testing.T) {
	file := Evidence{
		RecordingID: uuid.MustParse(firstID), Artist: "Radiohead",
		Title: "Karma Police", DurationMS: 240000,
	}
	outcome := decide(file, []musicbrainz.Recording{
		recording(firstID, "Karma Police", "Radiohead", 308973, "OK Computer"),
	})
	if outcome.Status != StatusConflict {
		t.Fatalf("status = %q, want %q", outcome.Status, StatusConflict)
	}
	if outcome.Identity != nil {
		t.Error("a contradicted identifier must not be stored as an identity")
	}
	if len(outcome.Candidates) != 1 || !strings.Contains(outcome.Summary, "duration") {
		t.Errorf("summary = %q candidates = %+v", outcome.Summary, outcome.Candidates)
	}
}

// Artist, title and duration together identify a recording across the whole
// provider database — but only while exactly one recording reaches that grade.
func TestArtistTitleAndDurationIdentifyOneRecording(t *testing.T) {
	file := Evidence{
		Artist: "Radiohead", Album: "OK Computer",
		Title: "Karma Police", DurationMS: 308500,
	}
	outcome := decide(file, []musicbrainz.Recording{
		recording(firstID, "Karma Police", "Radiohead", 308973, "OK Computer"),
		recording(secondID, "No Surprises", "Radiohead", 229000, "OK Computer"),
	})
	if outcome.Status != StatusResolved || outcome.Identity == nil {
		t.Fatalf("status = %q, want a resolved identity", outcome.Status)
	}
	if outcome.Identity.Method != "title-artist-duration" {
		t.Errorf("method = %q", outcome.Identity.Method)
	}
}

// Two recordings the evidence cannot choose between are a question, never a
// coin toss. A remaster with the same length as the original is the ordinary
// case here, not a rare one.
func TestEquallyStrongRecordingsAreAQuestion(t *testing.T) {
	file := Evidence{Artist: "Radiohead", Title: "Karma Police", DurationMS: 308500}
	outcome := decide(file, []musicbrainz.Recording{
		recording(firstID, "Karma Police", "Radiohead", 308973, "OK Computer"),
		recording(secondID, "Karma Police", "Radiohead", 308500, "OK Computer OKNOTOK"),
	})
	if outcome.Status != StatusNeedsReview || outcome.Identity != nil {
		t.Fatalf("status = %q identity = %v", outcome.Status, outcome.Identity)
	}
	if len(outcome.Candidates) != 2 {
		t.Fatalf("candidates = %d, want both of them", len(outcome.Candidates))
	}
	if outcome.Candidates[0].Rank != 1 || outcome.Candidates[1].Rank != 2 {
		t.Errorf("ranks = %d, %d", outcome.Candidates[0].Rank, outcome.Candidates[1].Rank)
	}
}

// A cover version shares the title and often the length. The artist is what
// stands between it and being filed as the original.
func TestCoverVersionIsNotTheOriginal(t *testing.T) {
	file := Evidence{Artist: "Some Tribute Band", Title: "Karma Police", DurationMS: 308500}
	outcome := decide(file, []musicbrainz.Recording{
		recording(firstID, "Karma Police", "Radiohead", 308973, "OK Computer"),
	})
	if outcome.Status != StatusNeedsReview {
		t.Fatalf("status = %q, want %q", outcome.Status, StatusNeedsReview)
	}
	if len(outcome.Candidates) != 1 || len(outcome.Candidates[0].Differs) == 0 {
		t.Fatalf("candidate = %+v, want the artist recorded as disagreeing", outcome.Candidates)
	}
}

// A release the file does not name is ordinary: the same recording is collected
// on many editions, so that alone must not stop an identity.
func TestADifferentReleaseDoesNotContradict(t *testing.T) {
	file := Evidence{
		Artist: "Radiohead", Album: "A Homemade Compilation",
		Title: "Karma Police", DurationMS: 308500,
	}
	outcome := decide(file, []musicbrainz.Recording{
		recording(firstID, "Karma Police", "Radiohead", 308973, "OK Computer"),
	})
	if outcome.Status != StatusResolved {
		t.Fatalf("status = %q, want the identity to stand", outcome.Status)
	}
}

func TestNothingFoundIsOwnedMusicWithoutAnIdentity(t *testing.T) {
	file := Evidence{Artist: "A Friend", Title: "Demo", DurationMS: 90000}
	outcome := decide(file, nil)
	if outcome.Status != StatusLocalOnly || outcome.Identity != nil {
		t.Fatalf("status = %q identity = %v", outcome.Status, outcome.Identity)
	}
	if !strings.Contains(outcome.Summary, "owned music") {
		t.Errorf("summary = %q, want it to say the file is still owned", outcome.Summary)
	}
}

func TestAFileWithNothingToAskAboutIsNotGuessedAt(t *testing.T) {
	outcome := decide(Evidence{}, nil)
	if outcome.Status != StatusLocalOnly {
		t.Fatalf("status = %q", outcome.Status)
	}
	if !strings.Contains(outcome.Summary, "no identifier and no title") {
		t.Errorf("summary = %q", outcome.Summary)
	}
}

// An ISRC names one recording, so its agreement identifies an entry on its own
// even when the streaming service decorated the title beyond recognition.
func TestISRCIdentifiesAnEntryThroughADecoratedTitle(t *testing.T) {
	found := recording(firstID, "Glory Box", "Portishead", 301000, "Dummy")
	found.ISRCs = []string{"GBAAA9400123"}

	outcome := decide(Evidence{
		Subject: SubjectEntry, ISRC: "GBAAA9400123", Artist: "Portishead",
		Title: "Glory Box - Remastered 2011", DurationMS: 301000,
	}, []musicbrainz.Recording{found})

	if outcome.Status != StatusResolved || outcome.Identity == nil {
		t.Fatalf("status = %q identity = %v", outcome.Status, outcome.Identity)
	}
	if outcome.Identity.Method != "isrc" {
		t.Errorf("method = %q, want isrc", outcome.Identity.Method)
	}
}

// An ISRC that names a different recording voids the pair whatever else agrees.
// Everything about this entry fits except the one identifier that is exact, and
// exactness is the reason it is trusted.
func TestAContradictedISRCRefusesEverythingElseThatAgrees(t *testing.T) {
	found := recording(firstID, "Glory Box", "Portishead", 301000, "Dummy")
	found.ISRCs = []string{"GBAAA9400999"}

	outcome := decide(Evidence{
		Subject: SubjectEntry, ISRC: "GBAAA9400123", Artist: "Portishead",
		Title: "Glory Box", DurationMS: 301000,
	}, []musicbrainz.Recording{found})

	if outcome.Status == StatusResolved {
		t.Fatalf("status = %q, want a contradicted ISRC to refuse the pair", outcome.Status)
	}
}

// An entry with no duration cannot text-conclude. Silence is not agreement, and
// artist and title alone put every cover, live take and remaster in the frame.
func TestAnEntryWithNoDurationIsNeverConcludedFromTextAlone(t *testing.T) {
	outcome := decide(Evidence{
		Subject: SubjectEntry, Artist: "Portishead", Title: "Glory Box",
	}, []musicbrainz.Recording{
		recording(firstID, "Glory Box", "Portishead", 301000, "Dummy"),
	})

	if outcome.Status != StatusNeedsReview {
		t.Fatalf("status = %q, want the missing duration to leave a question", outcome.Status)
	}
	if len(outcome.Candidates) != 1 {
		t.Fatalf("candidates = %d, want the plausible recording to be offered", len(outcome.Candidates))
	}
}

// A duration outside the five-second tolerance is a different recording, not a
// rounding error, and the tolerance is not stretched to reach an answer.
func TestADurationOutsideToleranceRefusesTheEntry(t *testing.T) {
	outcome := decide(Evidence{
		Subject: SubjectEntry, Artist: "Portishead", Title: "Glory Box",
		DurationMS: 301000,
	}, []musicbrainz.Recording{
		recording(firstID, "Glory Box", "Portishead", 306500, "Dummy"),
	})

	if outcome.Status == StatusResolved {
		t.Fatalf("status = %q, want a duration 5.5 seconds out to refuse the pair", outcome.Status)
	}
}

// A guest on the credit is still the artist's own track, so an entry crediting
// them together identifies the recording the provider credits alone.
func TestAGuestCreditIsTheSameArtist(t *testing.T) {
	outcome := decide(Evidence{
		Subject: SubjectEntry, Artist: "Gorillaz feat. De La Soul",
		Title: "Feel Good Inc", DurationMS: 341000,
	}, []musicbrainz.Recording{
		recording(firstID, "Feel Good Inc.", "Gorillaz", 341000, "Demon Days"),
	})

	if outcome.Status != StatusResolved {
		t.Fatalf("status = %q, want the guest credit to identify the recording", outcome.Status)
	}
}

// A compilation credit says nothing about any one track, so it is silence rather
// than a disagreement — but silence does not identify anything either.
func TestAVariousArtistsCreditNeitherAgreesNorContradicts(t *testing.T) {
	outcome := decide(Evidence{
		Subject: SubjectEntry, Artist: "Various Artists", Title: "Glory Box",
		DurationMS: 301000,
	}, []musicbrainz.Recording{
		recording(firstID, "Glory Box", "Portishead", 301000, "Dummy"),
	})

	if outcome.Status != StatusNeedsReview {
		t.Fatalf("status = %q, want an unattributable credit to leave a question", outcome.Status)
	}
	for _, candidate := range outcome.Candidates {
		for _, differs := range candidate.Differs {
			if differs == "artist" {
				t.Fatal("a compilation credit was read as a disagreement about the artist")
			}
		}
	}
}

// The streaming-metadata corpus. Every one of these titles is what a real export
// carries and none of them is the title of a MusicBrainz recording. Without an
// ISRC the answer is always the same: the recording is offered as a question and
// never concluded, because the decoration might be the whole difference between
// two recordings — a remaster, a radio edit, a live take. The one label that is
// not such a difference has its own test below.
func TestDecoratedTitlesAreAskedAboutAndNeverConcluded(t *testing.T) {
	decorations := []string{
		"Glory Box - Remastered 2011",
		"Glory Box - 2011 Remaster",
		"Glory Box (feat. Beth Gibbons)",
		"Glory Box - Radio Edit",
		"Glory Box - Live",
		"Glory Box – Remastered",
		"Glory Box, Pt. 1",
	}
	for _, title := range decorations {
		t.Run(title, func(t *testing.T) {
			outcome := decide(Evidence{
				Subject: SubjectEntry, Artist: "Portishead", Title: title,
				DurationMS: 301000,
			}, []musicbrainz.Recording{
				recording(firstID, "Glory Box", "Portishead", 301000, "Dummy"),
			})

			if outcome.Status == StatusResolved {
				t.Fatalf("status = %q, want a decorated title never to conclude", outcome.Status)
			}
			if len(outcome.Candidates) != 1 {
				t.Fatalf("candidates = %d, want the recording to be offered as a question",
					len(outcome.Candidates))
			}
			candidate := outcome.Candidates[0]
			if candidate.RecordingID != uuid.MustParse(firstID) {
				t.Fatalf("candidate = %s", candidate.RecordingID)
			}
			differs := strings.Join(candidate.Differs, ",")
			if !strings.Contains(differs, "title") {
				t.Fatalf("differs = %v, want the title disagreement to be shown", candidate.Differs)
			}
		})
	}
}

// The one decoration above that is not a decoration. "Original Mix" is how a
// release carrying remixes labels the track they were made from, so it says the
// recording is not a remix rather than saying which recording it is — there is
// no second Hadone recording of "How To Fake Success" for it to be picked out
// from. MusicBrainz recording 3ddb41a9-40bd-4f9a-bbed-68a4646d1a44 holds no
// ISRC, so before this the title was the only evidence that could have agreed,
// and the entry sat unresolved against the recording it plainly is.
func TestAnOriginalMixSuffixDoesNotStandBetweenAnEntryAndItsRecording(t *testing.T) {
	outcome := decide(Evidence{
		Subject: SubjectEntry, Artist: "Hadone", Title: "How To Fake Success - Original mix",
		DurationMS: 387000,
	}, []musicbrainz.Recording{
		recording(firstID, "How to Fake Success", "Hadone", 387083, "Defining Persuasion"),
	})

	if outcome.Status != StatusResolved || outcome.Identity == nil {
		t.Fatalf("status = %q identity = %v, want a resolved identity", outcome.Status, outcome.Identity)
	}
	if outcome.Identity.Method != "title-artist-duration" {
		t.Errorf("method = %q, want the tag agreement it rests on", outcome.Identity.Method)
	}
	if outcome.Identity.RecordingID != uuid.MustParse(firstID) {
		t.Errorf("recording = %s", outcome.Identity.RecordingID)
	}
}

// The label is read off the title and nothing else. A remix of the same track
// at the same length under the same artist is a different recording, and the
// entry naming one must not be concluded to be the other.
func TestARemixIsStillNotTheOriginalMixItWasMadeFrom(t *testing.T) {
	outcome := decide(Evidence{
		Subject: SubjectEntry, Artist: "Hadone", Title: "How To Fake Success - Regal Remix",
		DurationMS: 387000,
	}, []musicbrainz.Recording{
		recording(firstID, "How to Fake Success", "Hadone", 387083, "Defining Persuasion"),
	})

	if outcome.Status == StatusResolved {
		t.Fatalf("status = %q, want a remix never to conclude as the original", outcome.Status)
	}
}

// The same widening must not reach past the title. An entry matching only on
// duration, under an artist the provider disagrees about, is a coincidence and is
// not worth showing anybody.
func TestADifferentArtistAtTheSameLengthIsNotEvenACandidate(t *testing.T) {
	outcome := decide(Evidence{
		Subject: SubjectEntry, Artist: "Portishead", Title: "Glory Box - Remastered",
		DurationMS: 301000,
	}, []musicbrainz.Recording{
		recording(firstID, "Teardrop", "Massive Attack", 301000, "Mezzanine"),
	})

	if len(outcome.Candidates) != 0 {
		t.Fatalf("candidates = %d, want a coincidence of length to be offered to nobody",
			len(outcome.Candidates))
	}
}

// An entry nothing matches says so in words about an entry. Telling somebody
// they own music they have only asked for would be a lie.
func TestAnEntryNothingMatchesIsNotDescribedAsOwnedMusic(t *testing.T) {
	outcome := decide(Evidence{
		Subject: SubjectEntry, Artist: "Portishead", Title: "Glory Box",
	}, nil)

	if outcome.Status != StatusLocalOnly {
		t.Fatalf("status = %q", outcome.Status)
	}
	if strings.Contains(outcome.Summary, "owned music") {
		t.Fatalf("summary = %q, want nothing about owning it", outcome.Summary)
	}
	if !strings.Contains(outcome.Summary, "entry") {
		t.Fatalf("summary = %q, want it to name what could not be resolved", outcome.Summary)
	}
}

// The audio names exactly one recording and nothing contradicts it. This is the
// only way a file a stranger sent is admitted with nobody looking at it, and it
// is what item 4's loop rests on.
func TestUnambiguousAudioIdentifiesTheFile(t *testing.T) {
	outcome := decide(Evidence{
		Title: "Karma Police", DurationMS: 308500,
		Acoustic: heard(cluster(1, firstID)),
	}, []musicbrainz.Recording{
		recording(firstID, "Karma Police", "Radiohead", 308973, "OK Computer"),
	})

	if outcome.Status != StatusResolved || outcome.Identity == nil {
		t.Fatalf("status = %q identity = %v, want a resolved identity", outcome.Status, outcome.Identity)
	}
	if outcome.Identity.Method != "fingerprint" || outcome.Identity.Confidence != 1 {
		t.Errorf("identity = %+v, want the audio recorded as what proved it", outcome.Identity)
	}
}

// One cluster naming two recordings is AcoustID saying they are one piece of
// audio, and MusicBrainz keeping two rows for it. What the file is has been
// answered; which row to key the answer on has not, and that is a question about
// identifiers rather than about music, so both rows are offered.
func TestOneClusterNamingTwoRecordingsLeavesTheRowInQuestion(t *testing.T) {
	outcome := decide(Evidence{
		Title:    "Karma Police",
		Acoustic: heard(cluster(1, secondID, firstID)),
	}, []musicbrainz.Recording{
		recording(firstID, "Karma Police", "Radiohead", 308973, "OK Computer"),
		recording(secondID, "Karma Police", "Radiohead", 309100, "OK Computer OKNOTOK"),
	})

	if outcome.Status != StatusNeedsReview || outcome.Identity != nil {
		t.Fatalf("status = %q identity = %v, want a question rather than an answer",
			outcome.Status, outcome.Identity)
	}
	if len(outcome.Candidates) != 2 {
		t.Fatalf("candidates = %d, want both rows the cluster named", len(outcome.Candidates))
	}
}

// A file with no tags at all, whose audio is entered twice in MusicBrainz, has
// nothing on it to choose between the two rows with. It stays a question.
func TestAnUntaggedFileIsNotResolvedWhenTwoRowsFitItsAudio(t *testing.T) {
	outcome := decide(Evidence{
		Acoustic: heard(cluster(1, firstID, secondID)),
	}, []musicbrainz.Recording{
		recording(firstID, "Rites", "An Obscure Act", 0),
		recording(secondID, "Something Else", "Another Act", 0),
	})

	if outcome.Identity != nil {
		t.Fatalf("identity = %+v, want nothing admitted while two rows fit equally",
			outcome.Identity)
	}
}

// AcoustID knows this audio and MusicBrainz has no row for it, so the leading
// cluster names nothing. That is an answer about the databases rather than about
// the file, and reading down to a lower cluster for something to say would be
// answering with other audio.
func TestALeadingClusterWithNoRowBehindItIsSilence(t *testing.T) {
	file := Evidence{
		Artist: "Radiohead", Album: "OK Computer",
		Title: "Karma Police", DurationMS: 308500,
	}
	recordings := []musicbrainz.Recording{
		recording(firstID, "Karma Police", "Radiohead", 308973, "OK Computer"),
	}

	unlinked := file
	unlinked.Acoustic = heard(cluster(0.99), cluster(0.4, firstID))

	silent := decide(unlinked, recordings)
	unheard := decide(file, recordings)

	if silent.Status != unheard.Status || silent.Identity == nil {
		t.Fatalf("status = %q, want the same %q an unfingerprinted file reaches",
			silent.Status, unheard.Status)
	}
	if silent.Identity.Method != "title-artist-duration" {
		t.Errorf("method = %q, want the tag rules to have decided it", silent.Identity.Method)
	}
}

// The best cluster is read and nothing under it is. A recording a lower cluster
// named is other audio that resembled this file, so it is not the answer even
// when it is the only other thing in the running.
func TestOnlyTheBestClusterIdentifiesTheFile(t *testing.T) {
	outcome := decide(Evidence{
		Title:    "Karma Police",
		Acoustic: heard(cluster(0.99, secondID), cluster(0.42, firstID)),
	}, []musicbrainz.Recording{
		recording(firstID, "Karma Police", "Radiohead", 308973, "OK Computer"),
		recording(secondID, "Karma Police", "Radiohead", 309100, "OK Computer OKNOTOK"),
	})

	if outcome.Status != StatusResolved || outcome.Identity == nil {
		t.Fatalf("status = %q identity = %v, want the leading cluster to have decided it",
			outcome.Status, outcome.Identity)
	}
	if outcome.Identity.RecordingID != uuid.MustParse(secondID) {
		t.Errorf("recording = %s, want the one the leading cluster named",
			outcome.Identity.RecordingID)
	}
}

// The 卒業ですね case, and the one any new rule had to keep refusing: the wanted
// recording turns up in a lower cluster while the best one is a different remix
// that happens to share a compilation. A lower cluster is other audio, so the
// file is refused rather than admitted on being somewhere in the answer.
func TestARecordingNamedOnlyByALowerClusterIsRefused(t *testing.T) {
	outcome := decide(Evidence{
		RecordingID: uuid.MustParse(firstID), Artist: "Sewerslvt",
		Title: "卒業ですね (Sewerslvt remix)", DurationMS: 267000,
		Acoustic: heard(cluster(0.99, secondID), cluster(0.38, firstID)),
	}, []musicbrainz.Recording{
		recording(firstID, "卒業ですね (Sewerslvt remix)", "Sewerslvt", 267000),
	})

	if outcome.Identity != nil {
		t.Fatalf("identity = %+v, want the leading cluster's refusal to stand",
			outcome.Identity)
	}
	if outcome.Status != StatusConflict {
		t.Fatalf("status = %q, want %q", outcome.Status, StatusConflict)
	}
}

// The file's own identifier naming the same recording is a second, independent
// witness rather than the same one twice, and the stored decision says so: a
// reader months later can tell an identity two things agreed on from one the
// audio carried alone.
func TestTheFilesOwnIdentifierIsRecordedAsASecondWitness(t *testing.T) {
	first := recording(firstID, "Glory Box", "Portishead", 305000, "Dummy")
	first.ISRCs = []string{"GBAAA9400123"}

	outcome := decide(Evidence{
		ISRC:     "GBAAA9400123",
		Acoustic: heard(cluster(1, firstID)),
	}, []musicbrainz.Recording{first})

	if outcome.Status != StatusResolved || outcome.Identity == nil {
		t.Fatalf("status = %q, want the two witnesses to conclude", outcome.Status)
	}
	if outcome.Identity.Method != "fingerprint-and-identifier" {
		t.Errorf("method = %q, want both witnesses recorded", outcome.Identity.Method)
	}
}

// Audio that was recognised and does not name this recording is the file saying
// what it is, and it voids everything the tags claim. This is the remaster a
// peer labelled as the original.
func TestAudioNamingAnotherRecordingVoidsAgreeingTags(t *testing.T) {
	outcome := decide(Evidence{
		RecordingID: uuid.MustParse(firstID), Artist: "Radiohead",
		Title: "Karma Police", DurationMS: 308500,
		Acoustic: heard(cluster(1, secondID)),
	}, []musicbrainz.Recording{
		recording(firstID, "Karma Police", "Radiohead", 308973, "OK Computer"),
	})

	if outcome.Identity != nil {
		t.Fatalf("identity = %+v, want an agreeing recording ID refused by the audio",
			outcome.Identity)
	}
	if outcome.Status != StatusConflict {
		t.Fatalf("status = %q, want %q", outcome.Status, StatusConflict)
	}
}

// A duration a minute out contradicts whatever else agrees, the audio included.
// A truncated or padded copy is a question for a person, not an identification.
func TestAudioCannotIdentifyAgainstAContradictedDuration(t *testing.T) {
	outcome := decide(Evidence{
		Title: "Karma Police", DurationMS: 240000,
		Acoustic: heard(cluster(1, firstID)),
	}, []musicbrainz.Recording{
		recording(firstID, "Karma Police", "Radiohead", 308973, "OK Computer"),
	})

	if outcome.Identity != nil {
		t.Fatalf("identity = %+v, want the length contradiction to refuse it", outcome.Identity)
	}
}

// A fingerprint the database has never heard of is an answer about the database.
// It refuses nothing and admits nothing, so the tag rules decide exactly as they
// did before anybody listened.
func TestAudioTheDatabaseDoesNotKnowChangesNothing(t *testing.T) {
	file := Evidence{
		Artist: "Radiohead", Album: "OK Computer",
		Title: "Karma Police", DurationMS: 308500,
	}
	recordings := []musicbrainz.Recording{
		recording(firstID, "Karma Police", "Radiohead", 308973, "OK Computer"),
	}

	fingerprinted := file
	fingerprinted.Acoustic = &Acoustic{}

	silent := decide(fingerprinted, recordings)
	unheard := decide(file, recordings)

	if silent.Status != unheard.Status || silent.Identity == nil {
		t.Fatalf("status = %q, want the same %q an unfingerprinted file reaches",
			silent.Status, unheard.Status)
	}
	if silent.Identity.Method != "title-artist-duration" {
		t.Errorf("method = %q, want the tag rules to have decided it", silent.Identity.Method)
	}
}

// AcoustID decides for itself what reaches an answer at all, so one weak cluster
// arrives looking exactly like a certain one. Concluding on it would borrow
// AcoustID's own cutoff. Below the floor the audio identifies nothing, and a file
// a stranger sent has nothing left that may admit it.
func TestAudioTooUnlikeTheRecordingIdentifiesNothing(t *testing.T) {
	outcome := decide(Evidence{
		Title: "Karma Police", DurationMS: 308500,
		Acoustic: heard(cluster(0.51, firstID)),
	}, []musicbrainz.Recording{
		recording(firstID, "Karma Police", "Radiohead", 308973, "OK Computer"),
	})

	if outcome.Identity != nil && outcome.Identity.Method == "fingerprint" {
		t.Fatalf("identity = %+v, want a likeness nobody measured to admit nothing",
			outcome.Identity)
	}
}

// The second witness does not rescue audio that identified nothing. A weak
// cluster plus the file's own ISRC is one witness and a tag, and a tag never
// admits a copy a stranger sent (ADR 0002).
func TestAWeakLikenessIsNotRescuedByTheFilesOwnIdentifier(t *testing.T) {
	first := recording(firstID, "Glory Box", "Portishead", 305000, "Dummy")
	first.ISRCs = []string{"GBAAA9400123"}
	second := recording(secondID, "Glory Box", "Portishead", 306000, "Dummy: Remastered")

	outcome := decide(Evidence{
		ISRC:     "GBAAA9400123",
		Acoustic: heard(cluster(0.51, secondID, firstID)),
	}, []musicbrainz.Recording{first, second})

	if outcome.Identity != nil && outcome.Identity.Method == "fingerprint-and-identifier" {
		t.Fatalf("identity = %+v, want the audio not to count as a witness", outcome.Identity)
	}
}

// The floor gates admission and never a refusal. Audio that names some other
// recording is a statement about what the music is, and believing it less
// because the cluster scored low is the one direction that could let a wrong
// file in.
func TestAWeakLikenessStillRefusesAFileIdentifiedAsSomethingElse(t *testing.T) {
	outcome := decide(Evidence{
		RecordingID: uuid.MustParse(firstID), Artist: "Radiohead",
		Title: "Karma Police", DurationMS: 308500,
		Acoustic: heard(cluster(0.51, secondID)),
	}, []musicbrainz.Recording{
		recording(firstID, "Karma Police", "Radiohead", 308973, "OK Computer"),
	})

	if outcome.Status != StatusConflict || outcome.Identity != nil {
		t.Fatalf("status = %q identity = %v, want the audio to refuse it at any likeness",
			outcome.Status, outcome.Identity)
	}
}

// A copy nobody could admit has to say why in the words the review queue shows,
// or it reads as though the audio had agreed with it.
func TestAWeakLikenessSaysSoInTheEvidence(t *testing.T) {
	outcome := decide(Evidence{
		Title: "Karma Police", DurationMS: 308500,
		Acoustic: heard(cluster(0.51, firstID)),
	}, []musicbrainz.Recording{
		recording(firstID, "Karma Police", "Radiohead", 308973, "OK Computer"),
	})

	if len(outcome.Candidates) == 0 {
		t.Fatal("want the recording offered as a candidate to decide about")
	}
	said := strings.Join(outcome.Candidates[0].Agrees, "; ")
	if !strings.Contains(said, "51% likeness") {
		t.Errorf("agrees = %q, want the likeness stated rather than a bare agreement", said)
	}
}

// A differing artist credit does not void a pair the leading cluster identified.
// AcoustID named the audio, and a credit typed into a file by whoever encoded it
// does not overrule that (docs/decisions/0030). This test asserted the opposite
// until then.
func TestADifferingCreditKeepsAnAudioIdentification(t *testing.T) {
	outcome := decide(Evidence{
		Artist: "Cover Band", Title: "Song", DurationMS: 180000,
		Acoustic: heard(cluster(0.95, firstID)),
	}, []musicbrainz.Recording{
		recording(firstID, "Song", "Original Artist", 180000),
	})

	if outcome.Status != StatusResolved || outcome.Identity == nil {
		t.Fatalf("status = %q identity = %v, want the audio to have identified it",
			outcome.Status, outcome.Identity)
	}
	if outcome.Identity.Method != "fingerprint" {
		t.Errorf("method = %q, want the audio named as what identified it",
			outcome.Identity.Method)
	}
}

// The same holds for a leading cluster with two names in it, which is
// MusicBrainz holding one performance as two rows.
func TestADifferingCreditKeepsAudioIdentifiedRows(t *testing.T) {
	outcome := decide(Evidence{
		Artist: "Amelie Lens", Title: "Courtship Unleashed", DurationMS: 176880,
		Acoustic: heard(cluster(0.99, firstID)),
	}, []musicbrainz.Recording{
		recording(firstID, "Courtship Unleashed - Mixed", "Hadone", 176880),
	})

	if outcome.Status != StatusResolved || outcome.Identity == nil {
		t.Fatalf("status = %q identity = %v, want the audio to have identified it",
			outcome.Status, outcome.Identity)
	}
}

// Nothing listened, so there is nothing that outranks the credit.
func TestADifferingCreditStillRefusesWhenNobodyListened(t *testing.T) {
	outcome := decide(Evidence{
		Artist: "Amelie Lens", Title: "Courtship Unleashed - Mixed", DurationMS: 176880,
	}, []musicbrainz.Recording{
		recording(firstID, "Courtship Unleashed - Mixed", "Hadone", 176880),
	})

	if outcome.Identity != nil {
		t.Fatalf("identity = %+v, want a credit nothing outranks to refuse it", outcome.Identity)
	}
}

// A likeness below the floor is not an identification, so it cannot be what lets
// a credit through either.
func TestADifferingCreditStillRefusesUnderAWeakLikeness(t *testing.T) {
	outcome := decide(Evidence{
		Artist: "Amelie Lens", Title: "Courtship Unleashed", DurationMS: 176880,
		Acoustic: heard(cluster(0.51, firstID)),
	}, []musicbrainz.Recording{
		recording(firstID, "Courtship Unleashed - Mixed", "Hadone", 176880),
	})

	if outcome.Identity != nil {
		t.Fatalf("identity = %+v, want the floor to hold for the credit too", outcome.Identity)
	}
}

// A credit is still the strongest thing there is when the audio identified
// something else. A lower cluster naming this recording is other audio, so it
// neither rescues the credit nor answers anything.
func TestADifferingCreditStillRefusesWhenOnlyALowerClusterNamesIt(t *testing.T) {
	outcome := decide(Evidence{
		Artist: "Amelie Lens", Title: "Courtship Unleashed", DurationMS: 176880,
		Acoustic: heard(cluster(0.99, secondID), cluster(0.4, firstID)),
	}, []musicbrainz.Recording{
		recording(firstID, "Courtship Unleashed - Mixed", "Hadone", 176880),
	})

	if outcome.Identity != nil {
		t.Fatalf("identity = %+v, want a lower cluster to rescue nothing", outcome.Identity)
	}
}

// Only the credit was moved. An identifier naming other music is the file
// claiming to be a different recording, and a sole identification does not
// excuse it.
func TestAnIdentifierStillRefusesUnderASoleIdentification(t *testing.T) {
	first := recording(firstID, "Courtship Unleashed - Mixed", "Hadone", 176880)
	first.ISRCs = []string{"GBEXH1900606"}

	outcome := decide(Evidence{
		ISRC: "USAAA0000001", Title: "Courtship Unleashed", DurationMS: 176880,
		Acoustic: heard(cluster(0.99, firstID)),
	}, []musicbrainz.Recording{first})

	if outcome.Identity != nil {
		t.Fatalf("identity = %+v, want a contradicting ISRC to refuse it", outcome.Identity)
	}
}

// A length this recording cannot have is likewise untouched.
func TestALengthThatCannotBeStillRefusesUnderASoleIdentification(t *testing.T) {
	outcome := decide(Evidence{
		Artist: "Hadone", Title: "Courtship Unleashed", DurationMS: 120000,
		Acoustic: heard(cluster(0.99, firstID)),
	}, []musicbrainz.Recording{
		recording(firstID, "Courtship Unleashed - Mixed", "Hadone", 176880),
	})

	if outcome.Identity != nil {
		t.Fatalf("identity = %+v, want the length to refuse it", outcome.Identity)
	}
}

// A recording that was merged answers to the ID it was merged away from, and
// MusicBrainz is what says so. A file tagged before the merge is naming this
// recording.
func TestAMergedAwayRecordingIDIdentifiesTheFile(t *testing.T) {
	merged := recording(firstID, "Karma Police", "Radiohead", 308973, "OK Computer")
	merged.KnownAs = []uuid.UUID{uuid.MustParse(secondID)}

	outcome := decide(Evidence{
		RecordingID: uuid.MustParse(secondID), Artist: "Radiohead",
		Title: "Karma Police", DurationMS: 308500,
	}, []musicbrainz.Recording{merged})

	if outcome.Status != StatusResolved || outcome.Identity == nil {
		t.Fatalf("status = %q identity = %v, want the merged-away ID to identify it",
			outcome.Status, outcome.Identity)
	}
	if outcome.Identity.RecordingID != uuid.MustParse(firstID) {
		t.Errorf("recording = %s, want the canonical ID stored", outcome.Identity.RecordingID)
	}
}

// An ID the provider has not tied to this recording still disagrees. Nothing
// about merges loosens what an unrelated identifier means.
func TestAnUnrelatedRecordingIDStillRefuses(t *testing.T) {
	outcome := decide(Evidence{
		RecordingID: uuid.MustParse(secondID), Artist: "Radiohead",
		Title: "Karma Police", DurationMS: 308500,
	}, []musicbrainz.Recording{
		recording(firstID, "Karma Police", "Radiohead", 308973, "OK Computer"),
	})

	if outcome.Identity != nil {
		t.Fatalf("identity = %+v, want the identifier to refuse it", outcome.Identity)
	}
}

const thirdID = "6f4ba3fc-2d84-4e1a-9d8f-8b3d5c4a01c7"

// tagged is a recording carrying an ISRC, which is how MusicBrainz hands the
// same identifier to the single and to the edit collected beside it.
func tagged(id, title, artist string, durationMS int, isrc, release string) musicbrainz.Recording {
	result := recording(id, title, artist, durationMS, release)
	if isrc != "" {
		result.ISRCs = []string{isrc}
	}
	return result
}

// The case that put five wants in the queue: MusicBrainz carries one piece of
// music as several recordings and gives them the same ISRC, so the identifier
// that exists to be unique proves two of them at once. The release the entry
// names picks out one, by agreeing exactly where the others disagree.
func TestTheReleaseNamesOneOfTheRecordingsThatFit(t *testing.T) {
	entry := Evidence{
		Subject: SubjectEntry, Artist: "No_4mat", Album: "1992", Title: "1992",
		DurationMS: 404998, ISRC: "USLZJ1886830",
	}
	outcome := decide(entry, []musicbrainz.Recording{
		tagged(firstID, "1992", "No_4mat", 404000, "USLZJ1886830", "1992"),
		tagged(secondID, "1992", "No_4mat", 405000, "USLZJ1886830", "1992 (sped Up + slowed mixes)"),
		tagged(thirdID, "1992", "No_4mat", 405000, "", "MEC059 Infinity 2022"),
	})
	if outcome.Status != StatusResolved || outcome.Identity == nil {
		t.Fatalf("status = %q identity = %v, want the release to have named one",
			outcome.Status, outcome.Identity)
	}
	if outcome.Identity.RecordingID.String() != firstID {
		t.Fatalf("recording = %s, want the one on the release the entry names (%s)",
			outcome.Identity.RecordingID, firstID)
	}
	if !strings.Contains(outcome.Summary, "release this entry names") {
		t.Errorf("summary = %q, want it to say what chose between them", outcome.Summary)
	}
	// The stored decision has to be readable as the reason it was made: the ISRC
	// proved it was one of several, and the release picked which.
	if outcome.Identity.Method != "isrc-and-release" {
		t.Errorf("method = %q, want it to record that the release chose between equals",
			outcome.Identity.Method)
	}
}

// Two of them on that release, and it names neither. The rule is a way of being
// singled out, never a tie-break to be won.
func TestTheReleaseNamesNobodyWhenSeveralAreOnIt(t *testing.T) {
	entry := Evidence{
		Subject: SubjectEntry, Artist: "No_4mat", Album: "1992", Title: "1992",
		DurationMS: 404998, ISRC: "USLZJ1886830",
	}
	outcome := decide(entry, []musicbrainz.Recording{
		tagged(firstID, "1992", "No_4mat", 404000, "USLZJ1886830", "1992"),
		tagged(secondID, "1992", "No_4mat", 405000, "USLZJ1886830", "1992"),
	})
	if outcome.Status != StatusNeedsReview || outcome.Identity != nil {
		t.Fatalf("status = %q identity = %v, want the question kept",
			outcome.Status, outcome.Identity)
	}
}

// None of them on it. Nothing about a release that matches nobody says which
// recording was meant.
func TestAReleaseThatMatchesNoneOfThemNamesNothing(t *testing.T) {
	entry := Evidence{
		Subject: SubjectEntry, Artist: "No_4mat", Album: "Some Other Record",
		Title: "1992", DurationMS: 404998,
	}
	outcome := decide(entry, []musicbrainz.Recording{
		tagged(firstID, "1992", "No_4mat", 404000, "", "1992"),
		tagged(secondID, "1992", "No_4mat", 405000, "", "MEC059 Infinity 2022"),
	})
	if outcome.Status != StatusNeedsReview || outcome.Identity != nil {
		t.Fatalf("status = %q identity = %v, want the question kept",
			outcome.Status, outcome.Identity)
	}
}

// An entry with no album at all is silence, never agreement, so there is nothing
// for the release to name.
func TestAnEntryWithNoAlbumIsNotResolvedByItsRelease(t *testing.T) {
	entry := Evidence{
		Subject: SubjectEntry, Artist: "No_4mat", Title: "1992", DurationMS: 404998,
	}
	outcome := decide(entry, []musicbrainz.Recording{
		tagged(firstID, "1992", "No_4mat", 404000, "", "1992"),
		tagged(secondID, "1992", "No_4mat", 405000, "", "MEC059 Infinity 2022"),
	})
	if outcome.Status != StatusNeedsReview || outcome.Identity != nil {
		t.Fatalf("status = %q identity = %v, want the question kept",
			outcome.Status, outcome.Identity)
	}
}

// The release chooses between equals and never between a proof and a lesser one.
// Here the entry's ISRC names one recording and the entry's album names another,
// which is evidence pulling two ways: the weaker candidate does not win for
// sitting on the right record.
func TestTheReleaseDoesNotOverturnAStrongerProof(t *testing.T) {
	entry := Evidence{
		Subject: SubjectEntry, Artist: "No_4mat", Album: "1992", Title: "1992",
		DurationMS: 404998, ISRC: "USLZJ1886830",
	}
	outcome := decide(entry, []musicbrainz.Recording{
		tagged(firstID, "1992", "No_4mat", 404000, "USLZJ1886830", "MEC059 Infinity 2022"),
		tagged(secondID, "1992", "No_4mat", 405000, "", "1992"),
	})
	if outcome.Status != StatusNeedsReview || outcome.Identity != nil {
		t.Fatalf("status = %q identity = %v, want the weaker candidate refused",
			outcome.Status, outcome.Identity)
	}
}

// A candidate with everything agreeing and nothing against it has to say what is
// actually in the way, which is the recording beside it carrying the same ISRC.
func TestACandidateSaysWhatStopsItRatherThanThatItIsNotEnough(t *testing.T) {
	entry := Evidence{
		Subject: SubjectEntry, Artist: "No_4mat", Album: "1992", Title: "1992",
		DurationMS: 404998, ISRC: "USLZJ1886830",
	}
	outcome := decide(entry, []musicbrainz.Recording{
		tagged(firstID, "1992", "No_4mat", 404000, "USLZJ1886830", "1992"),
		tagged(secondID, "1992", "No_4mat", 405000, "USLZJ1886830", "1992"),
	})
	if len(outcome.Candidates) < 1 {
		t.Fatal("no candidates to read")
	}
	if !strings.Contains(outcome.Candidates[0].Summary, "same ISRC on another recording") {
		t.Errorf("summary = %q, want it to name the ISRC both recordings carry",
			outcome.Candidates[0].Summary)
	}
}

// The weakest proof there is, singled out the same way: no ISRC anywhere in
// MusicBrainz, two recordings named and timed alike, and one of them on the
// record the entry names. Three of the five wants that were waiting are this.
func TestTheReleaseNamesOneRecordingProvenOnlyByItsTags(t *testing.T) {
	entry := Evidence{
		Subject: SubjectEntry, Artist: "Randomer", Album: "Sleep of Reason",
		Title: "Sleep Of Reason", DurationMS: 371111,
	}
	outcome := decide(entry, []musicbrainz.Recording{
		tagged(firstID, "Sleep of Reason", "Randomer", 371111, "", "Sleep of Reason"),
		tagged(secondID, "Sleep of Reason", "Randomer", 371000, "", "Sleep of Reason / Fear"),
	})
	if outcome.Status != StatusResolved || outcome.Identity == nil {
		t.Fatalf("status = %q identity = %v, want the release to have named one",
			outcome.Status, outcome.Identity)
	}
	if outcome.Identity.RecordingID.String() != firstID {
		t.Fatalf("recording = %s, want %s", outcome.Identity.RecordingID, firstID)
	}
	if outcome.Identity.Method != "title-artist-duration-and-release" {
		t.Errorf("method = %q, want the tags and the release both recorded",
			outcome.Identity.Method)
	}
}

// Silence about where a recording appears is not evidence that it appears
// nowhere. A rival MusicBrainz lists no releases for has not been ruled out by
// an album it was never asked about — reading it as ruled out would let a
// recording lose for having its editions unfilled.
func TestARivalWithNoReleasesListedIsNotRuledOutByTheAlbum(t *testing.T) {
	unlisted := tagged(secondID, "1992", "No_4mat", 405000, "USLZJ1886830", "")
	unlisted.Releases = nil

	outcome := decide(Evidence{
		Subject: SubjectEntry, Artist: "No_4mat", Album: "1992", Title: "1992",
		DurationMS: 404998, ISRC: "USLZJ1886830",
	}, []musicbrainz.Recording{
		tagged(firstID, "1992", "No_4mat", 404000, "USLZJ1886830", "1992"),
		unlisted,
	})
	if outcome.Status != StatusNeedsReview || outcome.Identity != nil {
		t.Fatalf("status = %q identity = %v, want the question kept",
			outcome.Status, outcome.Identity)
	}
}

// A method is a name Schall uses to itself. "fingerprint-and-identifier" tells
// a reader nothing, and the interface used to print exactly that with the
// hyphens taken out, so the sentence every name turns into is fixed here.
func TestEveryMethodReadsAsASentence(t *testing.T) {
	const fallback = "Schall settled this automatically from the evidence it had."
	cases := map[string]string{
		"recording-id": "The file's tags carry this recording's MusicBrainz identifier.",
		"isrc": "The file's tags carry this recording's ISRC, the code a record " +
			"label gives a track.",
		"fingerprint": "Schall listened to the audio and it matches this recording.",
		"fingerprint-and-identifier": "Schall listened to the audio and it matches this " +
			"recording. The file's tags name the same one.",
		"published-sample": "Schall compared the audio with the sample this recording's " +
			"distributor publishes, and it is the same music.",
		"published-sample-and-release": "Schall compared the audio with the sample this " +
			"recording's distributor publishes, and it is the same music. More than one " +
			"recording fit, and the file's album tag names this one.",
		"library-witness": "Schall compared the audio with a file already in your library " +
			"that is proven to be this recording, and it is the same music.",
		"library-witness-and-release": "Schall compared the audio with a file already in " +
			"your library that is proven to be this recording, and it is the same music. " +
			"More than one recording fit, and the file's album tag names this one.",
		"title-artist-duration": "The title, artist and length in the file's tags all " +
			"match this recording.",
		"manual": "Somebody chose this recording by hand.",
		"recording-id-and-release": "The file's tags carry this recording's MusicBrainz " +
			"identifier. More than one recording fit, and the file's album tag names this one.",
		"isrc-and-release": "The file's tags carry this recording's ISRC, the code a record " +
			"label gives a track. More than one recording fit, and the file's album tag " +
			"names this one.",
		"fingerprint-and-release": "Schall listened to the audio and it matches this " +
			"recording. More than one recording fit, and the file's album tag names this one.",
		"fingerprint-and-identifier-and-release": "Schall listened to the audio and it " +
			"matches this recording. The file's tags name the same one. More than one " +
			"recording fit, and the file's album tag names this one.",
		"title-artist-duration-and-release": "The title, artist and length in the file's " +
			"tags all match this recording. More than one recording fit, and the file's " +
			"album tag names this one.",
	}
	for method, want := range cases {
		if got := MethodSentence(method); got != want {
			t.Errorf("MethodSentence(%q) = %q, want %q", method, got, want)
		}
	}

	// Every grade there is, as it is recorded and as the release records it, so
	// a new grade cannot arrive without a sentence. noMatch and candidateOnly
	// are named by no identity at all: they are why the name can be empty.
	for level := noMatch; level <= byFingerprint; level++ {
		methods := []string{level.method()}
		if level.conclusive() {
			methods = append(methods, level.method()+singledOutByRelease)
		}
		for _, method := range methods {
			sentence := MethodSentence(method)
			switch {
			case level.conclusive() && cases[method] == "":
				t.Errorf("method %q has no sentence of its own", method)
			case !level.conclusive() && sentence != fallback:
				t.Errorf("MethodSentence(%q) = %q, want the fallback %q", method, sentence, fallback)
			}
			if method != "" && (strings.Contains(sentence, method) ||
				strings.Contains(sentence, strings.ReplaceAll(method, "-", " "))) {
				t.Errorf("MethodSentence(%q) = %q, which prints the name itself", method, sentence)
			}
		}
	}
}

// A name nothing knows is a name from an older version of Schall, or one the
// seeded collection carries. It reads as a sentence too, because the point is
// that no name reaches the screen.
func TestAnUnknownMethodStillReadsAsASentence(t *testing.T) {
	const fallback = "Schall settled this automatically from the evidence it had."
	for _, method := range []string{
		"", "musicbrainz_recording_id", "no_candidate", "position-and-title", "-and-release",
	} {
		if got := MethodSentence(method); got != fallback {
			t.Errorf("MethodSentence(%q) = %q, want %q", method, got, fallback)
		}
	}
}

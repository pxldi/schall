package downloads

import (
	"strings"
	"testing"

	"github.com/pxldi/schall/internal/db"
)

// tags builds the ordinary case: a credit, a release, a title, a position and
// a length in seconds.
func tags(artist, album, title string, disc, number, seconds int) db.ImportTags {
	return db.ImportTags{
		Artist: artist, Album: album, Title: title,
		DiscNumber: disc, TrackNumber: number, DurationMS: seconds * 1000,
	}
}

// The case that made position-only matching untenable: a single whose only
// available copy was ripped from the album that later collected it. Everything
// that identifies a recording agrees; only the release it was filed under does
// not, and that is not evidence about which recording this is.
func TestMatchingIdentifiesARecordingCollectedOnAnotherRelease(t *testing.T) {
	catalogue := []db.ImportTags{tags("Sewerslvt", "Squids", "Squids", 1, 1, 344)}
	files := []fileTags{{
		name: "04 - Squids.flac",
		tags: tags("Sewerslvt", "Drowning in the Sewer", "Squids", 1, 4, 344),
	}}

	result := matchRelease(files, catalogue, nil)
	if result[0].track != 0 || result[0].match == nil {
		t.Fatalf("match = %+v, problem = %q", result[0].match, result[0].problem)
	}
	if result[0].match.Method != "title-and-duration" {
		t.Fatalf("method = %q", result[0].match.Method)
	}
	// What disagreed is remembered rather than waved through.
	notes := strings.Join(result[0].match.Notes, " | ")
	if !strings.Contains(notes, "position 1-4") || !strings.Contains(notes, "Drowning in the Sewer") {
		t.Fatalf("notes = %q", notes)
	}
}

// The same title and the same length under a different name is how a cover
// version becomes the original. Nothing may match across a credit like that.
func TestMatchingRefusesADifferentArtist(t *testing.T) {
	catalogue := []db.ImportTags{tags("Sewerslvt", "Squids", "Squids", 1, 1, 344)}
	files := []fileTags{{
		name: "01 - Squids.flac",
		tags: tags("Some Other Producer", "Squids", "Squids", 1, 1, 344),
	}}

	result := matchRelease(files, catalogue, nil)
	if result[0].track != -1 {
		t.Fatalf("matched a different artist: %+v", result[0])
	}
	if len(result[0].candidates) != 1 ||
		!contains(result[0].candidates[0].Differs, "artist") {
		t.Fatalf("candidates = %#v", result[0].candidates)
	}
}

// A guest credit is the same artist, and a compilation credit says nothing
// about any one track, so neither may stop a match on its own.
func TestMatchingAcceptsCreditsThatStillNameTheReleaseArtist(t *testing.T) {
	catalogue := []db.ImportTags{tags("Sewerslvt", "Album", "Song", 1, 1, 200)}
	for _, credit := range []string{
		"Sewerslvt feat. Kudasaibeats", "Sewerslvt & Guest", "Various Artists",
	} {
		files := []fileTags{{name: "01.flac", tags: tags(credit, "Album", "Song", 1, 1, 200)}}
		if result := matchRelease(files, catalogue, nil); result[0].track != 0 {
			t.Fatalf("credit %q was not matched: %q", credit, result[0].problem)
		}
	}
	// A credit that merely begins with the same word is a different artist.
	files := []fileTags{{name: "01.flac", tags: tags("Sewerslvt Tribute Band", "Album", "Song", 1, 1, 200)}}
	if result := matchRelease(files, catalogue, nil); result[0].track != -1 {
		t.Fatal("matched an artist that only shares a prefix")
	}
}

// A live take sits at the position of the studio version and is not it. The
// old position-first rule matched exactly this and had to be talked out of it
// by a duration check; the grade never gets that far.
func TestMatchingRefusesAnotherTakeAtTheSamePosition(t *testing.T) {
	catalogue := []db.ImportTags{tags("Artist", "Album", "Second", 1, 2, 200)}
	files := []fileTags{{name: "02.flac", tags: tags("Artist", "Album", "Second (live)", 1, 2, 400)}}

	result := matchRelease(files, catalogue, nil)
	if result[0].track != -1 {
		t.Fatalf("matched a different take: %+v", result[0].match)
	}
	if !strings.Contains(result[0].problem, "not conclusively any track") {
		t.Fatalf("problem = %q", result[0].problem)
	}
}

// A downloaded file labels the track the way the shop it came from does, and
// "Original Mix" is that label rather than the name of another recording. This
// is the download side of a rule tagmatch holds for both sides at once. What
// admits the folder is the user having chosen it; within a folder already
// chosen this decides which catalogue track each file is, so a file the label
// alone had kept unpaired now pairs with the track it plainly is.
func TestMatchingPairsAFileLabelledAsTheOriginalMix(t *testing.T) {
	catalogue := []db.ImportTags{tags("Hadone", "Defining Persuasion", "How to Fake Success", 1, 1, 387)}
	files := []fileTags{{
		name: "01 - How To Fake Success (Original Mix).flac",
		tags: tags("Hadone", "Defining Persuasion", "How To Fake Success - Original mix", 1, 1, 387),
	}}

	result := matchRelease(files, catalogue, nil)
	if result[0].track != 0 || result[0].match == nil {
		t.Fatalf("match = %+v, problem = %q", result[0].match, result[0].problem)
	}
}

// That label is the only one carrying no recording of its own. A remixer's name
// in the same place is different audio, and it must not pair with the original
// even when every other tag on the file agrees.
func TestMatchingStillRefusesAFileLabelledAsARemix(t *testing.T) {
	catalogue := []db.ImportTags{tags("Hadone", "Defining Persuasion", "How to Fake Success", 1, 1, 387)}
	files := []fileTags{{
		name: "01 - How To Fake Success (Regal Remix).flac",
		tags: tags("Hadone", "Defining Persuasion", "How To Fake Success - Regal Remix", 1, 1, 387),
	}}

	result := matchRelease(files, catalogue, nil)
	if result[0].track != -1 {
		t.Fatalf("matched a remix to the original: %+v", result[0].match)
	}
}

// Two tracks a file fits equally well are not a reason to pick the first one.
// The file here carries a position neither track uses, so nothing is left to
// tell the two interludes apart.
func TestMatchingWillNotChooseBetweenTwoEqualTracks(t *testing.T) {
	catalogue := []db.ImportTags{
		tags("Artist", "Album", "Interlude", 1, 3, 90),
		tags("Artist", "Album", "Interlude", 1, 7, 90),
	}
	files := []fileTags{{name: "04.flac", tags: tags("Artist", "Compilation", "Interlude", 1, 4, 90)}}

	result := matchRelease(files, catalogue, nil)
	if result[0].track != -1 {
		t.Fatalf("chose between identical tracks: %+v", result[0].match)
	}
	if !strings.Contains(result[0].problem, "does not choose between them") ||
		!strings.Contains(result[0].problem, "1-7") {
		t.Fatalf("problem = %q", result[0].problem)
	}
}

// When the file does carry a position, it is the thing that tells two
// otherwise identical tracks apart, and the stronger grade wins on its own.
func TestMatchingLetsPositionSettleOtherwiseIdenticalTracks(t *testing.T) {
	catalogue := []db.ImportTags{
		tags("Artist", "Album", "Interlude", 1, 3, 90),
		tags("Artist", "Album", "Interlude", 1, 7, 90),
	}
	files := []fileTags{{name: "03.flac", tags: tags("Artist", "Album", "Interlude", 1, 3, 90)}}

	result := matchRelease(files, catalogue, nil)
	if result[0].track != 0 || result[0].match.Method != "position-and-title" {
		t.Fatalf("outcome = %+v (%q)", result[0].match, result[0].problem)
	}
}

// Position and title do not identify a peer-sourced file without album or
// duration to corroborate them.
func TestMatchingRequiresAlbumOrDurationForPositionAndTitle(t *testing.T) {
	file := db.ImportTags{Title: "Interlude", DiscNumber: 1, TrackNumber: 3}
	track := db.ImportTags{Title: "Interlude", DiscNumber: 1, TrackNumber: 3}

	if level := gradeOf(compare(file, track)); level.conclusive() {
		t.Fatalf("grade = %v, want an inconclusive grade", level)
	}
}

// Nor are two files that fit one track equally well. Whichever is the real one,
// importing either would record provenance nobody established.
func TestMatchingWillNotChooseBetweenTwoEqualFiles(t *testing.T) {
	catalogue := []db.ImportTags{tags("Artist", "Album", "Song", 1, 1, 200)}
	files := []fileTags{
		{name: "01.flac", tags: tags("Artist", "Album", "Song", 1, 1, 200)},
		{name: "01 (1).flac", tags: tags("Artist", "Album", "Song", 1, 1, 200)},
	}

	result := matchRelease(files, catalogue, nil)
	for index, outcome := range result {
		if outcome.track != -1 {
			t.Fatalf("file %d was matched: %+v", index, outcome.match)
		}
		if !strings.Contains(outcome.problem, "both match catalogue track") {
			t.Fatalf("file %d problem = %q", index, outcome.problem)
		}
	}
}

// Settling one pair can leave a neighbour uncontested. Matching is expected to
// notice, rather than to give up on the whole grade.
func TestMatchingSettlesWhatAStrongerPairLeavesUncontested(t *testing.T) {
	recording := "6f1a4d7f-2f2c-4d3f-9d6a-2a9d4e6b1c11"
	catalogue := []db.ImportTags{
		tags("Artist", "Album", "Interlude", 1, 3, 90),
		tags("Artist", "Album", "Interlude", 1, 7, 90),
	}
	catalogue[0].MusicBrainzRecordingID = recording
	files := []fileTags{
		// Proven to be the first interlude, and nothing else.
		{name: "03.flac", tags: withRecording(tags("Artist", "Album", "Interlude", 1, 3, 90), recording)},
		// Ambiguous on its own; the only interlude left once the first is gone.
		{name: "07.flac", tags: tags("Artist", "Album", "Interlude", 1, 7, 90)},
	}

	result := matchRelease(files, catalogue, nil)
	if result[0].track != 0 || result[0].match.Method != "recording-id" {
		t.Fatalf("first file = %+v (%q)", result[0].match, result[0].problem)
	}
	if result[1].track != 1 {
		t.Fatalf("second file = %q", result[1].problem)
	}
}

// An identifier is the strongest evidence Schall has, and it is still only
// evidence. A recording ID that agrees while the length disagrees by a minute
// describes a mistagged file, not an identified one.
func TestMatchingRefusesAnIdentifierItsOwnFileContradicts(t *testing.T) {
	recording := "6f1a4d7f-2f2c-4d3f-9d6a-2a9d4e6b1c11"
	catalogue := []db.ImportTags{withRecording(tags("Artist", "Album", "Song", 1, 1, 200), recording)}
	files := []fileTags{{
		name: "01.flac",
		tags: withRecording(tags("Artist", "Album", "Song", 1, 1, 260), recording),
	}}

	result := matchRelease(files, catalogue, nil)
	if result[0].track != -1 {
		t.Fatalf("matched on a contradicted identifier: %+v", result[0].match)
	}
	if !contains(result[0].candidates[0].Differs, "duration (60 seconds out)") {
		t.Fatalf("candidate = %#v", result[0].candidates[0])
	}
}

// An identifier that agrees does settle a file whose title and position were
// never going to: a retitled track is what recording IDs are for.
func TestMatchingTrustsAnUncontradictedIdentifierOverEverythingElse(t *testing.T) {
	recording := "6f1a4d7f-2f2c-4d3f-9d6a-2a9d4e6b1c11"
	catalogue := []db.ImportTags{
		tags("Artist", "Album", "Second", 1, 2, 200),
		withRecording(tags("Artist", "Album", "Third", 1, 3, 200), recording),
	}
	files := []fileTags{{
		name: "02.flac",
		tags: withRecording(tags("Artist", "Album", "Untitled", 1, 2, 200), recording),
	}}

	result := matchRelease(files, catalogue, nil)
	if result[0].track != 1 || result[0].match.Method != "recording-id" {
		t.Fatalf("outcome = %+v (%q)", result[0].match, result[0].problem)
	}
}

// A track a person already answered for is not available to matching, and a
// file that wanted it is told what happened rather than left unexplained.
func TestMatchingLeavesResolvedTracksAlone(t *testing.T) {
	catalogue := []db.ImportTags{tags("Artist", "Album", "Song", 1, 1, 200)}
	files := []fileTags{{name: "02.flac", tags: tags("Artist", "Album", "Song", 1, 1, 200)}}

	result := matchRelease(files, catalogue, map[int]string{0: "01.flac"})
	if result[0].track != -1 {
		t.Fatal("matching overruled a recorded decision")
	}
	if !strings.Contains(result[0].problem, `already resolved to "01.flac"`) {
		t.Fatalf("problem = %q", result[0].problem)
	}
	if result[0].candidates[0].TakenBy != "01.flac" {
		t.Fatalf("candidate = %#v", result[0].candidates[0])
	}
}

// Silence is not agreement. A file with no tags at all matches nothing, however
// short the release is.
func TestMatchingReadsMissingTagsAsSilence(t *testing.T) {
	catalogue := []db.ImportTags{tags("Artist", "Album", "Song", 1, 1, 200)}
	files := []fileTags{{name: "01.flac", tags: db.ImportTags{}}}

	result := matchRelease(files, catalogue, nil)
	if result[0].track != -1 || len(result[0].candidates) != 0 {
		t.Fatalf("outcome = %+v", result[0])
	}
	if !strings.Contains(result[0].problem, "matches no track of this edition") {
		t.Fatalf("problem = %q", result[0].problem)
	}
}

// Rounding between a tagger and the catalogue is not a disagreement; a
// different performance is.
func TestDurationToleranceAbsorbsRoundingOnly(t *testing.T) {
	catalogue := []db.ImportTags{{Title: "Song", DiscNumber: 1, TrackNumber: 1, DurationMS: 200_000}}
	inside := compare(db.ImportTags{Title: "Song", DiscNumber: 1, TrackNumber: 1, DurationMS: 204_000}, catalogue[0])
	outside := compare(db.ImportTags{Title: "Song", DiscNumber: 1, TrackNumber: 1, DurationMS: 206_000}, catalogue[0])
	if inside.duration != agrees || outside.duration != differs {
		t.Fatalf("inside = %v, outside = %v", inside.duration, outside.duration)
	}
	// A duration only one side knows is silence rather than a disagreement.
	silent := compare(db.ImportTags{Title: "Song", DiscNumber: 1, TrackNumber: 1}, catalogue[0])
	if silent.duration != unknown {
		t.Fatalf("silent = %v", silent.duration)
	}
}

// An ISRC names a recording outright, so it settles a file whose title the
// tagger spelled its own way.
func TestMatchingIdentifiesAFileByItsISRC(t *testing.T) {
	catalogue := []db.ImportTags{tags("Artist", "Album", "Sorrow", 1, 1, 200)}
	catalogue[0].ISRC = "GBAYE0601498"
	file := tags("Artist", "Album", "sorrow (2004 remaster)", 1, 1, 200)
	file.ISRC = "GBAYE0601498"

	result := matchRelease([]fileTags{{name: "01.flac", tags: file}}, catalogue, nil)

	if result[0].track != 0 || result[0].match == nil {
		t.Fatalf("outcome = %+v (%q)", result[0].match, result[0].problem)
	}
	if result[0].match.Method != "isrc" || result[0].match.Summary != "matched by ISRC" {
		t.Fatalf("match = %+v, want the ISRC named as what settled it", result[0].match)
	}
}

// An ISRC that disagrees is a contradiction, and a contradiction voids the pair
// whatever else agrees — here everything else does.
func TestMatchingRefusesAFileWhoseISRCDisagrees(t *testing.T) {
	catalogue := []db.ImportTags{tags("Artist", "Album", "Sorrow", 1, 1, 200)}
	catalogue[0].ISRC = "GBAYE0601498"
	file := tags("Artist", "Album", "Sorrow", 1, 1, 200)
	file.ISRC = "USRC17607839"

	result := matchRelease([]fileTags{{name: "01.flac", tags: file}}, catalogue, nil)

	if result[0].track != -1 {
		t.Fatalf("matched across a disagreeing ISRC: %+v", result[0].match)
	}
	if !contains(result[0].candidates[0].Differs, "ISRC") {
		t.Fatalf("candidate = %#v, want the ISRC named as what stands against it", result[0].candidates[0])
	}
}

// A file that lost a track to stronger evidence is told which file took it, so
// review can see that the two files were compared rather than that this one was
// simply refused.
func TestAFileIsToldWhichFileMatchedItsTrackMoreConclusively(t *testing.T) {
	recording := "6f1a4d7f-2f2c-4d3f-9d6a-2a9d4e6b1c11"
	catalogue := []db.ImportTags{withRecording(tags("Artist", "Album", "Song", 1, 1, 200), recording)}
	files := []fileTags{
		{name: "proven.flac", tags: withRecording(tags("Artist", "Album", "Song", 1, 1, 200), recording)},
		{name: "other.flac", tags: tags("Artist", "Album", "Song", 1, 1, 200)},
	}

	result := matchRelease(files, catalogue, nil)

	if result[0].track != 0 {
		t.Fatalf("the proven file was not matched: %q", result[0].problem)
	}
	if result[1].track != -1 {
		t.Fatalf("two files were matched to one track: %+v", result[1].match)
	}
	if !strings.Contains(result[1].problem, `"proven.flac" matched more conclusively`) {
		t.Fatalf("problem = %q, want it to name the file that took the track", result[1].problem)
	}
}

// A file whose only conclusive track is still free is nonetheless not matched
// when the pass at its own grade refused it: the rival that blocked it was
// settled afterwards, on weaker evidence and a different track. Nothing goes in
// on a second thought, which is the conservative direction.
func TestAFileRefusedAtItsOwnGradeIsNotMatchedAfterwards(t *testing.T) {
	catalogue := []db.ImportTags{
		{Artist: "Artist", Album: "Album", Title: "Song", DiscNumber: 1, TrackNumber: 1},
		tags("Artist", "Album", "Song", 1, 2, 300),
	}
	files := []fileTags{
		// Reaches the untimed track by position and title, and nothing else.
		{name: "blocked.flac", tags: db.ImportTags{
			Artist: "Artist", Album: "Album", Title: "Song", DiscNumber: 1, TrackNumber: 1,
		}},
		// Reaches it the same way, and the second track by title and duration.
		{name: "rival.flac", tags: tags("Artist", "Album", "Song", 1, 1, 300)},
	}

	result := matchRelease(files, catalogue, nil)

	if result[1].track != 1 {
		t.Fatalf("the rival was not settled on the track it identified: %q", result[1].problem)
	}
	if result[0].track != -1 {
		t.Fatalf("a file refused at its own grade was matched later: %+v", result[0].match)
	}
	if !strings.Contains(result[0].problem, "the evidence did not settle it") {
		t.Fatalf("problem = %q", result[0].problem)
	}
}

// nearMisses is one file against a release of same-titled tracks, which is how
// review ends up with more plausible answers than it can use.
func nearMisses() ([]fileTags, []db.ImportTags) {
	catalogue := []db.ImportTags{
		tags("Artist", "Album", "Interlude", 1, 1, 90),
		tags("Artist", "Album", "Interlude", 1, 2, 92),
		tags("Artist", "Album", "Interlude", 1, 3, 200),
		{Artist: "Artist", Album: "Album", Title: "Interlude",
			DiscNumber: 1, TrackNumber: 5, DurationMS: 90_500},
	}
	files := []fileTags{{
		name: "04.flac", tags: tags("Artist", "Compilation", "Interlude", 1, 4, 90),
	}}
	return files, catalogue
}

// The candidate a reviewer is most likely to want is the one closest on the
// evidence, and among equally strong ones the closest in length.
func TestCandidatesAreRankedClosestFirst(t *testing.T) {
	files, catalogue := nearMisses()

	candidates := matchRelease(files, catalogue, nil)[0].candidates

	if len(candidates) < 2 {
		t.Fatalf("candidates = %#v, want the near misses reported", candidates)
	}
	if candidates[0].Track.TrackNumber != 1 {
		t.Fatalf("first candidate = %#v, want the exact length first", candidates[0])
	}
	if candidates[1].Track.TrackNumber != 5 {
		t.Fatalf("second candidate = %#v, want the next closest length", candidates[1])
	}
}

// Review needs the plausible answers, not the whole edition, so the list is
// capped rather than growing with the release.
func TestNoMoreThanThreeCandidatesAreReported(t *testing.T) {
	files, catalogue := nearMisses()

	candidates := matchRelease(files, catalogue, nil)[0].candidates

	if len(candidates) != maxCandidates {
		t.Fatalf("reported %d candidates, want at most %d", len(candidates), maxCandidates)
	}
}

func withRecording(value db.ImportTags, recording string) db.ImportTags {
	value.MusicBrainzRecordingID = recording
	return value
}

func contains(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}

package db

import (
	"testing"

	"github.com/jackc/pgx/v5/pgtype"
)

// The summary is what a refusal says first. It has to read as a sentence for
// one track as well as for many, and for a single-track release, which has no
// "all" to speak of.
func TestDuplicateSummaryReadsAsASentence(t *testing.T) {
	for name, testCase := range map[string]struct {
		evidence DuplicateEvidence
		want     string
	}{
		"nothing held": {
			evidence: DuplicateEvidence{TrackCount: 11},
			want:     "The library holds nothing that looks like this release.",
		},
		"the whole release": {
			evidence: DuplicateEvidence{
				TrackCount: 11, OwnedTrackCount: 11, Files: make([]DuplicateFile, 11),
			},
			want: "The library already holds all 11 tracks of this release.",
		},
		"a single-track release": {
			evidence: DuplicateEvidence{
				TrackCount: 1, OwnedTrackCount: 1, Files: make([]DuplicateFile, 1),
			},
			want: "The library already holds the only track of this release.",
		},
		"part of the release": {
			evidence: DuplicateEvidence{
				TrackCount: 11, OwnedTrackCount: 1, Files: make([]DuplicateFile, 1),
			},
			want: "The library already holds 1 of the 11 tracks of this release.",
		},
		"one unresolved file alone": {
			evidence: DuplicateEvidence{
				TrackCount: 11, UnresolvedCount: 1, Files: make([]DuplicateFile, 1),
			},
			want: "The library holds 1 file that looks like this release " +
				"without a proven track identity.",
		},
		"several unresolved files alone": {
			evidence: DuplicateEvidence{
				TrackCount: 11, UnresolvedCount: 3, Files: make([]DuplicateFile, 3),
			},
			want: "The library holds 3 files that look like this release " +
				"without a proven track identity.",
		},
		"owned and unresolved together": {
			evidence: DuplicateEvidence{
				TrackCount: 11, OwnedTrackCount: 2, UnresolvedCount: 1,
				Files: make([]DuplicateFile, 3),
			},
			want: "The library already holds 2 of the 11 tracks of this release, " +
				"and 1 file that looks like this release without a proven track identity.",
		},
		// A release whose catalogue tracks were never ingested still has to say
		// something true about the files that point at it.
		"an edition with no tracks yet": {
			evidence: DuplicateEvidence{
				OwnedTrackCount: 2, Files: make([]DuplicateFile, 2),
			},
			want: "The library already holds 2 tracks of this release.",
		},
	} {
		if got := duplicateSummary(testCase.evidence); got != testCase.want {
			t.Errorf("%s:\n got %q\nwant %q", name, got, testCase.want)
		}
	}
}

// Each file says why it counts, in the words of the evidence that put it there.
func TestDuplicateSentenceNamesItsEvidence(t *testing.T) {
	for name, testCase := range map[string]struct {
		row   duplicateRow
		track string
		want  string
	}{
		"owned by an imported download": {
			row:   duplicateRow{state: "owned", reason: "verified_download"},
			track: "1. Squids",
			want:  "is already matched through a download Schall imported to 1. Squids.",
		},
		"owned by hand": {
			row:   duplicateRow{state: "owned", reason: "manual"},
			track: "2. Blooming Blue",
			want:  "is already matched by hand to 2. Blooming Blue.",
		},
		// The mapping can outlive the track title in the evidence row; the
		// sentence still has to finish.
		"owned without a named track": {
			row:  duplicateRow{state: "owned", reason: "isrc"},
			want: "is already matched by ISRC to a track of this release.",
		},
		"unresolved by tags": {
			row:  duplicateRow{state: "unresolved", reason: "tags", matchStatus: "unmatched"},
			want: "is tagged as this release, but no catalogue track was proven for it.",
		},
		// A file with no identifiers of its own still counts against a
		// download once the providers have said what it is.
		"unresolved by a resolved identity": {
			row: duplicateRow{
				state: "unresolved", reason: "identity", matchStatus: "unmatched",
			},
			want: "was resolved to a recording from this release, " +
				"but no catalogue track was proven for it.",
		},
		"unresolved between several tracks": {
			row: duplicateRow{
				state: "unresolved", reason: "recording_id", matchStatus: "ambiguous",
			},
			want: "carries a MusicBrainz recording ID from this release, " +
				"and more than one catalogue track fits it.",
		},
		"owned by a MusicBrainz recording ID": {
			row:   duplicateRow{state: "owned", reason: "musicbrainz_recording_id"},
			track: "3. Ghost",
			want:  "is already matched by MusicBrainz recording ID to 3. Ghost.",
		},
		"owned through a resolved identity": {
			row:   duplicateRow{state: "owned", reason: "resolved_identity"},
			track: "4. Bloom",
			want:  "is already matched through the identity resolved for it to 4. Bloom.",
		},
		"owned by artist, album and position": {
			row:   duplicateRow{state: "owned", reason: "album_disc_track"},
			track: "5. Drift",
			want:  "is already matched by artist, album, and track position to 5. Drift.",
		},
		// A mapping made by a method this sentence has never heard of still has to
		// finish, because the file counts against the request either way.
		"owned by something unnamed": {
			row:   duplicateRow{state: "owned", reason: "some_later_method"},
			track: "6. Static",
			want:  "is already matched to 6. Static.",
		},
		// The same for evidence: an unresolved file with a reason nothing here
		// names is still a file that looks like this release.
		"unresolved for a reason nothing names": {
			row: duplicateRow{
				state: "unresolved", reason: "", matchStatus: "unmatched",
			},
			want: "is tagged as this release, but no catalogue track was proven for it.",
		},
		"unresolved by ISRC": {
			row: duplicateRow{
				state: "unresolved", reason: "isrc", matchStatus: "unmatched",
			},
			want: "carries an ISRC from this release, " +
				"but no catalogue track was proven for it.",
		},
	} {
		if got := duplicateSentence(testCase.row, testCase.track); got != testCase.want {
			t.Errorf("%s:\n got %q\nwant %q", name, got, testCase.want)
		}
	}
}

// A track's position is how somebody finds it on the sleeve, so a release with
// more than one disc has to say which disc as well as which track.
func TestATrackOnALaterDiscIsNamedWithItsDisc(t *testing.T) {
	label := trackLabel(duplicateRow{
		trackTitle:  pgtype.Text{String: "Ghost", Valid: true},
		discNumber:  pgtype.Int4{Int32: 2, Valid: true},
		trackNumber: pgtype.Int4{Int32: 3, Valid: true},
	})
	if label != "2-3. Ghost" {
		t.Fatalf("label = %q, want the disc named beside the track", label)
	}
}

// A single-disc release says only the track: "1-3." would be noise on a sleeve
// that has no second disc.
func TestATrackOnTheOnlyDiscIsNamedWithoutIt(t *testing.T) {
	label := trackLabel(duplicateRow{
		trackTitle:  pgtype.Text{String: "Ghost", Valid: true},
		discNumber:  pgtype.Int4{Int32: 1, Valid: true},
		trackNumber: pgtype.Int4{Int32: 3, Valid: true},
	})
	if label != "3. Ghost" {
		t.Fatalf("label = %q, want the track alone", label)
	}
}

// A track with no position at all is still named, because the title is what the
// sentence is about.
func TestATrackWithNoPositionIsNamedByItsTitleAlone(t *testing.T) {
	label := trackLabel(duplicateRow{trackTitle: pgtype.Text{String: "Ghost", Valid: true}})
	if label != "Ghost" {
		t.Fatalf("label = %q, want the title alone", label)
	}
}

// What a file says about itself, joined into one line. Blank tags are silence and
// are left out rather than rendered as empty gaps.
func TestAFilesOwnWordsSkipTheTagsItLeftBlank(t *testing.T) {
	label := tagLabel(duplicateRow{
		artistTag: pgtype.Text{String: "Anetha", Valid: true},
		albumTag:  pgtype.Text{String: "   ", Valid: true},
		titleTag:  pgtype.Text{String: "Salsed Punk", Valid: true},
	})
	if label != "Anetha — Salsed Punk" {
		t.Fatalf("label = %q, want the blank tag left out", label)
	}
}

// Nothing stores what the audio in a file actually is, so the only thing that
// can be said about the format is what the name claims.
func TestACopysFormatIsReadOffItsExtension(t *testing.T) {
	if format := fileFormat("/music/Mezzanine/06 Teardrop.flac"); format != "FLAC" {
		t.Fatalf("format = %q, want the extension upper-cased", format)
	}
}

// A file with no extension has nothing to say about its format, and an empty
// answer is better than one made up.
func TestACopyWithNoExtensionClaimsNoFormat(t *testing.T) {
	if format := fileFormat("/music/inbox/track01"); format != "" {
		t.Fatalf("format = %q, want nothing claimed", format)
	}
}

// How a copy came to answer the recording, in the words of the two things that
// can answer it. A file reached both ways says both, because either alone would
// leave out evidence somebody is being shown.
func TestACopyReachedBothWaysSaysSo(t *testing.T) {
	for name, testCase := range map[string]struct {
		byMapping  bool
		byIdentity bool
		want       string
	}{
		"through a track": {
			byMapping: true,
			want:      "Matched to a catalogue track",
		},
		"through a resolved identity": {
			byIdentity: true,
			want:       "Identified as this recording",
		},
		"through both": {
			byMapping: true, byIdentity: true,
			want: "Matched to a catalogue track, and identified as this recording",
		},
	} {
		t.Run(name, func(t *testing.T) {
			reason := heldTwiceReason(testCase.byMapping, testCase.byIdentity)
			if reason != testCase.want {
				t.Fatalf("reason = %q, want %q", reason, testCase.want)
			}
		})
	}
}

// Where a copy sits is the catalogue release it was matched on; a copy matched
// to nothing falls back to the album its own tags claim.
func TestACopyWithNoReleaseFallsBackToItsOwnTag(t *testing.T) {
	release := firstText(
		pgtype.Text{Valid: false},
		pgtype.Text{String: "Mezzanine", Valid: true},
	)
	if release != "Mezzanine" {
		t.Fatalf("release = %q, want the file's own tag", release)
	}
}

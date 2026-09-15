package sources

import (
	"strings"
	"testing"
)

// The whole point of the setting: a person whose player cannot read FLAC says
// so, and the copy they can play comes first.
func TestAPreferredFormatIsRankedAboveALosslessOne(t *testing.T) {
	ranked := Rank([]Candidate{
		candidate("lossless", "flac", 10, 0),
		candidate("lossy", "mp3", 10, 320),
	}, Query{})

	preferred, refused := Prefer(ranked, Preferences{Preferred: []string{"mp3"}})

	if len(refused) != 0 {
		t.Fatalf("refused %d candidates, want none", len(refused))
	}
	if preferred[0].Username != "lossy" {
		t.Fatalf("order = %v, want the preferred format first", usernames(preferred))
	}
}

// And it says so on the candidate, because a reader looking at a list that no
// longer puts lossless first is owed the reason.
func TestAPreferredFormatSaysThatIsWhyItIsThere(t *testing.T) {
	ranked := Rank([]Candidate{
		candidate("lossless", "flac", 10, 0),
		candidate("lossy", "mp3", 10, 320),
	}, Query{})

	preferred, _ := Prefer(ranked, Preferences{Preferred: []string{"mp3"}})

	if reasons := strings.Join(preferred[0].Reasons, " | "); !strings.Contains(reasons, "the format you prefer") {
		t.Fatalf("reasons %q do not say the format was preferred", reasons)
	}
	if reasons := strings.Join(preferred[1].Reasons, " | "); !strings.Contains(reasons, "below the formats you prefer") {
		t.Fatalf("reasons %q do not say the format was not preferred", reasons)
	}
}

// The order given is the order kept, so "MP3 then FLAC then everything else" is
// three statements rather than one.
func TestTheStatedOrderIsTheOrderTheResultsCome(t *testing.T) {
	ranked := Rank([]Candidate{
		candidate("third", "ogg", 10, 320),
		candidate("second", "flac", 10, 0),
		candidate("first", "mp3", 10, 320),
	}, Query{})

	preferred, _ := Prefer(ranked, Preferences{Preferred: []string{"mp3", "flac", "ogg"}})

	if got := usernames(preferred); got[0] != "first" || got[1] != "second" || got[2] != "third" {
		t.Fatalf("order = %v, want the order that was asked for", got)
	}
}

// A refused format leaves the results. Ranking it low would not do: the moment
// it is the only copy on offer, low is still first.
func TestAnUnacceptableFormatIsTakenOutRatherThanRankedLow(t *testing.T) {
	ranked := Rank([]Candidate{candidate("peer", "dsf", 10, 0)}, Query{})

	kept, refused := Prefer(ranked, Preferences{Unacceptable: []string{"dsf"}})

	if len(kept) != 0 {
		t.Fatalf("kept %d candidates, want the refused format gone", len(kept))
	}
	if len(refused) != 1 || !strings.Contains(refused[0].Reason, "DSF") {
		t.Fatalf("refused = %#v, want the format named", refused)
	}
	if refused[0].Kind != RefusedFormat {
		t.Fatalf("kind = %q, want the format rule named", refused[0].Kind)
	}
}

// A bitrate floor is about lossy copies. A lossless copy has no bitrate to
// compare, so the floor says nothing about it.
func TestABitrateFloorLeavesALosslessCopyAlone(t *testing.T) {
	ranked := Rank([]Candidate{candidate("peer", "flac", 10, 0)}, Query{})

	kept, refused := Prefer(ranked, Preferences{MinimumBitRate: 320})

	if len(kept) != 1 || len(refused) != 0 {
		t.Fatalf("kept %d and refused %d, want the lossless copy kept", len(kept), len(refused))
	}
}

// A bitrate the peer never stated does not meet a floor.
//
// This is the opposite of what the floor used to do, and deliberately so. The
// reading that silence is not evidence belongs to matching, where a guess
// invents a false match. A floor is not about what the music is: it says what
// quality of copy to fetch, and a copy of no stated quality is exactly the one
// somebody writing "at least 320" was refusing. Letting it through handed them
// the low-bitrate copy the setting exists to keep out.
func TestABitrateFloorRefusesAnUnstatedBitrate(t *testing.T) {
	ranked := Rank([]Candidate{candidate("peer", "mp3", 10, 0)}, Query{})

	kept, refused := Prefer(ranked, Preferences{MinimumBitRate: 320})

	if len(kept) != 0 {
		t.Fatalf("kept %d candidates, want the unstated bitrate refused", len(kept))
	}
	if len(refused) != 1 || refused[0].Kind != RefusedBitRate {
		t.Fatalf("refused = %#v, want one copy refused by the floor", refused)
	}
	if !strings.Contains(refused[0].Reason, "did not state") {
		t.Fatalf("reason = %q, want it to say the peer never stated a bitrate", refused[0].Reason)
	}
}

// A copy whose *format* nobody could work out is still kept. A floor names
// formats, and this candidate has named none: refusing it would be reading
// silence as an answer to a question that was never put.
func TestABitrateFloorLeavesACopyOfNoKnownFormatAlone(t *testing.T) {
	ranked := Rank([]Candidate{candidate("peer", "", 10, 0)}, Query{})

	kept, refused := Prefer(ranked, Preferences{MinimumBitRate: 320})

	if len(kept) != 1 || len(refused) != 0 {
		t.Fatalf("kept %d and refused %d, want the unknown format kept", len(kept), len(refused))
	}
}

// The point of the whole change: one floor per format, so "MP3 at least 320"
// can be said without refusing Opus, which never reaches 320.
func TestEachFormatIsHeldToItsOwnFloor(t *testing.T) {
	ranked := Rank([]Candidate{
		candidate("good mp3", "mp3", 10, 320),
		candidate("thin mp3", "mp3", 10, 192),
		candidate("good opus", "opus", 10, 192),
	}, Query{})

	kept, refused := Prefer(ranked, Preferences{
		FormatMinimumBitRate: map[string]int{"mp3": 320, "opus": 160},
	})

	if got := usernames(kept); len(got) != 2 {
		t.Fatalf("kept %v, want the 320 MP3 and the 192 Opus", got)
	}
	if len(refused) != 1 || refused[0].Username != "thin mp3" {
		t.Fatalf("refused = %#v, want only the MP3 under its own floor", refused)
	}
}

// A format with a floor of its own is held to that number and to nothing else.
// Somebody who wrote "MP3 at least 320" asked for 320, not for whichever of two
// numbers is larger.
func TestAFormatsOwnFloorReplacesTheGeneralOne(t *testing.T) {
	ranked := Rank([]Candidate{candidate("peer", "opus", 10, 192)}, Query{})

	kept, refused := Prefer(ranked, Preferences{
		MinimumBitRate:       320,
		FormatMinimumBitRate: map[string]int{"opus": 160},
	})

	if len(kept) != 1 || len(refused) != 0 {
		t.Fatalf("kept %d and refused %d, want the format's own floor to decide",
			len(kept), len(refused))
	}
}

// A lossy format with no floor of its own falls back to the general one.
func TestAFormatWithNoFloorOfItsOwnTakesTheGeneralOne(t *testing.T) {
	ranked := Rank([]Candidate{candidate("peer", "aac", 10, 128)}, Query{})

	kept, refused := Prefer(ranked, Preferences{
		MinimumBitRate:       256,
		FormatMinimumBitRate: map[string]int{"mp3": 320},
	})

	if len(kept) != 0 || len(refused) != 1 {
		t.Fatalf("kept %d and refused %d, want the general floor to apply to AAC",
			len(kept), len(refused))
	}
}

// A per-format floor with no general floor beside it still applies. Reading only
// the general number would leave the setting doing nothing at all.
func TestAPerFormatFloorAloneIsStillAFloor(t *testing.T) {
	ranked := Rank([]Candidate{candidate("peer", "mp3", 10, 128)}, Query{})

	kept, refused := Prefer(ranked, Preferences{
		FormatMinimumBitRate: map[string]int{"mp3": 320},
	})

	if len(kept) != 0 || len(refused) != 1 {
		t.Fatalf("kept %d and refused %d, want the per-format floor to apply",
			len(kept), len(refused))
	}
}

// What slskd reports for a variable-bitrate file is its average over the whole
// file, and that average is what the floor reads. A VBR MP3 averaging 245 kbps
// is below a 320 floor and goes, whatever any one second of it peaked at.
func TestAVariableBitRateCopyIsJudgedOnItsAverage(t *testing.T) {
	variable := candidate("peer", "mp3", 10, 245)
	for index := range variable.Files {
		variable.Files[index].VariableBitRate = true
	}
	ranked := Rank([]Candidate{variable}, Query{})

	kept, refused := Prefer(ranked, Preferences{FormatMinimumBitRate: map[string]int{"mp3": 320}})

	if len(kept) != 0 {
		t.Fatalf("kept %d candidates, want the average below the floor refused", len(kept))
	}
	if len(refused) != 1 || !strings.Contains(refused[0].Reason, "245 kbps") {
		t.Fatalf("refused = %#v, want the average named", refused)
	}
}

// A folder's bitrate is an average, and a folder averaging 320 kbps can hold a
// track at 128. A want asks for one file, so one file is what RefuseFile
// measures.
func TestOneFileUnderTheFloorIsRefusedThoughItsFolderPassed(t *testing.T) {
	preferences := Preferences{FormatMinimumBitRate: map[string]int{"mp3": 320}}

	reason, kind := preferences.RefuseFile("mp3", 128)

	if reason == "" || kind != RefusedBitRate {
		t.Fatalf("RefuseFile(mp3, 128) = %q, %q; want it refused by the floor", reason, kind)
	}
}

func TestOneFileAtTheFloorIsKept(t *testing.T) {
	preferences := Preferences{FormatMinimumBitRate: map[string]int{"mp3": 320}}

	if reason, _ := preferences.RefuseFile("mp3", 320); reason != "" {
		t.Fatalf("RefuseFile(mp3, 320) = %q, want it kept", reason)
	}
}

func TestOneLosslessFileIsLeftAloneByAFloor(t *testing.T) {
	preferences := Preferences{MinimumBitRate: 320}

	if reason, _ := preferences.RefuseFile("flac", 0); reason != "" {
		t.Fatalf("RefuseFile(flac, 0) = %q, want a lossless file left alone", reason)
	}
}

// BelowFloor is what the upgrade sweep asks about a library file. Unlike
// RefuseFile, a file of no stated bitrate is silence rather than a refusal:
// nobody has read it, which says nothing about whether it is worth replacing.
func TestBelowFloorNeverFlagsAFileOfNoStatedBitRate(t *testing.T) {
	preferences := Preferences{MinimumBitRate: 320}

	if preferences.BelowFloor("mp3", 0) {
		t.Fatal("a file of no stated bitrate was read as below the floor")
	}
}

func TestBelowFloorNeverFlagsALosslessFile(t *testing.T) {
	preferences := Preferences{MinimumBitRate: 1411}

	if preferences.BelowFloor("flac", 900) {
		t.Fatal("a lossless file was read as below the floor")
	}
}

func TestBelowFloorFlagsALossyFileUnderTheFloor(t *testing.T) {
	preferences := Preferences{MinimumBitRate: 320}

	if !preferences.BelowFloor("mp3", 192) {
		t.Fatal("a file under the floor was not flagged")
	}
}

func TestBelowFloorLeavesAFileAtTheFloorAlone(t *testing.T) {
	preferences := Preferences{MinimumBitRate: 320}

	if preferences.BelowFloor("mp3", 320) {
		t.Fatal("a file at the floor was flagged as below it")
	}
}

func TestOneFileOfNoStatedBitRateIsRefusedByAFloor(t *testing.T) {
	preferences := Preferences{FormatMinimumBitRate: map[string]int{"mp3": 320}}

	if reason, _ := preferences.RefuseFile("mp3", 0); reason == "" {
		t.Fatal("a file of no stated bitrate met a floor")
	}
}

// A lossy copy below the floor does leave, and says by how much.
func TestALossyCopyBelowTheFloorIsTakenOut(t *testing.T) {
	ranked := Rank([]Candidate{candidate("peer", "mp3", 10, 192)}, Query{})

	kept, refused := Prefer(ranked, Preferences{MinimumBitRate: 320})

	if len(kept) != 0 {
		t.Fatalf("kept %d candidates, want the copy below the floor gone", len(kept))
	}
	if len(refused) != 1 || !strings.Contains(refused[0].Reason, "192 kbps") {
		t.Fatalf("refused = %#v, want the bitrate named", refused)
	}
}

// An installation that never touches the setting behaves exactly as it did
// before the setting existed.
func TestSayingNothingChangesNothing(t *testing.T) {
	ranked := Rank([]Candidate{
		candidate("lossy", "mp3", 10, 320),
		candidate("lossless", "flac", 10, 0),
	}, Query{})
	before := usernames(ranked)
	scores := []float64{ranked[0].Score, ranked[1].Score}

	kept, refused := Prefer(ranked, Preferences{})

	if len(refused) != 0 {
		t.Fatalf("refused %d candidates with nothing said", len(refused))
	}
	after := usernames(kept)
	if after[0] != before[0] || after[1] != before[1] {
		t.Fatalf("order = %v, want %v", after, before)
	}
	if kept[0].Score != scores[0] || kept[1].Score != scores[1] {
		t.Fatalf("scores moved to %v and %v", kept[0].Score, kept[1].Score)
	}
}

// Corroboration is what the music is, and that was never a matter of taste.
// Laying a preference over a ranking must leave it exactly as it was.
func TestAPreferenceDoesNotTouchWhatCorroboratedACandidate(t *testing.T) {
	ranked := Rank([]Candidate{candidate("peer", "mp3", 2, 320)}, Query{
		ExpectedTrackCount: 2,
		Tracks: []QueryTrack{
			{Title: "track", DurationSeconds: 0},
			{Title: "track", DurationSeconds: 0},
		},
	})
	was := ranked[0].Match

	kept, _ := Prefer(ranked, Preferences{Preferred: []string{"mp3"}})

	if kept[0].Match != was {
		t.Fatalf("match = %#v, want %#v", kept[0].Match, was)
	}
}

package sources

import (
	"strings"
	"testing"
)

func candidate(username, format string, files int, bitRate int, options ...func(*Candidate)) Candidate {
	result := Candidate{Provider: "slskd", Username: username, Directory: "Music\\" + username}
	for index := 0; index < files; index++ {
		result.Files = append(result.Files, File{
			Name:      "track.",
			Extension: format,
			SizeBytes: 10 << 20,
			BitRate:   bitRate,
		})
	}
	for _, option := range options {
		option(&result)
	}
	result.Summarize()
	return result
}

func freeSlot(target *Candidate) { target.FreeUploadSlot = true }

func TestRankPrefersLosslessCompleteAndAvailable(t *testing.T) {
	ranked := Rank([]Candidate{
		candidate("low", "mp3", 12, 128, freeSlot),
		candidate("mp3-320", "mp3", 12, 320, freeSlot),
		candidate("flac-complete", "flac", 12, 0, freeSlot),
		candidate("flac-partial", "flac", 6, 0, freeSlot),
	}, Query{Text: "portishead dummy", ExpectedTrackCount: 12})

	if len(ranked) != 4 {
		t.Fatalf("ranked %d candidates, want 4", len(ranked))
	}
	want := []string{"flac-complete", "mp3-320", "flac-partial", "low"}
	for index, username := range want {
		if ranked[index].Username != username {
			t.Fatalf("rank %d = %s, want %s (order %v)", index, ranked[index].Username, username, usernames(ranked))
		}
	}
	if ranked[0].Score <= ranked[1].Score {
		t.Fatalf("best score %v is not above %v", ranked[0].Score, ranked[1].Score)
	}
	if ranked[0].Score > 1 {
		t.Fatalf("score %v exceeds 1", ranked[0].Score)
	}
}

func TestRankExplainsEveryCandidate(t *testing.T) {
	ranked := Rank([]Candidate{candidate("peer", "flac", 10, 0, freeSlot)},
		Query{ExpectedTrackCount: 10})

	reasons := strings.Join(ranked[0].Reasons, " | ")
	for _, want := range []string{"Lossless FLAC", "All 10 catalogue tracks present", "Free upload slot"} {
		if !strings.Contains(reasons, want) {
			t.Fatalf("reasons %q do not explain %q", reasons, want)
		}
	}
}

func TestRankPenalisesQueuedPeers(t *testing.T) {
	ranked := Rank([]Candidate{
		candidate("queued", "flac", 10, 0, func(target *Candidate) { target.QueueLength = 40 }),
		candidate("available", "flac", 10, 0, freeSlot),
	}, Query{ExpectedTrackCount: 10})

	if ranked[0].Username != "available" {
		t.Fatalf("order = %v, want the peer with a free slot first", usernames(ranked))
	}
}

// Real searches returned both a folder holding a few bonus tracks and one
// holding twice the release, and the flat oversupply score ranked them alike.
func TestRankGradesOversuppliedFolders(t *testing.T) {
	ranked := Rank([]Candidate{
		candidate("discography", "flac", 22, 0, freeSlot),
		candidate("bonus-tracks", "flac", 13, 0, freeSlot),
		candidate("exact", "flac", 11, 0, freeSlot),
		candidate("deluxe", "flac", 15, 0, freeSlot),
	}, Query{ExpectedTrackCount: 11})

	want := []string{"exact", "bonus-tracks", "deluxe", "discography"}
	for index, username := range want {
		if ranked[index].Username != username {
			t.Fatalf("rank %d = %s, want %s (order %v)", index, ranked[index].Username, username, usernames(ranked))
		}
	}
	if reasons := strings.Join(ranked[3].Reasons, " | "); !strings.Contains(reasons, "likely a different edition") {
		t.Fatalf("reasons %q do not explain the heavy oversupply", reasons)
	}
}

// A folder holding twice the release needs sorting out by hand afterwards, so
// an exact lossy match is worth more than an oversupplied lossless dump.
func TestRankPrefersAnExactLossyFolderOverADiscographyDump(t *testing.T) {
	ranked := Rank([]Candidate{
		candidate("flac-dump", "flac", 30, 0, freeSlot),
		candidate("mp3-exact", "mp3", 11, 320, freeSlot),
	}, Query{ExpectedTrackCount: 11})

	if ranked[0].Username != "mp3-exact" {
		t.Fatalf("order = %v, want the exact folder first", usernames(ranked))
	}
}

// The score measures how good a copy is and nothing about what the music is,
// so a pristine folder of the wrong release outscored a modest folder of the
// right one. Corroboration is what settles that, and it has to outrank score.
func TestRankPrefersACorroboratedFolderOverABetterScoringOne(t *testing.T) {
	right := offered(File{Name: "01 Cyberia lyr3.mp3", Extension: "mp3", BitRate: 320,
		SizeBytes: 5 << 20, DurationSeconds: 233})
	right.Username = "matches"
	right.Summarize()

	wrong := offered(audio("01 A Completely Different Track.flac", 415))
	wrong.Username = "lossless"
	wrong.FreeUploadSlot = true
	wrong.Summarize()

	ranked := Rank([]Candidate{wrong, right},
		wanted(QueryTrack{Title: "Cyberia lyr3", DurationSeconds: 233}))

	if ranked[0].Username != "matches" {
		t.Fatalf("order = %v, want the corroborated folder first", usernames(ranked))
	}
	if ranked[0].Score >= ranked[1].Score {
		t.Fatal("the test no longer proves corroboration beats score: the winner also scores higher")
	}
}

func TestRankRecordsTheMatchOnEveryCandidate(t *testing.T) {
	ranked := Rank([]Candidate{offered(audio("01 Newlove.flac", 190))},
		wanted(QueryTrack{Title: "Newlove", DurationSeconds: 190}))

	if !ranked[0].Match.Complete() {
		t.Fatalf("match = %+v, want the ranked candidate to carry its evidence", ranked[0].Match)
	}
}

func TestRankIsDeterministicForEqualCandidates(t *testing.T) {
	first := Rank([]Candidate{
		candidate("zoe", "flac", 10, 0, freeSlot),
		candidate("adam", "flac", 10, 0, freeSlot),
	}, Query{ExpectedTrackCount: 10})
	second := Rank([]Candidate{
		candidate("adam", "flac", 10, 0, freeSlot),
		candidate("zoe", "flac", 10, 0, freeSlot),
	}, Query{ExpectedTrackCount: 10})

	if usernames(first)[0] != "adam" || usernames(second)[0] != "adam" {
		t.Fatalf("tie-break is not stable: %v then %v", usernames(first), usernames(second))
	}
}

func TestRankDropsEmptyCandidates(t *testing.T) {
	if ranked := Rank([]Candidate{{Username: "peer"}}, Query{}); len(ranked) != 0 {
		t.Fatalf("ranked %d candidates, want none", len(ranked))
	}
}

// Within one lossy format the bitrate is the whole of the quality question, and
// the order it produces is the order the reasons claim.
func TestRankOrdersLossyFoldersByBitrate(t *testing.T) {
	ranked := Rank([]Candidate{
		candidate("mp3-192", "mp3", 10, 192, freeSlot),
		candidate("mp3-320", "mp3", 10, 320, freeSlot),
		candidate("mp3-128", "mp3", 10, 128, freeSlot),
		candidate("mp3-256", "mp3", 10, 256, freeSlot),
	}, Query{ExpectedTrackCount: 10})

	want := []string{"mp3-320", "mp3-256", "mp3-192", "mp3-128"}
	for index, username := range want {
		if ranked[index].Username != username {
			t.Fatalf("rank %d = %s, want %s (order %v)", index, ranked[index].Username, username, usernames(ranked))
		}
	}
}

// A bitrate the peer did not disclose is not a bad bitrate, but it is not a good
// one either, and the reason has to say which of the two it is.
func TestRankAdmitsWhenNobodyDisclosedTheBitrate(t *testing.T) {
	ranked := Rank([]Candidate{
		candidate("undisclosed", "mp3", 10, 0, freeSlot),
		candidate("disclosed", "mp3", 10, 320, freeSlot),
	}, Query{ExpectedTrackCount: 10})

	if ranked[0].Username != "disclosed" {
		t.Fatalf("order = %v, want the folder that said what it is first", usernames(ranked))
	}
	if reasons := strings.Join(ranked[1].Reasons, " | "); !strings.Contains(reasons, "unknown bitrate") {
		t.Fatalf("reasons %q do not admit the bitrate is unknown", reasons)
	}
}

// A folder whose format nobody could work out is scored as the unknown it is
// rather than as the worst thing it might be.
func TestRankAdmitsWhenTheFormatCouldNotBeWorkedOut(t *testing.T) {
	unknown := Candidate{Username: "unknown", Files: []File{{SizeBytes: 5 << 20}}}
	unknown.Summarize()

	ranked := Rank([]Candidate{unknown}, Query{ExpectedTrackCount: 1})

	if reasons := strings.Join(ranked[0].Reasons, " | "); !strings.Contains(reasons, "could not be determined") {
		t.Fatalf("reasons %q do not admit the format is unknown", reasons)
	}
}

// A folder one or two tracks short is a release somebody can nearly listen to;
// one missing most of itself is not.
func TestRankPrefersAnAlmostCompleteFolderToAMostlyEmptyOne(t *testing.T) {
	ranked := Rank([]Candidate{
		candidate("half", "flac", 5, 0, freeSlot),
		candidate("nearly", "flac", 9, 0, freeSlot),
	}, Query{ExpectedTrackCount: 11})

	if ranked[0].Username != "nearly" {
		t.Fatalf("order = %v, want the nearly complete folder first", usernames(ranked))
	}
	if reasons := strings.Join(ranked[0].Reasons, " | "); !strings.Contains(reasons, "Missing 2 tracks of 11") {
		t.Fatalf("reasons %q do not say what is missing", reasons)
	}
}

// Without a catalogue track count there is nothing to be complete against, so
// the candidate is judged on the copy alone and the reason says only what is on
// offer.
func TestRankJudgesACopyAloneWhenNothingSaysHowManyTracksThereShouldBe(t *testing.T) {
	ranked := Rank([]Candidate{candidate("peer", "flac", 1, 0, freeSlot)}, Query{})

	if reasons := strings.Join(ranked[0].Reasons, " | "); !strings.Contains(reasons, "1 track offered") {
		t.Fatalf("reasons %q, want the offer stated without a claim about completeness", reasons)
	}
}

// A folder holding the same release twice over in two formats is a folder
// somebody has to sort out by hand.
func TestRankPenalisesAFolderThatMixesFormats(t *testing.T) {
	mixed := Candidate{Username: "mixed", FreeUploadSlot: true, Files: []File{
		{Extension: "flac", SizeBytes: 30 << 20},
		{Extension: "flac", SizeBytes: 30 << 20},
		{Extension: "mp3", SizeBytes: 8 << 20, BitRate: 320},
	}}
	mixed.Summarize()
	consistent := candidate("consistent", "flac", 3, 0, freeSlot)

	ranked := Rank([]Candidate{mixed, consistent}, Query{ExpectedTrackCount: 3})

	if ranked[0].Username != "consistent" {
		t.Fatalf("order = %v, want the folder of one format first", usernames(ranked))
	}
	if reasons := strings.Join(ranked[1].Reasons, " | "); !strings.Contains(reasons, "mixes several audio formats") {
		t.Fatalf("reasons %q do not explain the mixture", reasons)
	}
}

// The speed is reported in the units a person reads it in, so a peer serving
// kilobytes is not shown as 0.0 MB/s.
func TestRankReportsUploadSpeedInReadableUnits(t *testing.T) {
	for _, testCase := range []struct {
		name  string
		speed int64
		want  string
	}{
		{"megabytes a second", 4 << 20, "4.0 MB/s"},
		{"kilobytes a second", 100 << 10, "100 kB/s"},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			ranked := Rank([]Candidate{
				candidate("peer", "flac", 10, 0, freeSlot, func(target *Candidate) {
					target.UploadSpeed = testCase.speed
				}),
			}, Query{ExpectedTrackCount: 10})

			if reasons := strings.Join(ranked[0].Reasons, " | "); !strings.Contains(reasons, testCase.want) {
				t.Fatalf("reasons %q do not report %q", reasons, testCase.want)
			}
		})
	}
}

// Past the point where an album downloads as fast as anybody cares about, more
// speed is not a better copy.
func TestRankStopsRewardingSpeedOnceItSaturates(t *testing.T) {
	ranked := Rank([]Candidate{
		candidate("fast", "flac", 10, 0, freeSlot, func(target *Candidate) {
			target.UploadSpeed = fastUploadSpeed
		}),
		candidate("faster", "flac", 10, 0, freeSlot, func(target *Candidate) {
			target.UploadSpeed = 10 * fastUploadSpeed
		}),
	}, Query{ExpectedTrackCount: 10})

	if ranked[0].Score != ranked[1].Score {
		t.Fatalf("scores %v and %v differ, want speed past saturation to count for nothing",
			ranked[0].Score, ranked[1].Score)
	}
}

// The weights sum to one so a score reads as a fraction of the best possible
// candidate. The best possible candidate has to actually reach one, and nothing
// may exceed it.
func TestThePerfectCandidateScoresExactlyOne(t *testing.T) {
	ranked := Rank([]Candidate{
		candidate("perfect", "flac", 10, 0, freeSlot, func(target *Candidate) {
			target.UploadSpeed = 10 * fastUploadSpeed
		}),
	}, Query{ExpectedTrackCount: 10})

	if ranked[0].Score != 1 {
		t.Fatalf("score = %v, want exactly 1", ranked[0].Score)
	}
}

// Corroboration outranks score, and within corroboration a folder that partly
// answers outranks one that does not answer at all.
func TestRankPrefersPartialCorroborationToNone(t *testing.T) {
	named := offered(File{Name: "01 Newlove.flac", Extension: "flac", SizeBytes: 10 << 20})
	named.Username = "named"
	named.Summarize()
	nothing := offered(audio("01 Something Else.flac", 999))
	nothing.Username = "nothing"
	nothing.FreeUploadSlot = true
	nothing.Summarize()

	ranked := Rank([]Candidate{nothing, named},
		wanted(QueryTrack{Title: "Newlove", DurationSeconds: 190}))

	if ranked[0].Username != "named" {
		t.Fatalf("order = %v, want the partly corroborated folder first", usernames(ranked))
	}
}

// Two folders nothing tells apart are ordered by how much of the release each
// holds, so the fuller one is offered first.
func TestRankPrefersTheFullerOfTwoOtherwiseEqualFolders(t *testing.T) {
	ranked := Rank([]Candidate{
		candidate("sparse", "flac", 3, 0, freeSlot),
		candidate("fuller", "flac", 8, 0, freeSlot),
	}, Query{})

	if ranked[0].Username != "fuller" {
		t.Fatalf("order = %v, want the fuller folder first", usernames(ranked))
	}
}

// One peer offering the same release in two folders is ordered by folder name,
// so the same search always presents the same one first.
func TestRankBreaksATieBetweenOnePeersFoldersByName(t *testing.T) {
	first := candidate("peer", "flac", 10, 0, freeSlot)
	first.Directory = `Music\A`
	second := candidate("peer", "flac", 10, 0, freeSlot)
	second.Directory = `Music\B`

	ranked := Rank([]Candidate{second, first}, Query{ExpectedTrackCount: 10})

	if ranked[0].Directory != `Music\A` {
		t.Fatalf("order = %v, want the folders ordered by name", []string{
			ranked[0].Directory, ranked[1].Directory,
		})
	}
}

// Two formats equally represented is a folder with no dominant format at all, so
// the choice has to be stable rather than whatever the map iterated first.
func TestSummarizeBreaksAFormatTieAlphabetically(t *testing.T) {
	for range 10 {
		target := Candidate{Files: []File{
			{Extension: "mp3", SizeBytes: 100, BitRate: 320},
			{Extension: "flac", SizeBytes: 200},
		}}
		target.Summarize()

		if target.Format != "flac" {
			t.Fatalf("format = %q, want the tie broken alphabetically every time", target.Format)
		}
	}
}

func TestSummarizeOmitsBitrateForLosslessFormats(t *testing.T) {
	target := Candidate{Files: []File{
		{Extension: "flac", SizeBytes: 100, BitRate: 320},
		{Extension: "flac", SizeBytes: 200, BitRate: 1411},
	}}
	target.Summarize()

	if target.Format != "flac" {
		t.Fatalf("format = %q, want flac", target.Format)
	}
	if target.AverageBitRate != 0 {
		t.Fatalf("average bitrate = %d, want 0 for a lossless folder", target.AverageBitRate)
	}
}

func TestSummarizeUsesDominantFormatAndAverageBitrate(t *testing.T) {
	target := Candidate{Files: []File{
		{Extension: ".MP3", SizeBytes: 100, BitRate: 320},
		{Extension: "mp3", SizeBytes: 200, BitRate: 256},
		{Name: "cover.flac", Extension: "", SizeBytes: 300},
	}}
	target.Summarize()

	if target.Format != "mp3" {
		t.Fatalf("format = %q, want mp3", target.Format)
	}
	if target.AverageBitRate != 288 {
		t.Fatalf("average bitrate = %d, want 288", target.AverageBitRate)
	}
	if target.TotalSizeBytes != 600 {
		t.Fatalf("total size = %d, want 600", target.TotalSizeBytes)
	}
	if target.Files[2].Extension != "flac" {
		t.Fatalf("extension fallback = %q, want flac", target.Files[2].Extension)
	}
}

func usernames(candidates []Candidate) []string {
	result := make([]string, 0, len(candidates))
	for _, item := range candidates {
		result = append(result, item.Username)
	}
	return result
}

package sources

import "testing"

func offered(files ...File) Candidate {
	result := Candidate{Provider: "slskd", Username: "peer", Directory: "Music\\peer"}
	for _, file := range files {
		if file.Extension == "" {
			file.Extension = "flac"
		}
		result.Files = append(result.Files, file)
	}
	result.Summarize()
	return result
}

func audio(name string, seconds int) File {
	return File{Name: name, Extension: "flac", SizeBytes: 10 << 20, DurationSeconds: seconds}
}

func wanted(tracks ...QueryTrack) Query {
	return Query{Text: "query", ExpectedTrackCount: len(tracks), Tracks: tracks}
}

func TestCorroborateConfirmsATrackMatchedByNameAndLength(t *testing.T) {
	match := Corroborate(
		offered(audio("Sewerslvt - Draining Love Story - 08 Mr. Kill Myself.flac", 214)),
		wanted(QueryTrack{Title: "Mr. Kill Myself", DurationSeconds: 214}),
	)

	if match.Confirmed != 1 || !match.Complete() {
		t.Fatalf("match = %+v, want one confirmed track", match)
	}
}

func TestCorroborateTreatsAMatchingNameWithTheWrongLengthAsAbsent(t *testing.T) {
	match := Corroborate(
		offered(audio("01 Not the Essential Mix.flac", 356)),
		wanted(QueryTrack{Title: "Not the Essential Mix", DurationSeconds: 7198}),
	)

	if match.Confirmed != 0 || match.Partial != 0 || match.Absent != 1 {
		t.Fatalf("match = %+v, want the length disagreement to void the name", match)
	}
	if match.Complete() {
		t.Fatal("a two-hour set matched to a six-minute file must not count as complete")
	}
}

// Peers drop punctuation that MusicBrainz keeps, and the search normalizer
// already joins such words back together. Corroboration has to agree with it,
// or a correct folder looks like a different release.
func TestCorroborateIgnoresApostropheSpelling(t *testing.T) {
	match := Corroborate(
		offered(audio("Sewerslvt - IRLY - 02 Euphoric Filth (Cherus Theme).flac", 246)),
		wanted(QueryTrack{Title: "Euphoric Filth (Cheru’s Theme)", DurationSeconds: 246}),
	)

	if match.Confirmed != 1 {
		t.Fatalf("match = %+v, want the typographic apostrophe to be ignored", match)
	}
}

// A discography dump would otherwise corroborate perfectly against any album
// in it, because every file carries the artist's name.
func TestCorroborateDoesNotLetOneFileStandForTwoTracks(t *testing.T) {
	match := Corroborate(
		offered(audio("Cynthoni - Lexapro Doesnt Work.flac", 443)),
		wanted(
			QueryTrack{Title: "Lexapro Doesn’t Work", DurationSeconds: 443},
			QueryTrack{Title: "Lexapro Doesn’t Work (slowed + reverb)", DurationSeconds: 500},
		),
	)

	if match.Confirmed != 1 || match.Absent != 1 {
		t.Fatalf("match = %+v, want one confirmed and one absent", match)
	}
	if match.Complete() {
		t.Fatal("a folder holding half the release must not count as complete")
	}
}

// Length alone is weak evidence: short tracks collide constantly. It is worth
// reporting, but never worth confirming on.
func TestCorroborateTreatsLengthAloneAsPartial(t *testing.T) {
	match := Corroborate(
		offered(audio("02 Something Else Entirely.flac", 214)),
		wanted(QueryTrack{Title: "Mr. Kill Myself", DurationSeconds: 214}),
	)

	if match.Confirmed != 0 || match.Partial != 1 {
		t.Fatalf("match = %+v, want length alone to be partial evidence", match)
	}
}

func TestCorroborateReportsNothingWhenTheQueryCarriesNoTracks(t *testing.T) {
	match := Corroborate(offered(audio("anything.flac", 100)), Query{Text: "query"})

	if match.Known() {
		t.Fatalf("match = %+v, want an unchecked match when there is nothing to check against", match)
	}
}

func TestCorroborateCountsUnmatchedCatalogueTracksAsAbsent(t *testing.T) {
	match := Corroborate(
		offered(audio("01 First.flac", 100)),
		wanted(
			QueryTrack{Title: "First", DurationSeconds: 100},
			QueryTrack{Title: "Second", DurationSeconds: 200},
			QueryTrack{Title: "Third", DurationSeconds: 300},
		),
	)

	if match.Confirmed != 1 || match.Absent != 2 || match.Expected != 3 {
		t.Fatalf("match = %+v, want one of three confirmed and two absent", match)
	}
}

func TestCorroborateAllowsASmallLengthDifference(t *testing.T) {
	match := Corroborate(
		offered(audio("01 Miss the Rage.flac", 160)),
		wanted(QueryTrack{Title: "Miss the Rage", DurationSeconds: 154}),
	)

	if match.Confirmed != 1 {
		t.Fatalf("match = %+v, want six seconds of fade to be tolerated", match)
	}
}

// Masters differ by a second or two of lead-in or fade and peers round, so the
// tolerance is real; twenty seconds apart is a different edit, so it has an
// edge. Both sides of that edge are the contract.
func TestCorroborateConfirmsRightUpToTheLengthTolerance(t *testing.T) {
	match := Corroborate(
		offered(audio("01 Miss the Rage.flac", 154+durationTolerance)),
		wanted(QueryTrack{Title: "Miss the Rage", DurationSeconds: 154}),
	)

	if match.Confirmed != 1 {
		t.Fatalf("match = %+v, want the tolerance itself to still confirm", match)
	}
}

func TestCorroborateWithholdsConfirmationOneSecondPastTheTolerance(t *testing.T) {
	match := Corroborate(
		offered(audio("01 Miss the Rage.flac", 154+durationTolerance+1)),
		wanted(QueryTrack{Title: "Miss the Rage", DurationSeconds: 154}),
	)

	if match.Confirmed != 0 || match.Partial != 0 || match.Absent != 1 {
		t.Fatalf("match = %+v, want a second past the tolerance to void the name", match)
	}
}

// A length the peer did not disclose is silence, not agreement: a file of
// unknown length must never confirm a track by running to it.
func TestCorroborateTreatsAnUndisclosedLengthAsSilence(t *testing.T) {
	match := Corroborate(
		offered(File{Name: "01 Miss the Rage.flac", Extension: "flac"}),
		wanted(QueryTrack{Title: "Miss the Rage", DurationSeconds: 154}),
	)

	if match.Confirmed != 0 || match.Partial != 1 {
		t.Fatalf("match = %+v, want a file of unknown length to confirm nothing", match)
	}
}

// A catalogue track with no length recorded cannot be confirmed by length
// either, whatever the file discloses.
func TestCorroborateTreatsAnUnrecordedCatalogueLengthAsSilence(t *testing.T) {
	match := Corroborate(
		offered(audio("01 Miss the Rage.flac", 154)),
		wanted(QueryTrack{Title: "Miss the Rage"}),
	)

	if match.Confirmed != 0 || match.Partial != 1 {
		t.Fatalf("match = %+v, want a track of unrecorded length to confirm nothing", match)
	}
}

// The summary is what the interface shows, and it names only the counts that are
// not zero: "0 partial" reads as though there were something partial to look at.
func TestMatchSummaryNamesOnlyTheCountsThatAreNotZero(t *testing.T) {
	for _, testCase := range []struct {
		name  string
		match Match
		want  string
	}{
		{"nothing was checked", Match{}, "Not checked against the catalogue"},
		{"one track confirmed", Match{Expected: 1, Confirmed: 1},
			"Track confirmed by name and length"},
		{"every track confirmed", Match{Expected: 3, Confirmed: 3},
			"All 3 tracks confirmed by name and length"},
		{"nothing matched at all", Match{Expected: 3, Absent: 3},
			"Nothing here matches the 3 tracks"},
		{"nothing matched one track", Match{Expected: 1, Absent: 1},
			"Nothing here matches the 1 track"},
		{"some confirmed, some missing", Match{Expected: 3, Confirmed: 2, Absent: 1},
			"2 of 3 confirmed, 1 missing"},
		{"some confirmed, some partial", Match{Expected: 3, Confirmed: 1, Partial: 2},
			"1 of 3 confirmed, 2 partial"},
		{"a bit of everything", Match{Expected: 4, Confirmed: 1, Partial: 1, Absent: 2},
			"1 of 4 confirmed, 1 partial, 2 missing"},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			if got := testCase.match.Summary(); got != testCase.want {
				t.Fatalf("Summary() = %q, want %q", got, testCase.want)
			}
		})
	}
}

// The summary says what the peer disclosed and never that the file is the
// recording, because nobody has heard it.
func TestOfferSummarySaysWhatThePeerDisclosedAndNoMore(t *testing.T) {
	for _, testCase := range []struct {
		name  string
		offer Offer
		want  string
	}{
		{"both fit", Offer{Confirmed: true, Named: true},
			"Named and timed like the wanted recording"},
		{"one of the two fits", Offer{Partial: true},
			"Either the name or the length fits, not both"},
		{"the name fits and the length contradicts it", Offer{Named: true, LengthDiffers: true},
			"Named like the wanted recording, but the peer says it runs to another length"},
		{"only the length was disclosed and it contradicts", Offer{LengthDiffers: true},
			"The peer says it runs to another length"},
		{"neither fits", Offer{},
			"Nothing the peer disclosed fits; it has not been heard either"},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			if got := testCase.offer.Summary(); got != testCase.want {
				t.Fatalf("Summary() = %q, want %q", got, testCase.want)
			}
		})
	}
}

// Two copies nothing tells apart are ordered by the one thing that cannot
// change between runs, so the same search always fetches the same bytes.
func TestOffersForBreaksARemainingTieByRemotePath(t *testing.T) {
	offers := OffersFor([]Candidate{{Username: "peer", Score: 0.5, Files: []File{
		{Path: `z.flac`, Name: "Candy from Strangers.flac", DurationSeconds: 487},
		{Path: `a.flac`, Name: "Candy from Strangers.flac", DurationSeconds: 487},
	}}}, QueryTrack{Title: "Candy from Strangers", DurationSeconds: 487})

	if len(offers) != 2 || offers[0].File.Path != "a.flac" {
		t.Fatalf("offers = %+v, want the remaining tie broken by path", offers)
	}
}

// A want with no title to read is still offered every file, because the audio is
// what decides and nobody has heard it. Nothing may be claimed about any of them.
func TestOffersForClaimsNothingAboutAWantWithNoTitle(t *testing.T) {
	offers := OffersFor([]Candidate{{Username: "peer", Files: []File{
		{Path: `a.flac`, Name: "anything at all.flac", DurationSeconds: 487},
	}}}, QueryTrack{DurationSeconds: 0})

	if len(offers) != 1 {
		t.Fatalf("offers = %+v, want the file still offered", offers)
	}
	if offers[0].Named || offers[0].Confirmed || offers[0].Partial {
		t.Fatalf("offer = %+v, want nothing claimed from a title nobody wrote down", offers[0])
	}
}

// What a peer discloses about a file is a filename and a length. Both fitting is
// the best reading order there is, and it is still only reading order — the file
// is checked against the recording once its bytes exist.
func TestOffersForPutsTheBestReadingOrderFirst(t *testing.T) {
	offers := OffersFor([]Candidate{{
		Provider: "slskd", Username: "peer", Directory: `Music\Anetha`, Score: 0.4,
		Files: []File{
			{Path: `Music\Anetha\01 Something Else.flac`, Name: "01 Something Else.flac", DurationSeconds: 200},
			{Path: `Music\Anetha\03 Candy from Strangers.flac`, Name: "03 Candy from Strangers.flac", DurationSeconds: 487},
			{Path: `Music\Anetha\04 Candy from Strangers (edit).flac`, Name: "04 Candy from Strangers (edit).flac"},
		},
	}}, QueryTrack{Title: "Candy from Strangers", DurationSeconds: 487})

	if len(offers) != 3 {
		t.Fatalf("offers = %d, want every file on offer", len(offers))
	}
	if !offers[0].Confirmed || offers[0].File.Name != "03 Candy from Strangers.flac" {
		t.Fatalf("first = %+v, want the name and the length fitting", offers[0])
	}
	if !offers[1].Partial || offers[1].File.Name != "04 Candy from Strangers (edit).flac" {
		t.Fatalf("second = %+v, want the name alone", offers[1])
	}
	if offers[2].Confirmed || offers[2].Partial {
		t.Fatalf("third = %+v, want nothing claimed for it", offers[2])
	}
}

// A copy nothing recommends is still a copy. Only the audio can rule one out, and
// nobody has heard it yet, so it is offered last rather than dropped.
func TestOffersForKeepsFilesNothingRecommends(t *testing.T) {
	offers := OffersFor([]Candidate{{
		Username: "peer",
		Files:    []File{{Path: `Music\track.flac`, Name: "track.flac"}},
	}}, QueryTrack{Title: "Candy from Strangers", DurationSeconds: 487})

	if len(offers) != 1 {
		t.Fatalf("offers = %+v, want the file kept", offers)
	}
}

// A peer serves the exact name it advertised, so a file with no path is one
// nothing could ever ask for.
func TestOffersForDropsFilesNothingCouldFetch(t *testing.T) {
	offers := OffersFor([]Candidate{{
		Username: "peer",
		Files:    []File{{Name: "no path at all.flac", DurationSeconds: 487}},
	}}, QueryTrack{Title: "Candy from Strangers", DurationSeconds: 487})

	if len(offers) != 0 {
		t.Fatalf("offers = %+v, want nothing fetchable", offers)
	}
}

// Two copies the peers disclose the same amount about are ordered by how good a
// copy each is, and then by name, so the same search always fetches the same one.
func TestOffersForBreaksTiesByQualityThenDeterministically(t *testing.T) {
	offers := OffersFor([]Candidate{
		{Username: "slow", Score: 0.2, Files: []File{
			{Path: `a.flac`, Name: "Candy from Strangers.flac", DurationSeconds: 487},
		}},
		{Username: "fast", Score: 0.9, Files: []File{
			{Path: `b.flac`, Name: "Candy from Strangers.flac", DurationSeconds: 487},
		}},
	}, QueryTrack{Title: "Candy from Strangers", DurationSeconds: 487})

	if len(offers) != 2 || offers[0].Username != "fast" {
		t.Fatalf("offers = %+v, want the better copy first", offers)
	}
}

// What a peer disclosed about the recording outranks how good their copy
// otherwise looks. A low-scoring copy that is named and timed right is read
// before a high-scoring copy only its name fits, which is read before a
// higher-scoring copy still that fits nothing at all.
func TestOffersForRanksDisclosedEvidenceAboveQuality(t *testing.T) {
	offers := OffersFor([]Candidate{
		{Username: "confirmed", Score: 0.1, Files: []File{
			{Path: "a.flac", Name: "Candy from Strangers.flac", DurationSeconds: 487},
		}},
		{Username: "partial", Score: 0.9, Files: []File{
			{Path: "b.flac", Name: "Candy from Strangers (edit).flac"},
		}},
		{Username: "undisclosed", Score: 0.99, Files: []File{
			{Path: "c.flac", Name: "Unrelated Track.flac", DurationSeconds: 10},
		}},
	}, QueryTrack{Title: "Candy from Strangers", DurationSeconds: 487})

	if len(offers) != 3 {
		t.Fatalf("offers = %d, want all three kept", len(offers))
	}
	if offers[0].Username != "confirmed" {
		t.Fatalf("first = %+v, want the confirmed copy despite the lower score", offers[0])
	}
	if offers[1].Username != "partial" {
		t.Fatalf("second = %+v, want the partial copy ahead of the undisclosed one", offers[1])
	}
	if offers[2].Username != "undisclosed" {
		t.Fatalf("third = %+v, want the highest-scoring copy last; nothing disclosed fits it", offers[2])
	}
}

// The want on 2026-09-11 was a 160-second track. Twenty peers offered a file
// named for it, and every one disclosed 243 seconds. The name alone had each of
// them fetched in turn. A disclosed length outside the tolerance is a
// contradiction: it voids the name, and the offer sinks to the tier the
// automatic pick never fetches from. It is still offered, last, for a person.
func TestOffersForLetsADisclosedLengthVoidTheName(t *testing.T) {
	offers := OffersFor([]Candidate{{
		Provider: "slskd", Username: "pertxas", Directory: `archive\Fornika`, Score: 0.9,
		Files: []File{
			{Path: `archive\Fornika\05 - Nikki war nie weg.mp3`, Name: "05 - Nikki war nie weg.mp3", DurationSeconds: 243},
			{Path: `archive\Fornika\06 - Something.mp3`, Name: "06 - Something.mp3", DurationSeconds: 160},
		},
	}}, QueryTrack{Title: "War nie weg", DurationSeconds: 160})

	if len(offers) != 2 {
		t.Fatalf("offers = %d, want both files on offer", len(offers))
	}
	if !offers[0].Partial || offers[0].File.Name != "06 - Something.mp3" {
		t.Fatalf("first = %+v, want the file whose length fits, whatever its name", offers[0])
	}
	named := offers[1]
	if named.Confirmed || named.Partial || !named.Named || !named.LengthDiffers {
		t.Fatalf("second = %+v, want the name voided by the length and said so", named)
	}
}

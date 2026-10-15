package acquisition

import (
	"testing"

	"github.com/pxldi/schall/internal/sources"
)

// The automatic path never fetches an offer whose peer disclosed nothing that
// fits the want. 3,024 of 18,200 fetches ever made were exactly this tier and
// produced zero imports.
func TestDisclosedOffersDropsTheOfferNothingDisclosedFits(t *testing.T) {
	confirmed := sources.Offer{Username: "confirmed", Confirmed: true}
	partial := sources.Offer{Username: "partial", Partial: true}
	nothing := sources.Offer{Username: "nothing"}

	kept := disclosedOffers([]sources.Offer{confirmed, partial, nothing})

	if len(kept) != 2 || kept[0].Username != "confirmed" || kept[1].Username != "partial" {
		t.Fatalf("kept = %+v, want the confirmed and partial offers only", kept)
	}
}

// When every candidate sits in that tier, nothing is left to fetch: the want's
// attempt ends the pass the same way it does when there were no candidates at
// all, rather than fetching something the peer disclosed nothing about.
func TestDisclosedOffersLeavesNothingWhenEverythingIsUndisclosed(t *testing.T) {
	kept := disclosedOffers([]sources.Offer{{Username: "a"}, {Username: "b"}})

	if len(kept) != 0 {
		t.Fatalf("kept = %+v, want nothing left to fetch", kept)
	}
}

// A file named for the want whose peer discloses a length outside the tolerance
// is not fetched by the automatic path. On 2026-09-11 a want for a 160-second
// track fetched five copies of a 243-second song with the same words in its
// name, one every five minutes, each refused by its audio, with twenty more in
// the queue.
func TestDisclosedOffersDropsANamedOfferWhoseLengthContradicts(t *testing.T) {
	offers := sources.OffersFor([]sources.Candidate{{
		Provider: "slskd", Username: "pertxas", Directory: `archive\Fornika`,
		Files: []sources.File{
			{Path: `archive\Fornika\05 - Nikki war nie weg.mp3`, Name: "05 - Nikki war nie weg.mp3", DurationSeconds: 243},
		},
	}}, sources.QueryTrack{Title: "WAR NIE WEG", DurationSeconds: 160})

	if kept := disclosedOffers(offers); len(kept) != 0 {
		t.Fatalf("kept = %+v, want the named file left alone because its length contradicts", kept)
	}
}

// A want that takes only lossless copies keeps the FLAC and refuses the MP3
// and the file of no stated format, each as lossy.
func TestAcceptableOffersKeepsOnlyLosslessFilesForALosslessOnlyWant(t *testing.T) {
	flac := sources.Offer{Username: "flac", File: sources.File{Extension: "flac"}}
	mp3 := sources.Offer{Username: "mp3", File: sources.File{Extension: "mp3", BitRate: 320}}
	unknown := sources.Offer{Username: "unknown"}

	kept, refused := acceptableOffers(
		[]sources.Offer{mp3, flac, unknown}, sources.Preferences{}, lossBar{losslessOnly: true})

	if len(kept) != 1 || kept[0].Username != "flac" {
		t.Fatalf("kept = %+v, want the FLAC only", kept)
	}
	if len(refused) != 2 || refused[0].Kind != sources.RefusedLossy || refused[1].Kind != sources.RefusedLossy {
		t.Fatalf("refused = %+v, want the MP3 and the unknown file refused as lossy", refused)
	}
}

// With re-encoding on, a lossy copy at the re-encode rate stands in for a
// lossless one. One below it, or of no stated rate, is still refused.
func TestAcceptableOffersLetsALossyFileInAtTheReencodeRate(t *testing.T) {
	flac := sources.Offer{Username: "flac", File: sources.File{Extension: "flac"}}
	at := sources.Offer{Username: "at", File: sources.File{Extension: "mp3", BitRate: 320}}
	under := sources.Offer{Username: "under", File: sources.File{Extension: "mp3", BitRate: 256}}
	unrated := sources.Offer{Username: "unrated", File: sources.File{Extension: "mp3"}}

	kept, refused := acceptableOffers([]sources.Offer{flac, at, under, unrated},
		sources.Preferences{}, lossBar{losslessOnly: true, lossyFromKbps: 320})

	if len(kept) != 2 || kept[0].Username != "flac" || kept[1].Username != "at" {
		t.Fatalf("kept = %+v, want the FLAC and the 320 kbps MP3", kept)
	}
	if len(refused) != 2 || refused[0].Kind != sources.RefusedLossy || refused[1].Kind != sources.RefusedLossy {
		t.Fatalf("refused = %+v, want the 256 kbps and the unrated MP3 refused as lossy", refused)
	}
}

package accuraterip

import "testing"

// OK Computer, the 1997 UK CD, as MusicBrainz keeps its disc ID
// rdosqrxASN.ZqF.d.5pPhHtgVyA-. AccurateRip answered for these numbers when
// they were checked on 2026-10-13.
var okComputer = TOC{
	Offsets: []int{150, 21480, 50242, 70320, 90180, 112625, 132247, 141047, 158345, 179735, 196875, 216347},
	Sectors: 240687,
}

func TestTheDiscIDsAreTheOnesAccurateRipFilesTheDiscUnder(t *testing.T) {
	want := IDs{Tracks: 12, ID1: 0x00188a8a, ID2: 0x00e12645, CDDB: 0xa60c870c}
	if got := okComputer.IDs(); got != want {
		t.Fatalf("IDs() = %+v, want %+v", got, want)
	}
}

func TestTheDiscIsLookedUpWhereAccurateRipKeepsIt(t *testing.T) {
	want := "/accuraterip/a/8/a/dBAR-012-00188a8a-00e12645-a60c870c.bin"
	if got := okComputer.IDs().path(); got != want {
		t.Fatalf("path() = %q, want %q", got, want)
	}
}

func TestATrackHoldsTheFramesUpToTheNextTrack(t *testing.T) {
	if got, want := okComputer.Frames(2), (50242-21480)*FramesPerSector; got != want {
		t.Fatalf("Frames(2) = %d, want %d", got, want)
	}
}

func TestTheLastTrackHoldsTheFramesUpToTheLeadOut(t *testing.T) {
	if got, want := okComputer.Frames(12), (240687-216347)*FramesPerSector; got != want {
		t.Fatalf("Frames(12) = %d, want %d", got, want)
	}
}

func TestATrackTheDiscDoesNotHaveHoldsNothing(t *testing.T) {
	if got := okComputer.Frames(13); got != 0 {
		t.Fatalf("Frames(13) = %d, want 0", got)
	}
}

func TestATableWhoseOffsetsFallIsNotValid(t *testing.T) {
	if (TOC{Offsets: []int{150, 9000, 8000}, Sectors: 20000}).Valid() {
		t.Fatal("Valid() = true, want false")
	}
}

// Eraserheads, Bananatype EP (1997), as the EAC log published beside its rip
// on the Internet Archive (Eraserheads_Bananatype1997_Ep) gives the table of
// contents. Decoding that rip's FLAC files gave these frame counts and the v2
// checksums the log reports as accurately ripped, for tracks 1, 3 and 5, on
// 2026-10-13; AccurateRip listed the same checksums under these IDs.
var bananatype = TOC{Offsets: []int{150, 27462, 50702, 66530, 80467}, Sectors: 93657}

func TestARealRipsDiscIDsAreTheOnesItsChecksumsAreFiledUnder(t *testing.T) {
	want := IDs{Tracks: 5, ID1: 0x0004da74, ID2: 0x0015e33c, CDDB: 0x4504de05}
	if got := bananatype.IDs(); got != want {
		t.Fatalf("IDs() = %+v, want %+v", got, want)
	}
}

func TestARealRipsTracksHoldTheFramesItsTableGives(t *testing.T) {
	for track, frames := range map[int]int{1: 16059456, 3: 9306864, 5: 7755720} {
		if got := bananatype.Frames(track); got != frames {
			t.Errorf("Frames(%d) = %d, want %d", track, got, frames)
		}
	}
}

package tagging

import (
	"slices"
	"testing"

	"go.senan.xyz/taglib"
)

// The genre a music server browses by is written from what Schall proved the
// file to be, like every other tag here.
func TestTheGenreIsWrittenIntoTheTagAServerBrowsesBy(t *testing.T) {
	written := Tags{Title: "Eden", Artist: "Talk Talk", Genre: "post-rock"}.values()

	if got := written[keyGenre]; len(got) != 1 || got[0] != "post-rock" {
		t.Fatalf("the genre was written as %q", got)
	}
}

// A genre Schall has none of clears the tag rather than leaving a stranger's
// word for it standing. It is the same rule as an album Schall cannot name: a
// managed copy says these things in Schall's words or in nobody's.
func TestAFileWithNoGenreHasTheTagCleared(t *testing.T) {
	written := Tags{Title: "Eden", Artist: "Talk Talk"}.values()

	value, present := written[keyGenre]
	if !present {
		t.Fatal("the genre tag was left alone, want it cleared")
	}
	if len(value) != 0 {
		t.Fatalf("the genre was written as %q, want nothing", value)
	}
}

// A genre is not enough to describe a file. A file with nothing else proved
// about it keeps everything it arrived with.
func TestAGenreAloneProvesNothing(t *testing.T) {
	if (Tags{Genre: "post-rock"}).proved() {
		t.Fatal("a genre alone was taken as enough to describe a file")
	}
}

// The genre tag is claimed the same way the artist and position tags are, so a
// revert has to give the peer's word back rather than leave Schall's own vote
// standing. A withdrawn match must not cost the peer's genre for good.
func TestARevertGivesBackThePeersGenre(t *testing.T) {
	path := fixture(t, "silence.flac")
	if err := taglib.WriteTags(path, map[string][]string{
		keyGenre: {"Electronic"},
	}, 0); err != nil {
		t.Fatalf("arrange the fixture: %v", err)
	}

	written := proved()
	written.Genre = "post-rock"
	if err := Write(path, written); err != nil {
		t.Fatalf("write the tags: %v", err)
	}
	if got := read(t, path)[keyGenre]; !slices.Equal(got, []string{"post-rock"}) {
		t.Fatalf("the genre reads %q after the write, want Schall's own", got)
	}

	if _, err := Revert(path); err != nil {
		t.Fatalf("give the file back its own tags: %v", err)
	}

	if got := read(t, path)[keyGenre]; !slices.Equal(got, []string{"Electronic"}) {
		t.Fatalf("the genre reads %q after the revert, want the peer's own", got)
	}
}

// A record written before the genre tag was kept holds no genre reading at
// all, the same as it holds no track number tag for a file tagged that long
// ago. A revert of such a file leaves GENRE cleared rather than inventing a
// value the record never held.
func TestAFileTaggedBeforeGenreWasKeptHasNoGenreToGiveBack(t *testing.T) {
	path := fixture(t, "silence.flac")
	if err := taglib.WriteTags(path, map[string][]string{
		keyGenre:         {"Schall Genre"},
		keyTaggedAt:      {"2026-08-05T00:00:00Z"},
		keyTagsAsArrived: {`{"artist":"Peer Artist","album":"Peer Album","title":"peer title"}`},
	}, 0); err != nil {
		t.Fatalf("write a record of the shape the older writes made: %v", err)
	}

	if _, err := Revert(path); err != nil {
		t.Fatalf("give the file back its own tags: %v", err)
	}

	if got := read(t, path)[keyGenre]; len(got) != 0 {
		t.Fatalf("the genre reads %q after the revert, want it cleared", got)
	}
}

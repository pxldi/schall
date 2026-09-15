package library

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/google/uuid"
	"github.com/pxldi/schall/internal/tagging"
	"github.com/rs/zerolog"
)

// answering builds the rows one file's mappings come back as: the same file,
// once per release it answers.
func answering(fileID uuid.UUID, identity *uuid.UUID, groups ...uuid.UUID) []mappedFile {
	answers := make([]mappedFile, 0, len(groups))
	for _, group := range groups {
		answers = append(answers, mappedFile{
			fileID:               fileID,
			path:                 "/music/a.flac",
			identityReleaseGroup: identity,
			tags: tagging.Tags{
				Title: "Tears", Artist: "Himera", AlbumArtist: "Himera",
				ReleaseGroupID: group,
			},
		})
	}
	return answers
}

func TestAFileAnsweringOneReleaseIsWrittenAsThatRelease(t *testing.T) {
	group := uuid.New()

	chosen := choose(answering(uuid.New(), nil, group))

	if chosen.ambiguous {
		t.Fatal("a file with one release to be written as was left alone")
	}
	if chosen.tags.ReleaseGroupID != group {
		t.Fatalf("written as release group %s", chosen.tags.ReleaseGroupID)
	}
}

// One file answers the same recording on every release that lists it, and those
// releases disagree about the album. What Schall resolved the audio to is the
// one thing that says which of them the file is a copy of.
func TestAFileAnsweringTwoReleasesIsWrittenAsTheOneItWasResolvedTo(t *testing.T) {
	single, compilation := uuid.New(), uuid.New()

	chosen := choose(answering(uuid.New(), &compilation, single, compilation))

	if chosen.ambiguous {
		t.Fatal("a file resolved to one of its releases was left alone")
	}
	if chosen.tags.ReleaseGroupID != compilation {
		t.Fatalf("written as release group %s, wanted the resolved one", chosen.tags.ReleaseGroupID)
	}
}

// And where nothing says which, nothing is written. An album picked from two is
// a guess about which release the user owns, and a wrong one files one album
// under two names — which is the thing this whole exercise exists to stop.
func TestAFileAnsweringTwoReleasesNothingChoosesBetweenIsLeftAlone(t *testing.T) {
	chosen := choose(answering(uuid.New(), nil, uuid.New(), uuid.New()))

	if !chosen.ambiguous {
		t.Fatal("a file nothing chose a release for was written anyway")
	}
	if chosen.tags.Album != "" || chosen.tags.Title != "" {
		t.Fatalf("something was still going to be written: %+v", chosen.tags)
	}
}

func TestAFileResolvedToAReleaseItDoesNotAnswerIsLeftAlone(t *testing.T) {
	elsewhere := uuid.New()

	chosen := choose(answering(uuid.New(), &elsewhere, uuid.New(), uuid.New()))

	if !chosen.ambiguous {
		t.Fatal("a file whose resolved release is none of the ones it answers was written anyway")
	}
}

// A file that answers releases nothing chooses between is counted as the
// reason it was left alone. "Left alone" covers two silences and a total that
// names neither leaves the reader guessing which one they are reading.
func TestAFileNothingChoseAReleaseForIsCountedAsAnsweringTwo(t *testing.T) {
	var run TagRun

	NewTagger(nil, zerolog.Nop()).tag(context.Background(), &run,
		mappedFile{path: "/music/a.flac", ambiguous: true})

	if run.SkippedAmbiguous != 1 || run.SkippedUnproved != 0 || run.Skipped != 1 {
		t.Fatalf("the pass counted %+v", run)
	}
}

// And a file the catalogue holds too little about to describe is counted as
// that. It is the same total and a different thing to tell somebody.
func TestAFileTooLittleIsProvedAboutIsCountedAsUnproved(t *testing.T) {
	path := filepath.Join(t.TempDir(), "track.flac")
	writeFLAC(t, path, 48000, 144000, "TITLE=walk to the closed bodega")
	var run TagRun

	// An album with no title to it and nobody it is by: the write refuses, the
	// file keeps everything it came with, and that is not a failure.
	NewTagger(nil, zerolog.Nop()).tag(context.Background(), &run,
		mappedFile{path: path, tags: tagging.Tags{Album: "Halfway House Near Me"}})

	if run.SkippedUnproved != 1 || run.SkippedAmbiguous != 0 || run.Skipped != 1 {
		t.Fatalf("the pass counted %+v", run)
	}
}

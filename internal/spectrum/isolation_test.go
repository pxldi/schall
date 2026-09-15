package spectrum

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// What this package produces is quality and never identity. A cutoff says
// nothing about which recording a file holds — a transcode of the right
// recording is still the right recording — so no rule that admits a file or
// refuses one may ever read any of it.
//
// That is a claim about code that does not exist rather than about code that
// does, and the ordinary way to make such a claim true is to say it in a comment
// and hope. This reads the source instead: the packages that decide what a file
// is must not mention this package, its measurement, or the columns it writes.
//
// A future change that wires a transcode flag into grading fails here, with this
// sentence attached, which is the point.
var wordsOfThisPackage = []string{
	"spectrum", "Spectral", "spectral_cutoff", "TranscodeSuspected", "transcode_suspected",
}

// The packages and files that decide what a file is. internal/downloads is not
// listed whole: single.go writes the measurement into a copy's evidence for the
// person reading the review queue, which is exactly what this feature is for.
// The two files in that package that grade are named instead.
var deciders = []string{
	"../matching",
	"../identity",
	"../tagmatch",
	"../acquisition",
	"../downloads/match.go",
	"../downloads/importer.go",
}

func TestNothingThatDecidesReadsTheMeasurement(t *testing.T) {
	for _, place := range deciders {
		for _, file := range goFilesUnder(t, place) {
			source, err := os.ReadFile(file)
			if err != nil {
				t.Fatalf("read %s: %v", file, err)
			}
			for _, word := range wordsOfThisPackage {
				if strings.Contains(string(source), word) {
					t.Errorf("%s mentions %q. What the audio is may be shown to a person "+
						"and read by nothing that admits or refuses a file.", file, word)
				}
			}
		}
	}
}

// And the one place inside internal/downloads that does measure a copy must not
// hand what it measured to the grader. candidateEvidence is where a copy's facts
// are turned into what the grader reads, and the measurement is written after
// it, into the evidence a person will read.
func TestTheGraderIsNeverHandedTheMeasurement(t *testing.T) {
	source, err := os.ReadFile("../downloads/single.go")
	if err != nil {
		t.Fatalf("read single.go: %v", err)
	}
	_, body, found := strings.Cut(string(source), "func candidateEvidence(")
	if !found {
		t.Fatal("single.go no longer builds the grader's evidence in candidateEvidence")
	}
	body, _, _ = strings.Cut(body, "\n}\n")
	for _, word := range append(wordsOfThisPackage, "evidence.Audio") {
		if strings.Contains(body, word) {
			t.Errorf("candidateEvidence passes %q to the grader", word)
		}
	}
}

func goFilesUnder(t *testing.T, place string) []string {
	t.Helper()
	info, err := os.Stat(place)
	if err != nil {
		t.Fatalf("look at %s: %v", place, err)
	}
	if !info.IsDir() {
		return []string{place}
	}
	found := []string{}
	err = filepath.WalkDir(place, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !entry.IsDir() && strings.HasSuffix(path, ".go") {
			found = append(found, path)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walk %s: %v", place, err)
	}
	return found
}

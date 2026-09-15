package library

import "testing"

// TestCandidateFieldsAskInOrder pins which source names a folder. The order is
// the whole of why a run against an untouched template moves nothing: the
// import that filed a release is asked before anything that merely describes
// it.
func TestCandidateFieldsAskInOrder(t *testing.T) {
	tests := []struct {
		name  string
		file  candidate
		want  Fields
		named bool
	}{
		{
			name: "the import that filed it answers first",
			file: candidate{
				artistTag: "boards of canada",
				albumTag:  "geogaddi",
				filedAs: fileNames{
					artist: "Boards of Canada", album: "Geogaddi", id: "a3f19c2b",
				},
				catalogued: fileNames{
					artist: "Boards Of Canada", album: "Geogaddi (2002)", id: "77c1e4da",
				},
			},
			want:  Fields{Artist: "Boards of Canada", Album: "Geogaddi", ID: "a3f19c2b"},
			named: true,
		},
		{
			// A copy fetched for a want answers a recording, so its import names
			// no release at all and the catalogue answers instead — name and
			// identifier both, because taking one from each is what files an
			// album's tracks in a folder apiece.
			name: "an import that named no release leaves the catalogue to answer",
			file: candidate{
				catalogued: fileNames{
					artist: "Jai Paul", album: "Leak 04-13", id: "77c1e4da",
				},
			},
			want:  Fields{Artist: "Jai Paul", Album: "Leak 04-13", ID: "77c1e4da"},
			named: true,
		},
		{
			// The credit is written per recording, so it carries the guests on
			// one track and whatever spelling that release printed. The album
			// row holds one artist for every track of the album, which is what
			// keeps them in one folder — and it is what both importers file a
			// folder under, so a migration lands music where an import would.
			name: "the album the file is matched to outranks the credit on it",
			file: candidate{
				identified: fileNames{
					artist: "Ufo361 feat. KC Rebell", album: "WAVE", id: "f3c445e4",
				},
				catalogued: fileNames{artist: "Ufo361", album: "WAVE", id: "f3c445e4"},
			},
			want:  Fields{Artist: "Ufo361", Album: "WAVE", ID: "f3c445e4"},
			named: true,
		},
		{
			// A release the catalogue does not hold. What the audio was proven
			// to be is then the only account of it there is.
			name: "an uncatalogued release is named by what the audio proved",
			file: candidate{
				identified: fileNames{artist: "crier", album: "bleed", id: "b3cb55a2"},
			},
			want:  Fields{Artist: "crier", Album: "bleed", ID: "b3cb55a2"},
			named: true,
		},
		{
			// Tags name a release without identifying one. Rendering them beside
			// an identifier from somewhere else is the mistake this whole order
			// exists to prevent.
			name: "what the file says about itself identifies no release",
			file: candidate{
				artistTag: "Burial", albumTag: "Untrue",
				filedAs: fileNames{id: "deadbeef"},
			},
			want:  Fields{Artist: "Burial", Album: "Untrue"},
			named: true,
		},
		{
			name: "then the release it is matched to",
			file: candidate{
				artistTag:  "aphex twin",
				albumTag:   "drukqs",
				catalogued: fileNames{artist: "Aphex Twin", album: "Drukqs"},
			},
			want:  Fields{Artist: "Aphex Twin", Album: "Drukqs"},
			named: true,
		},
		{
			name:  "then what the file says about itself",
			file:  candidate{artistTag: "Burial", albumTag: "Untrue"},
			want:  Fields{Artist: "Burial", Album: "Untrue"},
			named: true,
		},
		{
			// Half a name from one source and half from another would file a
			// release in a folder no source ever described.
			name:  "a source that knows only an album is still one source",
			file:  candidate{albumTag: "Untitled"},
			want:  Fields{Album: "Untitled"},
			named: true,
		},
		{
			name:  "a file nothing can name is not planned",
			file:  candidate{},
			named: false,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, named := test.file.fields()
			if named != test.named || got != test.want {
				t.Errorf("fields() = %#v, %v; want %#v, %v", got, named, test.want, test.named)
			}
		})
	}
}

func TestTemplateUsesIdentifier(t *testing.T) {
	tests := map[string]bool{
		DefaultTemplate:              true,
		"{artist}/{album}":           false,
		"{artist}/{album}/{id}":      true,
		"{id}":                       true,
		"Music/{artist} - {album}":   false,
		"{artist}/{album} ({album})": false,
	}
	for text, want := range tests {
		template, err := ParseTemplate(text)
		if err != nil {
			t.Fatalf("ParseTemplate(%q) error = %v", text, err)
		}
		if got := template.UsesIdentifier(); got != want {
			t.Errorf("ParseTemplate(%q).UsesIdentifier() = %v, want %v", text, got, want)
		}
	}
}

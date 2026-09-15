package library

import (
	"errors"
	"testing"
)

// The default is what both importers build today. It has to parse, and it has
// to render the folder they already write, or turning the setting on would
// move a library that is already correctly filed.
func TestTheDefaultTemplateRendersTheFolderTheImportersAlreadyWrite(t *testing.T) {
	template, err := ParseTemplate(DefaultTemplate)
	if err != nil {
		t.Fatalf("ParseTemplate(%q) = %v", DefaultTemplate, err)
	}
	got := template.Render(Fields{Artist: "Björk", Album: "Homogenic", ID: "a3f19c2b"})
	if want := "Björk/Homogenic [a3f19c2b]"; got != want {
		t.Fatalf("Render = %q, want %q", got, want)
	}
}

// A separator inside a value is part of the name, not an instruction about
// where the folder goes. Splitting on it would file the release one directory
// deeper than the template says.
func TestASeparatorInAValueStaysInsideItsFolder(t *testing.T) {
	template, err := ParseTemplate("{artist}/{album}")
	if err != nil {
		t.Fatal(err)
	}
	got := template.Render(Fields{Artist: "AC/DC", Album: "Back in Black"})
	if want := "AC_DC/Back in Black"; got != want {
		t.Fatalf("Render = %q, want %q", got, want)
	}
}

// A tag is allowed to be silent, and a folder that vanishes when it is moves
// the release up a level — a different place, not a missing name.
func TestAFolderWhoseFieldsAreAllSilentIsStillAFolder(t *testing.T) {
	template, err := ParseTemplate("{artist}/{album} [{id}]")
	if err != nil {
		t.Fatal(err)
	}
	got := template.Render(Fields{Album: "Untitled", ID: "7c04e881"})
	if want := "Unknown/Untitled [7c04e881]"; got != want {
		t.Fatalf("Render = %q, want %q", got, want)
	}
}

// A template that names nothing which differs between releases files the whole
// library into one folder, where the importer's "this folder already exists"
// path merges them.
func TestATemplateThatCannotTellReleasesApartIsRefused(t *testing.T) {
	_, err := ParseTemplate("{artist}/Albums")
	if !errors.Is(err, ErrTemplateNotDistinct) {
		t.Fatalf("err = %v, want ErrTemplateNotDistinct", err)
	}
}

// An absolute template writes music outside the library, past the allowed
// roots and past anything a scan will walk.
func TestAnAbsoluteTemplateIsRefused(t *testing.T) {
	_, err := ParseTemplate("/srv/music/{album}")
	if !errors.Is(err, ErrTemplateAbsolute) {
		t.Fatalf("err = %v, want ErrTemplateAbsolute", err)
	}
}

// The same, climbing rather than starting at the root.
func TestATemplateThatClimbsOutOfTheLibraryIsRefused(t *testing.T) {
	_, err := ParseTemplate("../{artist}/{album}")
	if !errors.Is(err, ErrTemplateEscapes) {
		t.Fatalf("err = %v, want ErrTemplateEscapes", err)
	}
}

// The refusal quotes the word back, because "unrecognised placeholder" leaves
// somebody hunting through their own template for it.
func TestAnUnknownPlaceholderIsNamedInTheRefusal(t *testing.T) {
	_, err := ParseTemplate("{albumartist}/{album}")
	var unknown UnknownFieldError
	if !errors.As(err, &unknown) {
		t.Fatalf("err = %v, want UnknownFieldError", err)
	}
	if unknown.Field != "albumartist" {
		t.Fatalf("Field = %q, want albumartist", unknown.Field)
	}
}

// An unclosed brace is a typo, and rendering it would file a release in a
// folder literally called "{album".
func TestATemplateWithAnUnclosedBraceIsRefused(t *testing.T) {
	_, err := ParseTemplate("{artist}/{album")
	if !errors.Is(err, ErrTemplateUnclosed) {
		t.Fatalf("err = %v, want ErrTemplateUnclosed", err)
	}
}

// A doubled or trailing separator is not a folder anybody asked for.
func TestAStraySeparatorDoesNotBecomeAFolder(t *testing.T) {
	template, err := ParseTemplate("{artist}//{album}/")
	if err != nil {
		t.Fatal(err)
	}
	got := template.Render(Fields{Artist: "Boards of Canada", Album: "Geogaddi"})
	if want := "Boards of Canada/Geogaddi"; got != want {
		t.Fatalf("Render = %q, want %q", got, want)
	}
}

// A stored template reads back as what was typed, not as something reassembled
// from its parts.
func TestATemplateReadsBackAsItWasWritten(t *testing.T) {
	template, err := ParseTemplate("  {artist}/{album} [{id}]  ")
	if err != nil {
		t.Fatal(err)
	}
	if got := template.String(); got != DefaultTemplate {
		t.Fatalf("String = %q, want %q", got, DefaultTemplate)
	}
}

// A component made only of dots and spaces is not a name a filesystem should
// be handed, and one made of them around a silent field is the same.
func TestAFolderThatSanitisesAwayEntirelyIsNamedRatherThanEmpty(t *testing.T) {
	template, err := ParseTemplate("{artist}/{album}")
	if err != nil {
		t.Fatal(err)
	}
	got := template.Render(Fields{Artist: "...", Album: "Selected Ambient Works"})
	if want := "Unknown/Selected Ambient Works"; got != want {
		t.Fatalf("Render = %q, want %q", got, want)
	}
}

// An empty template is a setting nobody can act on.
func TestAnEmptyTemplateIsRefused(t *testing.T) {
	_, err := ParseTemplate("   ")
	if !errors.Is(err, ErrTemplateEmpty) {
		t.Fatalf("err = %v, want ErrTemplateEmpty", err)
	}
}

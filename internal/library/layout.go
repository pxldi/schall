package library

import (
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"unicode"
)

// The placeholders a layout template may name. They are the fields both
// importers already hold when they choose a folder, and the list is short for
// that reason rather than by taste: a placeholder nothing can fill renders as
// nothing, and a layout with a hole in it files music somewhere nobody meant.
const (
	fieldArtist = "artist"
	fieldAlbum  = "album"
	fieldID     = "id"
)

// DefaultTemplate is what both importers build today, so an installation that
// never opens this setting keeps filing music where it always has, and a first
// migration run against an untouched template moves nothing.
const DefaultTemplate = "{artist}/{album} [{id}]"

// unknownComponent is what a folder the template asked for is called when
// every field in it turned out to be silent. Dropping it instead would file
// the release one level up, which is a different folder rather than a missing
// name.
const unknownComponent = "Unknown"

// Expected failures, as sentences, because every one of them is shown to
// somebody editing a template.
var (
	ErrTemplateEmpty = errors.New("the layout template is empty")

	// A template is turned into a path under the library. One that begins at
	// the root, or climbs above it, writes music outside the library
	// altogether — past the allowed roots, past what a scan will ever walk.
	ErrTemplateAbsolute = errors.New("the layout template must be a path inside the library, not an absolute path")
	ErrTemplateEscapes  = errors.New("the layout template must not contain ..")

	// Without something that differs between releases, every release renders to
	// the same folder and the importer's "this folder already exists" path
	// merges them into one.
	ErrTemplateNotDistinct = errors.New("the layout template must contain {album} or {id}, or every release is filed in the same folder")

	ErrTemplateUnclosed = errors.New("the layout template has a { with no closing }")
)

// UnknownFieldError names the placeholder nobody can fill. It is its own type
// so the answer can quote the offending word back rather than say that
// something, somewhere, was not recognised.
type UnknownFieldError struct{ Field string }

func (err UnknownFieldError) Error() string {
	return fmt.Sprintf("the layout template names {%s}, which is not one of {artist}, {album} or {id}", err.Field)
}

// Fields are what a template is rendered against: the release being filed.
//
// ID is the short identifier that keeps two different releases sharing an
// artist and a title in two folders rather than one. Every field may be empty,
// because a tag is allowed to be silent.
type Fields struct {
	Artist string
	Album  string
	ID     string
}

// Template is a parsed folder layout. It names folders only — a file keeps the
// name it arrived with — and it decides nothing about the music: no part of
// matching or identity reads a path.
type Template struct {
	text string
	// components are the folders, outermost first, already split at the
	// separators the template itself wrote. Splitting here rather than after
	// substitution is what stops a value from inventing a folder: an album
	// called "AC/DC Live" is one folder with an awkward name, not two.
	components [][]templatePart
}

// templatePart is either a literal run of the template or one placeholder.
type templatePart struct {
	literal string
	field   string
}

// ParseTemplate reads a template and reports the first thing wrong with it.
//
// Everything it refuses is refused where somebody types it, not where a
// release is filed: a template that cannot render is a setting nobody can act
// on, and finding that out during an import means finding it out with the
// bytes already copied.
func ParseTemplate(text string) (Template, error) {
	text = strings.TrimSpace(text)
	if text == "" {
		return Template{}, ErrTemplateEmpty
	}
	if strings.HasPrefix(text, "/") {
		return Template{}, ErrTemplateAbsolute
	}
	if strings.Contains(text, "..") {
		return Template{}, ErrTemplateEscapes
	}

	template := Template{text: text}
	current := []templatePart{}
	var distinct bool
	// Every component the template writes is kept, including empty ones, until
	// rendering decides what to do with them. A stray separator is the
	// template's mistake to report, not something to quietly absorb.
	appendLiteral := func(literal string) {
		segments := strings.Split(literal, "/")
		for index, segment := range segments {
			if index > 0 {
				template.components = append(template.components, current)
				current = []templatePart{}
			}
			if segment != "" {
				current = append(current, templatePart{literal: segment})
			}
		}
	}

	rest := text
	for {
		open := strings.IndexByte(rest, '{')
		if open < 0 {
			appendLiteral(rest)
			break
		}
		appendLiteral(rest[:open])
		closing := strings.IndexByte(rest[open:], '}')
		if closing < 0 {
			return Template{}, ErrTemplateUnclosed
		}
		field := rest[open+1 : open+closing]
		switch field {
		case fieldAlbum, fieldID:
			distinct = true
		case fieldArtist:
		default:
			return Template{}, UnknownFieldError{Field: field}
		}
		current = append(current, templatePart{field: field})
		rest = rest[open+closing+1:]
	}
	template.components = append(template.components, current)
	if !distinct {
		return Template{}, ErrTemplateNotDistinct
	}
	return template, nil
}

// String is the template as it was written, so a stored setting reads back as
// what was typed rather than as something reassembled from its parts.
func (template Template) String() string { return template.text }

// UsesIdentifier reports whether the template asks for {id}.
//
// It exists for the one caller that cannot answer with Unknown when a field is
// silent: filing a release Schall imported reads the identifier off the import,
// and a release it did not import has none to read. Rendering such a file would
// name a folder after an identifier that does not exist, so the migration is
// asked this first and leaves the file where it is instead.
func (template Template) UsesIdentifier() bool {
	for _, component := range template.components {
		for _, part := range component {
			if part.field == fieldID {
				return true
			}
		}
	}
	return false
}

// Render turns a template into a path relative to the library root.
func (template Template) Render(fields Fields) string {
	cleaned := make([]string, 0, len(template.components))
	for _, component := range template.components {
		// A component the template wrote nothing into at all — a doubled or
		// trailing separator — is not a folder anybody asked for.
		if len(component) == 0 {
			continue
		}
		var built strings.Builder
		for _, part := range component {
			if part.field == "" {
				built.WriteString(part.literal)
				continue
			}
			built.WriteString(escapeValue(fields.value(part.field)))
		}
		name := SafeComponent(built.String())
		if name == "" {
			// A folder made only of punctuation around silent fields has no
			// name of its own, and one made only of literals that sanitised
			// away is the same thing. Either way the template asked for a
			// folder here, so there is one.
			name = unknownComponent
		}
		cleaned = append(cleaned, name)
	}
	if len(cleaned) == 0 {
		return unknownComponent
	}
	return filepath.Join(cleaned...)
}

func (fields Fields) value(field string) string {
	switch field {
	case fieldArtist:
		return fields.Artist
	case fieldAlbum:
		return fields.Album
	case fieldID:
		return fields.ID
	}
	return ""
}

// escapeValue keeps a value inside the component the template put it in. A
// separator in an artist or album name is part of that name, not an
// instruction about where the folder goes.
func escapeValue(value string) string {
	return strings.NewReplacer("/", "_", "\\", "_").Replace(strings.TrimSpace(value))
}

// SafeComponent reduces one rendered component to something a filesystem will
// take. It is the rule both importers already apply, named here so that the
// layout and the importers cannot drift apart once the importers read a
// template.
//
// An empty result is the caller's to interpret, which is the one way it
// differs from the importers' private copies: a folder with no name is a
// different decision from a folder called Unknown, and Render is where that
// decision belongs.
func SafeComponent(value string) string {
	value = strings.Map(func(symbol rune) rune {
		if symbol == '/' || symbol == '\\' || unicode.IsControl(symbol) {
			return '_'
		}
		return symbol
	}, strings.TrimSpace(value))
	return strings.Trim(value, ". ")
}

package playlists

import (
	"bytes"
	"encoding/csv"
	"errors"
	"fmt"
	"io"
	"path/filepath"
	"strconv"
	"strings"
)

// A playlist can also arrive as a file: a CSV exported by another tool, or an
// M3U an old player wrote. This file is the reading of such a file and nothing
// else — it produces rows of evidence, exactly what the file says and no more,
// and every row then goes through the same walk a Spotify entry does.
//
// Nothing here concludes anything about music. Where a file is unclear, the
// reading keeps the words whole rather than guessing at their parts.

// MaxFileRows is the most rows one imported file may hold. A list longer than
// this is a database export, not a playlist, and reading it would hold a
// request open for minutes.
const MaxFileRows = 10000

// ErrUnreadableFile means the file's shape could not be read at all — as
// opposed to a single row inside it that could not be read.
var ErrUnreadableFile = errors.New("this file cannot be read as a playlist")

// ErrTooManyRows refuses a file above MaxFileRows.
var ErrTooManyRows = fmt.Errorf("this playlist holds more than %d songs", MaxFileRows)

// FileRow is one song as the file described it, and nothing more. Every field
// is verbatim except the ISRC, which is checked for shape, and the key, which
// exists only so that importing the same file twice lands on the same rows.
type FileRow struct {
	// Line is where the row sat in the file, counting from one, so a row that
	// was skipped can be named.
	Line int
	// Position is where the row sits in the playlist, counting from one.
	Position int

	Artist     string
	Title      string
	Album      string
	DurationMS int
	ISRC       string

	// LibraryPath is a path the file named for this song, if it named one.
	// Only an M3U carries these. It is looked up as an exact path later; the
	// reading here does not know what the library holds.
	LibraryPath string

	// Key is the row's identity for a second import of the same file. It is
	// the path where the row names one, the ISRC where the row has one, and
	// the row's own words otherwise. It
	// decides which stored entry a row refreshes and never decides what music
	// the row is: two rows with one key are one entry, exactly as a Spotify
	// track listed twice is one entry.
	Key string
}

// SkippedRow is a row that carried nothing to search for, named by its line so
// somebody can go and look at it.
type SkippedRow struct {
	Line   int
	Reason string
}

// ParsedFile is one file read: what it was called, what shape it was in, which
// columns were found, and the rows.
type ParsedFile struct {
	// Name is what the playlist would be called: the file's own name without
	// its extension, or the name an M3U gave itself.
	Name string
	// Format is 'csv' or 'm3u', for the preview to say which reading was used.
	Format string
	// Columns names the fields the file was found to carry, in a fixed order,
	// so the preview can say what this file gives and what it does not.
	Columns []string
	Rows    []FileRow
	Skipped []SkippedRow
}

// ParseFile reads an uploaded playlist file. The name decides the reading
// where it can, and the content decides it otherwise.
func ParseFile(filename string, content []byte) (ParsedFile, error) {
	content = bytes.TrimPrefix(content, []byte{0xEF, 0xBB, 0xBF})
	name := playlistNameFor(filename)
	switch {
	case looksLikeM3U(filename, content):
		return parseM3U(name, content)
	default:
		return parseDelimited(name, filename, content)
	}
}

// playlistNameFor is the file's name without its directory or its extension.
func playlistNameFor(filename string) string {
	base := filepath.Base(strings.ReplaceAll(strings.TrimSpace(filename), `\`, "/"))
	base = strings.TrimSuffix(base, filepath.Ext(base))
	base = strings.TrimSpace(base)
	if base == "" || base == "." || base == "/" {
		return "Imported playlist"
	}
	return base
}

func looksLikeM3U(filename string, content []byte) bool {
	switch strings.ToLower(filepath.Ext(filename)) {
	case ".m3u", ".m3u8":
		return true
	case ".csv", ".tsv":
		return false
	}
	return bytes.HasPrefix(bytes.TrimSpace(content), []byte("#EXTM3U"))
}

// parseM3U reads the player format. It holds two things per song at most: an
// "#EXTINF:" line with a length and a piece of text, and a path.
func parseM3U(name string, content []byte) (ParsedFile, error) {
	parsed := ParsedFile{Name: name, Format: "m3u"}
	carriesDuration, carriesPath := false, false

	var pendingText string
	var pendingDuration int
	pending := false

	for number, raw := range strings.Split(string(content), "\n") {
		line := strings.TrimRight(strings.TrimSpace(raw), "\r")
		if line == "" {
			continue
		}
		if strings.HasPrefix(line, "#") {
			switch {
			case strings.HasPrefix(line, "#EXTINF:"):
				pendingDuration, pendingText = readEXTINF(strings.TrimPrefix(line, "#EXTINF:"))
				pending = true
				if pendingDuration > 0 {
					carriesDuration = true
				}
			case strings.HasPrefix(line, "#PLAYLIST:"):
				// The file naming itself is a better name than its filename.
				if titled := strings.TrimSpace(strings.TrimPrefix(line, "#PLAYLIST:")); titled != "" {
					parsed.Name = titled
				}
			}
			continue
		}

		row := FileRow{Line: number + 1, LibraryPath: line}
		carriesPath = true
		if pending {
			row.Artist, row.Title = splitArtistTitle(pendingText)
			row.DurationMS = pendingDuration * 1000
		} else {
			// A path with nothing said about it is still a song. The whole
			// line is the title: it may name a file the library holds, and
			// where it does not, it is what there is to read.
			row.Title = line
		}
		pending, pendingText, pendingDuration = false, "", 0

		if strings.TrimSpace(row.Title) == "" {
			parsed.Skipped = append(parsed.Skipped, SkippedRow{
				Line: row.Line, Reason: "this line says nothing to search for",
			})
			continue
		}
		parsed.Rows = append(parsed.Rows, row)
	}

	parsed.Columns = []string{"title"}
	if anyArtist(parsed.Rows) {
		parsed.Columns = append(parsed.Columns, "artist")
	}
	if carriesDuration {
		parsed.Columns = append(parsed.Columns, "duration")
	}
	if carriesPath {
		parsed.Columns = append(parsed.Columns, "path")
	}
	return finish(parsed)
}

// readEXTINF splits "123,Artist - Title" into its length in seconds and its
// text. A length that is not a number is no length: -1 is what players write
// when they do not know it.
func readEXTINF(rest string) (seconds int, text string) {
	head, tail, found := strings.Cut(rest, ",")
	if !found {
		return 0, strings.TrimSpace(rest)
	}
	// Some writers put extra attributes after the length, before the comma.
	head, _, _ = strings.Cut(strings.TrimSpace(head), " ")
	value, err := strconv.Atoi(strings.TrimSpace(head))
	if err != nil || value < 0 {
		value = 0
	}
	return value, strings.TrimSpace(tail)
}

// splitArtistTitle reads "Artist - Title" and refuses to read anything else.
//
// The separator has to be exactly one " - ". A title that holds a dash of its
// own, or a credit line that holds several, would be cut in the wrong place,
// and half an artist name is worse evidence than none: the whole line becomes
// the title and the artist stays empty, which is silence rather than a guess.
func splitArtistTitle(text string) (artist, title string) {
	text = strings.TrimSpace(text)
	if strings.Count(text, " - ") != 1 {
		return "", text
	}
	before, after, _ := strings.Cut(text, " - ")
	before, after = strings.TrimSpace(before), strings.TrimSpace(after)
	if before == "" || after == "" {
		return "", text
	}
	return before, after
}

func anyArtist(rows []FileRow) bool {
	for _, row := range rows {
		if row.Artist != "" {
			return true
		}
	}
	return false
}

// columnNames maps the headers exporters write onto the four fields an entry
// can carry. Read case-insensitively, and a header nobody recognises is simply
// not read.
var columnNames = map[string]string{
	"track name": "title", "title": "title", "name": "title", "song": "title",
	"track": "title", "track title": "title",

	"artist name(s)": "artist", "artist name": "artist", "artist": "artist",
	"artists": "artist", "artist(s)": "artist", "artist names": "artist",

	"album name": "album", "album": "album", "album title": "album",

	"duration (ms)": "durationMs", "duration ms": "durationMs",
	"duration_ms": "durationMs", "durationms": "durationMs",
	"length (ms)": "durationMs",

	"duration": "duration", "length": "duration", "time": "duration",

	"isrc": "isrc",
}

// ambiguousColumnNames are the headers that name a field without meaning it on
// their own. "Track" is the only one: half the exporters write the song's name
// under it and the other half the number it sits at on the record. A file that
// carries both a Track and a Title column is the second kind, and reading its
// numbers as titles turned a playlist into a list of "1", "2", "3". So a header
// that means the field and nothing else takes it, wherever in the row it sits.
var ambiguousColumnNames = map[string]bool{"track": true}

// parseDelimited reads a CSV or a TSV. Two shapes are accepted: a file with
// named columns, which is what every exporter writes, and a plain two-column
// "artist,title" list, which is what somebody writes by hand.
func parseDelimited(name, filename string, content []byte) (ParsedFile, error) {
	records, err := readRecords(content, delimiterFor(filename, content))
	if err != nil {
		return ParsedFile{}, err
	}
	if len(records) == 0 {
		return ParsedFile{}, fmt.Errorf("%w: it holds no rows", ErrUnreadableFile)
	}

	parsed := ParsedFile{Name: name, Format: "csv"}
	columns, headed := readHeader(records[0])
	body := records
	if headed {
		body = records[1:]
	} else {
		// No header this reading knows. Two columns are "artist,title" by the
		// convention people write; anything else is not a playlist Schall can
		// read, and guessing which column is which is exactly the guess that
		// is never allowed.
		if !everyRecordHasTwoFields(records) {
			return ParsedFile{}, fmt.Errorf(
				"%w: no column here is named artist, title, album, duration or ISRC", ErrUnreadableFile)
		}
		columns = map[string]int{"artist": 0, "title": 1}
	}

	for index, record := range body {
		line := index + 1
		if headed {
			line++
		}
		row := FileRow{
			Line:   line,
			Artist: field(record, columns, "artist"),
			Title:  field(record, columns, "title"),
			Album:  field(record, columns, "album"),
			ISRC:   normalizeISRC(field(record, columns, "isrc")),
		}
		row.DurationMS = readDuration(
			field(record, columns, "durationMs"), field(record, columns, "duration"))
		if row.Title == "" && row.ISRC == "" {
			if isBlank(record) {
				continue
			}
			parsed.Skipped = append(parsed.Skipped, SkippedRow{
				Line: line, Reason: "this row has no title and no ISRC",
			})
			continue
		}
		parsed.Rows = append(parsed.Rows, row)
	}
	parsed.Columns = foundColumns(columns)
	return finish(parsed)
}

// delimiterFor picks the separator. The extension says it where it can; a file
// whose first line holds more tabs than commas says it otherwise.
func delimiterFor(filename string, content []byte) rune {
	if strings.EqualFold(filepath.Ext(filename), ".tsv") {
		return '\t'
	}
	first := content
	if end := bytes.IndexByte(first, '\n'); end >= 0 {
		first = first[:end]
	}
	if bytes.Count(first, []byte{'\t'}) > bytes.Count(first, []byte{','}) {
		return '\t'
	}
	return ','
}

func readRecords(content []byte, delimiter rune) ([][]string, error) {
	reader := csv.NewReader(bytes.NewReader(content))
	reader.Comma = delimiter
	// Rows of different widths are ordinary in files people edited by hand,
	// and a quote in the middle of an unquoted field is ordinary in a title.
	reader.FieldsPerRecord = -1
	reader.LazyQuotes = true
	reader.TrimLeadingSpace = true

	records := make([][]string, 0, 64)
	for {
		record, err := reader.Read()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("%w: %s", ErrUnreadableFile, err)
		}
		records = append(records, record)
		if len(records) > MaxFileRows+1 {
			return nil, ErrTooManyRows
		}
	}
	return records, nil
}

// readHeader recognises the first record as column names. It is a header only
// if it names a title or an artist: an ISRC column alone over unnamed columns
// says nothing about what the other columns hold.
func readHeader(record []string) (map[string]int, bool) {
	columns := make(map[string]int, len(record))
	ambiguous := make(map[string]bool, len(record))
	for index, cell := range record {
		header := strings.ToLower(strings.TrimSpace(cell))
		name, known := columnNames[header]
		if !known {
			continue
		}
		weak := ambiguousColumnNames[header]
		if _, taken := columns[name]; taken && !(ambiguous[name] && !weak) {
			continue
		}
		columns[name] = index
		ambiguous[name] = weak
	}
	_, hasTitle := columns["title"]
	_, hasArtist := columns["artist"]
	return columns, hasTitle || hasArtist
}

func everyRecordHasTwoFields(records [][]string) bool {
	for _, record := range records {
		if isBlank(record) {
			continue
		}
		if len(record) != 2 {
			return false
		}
	}
	return true
}

func isBlank(record []string) bool {
	for _, cell := range record {
		if strings.TrimSpace(cell) != "" {
			return false
		}
	}
	return true
}

func field(record []string, columns map[string]int, name string) string {
	index, found := columns[name]
	if !found || index >= len(record) {
		return ""
	}
	return strings.TrimSpace(record[index])
}

func foundColumns(columns map[string]int) []string {
	var found []string
	for _, name := range []string{"title", "artist", "album", "durationMs", "duration", "isrc"} {
		if _, ok := columns[name]; ok {
			if name == "durationMs" || name == "duration" {
				name = "duration"
			}
			if len(found) > 0 && found[len(found)-1] == name {
				continue
			}
			found = append(found, name)
		}
	}
	return found
}

// readDuration takes milliseconds where a column gave them, and reads a
// "3:45" or a count of seconds otherwise. Anything else is no duration: a
// length nobody can read is silence, and silence never agrees with anything.
func readDuration(milliseconds, written string) int {
	if milliseconds != "" {
		if value, err := strconv.Atoi(strings.TrimSpace(milliseconds)); err == nil && value > 0 {
			return value
		}
		return 0
	}
	written = strings.TrimSpace(written)
	if written == "" {
		return 0
	}
	if minutes, seconds, found := strings.Cut(written, ":"); found {
		minute, minuteErr := strconv.Atoi(strings.TrimSpace(minutes))
		second, secondErr := strconv.Atoi(strings.TrimSpace(seconds))
		if minuteErr != nil || secondErr != nil || minute < 0 || second < 0 || second > 59 {
			return 0
		}
		return (minute*60 + second) * 1000
	}
	if value, err := strconv.Atoi(written); err == nil && value > 0 {
		return value * 1000
	}
	return 0
}

// normalizeISRC keeps a registration code only when it has the shape of one:
// two characters for the country, three for the registrant, two digits for the
// year and five for the recording, written with or without hyphens. An ISRC
// concludes a match on its own, so a code of any other shape is dropped rather
// than carried — a wrong identifier is worse than none.
func normalizeISRC(written string) string {
	var built strings.Builder
	for _, letter := range strings.TrimSpace(written) {
		switch {
		case letter >= 'a' && letter <= 'z':
			built.WriteRune(letter - 'a' + 'A')
		case letter >= 'A' && letter <= 'Z', letter >= '0' && letter <= '9':
			built.WriteRune(letter)
		case letter == '-' || letter == ' ':
		default:
			return ""
		}
	}
	code := built.String()
	if len(code) != 12 {
		return ""
	}
	for index, letter := range code {
		digit := letter >= '0' && letter <= '9'
		switch {
		case index < 2 && digit:
			return ""
		case index >= 5 && !digit:
			return ""
		}
	}
	return code
}

// finish numbers the rows and gives each one its identity for a second import.
func finish(parsed ParsedFile) (ParsedFile, error) {
	if len(parsed.Rows) > MaxFileRows {
		return ParsedFile{}, ErrTooManyRows
	}
	for index := range parsed.Rows {
		parsed.Rows[index].Position = index + 1
		parsed.Rows[index].Key = rowKey(parsed.Rows[index])
	}
	return parsed, nil
}

// rowKey is what a second import of the same file reconciles by.
//
// It is an identity for a row of text, never a claim about music: two rows
// that say the same words are one entry on one list, which is the same rule
// the Spotify import applies to one track listed twice. Nothing downstream
// reads it, and matching never sees it.
//
// A path is read first where the row named one. An M3U is a list of files, and
// two of its lines can name two different files while saying the same words
// above them — the same song ripped twice, or the same tags on two takes. The
// words alone made those one line and dropped the second path. Only an M3U
// carries a path and only a CSV column carries an ISRC, so this changes what an
// M3U reconciles by and leaves every other file reading exactly as it did.
func rowKey(row FileRow) string {
	if row.LibraryPath != "" {
		return "path:" + row.LibraryPath
	}
	if row.ISRC != "" {
		return "isrc:" + row.ISRC
	}
	return "text:" + strings.ToLower(strings.Join(
		[]string{row.Artist, row.Title, row.Album}, "\x1f"))
}

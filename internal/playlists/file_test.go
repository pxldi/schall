package playlists

import (
	"errors"
	"strings"
	"testing"
)

func TestParseFileReadsAnExportifyCSV(t *testing.T) {
	content := "\ufeffTrack URI,Track Name,Artist Name(s),Album Name,Duration (ms),ISRC\r\n" +
		"spotify:track:1,Blue Monday,New Order,Substance,450000,GBAAA8500001\r\n" +
		"spotify:track:2,\"Hey, Hey\",The Band,\"Live, In Tokyo\",210500,\r\n"

	parsed, err := ParseFile("Exported.csv", []byte(content))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if parsed.Name != "Exported" || parsed.Format != "csv" {
		t.Fatalf("name %q format %q", parsed.Name, parsed.Format)
	}
	if got := strings.Join(parsed.Columns, ","); got != "title,artist,album,duration,isrc" {
		t.Fatalf("columns %q", got)
	}
	if len(parsed.Rows) != 2 {
		t.Fatalf("rows %d", len(parsed.Rows))
	}
	first := parsed.Rows[0]
	if first.Title != "Blue Monday" || first.Artist != "New Order" || first.Album != "Substance" {
		t.Fatalf("first row %+v", first)
	}
	if first.DurationMS != 450000 || first.ISRC != "GBAAA8500001" {
		t.Fatalf("first row evidence %+v", first)
	}
	if first.Key != "isrc:GBAAA8500001" {
		t.Fatalf("first key %q", first.Key)
	}
	second := parsed.Rows[1]
	if second.Title != "Hey, Hey" || second.Album != "Live, In Tokyo" {
		t.Fatalf("quoted commas %+v", second)
	}
	if second.ISRC != "" || second.Position != 2 {
		t.Fatalf("second row %+v", second)
	}
}

func TestParseFileReadsATwoColumnCSV(t *testing.T) {
	parsed, err := ParseFile("list.csv", []byte("New Order,Blue Monday\nAphex Twin,Xtal\n"))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if len(parsed.Rows) != 2 {
		t.Fatalf("rows %d", len(parsed.Rows))
	}
	if parsed.Rows[1].Artist != "Aphex Twin" || parsed.Rows[1].Title != "Xtal" {
		t.Fatalf("row %+v", parsed.Rows[1])
	}
	if parsed.Rows[0].Key != "text:new order\x1fblue monday\x1f" {
		t.Fatalf("key %q", parsed.Rows[0].Key)
	}
}

func TestParseFileRefusesColumnsItCannotName(t *testing.T) {
	_, err := ParseFile("mystery.csv", []byte("a,b,c\n1,2,3\n"))
	if !errors.Is(err, ErrUnreadableFile) {
		t.Fatalf("want ErrUnreadableFile, got %v", err)
	}
}

func TestParseFileSkipsARowWithNothingToSearchFor(t *testing.T) {
	parsed, err := ParseFile("list.csv", []byte("Title,Artist\nXtal,Aphex Twin\n,\n \n"))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if len(parsed.Rows) != 1 {
		t.Fatalf("rows %d", len(parsed.Rows))
	}
	if len(parsed.Skipped) != 0 {
		t.Fatalf("a blank row is not a skipped row: %+v", parsed.Skipped)
	}

	parsed, err = ParseFile("list.csv", []byte("Title,Artist,ISRC\nXtal,Aphex Twin,\n,Nobody,\n"))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if len(parsed.Skipped) != 1 || parsed.Skipped[0].Line != 3 {
		t.Fatalf("skipped %+v", parsed.Skipped)
	}
}

func TestParseFileKeepsOnlyAnISRCShapedLikeOne(t *testing.T) {
	parsed, err := ParseFile("list.csv", []byte(
		"Title,ISRC\nOne,gb-aaa-85-00001\nTwo,not-an-isrc\nThree,12AAA8500001\n"))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if parsed.Rows[0].ISRC != "GBAAA8500001" {
		t.Fatalf("hyphens and case: %q", parsed.Rows[0].ISRC)
	}
	if parsed.Rows[1].ISRC != "" || parsed.Rows[2].ISRC != "" {
		t.Fatalf("a code of the wrong shape is not carried: %+v", parsed.Rows[1:])
	}
}

func TestParseFileReadsDurationsPeopleWrite(t *testing.T) {
	parsed, err := ParseFile("list.csv", []byte("Title,Duration\nOne,3:45\nTwo,225\nThree,soon\n"))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	want := []int{225000, 225000, 0}
	for index, row := range parsed.Rows {
		if row.DurationMS != want[index] {
			t.Fatalf("row %d duration %d, want %d", index, row.DurationMS, want[index])
		}
	}
}

func TestParseFileReadsAnM3UWithEXTINF(t *testing.T) {
	content := "#EXTM3U\r\n#PLAYLIST:Evening\r\n#EXTINF:225,New Order - Blue Monday\r\n" +
		"/music/new order/blue monday.flac\r\n" +
		"#EXTINF:-1,Sunday Bloody Sunday - Live - Remastered\r\n" +
		"/music/u2/sunday.mp3\r\n"

	parsed, err := ParseFile("evening.m3u8", []byte(content))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if parsed.Name != "Evening" || parsed.Format != "m3u" {
		t.Fatalf("name %q format %q", parsed.Name, parsed.Format)
	}
	if len(parsed.Rows) != 2 {
		t.Fatalf("rows %d", len(parsed.Rows))
	}
	first := parsed.Rows[0]
	if first.Artist != "New Order" || first.Title != "Blue Monday" || first.DurationMS != 225000 {
		t.Fatalf("first row %+v", first)
	}
	if first.LibraryPath != "/music/new order/blue monday.flac" {
		t.Fatalf("path %q", first.LibraryPath)
	}
	// Two " - " separators: the line is kept whole rather than cut in a place
	// nobody can defend.
	second := parsed.Rows[1]
	if second.Artist != "" || second.Title != "Sunday Bloody Sunday - Live - Remastered" {
		t.Fatalf("second row %+v", second)
	}
	if second.DurationMS != 0 {
		t.Fatalf("an unknown length is no length: %d", second.DurationMS)
	}
}

func TestParseFileReadsAnM3UWithoutEXTINF(t *testing.T) {
	parsed, err := ParseFile("plain.m3u", []byte("/music/a.flac\n\n/music/b.flac\n"))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if len(parsed.Rows) != 2 {
		t.Fatalf("rows %d", len(parsed.Rows))
	}
	if parsed.Rows[0].Title != "/music/a.flac" || parsed.Rows[0].Artist != "" {
		t.Fatalf("row %+v", parsed.Rows[0])
	}
	if parsed.Rows[1].LibraryPath != "/music/b.flac" || parsed.Rows[1].Position != 2 {
		t.Fatalf("row %+v", parsed.Rows[1])
	}
	if parsed.Name != "plain" {
		t.Fatalf("name %q", parsed.Name)
	}
}

func TestParseFileReadsAnM3UByItsFirstLine(t *testing.T) {
	parsed, err := ParseFile("list", []byte("#EXTM3U\n#EXTINF:10,A - B\n/music/a.flac\n"))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if parsed.Format != "m3u" || len(parsed.Rows) != 1 {
		t.Fatalf("parsed %+v", parsed)
	}
}

func TestSplitArtistTitleOnlyCutsOneSeparator(t *testing.T) {
	cases := map[string][2]string{
		"New Order - Blue Monday": {"New Order", "Blue Monday"},
		"Blue Monday":             {"", "Blue Monday"},
		"A - B - C":               {"", "A - B - C"},
		"Bad-Tempered - Clavier":  {"Bad-Tempered", "Clavier"},
		" - Blue Monday":          {"", "- Blue Monday"},
	}
	for text, want := range cases {
		artist, title := splitArtistTitle(text)
		if artist != want[0] || title != want[1] {
			t.Fatalf("%q became %q / %q, want %q / %q", text, artist, title, want[0], want[1])
		}
	}
}

func TestParseFileRefusesAFileWithTooManyRows(t *testing.T) {
	var built strings.Builder
	built.WriteString("Title,Artist\n")
	for index := range MaxFileRows + 5 {
		built.WriteString("Song ")
		built.WriteString(string(rune('a' + index%26)))
		built.WriteString(",Somebody\n")
	}
	if _, err := ParseFile("big.csv", []byte(built.String())); !errors.Is(err, ErrTooManyRows) {
		t.Fatalf("want ErrTooManyRows, got %v", err)
	}
}

// Half the exporters write the song's name under "Track" and the other half the
// number it sits at. A file with both columns is the second kind, and the
// numbers are not titles.
func TestParseFileReadsTheTitleColumnWhenTrackHoldsNumbers(t *testing.T) {
	content := "Track,Artist,Title,Album\r\n" +
		"1,New Order,Blue Monday,Substance\r\n" +
		"2,New Order,Temptation,Substance\r\n"

	parsed, err := ParseFile("numbered.csv", []byte(content))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if len(parsed.Rows) != 2 {
		t.Fatalf("rows %d", len(parsed.Rows))
	}
	if parsed.Rows[0].Title != "Blue Monday" || parsed.Rows[1].Title != "Temptation" {
		t.Fatalf("titles %q and %q", parsed.Rows[0].Title, parsed.Rows[1].Title)
	}
}

// A file with only a Track column still reads it as the title: it is the only
// thing there that could be one.
func TestParseFileStillReadsTrackAsTheTitleOnItsOwn(t *testing.T) {
	parsed, err := ParseFile("plain.csv", []byte("Track,Artist\r\nBlue Monday,New Order\r\n"))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if len(parsed.Rows) != 1 || parsed.Rows[0].Title != "Blue Monday" {
		t.Fatalf("rows %+v", parsed.Rows)
	}
}

// An M3U is a list of files. Two lines can name two different files under the
// same words — the same song ripped twice, or one set of tags on two takes —
// and reconciling by the words alone dropped the second path.
func TestParseFileKeepsTwoM3ULinesThatNameDifferentFiles(t *testing.T) {
	content := "#EXTM3U\r\n" +
		"#EXTINF:225,New Order - Blue Monday\r\n/music/new order/blue monday.flac\r\n" +
		"#EXTINF:225,New Order - Blue Monday\r\n/music/singles/blue monday.mp3\r\n"

	parsed, err := ParseFile("twice.m3u", []byte(content))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if len(parsed.Rows) != 2 {
		t.Fatalf("rows %d", len(parsed.Rows))
	}
	if parsed.Rows[0].Key == parsed.Rows[1].Key {
		t.Fatalf("two files share one key: %q", parsed.Rows[0].Key)
	}
}

package library

import (
	"bytes"
	"context"
	"encoding/binary"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/rs/zerolog"

	"go.senan.xyz/taglib"

	"github.com/pxldi/schall/internal/dbtest"
	"github.com/pxldi/schall/internal/loudness"
)

// needsFfmpeg skips a test on a machine with no ffmpeg. Measuring loudness is
// the only thing that needs one, and Schall runs perfectly well without it.
func needsFfmpeg(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("ffmpeg"); err != nil {
		t.Skip("ffmpeg is not installed")
	}
}

// indexed records a file the way a scan would, with nothing measured about how
// loud it is.
func indexedFile(t *testing.T, pool *pgxpool.Pool, rootID uuid.UUID, path string) uuid.UUID {
	t.Helper()
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	var fileID uuid.UUID
	if err := pool.QueryRow(context.Background(), `
		INSERT INTO library_files (library_root_id, path, size_bytes, modified_at)
		VALUES ($1, $2, $3, $4) RETURNING id
	`, rootID, path, info.Size(), databaseTime(info.ModTime())).Scan(&fileID); err != nil {
		t.Fatal(err)
	}
	return fileID
}

// measurementOf reads back what the pass wrote down about one file.
func measurementOf(t *testing.T, pool *pgxpool.Pool, fileID uuid.UUID) (*float64, *float64, bool) {
	t.Helper()
	var gain, peak *float64
	var asked bool
	if err := pool.QueryRow(context.Background(), `
		SELECT replaygain_track_gain, replaygain_track_peak,
		       loudness_measured_at IS NOT NULL
		FROM library_files WHERE id = $1
	`, fileID).Scan(&gain, &peak, &asked); err != nil {
		t.Fatal(err)
	}
	return gain, peak, asked
}

// sineFile writes one second of a stereo 1 kHz sine at the given peak
// amplitude. See internal/loudness's own test for why that signal's loudness
// can be worked out on paper: it measures 20*log10(amplitude) LUFS, so the gain
// to the -18 LUFS reference is -18 minus that.
func sineFile(t *testing.T, path string, amplitude float64) {
	t.Helper()
	const rate = 44100
	var samples bytes.Buffer
	for frame := range rate {
		value := amplitude * math.Sin(2*math.Pi*1000*float64(frame)/rate)
		sample := int16(math.Round(value * 32767))
		_ = binary.Write(&samples, binary.LittleEndian, sample)
		_ = binary.Write(&samples, binary.LittleEndian, sample)
	}

	var file bytes.Buffer
	data := samples.Bytes()
	file.WriteString("RIFF")
	_ = binary.Write(&file, binary.LittleEndian, uint32(36+len(data)))
	file.WriteString("WAVEfmt ")
	_ = binary.Write(&file, binary.LittleEndian, uint32(16))
	_ = binary.Write(&file, binary.LittleEndian, uint16(1))
	_ = binary.Write(&file, binary.LittleEndian, uint16(2))
	_ = binary.Write(&file, binary.LittleEndian, uint32(rate))
	_ = binary.Write(&file, binary.LittleEndian, uint32(rate*4))
	_ = binary.Write(&file, binary.LittleEndian, uint16(4))
	_ = binary.Write(&file, binary.LittleEndian, uint16(16))
	file.WriteString("data")
	_ = binary.Write(&file, binary.LittleEndian, uint32(len(data)))
	file.Write(data)

	if err := os.WriteFile(path, file.Bytes(), 0o600); err != nil {
		t.Fatal(err)
	}
}

func newLoudnessSweep(pool *pgxpool.Pool, binary string) *Loudness {
	return NewLoudness(pool, loudness.NewMeasurer(binary), zerolog.Nop())
}

func TestTheLoudnessPassWritesDownHowLoudEachFileIs(t *testing.T) {
	needsFfmpeg(t)
	pool := dbtest.Setup(t)
	rootID, root := libraryRoot(t, pool)

	half := filepath.Join(root, "half.wav")
	sineFile(t, half, 0.5)
	quarter := filepath.Join(root, "quarter.wav")
	sineFile(t, quarter, 0.25)
	halfID := indexedFile(t, pool, rootID, half)
	quarterID := indexedFile(t, pool, rootID, quarter)

	more, err := newLoudnessSweep(pool, "").Sweep(context.Background())
	if err != nil {
		t.Fatalf("sweep: %v", err)
	}
	if more {
		t.Fatal("the pass says there is more to do with two files measured")
	}

	for _, want := range []struct {
		name   string
		fileID uuid.UUID
		gain   float64
		peak   float64
	}{
		{"half", halfID, -11.98, 0.5},
		{"quarter", quarterID, -5.96, 0.25},
	} {
		gain, peak, asked := measurementOf(t, pool, want.fileID)
		if !asked {
			t.Fatalf("%s was never recorded as measured", want.name)
		}
		if gain == nil {
			t.Fatalf("%s has no gain", want.name)
		}
		if math.Abs(*gain-want.gain) > 0.5 {
			t.Fatalf("%s gain is %v dB, want %v dB within half a decibel", want.name, *gain, want.gain)
		}
		if peak == nil || math.Abs(*peak-want.peak) > 0.01 {
			t.Fatalf("%s peak is %v, want %v", want.name, peak, want.peak)
		}
	}
}

func TestAFileWithNoLoudnessToGiveIsAskedOnceAndLeftEmpty(t *testing.T) {
	needsFfmpeg(t)
	pool := dbtest.Setup(t)
	rootID, root := libraryRoot(t, pool)

	// A FLAC header with no audio behind it: the library holds files like this,
	// and a pass that left them unrecorded would read the same ten files on
	// every turn and never reach the eleventh.
	path := filepath.Join(root, "headers-only.flac")
	writeFLAC(t, path, 48000, 144000)
	fileID := indexedFile(t, pool, rootID, path)

	if _, err := newLoudnessSweep(pool, "").Sweep(context.Background()); err != nil {
		t.Fatalf("sweep: %v", err)
	}

	gain, _, asked := measurementOf(t, pool, fileID)
	if !asked {
		t.Fatal("the file was not recorded as asked, so the pass will read it again forever")
	}
	if gain != nil {
		t.Fatalf("gain is %v, want nothing", *gain)
	}
}

func TestAnInstallationWithNoFfmpegWritesNothingDown(t *testing.T) {
	pool := dbtest.Setup(t)
	rootID, root := libraryRoot(t, pool)
	path := filepath.Join(root, "anything.wav")
	sineFile(t, path, 0.5)
	fileID := indexedFile(t, pool, rootID, path)

	more, err := newLoudnessSweep(pool, "schall-no-such-ffmpeg").Sweep(context.Background())
	if err != nil {
		t.Fatalf("sweep: %v", err)
	}
	if more {
		t.Fatal("the pass asked to come back although nothing can be measured")
	}

	// Nothing is written off. An installation that gains ffmpeg later measures
	// these files rather than skipping them forever.
	_, _, asked := measurementOf(t, pool, fileID)
	if asked {
		t.Fatal("the file was recorded as measured although nothing measured it")
	}
}

// secondTrack adds another track to a release the test already made, so the
// album can be complete or incomplete on purpose.
func secondTrack(t *testing.T, pool *pgxpool.Pool, release taggedRelease, title string) uuid.UUID {
	t.Helper()
	var trackID uuid.UUID
	if err := pool.QueryRow(context.Background(), `
		INSERT INTO tracks (album_id, musicbrainz_recording_id, title, disc_number, track_number)
		VALUES ($1, $2, $3, 1, 5) RETURNING id
	`, release.albumID, uuid.New(), title).Scan(&trackID); err != nil {
		t.Fatal(err)
	}
	return trackID
}

// wasMeasured writes a measurement onto a file the way the pass would, so a
// test about what gets written into tags does not need ffmpeg to arrange it.
func wasMeasured(t *testing.T, pool *pgxpool.Pool, fileID uuid.UUID, gain, peak float64, durationMS int) {
	t.Helper()
	if _, err := pool.Exec(context.Background(), `
		UPDATE library_files
		SET replaygain_track_gain = $2, replaygain_track_peak = $3,
		    measured_duration_ms = $4, loudness_measured_at = now()
		WHERE id = $1
	`, fileID, gain, peak, durationMS); err != nil {
		t.Fatal(err)
	}
}

func TestAWholeRecordMeasuredIsWrittenWithTheAlbumsGain(t *testing.T) {
	pool := dbtest.Setup(t)
	rootID, root := libraryRoot(t, pool)
	release := catalogueRelease(t, pool, "AgonyOST", "Halfway House Near Me", "Walk to the Closed Bodega")
	otherTrack := secondTrack(t, pool, release, "Walk Back")

	first := filepath.Join(root, "01.flac")
	second := filepath.Join(root, "02.flac")
	firstID := peersCopy(t, pool, rootID, first)
	secondID := peersCopy(t, pool, rootID, second)
	mapFileToTrack(t, pool, firstID, release.trackID)
	mapFileToTrack(t, pool, secondID, otherTrack)
	// Both tracks at the same loudness, so the album's gain is that loudness
	// however long either track is.
	wasMeasured(t, pool, firstID, -6, 0.5, 120_000)
	wasMeasured(t, pool, secondID, -6, 0.9, 240_000)

	tagger := newTagger(pool)
	jobID := queueTagging(t, tagger)
	if _, err := tagger.Tag(context.Background(), jobID); err != nil {
		t.Fatalf("tag the library: %v", err)
	}

	for _, path := range []string{first, second} {
		present := readTagsOf(t, path)
		if got := present["REPLAYGAIN_ALBUM_GAIN"]; len(got) != 1 || got[0] != "-6.00 dB" {
			t.Fatalf("%s album gain is %q, want -6.00 dB", filepath.Base(path), got)
		}
		if got := present["REPLAYGAIN_ALBUM_PEAK"]; len(got) != 1 || got[0] != "0.900000" {
			t.Fatalf("%s album peak is %q, want the loudest track's", filepath.Base(path), got)
		}
	}
	if got := readTagsOf(t, first)["REPLAYGAIN_TRACK_GAIN"]; len(got) != 1 || got[0] != "-6.00 dB" {
		t.Fatalf("track gain is %q, want -6.00 dB", got)
	}
}

func TestHalfARecordMeasuredIsWrittenWithNoAlbumGain(t *testing.T) {
	pool := dbtest.Setup(t)
	rootID, root := libraryRoot(t, pool)
	release := catalogueRelease(t, pool, "AgonyOST", "Halfway House Near Me", "Walk to the Closed Bodega")
	otherTrack := secondTrack(t, pool, release, "Walk Back")

	first := filepath.Join(root, "01.flac")
	second := filepath.Join(root, "02.flac")
	firstID := peersCopy(t, pool, rootID, first)
	secondID := peersCopy(t, pool, rootID, second)
	mapFileToTrack(t, pool, firstID, release.trackID)
	mapFileToTrack(t, pool, secondID, otherTrack)
	// Only one of the two tracks was measured, so the record cannot be levelled
	// as a record. Each file gets its own gain and nothing else.
	wasMeasured(t, pool, firstID, -6, 0.5, 120_000)

	tagger := newTagger(pool)
	jobID := queueTagging(t, tagger)
	if _, err := tagger.Tag(context.Background(), jobID); err != nil {
		t.Fatalf("tag the library: %v", err)
	}

	present := readTagsOf(t, first)
	if got := present["REPLAYGAIN_TRACK_GAIN"]; len(got) != 1 || got[0] != "-6.00 dB" {
		t.Fatalf("track gain is %q, want -6.00 dB", got)
	}
	if got := present["REPLAYGAIN_ALBUM_GAIN"]; len(got) != 0 {
		t.Fatalf("album gain is %q, want none for a record that is not all here", got)
	}
	// And the unmeasured file gets no gain at all rather than a gain of zero.
	if got := readTagsOf(t, second)["REPLAYGAIN_TRACK_GAIN"]; len(got) != 0 {
		t.Fatalf("the unmeasured file's gain is %q, want none", got)
	}
}

// readTagsOf is the tags on a file, read the way a music server reads them.
func readTagsOf(t *testing.T, path string) map[string][]string {
	t.Helper()
	present, err := taglib.ReadTags(path)
	if err != nil {
		t.Fatalf("read the tags on %s: %v", path, err)
	}
	return present
}

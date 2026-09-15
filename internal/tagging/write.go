package tagging

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
	"go.senan.xyz/taglib"

	"github.com/pxldi/schall/internal/filecopy"
)

// stagingPrefix names the copy a tag write is made on. It is not audio by
// extension, so a scan walking the folder mid-write passes it by, and a process
// that dies leaves it behind rather than a half-written file.
const stagingPrefix = ".schall-tag-"

// Arrived is what a file said about itself before Schall wrote its own account
// into it — the fields matching reads as evidence, exactly as the stranger who
// sent the file left them.
// It holds two kinds of field. The plain ones are readings: the artist a
// reader would take the file for, the position as a number. They are what
// matching compares, and a reading is not always a tag the file carries. The
// ones ending in Tag are the tags themselves, each exactly as the file wrote
// it, and they are what a revert puts back — see Revert.
//
// A record written before the Tag fields existed holds the readings alone. A
// revert of such a file restores what the record holds, which is what it always
// did; nothing here invents the tag a reading came from.
type Arrived struct {
	Artist                 string     `json:"artist,omitempty"`
	Album                  string     `json:"album,omitempty"`
	Title                  string     `json:"title,omitempty"`
	DiscNumber             int        `json:"discNumber,omitempty"`
	TrackNumber            int        `json:"trackNumber,omitempty"`
	ISRC                   string     `json:"isrc,omitempty"`
	MusicBrainzRecordingID *uuid.UUID `json:"musicbrainzRecordingId,omitempty"`
	// ArtistTag and AlbumArtistTag are the file's two artist tags. A file
	// carries either, both or neither, and a copy on a compilation carries two
	// that disagree — the track's artist and the compilation's. Artist above is
	// the album's where there is one and the track's otherwise, which is the
	// one value a reader ends up with; both tags are kept here because putting
	// that one value back into one field is not the file the peer sent.
	ArtistTag      string `json:"artistTag,omitempty"`
	AlbumArtistTag string `json:"albumArtistTag,omitempty"`
	// TrackNumberTag and DiscNumberTag are the positions as the file wrote
	// them. A position is often written "4/12", which says the track is the
	// fourth of twelve, and the number read out of it is only the first half.
	TrackNumberTag string `json:"trackNumberTag,omitempty"`
	DiscNumberTag  string `json:"discNumberTag,omitempty"`
	// GenreTag is the file's own GENRE tag, exactly as the peer wrote it. Genre
	// is claimed the same way the artist and position tags are — write.go's
	// keyGenre is on replaced — so a revert has to give the peer's word back
	// rather than clear the tag Schall put there. A record written before this
	// field existed holds no genre reading at all, and a revert of such a file
	// leaves GENRE cleared, which is the same bargain TrackNumberTag makes.
	GenreTag string `json:"genreTag,omitempty"`
	// ReplayGain is the loudness tags the file arrived carrying, and its being
	// here at all is the record that Schall has written loudness into this
	// file. That distinction is the whole reason it is a pointer.
	//
	// Schall wrote no loudness before this field existed, and it writes none
	// into a file it could not measure. A record from either case has no
	// ReplayGain in it, and what stands in the file is the peer's own — so a
	// revert must leave it exactly where it is. A record that has one is a file
	// whose gain tags Schall took over, and the revert gives back whatever this
	// holds, which for a peer who shipped none is nothing at all.
	//
	// An empty value cannot say that on its own: "the peer had no gain tags"
	// and "nobody ever asked" would both be the empty string. So presence is
	// the answer and the values inside it are the detail.
	ReplayGain *ArrivedReplayGain `json:"replayGain,omitempty"`
}

// ArrivedReplayGain is the four loudness tags exactly as the file carried them,
// each kept as the string it was rather than as a number read out of it. A peer
// who wrote "-7.2 dB" gets "-7.2 dB" back, not "-7.20 dB": a revert puts the
// file back, and rounding a stranger's tag to Schall's own precision is Schall
// still writing after it said it had stopped.
type ArrivedReplayGain struct {
	TrackGain string `json:"trackGain,omitempty"`
	TrackPeak string `json:"trackPeak,omitempty"`
	AlbumGain string `json:"albumGain,omitempty"`
	AlbumPeak string `json:"albumPeak,omitempty"`
}

// Write records into the file what Schall proved it to be.
//
// The bytes are the user's only copy, so nothing is written to them. The file
// is copied beside itself, the copy is tagged, the copy is read back to prove
// it is still audio saying what it was meant to say, and only then is it
// renamed over the original. A rename within a directory is atomic: at every
// instant the path holds either the whole file as it was or the whole file as
// it now is, and a process that dies anywhere in between leaves the original
// untouched with a staging file beside it, which the next write removes.
//
// Every write also records what the file arrived carrying, so that what Schall
// writes can never be read back as though a stranger had written it. See
// AsArrived.
func Write(path string, tags Tags) error {
	if !tags.proved() {
		return ErrNothingProved
	}
	info, err := os.Stat(path)
	if err != nil {
		return fmt.Errorf("read the file to tag %s: %w", path, err)
	}
	present, err := taglib.ReadTags(path)
	if err != nil {
		return fmt.Errorf("read the tags on %s: %w", path, err)
	}

	written := tags.values()
	// A file tagged twice keeps the account of its first arrival. The second
	// write's "before" is Schall's own first write, and recording that would
	// hand Schall's claims back to matching as the stranger's.
	written[keyTagsAsArrived] = []string{asArrivedRecord(present, written[keyTrackGain] != nil)}
	written[keyTaggedAt] = []string{time.Now().UTC().Format(time.RFC3339)}

	return apply(path, info.Mode().Perm(), written)
}

// Revert takes back what Schall wrote into a file, and reports whether it had
// anything to take back.
//
// A file is tagged because Schall proved which catalogue track it is. When it
// stops being that track's — somebody withdrew the decision they made by hand,
// somebody gave the track to another file, a re-evaluation left the file with
// no track it can be — the account in the file is a claim nothing stands behind
// any more, and a music server goes on grouping the file under an album it was
// never proved to be from. So the file is given back the account it arrived
// with, which is the one written down in it at the first write.
//
// A file Schall never tagged is left alone and answered false: nothing here
// writes over a stranger's tags without having replaced them first. A file
// marked as Schall's whose record cannot be read is a file whose own account is
// lost, and everything Schall wrote into it is cleared rather than left
// standing — the same bargain the empty record makes in asArrivedRecord.
//
// What the record does not hold cannot come back. The date, the credit lists
// and the release identifiers are Schall's own writing, so they are removed;
// the recording identifier and the ISRC were never written and are not touched
// here either. The loudness tags are given back where the record says Schall
// wrote them and left alone where it does not — see Arrived.ReplayGain, which
// is a pointer for exactly that reason.
func Revert(path string) (bool, error) {
	info, err := os.Stat(path)
	if err != nil {
		return false, fmt.Errorf("read the file to restore %s: %w", path, err)
	}
	present, err := taglib.ReadTags(path)
	if err != nil {
		return false, fmt.Errorf("read the tags on %s: %w", path, err)
	}
	arrived, tagged := arrivedIn(present)
	if !tagged {
		return false, nil
	}
	if err := apply(path, info.Mode().Perm(), arrived.values()); err != nil {
		return false, err
	}
	return true, nil
}

// values renders the file's own account in the shape TagLib writes. Every name
// Schall claims is present: the ones the file arrived carrying take their value
// back, and the rest are emptied, which is how a tag is removed. The two marks
// go with them — once the file says what it said before Schall wrote, there is
// no Schall account in it for a mark to point at.
//
// The numbers on the disc are cleared here where they are not cleared by a
// write. A write leaves a position it does not know alone, because a stranger's
// track number still orders an album; a revert has the file's own position
// written down and puts exactly that back — "4/12" and all, since a position
// written as one of twelve says something the number four does not — and puts
// nothing back where the file arrived without one.
//
// Both artist tags go back where they were. A file carries an album artist, a
// track artist, both or neither, and the two disagree on every compilation
// there is; a record that kept only the value a reader ends up with would give
// the file back a single artist it never had.
//
// A record written before those tags were kept holds the readings alone, and
// those are what it restores: the one artist into ARTIST, the position as the
// number it was read as. It is what the file has said since the day it was
// tagged, and inventing the rest of a tag from a reading of it would be Schall
// writing the peer's account for him.
func (arrived Arrived) values() map[string][]string {
	written := make(map[string][]string, len(replaced)+4)
	for _, name := range replaced {
		written[name] = nil
	}
	written[keyTrackNumber] = nil
	written[keyDiscNumber] = nil
	written[keyTaggedAt] = nil
	written[keyTagsAsArrived] = nil
	setText(written, keyTitle, arrived.Title)
	setText(written, keyAlbum, arrived.Album)
	setText(written, keyGenre, arrived.GenreTag)
	if arrived.ArtistTag == "" && arrived.AlbumArtistTag == "" {
		setText(written, keyArtist, arrived.Artist)
	} else {
		setText(written, keyArtist, arrived.ArtistTag)
		setText(written, keyAlbumArtist, arrived.AlbumArtistTag)
	}
	if arrived.TrackNumberTag != "" {
		setText(written, keyTrackNumber, arrived.TrackNumberTag)
	} else {
		setNumber(written, keyTrackNumber, arrived.TrackNumber)
	}
	if arrived.DiscNumberTag != "" {
		setText(written, keyDiscNumber, arrived.DiscNumberTag)
	} else {
		setNumber(written, keyDiscNumber, arrived.DiscNumber)
	}
	// The loudness tags are given back only where the record says Schall took
	// them over. Where it says nothing, the gain in the file is the peer's own
	// and is left standing: a record with nothing about loudness in it is
	// either a file written before Schall measured anything or one it could not
	// measure, and clearing those four names would delete a stranger's work in
	// the name of undoing Schall's.
	if arrived.ReplayGain != nil {
		for _, name := range replayGainKeys {
			written[name] = nil
		}
		setText(written, keyTrackGain, arrived.ReplayGain.TrackGain)
		setText(written, keyTrackPeak, arrived.ReplayGain.TrackPeak)
		setText(written, keyAlbumGain, arrived.ReplayGain.AlbumGain)
		setText(written, keyAlbumPeak, arrived.ReplayGain.AlbumPeak)
	}
	return written
}

// apply puts a set of tag values into a file, and is the only place in this
// package that replaces the user's bytes. Both directions use it: the account
// Schall proved, and the account the file arrived with once Schall has nothing
// left to say about it. See Write for why it is a copy, a read-back and a
// rename rather than a write.
func apply(path string, mode os.FileMode, written map[string][]string) error {
	return rewrite(path, mode, func(staging string) error {
		if err := taglib.WriteTags(staging, written, 0); err != nil {
			return fmt.Errorf("write the tags for %s: %w", path, err)
		}
		if err := verify(staging, written); err != nil {
			return fmt.Errorf("tagged copy of %s: %w", path, err)
		}
		return nil
	})
}

// WriteArtwork puts a picture inside the file.
//
// A picture beside the music as cover.jpg is what most players read, and it is
// what internal/coverart writes. A file that leaves its folder — copied to a
// phone, handed to somebody — leaves that picture behind, so the picture goes
// into the file as well. This is display and never evidence: nothing in Schall
// reads an embedded picture back as a claim about what the file is.
//
// The same bargain as a tag write. The user's only copy is never written to:
// the file is copied beside itself, the copy is given the picture, the copy is
// read back to prove the picture is in it, and only then is it renamed over the
// original.
func WriteArtwork(path string, image []byte) error {
	if len(image) == 0 {
		return errors.New("there is no picture to write into the file")
	}
	info, err := os.Stat(path)
	if err != nil {
		return fmt.Errorf("read the file to picture %s: %w", path, err)
	}
	return rewrite(path, info.Mode().Perm(), func(staging string) error {
		if err := taglib.WriteImage(staging, image); err != nil {
			return fmt.Errorf("write the picture into %s: %w", path, err)
		}
		written, err := taglib.ReadImage(staging)
		if err != nil {
			return fmt.Errorf("read back the picture written into %s: %w", path, err)
		}
		if len(written) == 0 {
			return fmt.Errorf("the picture written into %s is not there when it is read back", path)
		}
		return nil
	})
}

// rewrite copies the file, changes the copy, and renames the copy over the
// original. Every change Schall makes to a file's own bytes goes through here,
// so that a process that dies anywhere in the middle leaves the original
// untouched with a staging file beside it, which the next write removes.
func rewrite(path string, mode os.FileMode, change func(staging string) error) error {
	staging := filepath.Join(filepath.Dir(path), stagingPrefix+filepath.Base(path)+".part")
	if err := os.Remove(staging); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("clear the staging file for %s: %w", path, err)
	}
	defer func() { _ = os.Remove(staging) }()
	if err := filecopy.Copy(path, staging); err != nil {
		return fmt.Errorf("copy %s to tag it: %w", path, err)
	}
	if err := os.Chmod(staging, mode); err != nil {
		return fmt.Errorf("keep the permissions of %s: %w", path, err)
	}
	if err := change(staging); err != nil {
		return err
	}
	if err := sync(staging); err != nil {
		return fmt.Errorf("commit the tagged copy of %s: %w", path, err)
	}
	if err := os.Rename(staging, path); err != nil {
		return fmt.Errorf("put the tagged copy of %s in place: %w", path, err)
	}
	return nil
}

// AlreadySays reports whether the file already carries exactly what these tags
// would write into it, down to the last empty value.
//
// It exists so that a pass over a library that has already been tagged is a
// read of each file rather than a rewrite of it. Rewriting costs a copy of the
// whole file and changes its size, its time and everything downstream that
// watches those, so a file with nothing to gain from a write is not written.
//
// The mark and the account of the file's arrival are not compared. They say
// when Schall wrote and what the file said before it; neither is part of what a
// music server reads, and a file whose own tags happen to agree with the
// catalogue already says everything a write would make it say — its tags being
// the stranger's is exactly what makes leaving them alone safe.
//
// A file that cannot be read at all is not up to date: let Write say why.
func AlreadySays(path string, tags Tags) bool {
	if !tags.proved() {
		return false
	}
	present, err := taglib.ReadTags(path)
	if err != nil {
		return false
	}
	for name, want := range tags.values() {
		if !slices.Equal(present[name], want) {
			return false
		}
	}
	return true
}

// AsArrived reports what a file said about itself before Schall tagged it, and
// whether Schall tagged it at all.
//
// This is what keeps the tags Schall writes out of its own evidence. A match is
// provable only from what someone other than Schall said — a recording
// identifier a stranger wrote, audio AcoustID recognised — and a file holding
// Schall's own conclusions would otherwise prove that conclusion to the next
// scan, which is no proof at all. So a managed copy hands matching the account
// it arrived with, and the account Schall wrote is for the music server alone.
//
// A file Schall tagged whose record cannot be read is silence rather than a
// fallback to the tags on it: an unreadable record is the one case where
// reading the file would mean reading Schall's own claims.
func AsArrived(path string) (Arrived, bool) {
	present, err := taglib.ReadTags(path)
	if err != nil {
		return Arrived{}, false
	}
	return arrivedIn(present)
}

// arrivedIn reads that record out of tags already in hand, so that a caller
// holding the file's tags does not read the file a second time to ask.
func arrivedIn(present map[string][]string) (Arrived, bool) {
	record, tagged := first(present, keyTagsAsArrived)
	if !tagged {
		if _, marked := first(present, keyTaggedAt); !marked {
			return Arrived{}, false
		}
		return Arrived{}, true
	}
	var arrived Arrived
	if err := json.Unmarshal([]byte(record), &arrived); err != nil {
		return Arrived{}, true
	}
	return arrived, true
}

// asArrivedRecord is the account of the file's own tags that travels with it.
// Where the file has been tagged before, that first account is the one kept.
//
// capturingReplayGain says whether this write is about to take the file's
// loudness tags over. It has to be told, because a file can be tagged twice —
// once before it was ever measured and again after — and the account kept from
// the first write was written when Schall had no claim on those tags. At that
// moment the gain tags standing in the file are still the peer's, so this is
// the last chance to write them down; a write that missed it would leave a
// revert unable to tell Schall's gain from the stranger's, and the safe answer
// then is to leave Schall's own numbers in the file forever.
func asArrivedRecord(present map[string][]string, capturingReplayGain bool) string {
	if record, ok := first(present, keyTagsAsArrived); ok {
		if !capturingReplayGain {
			return record
		}
		return withReplayGain(record, present)
	}
	// A music server reads an album's artist from ALBUMARTIST and falls back
	// to ARTIST, and so does the scanner; the record holds the one value the
	// scanner would have read, and beside it the two tags that value was read
	// from, because a file with an album artist and a different track artist is
	// two tags and no single value puts it back.
	artist, ok := first(present, keyAlbumArtist)
	if !ok {
		artist, _ = first(present, keyArtist)
	}
	arrived := Arrived{Artist: artist}
	arrived.ArtistTag, _ = first(present, keyArtist)
	arrived.AlbumArtistTag, _ = first(present, keyAlbumArtist)
	arrived.Album, _ = first(present, keyAlbum)
	arrived.Title, _ = first(present, keyTitle)
	arrived.GenreTag, _ = first(present, keyGenre)
	arrived.ISRC, _ = first(present, keyISRC)
	if number, ok := first(present, keyDiscNumber); ok {
		arrived.DiscNumberTag = number
		arrived.DiscNumber = leadingNumber(number)
	}
	if number, ok := first(present, keyTrackNumber); ok {
		arrived.TrackNumberTag = number
		arrived.TrackNumber = leadingNumber(number)
	}
	if raw, ok := first(present, keyRecordingID); ok {
		if recordingID, err := uuid.Parse(strings.TrimSpace(raw)); err == nil {
			arrived.MusicBrainzRecordingID = &recordingID
		}
	}
	if capturingReplayGain {
		arrived.ReplayGain = replayGainIn(present)
	}
	record, err := json.Marshal(arrived)
	if err != nil {
		// Arrived is strings and numbers; there is no value of it that cannot
		// be written. An empty record still marks the file as Schall's, which
		// is the conservative half of the bargain.
		return "{}"
	}
	return string(record)
}

// withReplayGain adds the file's own loudness tags to an account written before
// Schall had any claim on them, and leaves an account that already has them
// alone. The one that already has them was written by an earlier measured
// write, so the tags in the file now are Schall's own and writing them down
// would record Schall's numbers as the stranger's.
//
// An account that will not parse is returned untouched. Such a file's own
// record is already lost — Revert says so — and the conservative answer there
// is to leave the loudness tags where they are rather than to guess whose they
// were.
func withReplayGain(record string, present map[string][]string) string {
	var arrived Arrived
	if err := json.Unmarshal([]byte(record), &arrived); err != nil {
		return record
	}
	if arrived.ReplayGain != nil {
		return record
	}
	arrived.ReplayGain = replayGainIn(present)
	updated, err := json.Marshal(arrived)
	if err != nil {
		return record
	}
	return string(updated)
}

// replayGainIn reads the four loudness tags out of what the file carries. It
// always answers with a record, empty where the file carried none: the record
// existing is what says Schall took these tags over, and a file that arrived
// with no gain of its own is one a revert leaves with none.
func replayGainIn(present map[string][]string) *ArrivedReplayGain {
	gain := &ArrivedReplayGain{}
	gain.TrackGain, _ = first(present, keyTrackGain)
	gain.TrackPeak, _ = first(present, keyTrackPeak)
	gain.AlbumGain, _ = first(present, keyAlbumGain)
	gain.AlbumPeak, _ = first(present, keyAlbumPeak)
	return gain
}

// verify reads the tagged copy back before anything replaces the original.
//
// Every value is checked, not a sample of them: a format that cannot hold a
// list of artists, or that rewrites a date into a shape of its own, is a format
// this file must not be silently traded for. A copy that does not say exactly
// what it was told to say is a failed write, and the original stays.
func verify(staging string, written map[string][]string) error {
	properties, err := taglib.ReadProperties(staging)
	if err != nil {
		return fmt.Errorf("is no longer readable audio: %w", err)
	}
	if properties.SampleRate == 0 {
		return errors.New("is no longer readable audio")
	}
	present, err := taglib.ReadTags(staging)
	if err != nil {
		return fmt.Errorf("cannot be read back: %w", err)
	}
	for name, want := range written {
		if !slices.Equal(present[name], want) {
			return fmt.Errorf("carries %s as %q where %q was written", name, present[name], want)
		}
	}
	return nil
}

// sync puts the tagged copy's bytes on the disk rather than in the page cache
// before anything renames it over the original, so the rename never promises a
// file whose contents were still only in memory. Whether the rename itself has
// reached the disk is the directory's business, and Schall leaves that where
// every other write here leaves it.
func sync(path string) error {
	file, err := os.Open(path)
	if err != nil {
		return err
	}
	if err := file.Sync(); err != nil {
		_ = file.Close()
		return err
	}
	return file.Close()
}

func first(values map[string][]string, name string) (string, bool) {
	found := values[name]
	if len(found) == 0 || strings.TrimSpace(found[0]) == "" {
		return "", false
	}
	return strings.TrimSpace(found[0]), true
}

// leadingNumber reads the number a tag starts with, so that "4/12" is the
// fourth track rather than nothing at all.
func leadingNumber(value string) int {
	digits := strings.TrimSpace(value)
	if cut := strings.IndexAny(digits, "/-"); cut >= 0 {
		digits = digits[:cut]
	}
	number, err := strconv.Atoi(strings.TrimSpace(digits))
	if err != nil || number < 0 {
		return 0
	}
	return number
}

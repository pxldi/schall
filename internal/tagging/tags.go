// Package tagging writes into a managed copy what Schall proved it to be.
//
// Schall files music into folders, but a music server groups by tags, so the
// tidiness of the folder tree reaches nobody: a peer's spelling of a title is
// what the user sees, one album arrives under two names, and a credit written
// as one string becomes an artist nobody has heard of. What Schall holds about
// a file it imported — the recording, the release, the release group, the
// numbers on the disc — is strictly better than what the peer wrote, so it is
// written into the file.
//
// Two rules bound that. Only what was proved is written: a file nobody
// identified is left exactly as it is, and a field Schall cannot fill is left
// empty rather than invented. And nothing Schall writes may ever come back as
// evidence — see write.go, which records what the file arrived carrying so that
// matching keeps reading the stranger's account rather than Schall's own.
package tagging

import (
	"errors"
	"strconv"
	"strings"

	"github.com/google/uuid"
)

// Credit is one artist as MusicBrainz credits them, with the identifier that
// tells two artists of one name apart.
//
// A credit is a list, not a sentence. "Porter Robinson, Ninajirachi" written
// into one string is one artist called that, and neither real artist is
// reachable from it, so a credit is only ever split where MusicBrainz already
// split it — never by cutting a string at a comma, which would make a band
// whose name holds one into two artists forever.
type Credit struct {
	Name                string
	MusicBrainzArtistID uuid.UUID
}

// Tags is everything Schall proved about one file. Every field is optional
// except that something must be there: an empty Tags proves nothing and writes
// nothing.
type Tags struct {
	Title string
	Album string
	// Artist is the credit as MusicBrainz joins it — the display name, join
	// phrases and all. Artists is the same credit as the list it really is,
	// and is written only where Schall holds that list; where it holds the
	// joined string alone, the string is written and the list stays empty.
	Artist       string
	Artists      []Credit
	AlbumArtist  string
	AlbumArtists []Credit
	TrackNumber  int
	DiscNumber   int
	// Genre is what the MusicBrainz community voted this music is: the release
	// group's leading genre, or the artist's where the release group has none.
	// It is one word because a music server browses by one — a file under five
	// genres appears in five lists and the browse stops meaning anything.
	//
	// It is written like the rest: from the release group and artist the file
	// was proved to belong to, so the genre on the file is the genre of the
	// music on it. Where MusicBrainz holds no votes for either, nothing is
	// written and the tag is cleared, the same as an album Schall cannot name.
	Genre string
	// Date is the release date as MusicBrainz gives it — a year, a year and
	// month, or a full date. It is written verbatim; a partial date is a
	// partial date and nothing here completes one.
	Date string
	// RecordingID is the MusicBrainz recording: what the audio is. It is held
	// here because it is what Schall proved, and it is never written into the
	// file — see withheld, below, which is the whole reason this package can
	// exist without matching ending up reading Schall's own conclusions.
	//
	// ReleaseID is the release the file was published on and ReleaseGroupID
	// the work that release belongs to. Both are written; neither is evidence
	// of anything here. A release whose edition Schall has not chosen has no
	// ReleaseID.
	RecordingID    uuid.UUID
	ReleaseID      uuid.UUID
	ReleaseGroupID uuid.UUID
	// Comment is where a file that came from somewhere other than the
	// catalogue says where. A track Schall holds by its SoundCloud address
	// carries that address here, so that a person looking at the file in any
	// player can see where the music came from.
	//
	// It is written only when it is filled in, and it is not in replaced: a
	// file whose comment Schall has nothing to say about keeps the comment it
	// arrived with. Revert therefore does not clear a comment Schall wrote,
	// which is chosen — the address is the truest thing in the file, and a
	// mapping being withdrawn does not make it untrue.
	Comment string
	// ReplayGain is how loud the audio in this file was measured to be, where
	// it was measured at all. It is nil for a file nobody has measured, and nil
	// is not the same as zero: a gain of zero decibels is a track already at
	// the reference loudness, and writing that into a file nothing was measured
	// for would tell the player to leave a lurching track alone.
	ReplayGain *ReplayGain
}

// ReplayGain is a measured loudness in the form a player reads.
//
// It is loudness and never identity. Nothing in it says what music the file
// holds, and it is written for the music server's benefit alone — the same
// standing as the release identifiers beside it.
type ReplayGain struct {
	// TrackGainDB is what a player adds to the volume to bring this track to
	// the reference loudness, and TrackPeak is the loudest moment as a fraction
	// of full scale. See internal/loudness for what those mean and how they are
	// measured.
	TrackGainDB float64
	TrackPeak   float64
	// Album is the same pair for the whole record, and is written only where
	// every track of the record was measured. A record levelled from some of
	// itself is a number for a record that does not exist.
	Album *AlbumGain
}

// AlbumGain is one record levelled as one performance, so that the quiet track
// the artist put after the loud one stays quiet.
type AlbumGain struct {
	GainDB float64
	Peak   float64
}

// ErrNothingProved reports a write asked for without enough to describe the
// file. A file Schall could not identify keeps whatever it came with: silence
// about it is not permission to invent an album name, and an owned tag is
// cleared when Schall cannot fill it, so writing half an identity would take
// away the little the peer did say and put nothing in its place.
var ErrNothingProved = errors.New("too little was proved about this file to write anything into it")

// These are the tag names a music server reads. They are TagLib's normalized
// property names, which it maps onto each format's own frames — a Vorbis
// comment in FLAC, an ID3 frame in MP3, an atom in M4A — and they are the names
// Navidrome's tag map lists for the fields it groups and links by: the album
// from ALBUM, its artist from ALBUMARTIST, the track's own artists from ARTISTS
// paired position by position with MUSICBRAINZ_ARTISTID, and the MusicBrainz
// identifiers under the Picard names.
//
// MUSICBRAINZ_TRACKID is the recording, not the track on the release: Picard
// named it that before releases had track identifiers of their own, and every
// reader since has followed. MUSICBRAINZ_RELEASETRACKID is the track.
const (
	keyTitle          = "TITLE"
	keyAlbum          = "ALBUM"
	keyArtist         = "ARTIST"
	keyArtists        = "ARTISTS"
	keyAlbumArtist    = "ALBUMARTIST"
	keyAlbumArtists   = "ALBUMARTISTS"
	keyTrackNumber    = "TRACKNUMBER"
	keyDiscNumber     = "DISCNUMBER"
	keyGenre          = "GENRE"
	keyDate           = "DATE"
	keyReleaseDate    = "RELEASEDATE"
	keyISRC           = "ISRC"
	keyRecordingID    = "MUSICBRAINZ_TRACKID"
	keyReleaseID      = "MUSICBRAINZ_ALBUMID"
	keyReleaseGroupID = "MUSICBRAINZ_RELEASEGROUPID"
	keyArtistIDs      = "MUSICBRAINZ_ARTISTID"
	keyAlbumArtistIDs = "MUSICBRAINZ_ALBUMARTISTID"
	keyReleaseTrackID = "MUSICBRAINZ_RELEASETRACKID"
	keyComment        = "COMMENT"
	keyTrackGain      = "REPLAYGAIN_TRACK_GAIN"
	keyTrackPeak      = "REPLAYGAIN_TRACK_PEAK"
	keyAlbumGain      = "REPLAYGAIN_ALBUM_GAIN"
	keyAlbumPeak      = "REPLAYGAIN_ALBUM_PEAK"
	keyTaggedAt       = "SCHALL_TAGGED_AT"
	keyTagsAsArrived  = "SCHALL_TAGS_AS_ARRIVED"
)

// replaced lists the tags a managed copy says in Schall's words and nobody
// else's. Which album a file belongs to is settled by these together, so one of
// them left holding a stranger's value while its neighbours hold Schall's is
// what files one album under two names — an album Schall cannot name is
// therefore nameless rather than named by the peer. Tags nobody here claims —
// lyrics, cover art — are the file's own and are never touched.
//
// The genre is claimed, and it was not always. It is here because a music
// server browses by genre and what a peer wrote in that tag is whatever they
// felt like: one album arrives half "Electronic" and half "electronica" and the
// browse splits it in two. What Schall writes is MusicBrainz's community vote
// for the release group the file was proved to be on, which is one answer for
// the whole album. Where there are no votes the tag is cleared, for the same
// reason an album Schall cannot name is nameless.
//
// The replay gain tags are not on this list either, and the reason is not that
// they are unclaimed. Schall does claim them, but only for a file it has
// measured: everything on this list is cleared whether or not Schall has a
// value to put back, and a file written while no loudness was measured for it —
// an installation with no ffmpeg, a file whose audio gave no answer — would
// have the peer's own gain taken away and nothing written in its place. So they
// are written by setReplayGain instead, which touches them only where there is
// a measurement and otherwise leaves the file saying what it said.
//
// The numbers on the disc are not here. A track number a stranger wrote orders
// an album; it does not split one, and dropping a position Schall does not
// happen to know would leave a release in no order at all.
var replaced = []string{
	keyTitle, keyAlbum, keyArtist, keyArtists, keyAlbumArtist, keyAlbumArtists,
	keyGenre, keyDate, keyReleaseDate, keyReleaseID, keyReleaseGroupID,
	keyArtistIDs, keyAlbumArtistIDs, keyReleaseTrackID,
}

// withheld names what Schall knows and does not write, and why.
//
// A match is provable only from something other than the match. Everything a
// scan grades, it reads out of the file, and two of those readings prove an
// identity on their own with no audio consulted: the recording identifier and
// the ISRC. One of those written by Schall into a file it had already decided
// about would prove Schall's own decision back to the next scan — on a fresh
// install, in another library, to anyone.
//
// A file can be marked as Schall's, and is; see write.go. But a mark inside a
// file is only as durable as the next tool to rewrite it, and anything that
// copies the fields it recognises while dropping the ones it does not would
// leave the identifier standing with nothing left to say who wrote it. So
// neither is ever written, and neither is cleared either: the one on the file
// is the stranger's, and it is his to keep saying.
//
// The identifiers that are written — release, release group, artists — are
// written because nothing in Schall reads them as evidence of anything, and a
// music server needs them to group an album and to link an artist.
var withheld = []string{keyRecordingID, keyISRC}

// proved reports whether enough is known to describe the file. A title and
// somebody it is by are the least a music server needs to show it as anything
// other than unknown, and they are the least Schall will exchange for what the
// file already says about itself.
//
// A credit list with a nameless artist in it is not somebody it is by: that list
// is not written at all (see setCredits), so a file whose only artist is that
// one would have its artist taken away and nothing put back.
func (tags Tags) proved() bool {
	named := tags.Artist != "" || tags.AlbumArtist != "" ||
		(len(tags.Artists) > 0 && !unnamed(tags.Artists)) ||
		(len(tags.AlbumArtists) > 0 && !unnamed(tags.AlbumArtists))
	return tags.Title != "" && named
}

// values renders the tags in the shape TagLib writes. Every replaced name is
// present: the ones Schall proved carry their value, and the rest carry an
// empty list, which is how a tag is removed. Nothing in withheld appears at
// all, so whatever the file says there it goes on saying.
func (tags Tags) values() map[string][]string {
	written := make(map[string][]string, len(replaced)+4)
	for _, name := range replaced {
		written[name] = nil
	}
	setText(written, keyTitle, tags.Title)
	setText(written, keyAlbum, tags.Album)
	setText(written, keyArtist, tags.Artist)
	setText(written, keyAlbumArtist, tags.AlbumArtist)
	setText(written, keyGenre, tags.Genre)
	setNumber(written, keyTrackNumber, tags.TrackNumber)
	setNumber(written, keyDiscNumber, tags.DiscNumber)
	// A music server reads the album's date from RELEASEDATE and the
	// recording's from DATE. Schall knows one date, the release's, so it says
	// so twice rather than letting the album's date go missing.
	setText(written, keyDate, tags.Date)
	setText(written, keyReleaseDate, tags.Date)
	setID(written, keyReleaseID, tags.ReleaseID)
	setID(written, keyReleaseGroupID, tags.ReleaseGroupID)
	setText(written, keyComment, tags.Comment)
	setCredits(written, keyArtists, keyArtistIDs, tags.Artists)
	setCredits(written, keyAlbumArtists, keyAlbumArtistIDs, tags.AlbumArtists)
	setReplayGain(written, tags.ReplayGain)
	return written
}

// setReplayGain writes how loud the file is, and writes nothing at all where
// nothing was measured.
//
// A measured file has all four of its gain tags owned by Schall from that
// moment on, so all four are named here even when only two carry a value. The
// album pair is cleared rather than left alone: Schall's track gain standing
// beside a peer's album gain is two different reference loudnesses written into
// one file, and a player following the album tag would level the record by a
// stranger's measurement of a different mastering of it.
//
// An unmeasured file has none of the four named, so whatever the peer wrote
// there the file goes on saying. That is the same silence rule the rest of this
// package keeps: a tag Schall cannot fill is not a tag Schall takes away.
func setReplayGain(written map[string][]string, gain *ReplayGain) {
	if gain == nil {
		return
	}
	written[keyTrackGain] = []string{formatGain(gain.TrackGainDB)}
	written[keyTrackPeak] = []string{formatPeak(gain.TrackPeak)}
	written[keyAlbumGain] = nil
	written[keyAlbumPeak] = nil
	if gain.Album == nil {
		return
	}
	written[keyAlbumGain] = []string{formatGain(gain.Album.GainDB)}
	written[keyAlbumPeak] = []string{formatPeak(gain.Album.Peak)}
}

// replayGainKeys is the same four names as one list, for the places that have
// to name them all: the revert, which gives them back, and the record of what
// the file arrived carrying, which keeps them.
var replayGainKeys = []string{keyTrackGain, keyTrackPeak, keyAlbumGain, keyAlbumPeak}

// formatGain writes a gain the way every player expects to read it: a number of
// decibels with the unit after it, as "-7.25 dB". Two decimal places is what
// the ReplayGain specification's own examples carry and is finer than anybody
// can hear.
func formatGain(gain float64) string {
	return strconv.FormatFloat(gain, 'f', 2, 64) + " dB"
}

// formatPeak writes a peak as the bare fraction of full scale it is, with no
// unit. Six decimal places, because what a player cares about is how close to
// 1.0 the number is, and the difference between 0.999 and 1.000 is the whole
// question. Nothing is clamped: a true peak above full scale is real, and
// rounding it down would tell a player there is headroom where there is none.
func formatPeak(peak float64) string {
	return strconv.FormatFloat(peak, 'f', 6, 64)
}

func setText(written map[string][]string, name, value string) {
	if value == "" {
		return
	}
	written[name] = []string{value}
}

func setNumber(written map[string][]string, name string, value int) {
	if value <= 0 {
		return
	}
	written[name] = []string{strconv.Itoa(value)}
}

func setID(written map[string][]string, name string, value uuid.UUID) {
	if value == uuid.Nil {
		return
	}
	written[name] = []string{value.String()}
}

// setCredits writes a credit as the list it is: every credited artist's name,
// in the order MusicBrainz printed them, and the identifiers only where every
// one of those artists has one.
//
// The names and the identifiers are read off against each other by position, so
// a list with a gap in it would have to hold that artist's place with something,
// and there is nothing to hold it with. An empty value does not survive the
// write — FLAC, MP3 and WAV each drop it, leading, middle or trailing — so three
// names come back paired against two identifiers and the second artist wears the
// third one's. And no sentinel can stand in its place either: anything that did
// survive is a string the next reader takes for a MusicBrainz identifier, and
// there is no such identifier for that artist.
//
// So one unidentified artist withholds the identifiers whole. A reader given
// names with no identifiers splits on the names, which is honest; losing every
// link on the track costs a link, where the alternative asserts something false
// about who made the music.
//
// A credit with no name withholds both lists, and the joined string in ARTIST
// carries the credit alone. Elsewhere here the names are the one thing never
// withheld — rejoining them has to reproduce what MusicBrainz printed — and this
// is the single case where keeping that would say something untrue about who
// made the music. There is no withholding trick left on this side: the names are
// the list, and a nameless one is dropped by the write exactly as an empty
// identifier is, leaving identifiers that outnumber the names and every artist
// after the gap wearing his neighbour's. The formats that drop it made that a
// failed write and the file went untagged; the one that keeps it wrote a
// nameless artist holding a real person's identifier. ARTIST is what every file
// carried before this list existed: prose, unsplit, and true of the record.
func setCredits(written map[string][]string, names, identifiers string, credits []Credit) {
	if len(credits) == 0 || unnamed(credits) {
		return
	}
	list := make([]string, 0, len(credits))
	known := make([]string, 0, len(credits))
	for _, credit := range credits {
		list = append(list, credit.Name)
		if credit.MusicBrainzArtistID == uuid.Nil {
			continue
		}
		known = append(known, credit.MusicBrainzArtistID.String())
	}
	written[names] = list
	if len(known) != len(credits) {
		return
	}
	written[identifiers] = known
}

// unnamed reports whether any credit in the list is one no name came with.
// Spaces are no name either — a tag is read here with its whitespace trimmed
// off, so a name made of spaces reaches every reader as the blank it is.
func unnamed(credits []Credit) bool {
	for _, credit := range credits {
		if strings.TrimSpace(credit.Name) == "" {
			return true
		}
	}
	return false
}

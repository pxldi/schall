package soundcloud

import "strings"

// Naming is what Schall reads off a SoundCloud title, offered to the person
// naming the file so they can correct it before it is written.
//
// It is a suggestion and never a claim. The file is the person's own and the
// address is their word about it, so the split is shown, edited if it is wrong,
// and stored as whatever they confirmed. Nothing here is evidence about a
// MusicBrainz recording, and nothing here is matched to one.
type Naming struct {
	// Artist is who the track is by. The uploader, where the title names
	// nobody else.
	Artist string
	// Title is the track without the artist in front of it and without the
	// download tag on the end.
	Title string
	// Remixer is the uploader, where the title names somebody else as the
	// artist. Empty where the uploader is the artist.
	Remixer string
}

// ReadNaming splits a SoundCloud title into who the track is by, what it is
// called, and who edited it.
//
// SoundCloud states one name as a field, the uploader's, and everything else
// about the track is in the title. `PinkPantheress - Illegal (Abo Edit)
// [FREE DL]` uploaded by `Abo` is a PinkPantheress track that Abo edited, and
// three rules read that off it:
//
//   - the first " - " separates who it is by from what it is called;
//   - a bracketed tag on the end — [FREE DL], [Free Download] — is the
//     uploader talking to their listeners, not part of the name;
//   - the uploader is the person who edited it, not the person it is by.
//
// Square brackets only. "(Abo Edit)" is part of the track's name and stays.
//
// A title with no " - " names nobody but the uploader, so the uploader is the
// artist and there is no remixer, which is what Schall recorded before this
// existed.
func ReadNaming(title, uploader string) Naming {
	title = strings.TrimSpace(dropTrailingTags(title))
	uploader = strings.TrimSpace(uploader)

	artist, track, split := strings.Cut(title, " - ")
	artist, track = strings.TrimSpace(artist), strings.TrimSpace(track)
	if !split || artist == "" || track == "" {
		return Naming{Artist: uploader, Title: title}
	}
	if strings.EqualFold(artist, uploader) {
		return Naming{Artist: uploader, Title: track}
	}
	return Naming{Artist: artist, Title: track, Remixer: uploader}
}

// dropTrailingTags removes the bracketed tags an uploader ends a title with.
// More than one is removed, because "[FREE DL] [BUY]" is two of them.
func dropTrailingTags(title string) string {
	for {
		trimmed := strings.TrimSpace(title)
		if !strings.HasSuffix(trimmed, "]") {
			return trimmed
		}
		open := strings.LastIndex(trimmed, "[")
		if open <= 0 {
			return trimmed
		}
		title = trimmed[:open]
	}
}

// orRead fills in whatever the person left empty from what the title says.
//
// The interface sends all three back, so in practice nothing is filled in here.
// It matters for a caller that sends none of them — an older client, or a test
// — which then gets the suggestion rather than a track with no name.
func (naming Naming) orRead(track Track) Naming {
	read := ReadNaming(track.TrackTitle(), track.Uploader)
	if strings.TrimSpace(naming.Artist) == "" {
		naming.Artist = read.Artist
	}
	if strings.TrimSpace(naming.Title) == "" {
		naming.Title = read.Title
		naming.Remixer = read.Remixer
	}
	naming.Artist = strings.TrimSpace(naming.Artist)
	naming.Title = strings.TrimSpace(naming.Title)
	naming.Remixer = strings.TrimSpace(naming.Remixer)
	return naming
}

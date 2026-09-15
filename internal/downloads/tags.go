package downloads

import (
	"context"

	"github.com/google/uuid"

	"github.com/pxldi/schall/internal/db"
	"github.com/pxldi/schall/internal/identity"
	"github.com/pxldi/schall/internal/tagging"
)

// An import ends by writing into the files what Schall proved them to be.
//
// Schall files music into folders; a music server groups by tags. Everything
// this codebase settles about a copy — which recording it is, which release it
// came from, where it sits on the disc — reached the folder name and stopped
// there, and what the user saw was whatever the peer had typed: one album under
// two spellings, a track with no album at all, two artists written as one name
// nobody has heard of. What Schall holds is better than that, so it is written
// down where the server will read it.
//
// Nothing here decides anything. It runs after the import is committed, on the
// copy in the library, and only ever writes what was already proved: a file
// with no catalogue track behind it is left exactly as it arrived.
//
// It also runs last, after the provider's copy has been let go. Everything that
// confirms a managed copy is the file that was validated reads its size and its
// bytes against the size the peer promised, and writing a tag changes both. So
// the ordering is not a matter of taste: tag before the source is released and
// an import that went perfectly refuses to release anything, because the file it
// goes looking for is the one it has just finished rewriting.

// tagRelease writes into every file of an imported release what the catalogue
// says that file is.
func (importer *Importer) tagRelease(
	ctx context.Context, row db.DownloadImportRow, localPaths map[string]db.ImportedDownloadFile,
) {
	// The files of this release that the catalogue named a track for. They are
	// gathered first because how loud the record is can only be worked out with
	// all of them in hand, and a file with no track is not part of the record.
	var tagged []string
	for _, imported := range localPaths {
		if _, known := trackOf(row.Tracks, imported.TrackID); known {
			tagged = append(tagged, imported.LocalPath)
		}
	}
	gains := importer.measureRelease(ctx, tagged, len(row.Tracks))

	for _, imported := range localPaths {
		track, known := trackOf(row.Tracks, imported.TrackID)
		if !known {
			continue
		}
		tags := releaseTags(row, track)
		tags.ReplayGain = gains[imported.LocalPath]
		importer.writeTags(imported.LocalPath, tags)
	}
}

// tagCopy writes into one file fetched for a want what was proved about it.
// found is nil where a person accepted the copy by hand: their decision is what
// the file is, and the want they accepted it for is what Schall knows about it.
// A want is one track, so there is no record to level here: each file carries
// its own gain and no album gain. Passing no track count says so — the album
// the file belongs to is somewhere else, and most of it is not in this import.
func (importer *Importer) tagCopy(
	ctx context.Context, row db.TargetImportRow, found *identity.Identity,
	localPaths map[string]db.ImportedDownloadFile,
) {
	var paths []string
	for _, imported := range localPaths {
		paths = append(paths, imported.LocalPath)
	}
	gains := importer.measureRelease(ctx, paths, 0)

	for _, imported := range localPaths {
		tags := copyTags(row, found)
		tags.ReplayGain = gains[imported.LocalPath]
		importer.writeTags(imported.LocalPath, tags)
	}
}

// writeTags records what could not be written and carries on. The import is
// committed by the time this runs and the bytes are in the library: a tag that
// would not go in is not a reason to undo an import that succeeded, and the
// file still holds everything it arrived with.
func (importer *Importer) writeTags(path string, tags tagging.Tags) {
	if err := tagging.Write(path, tags); err != nil {
		importer.logger.Warn().Err(err).Str("path", path).
			Msg("what Schall proved this file to be could not be written into it")
	}
}

// releaseTags is one track of a release as Schall holds it.
//
// The artist written as one string is the album's, on the track as well as on
// the album, because an album is filed under one artist in this catalogue. On a
// compilation that is the compilation's artist rather than the performer of the
// track — a credit invented per track from the release's would be a guess.
//
// The lists beside those strings are two different credits, and neither is
// written into the other's tag: the track's own artists, and the album's. A
// track credited to two people is written as two, in MusicBrainz's order and
// MusicBrainz's spelling, so a music server files it under both of them instead
// of under a third artist named after the pair. A track the catalogue holds no
// credit for is written with no list at all — one made by cutting the joined
// string at its commas would turn a band with a comma in its name into two
// artists forever.
func releaseTags(row db.DownloadImportRow, track db.ImportTrack) tagging.Tags {
	tags := tagging.Tags{
		Title:        track.Title,
		Album:        row.AlbumTitle,
		Artist:       row.ArtistName,
		Artists:      credits(track.Credits),
		AlbumArtist:  row.ArtistName,
		AlbumArtists: credits(row.AlbumCredits),
		TrackNumber:  track.TrackNumber,
		DiscNumber:   track.DiscNumber,
		Date:         row.ReleaseDate,
		Genre:        row.Genre,
	}
	if track.MusicBrainzRecordingID != nil {
		tags.RecordingID = *track.MusicBrainzRecordingID
	}
	if row.ReleaseID.Valid {
		tags.ReleaseID = row.ReleaseID.UUID
	}
	if row.ReleaseGroupID.Valid {
		tags.ReleaseGroupID = row.ReleaseGroupID.UUID
	}
	return tags
}

// copyTags is one fetched file as Schall holds it. A want names a recording and
// an entry names it in the user's own words; where the copy was proven, what
// the proof says wins, because it is MusicBrainz speaking rather than a
// playlist. No album is invented from the entry's title: a want is a track, and
// a track title is not an album name however tidy it would look.
//
// The release is one fact and is taken from one place at a time. Its name, its
// date and its identifier all describe a single edition, so a name read off one
// account and an identifier off another would leave the file calling itself one
// album and pointing at a different one. In order: the catalogue's album for the
// release group the want was resolved through, which is a release Schall holds,
// can date, and files the rest of that album under; then the release a proof
// named, which is a name and nothing else; then the album the entry itself gave,
// which is the playlist's account exactly as its artist and title already are.
// Below that is silence — a want whose release nobody has named gets no album
// and no date rather than a guess at one.
//
// Where it sits on that release rides with it. A position is a position on a
// release and means nothing apart from one, so it is written only where the
// catalogue's own album was: the same album, the same walk, the same track a
// whole release's import would have numbered this file from. A file filed under
// a name a proof gave or under the words of a playlist entry gets no numbers,
// because nothing here knows where on that release it sits — and one left
// unnumbered beside its siblings is a track without a disc, which is what a
// music server shows as a disc of its own.
//
// Who the file is by rides with it for the same reason. A credit is a credit on
// a release — the same recording is credited one way on the release it was
// written for and another on the compilation that gathers it — so the list is
// written only where the catalogue's own album was, off the same album and the
// same track. A file filed under a name a proof gave, or under the words of a
// playlist entry, keeps the joined string those gave it and gets no list: the
// entry's artist is what a playlist typed, and cutting it into names would be
// inventing MusicBrainz artists out of somebody else's punctuation.
func copyTags(row db.TargetImportRow, found *identity.Identity) tagging.Tags {
	tags := tagging.Tags{
		Title:       row.EntryTitle,
		Artist:      row.EntryArtist,
		AlbumArtist: row.EntryArtist,
		Album:       row.EntryAlbum,
		RecordingID: row.MusicBrainzRecordingID,
	}
	group := row.MusicBrainzReleaseGroupID
	if found != nil {
		tags.Title = firstNamed(found.TrackTitle, tags.Title)
		tags.Artist = firstNamed(found.ArtistName, tags.Artist)
		tags.AlbumArtist = tags.Artist
		if found.ReleaseTitle != "" {
			tags.Album = found.ReleaseTitle
		}
		if found.RecordingID != uuid.Nil {
			tags.RecordingID = found.RecordingID
		}
		if found.ReleaseGroupID != uuid.Nil {
			group = uuid.NullUUID{UUID: found.ReleaseGroupID, Valid: true}
		}
	}
	if group.Valid {
		tags.ReleaseGroupID = group.UUID
	}
	// The catalogue answers for the want's release group and for no other one.
	// A proof that named a different group is describing a different release,
	// and the album the catalogue holds for the want is not the one this file
	// turned out to be from.
	if row.AlbumTitle != "" && row.MusicBrainzReleaseGroupID.Valid &&
		group == row.MusicBrainzReleaseGroupID {
		tags.Album = row.AlbumTitle
		tags.Date = row.ReleaseDate
		// The genre rides with the album for the same reason the date does: it
		// is the community's vote on this release group, and it means nothing
		// apart from the release the file was proved to be from.
		tags.Genre = row.Genre
		tags.DiscNumber = row.DiscNumber
		tags.TrackNumber = row.TrackNumber
		tags.Artists = credits(row.TrackCredits)
		tags.AlbumArtists = credits(row.AlbumCredits)
		if row.ReleaseID.Valid {
			tags.ReleaseID = row.ReleaseID.UUID
		}
	}
	return tags
}

// credits is the catalogue's credit as the tagger writes it. Order, spelling
// and gaps are carried across untouched: an artist MusicBrainz has no entity
// for keeps its place with no identifier, and the tagger is what decides what a
// gap does to the identifiers it writes.
func credits(held []db.ArtistCredit) []tagging.Credit {
	if len(held) == 0 {
		return nil
	}
	written := make([]tagging.Credit, 0, len(held))
	for _, credit := range held {
		written = append(written, tagging.Credit{
			Name: credit.Name, MusicBrainzArtistID: credit.ArtistID,
		})
	}
	return written
}

// trackOf finds the catalogue track a file was imported as. A zero identifier
// is a file that was imported without one, and nothing is written into it.
func trackOf(tracks []db.ImportTrack, trackID uuid.UUID) (db.ImportTrack, bool) {
	if trackID == uuid.Nil {
		return db.ImportTrack{}, false
	}
	for _, track := range tracks {
		if track.ID == trackID {
			return track, true
		}
	}
	return db.ImportTrack{}, false
}

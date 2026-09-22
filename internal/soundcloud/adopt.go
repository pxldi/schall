package soundcloud

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/rs/zerolog"

	"github.com/pxldi/schall/internal/db"
	"github.com/pxldi/schall/internal/tagging"
)

// ErrFileNotFound reports a file the library does not hold, or one it has lost
// off the volume. Neither can be told it is a SoundCloud track: there is
// nothing on disc to tag.
var ErrFileNotFound = errors.New("the library does not hold that file")

// ErrFileAlreadyMatched reports a file that already carries a catalogue match
// or a manual local_only decision, between the moment somebody was shown a
// SoundCloud preview and the moment they confirmed it.
//
// Preview looks at what SoundCloud says and changes nothing; Adopt is the only
// step that writes. A scan can run in between and give the same file a
// track_mappings row, or a resolved identity, from tags or audio that happen to
// agree with an unrelated catalogue recording. Writing the source identity on
// top of that would not undo the mapping — nothing in this package touches
// track_mappings — so the file would carry two contradictory answers: a
// person's word that it holds no recording, and a mapping saying which one it
// holds. A manual local_only identity is the same settled answer without a
// mapping. Refusing leaves the choice with the person who made the decision.
var ErrFileAlreadyMatched = errors.New(
	"a scan already matched this file to a catalogue recording; check it before naming it from SoundCloud")

// Tracks is what the service asks about a track. The client above is the one
// implementation; a test supplies its own.
type Tracks interface {
	Lookup(ctx context.Context, permalink string) (Track, error)
	Artwork(ctx context.Context, artworkURL string) ([]byte, string, error)
}

// Service records that one file in the library is one track on SoundCloud.
type Service struct {
	pool   *pgxpool.Pool
	tracks Tracks
	logger zerolog.Logger
}

func NewService(pool *pgxpool.Pool, tracks Tracks, logger zerolog.Logger) *Service {
	return &Service{pool: pool, tracks: tracks, logger: logger}
}

// Adopted is what was decided, in the words the interface shows.
type Adopted struct {
	Track Track
	// Naming is who the track is by, what it is called and who edited it, as
	// the person confirmed it. TrackTitle is Naming.Title.
	Naming Naming
	// TrackTitle is what goes into the file's TITLE and ALBUM tags.
	TrackTitle string
	// ArtistID is the uploader's row in the artists table. The uploader is the
	// remixer where the title names somebody else, so this is not always the
	// row for the name the file is filed under.
	ArtistID uuid.UUID
	// Path is the file on the volume.
	Path string
	// Summary is one sentence saying why the file is what it now says it is.
	Summary string
	// Pictured is whether SoundCloud had artwork for the track.
	Pictured bool
}

// Preview asks SoundCloud what the address holds and changes nothing.
//
// The person pastes an address and is shown what came back before they confirm
// it. A decision that is permanent is a decision somebody should be able to see
// first.
func (service *Service) Preview(ctx context.Context, permalink string) (Track, error) {
	return service.tracks.Lookup(ctx, permalink)
}

// Adopt records that a library file holds the SoundCloud track at one address.
//
// This is a manual decision and it is permanent in the way every manual
// decision in Schall is: it is written with is_manual set, which is what stops
// identity resolution from ever asking MusicBrainz about the file again. It
// proves nothing about any recording — a source identity is a second key beside
// the MusicBrainz one, never a substitute for it — so the file can still not
// satisfy a want, and the weekly playlist still passes it by.
//
// What happens, in order: SoundCloud is asked what the track is; its artwork is
// fetched; the uploader, the identity and the picture are written in one
// transaction; and then the file's own tags are written. The tags come last and
// on their own, because a volume mounted read-only must not undo a decision the
// person already made.
func (service *Service) Adopt(
	ctx context.Context, fileID uuid.UUID, permalink string, confirmed Naming,
) (Adopted, error) {
	track, err := service.tracks.Lookup(ctx, permalink)
	if err != nil {
		return Adopted{}, err
	}
	naming := confirmed.orRead(track)

	var path string
	err = service.pool.QueryRow(ctx, `
		SELECT path FROM library_files WHERE id = $1 AND missing_at IS NULL
	`, fileID).Scan(&path)
	if errors.Is(err, pgx.ErrNoRows) {
		return Adopted{}, ErrFileNotFound
	}
	if err != nil {
		return Adopted{}, fmt.Errorf("read the file %s to name it: %w", fileID, err)
	}

	// A track with no picture, and a picture that could not be fetched, are the
	// same to the decision: it is made either way. The row records the absence
	// so the question is not asked again, exactly as release_cover_art does.
	image, contentType, err := service.tracks.Artwork(ctx, track.ArtworkURL)
	if err != nil {
		service.logger.Warn().Err(err).Str("file_id", fileID.String()).
			Str("artwork_url", track.ArtworkURL).Msg("fetch the artwork for a SoundCloud track")
		image, contentType = nil, ""
	}

	summary := fmt.Sprintf("Recorded by hand as the SoundCloud track %q, uploaded by %s.",
		track.TrackTitle(), track.Uploader)

	adopted := Adopted{
		Track:      track,
		Naming:     naming,
		TrackTitle: naming.Title,
		Path:       path,
		Summary:    summary,
		Pictured:   len(image) > 0,
	}

	transaction, err := service.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return Adopted{}, fmt.Errorf("start naming the file %s: %w", fileID, err)
	}
	defer func() { _ = transaction.Rollback(ctx) }()

	if err := refuseIfAlreadyMatched(ctx, transaction, fileID); err != nil {
		return Adopted{}, err
	}

	adopted.ArtistID, err = uploaderArtist(ctx, transaction, track)
	if err != nil {
		return Adopted{}, err
	}
	if err := writeIdentity(ctx, transaction, fileID, track, naming, summary); err != nil {
		return Adopted{}, err
	}
	if err := writeCover(ctx, transaction, fileID, image, contentType); err != nil {
		return Adopted{}, err
	}
	if err := transaction.Commit(ctx); err != nil {
		return Adopted{}, fmt.Errorf("record what the file %s is: %w", fileID, err)
	}

	service.writeTags(ctx, fileID, adopted, image)
	return adopted, nil
}

// AdoptAtPath is Adopt for a caller that knows the file only by where it sits
// on disc, not by its row — an upload confirming a naming before the scan that
// turns its bytes into a library_files row has run. It finds the row the
// moment it is asked, which is after that scan, and everything past that is
// Adopt: the same write, the same refusals, the same permanence.
func (service *Service) AdoptAtPath(
	ctx context.Context, path, permalink string, confirmed Naming,
) (Adopted, error) {
	var fileID uuid.UUID
	err := service.pool.QueryRow(ctx, `
		SELECT id FROM library_files WHERE path = $1 AND missing_at IS NULL
	`, path).Scan(&fileID)
	if errors.Is(err, pgx.ErrNoRows) {
		return Adopted{}, ErrFileNotFound
	}
	if err != nil {
		return Adopted{}, fmt.Errorf("find the file at %s to name it: %w", path, err)
	}
	return service.Adopt(ctx, fileID, permalink, confirmed)
}

// refuseIfAlreadyMatched reads whether a scan has already tied the file to a
// catalogue recording, and locks the file's row while it does so a concurrent
// reconcile cannot write one in behind it before this transaction commits.
//
// track_mappings is checked because that is the row the tagging pass reads to
// overwrite a file's tags — the actual contradiction the finding is about. The
// resolved identity is checked alongside it because a resolved identity is the
// same kind of claim a source identity would replace. A manual local_only
// identity is checked for the same reason: the person already decided that the
// file has no catalogue recording. Letting Adopt silently discard any of these
// decisions here would be the guess this package refuses to make.
func refuseIfAlreadyMatched(ctx context.Context, transaction pgx.Tx, fileID uuid.UUID) error {
	var matched bool
	err := transaction.QueryRow(ctx, `
		SELECT EXISTS (
		    SELECT 1 FROM track_mappings WHERE track_mappings.library_file_id = $1
		) OR EXISTS (
		    SELECT 1 FROM library_file_identities
		    WHERE library_file_identities.library_file_id = $1
		      AND (
		          library_file_identities.kind = 'external'
		          OR (library_file_identities.kind = 'local_only'
		              AND library_file_identities.is_manual)
		      )
		)
		FROM library_files
		WHERE library_files.id = $1
		FOR UPDATE OF library_files
	`, fileID).Scan(&matched)
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrFileNotFound
	}
	if err != nil {
		return fmt.Errorf("check whether the file %s already matched: %w", fileID, err)
	}
	if matched {
		return ErrFileAlreadyMatched
	}
	return nil
}

// uploaderArtist finds or makes the row for the account that published the
// track.
//
// The row is found by the service and the account's identifier there, never by
// name. "Abo" on SoundCloud and "Abo" in MusicBrainz are two names that happen
// to match, and joining them would put an uploader's single into a catalogue
// artist's discography for good.
//
// The uploader is not followed. Following is a promise to keep somebody's
// releases complete, and there are no releases: they are in the artists table
// for the same reason a catalogue artist is, because the library holds their
// music, and catalogue_summary is the sentence that says so.
func uploaderArtist(ctx context.Context, transaction pgx.Tx, track Track) (uuid.UUID, error) {
	return db.UpsertSourceUploader(ctx, transaction, Source, uploaderID(track), track.Uploader,
		"In the library because a track they uploaded to SoundCloud is.")
}

// uploaderID identifies the account. SoundCloud's oEmbed answer does not carry
// the account's number, so the account's own address is what tells two
// uploaders apart — it is the permalink SoundCloud itself hands out, and it is
// unique on the site.
func uploaderID(track Track) string {
	if url := strings.TrimSpace(track.UploaderURL); url != "" {
		return Source + ":" + strings.TrimSuffix(url, "/")
	}
	return Source + ":user:" + strings.ToLower(strings.TrimSpace(track.Uploader))
}

// writeIdentity replaces whatever the file was said to be with the source
// identity.
//
// Whatever stood there was either a guess resolution made or a decision
// somebody is now taking back by making this one, and a person pasting the
// track's own address is the most direct statement about a file that Schall
// accepts anywhere.
//
// The whole oEmbed answer goes into evidence, verbatim, so the decision can
// always be read back as the thing that was actually shown to the person who
// made it.
func writeIdentity(
	ctx context.Context, transaction pgx.Tx,
	fileID uuid.UUID, track Track, naming Naming, summary string,
) error {
	// The evidence keeps what SoundCloud said, word for word, beside what the
	// person made of it. The split is a suggestion they can correct, so the
	// thing it was read off has to stay readable next to the answer.
	evidence, err := json.Marshal([]string{
		"title: " + track.Title,
		"uploader: " + track.Uploader,
		"uploader page: " + track.UploaderURL,
		"artwork: " + track.ArtworkURL,
		"track: " + track.Permalink,
		"named as: " + naming.Artist + " — " + naming.Title,
		"remixer: " + naming.Remixer,
	})
	if err != nil {
		return fmt.Errorf("record what SoundCloud said about %s: %w", track.Permalink, err)
	}

	if _, err := transaction.Exec(ctx, `
		DELETE FROM library_file_identities WHERE library_file_id = $1
	`, fileID); err != nil {
		return fmt.Errorf("clear what the file %s was said to be: %w", fileID, err)
	}
	if _, err := transaction.Exec(ctx, `
		INSERT INTO library_file_identities (
		    library_file_id, kind, provider, source, external_id, external_url,
		    artist_name, release_title, track_title, remixer_name, method,
		    confidence, is_manual, summary, evidence
		)
		VALUES ($1, 'source', $2, $2, $3, $4, $5, $6, $6, nullif($7, ''),
		        'source_url', 1, true, $8, $9)
	`, fileID, Source, track.ExternalID, track.Permalink,
		naming.Artist, naming.Title, naming.Remixer, summary, evidence); err != nil {
		return fmt.Errorf("record the file %s as a SoundCloud track: %w", fileID, err)
	}
	if _, err := transaction.Exec(ctx, `
		UPDATE library_files
		SET resolution_status = 'source',
		    resolution_summary = $2,
		    resolution_error = NULL,
		    resolution_attempted_at = now(),
		    updated_at = now()
		WHERE id = $1
	`, fileID, summary); err != nil {
		return fmt.Errorf("record that the file %s no longer waits on MusicBrainz: %w", fileID, err)
	}
	return nil
}

func writeCover(
	ctx context.Context, transaction pgx.Tx,
	fileID uuid.UUID, image []byte, contentType string,
) error {
	return db.WriteFileCoverArt(ctx, transaction, fileID, image, contentType, Source)
}

// writeTags puts what SoundCloud said into the file itself, so that a player
// reading tags — which is every player — shows the track under its own name
// rather than under its filename.
//
// Every value is written exactly as it arrived. ALBUM is the title again: the
// track is a single, it belongs to no release, and inventing an album name for
// it would file it under a release that does not exist.
//
// A failure here is logged and nothing more. The decision is already recorded,
// and a volume mounted read-only is an ordinary thing to meet; the file is
// still the track it was said to be, and the library shows it as one.
func (service *Service) writeTags(
	ctx context.Context, fileID uuid.UUID, adopted Adopted, image []byte,
) {
	log := service.logger.With().Str("file_id", fileID.String()).
		Str("path", adopted.Path).Logger()

	// The artist is who the track is by, not who uploaded it. The remixer is
	// named in the title — "Illegal (Abo Edit)" — which is how every player
	// shows an edit, so there is no second credit to write.
	err := tagging.Write(adopted.Path, tagging.Tags{
		Title:       adopted.Naming.Title,
		Album:       adopted.Naming.Title,
		Artist:      adopted.Naming.Artist,
		AlbumArtist: adopted.Naming.Artist,
		Comment:     adopted.Track.Permalink,
	})
	if err != nil {
		log.Warn().Err(err).Msg("write the SoundCloud tags into the file")
		return
	}
	if len(image) == 0 || ctx.Err() != nil {
		return
	}
	if err := tagging.WriteArtwork(adopted.Path, image); err != nil {
		log.Warn().Err(err).Msg("write the SoundCloud artwork into the file")
	}
}

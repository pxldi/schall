package library

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/pxldi/schall/internal/coverart"
	"github.com/pxldi/schall/internal/lyrics"
	"github.com/rs/zerolog"
)

// moveBatch is how many files one apply job renames before it goes back to the
// queue. The worker that runs it is one goroutine working every other kind of
// job in turn, and a library is tens of thousands of renames: a job that held
// the lane until the last one would stop imports, scans and resolutions for as
// long as the migration took. A batch that finds more waiting queues its own
// replacement immediately, so the run still proceeds at full speed — it just
// yields between batches, in the shape the cover-art sweep set.
const moveBatch = 200

// planSample is how many rows a run reports alongside its counts. A migration
// is judged by what it is about to do, and the first page of it says that; the
// whole plan is sixty thousand rows nobody reads.
const planSample = 50

// Expected refusals, as sentences, because each one is shown to somebody who
// pressed a button.
var (
	// A plan is a claim on every file in it — library_file_moves_open_file_idx
	// makes that literal — so a second plan while one is open would race the
	// first to the same files. The open one is found rather than replaced.
	ErrRunOpen = errors.New("a layout migration is already planned; apply or discard it before planning another")

	ErrRunNotFound = errors.New("that layout migration run was not found")
)

// Mover plans and applies a layout migration.
//
// A move Schall performs updates the row it already has, so nothing is
// re-linked: the identity, the mappings, the provenance and the acquisition
// links are not carried across because they were never disturbed
// (ADR 0014). What the rename needs to keep consistent is the
// handful of places a path is stored as a lookup key rather than held as a
// foreign key, and those move inside the same transaction as the rename.
type Mover struct {
	pool   *pgxpool.Pool
	logger zerolog.Logger
}

func NewMover(pool *pgxpool.Pool, logger zerolog.Logger) *Mover {
	return &Mover{pool: pool, logger: logger}
}

// PlanResult is what planning a run decided, including what it left alone.
// Files that stay put are counted rather than journalled: the journal records
// moves, and a file that is not moving is not one.
type PlanResult struct {
	RunID uuid.UUID
	// Planned is how many files will be renamed.
	Planned int
	// Colliding is how many render to a path another file in this run is
	// already going to. They are journalled as failed before anything is
	// touched, so the refusal is readable in the plan rather than discovered
	// halfway through it.
	Colliding int
	// InPlace is how many are already where the template files them.
	InPlace int
	// Unfiled is how many the template cannot name because Schall did not file
	// them: see fileFields.
	Unfiled int
	// Absent is how many are recorded as missing from disc. There is nothing to
	// rename.
	Absent int
}

// MoveRow is one line of the journal.
type MoveRow struct {
	ID            uuid.UUID
	LibraryFileID uuid.UUID
	FromPath      string
	ToPath        string
	Status        string
	Error         string
	PlannedAt     time.Time
	MovedAt       *time.Time
}

// RunSummary is a run as the interface reads it: what is left, what happened,
// and the first page of the rows.
type RunSummary struct {
	RunID     uuid.UUID
	PlannedAt time.Time
	Planned   int
	Moved     int
	Failed    int
	Reverted  int
	Moves     []MoveRow
}

// Remaining reports whether the run still has work in it.
func (summary RunSummary) Remaining() bool { return summary.Planned > 0 }

// candidate is one library file as planning reads it.
type candidate struct {
	fileID     uuid.UUID
	path       string
	rootPath   string
	artistTag  string
	albumTag   string
	filedAs    fileNames
	identified fileNames
	catalogued fileNames
}

// fileNames is one source's account of the release a file belongs to: who and
// what it is, and which release that is. All of it is used together or none of
// it is — half a name from the import and half from the catalogue would file a
// release under a folder no source ever described, and an identifier from one
// source beside a name from another is the same mistake wearing brackets. That
// second half is not hypothetical: it filed a release under its catalogue name
// and its want's recording, which put every track of one album in a folder of
// its own.
type fileNames struct {
	artist string
	album  string
	// id is the release, never the recording and never the download. It is what
	// keeps two albums sharing an artist and a title apart, so it has to be the
	// same for every track of one release and different between releases. A
	// source that knows the names but no release leaves it empty rather than
	// reaching for something unique to this file.
	id string
}

// Plan writes what the current template would do with every file in the
// library, and touches nothing.
func (mover *Mover) Plan(ctx context.Context) (PlanResult, error) {
	open, err := mover.openRun(ctx)
	if err != nil {
		return PlanResult{}, err
	}
	if open != uuid.Nil {
		return PlanResult{}, ErrRunOpen
	}

	template, err := mover.template(ctx)
	if err != nil {
		return PlanResult{}, err
	}
	candidates, absent, err := mover.candidates(ctx)
	if err != nil {
		return PlanResult{}, err
	}

	result := PlanResult{RunID: uuid.New(), Absent: absent}
	var (
		fileIDs   []uuid.UUID
		fromPaths []string
		toPaths   []string
		statuses  []string
		reasons   []*string
	)
	// Where a file is going is decided once, here, so that a collision is a
	// property of the plan rather than of the order the renames happen to run
	// in. The first file to claim a destination keeps it; every later one is
	// refused, because two files at one path is one file.
	claimed := make(map[string]struct{}, len(candidates))
	needsIdentifier := template.UsesIdentifier()
	for _, file := range candidates {
		fields, named := file.fields()
		if !named || (needsIdentifier && fields.ID == "") {
			result.Unfiled++
			continue
		}
		destination := filepath.Join(file.rootPath, template.Render(fields), filepath.Base(file.path))
		if destination == file.path {
			result.InPlace++
			continue
		}
		status, reason := "planned", (*string)(nil)
		if _, taken := claimed[destination]; taken {
			collision := "another file in this run is already planned to move here"
			status, reason = "failed", &collision
			result.Colliding++
		} else {
			claimed[destination] = struct{}{}
			result.Planned++
		}
		fileIDs = append(fileIDs, file.fileID)
		fromPaths = append(fromPaths, file.path)
		toPaths = append(toPaths, destination)
		statuses = append(statuses, status)
		reasons = append(reasons, reason)
	}
	if len(fileIDs) == 0 {
		return result, nil
	}
	if _, err := mover.pool.Exec(ctx, `
		INSERT INTO library_file_moves (run_id, library_file_id, from_path, to_path, status, error)
		SELECT $1, *
		FROM unnest($2::uuid[], $3::text[], $4::text[], $5::text[], $6::text[])
	`, result.RunID, fileIDs, fromPaths, toPaths, statuses, reasons); err != nil {
		return PlanResult{}, fmt.Errorf("write layout migration plan %s: %w", result.RunID, err)
	}
	mover.logger.Info().Str("run_id", result.RunID.String()).
		Int("planned", result.Planned).Int("colliding", result.Colliding).
		Int("in_place", result.InPlace).Int("unfiled", result.Unfiled).
		Msg("library layout migration planned")
	return result, nil
}

// fields says what to render this file's folder from, and whether it can be
// rendered at all.
//
// The names are asked for in the order of how much each source knows about
// where the file belongs, and the first that answers is used whole:
//
//  1. the import that filed it, because that is what the folder it is in was
//     built from — so a run against an untouched template moves nothing;
//  2. the catalogue release it is matched to, which is what Schall knows the
//     file to be. It answers for every track of an album with one artist and
//     one title, whichever guest each track credits and however each arrived,
//     and it is the same album row both importers file a folder under;
//  3. what its audio was proven to be, for a release the catalogue does not
//     hold. The name here is the credit printed on that one recording, so it
//     reads "Ufo361 feat. KC Rebell" where the album is Ufo361's and "RIN"
//     where the album row says "Rin" — one album in several folders, which is
//     why it is asked after the catalogue and not before;
//  4. its own tags, which are what the file says it is.
//
// The identifier comes from whichever of those answered, because it is a fact
// about the release that answer named. Taking the name from one source and the
// identifier from another files a release under a folder no source describes:
// it filed a want's copy under its catalogue album's name and its own recording,
// so thirteen tracks of one album rendered thirteen folders all called after the
// album and none of them the same.
//
// Tags name a release without identifying one, so a file Schall never filed has
// no identifier and, under a template asking for {id}, is not planned and stays
// where it is. Dropping {id} is how a collection Schall never filed gets
// migrated.
func (file candidate) fields() (Fields, bool) {
	for _, source := range []fileNames{
		file.filedAs,
		file.catalogued,
		file.identified,
		{artist: file.artistTag, album: file.albumTag},
	} {
		if source.artist == "" && source.album == "" {
			continue
		}
		return Fields{Artist: source.artist, Album: source.album, ID: source.id}, true
	}
	return Fields{}, false
}

// candidates reads every file a run could move, together with everything the
// template might name it after. It also counts the files that are recorded as
// gone: a migration has nothing to rename for them, and saying so is better
// than a plan that silently covers less than the library.
func (mover *Mover) candidates(ctx context.Context) ([]candidate, int, error) {
	rows, err := mover.pool.Query(ctx, `
		SELECT files.id, files.path, roots.path,
		       coalesce(files.artist_tag, ''), coalesce(files.album_tag, ''),
		       coalesce(imported.artist_name, ''), coalesce(imported.album_title, ''),
		       coalesce(imported.short_id, ''),
		       coalesce(identified.artist_name, ''), coalesce(identified.release_title, ''),
		       coalesce(identified.short_id, ''),
		       coalesce(matched.artist_name, ''), coalesce(matched.album_title, ''),
		       coalesce(matched.short_id, '')
		FROM library_files files
		JOIN library_roots roots ON roots.id = files.library_root_id
		LEFT JOIN LATERAL (
			-- The release is joined loosely because a request does not always
			-- name one: a copy fetched for a want answers a recording.
			--
			-- The release this request was for, named and identified together.
			-- A request that names no release — a copy fetched for a want —
			-- answers nothing here at all, and the catalogue below answers
			-- instead: a name from one source beside an identifier from another
			-- is what put every track of an album in a folder of its own.
			SELECT artists.name AS artist_name, albums.title AS album_title,
			       left(coalesce(
			           albums.musicbrainz_release_group_id::text,
			           albums.id::text
			       ), 8) AS short_id
			FROM downloads
			JOIN download_requests requests ON requests.id = downloads.download_request_id
			JOIN albums ON albums.id = requests.album_id
			JOIN artists ON artists.id = albums.artist_id
			WHERE downloads.library_file_id = files.id
			ORDER BY downloads.queued_at, downloads.id
			LIMIT 1
		) imported ON true
		-- What the file was proven to be: the release group the audio was
		-- identified against, under the credit printed on that one recording.
		--
		-- It is read where the catalogue below answers nothing — a file matched
		-- to no album, or to two that nothing chooses between. A credit differs
		-- from track to track of one album, so the album is the better name
		-- wherever there is one.
		--
		-- An import and a migration agree on the folder exactly when the
		-- catalogue holds the album for the release group the audio named: both
		-- render that album row. Where it holds none, both fall back to this
		-- identity and still agree. The one file they can disagree about is one
		-- whose album the importer could not see — a copy fetched for a want
		-- resolved through a different release group than the audio turned out
		-- to be on. That file is filed under the credit and moved once, to the
		-- album, by the first migration run after it is matched.
		LEFT JOIN library_file_identities identities ON identities.library_file_id = files.id
		LEFT JOIN LATERAL (
			-- Two kinds of identity name a folder. One names a MusicBrainz
			-- release group and is identified by it. The other is a track
			-- Schall holds by its address on a service: it has no release
			-- group, and the service's own key for the track is what tells two
			-- of them apart.
			--
			-- Reading only the first left a named SoundCloud track in
			-- /music/Unknown, where the scanner filed it when it had no tags, so
			-- the folder said the opposite of what the row said and anything
			-- reading the library off disc saw an unknown artist (issue #491).
			SELECT identities.artist_name, identities.release_title,
			       CASE
			           WHEN identities.musicbrainz_release_group_id IS NOT NULL
			               THEN left(identities.musicbrainz_release_group_id::text, 8)
			           ELSE identities.source || '-'
			               || split_part(identities.external_id, ':', 2)
			       END AS short_id
			WHERE identities.musicbrainz_release_group_id IS NOT NULL
			   OR (identities.kind = 'source'
			       AND identities.source IS NOT NULL
			       AND identities.external_id IS NOT NULL)
		) identified ON true
		LEFT JOIN LATERAL (
			-- The release Schall knows this file to be part of. It answers for
			-- every track of an album with one name and one identifier, whatever
			-- each of them was fetched by, which is what files an album fetched
			-- a track at a time in one folder.
			--
			-- A file can be matched to more than one album. One recording is
			-- listed by every release that carries it — an EP and the
			-- compilation that gathers it — and those releases disagree about
			-- the album name and about the artist it is filed under. So the
			-- album is taken only where it is the single answer:
			--
			--   * one album, which is the ordinary case. Albums that render the
			--     same artist, title and identifier are one answer, because they
			--     are one folder;
			--   * or the one of them the audio was identified on, which is the
			--     only thing in Schall that says which release a file is a copy
			--     of. internal/library/tags.go choose() reads them in this same
			--     order and for this same reason.
			--
			-- Anything else answers nothing at all, and the identity above names
			-- the folder instead. Taking whichever album the database returned
			-- first would file one release under a name picked at random, and
			-- two planning runs could pick differently.
			SELECT artist_name, album_title, short_id
			FROM (
				SELECT mapped.*,
				       count(*) OVER () AS albums_mapped,
				       count(*) FILTER (WHERE mapped.on_the_identified_release)
				           OVER () AS albums_identified
				FROM (
					SELECT DISTINCT artists.name AS artist_name,
					       albums.title AS album_title,
					       left(coalesce(
					           albums.musicbrainz_release_group_id::text,
					           albums.id::text
					       ), 8) AS short_id,
					       coalesce(
					           albums.musicbrainz_release_group_id
					               = identities.musicbrainz_release_group_id,
					           false
					       ) AS on_the_identified_release
					FROM track_mappings
					JOIN tracks ON tracks.id = track_mappings.track_id
					JOIN albums ON albums.id = tracks.album_id
					JOIN artists ON artists.id = albums.artist_id
					WHERE track_mappings.library_file_id = files.id
				) mapped
			) answers
			WHERE albums_mapped = 1
			   OR (on_the_identified_release AND albums_identified = 1)
		) matched ON true
		WHERE files.missing_at IS NULL
		ORDER BY files.path
	`)
	if err != nil {
		return nil, 0, fmt.Errorf("read the library for a layout migration: %w", err)
	}
	defer rows.Close()

	candidates := make([]candidate, 0)
	for rows.Next() {
		var file candidate
		if err := rows.Scan(
			&file.fileID, &file.path, &file.rootPath, &file.artistTag, &file.albumTag,
			&file.filedAs.artist, &file.filedAs.album, &file.filedAs.id,
			&file.identified.artist, &file.identified.album, &file.identified.id,
			&file.catalogued.artist, &file.catalogued.album, &file.catalogued.id,
		); err != nil {
			return nil, 0, err
		}
		candidates = append(candidates, file)
	}
	if err := rows.Err(); err != nil {
		return nil, 0, err
	}
	var absent int
	if err := mover.pool.QueryRow(ctx, `
		SELECT count(*) FROM library_files WHERE missing_at IS NOT NULL
	`).Scan(&absent); err != nil {
		return nil, 0, err
	}
	return candidates, absent, nil
}

// template is the stored layout, or the default when nothing has departed from
// it. There is no column default: an absent row means DefaultTemplate, so the
// answer has one home (ADR 0014).
func (mover *Mover) template(ctx context.Context) (Template, error) {
	var text string
	err := mover.pool.QueryRow(ctx, `
		SELECT template FROM library_layout_settings WHERE singleton
	`).Scan(&text)
	if errors.Is(err, pgx.ErrNoRows) {
		text = DefaultTemplate
	} else if err != nil {
		return Template{}, fmt.Errorf("read the library layout: %w", err)
	}
	template, err := ParseTemplate(text)
	if err != nil {
		return Template{}, fmt.Errorf("parse the stored layout template: %w", err)
	}
	return template, nil
}

func (mover *Mover) openRun(ctx context.Context) (uuid.UUID, error) {
	var runID uuid.UUID
	err := mover.pool.QueryRow(ctx, `
		SELECT run_id FROM library_file_moves WHERE status = 'planned' LIMIT 1
	`).Scan(&runID)
	if errors.Is(err, pgx.ErrNoRows) {
		return uuid.Nil, nil
	}
	if err != nil {
		return uuid.Nil, err
	}
	return runID, nil
}

// Discard drops what a run planned and never did. A plan is an intention, not
// history — only a rename that happened is worth keeping — so the moved and
// failed rows stay and the planned ones go, which is also what releases the
// files for a plan somebody would rather have.
func (mover *Mover) Discard(ctx context.Context, runID uuid.UUID) (RunSummary, error) {
	if _, err := mover.pool.Exec(ctx, `
		DELETE FROM library_file_moves WHERE run_id = $1 AND status = 'planned'
	`, runID); err != nil {
		return RunSummary{}, fmt.Errorf("discard layout migration %s: %w", runID, err)
	}
	return mover.Run(ctx, runID)
}

// QueueApply asks for the run to be applied. The job is queued once per run:
// two of them would work the same journal, and while that is safe — a move is
// claimed with SKIP LOCKED — it is still two workers doing one job's work.
func (mover *Mover) QueueApply(ctx context.Context, runID uuid.UUID) error {
	_, err := mover.pool.Exec(ctx, `
		INSERT INTO jobs (kind, payload, max_attempts)
		SELECT 'apply_library_layout', jsonb_build_object('runId', $1::text), 5
		WHERE NOT EXISTS (
			SELECT 1 FROM jobs
			WHERE kind = 'apply_library_layout'
			  AND status IN ('queued', 'running')
			  AND payload->>'runId' = $1::text
		)
	`, runID)
	if err != nil {
		return fmt.Errorf("queue layout migration %s: %w", runID, err)
	}
	return nil
}

// Run reads one run back: its counts and the first page of its rows.
func (mover *Mover) Run(ctx context.Context, runID uuid.UUID) (RunSummary, error) {
	summary := RunSummary{RunID: runID}
	var plannedAt *time.Time
	err := mover.pool.QueryRow(ctx, `
		SELECT min(planned_at),
		       count(*) FILTER (WHERE status = 'planned'),
		       count(*) FILTER (WHERE status = 'moved'),
		       count(*) FILTER (WHERE status = 'failed'),
		       count(*) FILTER (WHERE status = 'reverted')
		FROM library_file_moves
		WHERE run_id = $1
	`, runID).Scan(&plannedAt, &summary.Planned, &summary.Moved,
		&summary.Failed, &summary.Reverted)
	if err != nil {
		return RunSummary{}, err
	}
	if plannedAt == nil {
		return RunSummary{}, ErrRunNotFound
	}
	summary.PlannedAt = *plannedAt
	summary.Moves, err = mover.moves(ctx, runID)
	if err != nil {
		return RunSummary{}, err
	}
	return summary, nil
}

// LatestRun is the run the interface shows without being told which one: the
// most recent, whether it is waiting, running or over. ErrRunNotFound means
// nothing has ever been planned here.
func (mover *Mover) LatestRun(ctx context.Context) (RunSummary, error) {
	var runID uuid.UUID
	err := mover.pool.QueryRow(ctx, `
		SELECT run_id
		FROM library_file_moves
		GROUP BY run_id
		ORDER BY min(planned_at) DESC
		LIMIT 1
	`).Scan(&runID)
	if errors.Is(err, pgx.ErrNoRows) {
		return RunSummary{}, ErrRunNotFound
	}
	if err != nil {
		return RunSummary{}, err
	}
	return mover.Run(ctx, runID)
}

// moves reads the first page of a run, worst first: a migration is read for
// what went wrong with it, and what is still to come is more interesting than
// what already worked.
func (mover *Mover) moves(ctx context.Context, runID uuid.UUID) ([]MoveRow, error) {
	rows, err := mover.pool.Query(ctx, `
		SELECT id, library_file_id, from_path, to_path, status, coalesce(error, ''),
		       planned_at, moved_at
		FROM library_file_moves
		WHERE run_id = $1
		ORDER BY array_position(
			ARRAY['failed', 'planned', 'moved', 'reverted'], status
		), from_path
		LIMIT $2
	`, runID, planSample)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	moves := make([]MoveRow, 0, planSample)
	for rows.Next() {
		var move MoveRow
		if err := rows.Scan(&move.ID, &move.LibraryFileID, &move.FromPath, &move.ToPath,
			&move.Status, &move.Error, &move.PlannedAt, &move.MovedAt); err != nil {
			return nil, err
		}
		moves = append(moves, move)
	}
	return moves, rows.Err()
}

// Apply renames a batch of a run's planned files and reports whether more are
// waiting. See moveBatch for why it is a batch.
//
// An error here is the run being unworkable — the database is gone — and not a
// file that could not be moved. A refusal about one file is written against
// that file and the run carries on: a migration that stopped at the first
// awkward folder would leave the library in two layouts and no closer to one.
func (mover *Mover) Apply(ctx context.Context, runID uuid.UUID) (bool, error) {
	for moved := 0; moved < moveBatch; moved++ {
		claimed, err := mover.applyNext(ctx, runID)
		if err != nil {
			return false, err
		}
		if !claimed {
			return false, nil
		}
	}
	open, err := mover.openRun(ctx)
	if err != nil {
		return false, err
	}
	return open == runID, nil
}

// claimedMove is one planned row, held for the length of its transaction.
type claimedMove struct {
	id     uuid.UUID
	fileID uuid.UUID
	// rootPath bounds the tidying up. A folder emptied by a move is removed, and
	// the walk up from it has to stop somewhere it cannot mistake for rubbish.
	rootPath string
	fromPath string
	toPath   string
}

// applyNext moves one file, and reports whether there was one to move.
//
// The rename and every row that names the path are one transaction, so the
// library is never a file at one path and a row at another for longer than the
// commit takes. What that transaction cannot cover is the case where the rename
// succeeds and the commit does not — the filesystem has no rollback — and that
// is precisely what the journal is for: the row is still planned, the file is
// already at its destination, and the next pass finishes the paperwork rather
// than refusing a move that has already happened.
func (mover *Mover) applyNext(ctx context.Context, runID uuid.UUID) (bool, error) {
	tx, err := mover.pool.Begin(ctx)
	if err != nil {
		return false, err
	}
	defer func() { _ = tx.Rollback(ctx) }()

	var move claimedMove
	err = tx.QueryRow(ctx, `
		SELECT moves.id, moves.library_file_id, roots.path, moves.from_path, moves.to_path
		FROM library_file_moves moves
		JOIN library_files files ON files.id = moves.library_file_id
		JOIN library_roots roots ON roots.id = files.library_root_id
		WHERE moves.run_id = $1 AND moves.status = 'planned'
		ORDER BY moves.from_path
		FOR UPDATE OF moves SKIP LOCKED
		LIMIT 1
	`, runID).Scan(
		&move.id, &move.fileID, &move.rootPath, &move.fromPath, &move.toPath,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}

	refusal, err := mover.check(ctx, tx, move)
	if err != nil {
		return true, err
	}
	if refusal != "" {
		return true, settle(ctx, tx, move.id, "failed", refusal)
	}

	// The row moves first. If the rename then fails there is nothing to undo:
	// the transaction is abandoned and the failure recorded on its own.
	if _, err := tx.Exec(ctx, `
		UPDATE library_files SET path = $2, updated_at = now() WHERE id = $1
	`, move.fileID, move.toPath); err != nil {
		return true, mover.abandon(ctx, tx, move, err)
	}
	if err := followPath(ctx, tx, move.fromPath, move.toPath); err != nil {
		return true, mover.abandon(ctx, tx, move, err)
	}

	// A file already at its destination is a rename whose transaction did not
	// survive it. Renaming again would fail on a source that is no longer
	// there; the move is finished instead.
	if exists(move.fromPath) {
		if err := os.MkdirAll(filepath.Dir(move.toPath), 0o755); err != nil {
			return true, mover.abandon(ctx, tx, move, err)
		}
		if err := os.Rename(move.fromPath, move.toPath); err != nil {
			return true, mover.abandon(ctx, tx, move, err)
		}
	}
	if err := settle(ctx, tx, move.id, "moved", ""); err != nil {
		return true, err
	}
	mover.carrySidecars(move)
	discardEmptied(filepath.Dir(move.fromPath), move.rootPath)
	mover.logger.Debug().Str("run_id", runID.String()).
		Str("from", move.fromPath).Str("to", move.toPath).Msg("library file moved")
	return true, nil
}

// carrySidecars takes the files Schall itself wrote beside a moved audio file
// along with it.
//
// Two features write into a release's folder without owning a library file
// there. The cover pass writes a picture named cover, folder or front, so that
// a player has something to show. The lyrics pass writes an .lrc sidecar with
// the same basename as each audio file, so that a player has words to display.
// The mover plans a move for library files alone and knows nothing about
// either, so before this they stayed where they were: the new folder had no
// picture and no words until the sweeps came round again, and the old folder
// was not empty, so discardEmptied left it standing. One migration left a shell
// of pictures and lyrics for music that was not there, once per release.
//
// Nothing here is evidence and nothing here decides anything. A picture and a
// lyric are display, and moving them changes only where they are shown from.
//
// Every failure is logged and swallowed. The audio has already moved and its
// row already says so; a sidecar that could not follow is a picture to fetch
// again, and taking the migration down over one is the worse answer. What is
// left behind keeps its folder alive, which is the honest outcome — the folder
// still holds something.
//
// The cover is per folder, not per file, so it travels with whichever file of
// that folder moves first and the rest find it gone. A destination that already
// has one is left alone.
func (mover *Mover) carrySidecars(move claimedMove) {
	if words := lyrics.SidecarFor(move.fromPath); words != "" && exists(words) {
		mover.follow(words, lyrics.SidecarFor(move.toPath))
	}

	fromFolder, toFolder := filepath.Dir(move.fromPath), filepath.Dir(move.toPath)
	if fromFolder == toFolder {
		return
	}
	if _, pictured := coverart.CoverIn(toFolder); pictured {
		return
	}
	if picture, ok := coverart.CoverIn(fromFolder); ok {
		mover.follow(picture, filepath.Join(toFolder, filepath.Base(picture)))
	}
}

// follow renames one sidecar to where the audio it belongs to has gone. A
// destination that already exists is left as it is: something is there under
// that name, and a migration is not the place to decide which copy wins.
func (mover *Mover) follow(from, to string) {
	if from == to || exists(to) {
		return
	}
	if err := os.MkdirAll(filepath.Dir(to), 0o755); err != nil {
		mover.logger.Warn().Err(err).Str("from", from).Str("to", to).
			Msg("could not make the folder a sidecar was to move into")
		return
	}
	if err := os.Rename(from, to); err != nil {
		mover.logger.Warn().Err(err).Str("from", from).Str("to", to).
			Msg("a sidecar was left behind by the layout migration")
	}
}

// discardEmptied removes the folder a file has just left, and its parent after
// it, for as long as they are empty and inside the library root.
//
// A migration that empties a folder and leaves it standing leaves the whole
// shape of the old layout behind: after one run the library held every folder it
// used to plus every folder it now has, and nothing on disk said which was
// which. Only os.Remove is used, which refuses a folder with anything at all in
// it, so a folder holding artwork, a cue sheet or a file Schall never knew about
// survives untouched — the emptiness is the permission, and it is never assumed.
func discardEmptied(folder, rootPath string) {
	root := filepath.Clean(rootPath)
	for folder = filepath.Clean(folder); strings.HasPrefix(folder, root+string(filepath.Separator)); {
		if os.Remove(folder) != nil {
			return
		}
		folder = filepath.Dir(folder)
	}
}

// check reads the world as it is now and reports why this move must not happen,
// or "" if it may. Every answer here is a refusal rather than a resolution: two
// files at one path is a question about which one the library keeps, and
// nothing about a layout answers it.
func (mover *Mover) check(ctx context.Context, tx pgx.Tx, move claimedMove) (string, error) {
	var currentPath string
	err := tx.QueryRow(ctx, `
		SELECT path FROM library_files WHERE id = $1 FOR UPDATE
	`, move.fileID).Scan(&currentPath)
	if errors.Is(err, pgx.ErrNoRows) {
		return "the library no longer holds this file", nil
	}
	if err != nil {
		return "", err
	}
	// A row that already carries the destination is a move most of the way
	// through, not a stranger; anything else is a file that went somewhere this
	// plan did not send it, and a plan about where it used to be is void.
	if currentPath != move.fromPath && currentPath != move.toPath {
		return "the file is no longer at the path this move was planned from", nil
	}

	var taken bool
	if err := tx.QueryRow(ctx, `
		SELECT EXISTS (SELECT 1 FROM library_files WHERE path = $1 AND id <> $2)
	`, move.toPath, move.fileID).Scan(&taken); err != nil {
		return "", err
	}
	if taken {
		return "another library file is already recorded at the destination", nil
	}

	switch here, there := exists(move.fromPath), exists(move.toPath); {
	case here && there:
		return "a file is already at the destination", nil
	case !here && !there:
		return "the file is no longer on disc", nil
	}
	return "", nil
}

// followPath carries the move to every other place the schema stores this path.
//
// A path is a lookup key here, not a foreign key: what is attached to the file
// is attached to its identifier and was never disturbed. Two places store the
// file's own path and both follow it. One stores the folder an import wrote,
// and it follows only once the last file that import placed has left — a folder
// half of whose files have moved is not somewhere else, it is scattered, and
// naming either half would be a claim about the release that is not true.
//
// Two places deliberately do not follow, and both are records of what was
// agreed at the time rather than of where a file is:
// navidrome_playlist_snapshot_tracks.library_path is the path Schall pushed to
// the player (ADR 0010), and download_import_reviews.detail is the
// audited line saying where an import landed.
func followPath(ctx context.Context, tx pgx.Tx, from, to string) error {
	if _, err := tx.Exec(ctx, `
		UPDATE downloads SET local_path = $2, updated_at = now() WHERE local_path = $1
	`, from, to); err != nil {
		return fmt.Errorf("carry the download provenance to %s: %w", to, err)
	}
	fromFolder, toFolder := filepath.Dir(from), filepath.Dir(to)
	if fromFolder == toFolder {
		return nil
	}
	if _, err := tx.Exec(ctx, `
		UPDATE download_requests
		SET import_path = $2, updated_at = now()
		WHERE import_path = $1
		  AND NOT EXISTS (
		      SELECT 1
		      FROM downloads
		      WHERE downloads.download_request_id = download_requests.id
		        AND downloads.local_path IS NOT NULL
		        AND left(downloads.local_path, length($1) + 1) = $1 || '/'
		  )
	`, fromFolder, toFolder); err != nil {
		return fmt.Errorf("carry the import folder to %s: %w", toFolder, err)
	}
	return nil
}

// abandon throws away a move that could not be completed and records why,
// outside the transaction that failed. The rename either did not run or did not
// stick; either way the row still says where the file was.
func (mover *Mover) abandon(ctx context.Context, tx pgx.Tx, move claimedMove, cause error) error {
	_ = tx.Rollback(ctx)
	mover.logger.Warn().Err(cause).Str("from", move.fromPath).Str("to", move.toPath).
		Msg("library file move refused")
	_, err := mover.pool.Exec(ctx, `
		UPDATE library_file_moves
		SET status = 'failed', error = $2
		WHERE id = $1 AND status = 'planned'
	`, move.id, cause.Error())
	return err
}

// settle closes a journal row and commits the transaction it was claimed in.
func settle(ctx context.Context, tx pgx.Tx, moveID uuid.UUID, status, reason string) error {
	var recorded any
	if reason != "" {
		recorded = reason
	}
	if _, err := tx.Exec(ctx, `
		UPDATE library_file_moves
		SET status = $2,
		    error = $3,
		    moved_at = CASE WHEN $2 IN ('moved', 'reverted') THEN now() END
		WHERE id = $1
	`, moveID, status, recorded); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// exists reports whether anything at all is at this path. A path that cannot be
// read is treated as occupied: refusing a move is always the safe answer.
func exists(path string) bool {
	_, err := os.Lstat(path)
	return !errors.Is(err, os.ErrNotExist)
}

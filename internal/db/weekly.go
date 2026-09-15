package db

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgtype"
)

// ErrWeeklyRunInFlight is returned when a second weekly refresh is asked for
// while one is still running. One refresh at a time is a database rule
// (`weekly_playlist_runs_live_idx`), not a convention this package keeps.
var ErrWeeklyRunInFlight = errors.New("a weekly playlist refresh is already running")

// WeeklyPlaylistSettingsRow is whether the weekly playlist runs at all, how big
// it is, and whether it may remove anything.
type WeeklyPlaylistSettingsRow struct {
	Enabled      bool
	SongsPerWeek int32
	Mode         string
	LibraryShare int32
	OnePerArtist bool
	UpdatedAt    pgtype.Timestamptz
}

// SaveWeeklyPlaylistSettingsParams is what a person set on the settings screen.
type SaveWeeklyPlaylistSettingsParams struct {
	Enabled      bool
	SongsPerWeek int32
	Mode         string
	LibraryShare int32
	OnePerArtist bool
}

const weeklyPlaylistSettingsColumns = `
	weekly_playlist_settings.enabled, weekly_playlist_settings.songs_per_week,
	weekly_playlist_settings.mode, weekly_playlist_settings.library_share,
	weekly_playlist_settings.one_per_artist, weekly_playlist_settings.updated_at
`

// WeeklyPlaylistSettings returns the settings row, or pgx.ErrNoRows when nobody
// has ever saved one. No row means the feature is off, which is the same shape
// the slskd and Navidrome settings use.
func (q *Queries) WeeklyPlaylistSettings(ctx context.Context) (WeeklyPlaylistSettingsRow, error) {
	return scanWeeklyPlaylistSettings(q.db.QueryRow(ctx, `
		SELECT`+weeklyPlaylistSettingsColumns+`
		FROM weekly_playlist_settings
		WHERE singleton
	`))
}

// SaveWeeklyPlaylistSettings writes the one settings row.
func (q *Queries) SaveWeeklyPlaylistSettings(
	ctx context.Context, params SaveWeeklyPlaylistSettingsParams,
) (WeeklyPlaylistSettingsRow, error) {
	return scanWeeklyPlaylistSettings(q.db.QueryRow(ctx, `
		INSERT INTO weekly_playlist_settings (
			enabled, songs_per_week, mode, library_share, one_per_artist
		)
		VALUES ($1, $2, $3, $4, $5)
		ON CONFLICT (singleton) DO UPDATE SET
			enabled = excluded.enabled,
			songs_per_week = excluded.songs_per_week,
			mode = excluded.mode,
			library_share = excluded.library_share,
			one_per_artist = excluded.one_per_artist,
			updated_at = now()
		RETURNING`+weeklyPlaylistSettingsColumns,
		params.Enabled, params.SongsPerWeek, params.Mode,
		params.LibraryShare, params.OnePerArtist,
	))
}

func scanWeeklyPlaylistSettings(row pgx.Row) (WeeklyPlaylistSettingsRow, error) {
	var settings WeeklyPlaylistSettingsRow
	if err := row.Scan(
		&settings.Enabled, &settings.SongsPerWeek, &settings.Mode,
		&settings.LibraryShare, &settings.OnePerArtist, &settings.UpdatedAt,
	); err != nil {
		return WeeklyPlaylistSettingsRow{}, err
	}
	return settings, nil
}

// WeeklyPlaylistRunRow is the account of one refresh.
type WeeklyPlaylistRunRow struct {
	ID            uuid.UUID
	PlaylistID    uuid.NullUUID
	Mode          string
	Status        string
	Detail        string
	ChosenCount   int32
	WantedCount   int32
	ArrivedCount  int32
	KeptCount     int32
	ExtendedCount int32
	LeavingCount  int32
	RemovedCount  int32
	LibraryCount  int32
	ReplacedCount int32
	LibraryShare  int32
	OnePerArtist  bool
	StartedAt     pgtype.Timestamptz
	FinishedAt    pgtype.Timestamptz
}

const weeklyRunColumns = `
	weekly_playlist_runs.id, weekly_playlist_runs.playlist_id, weekly_playlist_runs.mode,
	weekly_playlist_runs.status, weekly_playlist_runs.detail,
	weekly_playlist_runs.chosen_count, weekly_playlist_runs.wanted_count,
	weekly_playlist_runs.arrived_count, weekly_playlist_runs.kept_count,
	weekly_playlist_runs.extended_count, weekly_playlist_runs.leaving_count,
	weekly_playlist_runs.removed_count, weekly_playlist_runs.library_count,
	weekly_playlist_runs.replaced_count, weekly_playlist_runs.library_share,
	weekly_playlist_runs.one_per_artist,
	weekly_playlist_runs.started_at, weekly_playlist_runs.finished_at
`

// EnsureWeeklyPlaylist returns the one weekly playlist, creating it the first
// time. There is one for the life of the installation: it is rewritten every
// week rather than replaced, and the runs are what carry the history. The name
// and description follow the caller's, so a rebrand lands on the next refresh.
func (q *Queries) EnsureWeeklyPlaylist(ctx context.Context, name string) (PlaylistRow, error) {
	return scanPlaylist(q.db.QueryRow(ctx, `
		INSERT INTO playlists (source, name, description)
		VALUES ('weekly', $1, $2)
		ON CONFLICT ((source = 'weekly')) WHERE source = 'weekly' DO UPDATE SET
			name = EXCLUDED.name,
			description = EXCLUDED.description,
			updated_at = now()
		RETURNING`+playlistColumns+`,`+playlistEntryCounts,
		name, "Songs Schall picked for this week. Star one in your player to keep it.",
	))
}

// WeeklyPlaylist returns the weekly playlist, or pgx.ErrNoRows before the first
// refresh has made one.
func (q *Queries) WeeklyPlaylist(ctx context.Context) (PlaylistRow, error) {
	return scanPlaylist(q.db.QueryRow(ctx, `
		SELECT`+playlistColumns+`,`+playlistEntryCounts+`
		FROM playlists
		WHERE source = 'weekly'
	`))
}

// StartWeeklyPlaylistRunParams opens a run and records the rules it ran under.
// Mode, the mix and the per-artist rule are copied onto the row because the
// report a person reads a month later has to say what the rules were that week.
type StartWeeklyPlaylistRunParams struct {
	PlaylistID   uuid.NullUUID
	Mode         string
	LibraryShare int32
	OnePerArtist bool
}

// StartWeeklyPlaylistRun opens the account of one refresh. A second one while
// the first is still running is refused by the database, and that refusal
// arrives here as ErrWeeklyRunInFlight.
func (q *Queries) StartWeeklyPlaylistRun(
	ctx context.Context, params StartWeeklyPlaylistRunParams,
) (WeeklyPlaylistRunRow, error) {
	run, err := scanWeeklyRun(q.db.QueryRow(ctx, `
		INSERT INTO weekly_playlist_runs (playlist_id, mode, library_share, one_per_artist)
		VALUES ($1, $2, $3, $4)
		RETURNING`+weeklyRunColumns,
		params.PlaylistID, params.Mode, params.LibraryShare, params.OnePerArtist,
	))
	var failure *pgconn.PgError
	if errors.As(err, &failure) && failure.Code == "23505" &&
		failure.ConstraintName == "weekly_playlist_runs_live_idx" {
		return WeeklyPlaylistRunRow{}, ErrWeeklyRunInFlight
	}
	return run, err
}

// FinishWeeklyPlaylistRunParams closes a run with what it did.
type FinishWeeklyPlaylistRunParams struct {
	ID            uuid.UUID
	Status        string
	Detail        string
	ChosenCount   int32
	WantedCount   int32
	ArrivedCount  int32
	KeptCount     int32
	ExtendedCount int32
	LeavingCount  int32
	RemovedCount  int32
	LibraryCount  int32
	ReplacedCount int32
}

// FinishWeeklyPlaylistRun writes the counts and closes the run.
func (q *Queries) FinishWeeklyPlaylistRun(
	ctx context.Context, params FinishWeeklyPlaylistRunParams,
) (WeeklyPlaylistRunRow, error) {
	return scanWeeklyRun(q.db.QueryRow(ctx, `
		UPDATE weekly_playlist_runs SET
			status = $2,
			detail = $3,
			chosen_count = $4,
			wanted_count = $5,
			arrived_count = $6,
			kept_count = $7,
			extended_count = $8,
			leaving_count = $9,
			removed_count = $10,
			library_count = $11,
			replaced_count = $12,
			finished_at = now()
		WHERE id = $1 AND status = 'running'
		RETURNING`+weeklyRunColumns,
		params.ID, params.Status, params.Detail,
		params.ChosenCount, params.WantedCount, params.ArrivedCount,
		params.KeptCount, params.ExtendedCount, params.LeavingCount, params.RemovedCount,
		params.LibraryCount, params.ReplacedCount,
	))
}

// AbandonRunningWeeklyPlaylistRuns closes any run left open by a restart.
//
// A run is a row that says "a refresh is happening now", and one refresh at a
// time is enforced by an index over exactly that. If the process stops in the
// middle — a container restart, a machine losing power — the row stays open and
// no later refresh can ever start. Startup closes it as failed, saying so, which
// is honest: nobody knows how far it got.
func (q *Queries) AbandonRunningWeeklyPlaylistRuns(ctx context.Context, detail string) (int64, error) {
	tag, err := q.db.Exec(ctx, `
		UPDATE weekly_playlist_runs
		SET status = 'failed', detail = $1, finished_at = now()
		WHERE status = 'running'
	`, detail)
	if err != nil {
		return 0, fmt.Errorf("close weekly playlist runs left open: %w", err)
	}
	return tag.RowsAffected(), nil
}

// WeeklyPlaylistRuns returns the most recent runs, newest first.
func (q *Queries) WeeklyPlaylistRuns(ctx context.Context, limit int32) ([]WeeklyPlaylistRunRow, error) {
	rows, err := q.db.Query(ctx, `
		SELECT`+weeklyRunColumns+`
		FROM weekly_playlist_runs
		ORDER BY started_at DESC
		LIMIT $1
	`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	result := make([]WeeklyPlaylistRunRow, 0, limit)
	for rows.Next() {
		run, err := scanWeeklyRun(rows)
		if err != nil {
			return nil, err
		}
		result = append(result, run)
	}
	return result, rows.Err()
}

func scanWeeklyRun(row pgx.Row) (WeeklyPlaylistRunRow, error) {
	var run WeeklyPlaylistRunRow
	if err := row.Scan(
		&run.ID, &run.PlaylistID, &run.Mode, &run.Status, &run.Detail,
		&run.ChosenCount, &run.WantedCount, &run.ArrivedCount, &run.KeptCount,
		&run.ExtendedCount, &run.LeavingCount, &run.RemovedCount,
		&run.LibraryCount, &run.ReplacedCount, &run.LibraryShare, &run.OnePerArtist,
		&run.StartedAt, &run.FinishedAt,
	); err != nil {
		return WeeklyPlaylistRunRow{}, err
	}
	return run, nil
}

// LibraryFileLeaseRow is one file on trial, with what a person needs to read
// about it beside it.
type LibraryFileLeaseRow struct {
	ID                     uuid.UUID
	LibraryFileID          uuid.NullUUID
	LibraryPath            string
	MusicBrainzRecordingID uuid.UUID
	RunID                  uuid.UUID
	AcquisitionTargetID    uuid.UUID
	State                  string
	GrantedAt              pgtype.Timestamptz
	ExpiresAt              pgtype.Timestamptz
	Extensions             int32
	ExtendedReason         string
	RemovesAt              pgtype.Timestamptz
	LeavingRunID           uuid.NullUUID
	KeptBy                 pgtype.Text
	ReleasedAt             pgtype.Timestamptz
	RemovedAt              pgtype.Timestamptz
	SettledRunID           uuid.NullUUID

	// What the want asked for, so a row can be read without opening anything.
	EntryArtist string
	EntryTitle  string
	// Whether the file is still on disc as far as the scanner knows. A lease
	// whose file has gone missing is not judged; there is nothing to delete.
	FileMissingAt pgtype.Timestamptz
}

const leaseColumns = `
	library_file_leases.id, library_file_leases.library_file_id,
	library_file_leases.library_path, library_file_leases.musicbrainz_recording_id,
	library_file_leases.run_id, library_file_leases.acquisition_target_id,
	library_file_leases.state, library_file_leases.granted_at,
	library_file_leases.expires_at, library_file_leases.extensions,
	library_file_leases.extended_reason, library_file_leases.removes_at,
	library_file_leases.leaving_run_id, library_file_leases.kept_by,
	library_file_leases.released_at, library_file_leases.removed_at,
	library_file_leases.settled_run_id,
	acquisition_targets.entry_artist, acquisition_targets.entry_title,
	library_files.missing_at
`

const leaseJoins = `
	FROM library_file_leases
	JOIN acquisition_targets
	    ON acquisition_targets.id = library_file_leases.acquisition_target_id
	LEFT JOIN library_files
	    ON library_files.id = library_file_leases.library_file_id
`

// GrantLibraryFileLeaseParams is one file put on trial by one run.
type GrantLibraryFileLeaseParams struct {
	LibraryFileID          uuid.UUID
	LibraryPath            string
	MusicBrainzRecordingID uuid.UUID
	RunID                  uuid.UUID
	AcquisitionTargetID    uuid.UUID
	ExpiresAt              time.Time
}

// GrantLibraryFileLease puts one file on trial.
//
// A lease is the only thing in Schall that makes a file eligible for automatic
// deletion. It names the run that granted it and the want it filled, and both
// are required by the table, so a file that arrived any other way can never be
// reached from here. Granting a second live lease on one file is refused by
// `library_file_leases_live_idx`.
func (q *Queries) GrantLibraryFileLease(
	ctx context.Context, params GrantLibraryFileLeaseParams,
) (LibraryFileLeaseRow, error) {
	var id uuid.UUID
	err := q.db.QueryRow(ctx, `
		INSERT INTO library_file_leases (
			library_file_id, library_path, musicbrainz_recording_id,
			run_id, acquisition_target_id, expires_at
		)
		VALUES ($1, $2, $3, $4, $5, $6)
		RETURNING id
	`,
		params.LibraryFileID, params.LibraryPath, params.MusicBrainzRecordingID,
		params.RunID, params.AcquisitionTargetID, params.ExpiresAt,
	).Scan(&id)
	if err != nil {
		return LibraryFileLeaseRow{}, err
	}
	return q.LibraryFileLease(ctx, id)
}

// LibraryFileLease returns one lease, or pgx.ErrNoRows.
func (q *Queries) LibraryFileLease(ctx context.Context, id uuid.UUID) (LibraryFileLeaseRow, error) {
	return scanLease(q.db.QueryRow(ctx, `
		SELECT`+leaseColumns+leaseJoins+`
		WHERE library_file_leases.id = $1
	`, id))
}

// LiveLibraryFileLeases returns every file currently on trial, oldest lease
// first. This is what the weekly refresh judges and what the screen shows.
func (q *Queries) LiveLibraryFileLeases(ctx context.Context) ([]LibraryFileLeaseRow, error) {
	return q.leases(ctx, `
		WHERE library_file_leases.state IN ('held', 'leaving')
		ORDER BY library_file_leases.granted_at, library_file_leases.id
	`)
}

// LiveLibraryFileLeaseForFile returns the live lease on one file, or
// pgx.ErrNoRows when that file is not on trial. It is what the Keep control
// asks before it writes anything.
func (q *Queries) LiveLibraryFileLeaseForFile(ctx context.Context, fileID uuid.UUID) (LibraryFileLeaseRow, error) {
	return scanLease(q.db.QueryRow(ctx, `
		SELECT`+leaseColumns+leaseJoins+`
		WHERE library_file_leases.library_file_id = $1
		  AND library_file_leases.state IN ('held', 'leaving')
	`, fileID))
}

func (q *Queries) leases(ctx context.Context, where string, args ...any) ([]LibraryFileLeaseRow, error) {
	rows, err := q.db.Query(ctx, `SELECT`+leaseColumns+leaseJoins+where, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	result := make([]LibraryFileLeaseRow, 0)
	for rows.Next() {
		lease, err := scanLease(rows)
		if err != nil {
			return nil, err
		}
		result = append(result, lease)
	}
	return result, rows.Err()
}

func scanLease(row pgx.Row) (LibraryFileLeaseRow, error) {
	var lease LibraryFileLeaseRow
	if err := row.Scan(
		&lease.ID, &lease.LibraryFileID, &lease.LibraryPath, &lease.MusicBrainzRecordingID,
		&lease.RunID, &lease.AcquisitionTargetID, &lease.State, &lease.GrantedAt,
		&lease.ExpiresAt, &lease.Extensions, &lease.ExtendedReason, &lease.RemovesAt,
		&lease.LeavingRunID, &lease.KeptBy, &lease.ReleasedAt, &lease.RemovedAt,
		&lease.SettledRunID, &lease.EntryArtist, &lease.EntryTitle, &lease.FileMissingAt,
	); err != nil {
		return LibraryFileLeaseRow{}, err
	}
	return lease, nil
}

// KeepLibraryFileLease ends a lease because a signal said to keep the song. The
// run is named when a refresh read the signal, and is absent when the user
// pressed Keep between refreshes.
//
// Keeping a lease that is already settled is not an error: a person pressing
// Keep at the moment a refresh keeps the same song must not see a failure.
func (q *Queries) KeepLibraryFileLease(
	ctx context.Context, id uuid.UUID, keptBy string, runID uuid.NullUUID,
) (bool, error) {
	tag, err := q.db.Exec(ctx, `
		UPDATE library_file_leases SET
			state = 'kept',
			kept_by = $2,
			released_at = now(),
			settled_run_id = $3,
			removes_at = NULL,
			leaving_run_id = NULL
		WHERE id = $1 AND state IN ('held', 'leaving')
	`, id, keptBy, runID)
	if err != nil {
		return false, fmt.Errorf("keep lease %s: %w", id, err)
	}
	return tag.RowsAffected() == 1, nil
}

// AnnounceLibraryFileLeaseDeparture is the grace week. A refresh that found no
// keep writes the date instead of deleting anything, and the refresh whose date
// has arrived reads the signals again before it carries it out. Nothing is
// deleted that was not announced a week earlier.
func (q *Queries) AnnounceLibraryFileLeaseDeparture(
	ctx context.Context, id, runID uuid.UUID, removesAt time.Time,
) error {
	tag, err := q.db.Exec(ctx, `
		UPDATE library_file_leases SET
			state = 'leaving',
			removes_at = $2,
			leaving_run_id = $3
		WHERE id = $1 AND state = 'held'
	`, id, removesAt, runID)
	if err != nil {
		return fmt.Errorf("announce departure of lease %s: %w", id, err)
	}
	if tag.RowsAffected() == 0 {
		return pgx.ErrNoRows
	}
	return nil
}

// ExtendLibraryFileLease gives a file more time and says why.
//
// It is also how an announced departure is withdrawn: a lease whose second read
// could not be made goes back to held with its date cleared, because silence
// keeps. The reason is carried so the screen can say which week could not be
// read rather than showing a date that moved for no visible cause.
func (q *Queries) ExtendLibraryFileLease(
	ctx context.Context, id uuid.UUID, expiresAt time.Time, reason string,
) error {
	tag, err := q.db.Exec(ctx, `
		UPDATE library_file_leases SET
			state = 'held',
			expires_at = $2,
			extensions = extensions + 1,
			extended_reason = $3,
			removes_at = NULL,
			leaving_run_id = NULL
		WHERE id = $1 AND state IN ('held', 'leaving')
	`, id, expiresAt, reason)
	if err != nil {
		return fmt.Errorf("extend lease %s: %w", id, err)
	}
	if tag.RowsAffected() == 0 {
		return pgx.ErrNoRows
	}
	return nil
}

// RemoveLibraryFileLease records that the run deleted the file. The audio and
// the library row are gone by the time this is written, which is why the lease
// keeps its own copy of the path.
func (q *Queries) RemoveLibraryFileLease(ctx context.Context, id, runID uuid.UUID) error {
	tag, err := q.db.Exec(ctx, `
		UPDATE library_file_leases SET
			state = 'removed',
			removed_at = now(),
			settled_run_id = $2,
			library_file_id = NULL
		WHERE id = $1 AND state = 'leaving'
	`, id, runID)
	if err != nil {
		return fmt.Errorf("record removal of lease %s: %w", id, err)
	}
	if tag.RowsAffected() == 0 {
		return pgx.ErrNoRows
	}
	return nil
}

// AbandonLibraryFileLease closes a lease whose file left by another hand — the
// user deleted it from the duplicates screen, or a scan found it gone. Nothing
// was deleted here and nothing was decided, so no want is settled and no
// recording is suppressed.
func (q *Queries) AbandonLibraryFileLease(ctx context.Context, id, runID uuid.UUID) error {
	tag, err := q.db.Exec(ctx, `
		UPDATE library_file_leases SET
			state = 'gone',
			released_at = now(),
			settled_run_id = $2,
			library_file_id = NULL,
			removes_at = NULL,
			leaving_run_id = NULL
		WHERE id = $1 AND state IN ('held', 'leaving')
	`, id, runID)
	if err != nil {
		return fmt.Errorf("close lease %s whose file has gone: %w", id, err)
	}
	if tag.RowsAffected() == 0 {
		return pgx.ErrNoRows
	}
	return nil
}

// LibraryFileKeepReadRow is one answer about one signal.
type LibraryFileKeepReadRow struct {
	ID      uuid.UUID
	LeaseID uuid.UUID
	RunID   uuid.NullUUID
	Signal  string
	Outcome string
	Detail  string
	ReadAt  pgtype.Timestamptz
}

// RecordLibraryFileKeepReadParams is one look at one signal for one lease.
type RecordLibraryFileKeepReadParams struct {
	LeaseID uuid.UUID
	RunID   uuid.NullUUID
	Signal  string
	Outcome string
	Detail  string
}

// RecordLibraryFileKeepRead writes down what a signal said.
//
// Reading the same signal twice in one run is one answer, not two, so a refresh
// that retries a read overwrites its own earlier answer rather than being
// refused. The three outcomes are kept apart on purpose: 'absent' may lead to a
// removal and 'unreadable' never may.
func (q *Queries) RecordLibraryFileKeepRead(
	ctx context.Context, params RecordLibraryFileKeepReadParams,
) error {
	_, err := q.db.Exec(ctx, `
		INSERT INTO library_file_keep_reads (lease_id, run_id, signal, outcome, detail)
		VALUES ($1, $2, $3, $4, $5)
		ON CONFLICT (lease_id, run_id, signal) DO UPDATE SET
			outcome = excluded.outcome,
			detail = excluded.detail,
			read_at = now()
	`, params.LeaseID, params.RunID, params.Signal, params.Outcome, params.Detail)
	if err != nil {
		return fmt.Errorf("record %s read for lease %s: %w", params.Signal, params.LeaseID, err)
	}
	return nil
}

// LibraryFileKeepReads returns every answer recorded about one lease, newest
// first. This is the evidence behind a removal.
func (q *Queries) LibraryFileKeepReads(ctx context.Context, leaseID uuid.UUID) ([]LibraryFileKeepReadRow, error) {
	rows, err := q.db.Query(ctx, `
		SELECT id, lease_id, run_id, signal, outcome, detail, read_at
		FROM library_file_keep_reads
		WHERE lease_id = $1
		ORDER BY read_at DESC, signal
	`, leaseID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	result := make([]LibraryFileKeepReadRow, 0)
	for rows.Next() {
		var read LibraryFileKeepReadRow
		if err := rows.Scan(
			&read.ID, &read.LeaseID, &read.RunID, &read.Signal,
			&read.Outcome, &read.Detail, &read.ReadAt,
		); err != nil {
			return nil, err
		}
		result = append(result, read)
	}
	return result, rows.Err()
}

// PressedKeeps returns the leases the user has pressed Keep on, as a set. The
// Keep button is the one signal that cannot be unreadable: it is a row in this
// database, written when the person pressed it, and reading it needs nothing
// outside Schall.
func (q *Queries) PressedKeeps(ctx context.Context) (map[uuid.UUID]struct{}, error) {
	rows, err := q.db.Query(ctx, `
		SELECT DISTINCT lease_id
		FROM library_file_keep_reads
		WHERE signal = 'schall_keep'
	`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	pressed := make(map[uuid.UUID]struct{})
	for rows.Next() {
		var leaseID uuid.UUID
		if err := rows.Scan(&leaseID); err != nil {
			return nil, err
		}
		pressed[leaseID] = struct{}{}
	}
	return pressed, rows.Err()
}

// RecordRecommendationUnkept suppresses a recording that was obtained, kept for
// a week and removed again.
//
// A recording that comes back after its window and is again not kept counts up
// and gets the window again from that day. Repeated indifference is not turned
// into a dismissal: the user never said "not interested", and only they can.
func (q *Queries) RecordRecommendationUnkept(
	ctx context.Context, recordingID uuid.UUID, runID uuid.NullUUID, suppressedUntil time.Time,
) error {
	_, err := q.db.Exec(ctx, `
		INSERT INTO recommendation_unkept (
			musicbrainz_recording_id, suppressed_until, run_id
		)
		VALUES ($1, $2, $3)
		ON CONFLICT (musicbrainz_recording_id) DO UPDATE SET
			unkept_at = now(),
			suppressed_until = excluded.suppressed_until,
			occurrences = recommendation_unkept.occurrences + 1,
			run_id = excluded.run_id
	`, recordingID, suppressedUntil, runID)
	if err != nil {
		return fmt.Errorf("suppress unkept recording %s: %w", recordingID, err)
	}
	return nil
}

// SettleWeeklyTargetNotWanted moves an obtained want to not wanted after the
// weekly refresh removed the file it delivered.
//
// This transition exists for one caller. `StopPursuingAcquisitionTarget` is what
// a person presses on the wants screen and it accepts only a live target; this
// accepts only an obtained one, so neither can reach the other's rows. The
// arrival stamp is deliberately left standing: the song did arrive, and the
// record should keep saying so.
//
// It is also what stops the acquisition sweep fetching the same song again next
// week. `acquisition_targets_recording_idx` allows one live target per
// recording, so this row holds the recording's place rather than leaving it
// free for a second want.
func (q *Queries) SettleWeeklyTargetNotWanted(ctx context.Context, targetID uuid.UUID, summary string) error {
	tag, err := q.db.Exec(ctx, `
		UPDATE acquisition_targets SET
			status = 'not_wanted',
			not_wanted_at = now(),
			next_attempt_at = NULL,
			summary = $2,
			updated_at = now()
		WHERE id = $1 AND status = 'acquired'
	`, targetID, summary)
	if err != nil {
		return fmt.Errorf("settle want %s after removal: %w", targetID, err)
	}
	if tag.RowsAffected() == 0 {
		return pgx.ErrNoRows
	}
	return nil
}

// WeeklyPlaylistArrivalRow is one song on the weekly playlist that has arrived
// and is not yet on trial.
type WeeklyPlaylistArrivalRow struct {
	EntryID                uuid.UUID
	TargetID               uuid.UUID
	LibraryFileID          uuid.UUID
	LibraryPath            string
	MusicBrainzRecordingID uuid.UUID
	EntryArtist            string
	EntryTitle             string
}

// WeeklyPlaylistArrivals returns the entries of the weekly playlist whose want
// has been obtained, whose file is still on disc, and which carry no lease yet.
//
// The lease is granted here, by a run, rather than at the moment the download is
// imported. The import path knows nothing about this feature and must keep
// knowing nothing: a file becomes deletable because a weekly run put it on
// trial, never because of how it arrived.
func (q *Queries) WeeklyPlaylistArrivals(
	ctx context.Context, playlistID uuid.UUID,
) ([]WeeklyPlaylistArrivalRow, error) {
	rows, err := q.db.Query(ctx, `
		SELECT playlist_entries.id, acquisition_targets.id,
		       library_files.id, library_files.path,
		       acquisition_targets.musicbrainz_recording_id,
		       acquisition_targets.entry_artist, acquisition_targets.entry_title
		FROM playlist_entries
		JOIN playlist_entry_targets
		    ON playlist_entry_targets.playlist_entry_id = playlist_entries.id
		JOIN acquisition_targets
		    ON acquisition_targets.id = playlist_entry_targets.acquisition_target_id
		JOIN library_files
		    ON library_files.id = acquisition_targets.acquired_library_file_id
		WHERE playlist_entries.playlist_id = $1
		  AND acquisition_targets.status = 'acquired'
		  AND acquisition_targets.musicbrainz_recording_id IS NOT NULL
		  AND library_files.missing_at IS NULL
		  AND NOT EXISTS (
		      SELECT 1 FROM library_file_leases
		      WHERE library_file_leases.acquisition_target_id = acquisition_targets.id
		  )
		  -- A file Schall holds by its address on another service is passed
		  -- over. The weekly playlist decides what to keep and what to delete by
		  -- recording identifier: it suppresses a song the library already
		  -- holds, and it reads the player's stars back against the recording.
		  -- There is no recording here, so none of that can be done honestly,
		  -- and a lease is the only licence to delete music (ADR 0021). A file
		  -- the machinery cannot reason about is never put on trial.
		  AND NOT EXISTS (
		      SELECT 1 FROM library_file_identities
		      WHERE library_file_identities.library_file_id = library_files.id
		        AND library_file_identities.kind = 'source'
		  )
		ORDER BY playlist_entries.position
	`, playlistID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	result := make([]WeeklyPlaylistArrivalRow, 0)
	for rows.Next() {
		var arrival WeeklyPlaylistArrivalRow
		if err := rows.Scan(
			&arrival.EntryID, &arrival.TargetID, &arrival.LibraryFileID, &arrival.LibraryPath,
			&arrival.MusicBrainzRecordingID, &arrival.EntryArtist, &arrival.EntryTitle,
		); err != nil {
			return nil, err
		}
		result = append(result, arrival)
	}
	return result, rows.Err()
}

// AppendWeeklyPlaylistEntryParams is one suggestion put on this week's list.
type AppendWeeklyPlaylistEntryParams struct {
	PlaylistID             uuid.UUID
	MusicBrainzRecordingID uuid.UUID
	EntryArtist            string
	EntryTitle             string
	EntryAlbum             string
}

// AppendWeeklyPlaylistEntry adds one song to the end of the weekly playlist.
//
// The recording identifier is the entry's source track identity, so a refresh
// that picks a song already on the list refreshes that row instead of adding a
// second copy of it. The position is computed here, because the caller is adding
// to whatever the list currently holds.
func (q *Queries) AppendWeeklyPlaylistEntry(
	ctx context.Context, params AppendWeeklyPlaylistEntryParams,
) (PlaylistEntryRow, error) {
	var entry PlaylistEntryRow
	err := q.db.QueryRow(ctx, `
		INSERT INTO playlist_entries (
			playlist_id, position, source_track_id,
			entry_artist, entry_title, entry_album
		)
		VALUES (
			$1,
			(SELECT coalesce(max(position), 0) + 1 FROM playlist_entries WHERE playlist_id = $1),
			$2, $3, $4, $5
		)
		ON CONFLICT (playlist_id, source_track_id) WHERE source_track_id IS NOT NULL
		DO UPDATE SET
			entry_artist = excluded.entry_artist,
			entry_title = excluded.entry_title,
			entry_album = excluded.entry_album,
			updated_at = now()
		RETURNING id, playlist_id, position, source_track_id, entry_artist,
			entry_title, entry_album, entry_duration_ms, entry_isrc,
			owned_library_file_id
	`,
		params.PlaylistID, params.MusicBrainzRecordingID.String(),
		params.EntryArtist, params.EntryTitle, params.EntryAlbum,
	).Scan(
		&entry.ID, &entry.PlaylistID, &entry.Position, &entry.SourceTrackID,
		&entry.EntryArtist, &entry.EntryTitle, &entry.EntryAlbum,
		&entry.EntryDurationMS, &entry.EntryISRC, &entry.OwnedFileID,
	)
	if err != nil {
		return PlaylistEntryRow{}, fmt.Errorf("add recording %s to the weekly playlist: %w",
			params.MusicBrainzRecordingID, err)
	}
	return entry, nil
}

// WeeklyPlaylistOpenWants counts the entries of the weekly playlist whose want
// is still being looked for. A refresh adds songs up to the week's number
// counting these, so a slow week on Soulseek does not become a queue of two
// hundred searches.
func (q *Queries) WeeklyPlaylistOpenWants(ctx context.Context, playlistID uuid.UUID) (int, error) {
	var open int
	err := q.db.QueryRow(ctx, `
		SELECT count(*)
		FROM playlist_entries
		JOIN playlist_entry_targets
		    ON playlist_entry_targets.playlist_entry_id = playlist_entries.id
		JOIN acquisition_targets
		    ON acquisition_targets.id = playlist_entry_targets.acquisition_target_id
		WHERE playlist_entries.playlist_id = $1
		  AND acquisition_targets.status IN (
		      'unresolved', 'pending', 'searching', 'awaiting_review'
		  )
	`, playlistID).Scan(&open)
	if err != nil {
		return 0, fmt.Errorf("count open weekly wants: %w", err)
	}
	return open, nil
}

// WeeklyPlaylistStaleWants counts the open weekly wants asked for before the
// given moment. A want that has had a whole trial week and is still being
// searched for stops holding a slot, so the next refresh asks for its full
// number and the week ships full. The want itself is left standing: taking a
// slot back is not the statement "stop trying to obtain this recording", and
// making it 'not_wanted' would suppress the recording from the recommendation
// list for good over a slow week on Soulseek.
//
// 'awaiting_review' is deliberately not stale. A person has been asked a
// question about that want, and freeing its slot would answer it for them.
func (q *Queries) WeeklyPlaylistStaleWants(
	ctx context.Context, playlistID uuid.UUID, before time.Time,
) (int, error) {
	var stale int
	err := q.db.QueryRow(ctx, `
		SELECT count(*)
		FROM playlist_entries
		JOIN playlist_entry_targets
		    ON playlist_entry_targets.playlist_entry_id = playlist_entries.id
		JOIN acquisition_targets
		    ON acquisition_targets.id = playlist_entry_targets.acquisition_target_id
		WHERE playlist_entries.playlist_id = $1
		  AND acquisition_targets.status IN ('unresolved', 'pending', 'searching')
		  AND acquisition_targets.created_at < $2
		  AND NOT EXISTS (
		      SELECT 1
		      FROM download_requests
		      WHERE download_requests.acquisition_target_id = acquisition_targets.id
		        AND download_requests.status IN ('requested', 'started')
		  )
	`, playlistID, before).Scan(&stale)
	if err != nil {
		return 0, fmt.Errorf("count stale weekly wants: %w", err)
	}
	return stale, nil
}

// WeeklyPlaylistEntryArtists returns the artist of every song on the weekly
// playlist. The per-artist rule has to see the songs already there — an arrival
// on trial, a want still being looked for — or a week would stack an artist
// across two refreshes while each one looked correct on its own.
//
// The names are what was written on the entry. The playlist stores artist text
// and not MusicBrainz artist IDs, so this half of the rule matches on the name.
func (q *Queries) WeeklyPlaylistEntryArtists(ctx context.Context, playlistID uuid.UUID) ([]string, error) {
	rows, err := q.db.Query(ctx, `
		SELECT entry_artist
		FROM playlist_entries
		WHERE playlist_id = $1 AND btrim(entry_artist) <> ''
	`, playlistID)
	if err != nil {
		return nil, fmt.Errorf("read the weekly playlist artists: %w", err)
	}
	defer rows.Close()

	var artists []string
	for rows.Next() {
		var artist string
		if err := rows.Scan(&artist); err != nil {
			return nil, err
		}
		artists = append(artists, artist)
	}
	return artists, rows.Err()
}

// WeeklyLibraryCandidateRow is one song the collection already holds, offered to
// the library share of the week.
type WeeklyLibraryCandidateRow struct {
	LibraryFileID          uuid.UUID
	MusicBrainzRecordingID uuid.UUID
	Artist                 string
	Title                  string
	Album                  string
}

// WeeklyLibraryPoolCandidates returns songs the library holds that may join this
// week's list.
//
// A candidate needs a settled recording ID, a file still on disc, and a title to
// put on the playlist. It must not be on trial: a leased file is already the
// week's business and could be deleted. It must not already be on the list, and
// it must not have been offered by this pool inside the window, or a small
// collection would show the same songs every week.
//
// One row per recording. Two copies of one recording are one song to a listener,
// and which copy is offered does not matter — nothing is done to the file.
//
// Schall holds no play history, so this pool cannot prefer songs nobody has
// heard lately. It spreads over the whole collection instead, which is why the
// window exists.
func (q *Queries) WeeklyLibraryPoolCandidates(
	ctx context.Context, playlistID uuid.UUID, pickedAfter time.Time, limit int,
) ([]WeeklyLibraryCandidateRow, error) {
	rows, err := q.db.Query(ctx, `
		SELECT id, musicbrainz_recording_id, artist, title, album
		FROM (
			SELECT DISTINCT ON (library_files.musicbrainz_recording_id)
			    library_files.id AS id,
			    library_files.musicbrainz_recording_id AS musicbrainz_recording_id,
			    coalesce(library_files.artist_tag, '') AS artist,
			    coalesce(library_files.title_tag, '') AS title,
			    coalesce(library_files.album_tag, '') AS album
			FROM library_files
			WHERE library_files.musicbrainz_recording_id IS NOT NULL
			  AND library_files.missing_at IS NULL
			  AND btrim(coalesce(library_files.title_tag, '')) <> ''
			  AND btrim(coalesce(library_files.artist_tag, '')) <> ''
			  AND NOT EXISTS (
			      SELECT 1 FROM library_file_leases
			      WHERE library_file_leases.library_file_id = library_files.id
			        AND library_file_leases.state IN ('held', 'leaving')
			  )
			  AND NOT EXISTS (
			      SELECT 1 FROM weekly_library_picks
			      WHERE weekly_library_picks.musicbrainz_recording_id
			                = library_files.musicbrainz_recording_id
			        AND weekly_library_picks.picked_at > $2
			  )
			  AND NOT EXISTS (
			      SELECT 1 FROM playlist_entries
			      WHERE playlist_entries.playlist_id = $1
			        AND playlist_entries.source_track_id
			                = library_files.musicbrainz_recording_id::text
			  )
			ORDER BY library_files.musicbrainz_recording_id, library_files.id
		) AS pool
		ORDER BY random()
		LIMIT $3
	`, playlistID, pickedAfter, limit)
	if err != nil {
		return nil, fmt.Errorf("read the weekly library pool: %w", err)
	}
	defer rows.Close()

	var candidates []WeeklyLibraryCandidateRow
	for rows.Next() {
		var candidate WeeklyLibraryCandidateRow
		if err := rows.Scan(
			&candidate.LibraryFileID, &candidate.MusicBrainzRecordingID,
			&candidate.Artist, &candidate.Title, &candidate.Album,
		); err != nil {
			return nil, err
		}
		candidates = append(candidates, candidate)
	}
	return candidates, rows.Err()
}

// RecordWeeklyLibraryPick remembers that the library pool offered this recording.
// The playlist entry is deleted when the week rotates, so this row is the only
// record the window can be measured against.
func (q *Queries) RecordWeeklyLibraryPick(
	ctx context.Context, recordingID, fileID uuid.UUID, runID uuid.NullUUID,
) error {
	_, err := q.db.Exec(ctx, `
		INSERT INTO weekly_library_picks (musicbrainz_recording_id, library_file_id, run_id)
		VALUES ($1, $2, $3)
		ON CONFLICT (musicbrainz_recording_id) DO UPDATE SET
			library_file_id = excluded.library_file_id,
			run_id = excluded.run_id,
			picked_at = now()
	`, recordingID, fileID, runID)
	if err != nil {
		return fmt.Errorf("record the library pick %s: %w", recordingID, err)
	}
	return nil
}

// DropWeeklyLibraryEntries takes last week's library songs off the playlist.
//
// A library song has an owned file and no want, which is what identifies it
// here. It was never leased, so this deletes a playlist row and nothing else:
// the file on disc is untouched, whatever the mode says. That is the whole
// safety argument for the library pool, and it is enforced by the absence of a
// lease rather than by this query.
func (q *Queries) DropWeeklyLibraryEntries(ctx context.Context, playlistID uuid.UUID) (int64, error) {
	tag, err := q.db.Exec(ctx, `
		DELETE FROM playlist_entries
		WHERE playlist_entries.playlist_id = $1
		  AND playlist_entries.owned_library_file_id IS NOT NULL
		  AND NOT EXISTS (
		      SELECT 1 FROM playlist_entry_targets
		      WHERE playlist_entry_targets.playlist_entry_id = playlist_entries.id
		  )
	`, playlistID)
	if err != nil {
		return 0, fmt.Errorf("rotate the weekly library songs: %w", err)
	}
	return tag.RowsAffected(), nil
}

// DropSettledWeeklyPlaylistEntries takes off the weekly playlist the songs whose
// trial is over — kept, removed, or gone. A kept song is the user's now and
// lives in the library like any other; a removed one is not there to play. The
// want each entry created is left standing, as it is everywhere else: taking a
// track off a list is not the statement "stop trying to obtain this recording".
func (q *Queries) DropSettledWeeklyPlaylistEntries(ctx context.Context, playlistID uuid.UUID) (int64, error) {
	tag, err := q.db.Exec(ctx, `
		DELETE FROM playlist_entries
		WHERE playlist_entries.playlist_id = $1
		  AND EXISTS (
		      SELECT 1
		      FROM playlist_entry_targets
		      JOIN library_file_leases
		          ON library_file_leases.acquisition_target_id
		              = playlist_entry_targets.acquisition_target_id
		      WHERE playlist_entry_targets.playlist_entry_id = playlist_entries.id
		        AND library_file_leases.state IN ('kept', 'removed', 'gone')
		  )
	`, playlistID)
	if err != nil {
		return 0, fmt.Errorf("drop settled weekly playlist entries: %w", err)
	}
	return tag.RowsAffected(), nil
}

// QueueWeeklyPlaylistRefresh asks for one refresh. The partial unique index
// allows one queued or running refresh, so asking while one is waiting is
// answered by the one that already exists.
func (q *Queries) QueueWeeklyPlaylistRefresh(ctx context.Context, runAfter time.Time) error {
	_, err := q.db.Exec(ctx, `
		INSERT INTO jobs (kind, payload, run_after, max_attempts)
		VALUES ('refresh_weekly_playlist', '{}'::jsonb, $1, 1)
		ON CONFLICT (kind)
		    WHERE kind = 'refresh_weekly_playlist' AND status IN ('queued', 'running')
		DO NOTHING
	`, runAfter)
	if err != nil {
		return fmt.Errorf("queue weekly playlist refresh: %w", err)
	}
	return nil
}

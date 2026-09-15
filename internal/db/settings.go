package db

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
)

// SlskdSettingsRow carries the stored connection settings. APIKey never leaves
// the process boundary; handlers report only whether a key is configured.
type SlskdSettingsRow struct {
	BaseURL              string
	APIKey               string
	Enabled              bool
	SearchTimeoutSeconds int32
	ConnectionStatus     string
	ConnectionError      pgtype.Text
	ConnectionDetail     pgtype.Text
	LastCheckedAt        pgtype.Timestamptz
	// LoggedOutSince is when a real search was first refused because slskd was
	// not logged in to Soulseek, since the last time one went through. NULL
	// means nothing has refused this way since the last success. Set by
	// MarkSourceLoggedOut whenever acquisition hears the refusal, cleared by
	// ClearSourceLoggedOut whenever acquisition or a manual Test succeeds —
	// never guessed at from a single failed check, which is what keeps a
	// healthy source that has not been asked anything from reading as one that
	// has been signed out since forever.
	LoggedOutSince pgtype.Timestamptz
	UpdatedAt      pgtype.Timestamptz
}

type SaveSlskdSettingsParams struct {
	BaseURL              string
	APIKey               string
	Enabled              bool
	SearchTimeoutSeconds int32
}

const slskdSettingsColumns = `
	base_url, api_key, enabled, search_timeout_seconds,
	connection_status, connection_error, connection_detail,
	last_checked_at, logged_out_since, updated_at
`

// SlskdSettings returns the stored settings, or pgx.ErrNoRows when slskd has
// never been configured.
func (q *Queries) SlskdSettings(ctx context.Context) (SlskdSettingsRow, error) {
	return scanSlskdSettings(q.db.QueryRow(ctx, `
		SELECT`+slskdSettingsColumns+`
		FROM slskd_settings
		WHERE singleton
	`))
}

// unchangedEndpoint is true when neither the address nor the credential moved.
// A previous connection result only stays meaningful in that case; anything
// else must be rechecked before it can be trusted.
const unchangedEndpoint = `
	slskd_settings.base_url IS NOT DISTINCT FROM excluded.base_url
	AND slskd_settings.api_key IS NOT DISTINCT FROM excluded.api_key
`

// SaveSlskdSettings stores the connection settings. An empty API key keeps the
// stored key so the UI can update other fields without echoing the secret back
// to the server. The key is resolved in the inserted row rather than in the
// conflict branch, because PostgreSQL checks table constraints against the
// proposed tuple before it detects the conflict.
func (q *Queries) SaveSlskdSettings(ctx context.Context, params SaveSlskdSettingsParams) (SlskdSettingsRow, error) {
	return scanSlskdSettings(q.db.QueryRow(ctx, `
		INSERT INTO slskd_settings (base_url, api_key, enabled, search_timeout_seconds)
		VALUES (
			$1,
			coalesce(
				nullif($2::text, ''),
				(SELECT api_key FROM slskd_settings WHERE singleton),
				''
			),
			$3, $4
		)
		ON CONFLICT (singleton) DO UPDATE SET
			base_url = excluded.base_url,
			api_key = excluded.api_key,
			enabled = excluded.enabled,
			search_timeout_seconds = excluded.search_timeout_seconds,
			connection_status = CASE WHEN`+unchangedEndpoint+`
				THEN slskd_settings.connection_status ELSE 'unknown' END,
			connection_error = CASE WHEN`+unchangedEndpoint+`
				THEN slskd_settings.connection_error ELSE NULL END,
			connection_detail = CASE WHEN`+unchangedEndpoint+`
				THEN slskd_settings.connection_detail ELSE NULL END,
			last_checked_at = CASE WHEN`+unchangedEndpoint+`
				THEN slskd_settings.last_checked_at ELSE NULL END,
			-- A new endpoint has said nothing about being signed out; the clock
			-- an old one left running says nothing about it either.
			logged_out_since = CASE WHEN`+unchangedEndpoint+`
				THEN slskd_settings.logged_out_since ELSE NULL END,
			updated_at = now()
		RETURNING`+slskdSettingsColumns,
		params.BaseURL, params.APIKey, params.Enabled, params.SearchTimeoutSeconds,
	))
}

// RecordSlskdConnection stores the outcome of a bounded connection check. A
// check that finds slskd logged in is real confirmation the account is back,
// so it clears a clock acquisition left running; a check that fails is a
// single poll and never starts one (see MarkSourceLoggedOut).
func (q *Queries) RecordSlskdConnection(ctx context.Context, status, detail, failure string) (SlskdSettingsRow, error) {
	return scanSlskdSettings(q.db.QueryRow(ctx, `
		UPDATE slskd_settings SET
			connection_status = $1,
			connection_detail = nullif($2, ''),
			connection_error = nullif($3, ''),
			last_checked_at = now(),
			logged_out_since = CASE WHEN $1 = 'ok' THEN NULL ELSE logged_out_since END,
			updated_at = now()
		WHERE singleton
		RETURNING`+slskdSettingsColumns,
		status, detail, failure,
	))
}

// MarkSourceLoggedOut records the first moment a real search was refused
// because slskd was not logged in to Soulseek, since the last time one went
// through. Called again while the outage continues, it leaves the clock where
// it started: COALESCE keeps whichever timestamp is already there, and the
// returned time is that started-at moment, not necessarily 'at'.
//
// pgx.ErrNoRows means slskd has never been configured, which nothing here
// should be recording a refusal against.
func (q *Queries) MarkSourceLoggedOut(ctx context.Context, at time.Time) (time.Time, error) {
	var since time.Time
	err := q.db.QueryRow(ctx, `
		UPDATE slskd_settings SET
			logged_out_since = COALESCE(logged_out_since, $1)
		WHERE singleton
		RETURNING logged_out_since
	`, at).Scan(&since)
	return since, err
}

// ClearSourceLoggedOut stops the clock MarkSourceLoggedOut started, because a
// search has gone through and that only happens logged in. A no-op, and no
// write at all, once the clock is already stopped.
//
// The bool it reports is whether this call is the one that stopped the clock —
// true only the first time a search succeeds after an outage, never on every
// ordinary success afterwards. That is what the caller reads to know the
// difference between "logged in the whole time" and "just came back".
func (q *Queries) ClearSourceLoggedOut(ctx context.Context) (bool, error) {
	tag, err := q.db.Exec(ctx, `
		UPDATE slskd_settings SET logged_out_since = NULL
		WHERE singleton AND logged_out_since IS NOT NULL
	`)
	if err != nil {
		return false, err
	}
	return tag.RowsAffected() > 0, nil
}

func scanSlskdSettings(row pgx.Row) (SlskdSettingsRow, error) {
	var result SlskdSettingsRow
	err := row.Scan(
		&result.BaseURL, &result.APIKey, &result.Enabled, &result.SearchTimeoutSeconds,
		&result.ConnectionStatus, &result.ConnectionError, &result.ConnectionDetail,
		&result.LastCheckedAt, &result.LoggedOutSince, &result.UpdatedAt,
	)
	return result, err
}

// NavidromeSettingsRow carries the stored player connection. Password never
// leaves the process boundary; handlers report only whether one is stored.
type NavidromeSettingsRow struct {
	BaseURL          string
	Username         string
	Password         string
	Enabled          bool
	ConnectionStatus string
	ConnectionError  pgtype.Text
	ConnectionDetail pgtype.Text
	LastCheckedAt    pgtype.Timestamptz
	LastNotifiedAt   pgtype.Timestamptz
	UpdatedAt        pgtype.Timestamptz
}

type SaveNavidromeSettingsParams struct {
	BaseURL  string
	Username string
	Password string
	Enabled  bool
}

const navidromeSettingsColumns = `
	base_url, username, password, enabled,
	connection_status, connection_error, connection_detail,
	last_checked_at, last_notified_at, updated_at
`

// NavidromeSettings returns the stored settings, or pgx.ErrNoRows when no
// player has been configured.
func (q *Queries) NavidromeSettings(ctx context.Context) (NavidromeSettingsRow, error) {
	return scanNavidromeSettings(q.db.QueryRow(ctx, `
		SELECT`+navidromeSettingsColumns+`
		FROM navidrome_settings
		WHERE singleton
	`))
}

// unchangedPlayer is true when neither the address nor the account moved, which
// is the only case where an earlier connection result still says anything.
const unchangedPlayer = `
	navidrome_settings.base_url IS NOT DISTINCT FROM excluded.base_url
	AND navidrome_settings.username IS NOT DISTINCT FROM excluded.username
	AND navidrome_settings.password IS NOT DISTINCT FROM excluded.password
`

// SaveNavidromeSettings stores the player connection. An empty password keeps
// the stored one, so the UI can change the address without echoing the secret
// back, exactly as SaveSlskdSettings treats the API key.
func (q *Queries) SaveNavidromeSettings(
	ctx context.Context, params SaveNavidromeSettingsParams,
) (NavidromeSettingsRow, error) {
	return scanNavidromeSettings(q.db.QueryRow(ctx, `
		INSERT INTO navidrome_settings (base_url, username, password, enabled)
		VALUES (
			$1, $2,
			coalesce(
				nullif($3::text, ''),
				(SELECT password FROM navidrome_settings WHERE singleton),
				''
			),
			$4
		)
		ON CONFLICT (singleton) DO UPDATE SET
			base_url = excluded.base_url,
			username = excluded.username,
			password = excluded.password,
			enabled = excluded.enabled,
			connection_status = CASE WHEN`+unchangedPlayer+`
				THEN navidrome_settings.connection_status ELSE 'unknown' END,
			connection_error = CASE WHEN`+unchangedPlayer+`
				THEN navidrome_settings.connection_error ELSE NULL END,
			connection_detail = CASE WHEN`+unchangedPlayer+`
				THEN navidrome_settings.connection_detail ELSE NULL END,
			last_checked_at = CASE WHEN`+unchangedPlayer+`
				THEN navidrome_settings.last_checked_at ELSE NULL END,
			updated_at = now()
		RETURNING`+navidromeSettingsColumns,
		params.BaseURL, params.Username, params.Password, params.Enabled,
	))
}

// RecordNavidromeConnection stores the outcome of a bounded connection check.
func (q *Queries) RecordNavidromeConnection(
	ctx context.Context, status, detail, failure string,
) (NavidromeSettingsRow, error) {
	return scanNavidromeSettings(q.db.QueryRow(ctx, `
		UPDATE navidrome_settings SET
			connection_status = $1,
			connection_detail = nullif($2, ''),
			connection_error = nullif($3, ''),
			last_checked_at = now(),
			updated_at = now()
		WHERE singleton
		RETURNING`+navidromeSettingsColumns,
		status, detail, failure,
	))
}

// RecordNavidromeNotice remembers that the player accepted a request to look at
// the library again. Only a request that was accepted is written down: telling
// it is best-effort, and a failed attempt leaves the last real one standing
// rather than overwriting it with a moment nothing happened in.
func (q *Queries) RecordNavidromeNotice(ctx context.Context) error {
	_, err := q.db.Exec(ctx, `
		UPDATE navidrome_settings SET last_notified_at = now(), updated_at = now()
		WHERE singleton
	`)
	return err
}

// QueueNotifyPlayer asks for the player to be told the library changed, in
// thirty seconds rather than now. The delay is the coalescing: a burst of
// imports each asking for this queues one row between them, because the
// partial unique index from 00073 makes a second insert while the first is
// still queued or running do nothing rather than collide. That is also why
// this reports no error either way — there is either a fresh row or an
// existing one already waiting, and a caller that changed the library has
// nothing more to do about either.
func (q *Queries) QueueNotifyPlayer(ctx context.Context) error {
	_, err := q.db.Exec(ctx, `
		INSERT INTO jobs (kind, payload, max_attempts, run_after)
		VALUES ('notify_player', '{}'::jsonb, 1, now() + interval '30 seconds')
		ON CONFLICT (kind)
		    WHERE kind = 'notify_player' AND status IN ('queued', 'running')
		DO NOTHING
	`)
	return err
}

func scanNavidromeSettings(row pgx.Row) (NavidromeSettingsRow, error) {
	var result NavidromeSettingsRow
	err := row.Scan(
		&result.BaseURL, &result.Username, &result.Password, &result.Enabled,
		&result.ConnectionStatus, &result.ConnectionError, &result.ConnectionDetail,
		&result.LastCheckedAt, &result.LastNotifiedAt, &result.UpdatedAt,
	)
	return result, err
}

// ListenBrainzSettingsRow carries the account the recommendation source is read
// under. UserToken never leaves the process boundary; handlers report only
// whether one is stored.
type ListenBrainzSettingsRow struct {
	BaseURL             string
	LabsURL             string
	Username            string
	UserToken           pgtype.Text
	SimilarityAlgorithm string
	Enabled             bool
	ConnectionStatus    string
	ConnectionError     pgtype.Text
	ConnectionDetail    pgtype.Text
	LastCheckedAt       pgtype.Timestamptz
	UpdatedAt           pgtype.Timestamptz
}

// SaveListenBrainzSettingsParams carries the account. An empty UserToken keeps
// the stored one, as the slskd key and the Navidrome password are kept.
//
// ClearUserToken is the way back out, and it is separate because the two things
// an empty field could mean are opposites: the interface is never shown the
// token, so it cannot send one back to keep it, which leaves no spelling of
// "remove the one you have" that is not also the spelling of "leave it alone".
// The token is optional by design, so a stored one that nothing can remove
// would be a secret kept forever by accident.
//
// There is deliberately no address here. base_url and labs_url are the hosted
// service, they are what the column defaults say, and a save cannot move them:
// a caller that could choose the address could point it at a host of its own
// while omitting the token, have the save keep the stored one, and read the
// credential off the connection check's Authorization header. Leaving the
// fields off this struct is what makes that unspellable rather than merely
// unlikely — an operator who genuinely needs a different host edits the row.
type SaveListenBrainzSettingsParams struct {
	Username            string
	UserToken           string
	ClearUserToken      bool
	SimilarityAlgorithm string
	Enabled             bool
}

const listenBrainzSettingsColumns = `
	base_url, labs_url, username, user_token, similarity_algorithm, enabled,
	connection_status, connection_error, connection_detail,
	last_checked_at, updated_at
`

// ListenBrainzSettings returns the stored settings, or pgx.ErrNoRows when no
// account has been configured.
func (q *Queries) ListenBrainzSettings(ctx context.Context) (ListenBrainzSettingsRow, error) {
	return scanListenBrainzSettings(q.db.QueryRow(ctx, `
		SELECT`+listenBrainzSettingsColumns+`
		FROM listenbrainz_settings
		WHERE singleton
	`))
}

// unchangedAccount is true when neither the account nor the credential moved,
// which is the only case where an earlier connection result still says
// anything. The addresses are not in it because this statement cannot move
// them, and the similarity algorithm is not in it because the labs host is not
// what a connection check asks.
const unchangedAccount = `
	listenbrainz_settings.username IS NOT DISTINCT FROM excluded.username
	AND listenbrainz_settings.user_token IS NOT DISTINCT FROM excluded.user_token
`

// SaveListenBrainzSettings stores the account. The token is resolved in the
// inserted row rather than in the conflict branch, because PostgreSQL checks
// table constraints against the proposed tuple before it detects the conflict.
//
// base_url and labs_url are absent from both halves on purpose: a first save
// takes the column defaults, and a later one leaves whatever is stored alone.
// Nothing reachable from the API can move the address the token is sent to.
func (q *Queries) SaveListenBrainzSettings(
	ctx context.Context, params SaveListenBrainzSettingsParams,
) (ListenBrainzSettingsRow, error) {
	return scanListenBrainzSettings(q.db.QueryRow(ctx, `
		INSERT INTO listenbrainz_settings (
			username, user_token, similarity_algorithm, enabled
		)
		VALUES (
			$1,
			CASE WHEN $3::boolean THEN NULL ELSE coalesce(
				nullif($2::text, ''),
				(SELECT user_token FROM listenbrainz_settings WHERE singleton)
			) END,
			$4, $5
		)
		ON CONFLICT (singleton) DO UPDATE SET
			username = excluded.username,
			user_token = excluded.user_token,
			similarity_algorithm = excluded.similarity_algorithm,
			enabled = excluded.enabled,
			connection_status = CASE WHEN`+unchangedAccount+`
				THEN listenbrainz_settings.connection_status ELSE 'unknown' END,
			connection_error = CASE WHEN`+unchangedAccount+`
				THEN listenbrainz_settings.connection_error ELSE NULL END,
			connection_detail = CASE WHEN`+unchangedAccount+`
				THEN listenbrainz_settings.connection_detail ELSE NULL END,
			last_checked_at = CASE WHEN`+unchangedAccount+`
				THEN listenbrainz_settings.last_checked_at ELSE NULL END,
			updated_at = now()
		RETURNING`+listenBrainzSettingsColumns,
		params.Username, params.UserToken, params.ClearUserToken,
		params.SimilarityAlgorithm, params.Enabled,
	))
}

// RecordListenBrainzConnectionParams carries a verdict and the row it is a
// verdict about.
//
// ExpectedUpdatedAt is what the settings row said when the check started. Every
// save stamps updated_at, so a check that began against one account and
// finished after a save switched to another finds it moved — which is the whole
// point: a check is a statement about the account it asked, and writing it onto
// whatever the singleton happens to hold now would show account A's success
// under account B's name. ExpectedUsername is not redundant with it, it is what
// makes the intent legible at the call site.
type RecordListenBrainzConnectionParams struct {
	ExpectedUsername  string
	ExpectedUpdatedAt pgtype.Timestamptz
	Status            string
	Detail            string
	Failure           string
}

// RecordListenBrainzConnection stores the outcome of a bounded connection
// check, or returns pgx.ErrNoRows when the row moved out from under it.
func (q *Queries) RecordListenBrainzConnection(
	ctx context.Context, params RecordListenBrainzConnectionParams,
) (ListenBrainzSettingsRow, error) {
	return scanListenBrainzSettings(q.db.QueryRow(ctx, `
		UPDATE listenbrainz_settings SET
			connection_status = $1,
			connection_detail = nullif($2, ''),
			connection_error = nullif($3, ''),
			last_checked_at = now(),
			updated_at = now()
		WHERE singleton
			AND username IS NOT DISTINCT FROM $4
			AND updated_at IS NOT DISTINCT FROM $5
		RETURNING`+listenBrainzSettingsColumns,
		params.Status, params.Detail, params.Failure,
		params.ExpectedUsername, params.ExpectedUpdatedAt,
	))
}

func scanListenBrainzSettings(row pgx.Row) (ListenBrainzSettingsRow, error) {
	var result ListenBrainzSettingsRow
	err := row.Scan(
		&result.BaseURL, &result.LabsURL, &result.Username, &result.UserToken,
		&result.SimilarityAlgorithm, &result.Enabled,
		&result.ConnectionStatus, &result.ConnectionError, &result.ConnectionDetail,
		&result.LastCheckedAt, &result.UpdatedAt,
	)
	return result, err
}

// SourceRetentionKeep leaves the provider's copy of an imported release alone.
// It is the default, and it is what an installation that never opens Settings
// keeps doing: an import copies music into the managed library and takes
// nothing away.
//
// SourceRetentionDelete removes the provider's copy of the files Schall
// imported, and only those, once the managed copy and its provenance are
// durable. It is destructive and therefore never assumed.
const (
	SourceRetentionKeep   = "keep"
	SourceRetentionDelete = "delete"
)

type ImportSettingsRow struct {
	SourceRetention string
	// AcoustIDAPIKey is the application key AcoustID issues per installation.
	// It is stored because only the operator can obtain one, and it is never
	// returned to the interface.
	AcoustIDAPIKey  string
	AcoustIDEnabled bool
	// The four that say whether music is written into the library smaller than
	// it arrived, and how. Off by default; see migration 00060.
	TranscodeEnabled bool
	TranscodeTarget  string
	TranscodeBitrate string
	TranscodeWhen    string
	UpdatedAt        pgtype.Timestamptz
}

// SaveImportSettingsParams carries the import policy. An empty AcoustIDAPIKey
// means "leave the stored key alone", so saving any other setting cannot erase
// a key the interface was never shown. TranscodeEnabled is a pointer for the
// same reason: a bare bool cannot tell "turn re-encoding off" apart from "I
// have no opinion about re-encoding", and a caller that only means to change
// the source retention must not silently turn a stored policy off. nil means
// the second of those — keep whatever is already stored.
type SaveImportSettingsParams struct {
	SourceRetention  string
	AcoustIDAPIKey   string
	AcoustIDEnabled  bool
	TranscodeEnabled *bool
	TranscodeTarget  string
	TranscodeBitrate string
	TranscodeWhen    string
}

// ImportSettings returns the stored import policy. An installation that has
// never set one is not an error: it is the default, which is why this returns
// the non-destructive policy rather than pgx.ErrNoRows. The importer reads
// this on a path where a failure must not undo a completed import, so the
// answer is never ambiguous.
func (q *Queries) ImportSettings(ctx context.Context) (ImportSettingsRow, error) {
	result := DefaultImportSettings()
	err := q.db.QueryRow(ctx, `
		SELECT`+importSettingsColumns+`
		FROM import_settings WHERE singleton
	`).Scan(scanImportSettings(&result)...)
	if errors.Is(err, pgx.ErrNoRows) {
		return DefaultImportSettings(), nil
	}
	if err != nil {
		return ImportSettingsRow{}, err
	}
	return result, nil
}

// SaveImportSettings stores the import policy. The table constraint is the
// authority on which policies exist, so an unknown one is rejected by the
// database rather than silently written.
func (q *Queries) SaveImportSettings(
	ctx context.Context, params SaveImportSettingsParams,
) (ImportSettingsRow, error) {
	var result ImportSettingsRow
	err := q.db.QueryRow(ctx, `
		INSERT INTO import_settings (
			source_retention, acoustid_api_key, acoustid_enabled,
			transcode_enabled, transcode_target, transcode_bitrate, transcode_when
		)
		VALUES (
			$1,
			-- An empty key keeps the stored one: the interface is never shown
			-- the key, so it cannot send it back to keep it. Resolved here
			-- rather than in the conflict branch, because PostgreSQL checks the
			-- table constraints against the proposed tuple before it detects
			-- the conflict — and enabling AcoustID with a stored key would
			-- otherwise fail the check that a key must be present.
			coalesce(
				nullif($2::text, ''),
				(SELECT acoustid_api_key FROM import_settings WHERE singleton),
				''
			),
			$3,
			-- Same reasoning as the key above, and for the same reason: a
			-- caller that only means to change the source retention -- this
			-- test among them -- sends no opinion about re-encoding at all.
			-- A null $4 or an empty string keeps whatever is already stored,
			-- falling back to the column's own default when nothing is stored
			-- yet, rather than fail the table's check or silently reset a
			-- policy nobody asked to change.
			coalesce(
				$4::boolean,
				(SELECT transcode_enabled FROM import_settings WHERE singleton),
				false
			),
			coalesce(
				nullif($5::text, ''),
				(SELECT transcode_target FROM import_settings WHERE singleton),
				'mp3'
			),
			coalesce(
				nullif($6::text, ''),
				(SELECT transcode_bitrate FROM import_settings WHERE singleton),
				'320'
			),
			coalesce(
				nullif($7::text, ''),
				(SELECT transcode_when FROM import_settings WHERE singleton),
				'lossless'
			)
		)
		ON CONFLICT (singleton) DO UPDATE SET
			source_retention = excluded.source_retention,
			acoustid_api_key = excluded.acoustid_api_key,
			acoustid_enabled = excluded.acoustid_enabled,
			transcode_enabled = excluded.transcode_enabled,
			transcode_target = excluded.transcode_target,
			transcode_bitrate = excluded.transcode_bitrate,
			transcode_when = excluded.transcode_when,
			updated_at = now()
		RETURNING`+importSettingsColumns,
		params.SourceRetention, strings.TrimSpace(params.AcoustIDAPIKey), params.AcoustIDEnabled,
		params.TranscodeEnabled, params.TranscodeTarget,
		params.TranscodeBitrate, params.TranscodeWhen,
	).Scan(scanImportSettings(&result)...)
	return result, err
}

const importSettingsColumns = `
	source_retention, acoustid_api_key, acoustid_enabled,
	transcode_enabled, transcode_target, transcode_bitrate, transcode_when,
	updated_at
`

func scanImportSettings(row *ImportSettingsRow) []any {
	return []any{
		&row.SourceRetention, &row.AcoustIDAPIKey, &row.AcoustIDEnabled,
		&row.TranscodeEnabled, &row.TranscodeTarget, &row.TranscodeBitrate,
		&row.TranscodeWhen, &row.UpdatedAt,
	}
}

// DefaultImportSettings is what an installation that has never been asked
// answers: the provider's copy is kept, and music is filed exactly as it
// arrived. The table carries the same defaults, and both are the answer that
// changes nothing.
func DefaultImportSettings() ImportSettingsRow {
	return ImportSettingsRow{
		SourceRetention:  SourceRetentionKeep,
		TranscodeTarget:  "mp3",
		TranscodeBitrate: "320",
		TranscodeWhen:    "lossless",
	}
}

// RecordTranscode notes that one file was written into the library smaller than
// it arrived.
//
// It writes in two places on purpose. The import's history is what a person
// reads to see what became of their music; import_transcodes is the note the
// scan has not read yet, because the library row for this path does not exist
// until the scan that follows creates it.
func (q *Queries) RecordTranscode(
	ctx context.Context, requestID uuid.UUID, params RecordTranscodeParams,
) error {
	return q.inTransaction(ctx, "recording a re-encoded copy", func(tx pgx.Tx) error {
		if err := recordTranscodedFile(ctx, tx, params); err != nil {
			return err
		}
		_, err := tx.Exec(ctx, `
			INSERT INTO download_import_reviews (download_request_id, kind, detail)
			VALUES ($1, 'transcoded', $2)
		`, requestID, params.Detail)
		return err
	})
}

// RecordTranscodedFile notes one re-encoded copy and nothing else. An upload
// has no import history to write beside it — nobody asked a provider for it and
// no verdict admitted it — so the note the scan reads is the whole record.
func (q *Queries) RecordTranscodedFile(ctx context.Context, params RecordTranscodeParams) error {
	return recordTranscodedFile(ctx, q.db, params)
}

func recordTranscodedFile(ctx context.Context, run DBTX, params RecordTranscodeParams) error {
	_, err := run.Exec(ctx, `
		INSERT INTO import_transcodes (
			local_path, source_format, source_size_bytes, target_format
		) VALUES ($1, $2, $3, $4)
		ON CONFLICT (local_path) DO UPDATE SET
			source_format = excluded.source_format,
			source_size_bytes = excluded.source_size_bytes,
			target_format = excluded.target_format,
			created_at = now()
	`, params.LocalPath, params.SourceFormat, params.SourceSizeBytes, params.TargetFormat)
	return err
}

type RecordTranscodeParams struct {
	LocalPath       string
	SourceFormat    string
	SourceSizeBytes int64
	TargetFormat    string
	Detail          string
}

// RecordTranscodeRefused notes a re-encode the policy asked for that did not
// happen. A setting that appears to do nothing is then readable as the reason
// it did nothing, rather than as a setting that was ignored.
func (q *Queries) RecordTranscodeRefused(
	ctx context.Context, requestID uuid.UUID, detail string,
) error {
	_, err := q.db.Exec(ctx, `
		INSERT INTO download_import_reviews (download_request_id, kind, detail)
		VALUES ($1, 'transcode_failed', $2)
	`, requestID, detail)
	return err
}

// The two kinds of thing Schall can be told to send word to.
//
// NotifyNtfy is the push service: its server takes a plain text body and reads
// a Title and a Priority from headers, and its app puts the message on a phone.
// NotifyWebhook is anything else — a JSON object posted to an address, for
// somebody who has their own way of being told.
const (
	NotifyNtfy    = "ntfy"
	NotifyWebhook = "webhook"
)

// NotificationSettingsRow is where word is sent, and whether to send any.
type NotificationSettingsRow struct {
	Kind             string
	Endpoint         string
	Token            pgtype.Text
	Enabled          bool
	ConnectionStatus string
	ConnectionError  pgtype.Text
	LastCheckedAt    pgtype.Timestamptz
	UpdatedAt        pgtype.Timestamptz
}

// SaveNotificationSettingsParams carries the address. An empty Token keeps the
// stored one, exactly as the slskd key and the Navidrome password are kept, and
// ClearToken is the way back out — the interface is never shown the token, so
// an empty field cannot mean "remove it" and "leave it alone" at once.
type SaveNotificationSettingsParams struct {
	Kind       string
	Endpoint   string
	Token      string
	ClearToken bool
	Enabled    bool
}

const notificationSettingsColumns = `
	kind, endpoint, token, enabled,
	connection_status, connection_error, last_checked_at, updated_at
`

// NotificationSettings returns where word is sent, or pgx.ErrNoRows when
// nothing has been configured. Nothing configured means nothing is sent, which
// is what every installation starts with.
func (q *Queries) NotificationSettings(ctx context.Context) (NotificationSettingsRow, error) {
	return scanNotificationSettings(q.db.QueryRow(ctx, `
		SELECT`+notificationSettingsColumns+`
		FROM notification_settings
		WHERE singleton
	`))
}

// unchangedNotifier is true when neither the address nor the credential moved,
// which is the only case where an earlier delivery result still says anything
// about where messages are going now.
const unchangedNotifier = `
	notification_settings.endpoint IS NOT DISTINCT FROM excluded.endpoint
	AND notification_settings.token IS NOT DISTINCT FROM excluded.token
`

// SaveNotificationSettings stores the address. The token is resolved in the
// inserted row rather than in the conflict branch, because PostgreSQL checks
// table constraints against the proposed tuple before it detects the conflict.
func (q *Queries) SaveNotificationSettings(
	ctx context.Context, params SaveNotificationSettingsParams,
) (NotificationSettingsRow, error) {
	return scanNotificationSettings(q.db.QueryRow(ctx, `
		INSERT INTO notification_settings (kind, endpoint, token, enabled)
		VALUES (
			$1, $2,
			CASE WHEN $4::boolean THEN NULL ELSE coalesce(
				nullif($3::text, ''),
				(SELECT token FROM notification_settings WHERE singleton)
			) END,
			$5
		)
		ON CONFLICT (singleton) DO UPDATE SET
			kind = excluded.kind,
			endpoint = excluded.endpoint,
			token = excluded.token,
			enabled = excluded.enabled,
			connection_status = CASE WHEN`+unchangedNotifier+`
				THEN notification_settings.connection_status ELSE 'unknown' END,
			connection_error = CASE WHEN`+unchangedNotifier+`
				THEN notification_settings.connection_error ELSE NULL END,
			last_checked_at = CASE WHEN`+unchangedNotifier+`
				THEN notification_settings.last_checked_at ELSE NULL END,
			updated_at = now()
		RETURNING`+notificationSettingsColumns,
		params.Kind, params.Endpoint, params.Token, params.ClearToken, params.Enabled,
	))
}

// RecordNotificationDelivery stores what the last send came to, so the Settings
// page can say whether the address works without somebody having to make a
// question appear in the review queue to find out.
//
// It is written by the test button and by every real send alike: an address that
// worked yesterday and stopped is the case worth seeing, and it would be
// invisible if only the test button wrote here.
func (q *Queries) RecordNotificationDelivery(
	ctx context.Context, status, failure string,
) error {
	_, err := q.db.Exec(ctx, `
		UPDATE notification_settings SET
			connection_status = $1,
			connection_error = nullif($2, ''),
			last_checked_at = now()
		WHERE singleton
	`, status, failure)
	return err
}

func scanNotificationSettings(row pgx.Row) (NotificationSettingsRow, error) {
	var result NotificationSettingsRow
	err := row.Scan(
		&result.Kind, &result.Endpoint, &result.Token, &result.Enabled,
		&result.ConnectionStatus, &result.ConnectionError,
		&result.LastCheckedAt, &result.UpdatedAt,
	)
	return result, err
}

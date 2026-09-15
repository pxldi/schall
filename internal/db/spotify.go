package db

import (
	"context"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
)

// SpotifySettingsRow carries the stored Spotify credentials. ClientSecret and
// RefreshToken never leave the process boundary; handlers report only whether
// they are configured.
type SpotifySettingsRow struct {
	ClientID         string
	ClientSecret     string
	RefreshToken     pgtype.Text
	AccountName      pgtype.Text
	ConnectionStatus string
	ConnectionError  pgtype.Text
	LastCheckedAt    pgtype.Timestamptz
	UpdatedAt        pgtype.Timestamptz
}

const spotifySettingsColumns = `
	client_id, client_secret, refresh_token, account_name,
	connection_status, connection_error, last_checked_at, updated_at
`

// SpotifySettings returns the stored settings, or pgx.ErrNoRows when Spotify
// has never been configured.
func (q *Queries) SpotifySettings(ctx context.Context) (SpotifySettingsRow, error) {
	return scanSpotifySettings(q.db.QueryRow(ctx, `
		SELECT`+spotifySettingsColumns+`
		FROM spotify_settings
		WHERE singleton
	`))
}

// unchangedSpotifyClient is true when the application credentials did not
// move. A refresh token is minted for one client pair: change the pair and
// the stored authorization is dead weight that must be re-earned, so it is
// dropped rather than kept to fail later.
const unchangedSpotifyClient = `
	spotify_settings.client_id IS NOT DISTINCT FROM excluded.client_id
	AND spotify_settings.client_secret IS NOT DISTINCT FROM excluded.client_secret
`

// SaveSpotifySettings stores the application credentials. An empty secret
// keeps the stored secret so the UI can update other fields without echoing
// it back, exactly as SaveSlskdSettings treats the API key.
func (q *Queries) SaveSpotifySettings(ctx context.Context, clientID, clientSecret string) (SpotifySettingsRow, error) {
	return scanSpotifySettings(q.db.QueryRow(ctx, `
		INSERT INTO spotify_settings (client_id, client_secret)
		VALUES (
			$1,
			coalesce(
				nullif($2::text, ''),
				(SELECT client_secret FROM spotify_settings WHERE singleton),
				''
			)
		)
		ON CONFLICT (singleton) DO UPDATE SET
			client_id = excluded.client_id,
			client_secret = excluded.client_secret,
			refresh_token = CASE WHEN`+unchangedSpotifyClient+`
				THEN spotify_settings.refresh_token ELSE NULL END,
			account_name = CASE WHEN`+unchangedSpotifyClient+`
				THEN spotify_settings.account_name ELSE NULL END,
			connection_status = CASE WHEN`+unchangedSpotifyClient+`
				THEN spotify_settings.connection_status ELSE 'unknown' END,
			connection_error = CASE WHEN`+unchangedSpotifyClient+`
				THEN spotify_settings.connection_error ELSE NULL END,
			last_checked_at = CASE WHEN`+unchangedSpotifyClient+`
				THEN spotify_settings.last_checked_at ELSE NULL END,
			updated_at = now()
		RETURNING`+spotifySettingsColumns,
		clientID, clientSecret,
	))
}

// SaveSpotifyAuthorization stores what the OAuth round-trip earned. An empty
// account name keeps the stored one, because a token refresh learns a new
// token without re-learning whose it is.
func (q *Queries) SaveSpotifyAuthorization(ctx context.Context, refreshToken, accountName string) (SpotifySettingsRow, error) {
	return scanSpotifySettings(q.db.QueryRow(ctx, `
		UPDATE spotify_settings SET
			refresh_token = $1,
			account_name = coalesce(nullif($2, ''), account_name),
			connection_status = 'ok',
			connection_error = NULL,
			last_checked_at = now(),
			updated_at = now()
		WHERE singleton
		RETURNING`+spotifySettingsColumns,
		refreshToken, accountName,
	))
}

// RecordSpotifyConnection stores the outcome of talking to Spotify, so the
// settings page can say when the authorization last worked and why it stopped.
func (q *Queries) RecordSpotifyConnection(ctx context.Context, status, failure string) (SpotifySettingsRow, error) {
	return scanSpotifySettings(q.db.QueryRow(ctx, `
		UPDATE spotify_settings SET
			connection_status = $1,
			connection_error = nullif($2, ''),
			last_checked_at = now(),
			updated_at = now()
		WHERE singleton
		RETURNING`+spotifySettingsColumns,
		status, failure,
	))
}

func scanSpotifySettings(row pgx.Row) (SpotifySettingsRow, error) {
	var result SpotifySettingsRow
	err := row.Scan(
		&result.ClientID, &result.ClientSecret, &result.RefreshToken, &result.AccountName,
		&result.ConnectionStatus, &result.ConnectionError, &result.LastCheckedAt, &result.UpdatedAt,
	)
	return result, err
}

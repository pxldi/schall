package navidrome

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/pxldi/schall/internal/db"
	"github.com/rs/zerolog"
)

// noticeTimeout bounds one telling. The player answers as soon as it has begun
// looking rather than when it has finished, so this is generous for the request
// it makes and short enough that a wedged player cannot hold up the job that
// noticed the library had changed.
const noticeTimeout = 10 * time.Second

// Store is where the player's connection lives and where the fact that it was
// told is written down.
type Store interface {
	NavidromeSettings(context.Context) (db.NavidromeSettingsRow, error)
	RecordNavidromeNotice(context.Context) error
}

// Notifier tells the player that the library changed.
//
// It builds a client per telling rather than holding one, because the settings
// are the operator's to change while Schall is running and a client made once
// at startup would keep speaking to the address that was there then.
type Notifier struct {
	store  Store
	logger zerolog.Logger
	// newClient exists so a test can answer for the player without a network.
	newClient func(Options) (rescanner, error)
}

// rescanner is the part of a client a telling uses.
type rescanner interface {
	Rescan(context.Context) error
}

func NewNotifier(store Store, logger zerolog.Logger) *Notifier {
	return &Notifier{
		store:  store,
		logger: logger,
		newClient: func(options Options) (rescanner, error) {
			return NewClient(options)
		},
	}
}

// NoticeLibraryChange asks the configured player to look at the library again.
//
// An installation with no player, or one whose player has been switched off, is
// not a failure: there is nothing to tell and nothing wrong, so this reports
// success having done nothing. Everything else is reported, because a caller
// that wanted the player told deserves to hear that it was not — but nothing
// downstream depends on it, since the music is in the library either way and
// the player finds it on its own schedule regardless.
func (notifier *Notifier) NoticeLibraryChange(ctx context.Context) error {
	row, err := notifier.store.NavidromeSettings(ctx)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("read navidrome settings: %w", err)
	}
	if !row.Enabled {
		return nil
	}

	client, err := notifier.newClient(Options{
		BaseURL:  row.BaseURL,
		Username: row.Username,
		Password: row.Password,
	})
	if err != nil {
		return err
	}

	bounded, cancel := context.WithTimeout(ctx, noticeTimeout)
	defer cancel()
	if err := client.Rescan(bounded); err != nil {
		return err
	}

	// Written down with the caller's context rather than the bounded one, so a
	// telling that only just fit inside the budget is still recorded.
	if err := notifier.store.RecordNavidromeNotice(ctx); err != nil {
		return fmt.Errorf("record navidrome notice: %w", err)
	}
	notifier.logger.Info().Str("base_url", row.BaseURL).Msg("told the player the library changed")
	return nil
}

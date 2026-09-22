package acquisition

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/pxldi/schall/internal/anchor"
	"github.com/pxldi/schall/internal/chromaprint"
	"github.com/pxldi/schall/internal/db"
	"github.com/pxldi/schall/internal/tracksource"
)

// TrackSources reads the address of one track and takes an excerpt of its
// audio (ADR 0038 §2). internal/tracksource is the one implementation.
type TrackSources interface {
	Lookup(ctx context.Context, address string) (tracksource.Track, error)
	Excerpt(ctx context.Context, track tracksource.Track) ([]byte, error)
}

// SourceNamer writes the names a person confirmed into a file admitted for a
// keyed want, with its artwork and its uploader (ADR 0038 §5).
type SourceNamer interface {
	NameKeyedFile(ctx context.Context, targetID uuid.UUID) error
}

var (
	// ErrNoTrackSources reports an installation that cannot read addresses.
	ErrNoTrackSources = errors.New("this installation cannot read track addresses")
	// ErrAddressChanged reports an address that names another track than the
	// one the person was shown.
	ErrAddressChanged = errors.New("that address now names a different track")
	// ErrExcerptUnreadable reports an excerpt that could not be fingerprinted.
	// Without it the want has no anchor, and keying it would leave it with
	// nothing that can prove a copy.
	ErrExcerptUnreadable = errors.New("the audio at that address could not be read")
)

// keyedSummary is what a keyed want says while it is searched, and
// keyedAcquiredSummary what it says when the library already holds the track.
const (
	keyedSummary         = "Keyed to an address. Looking for a copy that matches its audio."
	keyedAcquiredSummary = "Keyed to an address. The library already holds this track."
)

// WithTrackSources registers what reads an address, and the fingerprinter its
// excerpt is measured with. Without them no want can be keyed.
func (service *Service) WithTrackSources(
	sources TrackSources, prints anchor.Fingerprinter,
) *Service {
	service.tracks = sources
	service.prints = prints
	return service
}

// WithSourceNamer registers what names a file admitted for a keyed want.
// Without it the file keeps the tags it arrived with.
func (service *Service) WithSourceNamer(namer SourceNamer) *Service {
	service.namer = namer
	return service
}

// LookUpSource says what an address holds, and changes nothing. The person
// sees it beside the entry before they confirm (ADR 0038 §8).
func (service *Service) LookUpSource(ctx context.Context, address string) (tracksource.Track, error) {
	if service.tracks == nil {
		return tracksource.Track{}, ErrNoTrackSources
	}
	return service.tracks.Lookup(ctx, address)
}

// KeyToSource keys a want to the track at an address (ADR 0038 §1, §3).
//
// The address is read again, and the excerpt fetched and fingerprinted, before
// anything is written. Key and anchor go in together, so a keyed want always
// has its excerpt to prove a copy with. confirmedID is the external id the
// person was shown; when it is given and the address now names another track,
// nothing is written.
//
// pgx.ErrNoRows means there is no such want. db.ErrNotKeyable and
// db.ErrAlreadyKeyed are the want refusing a key.
func (service *Service) KeyToSource(
	ctx context.Context, id uuid.UUID, address, confirmedID string,
) (db.AcquisitionTargetRow, error) {
	if service.tracks == nil || service.prints == nil {
		return db.AcquisitionTargetRow{}, ErrNoTrackSources
	}
	// Asked before the network, so a want that cannot take a key is refused
	// without fetching audio for it. The transaction asks again.
	target, err := service.store.AcquisitionTarget(ctx, id)
	if err != nil {
		return db.AcquisitionTargetRow{}, err
	}
	if err := keyable(target); err != nil {
		return db.AcquisitionTargetRow{}, err
	}
	if !service.prints.Available() {
		return db.AcquisitionTargetRow{}, fmt.Errorf("%w: no excerpt can be fingerprinted",
			chromaprint.ErrNoFpcalc)
	}

	track, err := service.tracks.Lookup(ctx, address)
	if err != nil {
		return db.AcquisitionTargetRow{}, err
	}
	if confirmedID = strings.TrimSpace(confirmedID); confirmedID != "" && confirmedID != track.ExternalID {
		return db.AcquisitionTargetRow{}, ErrAddressChanged
	}
	audio, err := service.tracks.Excerpt(ctx, track)
	if err != nil {
		return db.AcquisitionTargetRow{}, err
	}
	fingerprint, seconds, err := anchor.Measure(ctx, service.prints, audio)
	if errors.Is(err, chromaprint.ErrNoFingerprint) || errors.Is(err, chromaprint.ErrNotAFingerprint) {
		return db.AcquisitionTargetRow{}, fmt.Errorf("%w: %w", ErrExcerptUnreadable, err)
	}
	if err != nil {
		return db.AcquisitionTargetRow{}, fmt.Errorf("fingerprint the excerpt of %s: %w", track.URL, err)
	}

	keyed, err := service.store.KeyAcquisitionTargetToSource(ctx, db.KeySourceParams{
		TargetID: id, Source: track.Source, ExternalID: track.ExternalID, ExternalURL: track.URL,
		Lookup: db.SourceLookup{
			Title: track.Title, Artist: track.Artist, Uploader: track.Uploader,
			AccountID: track.AccountID, DurationMS: track.DurationMS, ArtworkURL: track.ArtworkURL,
		},
		Fingerprint: fingerprint, Seconds: seconds,
		Summary: keyedSummary, AcquiredSummary: keyedAcquiredSummary,
	})
	if err != nil {
		return db.AcquisitionTargetRow{}, err
	}
	service.logger.Info().
		Str("acquisition_target_id", id.String()).
		Str("external_id", track.ExternalID).
		Str("status", keyed.Status).
		Int("excerpt_seconds", seconds).
		Msg("keyed a want to an address")
	service.announce()
	return keyed, nil
}

// keyable refuses a want that cannot take a key, in the sentences the
// transaction uses.
func keyable(target db.AcquisitionTargetRow) error {
	switch {
	case target.Source != "":
		return db.ErrAlreadyKeyed
	case target.Status == "unresolved":
		return nil
	case target.Status == "awaiting_review" && target.ReviewReason.String == "resolution":
		return nil
	}
	return db.ErrNotKeyable
}

// SetMinimumBitrate changes a keyed want's floor (ADR 0038 §7). Held copies
// are judged again, because a lower floor can admit one.
func (service *Service) SetMinimumBitrate(
	ctx context.Context, id uuid.UUID, kbps int,
) (db.AcquisitionTargetRow, error) {
	target, err := service.store.SetAcquisitionTargetMinimumBitrate(ctx, id, kbps)
	if err != nil {
		return db.AcquisitionTargetRow{}, err
	}
	service.announce()
	return target, nil
}

// nameKeyedFile names the file a keyed want was just acquired with. A failure
// is logged: the want has its music, and the names are what 0026 §4 writes
// when it can.
func (service *Service) nameKeyedFile(ctx context.Context, target db.AcquisitionTargetRow) {
	if service.namer == nil || target.Source == "" {
		return
	}
	if err := service.namer.NameKeyedFile(ctx, target.ID); err != nil && !errors.Is(err, pgx.ErrNoRows) {
		service.logger.Warn().Err(err).
			Str("acquisition_target_id", target.ID.String()).
			Msg("name the file a keyed want was acquired with")
	}
}

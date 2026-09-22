// Package anchor gives a want a piece of audio it can be proven against.
//
// A want is one piece of music somebody asked for that is not in the library
// yet. Schall finds a copy of it on the Soulseek network, where the copy comes
// from a stranger and carries no provenance at all, so before it is let in
// something has to prove the audio is that recording.
//
// Until now only AcoustID could do that. AcoustID turns audio into a fingerprint
// and answers with the MusicBrainz recordings that fingerprint belongs to, so
// music MusicBrainz has never heard of can be neither admitted nor refused, and
// every copy of it becomes a question for a person. On 2026-08-16 that was 129
// copies behind 44 questions, with AcoustID naming a recording for none of them.
//
// An anchor is a second witness. Deezer publishes a thirty-second excerpt of
// most tracks it carries, free and with no key, and it is fetched by the want's
// own ISRC — the code the recording was registered under, an exact key rather
// than a text search. So the audio that comes back is the audio of exactly the
// recording the want names, and a copy can be compared against it without
// MusicBrainz holding anything.
//
// That key leaves out the music the review queue is actually full of: leaks,
// sessions and unreleased tracks, which carry no ISRC and which Deezer does not
// have. For those, an upload found on YouTube by the want's name and length is
// the reference instead. It is chosen rather than scored, by the rule in
// choose.go, and an anchor found that way may prove a copy and may leave one a
// question — it never refuses one. See
// ADR 0024 and
// ADR 0029.
//
// This package fetches that excerpt, once per want, and stores its fingerprint.
// It decides nothing: it neither admits a copy nor refuses one, and a want with
// no anchor is judged exactly as it is today.
package anchor

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/pxldi/schall/internal/chromaprint"
	"github.com/pxldi/schall/internal/db"
	"github.com/pxldi/schall/internal/deezer"
	"github.com/pxldi/schall/internal/identity"
	"github.com/pxldi/schall/internal/tagmatch"
	"github.com/rs/zerolog"
)

// How many wants one pass fetches for. Every one of them is a request to a free
// public service, so a pass is short and there are many of them rather than one
// long one holding a worker.
const sweepBatch = 20

// How long the pass waits between requests. Deezer refuses a caller that asks
// too fast, and a refusal costs the want a whole retry cycle, so the pace is set
// well under whatever the service would object to.
const requestPause = 500 * time.Millisecond

// How long a want waits before its preview is asked for again, by how many times
// it has already been tried. A fetch that could not run says nothing about the
// music, so the wait grows to stop a service in difficulty being asked about
// every want in the collection every few minutes.
var retryLadder = []time.Duration{
	5 * time.Minute, 30 * time.Minute, 2 * time.Hour, 12 * time.Hour, 24 * time.Hour,
}

// The shortest reference worth keeping. A preview is thirty seconds, which is
// about 240 frames; ten seconds is the point below which what came back is a
// scrap of audio rather than an excerpt, and a comparison against a scrap says
// too little to be worth storing as proof.
const minimumFrames = 80

// The sentences a want keeps when there is no anchor. They are two different
// facts and never one: the first is silence, which no rule may read as
// agreement, and the second is a check that could not run and is asked again
// (ADR 0002).
const (
	noPreviewPublished = "Deezer publishes no preview for this recording's ISRC."
	noISRCToAskBy      = "This entry carries no ISRC, and a preview is only ever fetched by ISRC."
	unusableISRC       = "This entry's ISRC is not a registration code a preview can be fetched by."
	unreadablePreview  = "The preview Deezer returned could not be read as audio."
)

// The sentences a want keeps when the upload search had nothing either. Each is
// written after the Deezer sentence above, because the two are separate facts
// and a person reading the row needs both: why the keyed reference is missing,
// and why the searched one is.
const (
	noLengthToKeyBy   = "This entry carries no length, and an upload is only ever chosen by name and length."
	noUploadMatched   = "No upload matched this entry's name and length."
	unreadableExcerpt = "The excerpt taken from the upload could not be read as audio."
)

// Distributor is the part of a music service Schall uses: which track carries a
// registration code, and the excerpt published for it.
type Distributor interface {
	TrackByISRC(ctx context.Context, isrc string) (deezer.Track, error)
	FetchPreview(ctx context.Context, previewURL string) ([]byte, error)
}

// Fingerprinter turns a file of audio into the fingerprint it is compared by.
type Fingerprinter interface {
	Available() bool
	Compute(ctx context.Context, file string, lengthSeconds int) (string, int, error)
}

// Store is what the sweep reads and writes. It is the whole of this package's
// reach into the database.
type Store interface {
	DueAnchors(ctx context.Context, now time.Time, limit int32) ([]db.AnchorDueRow, error)
	RecordAnchor(ctx context.Context, params db.AnchorParams) error
	RecordAnchorAbsent(ctx context.Context, targetID uuid.UUID, reason string) error
	DeferAnchor(ctx context.Context, targetID uuid.UUID, at time.Time, reason string) error
	// QueueWantCopyJudging asks for the want's held copies to be measured against
	// the anchor that just landed, which is the one thing this package causes to
	// happen outside itself (ADR 0029 §5).
	QueueWantCopyJudging(ctx context.Context, targetID uuid.UUID, runAfter time.Time) error
}

// Service fetches previews for the wants that are due one.
type Service struct {
	store       Store
	distributor Distributor
	finder      Finder
	prints      Fingerprinter
	logger      zerolog.Logger
	source      string
	now         func() time.Time
	pause       func(ctx context.Context, d time.Duration)
}

// NewService builds the sweep. finder may be nil, and then a want the
// distributor cannot answer for waits on the retry ladder instead of being
// recorded as having no anchor: nothing is lost before the search is wired up.
func NewService(
	store Store, distributor Distributor, finder Finder,
	prints Fingerprinter, logger zerolog.Logger,
) *Service {
	return &Service{
		store:       store,
		distributor: distributor,
		finder:      finder,
		prints:      prints,
		logger:      logger,
		source:      "deezer",
		now:         time.Now,
		pause:       wait,
	}
}

// Sweep fetches the previews that are due and reports whether there are more
// waiting.
//
// A pass never fails for one want. A recording with no preview is an ordinary
// recording, and only the service refusing to answer at all ends a pass early —
// asking harder is the one thing that makes rate limiting worse.
func (service *Service) Sweep(ctx context.Context) (bool, error) {
	if !service.prints.Available() {
		// Nothing here can be done without the program that computes
		// fingerprints, and fetching audio nothing can read would only burden the
		// distributor. This is a check that could not run, and it is said once for
		// the pass rather than once per want.
		return false, fmt.Errorf("%w: no preview can be fingerprinted", chromaprint.ErrNoFpcalc)
	}

	due, err := service.store.DueAnchors(ctx, service.now(), sweepBatch)
	if err != nil {
		return false, err
	}
	for index, want := range due {
		if index > 0 {
			service.pause(ctx, requestPause)
		}
		if ctx.Err() != nil {
			return false, ctx.Err()
		}
		if err := service.fetch(ctx, want); err != nil {
			if errors.Is(err, deezer.ErrRateLimited) || errors.Is(err, ErrFinderRateLimited) {
				// The pass stops rather than working through the rest. Every one of
				// them would meet the same refusal, and each would spend an attempt
				// on it.
				return false, nil
			}
			return false, err
		}
	}
	return len(due) == sweepBatch, nil
}

// fetch settles one want. The error it returns is a failure to write down what
// happened, or rate limiting, which the caller reads as a reason to stop. What
// the distributor said about the music is never an error here: it is recorded.
func (service *Service) fetch(ctx context.Context, want db.AnchorDueRow) error {
	if want.ISRC == "" {
		return service.fromUploads(ctx, want, noISRCToAskBy)
	}

	track, err := service.distributor.TrackByISRC(ctx, want.ISRC)
	switch {
	case err == nil:
	case errors.Is(err, deezer.ErrRateLimited):
		return service.reschedule(ctx, want, err)
	case errors.Is(err, deezer.ErrNoTrack), errors.Is(err, deezer.ErrNoPreview):
		return service.fromUploads(ctx, want, noPreviewPublished)
	case errors.Is(err, deezer.ErrNotAnISRC):
		// The code is there and is not a code. It is said in its own words rather
		// than as "no ISRC", because the two are fixed by different things: one
		// wants the entry to carry a code, the other wants the code it carries to
		// be corrected.
		return service.fromUploads(ctx, want, unusableISRC)
	default:
		return service.reschedule(ctx, want, err)
	}

	// Everything that can go wrong here is asked again. A preview address is
	// signed and expires, so a refusal at this step is the excerpt going out of
	// reach rather than the recording having none, and the next answer to
	// TrackByISRC carries a fresh address.
	audio, err := service.distributor.FetchPreview(ctx, track.PreviewURL)
	if err != nil {
		return service.reschedule(ctx, want, err)
	}

	value, seconds, err := service.measure(ctx, audio)
	switch {
	case err == nil:
	case errors.Is(err, chromaprint.ErrNoFingerprint), errors.Is(err, chromaprint.ErrNotAFingerprint):
		service.logger.Warn().Err(err).Str("acquisition_target_id", want.ID.String()).
			Msg("a fetched preview could not be fingerprinted")
		return service.store.RecordAnchorAbsent(ctx, want.ID, unreadablePreview)
	default:
		return service.reschedule(ctx, want, err)
	}

	service.logger.Info().
		Str("acquisition_target_id", want.ID.String()).
		Str("isrc", want.ISRC).
		Int("preview_seconds", seconds).
		Msg("anchored a want to its distributor preview")

	return service.record(ctx, want, db.AnchorParams{
		TargetID:    want.ID,
		Fingerprint: value,
		Source:      service.source,
		Reference:   strconv.FormatInt(track.ID, 10),
		Seconds:     seconds,
	})
}

// fromUploads anchors a want to a YouTube upload, for the wants a distributor's
// preview cannot reach (ADR 0029 §1).
//
// keyedSilence is why the preview path had nothing. It is kept and written
// alongside whatever this path finds, because the two are separate facts about
// the want and a person reading the row has to see both.
func (service *Service) fromUploads(
	ctx context.Context, want db.AnchorDueRow, keyedSilence string,
) error {
	if service.finder == nil || !service.finder.Available() {
		// Never absent. A search that is not wired up, or cannot run this minute,
		// has said nothing about whether an upload exists, and recording it as "no
		// upload" would make a want that has one look permanently like a want that
		// has none.
		return service.rescheduleUpload(ctx, want, ErrFinderUnavailable)
	}
	if want.DurationMS <= 0 {
		return service.store.RecordAnchorAbsent(
			ctx, want.ID, keyedSilence+" "+noLengthToKeyBy)
	}

	query := strings.TrimSpace(tagmatch.PrimaryCredit(want.Artist) + " " + want.Title)
	uploads, err := service.finder.Search(ctx, query, uploadResults)
	switch {
	case err == nil, errors.Is(err, ErrNoUpload):
	default:
		return service.rescheduleUpload(ctx, want, err)
	}

	chosen, found, topic := chooseUpload(uploads, want.Artist, want.Title, want.DurationMS)
	if !found {
		return service.store.RecordAnchorAbsent(
			ctx, want.ID, keyedSilence+" "+noUploadMatched)
	}

	// Everything that can go wrong here is asked again. YouTube breaks the client
	// that reads it regularly, and a client that failed today has said nothing
	// about the audio.
	audio, err := service.finder.Excerpt(ctx, chosen.ID, chosen.Seconds)
	if err != nil {
		return service.rescheduleUpload(ctx, want, err)
	}

	value, seconds, err := service.measure(ctx, audio)
	switch {
	case err == nil:
	case errors.Is(err, chromaprint.ErrNoFingerprint), errors.Is(err, chromaprint.ErrNotAFingerprint):
		service.logger.Warn().Err(err).Str("acquisition_target_id", want.ID.String()).
			Msg("an excerpt taken from an upload could not be fingerprinted")
		return service.store.RecordAnchorAbsent(ctx, want.ID, unreadableExcerpt)
	default:
		return service.rescheduleUpload(ctx, want, err)
	}

	service.logger.Info().
		Str("acquisition_target_id", want.ID.String()).
		Str("upload_id", chosen.ID).
		Int64("upload_views", chosen.Views).
		Int("excerpt_seconds", seconds).
		Msg("anchored a want to an upload found by name")

	source := identity.AnchorByName
	if topic {
		source = identity.AnchorByNameTopic
	}
	return service.record(ctx, want, db.AnchorParams{
		TargetID:    want.ID,
		Fingerprint: value,
		Source:      source,
		Reference:   chosen.ID,
		Label:       describeUpload(chosen),
		Views:       chosen.Views,
		Seconds:     seconds,
	})
}

// describeUpload is what a person reads instead of a video id: the upload's own
// title and the channel that published it.
func describeUpload(upload Upload) string {
	title, channel := strings.TrimSpace(upload.Title), strings.TrimSpace(upload.Channel)
	if title == "" {
		return channel
	}
	if channel == "" {
		return title
	}
	return title + " — " + channel
}

// record writes the anchor and asks for the want's held copies to be measured
// against it.
//
// The two are separate calls on purpose. Recording the anchor is evidence added
// to a want and must not depend on a queue; a re-judging pass that was never
// queued leaves every copy saying exactly what it said before.
func (service *Service) record(
	ctx context.Context, want db.AnchorDueRow, params db.AnchorParams,
) error {
	if err := service.store.RecordAnchor(ctx, params); err != nil {
		return err
	}
	if err := service.store.QueueWantCopyJudging(ctx, want.ID, service.now()); err != nil {
		service.logger.Error().Err(err).Str("acquisition_target_id", want.ID.String()).
			Msg("queue the copies of a newly anchored want for judging")
	}
	return nil
}

// measure fingerprints one excerpt with the service's fingerprinter.
func (service *Service) measure(ctx context.Context, audio []byte) (string, int, error) {
	return Measure(ctx, service.prints, audio)
}

// Measure writes an excerpt to a file and fingerprints it, because fpcalc reads
// a path and not a stream. The file is removed whatever happens: the fingerprint
// is the thing kept, and the audio itself is somebody else's to publish. A want
// keyed to an address is anchored through this too (ADR 0038 §3), so every
// excerpt passes the same checks.
func Measure(ctx context.Context, prints Fingerprinter, audio []byte) (string, int, error) {
	file, err := os.CreateTemp("", "schall-preview-*.mp3")
	if err != nil {
		return "", 0, fmt.Errorf("make room for a preview: %w", err)
	}
	name := file.Name()
	defer os.Remove(name)

	if _, err := file.Write(audio); err != nil {
		file.Close()
		return "", 0, fmt.Errorf("write a preview to %s: %w", name, err)
	}
	if err := file.Close(); err != nil {
		return "", 0, fmt.Errorf("close %s: %w", name, err)
	}

	value, seconds, err := prints.Compute(ctx, name, chromaprint.ReferenceLengthSeconds)
	if err != nil {
		return "", 0, err
	}
	// A reference that cannot be decoded back into frames cannot be compared
	// against anything, and a reference of almost no audio compares against
	// everything. Both are refused here rather than stored and discovered later
	// by whatever tries to read them.
	frames, err := chromaprint.Decode(value)
	if err != nil {
		return "", 0, err
	}
	if len(frames) < minimumFrames {
		return "", 0, fmt.Errorf("%w: the preview is %d frames of audio",
			chromaprint.ErrNoFingerprint, len(frames))
	}
	return value, seconds, err
}

// reschedule puts a want back on the schedule, and hands the rate-limiting case
// back to the caller so a pass can stop.
func (service *Service) reschedule(ctx context.Context, want db.AnchorDueRow, cause error) error {
	reason := fmt.Sprintf("The preview could not be fetched: %s", cause)
	if err := service.deferUntilLater(ctx, want, reason); err != nil {
		return err
	}
	if errors.Is(cause, deezer.ErrRateLimited) {
		return cause
	}
	service.logger.Warn().Err(cause).Str("acquisition_target_id", want.ID.String()).
		Msg("could not fetch a want's preview")
	return nil
}

// rescheduleUpload is reschedule for the upload path. It is a separate sentence
// because the two paths fail for different reasons and the row says which.
func (service *Service) rescheduleUpload(
	ctx context.Context, want db.AnchorDueRow, cause error,
) error {
	reason := fmt.Sprintf("No upload could be fetched for this entry: %s", cause)
	if err := service.deferUntilLater(ctx, want, reason); err != nil {
		return err
	}
	if errors.Is(cause, ErrFinderRateLimited) {
		return cause
	}
	service.logger.Warn().Err(cause).Str("acquisition_target_id", want.ID.String()).
		Msg("could not fetch an upload for a want")
	return nil
}

// deferUntilLater puts a want on the next rung of the ladder, with the sentence
// it keeps while it waits.
func (service *Service) deferUntilLater(
	ctx context.Context, want db.AnchorDueRow, reason string,
) error {
	at := service.now().Add(rungOf(retryLadder, want.Attempts))
	return service.store.DeferAnchor(ctx, want.ID, at, reason)
}

// rungOf reads the wait for an attempt off a ladder, holding at the last rung.
func rungOf(ladder []time.Duration, attempts int32) time.Duration {
	if attempts < 0 {
		attempts = 0
	}
	if int(attempts) >= len(ladder) {
		return ladder[len(ladder)-1]
	}
	return ladder[attempts]
}

func wait(ctx context.Context, d time.Duration) {
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
	case <-timer.C:
	}
}

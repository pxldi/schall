package jobs

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/pxldi/schall/internal/acoustid"
	"github.com/pxldi/schall/internal/db"
	"github.com/pxldi/schall/internal/events"
	"github.com/pxldi/schall/internal/labels"
	"github.com/pxldi/schall/internal/library"
	"github.com/pxldi/schall/internal/listens"
	"github.com/pxldi/schall/internal/lyrics"
	"github.com/pxldi/schall/internal/musicbrainz"
	"github.com/pxldi/schall/internal/navidrome"
	"github.com/pxldi/schall/internal/newreleases"
	"github.com/pxldi/schall/internal/playlists"
	"github.com/pxldi/schall/internal/recommendations"
	"github.com/pxldi/schall/internal/soundcloud"
	"github.com/pxldi/schall/internal/sources"
	"github.com/pxldi/schall/internal/upgrade"
	"github.com/pxldi/schall/internal/uploads"
	"github.com/pxldi/schall/internal/weekly"
	"github.com/rs/zerolog"
)

const (
	RefreshArtistMetadata = "refresh_artist_metadata"
	// RefreshLabelMetadata walks the releases one record label published. It is
	// paged over several jobs for a large label, each carrying only the label's
	// identifier: where the walk got to is kept on the label's own row.
	RefreshLabelMetadata      = "refresh_label_metadata"
	RefreshAlbumMetadata      = "refresh_album_metadata"
	ScanLibrary               = "scan_library"
	PollDownloads             = "poll_downloads"
	ImportDownload            = "import_download"
	ResolveLibraryFile        = "resolve_library_file"
	SweepLibraryWitnesses     = "sweep_library_witnesses"
	FingerprintLibraryWitness = "fingerprint_library_witness"
	IngestReleaseGroup        = "ingest_release_group"
	SearchAlbumSources        = "search_album_sources"
	SweepAcquisitionTargets   = "sweep_acquisition_targets"
	ImportPlaylist            = "import_playlist"
	SweepCoverArt             = "sweep_cover_art"
	SweepFollowFeed           = "sweep_follow_feed"
	SweepRecommendations      = "sweep_recommendations"
	// SweepLyrics is fixed by migration 00053, whose partial unique index is
	// keyed on this exact string.
	SweepLyrics = "sweep_lyrics"
	// SyncListens is fixed by migration 00094, whose partial unique index is
	// keyed on this exact string.
	SyncListens = "sync_listens"
	// SweepPreviewAnchors is fixed by migration 00058, whose partial unique
	// index is keyed on this exact string.
	SweepPreviewAnchors = "sweep_preview_anchors"
	// SweepGenres is fixed by migration 00068, whose partial unique index is
	// keyed on this exact string.
	SweepGenres = "sweep_genres"
	// SweepCopyFingerprints is fixed by migration 00062, whose partial unique
	// index is keyed on this exact string.
	SweepCopyFingerprints = "sweep_copy_fingerprints"
	// SweepLoudness is fixed by migration 00060, whose partial unique index is
	// keyed on this exact string.
	SweepLoudness = "sweep_loudness"
	// SweepAudioSpectrum is fixed by migration 00067, whose partial unique index
	// is keyed on this exact string.
	SweepAudioSpectrum = "sweep_audio_spectrum"
	// SweepUpgradeCandidates is fixed by migration 00072, whose partial unique
	// index is keyed on this exact string.
	SweepUpgradeCandidates = "sweep_upgrade_candidates"
	// JudgeCopiesAgain is asked for by a person and never scheduled. It is the
	// one pass that re-opens decisions Schall already made, and it is deliberate
	// rather than periodic: the rules it re-runs change rarely, and a pass that
	// ran on a timer would keep re-deciding copies nothing had changed for.
	JudgeCopiesAgain = "judge_copies_again"
	// JudgeCreditRefusals is queued once by migration 00087, when a credit
	// became the set of artists it names. Like JudgeCopiesAgain it is never
	// scheduled: it re-opens decisions Schall already made, and it does that
	// because one rule changed under them.
	JudgeCreditRefusals = "judge_credit_refusals"
	// JudgeWantCopies is the same second look at one want alone, queued by the
	// anchor sweep when a want gains a reference its copies were judged without
	// (ADR 0029 §5). Its payload names the want.
	JudgeWantCopies = "judge_want_copies"
	// RefreshWeeklyPlaylist is fixed by migration 00052, whose partial unique
	// index is keyed on this exact string.
	RefreshWeeklyPlaylist = "refresh_weekly_playlist"
	// RefreshNewReleasesPlaylist is fixed by migration 00075, whose partial
	// unique index is keyed on this exact string.
	RefreshNewReleasesPlaylist = "refresh_new_releases_playlist"
	ImportUpload               = "import_upload"
	ApplyLibraryLayout         = "apply_library_layout"
	// TagLibrary is fixed by migration 00077, whose partial unique index is
	// keyed on this exact string.
	TagLibrary     = "tag_library"
	TagLibraryFile = "tag_file"
	// SweepTranscode is fixed by migration 00071, whose partial unique index is
	// keyed on this exact string.
	SweepTranscode = "sweep_transcode"
	// SweepDuplicates is fixed by migration 00076, whose partial unique index is
	// keyed on this exact string.
	SweepDuplicates = "sweep_duplicates"
	// SyncNavidromePlaylists is fixed by migration 00038, whose partial unique
	// index is keyed on this exact string. An applied migration is never edited,
	// so this name is not one to tidy later.
	SyncNavidromePlaylists = "sync_navidrome_playlists"
	// NotifyPlayer is fixed by migration 00073, whose partial unique index is
	// keyed on this exact string.
	NotifyPlayer = "notify_player"
	// AnswerPeerChallenges is fixed by migration 00084, whose partial unique
	// index is keyed on this exact string.
	AnswerPeerChallenges = "answer_peer_challenges"
	// CleanInbox is asked for by a person and never scheduled. It walks the
	// download inbox once and deletes the files nothing needs any more
	// (ADR 0035).
	CleanInbox = "clean_inbox"
)

// lyricsIdle is how long the lyrics sweep waits when it has walked the whole
// library. LRCLIB gains songs every day and a song that had no words last month
// may have them now, so the walk starts again — slowly, because nothing about a
// collection that already has its words is urgent.
const lyricsIdle = 12 * time.Hour

// listensIdle is how often the listening history is brought up to date. A
// listen is a few hundred bytes, and the Overview's Recently played is read
// while the music is still on, so a quarter of an hour behind read as missing
// listens (2026-09-08). Five minutes costs the service twelve small reads an
// hour.
const listensIdle = 5 * time.Minute

// listensUnreachable is how long the sync waits when ListenBrainz stopped
// answering.
const listensUnreachable = time.Hour

// lyricsUnreachable is how long it waits when LRCLIB stopped answering. A free
// public service in difficulty is not asked again in a few minutes.
const lyricsUnreachable = time.Hour

// loudnessIdle is how long the loudness pass waits when it has measured
// everything the library holds. Nothing about a collection that is already
// measured is urgent, and the only thing that makes work for this pass is music
// arriving — which the pass finds on its next turn either way.
const loudnessIdle = 6 * time.Hour

// anchorIdle is how long the preview sweep waits when no want is due one. A want
// schedules itself when it is created, so this is only the floor under how long
// a new want waits to be anchored, and the anchor is wanted before a copy of the
// music turns up rather than the moment the want appears.
//
// It is also the floor under a want that met rate limiting. That want asks to be
// tried again in five minutes, and this is what decides when anybody looks: the
// shorter of the two is deliberately not the one that wins, because a pass that
// came back in five minutes would be the same pass meeting the same refusal.
const anchorIdle = 30 * time.Minute

// spectrumIdle is how long the spectrum sweep waits when every file in the
// library has been listened to. Nothing here changes on its own — a file's audio
// is the same audio tomorrow — so a pass only exists for what a scan has found
// or re-read since, and it is in no hurry to find it.
const spectrumIdle = 6 * time.Hour

// upgradeIdle is how long the upgrade sweep waits when it has walked the whole
// library and found nothing left under the floor. Nothing about a file's
// stored bit rate changes on its own, so a pass only exists for what a scan
// has added or the floor since it last walked, and it is in no hurry to find
// it.
const upgradeIdle = 6 * time.Hour

// tagLibraryIdle is how long the whole-library tagging pass waits, once it has
// finished a run, before it asks for another.
//
// The per-file job already covers a file the moment it is matched
// (internal/matching/service.go:316) or kept as a duplicate's survivor
// (internal/library/keepone.go:498). What neither of those catches is a file
// whose catalogue data changes after that moment — MusicBrainz corrects a
// release date or an artist credit, and the file on disk keeps the old value
// until somebody presses the button. A week is how long that correction is
// left standing before this pass finds it on its own.
const tagLibraryIdle = 7 * 24 * time.Hour

// anchorUnreachable is how long it waits when Deezer stopped answering
// altogether. It is the same reasoning as the lyrics sweep: a free public
// service in difficulty is not asked again in a few minutes.
const anchorUnreachable = time.Hour

// genreBatch is how many artists one genre backfill pass queues a refresh for.
// It is a queue depth rather than a request count: the refreshes themselves are
// claimed one at a time and paced by the MusicBrainz client, so this is how much
// work is put on the books at once.
const genreBatch = 25

// genresPace is how long the backfill waits when artists are still waiting. The
// refreshes it queued have to run before those artists stop answering to the
// question, and a pass that came straight back would find the same batch and
// queue nothing, over and over.
const genresPace = 10 * time.Minute

// genresIdle is how long it waits when every artist in the catalogue has been
// asked about. Genre votes change slowly and an artist entering the catalogue is
// genred by the refresh that put them there, so this is only the floor under how
// long a row that somehow escaped both waits.
const genresIdle = 12 * time.Hour

// copyFingerprintIdle is how long the copy fingerprint sweep waits when every
// held copy has been measured. It is a backfill: the copies that arrive from now
// on are measured while they are judged, so this pass exists for the ones judged
// before there was anywhere to keep the number, and for a copy whose file turns
// up again later. Nothing about it is urgent — a queue that is not grouped yet
// is a queue that reads exactly as it did last month.
const copyFingerprintIdle = 6 * time.Hour

// copyFingerprintUnreadable is how long it waits when the pass could not run at
// all, which in practice means fpcalc is not installed. That will not be true
// again in an hour, so it is asked rarely rather than never: somebody who
// installs it should not have to restart Schall.
const copyFingerprintUnreadable = 12 * time.Hour

// coverArtIdle is how long the picture sweep waits when it found nothing left to
// picture. It is not urgent work — a release without a cover is a release with a
// blank square — so this is slow enough to cost nothing and often enough that a
// catalogue refreshed today is pictured today.
const coverArtIdle = 6 * time.Hour

// coverArtUnreachable is how long the picture sweep waits when it gave up
// because an archive would not answer, rather than because there was nothing
// left to picture. Those are different facts and they were being told apart
// nowhere: a pass that exhausted its attempts against a failing archive queued
// its replacement six hours out, so a bad ten minutes cost the catalogue the
// rest of the day. Waiting is still right — an archive shedding load is not
// helped by being asked again immediately — but for as long as an outage
// plausibly lasts, not for as long as a fully pictured catalogue may sit.
const coverArtUnreachable = 15 * time.Minute

// followFeedIdle is how long the follow feed waits between passes. It is not the
// rate at which artists are asked about — the feed decides that for itself from
// how stale each one is — but how often it looks. Four times a day is often
// enough that a release published this morning is wanted today, and rare enough
// that a pass finding nothing costs one query.
const followFeedIdle = 6 * time.Hour

// weeklyIdle is how often the weekly playlist refreshes. It is a week because
// the list is a week's worth of music, and because the grace week — a departure
// announced by one refresh and carried out by the next — is measured in
// refreshes rather than in a clock of its own. A pass that had nothing to do
// costs a handful of queries.
const weeklyIdle = 7 * 24 * time.Hour

// recommendationIdle is how often Schall asks for a new provider snapshot
// when a pass consumed everything available. ListenBrainz builds its model in
// batches and MusicBrainz expansion is the expensive half of the sweep, so a
// daily pass keeps the list current without repeatedly fetching one batch.
const recommendationIdle = 24 * time.Hour

// recommendationUnreachable is how long the recommendation sweep waits when a
// pass got nothing at all out of a service it asked, rather than when it had
// asked about everything on offer.
//
// Those are different facts and the daily wait told them apart nowhere. A pass
// cut short by a bad minute stored what it had, called the snapshot partial and
// then sat on it until the next day, so one refused request cost the list
// twenty-four hours of the records it never got to ask about. Waiting is still
// right — a service shedding load is not helped by being asked again at once —
// but for as long as an outage plausibly lasts, not for as long as a finished
// snapshot may sit.
//
// A pass that got answers and then met one failed look-up is not one of these.
// It is a continuation and it comes straight back, because a service that
// answered several times in a row is up and merely slow, and thirty minutes for
// each slow request is longer than the whole sweep (issue #310).
//
// Three passes reach this wait. The first is a pass MusicBrainz answered about
// nothing at all, which is the pass whose very first look-up failed. A pass it
// did answer about is not one of these, whether or not the pass could store a
// row from the answer, because a recording MusicBrainz knows too thinly to store
// is still MusicBrainz being there (issue #314).
//
// The second comes from the sweep's other service: a pass asks ListenBrainz for
// the account's top recordings and for the records like them, and when that part
// of the answer is missing the chain walks a shorter list than it was owed. That
// chain can finish and publish, so it has nothing left to continue, and half an
// hour is when it is worth asking for the whole answer again.
//
// The third is a pass ListenBrainz named no records at all to, which kept the
// list it had already published rather than replace it with nothing (issue
// #317). Nobody can tell that answer from an account with nothing to suggest, so
// it is put again once an outage has plausibly passed.
const recommendationUnreachable = 30 * time.Minute

// playerSyncIdle is how long the sweep over the player waits between passes.
//
// It is a conversation with another program about lists that only change when
// somebody changes them, and both sides of that change announce themselves
// otherwise: an import queues a pass over the list it just reconciled, and a
// press queues one now. This is the floor under both — what makes a copy the
// acquisition loop fetched overnight reach the player without anybody asking.
const playerSyncIdle = 6 * time.Hour

// rateLimitBackoff is how long a job waits after the download source refused it
// for asking too often. Concurrent searches are enough to trip slskd's limit,
// and a release that was never searched must not spend its attempts finding
// that out repeatedly.
const rateLimitBackoff = 60 * time.Second

// sweepFailureBackoff is how long the acquisition sweeper waits after a sweep
// that could not be finished at all.
//
// It exists because that sweeper schedules itself: an exhausted sweep has to
// queue its own replacement or nothing ever sweeps again, and queueing it for
// the moment a target became due — which is a moment already past — would ask
// again immediately and keep asking. Nothing is lost by waiting, because a sweep
// that failed changed nothing and every want keeps its own schedule. It matches
// the top of the ordinary retry ladder.
const sweepFailureBackoff = 5 * time.Minute

// leaseExpiredAfter is how long a running job may go without its worker proving
// that it still owns the claim. It is deliberately much longer than the
// heartbeat interval, so one delayed heartbeat cannot hand live work to another
// worker.
const leaseExpiredAfter = 10 * time.Minute

// heartbeatInterval is how often a worker renews the lease on the job it is
// processing. Its attempt and start time identify the exact claim, so a worker
// cannot renew a later claim of the same row.
const heartbeatInterval = time.Minute

// duplicateSweepInterval is how long the pass over the recordings held more
// than once waits before looking again. Duplicates appear when music arrives,
// and a scan that brought files in asks for a pass of its own, so this is the
// backstop rather than the way the work is found.
const duplicateSweepInterval = 6 * time.Hour

// reapInterval is how often the worker looks for those rows. It is the
// resolution of the healing rather than its threshold: a leak is put back
// somewhere within this of lease expiry, and looking oftener would only ask the
// same question of the same rows.
const reapInterval = 10 * time.Minute

// tidyInterval is how often the worker looks at the jobs that spent every
// attempt they had. Nothing used to look at them at all: a job that failed for
// good stayed in the failed list until a person pressed Retry on it, and the
// count of them in the navigation never went back to nothing on its own.
const tidyInterval = 15 * time.Minute

// reviveSpentAfter is how long a job that spent its attempts on something
// unreachable waits before Schall tries it once more by itself.
//
// The case it is for is an outage: on 12 August 2026 the machine could not
// resolve the name musicbrainz.org for a few minutes, and seventy-one jobs spent
// every attempt inside it. Every one of them would have succeeded ten minutes
// later, and the only thing offered to the person who found them was a Retry
// button on each — which is Schall asking somebody to do what Schall can do.
//
// Half an hour is chosen because it is longer than a name-resolution outage
// usually lasts and shorter than the patience of somebody who has noticed the
// count in the navigation.
const reviveSpentAfter = 30 * time.Minute

// reviveBatch bounds how many spent jobs one pass puts back. A pass that put
// back a thousand at once would hand the worker a thousand requests to a service
// that has only just come back; what is left over is taken by the next pass.
const reviveBatch = 200

// spentJobRetention is how long a job that spent its attempts is kept after it
// failed. It is history rather than work: the cause was read, or it was tried
// again and failed again, and a row nobody has acted on in a fortnight is not
// going to be acted on. Keeping them forever is what made the failed list a
// table nobody could read.
const spentJobRetention = 14 * 24 * time.Hour

// resolutionBatch bounds how many files one completed scan asks the providers
// about. Every resolution is at least one rate-limited request, so a first scan
// of a large library queues work steadily rather than all at once; what is left
// is picked up by the next scan.
const resolutionBatch = 500

// resolutionNotice is the shortest gap between two notices from a run of file
// resolutions. Most of a batch waits on a rate-limited provider, but a file the
// library can already answer for takes no time at all, and a notice each would
// cost every open interface a refetch it could not read. The last one is still
// sent: rationing delays a notice, it never drops the final one.
const resolutionNotice = 2 * time.Second

type Job struct {
	ID          uuid.UUID
	Kind        string
	Payload     json.RawMessage
	Attempts    int
	MaxAttempts int
	StartedAt   time.Time
}

// Lane is which job kinds a worker will claim. Only takes those kinds and
// nothing else; Except takes everything but those. The pair exists so that the
// lanes can be defined once, in terms of each other: a job kind added later
// lands on the general lane by being absent from the other one, rather than on
// no lane at all.
//
// A zero Lane claims anything, which is what a single worker did before there
// was more than one.
type Lane struct {
	Only   []string
	Except []string
	// First are the kinds this lane claims ahead of the rest, whatever order
	// they were created in. Everything else keeps its place in the queue.
	First []string
}

// AcquisitionKinds are the jobs whose cost is a wait on somebody else rather
// than work Schall does. A Soulseek search is a broadcast that collects replies
// until a fixed window closes — twenty seconds whether the answer arrives in
// two or never — and an acquisition sweep is a run of them. On one worker that
// window was also blocking the import of a transfer that had already finished.
var AcquisitionKinds = []string{SweepAcquisitionTargets, SearchAlbumSources}

// AnchorKinds are the preview-anchor sweep. It gets a lane of its own because
// each pass queues one judge per want it anchored, and on a shared lane those
// judges ran before the next pass: twenty anchors, then twenty multi-minute
// judges, and the sweep sat at 62 of 332 anchors for hours. The sweep
// re-queues itself, so one worker keeps it moving.
var AnchorKinds = []string{SweepPreviewAnchors}

// PressedKinds are the jobs a person is waiting on with a screen open. They are
// claimed before whatever else is queued, because the queue regularly holds
// hundreds of rows of background work — a catalogue refresh of every album is
// two hundred jobs paced one a second — and a button pressed behind that
// backlog looked to the person like a button that does nothing.
//
// Only kinds a press creates are listed. tag_library_file is not one: the
// tagging pass makes those in bulk, and a bulk kind in here would be a second
// backlog rather than a way past the first.
var PressedKinds = []string{
	ScanLibrary,
	TagLibrary,
	ApplyLibraryLayout,
	ImportUpload,
	SweepTranscode,
	SweepUpgradeCandidates,
	SyncNavidromePlaylists,
}

// TransferKinds are the jobs that follow a download from the moment slskd starts
// sending it to the moment its files are in the library. They get a lane of
// their own because the general queue regularly holds hundreds of rows of
// catalogue work — a refresh of every album is four thousand jobs paced one a
// second — and behind that backlog a finished transfer sat unimported for
// hours. PressedKinds was not enough: those two are not pressed by anybody,
// they are queued by the transfer itself.
var TransferKinds = []string{PollDownloads, ImportDownload}

// JudgingKinds are the copy judges. They get a lane of their own because judges
// are serial per want by design. One worker is enough: a judge and an import on
// the same want already run on different lanes, so this adds no new overlap.
// The library witness decode runs here too: it is one fpcalc run per file, up
// to two minutes each, and twenty of them in a row on the general lane would
// hold scans, sweeps and everything else behind them.
var JudgingKinds = []string{
	JudgeWantCopies, JudgeCopiesAgain, JudgeCreditRefusals, FingerprintLibraryWitness,
}

// Acquisition is the lane for searching, Transfers follows downloads, Anchors
// runs preview sweeps, Judging runs copy judges, and General is everything else.
// Each lane is one worker rather than a pool:
// source-search jobs are claimed one at a time in creation order and running
// two would break that, and slskd answers 429 under concurrency, so the
// parallelism worth having belongs behind its rate limit rather than here. One
// transfer worker is enough for the same reason — the poller is a single loop
// over what slskd is sending, and two would poll the same transfers twice. The
// anchor and judging lanes have their own reasons above.
func Acquisition() Lane { return Lane{Only: AcquisitionKinds} }
func Transfers() Lane   { return Lane{Only: TransferKinds} }
func Anchors() Lane     { return Lane{Only: AnchorKinds} }

// The judges go before the witness decodes: a re-judge answers a want that is
// waiting, a decode only prepares evidence for later, and a library-sized
// backfill of decodes would otherwise hold every wake for hours.
func Judging() Lane {
	return Lane{
		Only:  JudgingKinds,
		First: []string{JudgeCopiesAgain, JudgeWantCopies, JudgeCreditRefusals},
	}
}
func General() Lane {
	return Lane{
		Except: slices.Concat(AcquisitionKinds, TransferKinds, AnchorKinds, JudgingKinds),
		First:  PressedKinds,
	}
}

// SpentJob is a job that failed as often as it was allowed to and is waiting in
// the failed list. Error is the text the last attempt recorded, which is what
// Classify reads to say what stopped it.
type SpentJob struct {
	ID    uuid.UUID
	Kind  string
	Error string
}

type Queue interface {
	RecoverInterrupted(context.Context) error
	ReapStalled(context.Context, time.Duration) (int64, error)
	// SpentJobs lists the jobs that failed for good before a moment, and that
	// Schall has not already tried again by itself.
	SpentJobs(context.Context, time.Time, int32) ([]SpentJob, error)
	// ReviveSpentJobs puts those jobs back on the queue for one more attempt. It
	// answers how many went back, and how many were left alone because the same
	// work is queued or running already.
	ReviveSpentJobs(context.Context, []uuid.UUID) (int64, int64, error)
	// ForgetSpentJobs removes the failures that are old enough to be history.
	ForgetSpentJobs(context.Context, time.Time) (int64, error)
	Claim(context.Context, Lane) (Job, error)
	Heartbeat(context.Context, Job) (bool, error)
	ArtistMusicBrainzID(context.Context, uuid.UUID) (uuid.UUID, error)
	AlbumMusicBrainzIDs(context.Context, uuid.UUID) (uuid.UUID, uuid.UUID, error)
	SaveArtistCatalogue(context.Context, uuid.UUID, musicbrainz.ArtistCatalogue) error
	// MarkArtistGenresAsked and MarkAlbumGenresAsked record that MusicBrainz was
	// asked for genres and had none to give, which is what stops the genre
	// backfill asking again forever.
	MarkArtistGenresAsked(context.Context, uuid.UUID) error
	MarkAlbumGenresAsked(context.Context, uuid.UUID) error
	SaveReleaseCatalogue(context.Context, uuid.UUID, musicbrainz.ReleaseCatalogue) error
	SaveCatalogueReleaseGroup(context.Context, musicbrainz.ReleaseGroupDetail) (uuid.UUID, error)
	Complete(context.Context, Job) error
	JobPayload(context.Context, uuid.UUID) ([]byte, error)
	CompleteAcquisitionSweep(context.Context, Job) (bool, error)
	CompleteLibraryWitnessSweep(context.Context, Job) (bool, error)
	Retry(context.Context, Job, string, time.Time) error
	Fail(context.Context, Job, string) error
	Cancel(context.Context, Job, string) error
	SourceSearchRunCancelled(context.Context, uuid.UUID) (bool, error)
	QueueDownloadPoll(context.Context, time.Time) error
	QueueAcquisitionSweep(context.Context, time.Time) error
	QueueCoverArtSweep(context.Context, time.Time) error
	QueueLoudnessSweep(context.Context, time.Time) error
	QueueFollowFeedSweep(context.Context, time.Time) error
	QueueRecommendationSweep(context.Context, time.Time) error
	QueueWeeklyPlaylistRefresh(context.Context, time.Time) error
	QueueNewReleasesPlaylistRefresh(context.Context, time.Time) error
	QueueRecommendationSweepContinuation(context.Context, time.Time, json.RawMessage) error
	QueueLyricsSweep(context.Context, time.Time, json.RawMessage) error
	QueueListensSync(context.Context, time.Time) error
	QueuePreviewAnchorSweep(context.Context, time.Time) error
	QueueWantCopyJudging(context.Context, uuid.UUID, time.Time) error
	QueueCopyFingerprintSweep(context.Context, time.Time) error
	QueueSpectrumSweep(context.Context, time.Time) error
	QueueUpgradeSweep(context.Context, time.Time, json.RawMessage) error
	QueueNavidromePlaylistSweep(context.Context, time.Time) error
	QueueGenreSweep(context.Context, time.Time) error
	BackfillGenres(context.Context, int32) (db.BackfillGenresRow, error)
	QueueFileResolutions(context.Context, int32) (int64, error)
	QueueLibraryWitnessFingerprintSweep(context.Context, time.Time) error
	QueueLibraryWitnessFingerprintJobs(context.Context, int32) (int64, error)
	MarkUploadedFiles(context.Context, uuid.UUID, []string) error
	// QueueNotifyPlayer asks for the player to be told the library changed,
	// coalescing a burst of changes into one telling. See its implementation
	// for what "coalescing" means here.
	QueueNotifyPlayer(context.Context) error
	// QueuePeerChallenges asks for the private chat of the peers with transfers
	// waiting to be read. It is queued when a request is started and when a
	// transfer settles, and holds one live row however many callers ask.
	QueuePeerChallenges(context.Context, time.Time) error
}

type CatalogueProvider interface {
	ArtistCatalogue(context.Context, uuid.UUID) (musicbrainz.ArtistCatalogue, error)
	ReleaseCatalogue(context.Context, uuid.UUID, uuid.UUID) (musicbrainz.ReleaseCatalogue, error)
	ReleaseGroup(context.Context, uuid.UUID) (musicbrainz.ReleaseGroupDetail, error)
}

type LibraryScanner interface {
	Scan(context.Context) (library.ScanResult, error)
}

type WitnessFingerprinter interface {
	Fingerprint(context.Context, uuid.UUID) error
}

// TransferTracker follows transfers a provider is running. Poll reports whether
// anything is still moving, so the worker can stop looking once nothing is.
type TransferTracker interface {
	Poll(context.Context) (bool, error)
	PollInterval() time.Duration
}

// PeerChallengeAnswerer answers the human checks some Soulseek peers send by
// private message before they will send a file. Answer reports whether any
// transfer is still open with a peer worth reading, so the worker can stop
// looking once none is.
type PeerChallengeAnswerer interface {
	Answer(context.Context) (bool, error)
	Interval() time.Duration
}

type DownloadImporter interface {
	Import(context.Context, uuid.UUID) error
	Fail(context.Context, uuid.UUID, string) error
	// JudgeAgain puts the copies already decided about through validation a
	// second time, where the rules that decided them have since changed. What it
	// did is reported to the log by the pass itself.
	JudgeAgain(context.Context) error
	// JudgeCreditRefusalsAgain does the same for the copies refused on the
	// artist tag alone, which is a different set and a different rule change
	// (ADR 0030).
	JudgeCreditRefusalsAgain(context.Context) error
	// JudgeWantCopies does the same for one want that has just been anchored. The
	// copies whose bytes are still there go back through validation; the ones
	// whose bytes have gone are measured from the fingerprint kept on them, and
	// keep the verdict they have.
	JudgeWantCopies(context.Context, uuid.UUID) error
	// FingerprintHeldCopies measures the audio of the held copies that have no
	// fingerprint on record, and reports whether more are waiting. It decides
	// nothing: the number is what lets the review queue show the copies that are
	// one piece of audio as one row.
	FingerprintHeldCopies(context.Context) (bool, error)
}

// InboxCleaner deletes the files in the download inbox that nothing needs any
// more. Optional: without it the route reports itself unavailable and the inbox
// keeps everything, which is what it did before.
type InboxCleaner interface {
	Clean(context.Context, uuid.UUID) error
}

// FileResolver asks the metadata providers what one library file is. Fail
// records that the provider could not be reached, once retrying has stopped, so
// that a file is never left looking as though nobody had got to it yet.
type FileResolver interface {
	Resolve(context.Context, uuid.UUID) error
	Fail(context.Context, uuid.UUID, string) error
}

// AcquisitionSweeper attempts the wanted recordings whose turn it is. NextDue
// says when the next one is due, so the sweeper can be scheduled for exactly
// that moment; a zero time means nothing is scheduled and there is nothing to
// wake up for. It is separate from Sweep because it may only be asked once this
// job is no longer live — see processAcquisitionSweep.
type AcquisitionSweeper interface {
	Sweep(context.Context) error
	NextDue(context.Context) (time.Time, error)
}

// StandingWanter wants the missing tracks of a release whose tracklist has just
// arrived, when the artist behind it has a standing "want what is missing". It
// applies the rule itself and reports how many wants it made, so a release
// nobody has a standing want for, or one outside the artist's monitor level,
// comes back as nought.
//
// Optional: without it a tracklist arriving wants nothing at all, which is what
// happened before the standing want existed.
type StandingWanter interface {
	WantAlbumMissing(ctx context.Context, albumID uuid.UUID) (int, error)
}

// SourceSearcher looks for download sources for one release of a bulk run. It
// records what it found; it never chooses among them.
type SourceSearcher interface {
	Search(ctx context.Context, runID, albumID uuid.UUID) error
	Fail(ctx context.Context, runID, albumID uuid.UUID, reason string) error
}

// CoverArtSweeper fills the picture cache. Sweep reports whether releases were
// left over, which is how the job knows to come straight back rather than wait.
type CoverArtSweeper interface {
	Sweep(context.Context) (bool, error)
}

// LoudnessSweeper measures how loud the files already in the library are, so a
// player can even the collection out. Sweep reports whether files were left
// over, which is how the job knows to come straight back rather than wait.
type LoudnessSweeper interface {
	Sweep(context.Context) (bool, error)
}

// FollowFeedSweeper asks about the followed artists again and wants what they
// have released since the follow. Sweep reports whether wants were left over,
// which is how the job knows to come straight back rather than wait.
type FollowFeedSweeper interface {
	Sweep(context.Context) (bool, error)
}

// LabelRefresher walks the releases a record label published and brings them
// into the catalogue. Refresh reports whether pages were left over, which is how
// the job knows to queue the next pass rather than stop; QueueRefresh is that
// next pass, queued by the refresher because a pass is named by a label
// identifier it already carries.
type LabelRefresher interface {
	Refresh(context.Context, uuid.UUID) (bool, error)
	QueueRefresh(context.Context, uuid.UUID) error
}

// RecommendationSweeper replaces the external source's current candidate
// snapshot. Sweep reports whether work was deliberately left for another pass;
// provider failures are errors so the job record says why the snapshot stayed
// unchanged.
type RecommendationSweeper interface {
	SweepPage(context.Context, recommendations.SweepPosition) (recommendations.SweepResult, error)
}

// WeeklyPlaylistRefresher runs one pass of the weekly playlist: judge what is on
// trial, take in what arrived, choose the next songs.
type WeeklyPlaylistRefresher interface {
	Refresh(context.Context) (weekly.RunResult, error)
}

// NewReleasesPlaylistRefresher runs one pass of the new-releases playlist: put
// on it what the library owns from the releases the follow feed found lately,
// and take off what has aged out of the window. It moves playlist rows only —
// no file, no want and no decision is touched by it.
type NewReleasesPlaylistRefresher interface {
	Refresh(context.Context) (newreleases.RefreshResult, error)
}

// PlaylistImporter reconciles one followed playlist against its source.
type PlaylistImporter interface {
	Import(context.Context, uuid.UUID) error
}

// UploadImporter validates one staged upload and takes it into the library. It
// is a job rather than part of the request that sent the bytes, because
// validating and copying a 400 MB release is minutes of work and nobody should
// be holding a browser connection open for it.
//
// Import returns where each file landed, in the order the upload's own files
// are in. It is how the worker finds the one file a SoundCloud address named,
// without asking the layout template a second time for where that turned out
// to be.
type UploadImporter interface {
	Import(context.Context, uuid.UUID) ([]string, error)
}

// SoundCloudNamer records that a library file holds one SoundCloud track. It
// is soundcloud.Service.Adopt reached by path rather than by row id: an
// upload knows its address before the scan below has given the file a row to
// write against.
type SoundCloudNamer interface {
	AdoptAtPath(ctx context.Context, path, permalink string, confirmed soundcloud.Naming) (soundcloud.Adopted, error)
}

// PlaybackNotifier tells the player that the library changed, so music Schall
// has just written is playable now rather than whenever the player next looks
// of its own accord.
//
// An installation with no player configured is not an error: nothing is owed to
// anybody, so a notifier with nothing to tell reports success.
type PlaybackNotifier interface {
	NoticeLibraryChange(context.Context) error
}

// LayoutMover applies a planned layout migration. Apply reports whether the run
// still has files waiting, which is how the job knows to queue its replacement
// and yield rather than hold the lane for tens of thousands of renames. It
// queues that replacement itself, because a run is named by an identifier the
// mover already carries and nothing else in the queue needs to know about it.
type LayoutMover interface {
	Apply(context.Context, uuid.UUID) (bool, error)
	QueueApply(context.Context, uuid.UUID) error
}

// LibraryTagger writes into the files already in the library what Schall proved
// them to be. Tag reports whether files are still waiting, which is how the job
// knows to queue its replacement and yield rather than hold the lane for a
// library's worth of file copies. Both take the job the pass is carried by,
// because the pass lives in that job's payload and nothing else needs to know
// what a pass is.
// TagFile is the same write on one file whose mappings have just changed,
// taking the file rather than a job because a single file is not a pass. It
// reads what the file is mapped to and writes accordingly: the catalogue's
// account into a file that is a track's, and the tags the file arrived with
// back into one that is no longer any track's. It answers whether the file was
// rewritten, so that a player is told about a library that changed and left
// alone about one that did not.
type LibraryTagger interface {
	Tag(context.Context, uuid.UUID) (bool, error)
	QueueNextTagging(context.Context, uuid.UUID) error
	TagFile(context.Context, uuid.UUID) (bool, error)
	// QueueLibrarySweep asks for the next whole-library pass at a moment. A
	// finished run says it this way to schedule its own weekly successor.
	QueueLibrarySweep(context.Context, time.Time) error
}

// TranscodeSweeper shrinks files already in the library that the transcoding
// setting asks for, running behind the same job shape as LibraryTagger: Sweep
// reports whether files are still waiting, which is how the job knows to queue
// its replacement rather than hold the lane for a library's worth of
// re-encodes.
type TranscodeSweeper interface {
	Sweep(context.Context, uuid.UUID) (bool, error)
	QueueNextSweep(context.Context, uuid.UUID) error
}

// DuplicateSweeper settles the recordings the library holds more than once,
// keeping one copy of each. It reads the setting itself and does nothing when
// it is off, so the worker never has to know what the person chose.
type DuplicateSweeper interface {
	SweepDuplicates(context.Context) (SweptDuplicates, error)
	// QueueDuplicateSweep asks for a pass at a moment. A scan that brought
	// files in asks for one now; a pass that has just finished asks for the
	// next one hours away.
	QueueDuplicateSweep(context.Context, time.Time) error
}

// SweptDuplicates is what one pass came to, in the shape the worker logs it.
type SweptDuplicates struct {
	Skipped   bool
	Resolved  int
	Removed   int
	LeftAlone int
}

// PlaylistSyncer joins the want lists to the player's playlists, one list at a
// time or across the whole player. Both answer with what the pass did rather
// than with nothing, because a track nobody could pair and a break nobody could
// repair are answers worth a line in the log.
//
// An installation with no player configured is not an error here either: the
// result says it did nothing.
type PlaylistSyncer interface {
	SyncPlaylist(context.Context, uuid.UUID) (navidrome.SyncResult, error)
	Sweep(context.Context) (navidrome.SweepResult, error)
}

type Worker struct {
	queue     Queue
	provider  CatalogueProvider
	scanner   LibraryScanner
	witnesses WitnessFingerprinter
	transfers TransferTracker
	// challenges answers the peers that will not send a file until a word is
	// typed in chat. Optional: without it those transfers fail exactly as they
	// did before, and the message is never read.
	challenges PeerChallengeAnswerer
	importer   DownloadImporter
	// inbox deletes the downloaded files nothing needs any more. Optional: an
	// installation without it never deletes anything from the inbox.
	inbox    InboxCleaner
	resolver FileResolver
	searcher SourceSearcher
	wants    AcquisitionSweeper
	// standing wants the missing tracks of a release whose tracklist has just
	// arrived, for an artist somebody asked to keep complete. See
	// StandingWanter.
	standing StandingWanter
	covers   CoverArtSweeper
	// loud measures how loud a file is. Optional: without it the library keeps
	// whatever replay gain its files arrived with, which is what it had before.
	loud LoudnessSweeper
	feed FollowFeedSweeper
	// labels walks a followed record label's releases. Optional: without it a
	// label cannot be refreshed, which is what an installation that follows no
	// label already has.
	labels     LabelRefresher
	recommends RecommendationSweeper
	// lyrics writes the words of a song beside it. Optional: without it the
	// library simply has no lyrics, which is what it had before.
	lyrics  LyricsSweeper
	listens ListensSyncer
	// anchors fetches the distributor's preview of a wanted recording, so a copy
	// a stranger sends can be compared against audio of the right recording.
	// Optional: without it a want has no anchor and its copies are judged by
	// whatever else can speak about them, which is what happened before.
	anchors AnchorSweeper
	// spectra measure what the audio in a library file is, as opposed to what
	// the name on it claims. Optional, and it decides nothing either way:
	// without it two copies of one recording are compared on exactly what they
	// were compared on before.
	spectra SpectrumSweeper
	// upgrades looks for files below the quality floor and raises a want for
	// each one. Optional, and switched off in its own settings by default: an
	// installation that never opens the setting has this sweep find nothing.
	upgrades UpgradeSweeper
	// notifier tells the user the review queue has a question. Optional, and
	// switched off in the settings by default: an installation that never opens
	// that setting sends nothing anywhere.
	notifier    QueueNotifier
	weekly      WeeklyPlaylistRefresher
	newReleases NewReleasesPlaylistRefresher
	playlists   PlaylistImporter
	uploads     UploadImporter
	// soundCloud names an uploaded file from the address it arrived with.
	// Optional: without it, or without a scanner to give the file a row first,
	// an upload that carried an address lands unnamed, the same as one that
	// carried none.
	soundCloud SoundCloudNamer
	playback   PlaybackNotifier
	playerSync PlaylistSyncer
	layout     LayoutMover
	tagger     LibraryTagger
	// duplicates keeps one copy of every recording the library holds more than
	// once. Optional, and it does nothing at all unless the setting is on:
	// without it a recording held twice stays a question for a person.
	duplicates DuplicateSweeper
	// shrink re-encodes proven library files already on disc into the smaller
	// format the transcoding setting asks for. Optional: without it a file
	// already in the library is never re-encoded, only ever the ones an import
	// shrinks as they arrive.
	shrink TranscodeSweeper
	// notices tells connected interfaces that a job changed what they show. It
	// is optional: without it the views go back to asking on a timer.
	notices *events.Hub
	// resolutions is the same topic rationed, for the one job kind that arrives
	// in hundreds. See resolutionNotice.
	resolutions  *events.Rationed
	logger       zerolog.Logger
	pollInterval time.Duration
	now          func() time.Time
	// recovery requeues the jobs a shutdown interrupted, and must happen once
	// however many lanes are running. A second pass would put a job another
	// lane had just claimed back on the queue.
	recovery sync.Once
	// Lease timings are fields so tests can drive them without waiting on the
	// production clock.
	leaseExpiredAfter time.Duration
	heartbeatEvery    time.Duration
	reapEvery         time.Duration
	tidyEvery         time.Duration
	// One reaper serves every lane. It runs independently of processing so two
	// busy lanes cannot postpone recovery until one of their jobs finishes.
	reaper sync.Once
	// The pass over the failed list runs once for the same reason, and beside
	// the reaper rather than inside it: they look at opposite ends of the queue
	// and neither should wait on the other.
	tidier sync.Once
}

func NewWorker(queue Queue, provider CatalogueProvider, logger zerolog.Logger, scanners ...LibraryScanner) *Worker {
	worker := &Worker{
		queue:             queue,
		provider:          provider,
		logger:            logger,
		pollInterval:      time.Second,
		leaseExpiredAfter: leaseExpiredAfter,
		heartbeatEvery:    heartbeatInterval,
		reapEvery:         reapInterval,
		tidyEvery:         tidyInterval,
		now:               time.Now,
	}
	if len(scanners) > 0 {
		worker.scanner = scanners[0]
	}
	return worker
}

// WithTransferTracker registers the tracker that follows download transfers.
func (worker *Worker) WithWitnessFingerprinter(
	fingerprinter WitnessFingerprinter,
) *Worker {
	worker.witnesses = fingerprinter
	return worker
}

func (worker *Worker) WithTransferTracker(transfers TransferTracker) *Worker {
	worker.transfers = transfers
	return worker
}

// WithPeerChallengeAnswerer registers the answerer that reads the private chat
// of peers holding transfers.
func (worker *Worker) WithPeerChallengeAnswerer(challenges PeerChallengeAnswerer) *Worker {
	worker.challenges = challenges
	return worker
}

func (worker *Worker) WithDownloadImporter(importer DownloadImporter) *Worker {
	worker.importer = importer
	return worker
}

// WithFileResolver registers the component that resolves the identity of
// independently acquired files.
// WithInboxCleaner registers what deletes the downloaded files nothing needs
// any more. Without it nothing is ever deleted from the download inbox.
func (worker *Worker) WithInboxCleaner(inbox InboxCleaner) *Worker {
	worker.inbox = inbox
	return worker
}

func (worker *Worker) WithFileResolver(resolver FileResolver) *Worker {
	worker.resolver = resolver
	return worker
}

// WithSourceSearcher registers the component that searches providers for the
// releases a bulk run covers.
func (worker *Worker) WithSourceSearcher(searcher SourceSearcher) *Worker {
	worker.searcher = searcher
	return worker
}

// WithAcquisitionSweeper registers the component that attempts the wanted
// recordings whose turn it is.
func (worker *Worker) WithAcquisitionSweeper(wants AcquisitionSweeper) *Worker {
	worker.wants = wants
	return worker
}

// WithStandingWants registers what carries out a standing "want what is
// missing" on a release whose tracklist has just arrived.
func (worker *Worker) WithStandingWants(standing StandingWanter) *Worker {
	worker.standing = standing
	return worker
}

// WithCoverArtSweeper registers the component that fills the picture cache.
// Without it a release simply has no cover.
func (worker *Worker) WithCoverArtSweeper(covers CoverArtSweeper) *Worker {
	worker.covers = covers
	return worker
}

// WithLoudnessSweeper registers the component that measures how loud the
// library's files are. Without it nothing is measured and no gain is written,
// and the collection goes on lurching between masterings exactly as before.
func (worker *Worker) WithLoudnessSweeper(loud LoudnessSweeper) *Worker {
	worker.loud = loud
	return worker
}

// WithFollowFeedSweeper registers the component that watches followed artists
// for new releases. Without it a follow still refreshes once, at the press, and
// nothing after that is noticed.
func (worker *Worker) WithFollowFeedSweeper(feed FollowFeedSweeper) *Worker {
	worker.feed = feed
	return worker
}

// QueueNotifier tells the user that work has stopped and is waiting on them.
//
// It is called after a pass rather than at the moment a copy is held, which is
// what makes one pass that held ten copies one message instead of ten. It
// reports nothing and can fail nothing: the question is already in the queue,
// and the pass that produced it is already finished.
type QueueNotifier interface {
	Check(context.Context)
}

// WithQueueNotifier registers what tells the user the review queue has a
// question.
func (worker *Worker) WithQueueNotifier(notifier QueueNotifier) *Worker {
	worker.notifier = notifier
	return worker
}

// tellAboutTheQueue is the one call site's worth of nil check, so the two places
// that make questions do not each carry it.
func (worker *Worker) tellAboutTheQueue(ctx context.Context) {
	if worker.notifier != nil {
		worker.notifier.Check(ctx)
	}
}

// LyricsSweeper walks the library writing .lrc sidecars, one page at a time,
// and says where it stopped.
type LyricsSweeper interface {
	Sweep(context.Context, lyrics.Position) (lyrics.Result, error)
	// ForFile words one file, for the moment it becomes a catalogue track's. It
	// reports nothing: a lyrics service that is down costs the song its words
	// and must cost the file nothing else.
	ForFile(context.Context, uuid.UUID)
}

// WithLyrics registers what writes the words of a song beside the music.
func (worker *Worker) WithLyrics(sweeper LyricsSweeper) *Worker {
	worker.lyrics = sweeper
	return worker
}

// ListensSyncer copies the account's listening history from ListenBrainz
// into the listens table. Sync says whether it stopped with history still
// unread, which is how the job knows to come straight back.
type ListensSyncer interface {
	Sync(context.Context) (listens.Result, error)
}

// WithListens registers what keeps the listening history current.
func (worker *Worker) WithListens(syncer ListensSyncer) *Worker {
	worker.listens = syncer
	return worker
}

// AnchorSweeper fetches the distributor's preview of the recordings that are
// wanted, and keeps the fingerprint of that audio on the want. Sweep reports
// whether wants were left over, which is how the job knows to come straight back
// rather than wait.
type AnchorSweeper interface {
	Sweep(context.Context) (bool, error)
}

// WithAnchorSweeper registers what fetches those previews.
func (worker *Worker) WithAnchorSweeper(sweeper AnchorSweeper) *Worker {
	worker.anchors = sweeper
	return worker
}

// SpectrumSweeper decodes a few seconds of the library's audio, a batch at a
// time, and writes down where the music in each file stops. Sweep reports
// whether files were left over, which is how the job knows to come straight back
// rather than wait.
//
// What it writes is quality and never identity: nothing that admits or refuses a
// file reads any of it.
type SpectrumSweeper interface {
	Sweep(context.Context) (bool, error)
}

// WithSpectrumSweeper registers what measures the library's audio.
func (worker *Worker) WithSpectrumSweeper(sweeper SpectrumSweeper) *Worker {
	worker.spectra = sweeper
	return worker
}

// UpgradeSweeper walks the library's files, a page at a time, and raises an
// acquisition want for each one whose stored quality falls under the floor.
// It decides nothing about what a file is — see internal/upgrade.
type UpgradeSweeper interface {
	Sweep(ctx context.Context, after uuid.UUID) (upgrade.Result, error)
}

// WithUpgradeSweeper registers what looks for files below the quality floor.
func (worker *Worker) WithUpgradeSweeper(sweeper UpgradeSweeper) *Worker {
	worker.upgrades = sweeper
	return worker
}

// WithRecommendationSweeper registers the component that refreshes the raw
// provider candidate snapshot. Without it no recommendation job can run.
func (worker *Worker) WithRecommendationSweeper(recommends RecommendationSweeper) *Worker {
	worker.recommends = recommends
	return worker
}

// WithWeeklyPlaylistRefresher registers the component that runs the weekly
// playlist. Without it no weekly job can run, and nothing is ever removed.
func (worker *Worker) WithWeeklyPlaylistRefresher(refresher WeeklyPlaylistRefresher) *Worker {
	worker.weekly = refresher
	return worker
}

// WithNewReleasesPlaylistRefresher registers the component that keeps the
// new-releases playlist. Without it the list is simply never made, and nothing
// else on the installation changes.
func (worker *Worker) WithNewReleasesPlaylistRefresher(refresher NewReleasesPlaylistRefresher) *Worker {
	worker.newReleases = refresher
	return worker
}

// WithLabelRefresher registers what reads the releases a record label
// published. Without it a followed label is never asked about, and the follow
// feed has nothing of the label's to want.
func (worker *Worker) WithLabelRefresher(labels LabelRefresher) *Worker {
	worker.labels = labels
	return worker
}

// WithPlaylistImporter registers the component that reconciles followed
// playlists against their source.
func (worker *Worker) WithPlaylistImporter(playlists PlaylistImporter) *Worker {
	worker.playlists = playlists
	return worker
}

// WithUploadImporter registers the component that takes staged uploads into the
// library. Without it an installation simply has no upload route.
func (worker *Worker) WithUploadImporter(uploads UploadImporter) *Worker {
	worker.uploads = uploads
	return worker
}

// WithSoundCloud registers the service an upload's address is named through.
// Without it an upload that carried an address lands unnamed, same as one
// that carried none.
func (worker *Worker) WithSoundCloud(soundCloud SoundCloudNamer) *Worker {
	worker.soundCloud = soundCloud
	return worker
}

// WithPlaybackNotifier registers the player told about music Schall has just
// written. Without it nothing is told and nothing is missing: the player still
// finds new music on its own schedule.
func (worker *Worker) WithPlaybackNotifier(playback PlaybackNotifier) *Worker {
	worker.playback = playback
	return worker
}

// WithPlaylistSyncer registers the component that joins the want lists to the
// player's playlists. Without it nothing is pushed and nothing is adopted,
// which is what an installation with no player has anyway.
func (worker *Worker) WithPlaylistSyncer(playerSync PlaylistSyncer) *Worker {
	worker.playerSync = playerSync
	return worker
}

// WithLayoutMover registers the component that applies a planned layout
// migration. Without it a run can be planned and read but never applied, which
// is the state the library was in before there was a mover at all.
func (worker *Worker) WithLayoutMover(layout LayoutMover) *Worker {
	worker.layout = layout
	return worker
}

// WithTagger registers what writes the catalogue's account of a file into the
// file. Without it the only tags Schall writes are the ones it writes at
// import, and the library from before that stays as it arrived.
func (worker *Worker) WithTagger(tagger LibraryTagger) *Worker {
	worker.tagger = tagger
	return worker
}

// WithTranscodeSweeper registers what shrinks proven files already in the
// library. Without it the transcoding setting only ever reaches music
// imported after it was turned on.
func (worker *Worker) WithTranscodeSweeper(shrink TranscodeSweeper) *Worker {
	worker.shrink = shrink
	return worker
}

// WithDuplicateSweeper registers what keeps one copy of each recording the
// library holds more than once. Without it every such recording stays a
// question for a person, however the setting reads.
func (worker *Worker) WithDuplicateSweeper(duplicates DuplicateSweeper) *Worker {
	worker.duplicates = duplicates
	return worker
}

// WithEvents registers the hub the worker announces finished work on.
func (worker *Worker) WithEvents(hub *events.Hub) *Worker {
	worker.notices = hub
	worker.resolutions = events.NewRationed(hub, events.TopicLibrary, resolutionNotice)
	return worker
}

// announce says a view is stale. It carries no state: the view refetches
// through the endpoints it already uses.
//
// Every publisher here is a job boundary — claimed, or settled one way or the
// other — because that is where a status the interface shows changes, and
// because it bounds how often a notice can arrive to how often a job can start
// and stop.
func (worker *Worker) announce(topic events.Topic) { worker.notices.Publish(topic) }

// Run works every kind of job. It is RunLane with no restriction.
func (worker *Worker) Run(ctx context.Context) { worker.RunLane(ctx, Lane{}) }

// RunLane works one lane's kinds until the context ends. Several lanes may share
// one Worker: what it was wired with is set before any of them starts and only
// read afterwards, one background reaper serves them all, and the queue hands
// the same claim to at most one of them.
func (worker *Worker) RunLane(ctx context.Context, lane Lane) {
	worker.recovery.Do(func() {
		if err := worker.queue.RecoverInterrupted(ctx); err != nil {
			worker.logger.Error().Err(err).Msg("recover interrupted jobs")
		}
	})
	worker.reaper.Do(func() {
		go worker.runReaper(ctx)
	})
	worker.tidier.Do(func() {
		go worker.runTidy(ctx)
	})

	for {
		job, err := worker.queue.Claim(ctx, lane)
		switch {
		case err == nil:
			worker.processClaim(ctx, job)
		case errors.Is(err, pgx.ErrNoRows):
			if !wait(ctx, worker.pollInterval) {
				return
			}
		case errors.Is(err, context.Canceled):
			return
		default:
			worker.logger.Error().Err(err).Msg("claim job")
			if !wait(ctx, worker.pollInterval) {
				return
			}
		}
	}
}

// runReaper puts back rows whose workers stopped renewing their leases. It runs
// apart from both claim loops so long work on both lanes cannot postpone it.
func (worker *Worker) runReaper(ctx context.Context) {
	for wait(ctx, worker.reapEvery) {
		worker.reapStalled(ctx)
	}
}

// reapStalled puts back the rows that are running with nobody running them.
//
// The recovery pass at startup only catches what a shutdown left behind, and a
// worker that stopped without one — killed outright, or unable to reach the
// database to settle the job it had — leaves a row that nothing puts back until
// somebody restarts the application. That row is worse than lost work: the
// partial unique indexes read it as live, so the transfer it was importing or
// the release it was refreshing cannot be queued again at all. Making the same
// pass on a clock is what lets a leak of any kind heal on its own.
//
// It is deliberately the recovery pass and not a second kind of settlement: a
// swept row arrives back in the queue in exactly the state a restart would have
// left it in, waiting again with the attempt it was claimed on still spent.
func (worker *Worker) reapStalled(ctx context.Context) {
	requeued, err := worker.queue.ReapStalled(ctx, worker.leaseExpiredAfter)
	if err != nil {
		// A shutting-down worker fails this on the way out, which is not worth an
		// error line: whatever is running is about to be left for the recovery
		// pass anyway.
		if ctx.Err() == nil {
			worker.logger.Error().Err(err).Msg("requeue jobs left running")
		}
		return
	}
	if requeued > 0 {
		worker.logger.Warn().
			Int64("jobs", requeued).
			Dur("lease_expired_for", worker.leaseExpiredAfter).
			Msg("put jobs left running with no worker on them back on the queue")
	}
}

// runTidy looks after the other end of the queue: the jobs that will not be
// tried again unless somebody asks.
func (worker *Worker) runTidy(ctx context.Context) {
	for wait(ctx, worker.tidyEvery) {
		worker.reviveSpent(ctx)
		worker.forgetSpent(ctx)
	}
}

// reviveSpent tries again, once and by itself, the jobs that were stopped by
// something that goes away on its own.
//
// A job that spent every attempt sits in the failed list until a person presses
// Retry. For most causes that is right — a release MusicBrainz does not know
// will not become known by being asked a fourth time — but for an outage it is
// exactly wrong: the service came back, the work is still worth doing, and the
// only thing offered was a button that says "try again". Schall presses that
// button itself, half an hour later, for the causes Classify can name as
// temporary: a server that could not be reached, a service that answered with a
// fault of its own, and a service that asked to be asked less often.
//
// Exactly once. The revived attempt is taken from beyond the job's allowance —
// it is claimed with its attempts already spent — so a job that fails again
// comes back with more attempts behind it than it was ever given, and this pass
// does not offer it a second free one. A person pressing Retry gives the whole
// ladder back, and with it one more of these.
//
// A failure Schall could not classify is left alone. It is the bucket that
// holds a wrong setting, an unreadable file, a bug: things a person has to see,
// and things that would be no different for having been run twice.
//
// A job whose work is already queued or running is left alone too. Some kinds
// are allowed one live copy at a time — the sweeps that schedule their own
// successor are the common case — and for those the failed row sits beside the
// successor that replaced it, so there is nothing to put back. The queue itself
// refuses that one, job by job, and the rest of the pass goes through; a pass
// that stopped at the first of them left the transfer imports it exists for in
// the failed list.
func (worker *Worker) reviveSpent(ctx context.Context) {
	failedBefore := worker.now().Add(-reviveSpentAfter)
	spent, err := worker.queue.SpentJobs(ctx, failedBefore, reviveBatch)
	if err != nil {
		if ctx.Err() == nil {
			worker.logger.Error().Err(err).Msg("read the jobs that spent their attempts")
		}
		return
	}
	revivable := make([]uuid.UUID, 0, len(spent))
	causes := make(map[string]int, len(spent))
	for _, job := range spent {
		failure := ClassifyMessage(job.Error)
		if !failure.Temporary() {
			continue
		}
		revivable = append(revivable, job.ID)
		causes[failure.Key]++
	}
	if len(revivable) == 0 {
		return
	}
	revived, skipped, err := worker.queue.ReviveSpentJobs(ctx, revivable)
	if err != nil {
		if ctx.Err() == nil {
			worker.logger.Error().Err(err).Msg("queue the jobs stopped by an outage again")
		}
		return
	}
	if revived > 0 {
		worker.logger.Info().
			Int64("jobs", revived).
			Int64("already_queued", skipped).
			Int("causes", len(causes)).
			Dur("failed_at_least", reviveSpentAfter).
			Msg("queued the jobs an outage had stopped again, one attempt each")
	}
}

// forgetSpent removes the failures that are old enough to be history rather
// than work. Nothing else ever removed one, so the failed list only grew and the
// count beside Settings could not return to nothing however well the queue was
// running.
func (worker *Worker) forgetSpent(ctx context.Context) {
	forgotten, err := worker.queue.ForgetSpentJobs(ctx, worker.now().Add(-spentJobRetention))
	if err != nil {
		if ctx.Err() == nil {
			worker.logger.Error().Err(err).Msg("forget the failures that are older than the window")
		}
		return
	}
	if forgotten > 0 {
		worker.logger.Info().
			Int64("jobs", forgotten).
			Dur("kept_for", spentJobRetention).
			Msg("forgot failures older than the window they are kept for")
	}
}

// processClaim keeps the claim's lease alive while its handler runs. Losing the
// exact claim cancels that handler: even if the work ignores cancellation and
// returns later, settlement is guarded by the same claim token.
func (worker *Worker) processClaim(ctx context.Context, job Job) {
	jobCtx, cancel := context.WithCancel(ctx)
	done := make(chan struct{})
	go func() {
		defer close(done)
		worker.heartbeat(jobCtx, cancel, job)
	}()

	worker.process(jobCtx, job)
	cancel()
	<-done
}

func (worker *Worker) heartbeat(ctx context.Context, cancel context.CancelFunc, job Job) {
	for wait(ctx, worker.heartbeatEvery) {
		owned, err := worker.queue.Heartbeat(ctx, job)
		if err != nil {
			if ctx.Err() == nil {
				worker.logger.Error().Err(err).Str("job_id", job.ID.String()).Msg("renew job lease")
				cancel()
			}
			return
		}
		if !owned {
			if ctx.Err() == nil {
				worker.logger.Warn().Str("job_id", job.ID.String()).Msg("job lease was lost")
				cancel()
			}
			return
		}
	}
}

func (worker *Worker) process(ctx context.Context, job Job) {
	switch job.Kind {
	case RefreshArtistMetadata:
		worker.processArtist(ctx, job)
	case RefreshAlbumMetadata:
		worker.processAlbum(ctx, job)
	case RefreshLabelMetadata:
		worker.processLabelRefresh(ctx, job)
	case ScanLibrary:
		worker.processLibraryScan(ctx, job)
	case PollDownloads:
		worker.processDownloadPoll(ctx, job)
	case AnswerPeerChallenges:
		worker.processPeerChallenges(ctx, job)
	case ImportDownload:
		worker.processDownloadImport(ctx, job)
	case ResolveLibraryFile:
		worker.processFileResolution(ctx, job)
	case SweepLibraryWitnesses:
		worker.processLibraryWitnessSweep(ctx, job)
	case FingerprintLibraryWitness:
		worker.processLibraryWitness(ctx, job)
	case IngestReleaseGroup:
		worker.processReleaseGroupIngest(ctx, job)
	case SearchAlbumSources:
		worker.processSourceSearch(ctx, job)
	case SweepAcquisitionTargets:
		worker.processAcquisitionSweep(ctx, job)
	case ImportPlaylist:
		worker.processPlaylistImport(ctx, job)
	case SweepCoverArt:
		worker.processCoverArtSweep(ctx, job)
	case SweepFollowFeed:
		worker.processFollowFeedSweep(ctx, job)
	case SweepLyrics:
		worker.processLyricsSweep(ctx, job)
	case SyncListens:
		worker.processListensSync(ctx, job)
	case SweepPreviewAnchors:
		worker.processAnchorSweep(ctx, job)
	case SweepGenres:
		worker.processGenreSweep(ctx, job)
	case SweepCopyFingerprints:
		worker.processCopyFingerprintSweep(ctx, job)
	case SweepLoudness:
		worker.processLoudnessSweep(ctx, job)
	case SweepAudioSpectrum:
		worker.processSpectrumSweep(ctx, job)
	case SweepTranscode:
		worker.processTranscodeSweep(ctx, job)
	case SweepDuplicates:
		worker.processDuplicateSweep(ctx, job)
	case SweepUpgradeCandidates:
		worker.processUpgradeSweep(ctx, job)
	case JudgeCopiesAgain:
		worker.processJudgeAgain(ctx, job)
	case JudgeCreditRefusals:
		worker.processJudgeCreditRefusals(ctx, job)
	case JudgeWantCopies:
		worker.processWantJudging(ctx, job)
	case SweepRecommendations:
		worker.processRecommendationSweep(ctx, job)
	case RefreshWeeklyPlaylist:
		worker.processWeeklyPlaylistRefresh(ctx, job)
	case RefreshNewReleasesPlaylist:
		worker.processNewReleasesPlaylistRefresh(ctx, job)
	case ImportUpload:
		worker.processUploadImport(ctx, job)
	case SyncNavidromePlaylists:
		worker.processNavidromePlaylistSync(ctx, job)
	case ApplyLibraryLayout:
		worker.processLayoutMoves(ctx, job)
	case TagLibrary:
		worker.processLibraryTagging(ctx, job)
	case TagLibraryFile:
		worker.processFileTagging(ctx, job)
	case NotifyPlayer:
		worker.processNotifyPlayer(ctx, job)
	case CleanInbox:
		worker.processInboxCleanup(ctx, job)
	default:
		worker.finishFailed(ctx, job, fmt.Errorf("unsupported job kind %q", job.Kind), false)
	}
}

// processCoverArtSweep fills the picture cache a batch at a time.
//
// A pass that filled its batch asks for the next one straight away, because there
// are more releases waiting and the pace is already set inside the sweep. A pass
// that did not comes back in a while, to picture whatever the catalogue has
// gained since. Either way it schedules its own replacement, so a catalogue that
// grows is pictured without anybody pressing anything.
func (worker *Worker) processCoverArtSweep(ctx context.Context, job Job) {
	if worker.covers == nil {
		worker.finishFailed(ctx, job, errors.New("cover art is not configured"), false)
		return
	}
	more, err := worker.covers.Sweep(ctx)
	if err != nil {
		worker.finishFailed(ctx, job, fmt.Errorf("sweep cover art: %w", err), true)
		if spent(job, err) {
			worker.queueCoverArtSweep(ctx, worker.now().Add(coverArtUnreachable))
		}
		return
	}
	if err := worker.queue.Complete(ctx, job); err != nil {
		worker.logger.Error().Err(err).Str("job_id", job.ID.String()).Msg("complete job")
		return
	}
	next := worker.now().Add(coverArtIdle)
	if more {
		next = worker.now()
	}
	worker.queueCoverArtSweep(ctx, next)
	worker.announce(events.TopicCatalogue)
}

// processLoudnessSweep measures how loud a batch of the library's files are.
//
// A pass that filled its batch asks for the next one straight away, because
// there is a backlog and nothing is gained by making it wait. A pass that did
// not comes back in a while, for whatever the library has gained since. Either
// way it schedules its own replacement, so music that arrives at any hour is
// measured without anybody pressing anything.
//
// Nothing here fails for a file. A file whose audio says nothing is recorded as
// asked and left empty inside the sweep, and an installation with no ffmpeg
// simply measures nothing — that is a library without replay gain, which is the
// library everybody had until now, and not a job to retry.
func (worker *Worker) processLoudnessSweep(ctx context.Context, job Job) {
	if worker.loud == nil {
		worker.finishFailed(ctx, job, errors.New("loudness measuring is not configured"), false)
		return
	}
	more, err := worker.loud.Sweep(ctx)
	if err != nil {
		worker.finishFailed(ctx, job, fmt.Errorf("sweep loudness: %w", err), true)
		if spent(job, err) {
			worker.queueLoudnessSweep(ctx, worker.now().Add(loudnessIdle))
		}
		return
	}
	if err := worker.queue.Complete(ctx, job); err != nil {
		worker.logger.Error().Err(err).Str("job_id", job.ID.String()).Msg("complete job")
		return
	}
	next := worker.now().Add(loudnessIdle)
	if more {
		next = worker.now()
	}
	worker.queueLoudnessSweep(ctx, next)
}

func (worker *Worker) queueLoudnessSweep(ctx context.Context, at time.Time) {
	if err := worker.queue.QueueLoudnessSweep(ctx, at); err != nil {
		worker.logger.Error().Err(err).Msg("queue loudness sweep")
	}
}

func (worker *Worker) queueCoverArtSweep(ctx context.Context, at time.Time) {
	if err := worker.queue.QueueCoverArtSweep(ctx, at); err != nil {
		worker.logger.Error().Err(err).Msg("queue cover art sweep")
	}
}

// processFollowFeedSweep asks about the followed artists whose catalogue has
// gone stale and wants what they have released since being followed.
//
// A pass that filled its batch of wants comes straight back, because there is
// more new music waiting and nothing is gained by making it wait six hours. A
// pass that did not returns on the idle schedule. Either way it queues its own
// replacement, so following an artist keeps meaning something without anybody
// pressing anything — which is the whole of what a follow buys.
func (worker *Worker) processFollowFeedSweep(ctx context.Context, job Job) {
	if worker.feed == nil {
		worker.finishFailed(ctx, job, errors.New("the follow feed is not configured"), false)
		return
	}
	more, err := worker.feed.Sweep(ctx)
	if err != nil {
		worker.finishFailed(ctx, job, fmt.Errorf("sweep follow feed: %w", err), true)
		if spent(job, err) {
			worker.queueFollowFeedSweep(ctx, worker.now().Add(followFeedIdle))
		}
		return
	}
	if err := worker.queue.Complete(ctx, job); err != nil {
		worker.logger.Error().Err(err).Str("job_id", job.ID.String()).Msg("complete job")
		return
	}
	next := worker.now().Add(followFeedIdle)
	if more {
		next = worker.now()
	}
	worker.queueFollowFeedSweep(ctx, next)
	// And the list that shows what following somebody has brought in. The pass
	// has just decided which releases are new, which is the one moment the
	// window it draws can have moved.
	worker.queueNewReleasesPlaylistRefresh(ctx)
	worker.announce(events.TopicAcquisitions)
}

func (worker *Worker) queueFollowFeedSweep(ctx context.Context, at time.Time) {
	if err := worker.queue.QueueFollowFeedSweep(ctx, at); err != nil {
		worker.logger.Error().Err(err).Msg("queue follow feed sweep")
	}
}

// processRecommendationSweep refreshes the provider facts without applying
// local suppression. A usable pass completes before it queues its successor;
// an exhausted failed pass still leaves one behind so an outage cannot silence
// recommendations permanently.
//
// A sweep is a chain of passes, and a pass that ends the chain early — because
// ListenBrainz or MusicBrainz stopped answering — keeps what the chain built,
// unpublished. Its successor therefore carries the position forward and finishes
// that chain rather than starting again: one slow request cost a chain 1006 of
// its 1298 records before this (issue #305).
func (worker *Worker) processRecommendationSweep(ctx context.Context, job Job) {
	if worker.recommends == nil {
		worker.finishFailed(ctx, job, errors.New("recommendation sweeping is not configured"), false)
		return
	}
	var position recommendations.SweepPosition
	if len(job.Payload) > 0 {
		if err := json.Unmarshal(job.Payload, &position); err != nil {
			worker.finishFailed(ctx, job, fmt.Errorf("invalid recommendation sweep payload: %w", err), false)
			return
		}
	}
	result, err := worker.recommends.SweepPage(ctx, position)
	if err != nil {
		permanent := errors.Is(err, recommendations.ErrDisabled) ||
			errors.Is(err, recommendations.ErrNotConfigured)
		worker.finishFailed(ctx, job, fmt.Errorf("sweep recommendations: %w", err), !permanent)
		if !permanent && spent(job, err) {
			worker.queueRecommendationSweep(ctx, worker.now().Add(recommendationIdle))
		}
		return
	}
	if err := worker.queue.Complete(ctx, job); err != nil {
		worker.logger.Error().Err(err).Str("job_id", job.ID.String()).Msg("complete job")
		return
	}
	worker.logger.Info().
		Str("job_id", job.ID.String()).
		Str("snapshot_id", result.Report.SnapshotID).
		Int("offered", result.Report.Offered).
		Int("asked", result.Report.Asked).
		Int("stored", result.Report.Stored).
		Str("status", result.Report.Status).
		Str("detail", result.Report.Detail).
		Bool("more", result.More).
		Bool("published", result.Report.Published).
		Bool("kept_published", result.Report.KeptPublished).
		Msg("recommendations swept")

	// A pass that was told of no records at all, and kept the published list
	// because of it, waits as long as a pass that reached nobody waits. Nobody can
	// tell the two ListenBrainz services being quiet from an account with nothing
	// to suggest, so the answer is put again once an outage has plausibly passed
	// rather than in a day's time (issue #317).
	next := worker.now().Add(recommendationIdle)
	switch {
	case result.More:
		next = worker.now()
	case result.Report.ProviderFailed, result.Report.KeptPublished:
		next = worker.now().Add(recommendationUnreachable)
	}
	if result.Resume {
		worker.queueRecommendationSweepContinuation(ctx, next, result.Position)
		return
	}
	worker.queueRecommendationSweep(ctx, next)
}

// processLyricsSweep walks one page of the library and writes the words it can
// find beside the music.
//
// The page it walks is carried in the payload, because nothing in the database
// records which files have been asked about — the .lrc file beside the music is
// that record. So each pass says where it stopped and its successor starts from
// there; a pass that reached the end of the library says so by starting the next
// one at the beginning.
//
// The pass never fails the job for a song. A song LRCLIB has no words for is an
// ordinary song, and only the service refusing to answer at all ends a pass
// early — and even then the position is kept, so nothing is walked twice.
func (worker *Worker) processLyricsSweep(ctx context.Context, job Job) {
	if worker.lyrics == nil {
		worker.finishFailed(ctx, job, errors.New("lyrics are not configured"), false)
		return
	}
	var from lyrics.Position
	if len(job.Payload) > 0 {
		if err := json.Unmarshal(job.Payload, &from); err != nil {
			worker.finishFailed(ctx, job, fmt.Errorf("invalid lyrics sweep payload: %w", err), false)
			return
		}
	}
	result, err := worker.lyrics.Sweep(ctx, from)
	if err != nil {
		worker.finishFailed(ctx, job, fmt.Errorf("sweep lyrics: %w", err), true)
		if spent(job, err) {
			worker.queueLyricsSweep(ctx, worker.now().Add(lyricsUnreachable), result.Next)
		}
		return
	}
	if err := worker.queue.Complete(ctx, job); err != nil {
		worker.logger.Error().Err(err).Str("job_id", job.ID.String()).Msg("complete job")
		return
	}
	if result.Written > 0 || result.Instrumental > 0 {
		worker.logger.Info().
			Int("looked", result.Looked).
			Int("written", result.Written).
			Int("instrumental", result.Instrumental).
			Msg("lyrics swept")
	}
	next := worker.now().Add(lyricsIdle)
	if result.More {
		next = worker.now()
	}
	worker.queueLyricsSweep(ctx, next, result.Next)
}

// processAnchorSweep fetches the distributor previews the wants are due, a batch
// at a time.
//
// A pass that filled its batch asks for the next one straight away, because
// there are more wants waiting and the pace between requests is already set
// inside the sweep. A pass that did not comes back in a while, for whatever the
// collection has been asked for since. Either way it schedules its own
// replacement, so a want created at any hour is anchored without anybody
// pressing anything.
func (worker *Worker) processAnchorSweep(ctx context.Context, job Job) {
	if worker.anchors == nil {
		worker.finishFailed(ctx, job, errors.New("preview anchors are not configured"), false)
		return
	}
	more, err := worker.anchors.Sweep(ctx)
	if err != nil {
		worker.finishFailed(ctx, job, fmt.Errorf("sweep preview anchors: %w", err), true)
		if spent(job, err) {
			worker.queueAnchorSweep(ctx, worker.now().Add(anchorUnreachable))
		}
		return
	}
	if err := worker.queue.Complete(ctx, job); err != nil {
		worker.logger.Error().Err(err).Str("job_id", job.ID.String()).Msg("complete job")
		return
	}
	next := worker.now().Add(anchorIdle)
	if more {
		next = worker.now()
	}
	worker.queueAnchorSweep(ctx, next)
}

// processSpectrumSweep measures what the audio in the library's files is, a
// batch at a time.
//
// A pass that filled its batch asks for the next one straight away, because
// there are more files waiting. A pass that did not comes back in a while, for
// whatever the library has gained or re-read since. Either way it schedules its
// own replacement, so a file indexed at any hour is measured without anybody
// pressing anything.
//
// Nothing waits on this. What it writes is shown to a person choosing between
// two copies of one recording, and is read by nothing that admits or refuses a
// file, so a pass that cannot run costs the library nothing it had.
func (worker *Worker) processSpectrumSweep(ctx context.Context, job Job) {
	if worker.spectra == nil {
		worker.finishFailed(ctx, job, errors.New("audio measurement is not configured"), false)
		return
	}
	more, err := worker.spectra.Sweep(ctx)
	if err != nil {
		// A missing decoder never reaches here: spectrum.Service absorbs it and
		// reports a clean pass with nothing done. What arrives as an error is a
		// fact about the database or the request, not about ffmpeg.
		worker.finishFailed(ctx, job, fmt.Errorf("sweep audio spectrum: %w", err), true)
		if spent(job, err) {
			worker.queueSpectrumSweep(ctx, worker.now().Add(spectrumIdle))
		}
		return
	}
	if err := worker.queue.Complete(ctx, job); err != nil {
		worker.logger.Error().Err(err).Str("job_id", job.ID.String()).Msg("complete job")
		return
	}
	next := worker.now().Add(spectrumIdle)
	if more {
		next = worker.now()
	}
	worker.queueSpectrumSweep(ctx, next)
}

// upgradeSweepPosition is what one upgrade sweep pass carries to its
// successor: the file id it stopped at, in the shape lyrics.Position carries a
// walk's place through the library.
type upgradeSweepPosition struct {
	After uuid.UUID `json:"after"`
}

// processUpgradeSweep reads one page of the library's files against the
// stored quality floor and raises a want for each one under it.
//
// A pass that filled its page asks for the next one straight away; one that
// did not comes back in a while, for whatever a scan has added or the floor
// has changed since. Either way it schedules its own replacement, so a file
// imported before the floor was set is found without anybody pressing
// anything — which is the whole of what this feature does.
func (worker *Worker) processUpgradeSweep(ctx context.Context, job Job) {
	if worker.upgrades == nil {
		worker.finishFailed(ctx, job, errors.New("the upgrade sweep is not configured"), false)
		return
	}
	var position upgradeSweepPosition
	if len(job.Payload) > 0 {
		if err := json.Unmarshal(job.Payload, &position); err != nil {
			worker.finishFailed(ctx, job, fmt.Errorf("invalid upgrade sweep payload: %w", err), false)
			return
		}
	}
	result, err := worker.upgrades.Sweep(ctx, position.After)
	if err != nil {
		worker.finishFailed(ctx, job, fmt.Errorf("sweep upgrade candidates: %w", err), true)
		if spent(job, err) {
			worker.queueUpgradeSweep(ctx, worker.now().Add(upgradeIdle), upgradeSweepPosition{})
		}
		return
	}
	if err := worker.queue.Complete(ctx, job); err != nil {
		worker.logger.Error().Err(err).Str("job_id", job.ID.String()).Msg("complete job")
		return
	}
	if result.Raised > 0 {
		worker.logger.Info().
			Int("looked", result.Looked).
			Int("raised", result.Raised).
			Msg("wants raised for files below the quality floor")
	}
	next := worker.now().Add(upgradeIdle)
	from := upgradeSweepPosition{}
	if result.More {
		next = worker.now()
		from = upgradeSweepPosition{After: result.Next}
	}
	worker.queueUpgradeSweep(ctx, next, from)
}

func (worker *Worker) queueUpgradeSweep(ctx context.Context, at time.Time, from upgradeSweepPosition) {
	payload, err := json.Marshal(from)
	if err != nil {
		worker.logger.Error().Err(err).Msg("write the upgrade sweep position")
		return
	}
	if err := worker.queue.QueueUpgradeSweep(ctx, at, payload); err != nil {
		worker.logger.Error().Err(err).Msg("queue upgrade sweep")
	}
}

// processJudgeAgain puts the copies already decided about through validation a
// second time.
//
// It queues no replacement, and that is the whole difference between it and
// every sweep above. A sweep exists because the world keeps changing; this
// exists because Schall's own rules changed once, and running it on a timer
// would re-decide copies nothing has changed for. Somebody asks for it.
// processInboxCleanup walks the download inbox once and deletes what nothing
// needs any more.
//
// It gets one attempt. The pass writes what went wrong onto its own row, so a
// failure is a sentence on the settings screen rather than a job somebody has to
// find, and asking again is one press.
func (worker *Worker) processInboxCleanup(ctx context.Context, job Job) {
	if worker.inbox == nil {
		worker.finishFailed(ctx, job, errors.New("cleaning the download inbox is not configured"), false)
		return
	}
	var named struct {
		CleanupID uuid.UUID `json:"cleanup_id"`
	}
	if err := json.Unmarshal(job.Payload, &named); err != nil {
		worker.finishFailed(ctx, job, fmt.Errorf("read which pass over the inbox to run: %w", err), false)
		return
	}
	if named.CleanupID == uuid.Nil {
		worker.finishFailed(ctx, job, errors.New("this job names no pass over the inbox"), false)
		return
	}
	if err := worker.inbox.Clean(ctx, named.CleanupID); err != nil {
		worker.finishFailed(ctx, job, fmt.Errorf("clean the download inbox: %w", err), false)
		worker.announce(events.TopicDownloads)
		return
	}
	if err := worker.queue.Complete(ctx, job); err != nil {
		worker.logger.Error().Err(err).Str("job_id", job.ID.String()).Msg("complete job")
	}
	worker.announce(events.TopicDownloads)
}

func (worker *Worker) processJudgeAgain(ctx context.Context, job Job) {
	if worker.importer == nil {
		worker.finishFailed(ctx, job, errors.New("download importer is not configured"), false)
		return
	}
	if err := worker.importer.JudgeAgain(ctx); err != nil {
		worker.finishFailed(ctx, job, fmt.Errorf("judge copies again: %w", err), true)
		return
	}
	if err := worker.queue.Complete(ctx, job); err != nil {
		worker.logger.Error().Err(err).Str("job_id", job.ID.String()).Msg("complete job")
	}
}

// processJudgeCreditRefusals puts the copies refused on the artist tag alone
// back through validation. It queues no replacement, for the same reason
// processJudgeAgain does not.
func (worker *Worker) processJudgeCreditRefusals(ctx context.Context, job Job) {
	if worker.importer == nil {
		worker.finishFailed(ctx, job, errors.New("download importer is not configured"), false)
		return
	}
	if err := worker.importer.JudgeCreditRefusalsAgain(ctx); err != nil {
		worker.finishFailed(ctx, job, fmt.Errorf("judge credit refusals again: %w", err), true)
		return
	}
	if err := worker.queue.Complete(ctx, job); err != nil {
		worker.logger.Error().Err(err).Str("job_id", job.ID.String()).Msg("complete job")
	}
}

// processWantJudging measures one newly anchored want's held copies against the
// anchor that just landed.
//
// It queues no replacement, like the passes above. This one is caused by an
// anchor arriving rather than asked for by a person, and an anchor arrives once.
func (worker *Worker) processWantJudging(ctx context.Context, job Job) {
	if worker.importer == nil {
		worker.finishFailed(ctx, job, errors.New("download importer is not configured"), false)
		return
	}
	var named struct {
		AcquisitionTargetID uuid.UUID `json:"acquisition_target_id"`
	}
	if err := json.Unmarshal(job.Payload, &named); err != nil {
		worker.finishFailed(ctx, job, fmt.Errorf("read which want to judge: %w", err), false)
		return
	}
	if named.AcquisitionTargetID == uuid.Nil {
		worker.finishFailed(ctx, job, errors.New("this job names no want"), false)
		return
	}
	if err := worker.importer.JudgeWantCopies(ctx, named.AcquisitionTargetID); err != nil {
		worker.finishFailed(ctx, job,
			fmt.Errorf("judge the copies of want %s: %w", named.AcquisitionTargetID, err), true)
		return
	}
	if err := worker.queue.Complete(ctx, job); err != nil {
		worker.logger.Error().Err(err).Str("job_id", job.ID.String()).Msg("complete job")
		return
	}
	payload, err := worker.queue.JobPayload(ctx, job.ID)
	if err != nil {
		worker.logger.Error().Err(err).Str("job_id", job.ID.String()).Msg("read completed want judging job")
		return
	}
	var retry struct {
		After *time.Time `json:"rejudge_after"`
	}
	if err := json.Unmarshal(payload, &retry); err == nil && retry.After != nil {
		if err := worker.queue.QueueWantCopyJudging(ctx, named.AcquisitionTargetID, *retry.After); err != nil {
			worker.logger.Error().Err(err).Str("job_id", job.ID.String()).Msg("queue another want judging pass")
		}
	}
}

// processGenreSweep fills in the genres of a catalogue that predates them.
//
// It asks MusicBrainz nothing. It finds the artists whose community genres
// nobody has asked for — their own, or one of their release groups' — and
// queues the ordinary artist refresh, which visits the artist and browses their
// release groups and now reads the votes off responses it was fetching anyway.
// So the backfill costs no request that pressing Refresh would not have made,
// and it runs at the refresh's pace rather than at its own.
//
// A pass that queued a batch comes back after a pause rather than at once,
// because those refreshes have to run before the artists behind them stop
// answering to this question. A pass with nothing left comes back in half a day,
// for whatever entered the catalogue since.
func (worker *Worker) processGenreSweep(ctx context.Context, job Job) {
	defer worker.announce(events.TopicCatalogue)

	result, err := worker.queue.BackfillGenres(ctx, genreBatch)
	if err != nil {
		worker.finishFailed(ctx, job, fmt.Errorf("backfill genres: %w", err), true)
		if spent(job, err) {
			worker.queueGenreSweep(ctx, worker.now().Add(genresIdle))
		}
		return
	}
	if err := worker.queue.Complete(ctx, job); err != nil {
		worker.logger.Error().Err(err).Str("job_id", job.ID.String()).Msg("complete job")
		return
	}
	if result.QueuedCount > 0 {
		worker.logger.Info().
			Int64("waiting", result.WaitingCount).
			Int64("queued", result.QueuedCount).
			Msg("artists queued for their genres")
	}
	next := worker.now().Add(genresIdle)
	if result.WaitingCount > 0 {
		next = worker.now().Add(genresPace)
	}
	worker.queueGenreSweep(ctx, next)
}

func (worker *Worker) queueGenreSweep(ctx context.Context, at time.Time) {
	if err := worker.queue.QueueGenreSweep(ctx, at); err != nil {
		worker.logger.Error().Err(err).Msg("queue genre sweep")
	}
}

// processCopyFingerprintSweep measures the audio of the held copies that have
// never been measured, a batch at a time.
//
// A held copy is one file a peer sent for a wanted recording that nothing could
// decide about, kept as a question for a person. The measurement is what lets
// the review queue show five copies of one master as one row instead of five,
// and it admits nothing and refuses nothing.
//
// A pass that filled its batch asks for the next one straight away; one that did
// not comes back in a while, for whatever has been held since. Either way it
// schedules its own replacement, so the backfill finishes without anybody
// pressing anything.
const libraryWitnessBatch = 20

func (worker *Worker) processLibraryWitnessSweep(ctx context.Context, job Job) {
	queued, err := worker.queue.QueueLibraryWitnessFingerprintJobs(ctx, libraryWitnessBatch)
	if err != nil {
		worker.finishFailed(ctx, job, fmt.Errorf("queue library witness fingerprints: %w", err), true)
		return
	}
	woken, err := worker.queue.CompleteLibraryWitnessSweep(ctx, job)
	if err != nil {
		worker.logger.Error().Err(err).Str("job_id", job.ID.String()).Msg("complete job")
		return
	}
	if woken {
		worker.logger.Info().Str("job_id", job.ID.String()).Msg("a wake landed while a library witness sweep was running; another was queued")
	}
	if queued == libraryWitnessBatch && !woken {
		worker.queueLibraryWitnessSweep(ctx, worker.now())
	}
}

func (worker *Worker) processLibraryWitness(ctx context.Context, job Job) {
	if worker.witnesses == nil {
		worker.finishFailed(ctx, job, errors.New("library witness fingerprinting is not configured"), false)
		return
	}
	var payload struct {
		FileID uuid.UUID `json:"fileId"`
	}
	if err := json.Unmarshal(job.Payload, &payload); err != nil || payload.FileID == uuid.Nil {
		worker.finishFailed(ctx, job, errors.New("invalid library witness fingerprint payload"), false)
		return
	}
	if err := worker.witnesses.Fingerprint(ctx, payload.FileID); err != nil {
		worker.finishFailed(ctx, job, fmt.Errorf("fingerprint library witness %s: %w", payload.FileID, err), true)
		return
	}
	if err := worker.queue.Complete(ctx, job); err != nil {
		worker.logger.Error().Err(err).Str("job_id", job.ID.String()).Msg("complete job")
		return
	}
	worker.queueLibraryWitnessSweep(ctx, worker.now())
}

func (worker *Worker) queueLibraryWitnessSweep(ctx context.Context, at time.Time) {
	if err := worker.queue.QueueLibraryWitnessFingerprintSweep(ctx, at); err != nil {
		worker.logger.Error().Err(err).Msg("queue library witness fingerprint sweep")
	}
}

func (worker *Worker) processCopyFingerprintSweep(ctx context.Context, job Job) {
	if worker.importer == nil {
		worker.finishFailed(ctx, job, errors.New("the download importer is not configured"), false)
		return
	}
	more, err := worker.importer.FingerprintHeldCopies(ctx)
	if err != nil {
		worker.finishFailed(ctx, job, fmt.Errorf("fingerprint held copies: %w", err), true)
		if spent(job, err) {
			worker.queueCopyFingerprintSweep(ctx, worker.now().Add(copyFingerprintUnreadable))
		}
		return
	}
	if err := worker.queue.Complete(ctx, job); err != nil {
		worker.logger.Error().Err(err).Str("job_id", job.ID.String()).Msg("complete job")
		return
	}
	next := worker.now().Add(copyFingerprintIdle)
	if more {
		next = worker.now()
	}
	worker.queueCopyFingerprintSweep(ctx, next)
}

func (worker *Worker) queueCopyFingerprintSweep(ctx context.Context, at time.Time) {
	if err := worker.queue.QueueCopyFingerprintSweep(ctx, at); err != nil {
		worker.logger.Error().Err(err).Msg("queue copy fingerprint sweep")
	}
}

func (worker *Worker) queueAnchorSweep(ctx context.Context, at time.Time) {
	if err := worker.queue.QueuePreviewAnchorSweep(ctx, at); err != nil {
		worker.logger.Error().Err(err).Msg("queue preview anchor sweep")
	}
}

func (worker *Worker) queueSpectrumSweep(ctx context.Context, at time.Time) {
	if err := worker.queue.QueueSpectrumSweep(ctx, at); err != nil {
		worker.logger.Error().Err(err).Msg("queue audio spectrum sweep")
	}
}

func (worker *Worker) queueLyricsSweep(ctx context.Context, at time.Time, from lyrics.Position) {
	payload, err := json.Marshal(from)
	if err != nil {
		worker.logger.Error().Err(err).Msg("write the lyrics sweep position")
		return
	}
	if err := worker.queue.QueueLyricsSweep(ctx, at, payload); err != nil {
		worker.logger.Error().Err(err).Msg("queue lyrics sweep")
	}
}

// processListensSync copies what ListenBrainz holds that the table does not,
// and asks for the next pass: at once when history is still unread, in a
// quarter of an hour otherwise. An account that is not configured or is
// switched off ends the job for good; startup queues another when it is on.
func (worker *Worker) processListensSync(ctx context.Context, job Job) {
	if worker.listens == nil {
		worker.finishFailed(ctx, job, errors.New("listen syncing is not configured"), false)
		return
	}
	result, err := worker.listens.Sync(ctx)
	if errors.Is(err, listens.ErrDisabled) || errors.Is(err, listens.ErrNotConfigured) {
		// Somebody switched the account off since this was queued. That is
		// not a failure to record; the job ends and asks for no successor,
		// and startup queues one again when the account is on.
		if err := worker.queue.Complete(ctx, job); err != nil {
			worker.logger.Error().Err(err).Str("job_id", job.ID.String()).Msg("complete job")
		}
		return
	}
	if err != nil {
		worker.finishFailed(ctx, job, fmt.Errorf("sync listens: %w", err), true)
		if spent(job, err) {
			worker.queueListensSync(ctx, worker.now().Add(listensUnreachable))
		}
		return
	}
	if err := worker.queue.Complete(ctx, job); err != nil {
		worker.logger.Error().Err(err).Str("job_id", job.ID.String()).Msg("complete job")
		return
	}
	worker.logger.Info().
		Str("job_id", job.ID.String()).
		Int("fetched", result.Fetched).
		Int64("stored", result.Stored).
		Bool("more", result.More).
		Msg("listens synced")
	next := worker.now().Add(listensIdle)
	if result.More {
		next = worker.now()
	}
	worker.queueListensSync(ctx, next)
}

func (worker *Worker) queueListensSync(ctx context.Context, at time.Time) {
	if err := worker.queue.QueueListensSync(ctx, at); err != nil {
		worker.logger.Error().Err(err).Msg("queue listens sync")
	}
}

// processWeeklyPlaylistRefresh runs one weekly pass and asks for the next one.
//
// The pass itself never fails the job. A week when Soulseek was slow or the
// player did not answer is an ordinary week: the run row says what it could not
// do and the next one is queued all the same. Only a failure that leaves the
// account of the run untrustworthy — the row could not be opened or closed —
// arrives here as an error, and even then the next refresh is queued, because a
// feature that stops asking is a feature that silently switches itself off.
func (worker *Worker) processWeeklyPlaylistRefresh(ctx context.Context, job Job) {
	if worker.weekly == nil {
		worker.finishFailed(ctx, job, errors.New("the weekly playlist is not configured"), false)
		return
	}
	result, err := worker.weekly.Refresh(ctx)
	if err != nil {
		worker.finishFailed(ctx, job, fmt.Errorf("refresh weekly playlist: %w", err), false)
		worker.queueWeeklyPlaylistRefresh(ctx, worker.now().Add(weeklyIdle))
		return
	}
	if err := worker.queue.Complete(ctx, job); err != nil {
		worker.logger.Error().Err(err).Str("job_id", job.ID.String()).Msg("complete job")
		return
	}
	if result.Skipped {
		worker.logger.Debug().Str("job_id", job.ID.String()).
			Str("reason", result.Reason).Msg("weekly playlist refresh skipped")
	}
	worker.queueWeeklyPlaylistRefresh(ctx, worker.now().Add(weeklyIdle))
}

// processNewReleasesPlaylistRefresh brings the new-releases playlist up to date
// with what the library owns.
//
// It schedules no successor. Everything this pass reads changes at two moments
// and no others: the follow feed finding a release, and a scan bringing the
// files in — and both of those ask for a refresh themselves. A pass on a timer
// would only re-read the same rows on a day nothing had happened.
//
// A pass that fails is not retried either. It removes nothing that cannot be
// worked out again, and the next feed sweep or scan asks for another one within
// hours.
func (worker *Worker) processNewReleasesPlaylistRefresh(ctx context.Context, job Job) {
	if worker.newReleases == nil {
		worker.finishFailed(ctx, job, errors.New("the new-releases playlist is not configured"), false)
		return
	}
	result, err := worker.newReleases.Refresh(ctx)
	if err != nil {
		worker.finishFailed(ctx, job, fmt.Errorf("refresh new releases playlist: %w", err), false)
		return
	}
	if err := worker.queue.Complete(ctx, job); err != nil {
		worker.logger.Error().Err(err).Str("job_id", job.ID.String()).Msg("complete job")
		return
	}
	if result.Skipped {
		worker.logger.Debug().Str("job_id", job.ID.String()).
			Str("reason", result.Reason).Msg("new releases playlist refresh skipped")
		return
	}
	worker.announce(events.TopicPlaylists)
}

// queueNewReleasesPlaylistRefresh asks for a refresh of the new-releases
// playlist. It is a courtesy from whatever just changed what the list would
// hold, so a queue that refuses it is logged and never fails the job that
// noticed.
func (worker *Worker) queueNewReleasesPlaylistRefresh(ctx context.Context) {
	if worker.newReleases == nil {
		return
	}
	if err := worker.queue.QueueNewReleasesPlaylistRefresh(ctx, worker.now()); err != nil {
		worker.logger.Error().Err(err).Msg("queue new releases playlist refresh")
	}
}

func (worker *Worker) queueWeeklyPlaylistRefresh(ctx context.Context, at time.Time) {
	if err := worker.queue.QueueWeeklyPlaylistRefresh(ctx, at); err != nil {
		worker.logger.Error().Err(err).Msg("queue weekly playlist refresh")
	}
}

func (worker *Worker) queueRecommendationSweep(ctx context.Context, at time.Time) {
	if err := worker.queue.QueueRecommendationSweep(ctx, at); err != nil {
		worker.logger.Error().Err(err).Msg("queue recommendation sweep")
	}
}

func (worker *Worker) queueRecommendationSweepContinuation(
	ctx context.Context, at time.Time, position recommendations.SweepPosition,
) {
	payload, err := json.Marshal(position)
	if err != nil {
		worker.logger.Error().Err(err).Msg("encode recommendation sweep continuation")
		return
	}
	if err := worker.queue.QueueRecommendationSweepContinuation(ctx, at, payload); err != nil {
		worker.logger.Error().Err(err).Msg("queue recommendation sweep continuation")
	}
}

func (worker *Worker) processPlaylistImport(ctx context.Context, job Job) {
	if worker.playlists == nil {
		worker.finishFailed(ctx, job, errors.New("playlist importer is not configured"), false)
		return
	}
	var payload struct {
		PlaylistID uuid.UUID `json:"playlistId"`
	}
	if err := json.Unmarshal(job.Payload, &payload); err != nil || payload.PlaylistID == uuid.Nil {
		worker.finishFailed(ctx, job, errors.New("invalid playlist import payload"), false)
		return
	}
	defer worker.announce(events.TopicPlaylists)

	if err := worker.playlists.Import(ctx, payload.PlaylistID); err != nil {
		// Missing credentials, a disconnected account, or an unfollowed list
		// cannot be retried into existence; a provider that stumbled can be
		// asked again.
		retryable := !errors.Is(err, pgx.ErrNoRows) &&
			!errors.Is(err, playlists.ErrNotConfigured) &&
			!errors.Is(err, playlists.ErrNotConnected) &&
			job.Attempts < job.MaxAttempts
		worker.finishFailed(ctx, job, fmt.Errorf("import playlist %s: %w", payload.PlaylistID, err), retryable)
		return
	}
	if err := worker.queue.Complete(ctx, job); err != nil {
		worker.logger.Error().Err(err).Str("job_id", job.ID.String()).Msg("complete job")
	}
}

// processUploadImport validates a staged upload and takes it into the library.
//
// Only the filesystem and the database are worth a second attempt. Everything
// the upload itself answers — a file that is not audio, a folder that is no
// longer staged — would answer the same way three times over, and spending the
// attempts on it would only delay the reason reaching the person who uploaded
// it. The staging folder is left alone either way, so a refused upload can be
// looked at and discarded rather than having vanished with its explanation.
func (worker *Worker) processUploadImport(ctx context.Context, job Job) {
	// Announced when the job stops running rather than when it is claimed: what
	// the uploads view shows is the outcome, and a job that has been picked up
	// has not produced one yet.
	defer worker.announce(events.TopicLibrary)

	if worker.uploads == nil {
		worker.finishFailed(ctx, job, errors.New("uploading is not configured"), false)
		return
	}
	var payload struct {
		UploadID uuid.UUID        `json:"uploadId"`
		Naming   *db.UploadNaming `json:"naming"`
	}
	if err := json.Unmarshal(job.Payload, &payload); err != nil || payload.UploadID == uuid.Nil {
		worker.finishFailed(ctx, job, errors.New("invalid upload import payload"), false)
		return
	}
	landed, err := worker.uploads.Import(ctx, payload.UploadID)
	if err != nil {
		worker.finishFailed(ctx, job,
			fmt.Errorf("import upload %s: %w", payload.UploadID, err), !uploads.Refused(err))
		return
	}
	// The import already succeeded — the file is in the library either way, so
	// nothing here can fail the job. A naming taken off the address is worth
	// having; it is not worth losing the file over.
	scanned := worker.scanUploadedFiles(ctx, payload.UploadID, landed)
	if scanned {
		worker.nameFromSoundCloud(ctx, payload.UploadID, landed, payload.Naming)
	}
	if err := worker.queue.Complete(ctx, job); err != nil {
		worker.logger.Error().Err(err).Str("job_id", job.ID.String()).Msg("complete job")
		return
	}
	// The library holds a release it did not before.
	worker.queueNotifyPlayer(ctx)
}

// scanUploadedFiles creates the library rows and marks the paths for the
// identity resolver. A scan or marker failure leaves the imported bytes in the
// library and must not turn a completed upload into a failed job.
func (worker *Worker) scanUploadedFiles(
	ctx context.Context, uploadID uuid.UUID, landed []string,
) bool {
	if worker.scanner == nil {
		worker.logger.Warn().Str("upload_id", uploadID.String()).
			Msg("an upload landed but the library scanner is not configured")
		return false
	}
	if _, err := worker.scanner.Scan(ctx); err != nil {
		worker.logger.Warn().Err(err).Str("upload_id", uploadID.String()).
			Msg("scan the library to find the files an upload just landed")
		return false
	}
	if err := worker.queue.MarkUploadedFiles(ctx, uploadID, landed); err != nil {
		worker.logger.Warn().Err(err).Str("upload_id", uploadID.String()).
			Msg("mark the files an upload just landed")
	}
	return true
}

// nameFromSoundCloud records the address a one-file upload arrived with,
// against the file it named, once that file has a row to write against.
//
// The row does not exist yet when Import returns: a scan is what turns a
// path into a library_files row, and CompleteUploadImport only queued one.
// scanUploadedFiles has already run that scan by the time this is called, so
// the file it names has a row to write against.
func (worker *Worker) nameFromSoundCloud(
	ctx context.Context, uploadID uuid.UUID, landed []string, naming *db.UploadNaming,
) {
	if naming == nil {
		return
	}
	log := worker.logger.With().Str("upload_id", uploadID.String()).Logger()
	if len(landed) != 1 {
		// uploadNaming already refused to queue a naming for more than one file;
		// this is the belt the suspenders sit beside, for a payload written by
		// anything else.
		log.Warn().Int("files", len(landed)).
			Msg("a SoundCloud address named more than one file; it names none of them")
		return
	}
	if worker.soundCloud == nil {
		log.Warn().Msg("a SoundCloud address arrived but SoundCloud is not configured; the file lands unnamed")
		return
	}
	confirmed := soundcloud.Naming{Artist: naming.Artist, Title: naming.Title, Remixer: naming.Remixer}
	_, err := worker.soundCloud.AdoptAtPath(ctx, landed[0], naming.URL, confirmed)
	switch {
	case err == nil:
		log.Info().Str("path", landed[0]).Msg("named the upload from its SoundCloud address")
	case errors.Is(err, soundcloud.ErrFileAlreadyMatched):
		// A scan can find a catalogue match in the gap between the upload
		// landing and this naming being asked for. Adopt refuses rather than
		// overwrite a decision a person has not seen (see ErrFileAlreadyMatched);
		// the file keeps that match and is not named from SoundCloud.
		log.Info().Str("path", landed[0]).
			Msg("the uploaded file already matched a catalogue recording; its SoundCloud address was not written")
	default:
		log.Warn().Err(err).Str("path", landed[0]).
			Msg("name the upload from its SoundCloud address; it lands unnamed")
	}
}

// processNavidromePlaylistSync brings one want list and the player's playlist
// into agreement, or — with no playlist named — looks at the whole player and
// asks for a pass over every list.
//
// Nothing else depends on this job. A pass that failed pushed nothing and wrote
// no snapshot, so the next one makes the same comparison from the same state;
// the music is on the disc and in the want list either way, and the player goes
// on playing what it was last told. That is why a failure here is a warning in
// the log and never anything's problem but its own.
//
// A pass that could not pair anything is not retried. Every cause of it — real
// paths not enabled, a library the player has not scanned, a path map that does
// not describe the deployment — is fixed by a person or by a scan, and neither
// happens inside the retry ladder; spending the attempts on it would only
// repeat the same refusal three times.
func (worker *Worker) processNavidromePlaylistSync(ctx context.Context, job Job) {
	if worker.playerSync == nil {
		worker.finishFailed(ctx, job, errors.New("playlist sync is not configured"), false)
		return
	}
	var payload struct {
		PlaylistID uuid.UUID `json:"playlistId"`
	}
	if err := json.Unmarshal(job.Payload, &payload); err != nil {
		worker.finishFailed(ctx, job, errors.New("invalid navidrome sync payload"), false)
		return
	}
	// Announced when the pass has finished: what a playlist view shows is the
	// entries, and a pass that has been claimed has not changed one yet.
	defer worker.announce(events.TopicPlaylists)

	if payload.PlaylistID == uuid.Nil {
		result, err := worker.playerSync.Sweep(ctx)
		if err != nil {
			worker.finishFailed(ctx, job, fmt.Errorf("sweep navidrome playlists: %w", err),
				!errors.Is(err, navidrome.ErrNothingPaired))
			// A pass that has run out of attempts must still leave one behind
			// it, or the player stops being told anything until a restart. The
			// cause is usually a player that was down, and a player that was
			// down is exactly the case worth trying again later.
			if spent(job, err) {
				worker.queueNavidromePlaylistSweep(ctx, worker.now().Add(playerSyncIdle))
			}
			return
		}
		worker.logger.Info().
			Str("job_id", job.ID.String()).
			Bool("player_configured", result.Configured).
			Int("adopted", len(result.Adopted)).
			Int("skipped", result.Skipped).
			Int("queued", result.Queued).
			Int("songs_the_library_does_not_hold", len(result.UnknownAdditions)).
			Msg("navidrome playlists swept")
	} else {
		result, err := worker.playerSync.SyncPlaylist(ctx, payload.PlaylistID)
		if err != nil {
			worker.finishFailed(ctx, job,
				fmt.Errorf("sync navidrome playlist %s: %w", payload.PlaylistID, err),
				!errors.Is(err, navidrome.ErrNothingPaired))
			return
		}
		worker.logger.Info().
			Str("job_id", job.ID.String()).
			Str("playlist_id", payload.PlaylistID.String()).
			Bool("player_configured", result.Configured).
			Bool("created", result.Created).
			Bool("remote_deleted", result.RemoteDeleted).
			Int("pushed", result.Pushed).
			Int("removals", result.RemovalsActed).
			Int("breaks_repaired", result.BreaksRepaired).
			Int("breaks_left", len(result.BreaksLeft)).
			Int("additions_adopted", result.AdditionsAdopted).
			Int("unpaired", len(result.Unpaired)).
			// A count says something is wrong and nothing about what: the sample
			// and the reasons behind it are what tell a lagging player from a
			// search that never reaches the file.
			Func(result.LogPairing).
			Msg("navidrome playlist synced")
	}
	if err := worker.queue.Complete(ctx, job); err != nil {
		worker.logger.Error().Err(err).Str("job_id", job.ID.String()).Msg("complete job")
		return
	}
	// The sweep queues its own replacement; a pass over one list does not. The
	// sweep is what makes the player hear about music that arrived while
	// nobody was looking, and it is the only pass nothing else asks for.
	if payload.PlaylistID == uuid.Nil {
		worker.queueNavidromePlaylistSweep(ctx, worker.now().Add(playerSyncIdle))
	}
}

func (worker *Worker) queueNavidromePlaylistSweep(ctx context.Context, at time.Time) {
	if err := worker.queue.QueueNavidromePlaylistSweep(ctx, at); err != nil {
		worker.logger.Error().Err(err).Msg("queue navidrome playlist sweep")
	}
}

func (worker *Worker) processDownloadImport(ctx context.Context, job Job) {
	if worker.importer == nil {
		worker.finishFailed(ctx, job, errors.New("download importer is not configured"), false)
		return
	}
	var payload struct {
		RequestID uuid.UUID `json:"requestId"`
	}
	if err := json.Unmarshal(job.Payload, &payload); err != nil || payload.RequestID == uuid.Nil {
		worker.finishFailed(ctx, job, errors.New("invalid download import payload"), false)
		return
	}
	if err := worker.importer.Import(ctx, payload.RequestID); err != nil {
		// The import was already decided about, or the request is gone. Neither
		// is a fault of this job: the request carries its own reason, and the
		// person is offered the action that belongs to it. Failing here would
		// bury that reason under six attempts at a sentence about no rows.
		if errors.Is(err, db.ErrImportAlreadyDecided) || errors.Is(err, db.ErrDownloadRequestGone) {
			worker.logger.Info().Err(err).Str("job_id", job.ID.String()).
				Str("request_id", payload.RequestID.String()).
				Msg("nothing left to import for this download")
			if completeErr := worker.queue.Complete(ctx, job); completeErr != nil {
				worker.logger.Error().Err(completeErr).Str("job_id", job.ID.String()).
					Msg("complete job")
			}
			return
		}
		if spent(job, err) {
			if recordErr := worker.importer.Fail(ctx, payload.RequestID, truncateError(err)); recordErr != nil {
				worker.logger.Error().Err(recordErr).Str("request_id", payload.RequestID.String()).
					Msg("record exhausted download import")
			}
		}
		worker.finishFailed(ctx, job, fmt.Errorf("import download: %w", err), true)
		return
	}
	if err := worker.queue.Complete(ctx, job); err != nil {
		worker.logger.Error().Err(err).Str("job_id", job.ID.String()).Msg("complete job")
	}
	// An import that succeeded may still have stopped and asked: the whole-release
	// path pauses on a folder it cannot account for rather than importing part of
	// it. One message for the whole import, whatever it could not decide about.
	worker.tellAboutTheQueue(ctx)
	// And the player, which is reading the same directory. Over-telling it about
	// an import that admitted nothing costs a scan that finds nothing new; the
	// alternative is music that arrived overnight and stayed unannounced until
	// some other change happened to queue a scan.
	worker.queueNotifyPlayer(ctx)
}

// processFileResolution asks the providers what one file is. Only the provider
// being unreachable is a failure worth retrying; every answer, including the
// answer that no external identity exists, is recorded by the resolver itself.
func (worker *Worker) processFileResolution(ctx context.Context, job Job) {
	// Announced when the job stops running, whether it succeeded, failed, or is
	// going to be retried; not when it is claimed. One scan queues hundreds of
	// these, so they are rationed rather than sent one per file, and a file
	// that is briefly being asked about is not worth doubling that.
	defer worker.resolutions.Publish()

	if worker.resolver == nil {
		worker.finishFailed(ctx, job, errors.New("file resolution is not configured"), false)
		return
	}
	var payload struct {
		FileID uuid.UUID `json:"fileId"`
	}
	if err := json.Unmarshal(job.Payload, &payload); err != nil || payload.FileID == uuid.Nil {
		worker.finishFailed(ctx, job, errors.New("invalid file resolution payload"), false)
		return
	}
	if err := worker.resolver.Resolve(ctx, payload.FileID); err != nil {
		// AcoustID answering "asked too fast" is the provider asking for less,
		// not an answer about this file. The first scan of a new library queues
		// hundreds of these and AcoustID allows roughly three requests a
		// second, so a burst crosses the limit routinely — and the ordinary
		// backoff starts at five seconds, which is inside the throttled window
		// and collects the same refusal again.
		backoff := retryDelay(Classify(err), job.Attempts)
		limited := errors.Is(err, acoustid.ErrRateLimited)
		if limited {
			backoff = rateLimitBackoff
			worker.logger.Warn().Str("file_id", payload.FileID.String()).
				Dur("backoff", backoff).
				Msg("AcoustID is rate limiting; backing off")
		}
		// Nothing is written against the file for a throttle, even once the
		// attempts are spent. The file's identity panel would otherwise read
		// "AcoustID is rate limiting requests" as its stored resolution
		// failure — a fact about the afternoon recorded as if it were a fact
		// about the audio — and it would say that to a person who could do
		// nothing about it. Left unrecorded the file stays unasked, which the
		// next scan picks up (QueueFileResolutions takes the pending files),
		// and the job's own failed row still carries the reason for anybody
		// looking at the queue.
		//
		// The attempt is still spent. Handing it back would be the kinder rule
		// for one job and the wrong one for a queue: the lane claims one job at
		// a time, so hundreds of rate-limited resolutions that never spend an
		// attempt become a loop that asks AcoustID again for every file, for
		// ever, which is the burst that caused the throttle in the first place.
		if spent(job, err) && !limited {
			if recordErr := worker.resolver.Fail(ctx, payload.FileID, truncateError(err)); recordErr != nil {
				worker.logger.Error().Err(recordErr).Str("file_id", payload.FileID.String()).
					Msg("record exhausted file resolution")
			}
		}
		worker.finishFailedAfter(ctx, job,
			fmt.Errorf("resolve library file: %w", err), true, backoff)
		return
	}
	if err := worker.queue.Complete(ctx, job); err != nil {
		worker.logger.Error().Err(err).Str("job_id", job.ID.String()).Msg("complete job")
	}
}

// processSourceSearch searches the configured provider for one release of a
// bulk run. It is one job per release rather than one per run so that a peer
// nobody can reach costs that release its result and not the other eleven.
func (worker *Worker) processSourceSearch(ctx context.Context, job Job) {
	if worker.searcher == nil {
		worker.finishFailed(ctx, job, errors.New("source searching is not configured"), false)
		return
	}
	var payload struct {
		RunID   uuid.UUID `json:"runId"`
		AlbumID uuid.UUID `json:"albumId"`
	}
	if err := json.Unmarshal(job.Payload, &payload); err != nil ||
		payload.RunID == uuid.Nil || payload.AlbumID == uuid.Nil {
		worker.finishFailed(ctx, job, errors.New("invalid source search payload"), false)
		return
	}
	if err := worker.searcher.Search(ctx, payload.RunID, payload.AlbumID); err != nil {
		// A run stopped while this release was being searched ends here. The
		// job was running, so cancelling left it alone to finish; finishing is
		// what it has now done, and putting it back in the queue would search a
		// release after the run it belongs to was called off. Nothing is
		// withdrawn by stopping: a search that returns an error has recorded no
		// answer, and one that recorded an answer never reaches this branch.
		if worker.sourceSearchRunCancelled(ctx, payload.RunID) {
			// What cancelling took from this release is the attempts it had
			// left, so short of the last one 'cancelled' is the whole of why it
			// has no sources: it stopped being asked about. On the last attempt
			// there was nothing left to take — the release had been searched as
			// often as it ever would be — and what it has to show for it is the
			// provider's own refusal, which is worth more to somebody asking
			// why this release found nothing than the run's cancellation is.
			// Writing that down withdraws no answer and chases none: the job is
			// stopped either way, and stopped is what it stays.
			if spent(job, err) {
				worker.recordSpentSourceSearch(ctx, payload.RunID, payload.AlbumID, err)
			}
			worker.stopCancelledSourceSearch(ctx, job, err)
			return
		}
		// Rate limiting is the provider asking for less, not an answer about
		// this release. Retrying on the ordinary five-second backoff would only
		// spend the remaining attempts collecting the same refusal.
		backoff := retryDelay(Classify(err), job.Attempts)
		switch {
		case errors.Is(err, sources.ErrRateLimited):
			backoff = rateLimitBackoff
			worker.logger.Warn().Str("album_id", payload.AlbumID.String()).
				Dur("backoff", backoff).
				Msg("the download source is rate limiting; backing off")
		case errors.Is(err, sources.ErrProviderUnavailable):
			// The same wait for the same reason: nothing was asked, and asking
			// again in five seconds would only spend the run's attempts on a
			// provider that has not had time to become able to answer.
			backoff = rateLimitBackoff
			worker.logger.Warn().Err(err).Str("album_id", payload.AlbumID.String()).
				Dur("backoff", backoff).
				Msg("the download source cannot search; backing off")
		}
		if spent(job, err) {
			worker.recordSpentSourceSearch(ctx, payload.RunID, payload.AlbumID, err)
		}
		worker.finishFailedAfter(ctx, job,
			fmt.Errorf("search album sources: %w", err), true, backoff)
		return
	}
	if err := worker.queue.Complete(ctx, job); err != nil {
		worker.logger.Error().Err(err).Str("job_id", job.ID.String()).Msg("complete job")
	}
}

// sourceSearchRunCancelled reports whether the run this release belongs to was
// stopped. A queue that cannot answer counts as not cancelled: the ordinary
// retry is bounded by the job's attempts, and spending a release's search on a
// failed lookup would cost it its result for a reason of our own.
func (worker *Worker) sourceSearchRunCancelled(ctx context.Context, runID uuid.UUID) bool {
	cancelled, err := worker.queue.SourceSearchRunCancelled(ctx, runID)
	if err != nil {
		// A shutting-down worker fails this lookup on the way out, which is not
		// worth an error line: the job is left running for RecoverInterrupted.
		if ctx.Err() == nil {
			worker.logger.Error().Err(err).Str("run_id", runID.String()).
				Msg("could not tell whether the source search run was cancelled")
		}
		return false
	}
	return cancelled
}

// recordSpentSourceSearch settles the release of a search that will not be
// tried again, in the provider's own words. Exhaustion is recorded whatever the
// cause, including rate limiting: a release left unsearched must read as
// attempted rather than sit in the run looking as though nobody had got to it.
//
// The error is passed as it was raised rather than wrapped, so a sentinel like
// sources.ErrProviderUnavailable reaches the row saying what it says — slskd
// abandoned the search, or is not logged in to Soulseek — instead of a sentence
// about jobs that means nothing to somebody looking at one release.
func (worker *Worker) recordSpentSourceSearch(
	ctx context.Context, runID, albumID uuid.UUID, cause error,
) {
	if err := worker.searcher.Fail(ctx, runID, albumID, truncateError(cause)); err != nil {
		worker.logger.Error().Err(err).Str("album_id", albumID.String()).
			Msg("record exhausted source search")
	}
}

// stopCancelledSourceSearch ends the job of a release whose run was stopped
// under it. The job is cancelled rather than failed because nothing is to be
// queued again: the run was called off, and a retry would search a release
// nobody is waiting for. It says nothing about what the release itself reads
// as — that is settled before this, and a run that was cancelled is cancelled
// however one of its releases ended.
func (worker *Worker) stopCancelledSourceSearch(ctx context.Context, job Job, cause error) {
	if err := worker.queue.Cancel(ctx, job, truncateError(cause)); err != nil {
		worker.logger.Error().Err(err).Str("job_id", job.ID.String()).
			Msg("stop the search of a cancelled run")
		return
	}
	worker.logger.Info().Err(cause).Str("job_id", job.ID.String()).
		Msg("a failed search was left stopped: its run had been cancelled")
	worker.announce(events.TopicSourceSearches)
}

// processReleaseGroupIngest brings the release behind a proven file identity
// into the catalogue, under an artist held rather than followed.
//
// A release group MusicBrainz does not know is an answer, not a failure: the
// identity that named it was proven against the same provider, so an ID that
// has since been merged away is worth recording and not worth retrying.
func (worker *Worker) processReleaseGroupIngest(ctx context.Context, job Job) {
	// Announced when the job stops running rather than when it is claimed:
	// until the release group has been saved there is no artist or release for
	// a view to have got wrong.
	defer worker.announce(events.TopicCatalogue)

	var payload struct {
		ReleaseGroupID uuid.UUID `json:"releaseGroupId"`
	}
	if err := json.Unmarshal(job.Payload, &payload); err != nil || payload.ReleaseGroupID == uuid.Nil {
		worker.finishFailed(ctx, job, errors.New("invalid release group ingest payload"), false)
		return
	}
	detail, err := worker.provider.ReleaseGroup(ctx, payload.ReleaseGroupID)
	if errors.Is(err, musicbrainz.ErrNotFound) {
		worker.finishFailed(ctx, job, fmt.Errorf(
			"MusicBrainz does not know release group %s", payload.ReleaseGroupID), false)
		return
	}
	if err != nil {
		worker.finishFailed(ctx, job, fmt.Errorf("fetch release group: %w", err), true)
		return
	}
	albumID, err := worker.queue.SaveCatalogueReleaseGroup(ctx, detail)
	if err != nil {
		worker.finishFailed(ctx, job, fmt.Errorf("save release group: %w", err), true)
		return
	}
	if err := worker.queue.Complete(ctx, job); err != nil {
		worker.logger.Error().Err(err).Str("job_id", job.ID.String()).Msg("complete job")
		return
	}
	worker.logger.Info().
		Str("job_id", job.ID.String()).
		Str("album_id", albumID.String()).
		Str("release_group_id", payload.ReleaseGroupID.String()).
		Str("artist", detail.Artist.Name).
		Msg("release group ingested into the catalogue")
}

func (worker *Worker) processLibraryScan(ctx context.Context, job Job) {
	// Both ends of the scan are announced. A view that is not already asking
	// has no other way to learn that one started, and a scan that failed has to
	// stop reading as one still running. The scanner announces its own progress
	// in between.
	worker.announce(events.TopicLibrary)
	defer worker.announce(events.TopicLibrary)

	if worker.scanner == nil {
		worker.finishFailed(ctx, job, errors.New("library scanner is not configured"), false)
		return
	}
	result, err := worker.scanner.Scan(ctx)
	if err != nil {
		worker.finishFailed(ctx, job, fmt.Errorf("scan library: %w", err), false)
		return
	}
	if err := worker.queue.Complete(ctx, job); err != nil {
		worker.logger.Error().Err(err).Str("job_id", job.ID.String()).Msg("complete job")
		return
	}
	// A scan discovers music; it does not find out what that music is. Files
	// nobody has asked the providers about are queued after the scan rather
	// than during it, so a scan is never held up by a rate-limited request.
	queued, err := worker.queue.QueueFileResolutions(ctx, resolutionBatch)
	if err != nil {
		worker.logger.Error().Err(err).Msg("queue file resolutions")
	}
	worker.logger.Info().
		Str("job_id", job.ID.String()).
		Int64("queued_for_resolution", queued).
		Int("discovered", result.Discovered).
		Int("updated", result.Updated).
		Int("unchanged", result.Unchanged).
		Int("missing", result.Missing).
		Int("errors", result.Errors).
		Msg("library scan completed")
	// The player reads the same directory Schall writes, and a scan is one of
	// several places that ask for it to be told — an import, an upload, a
	// deletion and a tagging pass each queue the same job. A scan that found
	// the library exactly as it left it has nothing to tell anybody about.
	//
	// Updated is a file written or rewritten and Missing is one that went away;
	// Discovered is every file the walk saw, unchanged ones included, so it says
	// only that the library is not empty and must not be read as news.
	if result.Updated+result.Missing > 0 {
		// A changed or newly discovered file needs a fresh whole-file witness.
		worker.queueLibraryWitnessSweep(ctx, worker.now())
		worker.queueNotifyPlayer(ctx)
		// A scan is how a copy fetched for a want becomes a library file, so it
		// is also the moment a track from a new release becomes one the library
		// owns — which is all the new-releases playlist holds.
		worker.queueNewReleasesPlaylistRefresh(ctx)
		// And the moment a recording starts being held twice: a second copy of
		// something the library already had is a file the scan has just seen.
		worker.queueDuplicateSweep(ctx)
	}
	// A scan is how a copy fetched for a want becomes a library file, and that
	// row is the only thing the want can be settled against. Without this the
	// want waits for whatever time it was last given — half an hour, for music
	// already on the disc.
	worker.rescheduleAcquisitionSweep(ctx)
}

// processLayoutMoves renames a batch of a planned layout migration.
//
// A batch that leaves files waiting queues the next one for now rather than
// carrying on, so a migration of tens of thousands of files never holds the
// lane long enough to stop an import, a scan or a resolution. What that costs
// is one claim per batch; what it buys is a worker that is still answering.
//
// A batch that could not be worked at all is retryable: the run is journalled,
// so a retry picks up exactly the files the last attempt did not reach. It is
// individual files that are never retried — a refusal about one file is written
// against that file, and the run moves past it.
func (worker *Worker) processLayoutMoves(ctx context.Context, job Job) {
	worker.announce(events.TopicLibrary)
	defer worker.announce(events.TopicLibrary)

	if worker.layout == nil {
		worker.finishFailed(ctx, job, errors.New("the library mover is not configured"), false)
		return
	}
	var payload struct {
		RunID uuid.UUID `json:"runId"`
	}
	if err := json.Unmarshal(job.Payload, &payload); err != nil || payload.RunID == uuid.Nil {
		worker.finishFailed(ctx, job, errors.New("invalid layout migration payload"), false)
		return
	}
	more, err := worker.layout.Apply(ctx, payload.RunID)
	if err != nil {
		worker.finishFailed(ctx, job, fmt.Errorf("apply layout migration %s: %w", payload.RunID, err), true)
		return
	}
	if err := worker.queue.Complete(ctx, job); err != nil {
		worker.logger.Error().Err(err).Str("job_id", job.ID.String()).Msg("complete job")
		return
	}
	if more {
		if err := worker.layout.QueueApply(ctx, payload.RunID); err != nil {
			worker.logger.Error().Err(err).Str("run_id", payload.RunID.String()).
				Msg("queue the rest of the layout migration")
		}
		return
	}
	worker.logger.Info().Str("run_id", payload.RunID.String()).Msg("library layout migration finished")
	// The files are somewhere else now, so the player is looking at paths that
	// no longer exist until it is told otherwise.
	worker.queueNotifyPlayer(ctx)
}

// processLibraryTagging writes what the catalogue says into a batch of the
// files already in the library, and queues its replacement while more are
// waiting — the same bargain the layout migration makes, and for the same
// reason: a batch that held the lane until the last file would stop imports,
// scans and resolutions for as long as a library takes to rewrite.
//
// A batch that could not be worked at all is retryable, because the pass
// records where it reached as it goes: a retry picks up at the file the last
// attempt stopped at rather than at the beginning. Individual files are never
// retried — what could not be written is written against that file in the log,
// and the pass moves past it.
//
// A run that reaches the end schedules its own successor a week out, whether
// it was pressed or already running on the schedule. That is what makes this
// recurring rather than a one-off: the button asks for a pass to happen now,
// and finishing one is also always asking for the next one to happen in a
// week.
func (worker *Worker) processLibraryTagging(ctx context.Context, job Job) {
	worker.announce(events.TopicLibrary)
	defer worker.announce(events.TopicLibrary)

	if worker.tagger == nil {
		worker.finishFailed(ctx, job, errors.New("writing the library's tags is not configured"), false)
		return
	}
	more, err := worker.tagger.Tag(ctx, job.ID)
	if err != nil {
		worker.finishFailed(ctx, job, fmt.Errorf("write the library's tags: %w", err), true)
		return
	}
	if err := worker.queue.Complete(ctx, job); err != nil {
		worker.logger.Error().Err(err).Str("job_id", job.ID.String()).Msg("complete job")
		return
	}
	if more {
		if err := worker.tagger.QueueNextTagging(ctx, job.ID); err != nil {
			worker.logger.Error().Err(err).Str("job_id", job.ID.String()).
				Msg("queue the rest of the pass over the library's tags")
		}
		return
	}
	worker.logger.Info().Str("job_id", job.ID.String()).Msg("the library's tags were written")
	// The files say something else now, and the player groups by what they say.
	worker.queueNotifyPlayer(ctx)
	worker.queueLibrarySweep(ctx, worker.now().Add(tagLibraryIdle))
}

// queueLibrarySweep asks the tagger for the next whole-library pass. Its own
// guard — migration 00077's partial unique index — is what lets this be asked
// unconditionally: a press already queued or running answers "nothing to do"
// rather than an error.
func (worker *Worker) queueLibrarySweep(ctx context.Context, at time.Time) {
	if worker.tagger == nil {
		return
	}
	if err := worker.tagger.QueueLibrarySweep(ctx, at); err != nil {
		worker.logger.Error().Err(err).Msg("queue the next weekly pass over the library's tags")
	}
}

// processTranscodeSweep shrinks a batch of the files already in the library,
// and queues its replacement while more are waiting — the same bargain
// processLibraryTagging makes, and for the same reason: a batch that held the
// lane until the last file would stop imports, scans and resolutions for as
// long as re-encoding the library takes.
//
// A batch that could not be worked at all is retryable, because the pass
// records where it reached as it goes. Individual files are never retried by
// this: a file that could not be shrunk is left as it was and the pass moves
// past it.
func (worker *Worker) processTranscodeSweep(ctx context.Context, job Job) {
	worker.announce(events.TopicLibrary)
	defer worker.announce(events.TopicLibrary)

	if worker.shrink == nil {
		worker.finishFailed(ctx, job, errors.New("shrinking the library is not configured"), false)
		return
	}
	more, err := worker.shrink.Sweep(ctx, job.ID)
	if err != nil {
		worker.finishFailed(ctx, job, fmt.Errorf("shrink the library: %w", err), true)
		return
	}
	if err := worker.queue.Complete(ctx, job); err != nil {
		worker.logger.Error().Err(err).Str("job_id", job.ID.String()).Msg("complete job")
		return
	}
	if more {
		if err := worker.shrink.QueueNextSweep(ctx, job.ID); err != nil {
			worker.logger.Error().Err(err).Str("job_id", job.ID.String()).
				Msg("queue the rest of the pass to shrink the library")
		}
		return
	}
	worker.logger.Info().Str("job_id", job.ID.String()).Msg("the pass to shrink the library finished")
}

// queueDuplicateSweep asks for a pass over the recordings held more than once.
// It is asked for rather than decided here: the pass itself reads the setting
// and does nothing when it is off, so a scan never has to know what somebody
// chose.
func (worker *Worker) queueDuplicateSweep(ctx context.Context) {
	if worker.duplicates == nil {
		return
	}
	if err := worker.duplicates.QueueDuplicateSweep(ctx, worker.now()); err != nil {
		worker.logger.Error().Err(err).
			Msg("queue the pass over the recordings held more than once")
	}
}

// processDuplicateSweep keeps one copy of every recording the library holds
// more than once, where it can prove the copies are the same audio.
//
// It is the only recurring thing in Schall that deletes music with nobody
// present for each deletion, so two things about it are deliberate. It runs
// only when somebody has switched it on — the sweeper reads that itself and
// answers "skipped" otherwise — and every file it deletes is licensed exactly
// as a press on the duplicates screen is, with the licence saying which of the
// two it was.
//
// A recording whose copies cannot be shown to be one piece of audio is not
// touched and stays a question. That is the whole of what makes this safe: the
// instruction being carried out is "any of these copies will do", and a group
// Schall cannot tell is copies at all is not a group the instruction reaches.
func (worker *Worker) processDuplicateSweep(ctx context.Context, job Job) {
	worker.announce(events.TopicLibrary)
	defer worker.announce(events.TopicLibrary)

	if worker.duplicates == nil {
		worker.finishFailed(ctx, job, errors.New("settling duplicates is not configured"), false)
		return
	}
	swept, err := worker.duplicates.SweepDuplicates(ctx)
	if err != nil {
		worker.finishFailed(ctx, job, fmt.Errorf("keep one copy of each recording: %w", err), true)
		return
	}
	if err := worker.queue.Complete(ctx, job); err != nil {
		worker.logger.Error().Err(err).Str("job_id", job.ID.String()).Msg("complete job")
		return
	}
	if err := worker.duplicates.QueueDuplicateSweep(ctx, worker.now().Add(duplicateSweepInterval)); err != nil {
		worker.logger.Error().Err(err).Str("job_id", job.ID.String()).
			Msg("queue the next pass over the recordings held more than once")
	}
	if swept.Skipped {
		return
	}
	worker.logger.Info().
		Str("job_id", job.ID.String()).
		Int("resolved", swept.Resolved).
		Int("removed", swept.Removed).
		Int("left_alone", swept.LeftAlone).
		Msg("one copy was kept of every recording held more than once")
}

// processFileTagging brings one file's tags into line with the catalogue tracks
// it is mapped to, so that a library matched between two passes is still
// described by the catalogue rather than by whoever sent the file — and so that
// a file which stopped being a track's stops saying it is one.
//
// It is retryable in the way the pass is not. A pass moves past the file it
// could not write, because stopping at it would leave the rest of the library
// in two accounts of itself; this job is that one file and nothing else waits
// behind it, so a write that failed is worth attempting again. A file that went
// missing in between writes nothing and is not a failure.
func (worker *Worker) processFileTagging(ctx context.Context, job Job) {
	if worker.tagger == nil {
		worker.finishFailed(ctx, job, errors.New("writing the library's tags is not configured"), false)
		return
	}
	var payload struct {
		FileID uuid.UUID `json:"fileId"`
	}
	if err := json.Unmarshal(job.Payload, &payload); err != nil || payload.FileID == uuid.Nil {
		worker.finishFailed(ctx, job, errors.New("invalid file tagging payload"), false)
		return
	}
	written, err := worker.tagger.TagFile(ctx, payload.FileID)
	if err != nil {
		worker.finishFailed(ctx, job, fmt.Errorf("write the tags for library file %s: %w", payload.FileID, err), true)
		return
	}
	if err := worker.queue.Complete(ctx, job); err != nil {
		worker.logger.Error().Err(err).Str("job_id", job.ID.String()).Msg("complete job")
		return
	}
	if written {
		// The file says something else now, and the player groups by what it says.
		worker.queueNotifyPlayer(ctx)
	}
	// And the words of the song beside it, now that the catalogue can say what
	// the song is. A file becomes a catalogue track's here, which is the
	// earliest anything true can be asked about it — before it, the only names
	// available are the ones whoever sent the file typed. A file that is a
	// track's no longer is asked nothing: the query behind this reads the
	// mapping and finds none.
	//
	// It runs after the job is complete and reports nothing, because a lyrics
	// service that is down must never hold up a file being described. The sweep
	// walks past this file again within the day either way.
	if worker.lyrics != nil {
		worker.lyrics.ForFile(ctx, payload.FileID)
	}
}

// noticePlayback tells the player the library changed.
//
// A player that could not be told is worth saying and never worth failing the
// scan for: the music is on the disc and in the catalogue either way, and the
// player finds it on its own schedule. Retrying would be chasing a courtesy at
// the cost of a scan that has already succeeded.
func (worker *Worker) noticePlayback(ctx context.Context) {
	if worker.playback == nil {
		return
	}
	if err := worker.playback.NoticeLibraryChange(ctx); err != nil {
		worker.logger.Warn().Err(err).Msg("tell the player the library changed")
	}
}

// queueNotifyPlayer asks for the notify_player job rather than telling the
// player directly, so that every site the library changes at shares one path
// and a burst of them becomes one telling. Queueing is itself a courtesy: the
// caller has already made the change that matters, and a queue that could not
// be written to is worth a warning and never a failed job.
func (worker *Worker) queueNotifyPlayer(ctx context.Context) {
	if err := worker.queue.QueueNotifyPlayer(ctx); err != nil {
		worker.logger.Warn().Err(err).Msg("queue telling the player the library changed")
	}
}

// processNotifyPlayer tells the player the library changed. It is the one
// place noticePlayback still runs from — everything that changes the library
// asks for this job instead of calling the notifier directly, which is what
// turns a burst of imports into one telling rather than one per file.
//
// Failure is a warning and the job completes anyway, the same rule
// noticePlayback always kept: the music is on the disc and in the catalogue
// either way, and retrying would only be chasing a courtesy.
func (worker *Worker) processNotifyPlayer(ctx context.Context, job Job) {
	worker.noticePlayback(ctx)
	if err := worker.queue.Complete(ctx, job); err != nil {
		worker.logger.Error().Err(err).Str("job_id", job.ID.String()).Msg("complete job")
	}
}

// processDownloadPoll follows transfer progress and reschedules itself only
// while something is still moving, so a finished download stops the polling
// rather than leaving a timer running forever.
func (worker *Worker) processDownloadPoll(ctx context.Context, job Job) {
	if worker.transfers == nil {
		worker.finishFailed(ctx, job, errors.New("download transfers are not configured"), false)
		return
	}
	active, pollErr := worker.transfers.Poll(ctx)
	if pollErr != nil {
		// A provider that cannot be read right now is worth retrying, because
		// the transfers it is running carry on without Schall watching.
		worker.finishFailed(ctx, job, fmt.Errorf("poll download transfers: %w", pollErr), true)
		return
	}
	if err := worker.queue.Complete(ctx, job); err != nil {
		worker.logger.Error().Err(err).Str("job_id", job.ID.String()).Msg("complete job")
		return
	}
	if !active {
		return
	}
	// Queued after completing this run, because only one poll job may be
	// queued or running at a time.
	runAfter := worker.now().Add(worker.transfers.PollInterval())
	if err := worker.queue.QueueDownloadPoll(ctx, runAfter); err != nil {
		worker.logger.Error().Err(err).Msg("reschedule download poller")
	}
}

// processPeerChallenges reads the private chat of the peers holding transfers
// and answers what it can, rescheduling itself only while a peer still has
// something of ours open. It follows the transfer poller in shape for the same
// reason: nothing here is worth asking about when nothing is waiting.
func (worker *Worker) processPeerChallenges(ctx context.Context, job Job) {
	if worker.challenges == nil {
		worker.finishFailed(ctx, job, errors.New("peer challenges are not configured"), false)
		return
	}
	waiting, answerErr := worker.challenges.Answer(ctx)
	if answerErr != nil {
		// slskd being unreadable for a moment says nothing about the peers, and
		// the transfers they are holding stay held.
		worker.finishFailed(ctx, job, fmt.Errorf("answer peer challenges: %w", answerErr), true)
		return
	}
	if err := worker.queue.Complete(ctx, job); err != nil {
		worker.logger.Error().Err(err).Str("job_id", job.ID.String()).Msg("complete job")
		return
	}
	if !waiting {
		return
	}
	runAfter := worker.now().Add(worker.challenges.Interval())
	if err := worker.queue.QueuePeerChallenges(ctx, runAfter); err != nil {
		worker.logger.Error().Err(err).Msg("reschedule the peer challenge reader")
	}
}

// processAcquisitionSweep attempts the wanted recordings whose turn it is and
// schedules itself for the moment the next one is due.
//
// It is the transfer poller's shape, with one difference: the poller runs on a
// fixed interval while anything is moving, and a want is due at a time the
// target itself records. So the next run is asked for rather than assumed, and
// a sweep that leaves nothing scheduled queues nothing at all — the wants that
// exist are then woken by whatever changes them.
func (worker *Worker) processAcquisitionSweep(ctx context.Context, job Job) {
	if worker.wants == nil {
		worker.finishFailed(ctx, job, errors.New("acquisition targets are not configured"), false)
		return
	}
	defer worker.announce(events.TopicAcquisitions)

	if err := worker.wants.Sweep(ctx); err != nil {
		worker.finishFailed(ctx, job, fmt.Errorf("sweep acquisition targets: %w", err), true)
		// The sweeper schedules itself, so a sweep that ended for good having
		// queued nothing would be the last one this installation ever ran.
		//
		// Only an exhausted job needs a fresh one: a retried job is itself the
		// next sweep and already carries its own backoff, and asking for one at
		// the moment a target became due would pull that backoff back to a time
		// already past. For the same reason the replacement is dated forward
		// rather than at what is due — whatever was due stays due, and a sweep
		// that keeps failing must not be retried as fast as the worker can loop.
		if spent(job, err) {
			worker.queueAcquisitionSweep(ctx, worker.now().Add(sweepFailureBackoff))
		}
		return
	}
	// CompleteAcquisitionSweep queues a follow-up sweep itself, in the same
	// transaction that retires this row, if something asked to be woken while
	// this pass was still running — a wake that could not queue one of its
	// own, because the partial index allows only one live sweeper. woken is
	// reported for the log line below, nothing else.
	woken, err := worker.queue.CompleteAcquisitionSweep(ctx, job)
	if err != nil {
		worker.logger.Error().Err(err).Str("job_id", job.ID.String()).Msg("complete job")
		return
	}
	if woken {
		worker.logger.Info().Str("job_id", job.ID.String()).
			Msg("a wake landed while an acquisition sweep was running; another was queued")
	}

	// Asked only now, because while this job was live it was the one sweep the
	// index allows, and a want created alongside it could not queue one of its
	// own. Completed first, the question is answered against a schedule nothing
	// is hidden from, and a want that arrives from here on queues its own sweep
	// which this one then merely moves earlier.
	worker.rescheduleAcquisitionSweep(ctx)

	// And then, if this pass held any copies nothing could identify, one message
	// about all of them. Here rather than where each copy is held: a pass that
	// held ten would otherwise send ten notifications about one bad night.
	worker.tellAboutTheQueue(ctx)
}

// rescheduleAcquisitionSweep asks for the next sweep at the moment the next want
// is due, if any is.
func (worker *Worker) rescheduleAcquisitionSweep(ctx context.Context) {
	if worker.wants == nil {
		return
	}
	nextDue, err := worker.wants.NextDue(ctx)
	if err != nil {
		worker.logger.Error().Err(err).Msg("read the next acquisition attempt")
		return
	}
	if nextDue.IsZero() {
		return
	}
	// Queued after this run finished, because only one sweep may be queued or
	// running at a time. A time already past is how a full batch asks to carry
	// straight on with what it did not reach.
	worker.queueAcquisitionSweep(ctx, nextDue)
}

// queueAcquisitionSweep asks for a sweep no later than the given time. A failure
// here is logged rather than returned: the schedule is on the targets, and the
// sweep is woken again by whatever changes one.
func (worker *Worker) queueAcquisitionSweep(ctx context.Context, runAfter time.Time) {
	if err := worker.queue.QueueAcquisitionSweep(ctx, runAfter); err != nil {
		worker.logger.Error().Err(err).Msg("reschedule acquisition sweep")
	}
}

func (worker *Worker) processArtist(ctx context.Context, job Job) {
	worker.announce(events.TopicCatalogue)
	defer worker.announce(events.TopicCatalogue)

	var payload struct {
		ArtistID uuid.UUID `json:"artistId"`
	}
	if err := json.Unmarshal(job.Payload, &payload); err != nil || payload.ArtistID == uuid.Nil {
		worker.finishFailed(ctx, job, errors.New("invalid refresh artist payload"), false)
		return
	}

	musicBrainzID, err := worker.queue.ArtistMusicBrainzID(ctx, payload.ArtistID)
	if err != nil {
		worker.finishFailed(ctx, job, fmt.Errorf("load artist: %w", err), true)
		return
	}
	catalogue, err := worker.provider.ArtistCatalogue(ctx, musicBrainzID)
	if err != nil {
		// Two of these are answers rather than failures, and trying again would
		// only give the same answer more slowly. A placeholder has no catalogue,
		// and a catalogue too large to read stays too large. Both are told once
		// and left alone.
		//
		// Both also write an empty genre list, because the genre backfill reads a
		// NULL one as "nobody has asked". Without it the refusal was re-queued
		// every day forever, and the artist counted as waiting in between, which
		// held the sweep at its ten-minute pace instead of letting it fall to
		// twelve hours (issue #494). A refresh that later succeeds overwrites the
		// empty list, so nothing is lost if MusicBrainz changes its mind.
		answered := errors.Is(err, musicbrainz.ErrNotAnArtist) || errors.Is(err, musicbrainz.ErrCatalogueTooLarge)
		if answered {
			if markErr := worker.queue.MarkArtistGenresAsked(ctx, payload.ArtistID); markErr != nil {
				worker.logger.Error().Err(markErr).
					Str("artist_id", payload.ArtistID.String()).
					Msg("record that the artist genres were asked for")
			}
		}
		worker.finishFailed(ctx, job, fmt.Errorf("fetch artist catalogue: %w", err), !answered)
		return
	}
	if err := worker.queue.SaveArtistCatalogue(ctx, payload.ArtistID, catalogue); err != nil {
		worker.finishFailed(ctx, job, fmt.Errorf("save artist catalogue: %w", err), true)
		return
	}
	if err := worker.queue.Complete(ctx, job); err != nil {
		worker.logger.Error().Err(err).Str("job_id", job.ID.String()).Msg("complete job")
		return
	}
	worker.logger.Info().
		Str("job_id", job.ID.String()).
		Str("artist_id", payload.ArtistID.String()).
		Int("release_groups", len(catalogue.ReleaseGroups)).
		Msg("artist metadata refreshed")
}

// processLabelRefresh reads one pass of a followed label's releases.
//
// A label with a large catalogue is more pages than one pass reads, so a pass
// that stopped part way queues the pass that carries on. Where it got to is on
// the label's row, not in this job's payload, so a pass that fails for good
// still leaves the walk where it reached rather than back at the beginning.
func (worker *Worker) processLabelRefresh(ctx context.Context, job Job) {
	worker.announce(events.TopicCatalogue)
	defer worker.announce(events.TopicCatalogue)

	if worker.labels == nil {
		worker.finishFailed(ctx, job, errors.New("labels are not configured"), false)
		return
	}
	var payload struct {
		LabelID uuid.UUID `json:"labelId"`
	}
	if err := json.Unmarshal(job.Payload, &payload); err != nil || payload.LabelID == uuid.Nil {
		worker.finishFailed(ctx, job, errors.New("invalid refresh label payload"), false)
		return
	}

	more, err := worker.labels.Refresh(ctx, payload.LabelID)
	// A label MusicBrainz will not answer about is an answer, not a failure:
	// asking again would ask the same question and get the same silence.
	if errors.Is(err, labels.ErrUnknownLabel) {
		worker.finishFailed(ctx, job, err, false)
		return
	}
	if err != nil {
		worker.finishFailed(ctx, job, fmt.Errorf("refresh label: %w", err), true)
		return
	}
	if err := worker.queue.Complete(ctx, job); err != nil {
		worker.logger.Error().Err(err).Str("job_id", job.ID.String()).Msg("complete job")
		return
	}
	// Refresh itself reports no more pages the moment the label is no longer
	// followed (internal/labels/refresh.go), so this never requeues a walk
	// over a label somebody unfollowed between one pass and the next.
	if more {
		if err := worker.labels.QueueRefresh(ctx, payload.LabelID); err != nil {
			worker.logger.Error().Err(err).Str("label_id", payload.LabelID.String()).
				Msg("queue the next pass over a label")
		}
	}
	worker.logger.Info().
		Str("job_id", job.ID.String()).
		Str("label_id", payload.LabelID.String()).
		Bool("more", more).
		Msg("label releases refreshed")
}

func (worker *Worker) processAlbum(ctx context.Context, job Job) {
	worker.announce(events.TopicCatalogue)
	defer worker.announce(events.TopicCatalogue)

	var payload struct {
		AlbumID uuid.UUID `json:"albumId"`
	}
	if err := json.Unmarshal(job.Payload, &payload); err != nil || payload.AlbumID == uuid.Nil {
		worker.finishFailed(ctx, job, errors.New("invalid refresh album payload"), false)
		return
	}

	releaseGroupID, selectedReleaseID, err := worker.queue.AlbumMusicBrainzIDs(ctx, payload.AlbumID)
	if err != nil {
		worker.finishFailed(ctx, job, fmt.Errorf("load album: %w", err), true)
		return
	}
	catalogue, err := worker.provider.ReleaseCatalogue(ctx, releaseGroupID, selectedReleaseID)
	if err != nil {
		// A release group with no edition Schall can read a track list off is an
		// answer, and reading the same releases again gives the same one. It
		// writes the empty genre list for the reason the artist refresh above
		// does: four such release groups kept the genre backfill counting work it
		// could never finish (issue #494).
		answered := errors.Is(err, musicbrainz.ErrNoUsableEditions)
		if answered {
			if markErr := worker.queue.MarkAlbumGenresAsked(ctx, payload.AlbumID); markErr != nil {
				worker.logger.Error().Err(markErr).
					Str("album_id", payload.AlbumID.String()).
					Msg("record that the release genres were asked for")
			}
		}
		worker.finishFailed(ctx, job, fmt.Errorf("fetch release catalogue: %w", err), !answered)
		return
	}
	if err := worker.queue.SaveReleaseCatalogue(ctx, payload.AlbumID, catalogue); err != nil {
		worker.finishFailed(ctx, job, fmt.Errorf("save release catalogue: %w", err), true)
		return
	}
	// The other half of a standing "want what is missing". Following an artist
	// queues one of these jobs per release group and they land one at a time
	// behind MusicBrainz's rate limit, so the press on the artist's page reaches
	// only the releases whose tracks had already arrived; this is where the rest
	// of them are wanted.
	//
	// It runs before the job is completed, so a failure here retries rather than
	// losing the wants. Both halves are idempotent: the fetch above answers the
	// same, saving the catalogue again changes nothing, and want creation is
	// idempotent on the recording ID.
	wanted := 0
	if worker.standing != nil {
		made, err := worker.standing.WantAlbumMissing(ctx, payload.AlbumID)
		if err != nil {
			worker.finishFailed(ctx, job, fmt.Errorf("want what is missing: %w", err), true)
			return
		}
		wanted = made
	}
	if err := worker.queue.Complete(ctx, job); err != nil {
		worker.logger.Error().Err(err).Str("job_id", job.ID.String()).Msg("complete job")
		return
	}
	worker.logger.Info().
		Str("job_id", job.ID.String()).
		Str("album_id", payload.AlbumID.String()).
		Str("release_id", catalogue.Edition.ID.String()).
		Int("tracks", len(catalogue.Tracks)).
		Int("wanted", wanted).
		Msg("release metadata refreshed")
}

// spent reports that this failure ends the job rather than putting it a rung up
// its ladder, so that whatever else was waiting on the last attempt happens on
// this one: a transfer recorded as failed, a file recorded as unresolved, a
// sweep queueing its own replacement.
//
// Two things end a job. It has made as many attempts as it was allowed, or the
// answer it met will be the same answer next time — a service that said no is
// not asked twice, and everything that used to wait for the third attempt has
// to happen at the first, or a permanent failure would leave a download stuck
// reading as still importing and a sweep never queued again.
func spent(job Job, cause error) bool {
	return job.Attempts >= job.MaxAttempts || Classify(cause).Permanent
}

func (worker *Worker) finishFailed(ctx context.Context, job Job, cause error, retryable bool) {
	worker.finishFailedAfter(ctx, job, cause, retryable, retryDelay(Classify(cause), job.Attempts))
}

// finishFailedAfter is finishFailed with the wait chosen by the caller, for a
// failure that says how long to wait. Rate limiting is the case that needs it:
// the ordinary backoff starts at five seconds, which is far too soon to be
// anything but a second refusal.
func (worker *Worker) finishFailedAfter(
	ctx context.Context, job Job, cause error, retryable bool, backoff time.Duration,
) {
	// A worker that is stopping records nothing and leaves the row where it is.
	// Nothing was learned, the write would be refused by this same finished
	// context anyway, and the row is put back: by the recovery pass at the next
	// start, or by the reaper once the lease on it expires.
	//
	// What decides that is this context, and not what the error wraps. A bounded
	// call inside a healthy job — a provider search, an HTTP request, anything
	// carrying a timeout of its own — fails with a deadline the job is expected
	// to survive, and reading that as the worker stopping abandoned the job
	// mid-flight while the worker went on to claim the next one. The row it left
	// behind is worse than a spent attempt: every liveness index over
	// ('queued', 'running') is partial and unique, so a download, an album
	// refresh or a sweep whose row sits in 'running' for good can never be queued
	// again — the replacement conflicts with a job nobody is running. It was
	// silent, too, sitting above the warning that says a job failed (issue #288).
	//
	// This context is also what the lease cancels. A worker that has lost the
	// claim must not settle it, because the row now belongs to a later one.
	if ctx.Err() != nil {
		return
	}

	// An answer that will be the same next time is not retried, however many
	// attempts the job has left. A release group MusicBrainz answers 404 for is
	// not there, and asking twice more only delays the reason reaching the
	// person who can do something about it — while filling the failed list with
	// three rows about one fact.
	if Classify(cause).Permanent {
		retryable = false
	}

	message := truncateError(cause)
	var err error
	if retryable && job.Attempts < job.MaxAttempts {
		runAfter := worker.now().Add(backoff)
		err = worker.queue.Retry(ctx, job, message, runAfter)
	} else {
		err = worker.queue.Fail(ctx, job, message)
	}
	if err != nil {
		worker.logger.Error().Err(err).Str("job_id", job.ID.String()).Msg("record job failure")
		return
	}
	worker.logger.Warn().
		Err(cause).
		Str("job_id", job.ID.String()).
		Bool("retrying", retryable && job.Attempts < job.MaxAttempts).
		Msg("job failed")
}

// retryLadder is how long a job waits before each attempt: the first wait, and
// the longest one the doubling is allowed to reach.
type retryLadder struct {
	first   time.Duration
	longest time.Duration
}

// ordinaryLadder is the wait for a failure that says nothing about how long to
// wait. Five seconds, doubling, is right for a database that stumbled or a file
// that was being written as it was read.
var ordinaryLadder = retryLadder{first: 5 * time.Second, longest: 5 * time.Minute}

// unreachableLadder is the wait for a failure where no answer came back at all:
// a server name that could not be resolved, a connection that was refused, a
// request that ran out of time, a connection the far side closed part way
// through.
//
// It exists because five seconds is not a wait, it is a repetition. A job with
// three attempts spends all of them inside twenty seconds, and an outage of any
// kind lasts longer than that — so on 12 August 2026 a few minutes without
// working name resolution killed seventy-one jobs that would each have
// succeeded on their own a quarter of an hour later.
//
// The shape is the same shape, and only what feeds it has changed: two minutes,
// doubling, up to half an hour. Three attempts now span six minutes rather than
// twenty seconds, and the pass over the failed list gives one more attempt half
// an hour after that.
var unreachableLadder = retryLadder{first: 2 * time.Minute, longest: 30 * time.Minute}

// retryDelay is how long this failure waits before the next attempt.
func retryDelay(failure Failure, attempt int) time.Duration {
	if failure.Unreachable {
		return unreachableLadder.wait(attempt)
	}
	return ordinaryLadder.wait(attempt)
}

func (ladder retryLadder) wait(attempt int) time.Duration {
	if attempt < 1 {
		attempt = 1
	}
	delay := ladder.first
	for current := 1; current < attempt && delay < ladder.longest; current++ {
		delay *= 2
	}
	if delay > ladder.longest {
		return ladder.longest
	}
	return delay
}

func truncateError(err error) string {
	const maxLength = 2000
	message := strings.TrimSpace(err.Error())
	if len(message) <= maxLength {
		return message
	}
	return message[:maxLength]
}

func wait(ctx context.Context, duration time.Duration) bool {
	timer := time.NewTimer(duration)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-timer.C:
		return true
	}
}

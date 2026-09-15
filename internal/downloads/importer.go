package downloads

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
	"unicode"

	"github.com/google/uuid"
	"github.com/pxldi/schall/internal/acoustid"
	"github.com/pxldi/schall/internal/db"
	"github.com/pxldi/schall/internal/filecopy"
	"github.com/pxldi/schall/internal/identity"
	"github.com/pxldi/schall/internal/library"
	"github.com/pxldi/schall/internal/loudness"
	"github.com/pxldi/schall/internal/tagmatch"
	"github.com/pxldi/schall/internal/transcode"
	"github.com/rs/zerolog"
)

type ImportStore interface {
	ClaimDownloadImport(context.Context, uuid.UUID) (db.DownloadImportRow, error)
	// The rest is what importing one file fetched for a want needs. It is the
	// same store because it is the same import: one shape validates a release
	// against its edition, the other one file against one recording.
	DownloadRequestServesWant(context.Context, uuid.UUID) (bool, error)
	ClaimTargetImport(context.Context, uuid.UUID) (db.TargetImportRow, error)
	CompleteTargetImport(
		ctx context.Context, verdict db.RecordAcquiredFileParams, targetSummary string,
		rootPath, importPath string, localPaths map[string]db.ImportedDownloadFile,
	) error
	SettleAcquiredFile(context.Context, db.SettleAcquiredFileParams) error
	// SettleAcquiredFileForGoneRecording is SettleAcquiredFile's counterpart for
	// a copy that could not be checked because MusicBrainz no longer knows the
	// recording its want named: the copy is discarded the same way, and the
	// want is sent back to resolution rather than back into the search.
	SettleAcquiredFileForGoneRecording(context.Context, db.RecordingGoneParams) error
	// The files the library already holds and has already proven to be one
	// recording. They are what a copy is compared against before anybody outside
	// the machine is asked anything about it.
	SettledWitnesses(context.Context, uuid.UUID, int32) ([]db.LibraryWitnessRow, error)
	UserAcceptedCopy(context.Context, uuid.UUID) (bool, error)
	// The three a second pass over copies already decided about needs. See
	// JudgeAgain: it offers copies, checks the want was not answered in the
	// meantime, and puts one request back where validation claims from.
	CopiesToJudgeAgain(context.Context) ([]db.JudgeAgainRow, error)
	CopiesRefusedOnCreditAlone(context.Context) ([]db.JudgeAgainRow, error)
	WantHasAcceptedCopy(context.Context, uuid.UUID) (bool, error)
	ReopenCopyForJudging(context.Context, uuid.UUID) error
	// The two a pass over one newly anchored want needs. See JudgeWantCopies: it
	// offers that want's copies alone, and writes what a copy whose bytes have
	// gone measured to against the anchor. The second touches no verdict.
	CopiesToJudgeAgainForWant(context.Context, uuid.UUID) ([]db.JudgeAgainRow, error)
	RecordCopyAnchorReading(
		ctx context.Context, copyID uuid.UUID, anchor db.ImportAnchor, summary string,
	) error
	// RearmWantNowAnchored gives back the next look of a want that stopped while
	// a copy waited for an answer, now that the anchor has ruled on its copies.
	RearmWantNowAnchored(ctx context.Context, targetID uuid.UUID, summary string) (bool, error)
	// The two the backfill pass needs. See FingerprintHeldCopies: it reads the
	// held copies whose audio was never measured, and writes the measurement
	// back. Neither touches a verdict.
	CopiesMissingFingerprint(context.Context, int32) ([]db.UnfingerprintedCopyRow, error)
	RecordCopyFingerprint(ctx context.Context, copyID uuid.UUID, print string, seconds int) error
	MarkDownloadImportNeedsReview(context.Context, uuid.UUID, string, *db.ImportEvidence) error
	CompleteDownloadImport(context.Context, uuid.UUID, string, string, map[string]db.ImportedDownloadFile) error
	ImportSettings(context.Context) (db.ImportSettingsRow, error)
	RecordSourceRetention(context.Context, uuid.UUID, bool, string) error
	// RecheckAfterMusicBrainzSilence records that this want is waiting on a
	// provider that could not be asked, and queues the next re-judge while it
	// has one left. False means the schedule is spent and the want is a
	// question for a person.
	RecheckAfterMusicBrainzSilence(
		ctx context.Context, targetID uuid.UUID, now time.Time, spentSummary string,
	) (bool, error)
	// EndTheMusicBrainzWaitIfAnswered takes that wait off a want with no copy
	// stopped on the registration question left. It reads the state rather than
	// any one verdict, so it is asked after every decision.
	EndTheMusicBrainzWaitIfAnswered(context.Context, uuid.UUID) error
	// ReleaseCopyFromJudging undoes a judging pass's claim on a request the pass
	// could not decide, so the copy is a question again and the want may fetch.
	ReleaseCopyFromJudging(context.Context, uuid.UUID) error
	// The two that record what became of the bytes when the operator asked for
	// smaller copies: one file written smaller, and one that was meant to be
	// and was not.
	RecordTranscode(context.Context, uuid.UUID, db.RecordTranscodeParams) error
	RecordTranscodeRefused(context.Context, uuid.UUID, string) error
}

// AcousticIdentifier says what a file is by decoding its audio, rather than by
// reading what the file claims about itself. It is optional: without one,
// validation asks exactly the questions it always did.
type AcousticIdentifier interface {
	// Identify returns what the audio was recognised as together with how long it
	// ran, because deciding a file needs both and only the decode knows either.
	Identify(ctx context.Context, path string) (acoustid.Identification, error)
	// Configured is asked once per release rather than per file, and takes a
	// context because whether the check is on is stored, not compiled in.
	Configured(ctx context.Context) bool
}

type Importer struct {
	store       ImportStore
	inboxPath   string
	libraryPath string
	inspect     func(string) library.AudioMetadata
	acoustic    AcousticIdentifier
	verifier    Verifier
	prints      Fingerprinter
	// quality measures what a copy's audio is rather than what it claims. It
	// decides nothing: see AudioMeasurer.
	quality AudioMeasurer
	// shapes draws the waveform the review queue puts on a held copy's card.
	// Optional and decides nothing: see WaveformReader.
	shapes WaveformReader
	// layout is the folder shape the operator chose, read per import so that
	// changing it takes effect without a restart. Without one, music is filed
	// where an untouched installation has always filed it.
	layout func(context.Context) (library.Template, error)
	// loud measures how loud an imported file is, so a player can even the
	// collection out. Optional: without it an import writes no replay gain.
	loud *loudness.Measurer
	// transcoder writes a smaller copy of a file that is already proven, when
	// the operator asked for one. Optional and nil by default: without it every
	// file is filed exactly as it arrived, which is what every import did
	// before this existed.
	transcoder *transcode.Service
	logger     zerolog.Logger
	now        func() time.Time
}

func NewImporter(store ImportStore, inboxPath, libraryPath string, logger zerolog.Logger) *Importer {
	return &Importer{
		store: store, inboxPath: filepath.Clean(inboxPath),
		libraryPath: filepath.Clean(libraryPath), inspect: library.InspectAudio,
		logger: logger, now: time.Now,
	}
}

// WithClock sets the clock used for delayed re-judges.
func (importer *Importer) WithClock(now func() time.Time) *Importer {
	importer.now = now
	return importer
}

// WithLayout registers where the folder shape is read from. It is the same
// setting the layout migration renders against, so what an import writes and
// what a migration would move it to are one answer rather than two.
func (importer *Importer) WithLayout(
	layout func(context.Context) (library.Template, error),
) *Importer {
	importer.layout = layout
	return importer
}

// WithTranscoding registers what writes a smaller copy of a proven file. It
// runs after every verdict and can never change one: without it, and on every
// installation that has not asked for smaller copies, files are imported
// exactly as they arrived.
func (importer *Importer) WithTranscoding(service *transcode.Service) *Importer {
	importer.transcoder = service
	return importer
}

// catalogueFields is the release the catalogue says a file belongs to, in the
// shape the layout renders: who the album is by, what it is called, and which
// release it is.
//
// Both import paths build it here and nowhere else. A whole release chosen by
// hand and one track fetched for a want are the same album whenever the
// catalogue answers for the same release group, so the two have to render one
// folder — and they only keep doing that while one function says what the
// folder is made of.
//
// The artist is the album's own — the row in artists the album points at — and
// never the credit printed on a track. A guest appearance is credited
// "Ufo361 feat. KC Rebell" on the track while the album stays Ufo361's, and a
// stylised credit spells the same artist "RIN" on one release and "Rin" on the
// next. Filing by either gave one album a folder per credit.
func catalogueFields(
	artistName, albumTitle string, releaseGroup uuid.NullUUID, albumID uuid.UUID,
) library.Fields {
	return library.Fields{
		Artist: artistName,
		Album:  albumTitle,
		ID:     shortID(releaseGroup, albumID),
	}
}

// releaseFolder is where one release's files belong under the library.
//
// The identifier is the release rather than the download, which is the whole
// point of it: two downloads of one album, or thirteen wanted tracks off it
// arriving one at a time, are the same release and belong in the same folder.
// Filing by the download instead gave every arrival a folder of its own, and a
// library with one folder per file cannot tell a second copy from a first.
//
// A template that cannot be read files music the way an untouched installation
// always has rather than refusing the import: the copy is already validated and
// on disk, and a settings row nobody can read is not a reason to lose it.
func (importer *Importer) releaseFolder(
	ctx context.Context, fields library.Fields,
) string {
	template, err := library.ParseTemplate(library.DefaultTemplate)
	if importer.layout != nil {
		chosen, layoutErr := importer.layout(ctx)
		if layoutErr != nil {
			importer.logger.Warn().Err(layoutErr).
				Msg("read the library layout; filing this import the default way")
		} else {
			template, err = chosen, nil
		}
	}
	if err != nil {
		return filepath.Join(importer.libraryPath, safeComponent(fields.Artist),
			safeComponent(fields.Album)+" ["+fields.ID+"]")
	}
	return filepath.Join(importer.libraryPath, template.Render(fields))
}

// SettingsIdentifier builds an acoustic identifier from the stored settings on
// every use, so turning the check on or changing the key takes effect without a
// restart — and so a key that was never entered simply means no check.
type SettingsIdentifier struct {
	settings   func(context.Context) (db.ImportSettingsRow, error)
	fpcalcPath string
}

func NewSettingsIdentifier(
	settings func(context.Context) (db.ImportSettingsRow, error), fpcalcPath string,
) *SettingsIdentifier {
	return &SettingsIdentifier{settings: settings, fpcalcPath: fpcalcPath}
}

// Configured reports whether the stored settings both enable the check and
// carry a key. Without both, no file is fingerprinted and no evidence is
// recorded: a check that did not run must not leave a trace suggesting it did.
func (identifier *SettingsIdentifier) Configured(ctx context.Context) bool {
	row, err := identifier.settings(ctx)
	return err == nil && row.AcoustIDEnabled && strings.TrimSpace(row.AcoustIDAPIKey) != ""
}

// client builds the AcoustID client the stored settings describe, or nothing at
// all when the check is switched off. Off is answered as silence rather than as
// a failure, here as everywhere: a check that did not run must leave no trace
// suggesting it did.
func (identifier *SettingsIdentifier) client(ctx context.Context) (*acoustid.Client, error) {
	row, err := identifier.settings(ctx)
	if err != nil {
		return nil, err
	}
	if !row.AcoustIDEnabled || strings.TrimSpace(row.AcoustIDAPIKey) == "" {
		return nil, nil
	}
	return acoustid.NewClient(acoustid.Options{
		APIKey: row.AcoustIDAPIKey, FpcalcPath: identifier.fpcalcPath,
	}), nil
}

func (identifier *SettingsIdentifier) Identify(
	ctx context.Context, path string,
) (acoustid.Identification, error) {
	client, err := identifier.client(ctx)
	if err != nil || client == nil {
		return acoustid.Identification{}, err
	}
	print, err := client.Fingerprint(ctx, path)
	if err != nil {
		return acoustid.Identification{}, err
	}
	clusters, err := client.Identify(ctx, print)
	if err != nil {
		return acoustid.Identification{}, err
	}
	// The decoded length is kept rather than spent on the lookup and thrown away.
	// It is the one duration on this path nobody wrote down by hand.
	return acoustid.Identification{
		DurationSeconds: print.DurationSeconds, Clusters: clusters, Damaged: print.Damaged,
	}, nil
}

// Fingerprint computes one file's fingerprint without looking it up, for a caller
// that means to keep it. It is the same decode Identify performs, split from the
// lookup because the two cost different things: this one is seconds of CPU and
// describes the audio for as long as the audio is unchanged, while the lookup
// behind it is a request to a service that rate-limits.
//
// Audio that will not decode comes back as an empty fingerprint and no error. The
// bytes on disk will not decode differently next time, so it is a fact about the
// copy rather than a failure worth retrying.
func (identifier *SettingsIdentifier) Fingerprint(
	ctx context.Context, path string,
) (string, error) {
	client, err := identifier.client(ctx)
	if err != nil || client == nil {
		return "", err
	}
	print, err := client.Fingerprint(ctx, path)
	if errors.Is(err, acoustid.ErrNoFingerprint) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	return keptFingerprint(print), nil
}

// Lookup asks AcoustID what a kept fingerprint is.
//
// The clusters cross the boundary with their own edges intact, exactly as they do
// on the import path: within one, AcoustID is saying those recordings are the same
// piece of audio; between two, that the second is other audio which resembled the
// fingerprint. Flattening them would be the one change that could admit a file
// nobody identified.
//
// An answer with no clusters in it is silence, and it is returned as an answer
// rather than as nothing, because "the database has never heard this" and "nobody
// listened" are different facts about a file.
func (identifier *SettingsIdentifier) Lookup(
	ctx context.Context, fingerprint string,
) (*identity.Acoustic, error) {
	client, err := identifier.client(ctx)
	if err != nil || client == nil {
		return nil, err
	}
	print, err := readKeptFingerprint(fingerprint)
	if err != nil {
		return nil, err
	}
	clusters, err := client.Identify(ctx, print)
	if err != nil {
		return nil, err
	}
	acoustic := identity.Acoustic{Clusters: make([]identity.AcousticCluster, 0, len(clusters))}
	for _, cluster := range clusters {
		acoustic.Clusters = append(acoustic.Clusters, identity.AcousticCluster{
			ID: cluster.ID, Recordings: cluster.Recordings, Similarity: cluster.Score,
		})
	}
	return &acoustic, nil
}

// keptFingerprint writes down everything a lookup needs and nothing else: the
// fingerprint, and the length of the audio it was computed from, which AcoustID
// asks for alongside it. The decoded length and the decoder's complaints are not
// kept — both were spent deciding whether this fingerprint describes the file at
// all, and that decision was made when it was computed.
//
// The length goes first because a fingerprint never contains a colon, so the
// split is unambiguous whatever fpcalc produced.
func keptFingerprint(print acoustid.Fingerprint) string {
	return strconv.Itoa(print.DurationSeconds) + ":" + print.Value
}

func readKeptFingerprint(kept string) (acoustid.Fingerprint, error) {
	seconds, value, found := strings.Cut(kept, ":")
	duration, err := strconv.Atoi(seconds)
	if !found || err != nil || value == "" {
		return acoustid.Fingerprint{}, fmt.Errorf(
			"%w: %q", identity.ErrFingerprintUnreadable, kept)
	}
	return acoustid.Fingerprint{DurationSeconds: duration, Value: value}, nil
}

// WithAcousticIdentifier registers the component that identifies audio by sound.
// It is what turns a mislabelled file from something a reviewer might wave
// through into something the import refuses on evidence.
func (importer *Importer) WithAcousticIdentifier(identifier AcousticIdentifier) *Importer {
	importer.acoustic = identifier
	return importer
}

func (importer *Importer) Fail(ctx context.Context, requestID uuid.UUID, detail string) error {
	return importer.store.MarkDownloadImportNeedsReview(ctx, requestID, detail, nil)
}

// Import validates a whole completed release before copying any of it into the
// managed library. Validation failures are durable review decisions, not
// retryable worker errors. Filesystem and database failures remain retryable.
func (importer *Importer) Import(ctx context.Context, requestID uuid.UUID) error {
	// One file fetched for a want is validated against the recording that want
	// names, not against a release it may not have. The two paths are chosen
	// before either claims anything, so neither can take the other's work.
	servesWant, err := importer.store.DownloadRequestServesWant(ctx, requestID)
	if err != nil {
		return err
	}
	if servesWant {
		// A copy somebody already decided about needs no verifier: there is
		// nothing left to check, and refusing here would strand their decision on
		// an installation that has no acoustic identification configured.
		byHand, err := importer.store.UserAcceptedCopy(ctx, requestID)
		if err != nil {
			return err
		}
		if importer.verifier == nil && !byHand {
			return ErrNoVerifier
		}
		return importer.ImportOne(ctx, requestID)
	}
	row, err := importer.store.ClaimDownloadImport(ctx, requestID)
	if err != nil {
		return err
	}
	files, err := importer.validate(ctx, row)
	var invalid *validationError
	if errors.As(err, &invalid) {
		if recordErr := importer.store.MarkDownloadImportNeedsReview(
			ctx, requestID, invalid.Error(), invalid.evidence,
		); recordErr != nil {
			return recordErr
		}
		importer.logger.Warn().Str("request_id", requestID.String()).Err(invalid).
			Msg("download import needs review")
		return nil
	}
	if err != nil {
		return err
	}

	destination := importer.releaseFolder(ctx, catalogueFields(
		row.ArtistName, row.AlbumTitle, row.ReleaseGroupID, row.AlbumID,
	))
	localPaths, placed, err := importer.copyRelease(ctx, destination, files)
	if err != nil {
		return err
	}
	// Written before the import is completed, because completing it is what
	// queues the scan, and the scan is what turns these paths into rows. A note
	// that arrived after the walk would be a note nothing ever reads.
	importer.recordPlacements(ctx, requestID, placed, localPaths, files)
	if err := importer.store.CompleteDownloadImport(
		ctx, requestID, importer.libraryPath, destination, localPaths,
	); err != nil {
		return err
	}
	importer.logger.Info().Str("request_id", requestID.String()).
		Str("path", destination).Int("files", len(files)).Msg("download imported")
	importer.releaseSource(ctx, requestID, files, placed, localPaths)
	importer.tagRelease(ctx, row, localPaths)
	return nil
}

// releaseSource applies the retention policy to the provider's copy. It runs
// only after the import is committed, because that transaction is what makes
// the managed copy and its provenance durable, and a source file that has been
// removed cannot be imported a second time.
//
// Nothing here can fail an import that has already succeeded, so no error is
// returned. A refusal is recorded against the request instead: a policy that
// silently does nothing is worse than one that says why it did nothing.
func (importer *Importer) releaseSource(
	ctx context.Context,
	requestID uuid.UUID,
	files []validatedFile,
	placed []transcode.Placement,
	localPaths map[string]db.ImportedDownloadFile,
) {
	settings, err := importer.store.ImportSettings(ctx)
	if err != nil {
		importer.logger.Error().Err(err).Str("request_id", requestID.String()).
			Msg("import retention policy could not be read; the provider copy was kept")
		return
	}
	// Keeping is the absence of an action, so it is not worth an entry in the
	// history of every import that ever succeeds.
	if settings.SourceRetention != db.SourceRetentionDelete {
		return
	}

	removed, detail := importer.removeSource(files, placed, localPaths)
	if err := importer.store.RecordSourceRetention(ctx, requestID, removed, detail); err != nil {
		importer.logger.Error().Err(err).Str("request_id", requestID.String()).
			Msg("what became of the provider copy could not be recorded")
	}
	event := importer.logger.Info()
	if !removed {
		event = importer.logger.Warn()
	}
	event.Str("request_id", requestID.String()).Bool("removed", removed).
		Msg("provider copy retention applied: " + detail)
}

// removeSource deletes the provider's copy of exactly the files that were
// imported, and reports what happened in a sentence fit to store.
//
// Every managed copy is confirmed before anything is deleted, because a
// partial removal leaves a release that can be neither replayed from the inbox
// nor completed from the library. Files Schall did not import — artwork, logs,
// anything else the folder holds — are never touched; the folder itself goes
// only if removing the audio left it empty.
func (importer *Importer) removeSource(
	files []validatedFile, placed []transcode.Placement,
	localPaths map[string]db.ImportedDownloadFile,
) (bool, string) {
	for index, file := range files {
		imported, known := localPaths[file.request.Path]
		if !known {
			return false, fmt.Sprintf("%q has no recorded managed copy; nothing was removed", file.request.Name)
		}
		info, err := os.Lstat(imported.LocalPath)
		switch {
		case err != nil:
			return false, fmt.Sprintf("the managed copy of %q could not be confirmed: %v; nothing was removed",
				file.request.Name, err)
		// Measured against what was written rather than against what arrived: a
		// copy that was re-encoded is deliberately not the size of its source,
		// and comparing the two would refuse every removal on an installation
		// that asked for smaller copies.
		case !info.Mode().IsRegular() || info.Size() != placed[index].SizeBytes:
			return false, fmt.Sprintf("the managed copy of %q is not the file that was validated; nothing was removed",
				file.request.Name)
		}
	}

	for index, file := range files {
		if err := os.Remove(file.source); err != nil && !errors.Is(err, os.ErrNotExist) {
			return false, fmt.Sprintf("%q could not be removed from the provider folder: %v (%d of %d removed)",
				file.request.Name, err, index, len(files))
		}
	}
	detail := fmt.Sprintf("%d imported file(s) removed from the provider folder", len(files))
	if len(files) > 0 {
		// An empty folder left behind is the provider's to keep, so a failure to
		// remove it is not a failure of the policy.
		if err := os.Remove(filepath.Dir(files[0].source)); err == nil {
			detail += ", which was then empty and removed as well"
		}
	}
	return true, detail
}

type validatedFile struct {
	request db.DownloadRequestFile
	source  string
	trackID uuid.UUID
}

// inspected is one downloaded file after it has been read from disk, before
// anything is known about which catalogue track it is.
type inspected struct {
	request  db.DownloadRequestFile
	source   string
	evidence db.ImportFileEvidence
	// tags are what the file says about itself, or nil when it could not be
	// read as tagged audio.
	tags *db.ImportTags
	// damaged records a problem about the bytes rather than about identity.
	// A person cannot decide those away.
	damaged bool
	// settled marks a file whose identity was already answered, by a reviewer's
	// decision or by that decision having gone stale. Matching leaves it alone.
	settled bool
	// track indexes the catalogue track this file was tied to, or -1.
	track int
}

// validate compares a whole downloaded folder against the catalogue edition and
// keeps going after the first disagreement, so a paused import can be reviewed
// as a whole release rather than as the one reason validation stopped at. It
// imports nothing unless every file was identified and every track was covered.
//
// Identity is decided by matchRelease, which assigns a file only when the
// evidence points at one track and nothing contradicts it. Everything this
// function adds is about the bytes: whether the file is there, whole, and
// readable. Those two questions stay separate on purpose — one is a matter of
// judgement a reviewer may settle, the other never is.
//
// Filesystem and database failures stay retryable errors. Anything the files
// themselves say — including a promised file the provider folder does not
// contain — is durable evidence, because retrying cannot change it.
func (importer *Importer) validate(ctx context.Context, row db.DownloadImportRow) ([]validatedFile, error) {
	if len(row.Files) == 0 || len(row.Tracks) == 0 {
		return nil, invalidf("the request or catalogue has no tracks")
	}
	sourceRoot, err := inboxFolder(importer.inboxPath, row.SourceDirectory)
	if errors.Is(err, errUnsafeFolderName) {
		return nil, invalidf("%s", err)
	}
	if err != nil {
		return nil, err
	}

	evidence := &db.ImportEvidence{
		Files:           make([]db.ImportFileEvidence, 0, len(row.Files)),
		UnmatchedTracks: []db.ImportTags{},
		Problems:        []string{},
	}
	tracks := make([]db.ImportTags, 0, len(row.Tracks))
	positions := make(map[string]bool, len(row.Tracks))
	for _, track := range row.Tracks {
		tracks = append(tracks, catalogueTags(track, row))
		// A position the edition uses twice cannot identify anything, but it is
		// a defect in the catalogue rather than in the download, so it is
		// reported and matching carries on with the rest of the evidence.
		if key := position(track.DiscNumber, track.TrackNumber); positions[key] {
			evidence.Problems = append(evidence.Problems,
				fmt.Sprintf("the catalogue edition lists position %s more than once", key))
		} else {
			positions[key] = true
		}
	}
	if len(row.Files) != len(row.Tracks) {
		evidence.Problems = append(evidence.Problems,
			fmt.Sprintf("download has %d files but the catalogue edition has %d tracks",
				len(row.Files), len(row.Tracks)))
	}

	files := make([]inspected, 0, len(row.Files))
	for _, requested := range row.Files {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		file, err := importer.read(sourceRoot, requested)
		if err != nil {
			return nil, err
		}
		files = append(files, file)
	}
	// A recorded judgement is a person's answer to exactly this question, so it
	// is applied before matching runs and the track it names is withheld from
	// matching entirely. Matching may add agreement; it may never overrule one.
	reserved := applyDecisions(files, row)
	matchRemaining(files, tracks, reserved)
	importer.verifyAcoustically(ctx, files, row)

	claimed := make([]bool, len(tracks))
	result := make([]validatedFile, 0, len(row.Files))
	for _, file := range files {
		if file.track >= 0 {
			claimed[file.track] = true
			if len(file.evidence.Problems) == 0 {
				result = append(result, validatedFile{
					request: file.request, source: file.source,
					trackID: row.Tracks[file.track].ID,
				})
			}
		}
		evidence.Files = append(evidence.Files, file.evidence)
	}
	for index, track := range tracks {
		if !claimed[index] {
			evidence.UnmatchedTracks = append(evidence.UnmatchedTracks, track)
		}
	}
	if len(evidence.UnmatchedTracks) > 0 {
		evidence.Problems = append(evidence.Problems,
			fmt.Sprintf("%d catalogue track(s) have no matching downloaded file",
				len(evidence.UnmatchedTracks)))
	}
	if summary := summarize(evidence); summary != "" {
		return nil, &validationError{message: summary, evidence: evidence}
	}
	return result, nil
}

// read gathers everything one downloaded file has to say about itself. It
// answers only whether the file is there, whole, and readable; which track it
// is comes later, from the evidence as a whole.
//
// A file carrying no tags at all is refused here, because this is the release
// path: tags are how a file claims its track, and a file that can claim
// nothing can only ever be an unmatched problem. The single-file path opts out
// of that through readUntagged — there the audio alone decides, and absent
// tags are silence.
func (importer *Importer) read(
	sourceRoot string, requested db.DownloadRequestFile,
) (inspected, error) {
	return importer.readFile(sourceRoot, requested, false)
}

// readUntagged reads a file for the single-file path, where a file with no
// tags at all is still worth listening to: nothing a tag could have said is
// evidence anyway (ADR 0002), and AcoustID needs only the audio.
func (importer *Importer) readUntagged(
	sourceRoot string, requested db.DownloadRequestFile,
) (inspected, error) {
	return importer.readFile(sourceRoot, requested, true)
}

func (importer *Importer) readFile(
	sourceRoot string, requested db.DownloadRequestFile, untaggedIsSilence bool,
) (inspected, error) {
	file := inspected{
		request:  requested,
		evidence: db.ImportFileEvidence{Name: requested.Name, Problems: []string{}},
		damaged:  true,
		settled:  true,
		track:    -1,
	}
	refuse := func(format string, values ...any) (inspected, error) {
		file.evidence.Problems = append(file.evidence.Problems, fmt.Sprintf(format, values...))
		return file, nil
	}
	if requested.Name == "" || filepath.Base(requested.Name) != requested.Name {
		return refuse("requested filename %q is unsafe", requested.Name)
	}
	file.source = filepath.Join(sourceRoot, requested.Name)
	info, err := os.Lstat(file.source)
	switch {
	case errors.Is(err, os.ErrNotExist):
		return refuse("%q is not in the provider folder", requested.Name)
	case err != nil:
		return file, fmt.Errorf("inspect downloaded file %q: %w", requested.Name, err)
	}
	if !info.Mode().IsRegular() {
		return refuse("%q is not a regular file", requested.Name)
	}
	metadata := importer.inspect(file.source)
	if metadata.Error != "" && !(untaggedIsSilence && metadata.TagsAbsent) {
		return refuse("%q cannot be read as tagged audio: %s", requested.Name, metadata.Error)
	}
	disc := metadata.DiscNumber
	if disc == 0 {
		disc = 1
	}
	file.tags = observedTags(metadata, disc)
	file.evidence.Observed = file.tags
	if metadata.TrackNumber > 0 {
		file.evidence.Position = position(disc, metadata.TrackNumber)
	}
	// The size is checked last so that a file of the wrong length still says
	// which track it was meant to be. It stays a refusal either way: a person
	// can decide what a file is, never that truncated audio is complete.
	if info.Size() != requested.SizeBytes {
		file.evidence.Problems = append(file.evidence.Problems,
			fmt.Sprintf("%q is %d bytes; the selected source promised %d",
				requested.Name, info.Size(), requested.SizeBytes))
		file.settled = false
		return file, nil
	}
	file.damaged, file.settled = false, false
	return file, nil
}

// verifyAcoustically checks each matched file against what its audio actually
// is, and refuses only where the audio contradicts the catalogue.
//
// Every other signal validation has comes from something the file says about
// itself. A file tagged as the right track, of the right length, in the right
// position, containing entirely the wrong music is indistinguishable from a
// correct one until somebody listens. This listens.
//
// Silence is never evidence. AcoustID has not heard most self-released music,
// and an unrecognised recording says nothing at all about the file: it is
// recorded as evidence and changes nothing. Only a positive identification that
// excludes the catalogue's own recording stops an import, because only that is
// the file saying it is something else.
func (importer *Importer) verifyAcoustically(
	ctx context.Context, files []inspected, row db.DownloadImportRow,
) {
	if importer.acoustic == nil || !importer.acoustic.Configured(ctx) {
		return
	}
	for index := range files {
		file := &files[index]
		// Nothing to check against, or already settled on other grounds.
		if file.track < 0 || file.damaged || file.source == "" {
			continue
		}
		expected := row.Tracks[file.track].MusicBrainzRecordingID

		identified, err := importer.acoustic.Identify(ctx, file.source)
		if err != nil {
			// A check that could not run is reported rather than assumed to
			// have passed, but it never refuses the file on its own.
			file.evidence.Acoustic = &db.ImportAcoustic{Unavailable: truncateReason(err.Error())}
			importer.logger.Warn().Err(err).Str("file", file.request.Name).
				Msg("could not identify a downloaded file by its audio")
			continue
		}

		verdict := acousticEvidence(identified)
		file.evidence.Acoustic = verdict

		if expected == nil || len(verdict.RecordingIDs) == 0 {
			continue
		}
		// Read across every cluster, which is the one place in Schall that still
		// is. Here the audio may only ever object — a folder is admitted by the
		// user having chosen it, never by the fingerprint — so an answer naming
		// the catalogue's recording anywhere in it is enough to stop objecting.
		// Narrowing this to the best cluster would block folders that are fetched
		// and imported today, which is not what the cluster boundary was decided
		// for.
		agrees := false
		for _, recordingID := range verdict.RecordingIDs {
			if recordingID == expected.String() {
				agrees = true
				break
			}
		}
		verdict.Agrees = &agrees
		if agrees {
			continue
		}
		// The audio was recognised and is not this recording. That is the file
		// telling us what it is, and it outranks every tag on it.
		file.evidence.Problems = append(file.evidence.Problems, fmt.Sprintf(
			"%q is a different recording than the catalogue track: the audio matches %s",
			file.request.Name, strings.Join(verdict.RecordingIDs, ", ")))
		file.evidence.Resolvable = true
		file.settled = false
	}
}

// acousticEvidence writes down one identification, in both readings of it.
//
// Clusters is the answer with AcoustID's own boundaries intact, which is what
// decides a copy. RecordingIDs is every name in it, flattened and in the order
// the answer arrived, because that is what the review page reads and what
// evidence written before clusters were recorded contains — a copy held last
// week has to go on being readable. Score is the leading cluster's, which is the
// number the likeness floor was always applied to.
func acousticEvidence(identified acoustid.Identification) *db.ImportAcoustic {
	verdict := &db.ImportAcoustic{
		RecordingIDs: []string{},
		Clusters:     make([]db.ImportAcousticCluster, 0, len(identified.Clusters)),
		Damaged:      truncateReason(identified.Damaged),
	}
	seen := make(map[uuid.UUID]bool)
	for _, cluster := range identified.Clusters {
		recorded := db.ImportAcousticCluster{
			ID:           cluster.ID,
			Score:        cluster.Score,
			RecordingIDs: make([]string, 0, len(cluster.Recordings)),
		}
		for _, recordingID := range cluster.Recordings {
			recorded.RecordingIDs = append(recorded.RecordingIDs, recordingID.String())
			if !seen[recordingID] {
				seen[recordingID] = true
				verdict.RecordingIDs = append(verdict.RecordingIDs, recordingID.String())
			}
		}
		verdict.Clusters = append(verdict.Clusters, recorded)
	}
	if len(identified.Clusters) > 0 {
		verdict.Score = identified.Clusters[0].Score
	}
	return verdict
}

// truncateReason keeps a stored explanation to a readable length.
// shortID is the identifier a release is filed under: its MusicBrainz release
// group where there is one, and the catalogue row otherwise. Either is stable
// for the release, which is the only property the folder name needs — a file
// arriving next month has to render the same name as the one that arrived
// today. It is shortened because it is read by people looking at folders.
func shortID(preferred uuid.NullUUID, fallback uuid.UUID) string {
	if preferred.Valid && preferred.UUID != uuid.Nil {
		return preferred.UUID.String()[:8]
	}
	if fallback == uuid.Nil {
		return ""
	}
	return fallback.String()[:8]
}

func truncateReason(value string) string {
	value = strings.TrimSpace(value)
	if len(value) > 200 {
		return value[:200]
	}
	return value
}

// applyDecisions settles the files a reviewer already judged and returns the
// catalogue tracks those judgements spoke for, which matching must not reuse.
// A decision holds only while the file still says what the reviewer saw.
func applyDecisions(files []inspected, row db.DownloadImportRow) map[int]string {
	reserved := make(map[int]string, len(row.Decisions))
	for index := range files {
		file := &files[index]
		decision, decided := row.Decisions[file.request.Name]
		if !decided || file.tags == nil {
			continue
		}
		file.settled = true
		track, known := trackIndexByID(row.Tracks, decision.TrackID)
		if !known {
			file.evidence.Problems = append(file.evidence.Problems, fmt.Sprintf(
				"%q was resolved to a track the selected edition does not contain", file.request.Name))
			file.evidence.Resolvable = !file.damaged
			continue
		}
		file.evidence.Expected = pointerTo(catalogueTags(row.Tracks[track], row))
		if decision.Fingerprint != file.tags.Fingerprint() {
			file.evidence.Problems = append(file.evidence.Problems, fmt.Sprintf(
				"%q no longer says what it said when it was resolved as %q; decide again",
				file.request.Name, row.Tracks[track].Title))
			file.evidence.Resolvable = !file.damaged
			continue
		}
		file.evidence.Decision = &db.ImportFileDecision{
			TrackID: decision.TrackID, TrackTitle: row.Tracks[track].Title,
			Note: decision.Note, DecidedAt: decision.DecidedAt,
		}
		file.track = track
		reserved[track] = file.request.Name
	}
	return reserved
}

// matchRemaining asks the evidence which track each unsettled file is, and
// records the answer — or the reason there is not one — against that file.
func matchRemaining(files []inspected, tracks []db.ImportTags, reserved map[int]string) {
	pool := make([]fileTags, 0, len(files))
	origin := make([]int, 0, len(files))
	for index, file := range files {
		if file.settled || file.tags == nil {
			continue
		}
		pool = append(pool, fileTags{name: file.request.Name, tags: *file.tags})
		origin = append(origin, index)
	}
	for offset, outcome := range matchRelease(pool, tracks, reserved) {
		file := &files[origin[offset]]
		file.evidence.Candidates = outcome.candidates
		if outcome.track < 0 {
			file.evidence.Problems = append(file.evidence.Problems, outcome.problem)
			file.evidence.Resolvable = !file.damaged
			continue
		}
		file.track = outcome.track
		file.evidence.Expected = pointerTo(tracks[outcome.track])
		file.evidence.Match = outcome.match
		// A matched file can still be refused for its bytes, and that refusal
		// is not something a decision could settle.
		file.evidence.Resolvable = len(file.evidence.Problems) > 0 && !file.damaged
	}
}

func trackIndexByID(tracks []db.ImportTrack, id uuid.UUID) (int, bool) {
	for index, track := range tracks {
		if track.ID == id {
			return index, true
		}
	}
	return -1, false
}

// summarize reduces gathered evidence to the one line stored on the request. A
// release that failed for a single reason still reads as that reason; anything
// larger is counted, because the detail belongs in the evidence.
func summarize(evidence *db.ImportEvidence) string {
	unhappyFiles, fileProblems, firstFileProblem := 0, 0, ""
	for _, file := range evidence.Files {
		if len(file.Problems) == 0 {
			continue
		}
		unhappyFiles++
		fileProblems += len(file.Problems)
		if firstFileProblem == "" {
			firstFileProblem = file.Problems[0]
		}
	}
	var summary string
	switch total := fileProblems + len(evidence.Problems); {
	case total == 0:
		return ""
	case unhappyFiles == 0:
		return strings.Join(evidence.Problems, "; ")
	// One file in trouble is reported as what is wrong with it. A release does
	// not read any better for being told that one of one files disagrees.
	case unhappyFiles == 1 && fileProblems == 1:
		summary = firstFileProblem
	case unhappyFiles == 1:
		summary = fmt.Sprintf("%s (and %d more about the same file)",
			firstFileProblem, fileProblems-1)
	default:
		summary = fmt.Sprintf("%d of %d downloaded files do not match the selected edition",
			unhappyFiles, len(evidence.Files))
	}
	if len(evidence.Problems) > 0 {
		summary += "; " + strings.Join(evidence.Problems, "; ")
	}
	return summary
}

func observedTags(metadata library.AudioMetadata, disc int) *db.ImportTags {
	tags := db.ImportTags{
		Artist: metadata.Artist, Album: metadata.Album, Title: metadata.Title,
		DiscNumber: disc, TrackNumber: metadata.TrackNumber,
		DurationMS: metadata.DurationMS, ISRC: metadata.ISRC,
	}
	if metadata.MusicBrainzRecordingID != nil {
		tags.MusicBrainzRecordingID = metadata.MusicBrainzRecordingID.String()
	}
	return &tags
}

// catalogueTags is one catalogue track as matching and review see it. The
// release artist and title belong on this side too: a downloaded file that
// names a different album is then visible as a comparison that disagreed
// rather than as one nobody made.
func catalogueTags(track db.ImportTrack, row db.DownloadImportRow) db.ImportTags {
	tags := db.ImportTags{
		TrackID: track.ID.String(), Artist: row.ArtistName, Album: row.AlbumTitle,
		Title: track.Title, DiscNumber: track.DiscNumber, TrackNumber: track.TrackNumber,
		DurationMS: track.DurationMS, ISRC: track.ISRC,
	}
	if track.MusicBrainzRecordingID != nil {
		tags.MusicBrainzRecordingID = track.MusicBrainzRecordingID.String()
	}
	return tags
}

func pointerTo[Value any](value Value) *Value { return &value }

// copyRelease writes a validated release into the folder it is filed under.
//
// A release is one folder however many times music arrives, so finding that
// folder already there is the ordinary case rather than the mark of a repeat:
// the fourteenth track of an album arrives to find the thirteen before it. A
// folder that is not there yet is assembled beside its name and renamed onto
// it, so no scan ever walks into one half full. Once it is there, a file is the
// largest thing that can arrive at once, and each one is renamed into place the
// same way.
// It also answers what each file becomes on the way in. On an installation
// that asked for smaller copies, a lossless file is re-encoded here — after it
// was proven and before it is filed, which is the only place that order can be
// guaranteed. Every placement is returned, re-encoded or not, so what was
// written is what the rest of the import reads.
func (importer *Importer) copyRelease(
	ctx context.Context,
	destination string,
	files []validatedFile,
) (map[string]db.ImportedDownloadFile, []transcode.Placement, error) {
	placed, cleanupWork := importer.plan(ctx, files)
	defer cleanupWork()

	// The folder used to be new every time, and an exclusive create was what
	// caught two files wearing one name. A folder that already holds music
	// cannot catch it, so the release is asked about itself before anything is
	// written. It is asked about the names it will write rather than the names
	// that arrived, because re-encoding changes them.
	if err := oneNameEach(placed); err != nil {
		return nil, nil, err
	}
	if info, err := os.Stat(destination); err == nil && info.IsDir() {
		paths, err := importer.fileInto(ctx, destination, files, placed)
		return paths, placed, err
	} else if err == nil {
		return nil, nil, fmt.Errorf("import destination %q is not a directory", destination)
	} else if !errors.Is(err, os.ErrNotExist) {
		return nil, nil, err
	}
	parent := filepath.Dir(destination)
	if err := os.MkdirAll(parent, 0o755); err != nil {
		return nil, nil, err
	}
	staging := filepath.Join(parent, ".schall-import-"+filepath.Base(destination))
	if err := os.RemoveAll(staging); err != nil {
		return nil, nil, err
	}
	if err := os.Mkdir(staging, 0o755); err != nil {
		return nil, nil, err
	}
	cleanup := true
	defer func() {
		if cleanup {
			_ = os.RemoveAll(staging)
		}
	}()
	for _, placement := range placed {
		if err := ctx.Err(); err != nil {
			return nil, nil, err
		}
		target := filepath.Join(staging, placement.Name)
		if err := filecopy.Copy(placement.Source, target); err != nil {
			return nil, nil, err
		}
	}
	if err := os.Rename(staging, destination); err != nil {
		return nil, nil, err
	}
	cleanup = false
	paths, err := existingLocalPaths(destination, files, placed)
	return paths, placed, err
}

// plan asks what each proven file should be written as. Without a transcoder
// configured, and on every installation that has not asked for smaller copies,
// the answer is the file exactly as it arrived.
func (importer *Importer) plan(
	ctx context.Context, files []validatedFile,
) ([]transcode.Placement, func()) {
	requests := make([]transcode.Request, len(files))
	for index, file := range files {
		requests[index] = transcode.Request{
			Source: file.source, Name: file.request.Name, SizeBytes: file.request.SizeBytes,
		}
	}
	return importer.transcoder.Plan(ctx, requests)
}

// recordPlacements notes every file that was written smaller than it arrived,
// and every re-encode the policy asked for that did not happen.
//
// It runs before the import is completed, because completing it queues the
// scan that creates the library rows these notes are for. It can fail nothing:
// the music is in the library either way. What is written here is what lets the
// file row say
// the copy used to be a FLAC and how much room that saved — and, for a
// re-encode that did not happen, the only place that says why the setting
// appeared to do nothing.
func (importer *Importer) recordPlacements(
	ctx context.Context,
	requestID uuid.UUID,
	placed []transcode.Placement,
	localPaths map[string]db.ImportedDownloadFile,
	files []validatedFile,
) {
	for index, placement := range placed {
		if placement.Note != "" {
			if err := importer.store.RecordTranscodeRefused(
				ctx, requestID, placement.Note,
			); err != nil {
				importer.logger.Error().Err(err).Str("request_id", requestID.String()).
					Msg("a re-encode that did not happen could not be recorded")
			}
		}
		if !placement.Transcoded() || index >= len(files) {
			continue
		}
		imported, known := localPaths[files[index].request.Path]
		if !known {
			continue
		}
		detail := fmt.Sprintf("%s was written as %s: %s became %s.",
			files[index].request.Name, placement.Name,
			byteCount(placement.OriginalSizeBytes), byteCount(placement.SizeBytes))
		if err := importer.store.RecordTranscode(ctx, requestID, db.RecordTranscodeParams{
			LocalPath:       imported.LocalPath,
			SourceFormat:    placement.FromFormat,
			SourceSizeBytes: placement.OriginalSizeBytes,
			TargetFormat:    transcode.Format(placement.Name),
			Detail:          detail,
		}); err != nil {
			importer.logger.Error().Err(err).Str("request_id", requestID.String()).
				Msg("a re-encoded copy could not be recorded")
		}
	}
}

// byteCount writes a size the way a sentence in the import history should read
// it. Powers of a thousand, because that is what a disc is sold in and what the
// rest of the interface says.
func byteCount(size int64) string {
	const unit = 1000
	if size < unit {
		return fmt.Sprintf("%d B", size)
	}
	value, exponent := float64(size)/unit, 0
	for value >= unit && exponent < 3 {
		value /= unit
		exponent++
	}
	return fmt.Sprintf("%.1f %s", value, []string{"kB", "MB", "GB", "TB"}[exponent])
}

// fileInto copies a release into a folder that already holds music.
//
// A name already taken is never written over. Where the file there is the file
// this import would write, the import is arriving a second time and the copy
// that landed first is the copy. Where it is anything else, another file is
// wearing the name, and which one the library keeps is a question for a person
// rather than something a copy settles by overwriting.
//
// The bytes are read rather than the size trusted. A folder is shared now, so
// the file under this name may be one another import placed, and two different
// recordings can be the same length; accepting one of them as this copy would
// record provenance pointing at somebody else's file and quietly import
// nothing.
func (importer *Importer) fileInto(
	ctx context.Context, destination string,
	files []validatedFile, placed []transcode.Placement,
) (map[string]db.ImportedDownloadFile, error) {
	for _, placement := range placed {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		localPath := filepath.Join(destination, placement.Name)
		info, err := os.Lstat(localPath)
		switch {
		case errors.Is(err, os.ErrNotExist):
			if err := filecopy.Place(placement.Source, localPath, ".schall-import-"); err != nil {
				return nil, err
			}
			continue
		case err != nil:
			return nil, err
		case !info.Mode().IsRegular() || info.Size() != placement.SizeBytes:
			return nil, fmt.Errorf("existing imported file %q does not match the validated source", localPath)
		}
		same, err := filecopy.SameBytes(placement.Source, localPath)
		if err != nil {
			return nil, err
		}
		if !same {
			return nil, fmt.Errorf("existing imported file %q does not match the validated source", localPath)
		}
	}
	return existingLocalPaths(destination, files, placed)
}

// oneNameEach refuses a release whose own files would land on top of each
// other. Two entries under one name are one file in the library whatever the
// order they are copied in, and nothing about a folder answers which of them it
// should have been.
func oneNameEach(placed []transcode.Placement) error {
	taken := make(map[string]struct{}, len(placed))
	for _, placement := range placed {
		if _, seen := taken[placement.Name]; seen {
			return fmt.Errorf("two files in this release are both called %q", placement.Name)
		}
		taken[placement.Name] = struct{}{}
	}
	return nil
}

func existingLocalPaths(
	destination string, files []validatedFile, placed []transcode.Placement,
) (map[string]db.ImportedDownloadFile, error) {
	result := make(map[string]db.ImportedDownloadFile, len(files))
	for index, file := range files {
		placement := placed[index]
		localPath := filepath.Join(destination, placement.Name)
		info, err := os.Lstat(localPath)
		if err != nil {
			return nil, err
		}
		if !info.Mode().IsRegular() || info.Size() != placement.SizeBytes {
			return nil, fmt.Errorf("existing imported file %q does not match the validated source", localPath)
		}
		result[file.request.Path] = db.ImportedDownloadFile{
			LocalPath: localPath,
			TrackID:   file.trackID,
		}
	}
	return result, nil
}

func containedDirectory(root, candidate string) (string, error) {
	root, err := filepath.Abs(root)
	if err != nil {
		return "", err
	}
	candidate, err = filepath.EvalSymlinks(candidate)
	if err != nil {
		return "", fmt.Errorf("resolve downloaded folder: %w", err)
	}
	relative, err := filepath.Rel(root, candidate)
	if err != nil || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
		return "", errors.New("downloaded folder escapes the configured inbox")
	}
	info, err := os.Stat(candidate)
	if err != nil {
		return "", err
	}
	if !info.IsDir() {
		return "", errors.New("downloaded folder is not a directory")
	}
	return candidate, nil
}

// validationError is a durable review decision rather than a retryable
// failure. Evidence is nil when validation could not get as far as comparing
// files against the catalogue.
type validationError struct {
	message  string
	evidence *db.ImportEvidence
}

func (err *validationError) Error() string { return err.message }
func invalidf(format string, values ...any) error {
	return &validationError{message: fmt.Sprintf(format, values...)}
}

func sameText(left, right string) bool { return tagmatch.SameText(left, right) }

func identityText(value string) string { return tagmatch.Text(value) }

func position(disc, track int) string { return fmt.Sprintf("%d-%d", disc, track) }
func abs(value int) int {
	if value < 0 {
		return -value
	}
	return value
}

func safeComponent(value string) string {
	value = strings.Map(func(symbol rune) rune {
		if symbol == '/' || symbol == '\\' || unicode.IsControl(symbol) {
			return '_'
		}
		return symbol
	}, strings.TrimSpace(value))
	value = strings.Trim(value, ". ")
	if value == "" {
		return "Unknown"
	}
	return value
}

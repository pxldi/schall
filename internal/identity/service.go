package identity

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/pxldi/schall/internal/db"
	"github.com/rs/zerolog"
)

// ErrNoDecision reports that there was no manual decision to withdraw.
var ErrNoDecision = errors.New("no decision has been recorded for this file")

// ErrUnknownCandidate reports that a file was asked to accept a recording that
// is not among the candidates found for it. Accepting anything else would let
// the UI invent an identity the provider never offered.
var ErrUnknownCandidate = errors.New("that recording is not a candidate for this file")

// ErrDecidedByHand reports that a person has already recorded what a file is.
// Their decision is permanent, so nothing automatic writes over it.
var ErrDecidedByHand = errors.New("a person has already decided what this file is")

// Matcher re-evaluates one file against the catalogue. Resolution calls it
// after learning a recording ID, because a file whose identity is now known may
// map automatically to a catalogue track that was already there.
type Matcher interface {
	ReconcileFile(context.Context, uuid.UUID) error
}

// UploadReplacer is how an uploaded file that turned out to be a better copy
// of music the library already holds replaces it.
type UploadReplacer interface {
	ReplaceOwnedWithUpload(ctx context.Context, uploadedFileID uuid.UUID) (bool, error)
}

// ErrFingerprintUnreadable reports that a fingerprint kept for a file is not one
// the listener can use. It is a statement about the token that was stored and
// never about the audio, so the answer is to compute it again rather than to give
// up on the file.
var ErrFingerprintUnreadable = errors.New("the kept fingerprint cannot be read")

// Listener is what a file's audio is put to when nothing written on the file
// could say what it is. It is optional: an installation without one resolves
// exactly the files it always did, from exactly the tags it always read.
//
// The two halves are separate because they cost different things. Computing a
// fingerprint decodes the audio and takes seconds of CPU; looking one up is a
// request to a service that rate-limits. A fingerprint is derived from the audio
// and nothing else, so it is worth keeping — which is why it crosses here as an
// opaque token rather than as anything this package reads. What is inside it is
// the listener's business, and a token this package hands back is one it was
// handed.
type Listener interface {
	// Configured reports whether listening is switched on and possible. Off means
	// this does nothing at all, and quietly: a file is left exactly as its tags
	// left it, with no trace of a check that never ran.
	Configured(ctx context.Context) bool
	// Fingerprint computes one file's fingerprint. An empty token with no error is
	// audio that could not be decoded, which is a fact about the bytes on disk and
	// will not read differently next time — so it is silence rather than a failure
	// to retry.
	Fingerprint(ctx context.Context, path string) (string, error)
	// Lookup asks what a fingerprint is. A nil answer means nobody listened; an
	// answer with no clusters in it means somebody did and the database had never
	// heard the music, which is a different thing and graded as such.
	Lookup(ctx context.Context, fingerprint string) (*Acoustic, error)
}

// Service resolves what independently acquired files are.
//
// It exists because a file nobody followed an artist for was, until now,
// indistinguishable from a file whose matching had failed. Resolution asks the
// providers directly, without requiring anyone to follow anything first, and
// records either one proven identity or the candidates behind the question.
type Service struct {
	pool           *pgxpool.Pool
	resolver       *Resolver
	matcher        Matcher
	listener       Listener
	uploadReplacer UploadReplacer
	logger         zerolog.Logger
}

func NewService(pool *pgxpool.Pool, provider Provider, logger zerolog.Logger, matchers ...Matcher) *Service {
	// The resolver remembers merges against the same database this service
	// reads, so what the provider says about a retired identifier reaches the
	// two comparisons that cannot ask the provider themselves.
	service := &Service{
		pool:     pool,
		resolver: NewResolver(provider).WithAliases(db.New(pool)),
		logger:   logger,
	}
	if len(matchers) > 0 {
		service.matcher = matchers[0]
	}
	return service
}

// WithListener registers what a file's audio is put to. Without one, resolution
// reads tags and stops there, which is what it did before anything could listen.
func (service *Service) WithListener(listener Listener) *Service {
	service.listener = listener
	return service
}

// WithUploadReplacer registers what checks an automatically identified upload
// against the copies the library already holds.
func (service *Service) WithUploadReplacer(replacer UploadReplacer) *Service {
	service.uploadReplacer = replacer
	return service
}

// fileRow is one library file as resolution sees it.
type fileRow struct {
	evidence Evidence
	// path is where the audio is, and fingerprint is what was computed from it
	// last time, empty when nothing has been. They are read together because the
	// second is only ever worth having while it still describes the first.
	path        string
	fingerprint string
	missing     bool
	matched     bool
	// decided marks a file whose identity a person settled, or whose source
	// identity its want's anchor proved. A manual decision is permanent: the
	// user should never be asked the same question twice.
	decided bool
	// proven marks a file that already carries an identity and whose resolution
	// is settled — the shape acquisition leaves behind when the audio proved a
	// copy. The proof outranks anything the providers could conclude from tags.
	proven bool
}

// Resolve asks the providers what one file is and records the answer.
//
// A provider that cannot be reached is returned as an error so the job that
// called this can retry; everything else is an answer, including the answer
// that no external identity exists.
func (service *Service) Resolve(ctx context.Context, fileID uuid.UUID) error {
	file, err := service.load(ctx, fileID)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	switch {
	case file.missing || file.decided:
		return nil
	case file.proven:
		// The identity was written by acquisition when the audio proved the
		// copy, and this job is the one the scan queued before that proof
		// landed. The providers know this file only by its tags — routinely a
		// peer's junk — so asking them again could only replace proof with
		// silence, which is the deletion ADR 0002 exists to forbid.
		return nil
	case file.matched:
		// A file mapped to a catalogue track already has a proven identity, and
		// the mapping is where it is recorded. Only the status is written here.
		// The ordinary store deletes every identity nobody decided by hand
		// before writing what this attempt concluded, and this attempt concluded
		// nothing — so storing threw the proof away and left the file resolved
		// with no identity at all, which is the deletion ADR 0002 forbids. An
		// acquired file reaches this branch on the ordinary path: the tagging
		// pass rewrites its tags, the next scan sees bytes it did not record and
		// puts the file back in the resolution queue, and by then matching has
		// mapped it. A want holding such a file read the null as the library
		// calling the file something else, and stopped.
		return service.recordMatched(ctx, fileID)
	}

	outcome, err := service.resolver.Resolve(ctx, file.evidence)
	if err != nil {
		return err
	}
	outcome, err = service.listenFor(ctx, fileID, file, outcome)
	if err != nil {
		return err
	}
	return service.store(ctx, fileID, outcome)
}

// listenFor puts the audio of a file the tags could not identify to the listener,
// and resolves it again with what the audio came to added to its evidence.
//
// It runs at exactly two outcomes, and both are the reading having got nowhere: a
// file nothing could be found for at all (local_only, which is also where a file
// with no tags on it lands), and a file with several possible answers and none
// conclusive (needs_review). A file the tags already identified is not listened to
// and not re-graded — the audio could only overturn a decision that is already
// made, and this is meant to be a change nothing working today can be flipped by.
// A conflict is left alone from the other side: it is a question already in front
// of a person, and answering it with the audio is a decision of its own.
//
// An installation with no listener, or one with the check switched off, does none
// of this and does it quietly. The outcome handed in is the outcome handed back.
func (service *Service) listenFor(
	ctx context.Context, fileID uuid.UUID, file fileRow, outcome Outcome,
) (Outcome, error) {
	if service.listener == nil || file.path == "" {
		return outcome, nil
	}
	if outcome.Status != StatusLocalOnly && outcome.Status != StatusNeedsReview {
		return outcome, nil
	}
	if !service.listener.Configured(ctx) {
		return outcome, nil
	}
	acoustic, err := service.identify(ctx, fileID, file)
	if err != nil {
		return Outcome{}, err
	}
	// Silence is an answer about the database rather than about the file: AcoustID
	// has never heard most self-released music, and a file it cannot name is left
	// exactly as the tags left it. Resolving again would put the same questions to
	// the same provider and get the same answer, at the price of the requests.
	if acoustic.best() == nil {
		return outcome, nil
	}
	file.evidence.Acoustic = acoustic
	return service.resolver.Resolve(ctx, file.evidence)
}

// identify computes the file's fingerprint if it has none kept, and asks the
// listener what it is.
func (service *Service) identify(
	ctx context.Context, fileID uuid.UUID, file fileRow,
) (*Acoustic, error) {
	print, err := service.fingerprintOf(ctx, fileID, file.path, file.fingerprint)
	if err != nil || print == "" {
		return nil, err
	}
	acoustic, err := service.listener.Lookup(ctx, print)
	if errors.Is(err, ErrFingerprintUnreadable) && file.fingerprint != "" {
		// What was kept is not a fingerprint this listener can use, which says
		// nothing about the audio. Compute it once more and ask again; a second
		// unreadable token is the listener's problem and is reported as one.
		if print, err = service.fingerprintOf(ctx, fileID, file.path, ""); err != nil || print == "" {
			return nil, err
		}
		acoustic, err = service.listener.Lookup(ctx, print)
	}
	if err != nil {
		return nil, fmt.Errorf("identify the audio of file %s: %w", fileID, err)
	}
	return acoustic, nil
}

// fingerprintOf returns the fingerprint kept for a file, computing and keeping
// one when there is none.
//
// It is kept because it costs seconds of decoding and describes the audio and
// nothing else. So it survives every tag Schall writes — the tagger records the
// size and time it wrote, the next scan therefore sees an unchanged file, and
// only a scan that finds bytes it did not record throws the fingerprint away.
func (service *Service) fingerprintOf(
	ctx context.Context, fileID uuid.UUID, path, kept string,
) (string, error) {
	if kept != "" {
		return kept, nil
	}
	computed, err := service.listener.Fingerprint(ctx, path)
	if err != nil {
		return "", fmt.Errorf("fingerprint file %s: %w", fileID, err)
	}
	if computed == "" {
		return "", nil
	}
	if _, err := service.pool.Exec(ctx, `
		UPDATE library_files SET fingerprint = $2, updated_at = now() WHERE id = $1
	`, fileID, computed); err != nil {
		return "", fmt.Errorf("keep the fingerprint of file %s: %w", fileID, err)
	}
	return computed, nil
}

func (service *Service) load(ctx context.Context, fileID uuid.UUID) (fileRow, error) {
	var row fileRow
	var recordingID uuid.NullUUID
	var isrc, artist, album, title, fingerprint pgtype.Text
	var duration pgtype.Int4
	err := service.pool.QueryRow(ctx, `
		SELECT library_files.musicbrainz_recording_id, library_files.isrc,
		       library_files.artist_tag, library_files.album_tag,
		       library_files.title_tag, library_files.duration_ms,
		       library_files.path, library_files.fingerprint,
		       library_files.missing_at IS NOT NULL,
		       EXISTS (SELECT 1 FROM track_mappings
		               WHERE track_mappings.library_file_id = library_files.id),
		       -- A source identity is decided whoever wrote it. A person pastes
		       -- the address, or the want's anchor proved the audio (ADR 0026,
		       -- 0037), and asking MusicBrainz about the file's tags could only
		       -- replace that with a weaker answer.
		       EXISTS (SELECT 1 FROM library_file_identities
		               WHERE library_file_identities.library_file_id = library_files.id
		                 AND (library_file_identities.is_manual
		                      OR library_file_identities.kind = 'source')),
		       library_files.resolution_status = 'resolved'
		       AND EXISTS (SELECT 1 FROM library_file_identities
		                   WHERE library_file_identities.library_file_id = library_files.id)
		FROM library_files
		WHERE library_files.id = $1
	`, fileID).Scan(
		&recordingID, &isrc, &artist, &album, &title, &duration,
		&row.path, &fingerprint,
		&row.missing, &row.matched, &row.decided, &row.proven,
	)
	if err != nil {
		return fileRow{}, err
	}
	if recordingID.Valid {
		row.evidence.RecordingID = recordingID.UUID
	}
	row.fingerprint = fingerprint.String
	row.evidence.Subject = SubjectFile
	row.evidence.ISRC = isrc.String
	row.evidence.Artist = artist.String
	row.evidence.Album = album.String
	row.evidence.Title = title.String
	row.evidence.DurationMS = int(duration.Int32)
	return row, nil
}

// store writes what one attempt concluded. Candidates describe what the
// providers say now and are replaced wholesale; a manual identity is never
// touched, because a person's decision outranks a later attempt.
func (service *Service) store(ctx context.Context, fileID uuid.UUID, outcome Outcome) error {
	transaction, err := service.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return err
	}
	defer func() { _ = transaction.Rollback(ctx) }()

	if _, err := transaction.Exec(ctx, `
		DELETE FROM library_file_candidates WHERE library_file_id = $1
	`, fileID); err != nil {
		return err
	}
	if _, err := transaction.Exec(ctx, `
		DELETE FROM library_file_identities
		WHERE library_file_id = $1 AND NOT is_manual
	`, fileID); err != nil {
		return err
	}
	if outcome.Identity != nil {
		if err := insertIdentity(ctx, transaction, fileID, *outcome.Identity, false); err != nil {
			return err
		}
		if err := queueReleaseGroupIngest(ctx, transaction, outcome.Identity.ReleaseGroupID); err != nil {
			return err
		}
	}
	for _, candidate := range outcome.Candidates {
		if err := insertCandidate(ctx, transaction, fileID, candidate); err != nil {
			return err
		}
	}
	if _, err := transaction.Exec(ctx, `
		UPDATE library_files
		SET resolution_status = $2, resolution_summary = $3,
		    resolution_attempted_at = now(), resolution_error = NULL,
		    updated_at = now()
		WHERE id = $1
	`, fileID, outcome.Status, outcome.Summary); err != nil {
		return err
	}
	if outcome.Identity != nil {
		// The library has just said what this file is, and two kinds of want were
		// waiting for exactly that. One holds this file and stopped because the
		// library called it something else; it gets its next look back and is
		// judged again by the rules a want reaching that state today is judged by.
		// The others hold copies nobody could decide about of this same recording,
		// and this file is now a witness they were judged without.
		//
		// It runs after the status is written and not beside the identity, because
		// a file only speaks for a recording once the library has finished with it:
		// QueueJudgingForSettledWitness reads resolution_status, and asking before
		// the write queued nothing for a file that had just been resolved.
		//
		// Neither admits anything. The first re-runs the filing rules, the second
		// re-runs ImportOne, and both reach whatever they would have reached had
		// this file been settled when the copy arrived.
		if err := RearmWantsHoldingFiles(ctx, transaction,
			[]uuid.UUID{fileID}, db.RearmedByAResolutionSummary); err != nil {
			return err
		}
		if err := db.New(transaction).QueueJudgingForSettledWitness(
			ctx, outcome.Identity.RecordingID, time.Now(),
		); err != nil {
			return err
		}
	}
	if err := transaction.Commit(ctx); err != nil {
		return err
	}
	if outcome.Identity != nil {
		reconcileErr := service.reconcile(ctx, fileID)
		service.replaceUploaded(ctx, fileID)
		if reconcileErr != nil {
			return reconcileErr
		}
	}
	return nil
}

// recordMatched writes down that a file mapped to a catalogue track has been
// answered, and touches nothing else.
//
// The candidates go, because the question they belong to is settled. Whatever
// identity the file carries stays exactly where it is: the mapping is a proof of
// its own, and deleting the row beside it would take away the only thing an
// acquired file has to say what its audio was proven to be.
func (service *Service) recordMatched(ctx context.Context, fileID uuid.UUID) error {
	const summary = "The file is matched to a catalogue track, which proves its identity."
	transaction, err := service.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return err
	}
	defer func() { _ = transaction.Rollback(ctx) }()
	if _, err := transaction.Exec(ctx, `
		DELETE FROM library_file_candidates WHERE library_file_id = $1
	`, fileID); err != nil {
		return err
	}
	if _, err := transaction.Exec(ctx, `
		UPDATE library_files
		SET resolution_status = $2, resolution_summary = $3,
		    resolution_attempted_at = now(), resolution_error = NULL,
		    updated_at = now()
		WHERE id = $1
	`, fileID, StatusResolved, summary); err != nil {
		return err
	}
	return transaction.Commit(ctx)
}

// replaceUploaded consumes the one-shot upload marker before asking the
// library to compare the newly proven file. A failed check must not run again
// when a later resolution revisits the same file.
func (service *Service) replaceUploaded(ctx context.Context, fileID uuid.UUID) {
	if service.uploadReplacer == nil {
		return
	}
	transaction, err := service.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		service.logger.Warn().Err(err).Str("file_id", fileID.String()).
			Msg("begin clearing the upload marker before checking for a better copy")
		return
	}
	defer func() { _ = transaction.Rollback(ctx) }()

	var uploadID uuid.UUID
	err = transaction.QueryRow(ctx, `
		SELECT arrived_by_upload_id FROM library_files
		WHERE id = $1 AND arrived_by_upload_id IS NOT NULL
		FOR UPDATE
	`, fileID).Scan(&uploadID)
	if errors.Is(err, pgx.ErrNoRows) {
		return
	}
	if err != nil {
		service.logger.Warn().Err(err).Str("file_id", fileID.String()).
			Msg("read the upload marker before checking for a better copy")
		return
	}
	if _, err := transaction.Exec(ctx, `
		UPDATE library_files SET arrived_by_upload_id = NULL, updated_at = now()
		WHERE id = $1
	`, fileID); err != nil {
		service.logger.Warn().Err(err).Str("file_id", fileID.String()).
			Msg("clear the upload marker before checking for a better copy")
		return
	}
	if err := transaction.Commit(ctx); err != nil {
		service.logger.Warn().Err(err).Str("file_id", fileID.String()).
			Msg("commit clearing the upload marker before checking for a better copy")
		return
	}
	if _, err := service.uploadReplacer.ReplaceOwnedWithUpload(ctx, fileID); err != nil {
		service.logger.Warn().Err(err).Str("file_id", fileID.String()).
			Msg("check whether an uploaded file replaces a worse copy")
	}
}

// Fail records that the provider could not be asked. It is called once the job
// has stopped retrying, so that a file is not left looking pending forever.
func (service *Service) Fail(ctx context.Context, fileID uuid.UUID, detail string) error {
	_, err := service.pool.Exec(ctx, `
		UPDATE library_files
		SET resolution_status = 'failed', resolution_error = $2,
		    resolution_summary = 'The metadata provider could not be reached, so this file has not been asked about yet.',
		    resolution_attempted_at = now(), updated_at = now()
		WHERE id = $1
	`, fileID, detail)
	return err
}

// Accept records a person's choice among the candidates. It is permanent: later
// attempts leave it alone, and the file is never asked about again unless the
// decision is withdrawn.
func (service *Service) Accept(ctx context.Context, fileID, recordingID uuid.UUID) error {
	transaction, err := service.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return err
	}
	defer func() { _ = transaction.Rollback(ctx) }()

	var identity Identity
	var releaseGroup uuid.NullUUID
	var release, isrc pgtype.Text
	var duration pgtype.Int4
	err = transaction.QueryRow(ctx, `
		SELECT musicbrainz_recording_id, musicbrainz_release_group_id, artist_name,
		       release_title, track_title, duration_ms, isrc
		FROM library_file_candidates
		WHERE library_file_id = $1 AND musicbrainz_recording_id = $2
	`, fileID, recordingID).Scan(
		&identity.RecordingID, &releaseGroup, &identity.ArtistName,
		&release, &identity.TrackTitle, &duration, &isrc,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrUnknownCandidate
	}
	if err != nil {
		return err
	}
	if releaseGroup.Valid {
		identity.ReleaseGroupID = releaseGroup.UUID
	}
	identity.ReleaseTitle, identity.ISRC = release.String, isrc.String
	if duration.Valid {
		value := int(duration.Int32)
		identity.DurationMS = &value
	}
	identity.Method = "manual"
	identity.Confidence = 1
	identity.Evidence = []string{}
	identity.Summary = fmt.Sprintf("Resolved by hand to %q by %s.",
		identity.TrackTitle, credited(identity.ArtistName))

	fallen, err := replaceIdentity(
		ctx, transaction, fileID, &identity, true, StatusResolved, identity.Summary)
	if err != nil {
		return err
	}
	if err := queueReleaseGroupIngest(ctx, transaction, identity.ReleaseGroupID); err != nil {
		return err
	}
	// A want may be waiting on exactly this answer. Its copy is on the disc and
	// the library called that file another recording, so the want stopped: it has
	// no time to be looked at again and nothing automatic was ever going to give
	// it one. Saying what the file is gives it one, in the same transaction as
	// the answer, and the sweep that follows settles the want against the file
	// already there. Everything that fell with the old decision is rearmed the
	// same way: a want may hold one of those files rather than this one.
	if err := RearmWantsHoldingFiles(ctx, transaction,
		append([]uuid.UUID{fileID}, fallen...), db.RearmedByADecisionSummary); err != nil {
		return err
	}
	if err := transaction.Commit(ctx); err != nil {
		return err
	}
	return service.reconcileAll(ctx, append([]uuid.UUID{fileID}, fallen...))
}

// AcceptAsWanted records a person's decision that one library file is the
// recording a wishlist entry asked for.
//
// It answers the one question the review queue asks about a stopped want. The
// copy was fetched, proven and imported; the library then filed the file it
// became under a different MusicBrainz recording; and nothing automatic may
// choose between the two. The person reads both credits and says which it is.
//
// Nothing proved this and nothing here pretends otherwise. It is written down as
// a manual decision, with no evidence beside it, and it is permanent like every
// other manual decision. The recording it names is the want's own, which is
// where the copy was checked against in the first place.
//
// It is not Accept. Accept chooses among the candidates a provider offered, and
// a file the library has already filed has none: writing that answer deleted
// them. The recording is described from the catalogue instead.
func (service *Service) AcceptAsWanted(ctx context.Context, fileID, recordingID uuid.UUID) error {
	if service.resolver == nil {
		return fmt.Errorf("describe recording %s: no catalogue is configured", recordingID)
	}
	recording, err := service.resolver.Recording(ctx, recordingID)
	if err != nil {
		return fmt.Errorf("look up recording %s: %w", recordingID, err)
	}
	described := describe(Evidence{}, recording)
	chosen := Identity{
		RecordingID:    described.RecordingID,
		ReleaseGroupID: described.ReleaseGroupID,
		ArtistName:     described.ArtistName,
		ReleaseTitle:   described.ReleaseTitle,
		TrackTitle:     described.TrackTitle,
		DurationMS:     described.DurationMS,
		ISRC:           described.ISRC,
		Method:         "manual",
		Confidence:     1,
		Evidence:       []string{},
	}
	chosen.Summary = fmt.Sprintf("Resolved by hand to %q by %s, the recording a wishlist entry asked for.",
		chosen.TrackTitle, credited(chosen.ArtistName))

	transaction, err := service.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return err
	}
	defer func() { _ = transaction.Rollback(ctx) }()

	fallen, err := replaceIdentity(
		ctx, transaction, fileID, &chosen, true, StatusResolved, chosen.Summary)
	if err != nil {
		return err
	}
	if err := queueReleaseGroupIngest(ctx, transaction, chosen.ReleaseGroupID); err != nil {
		return err
	}
	// The want this was answered for is one of these, and the sweep that follows
	// settles it against the file already on the disc. Everything that fell with
	// the old decision is rearmed the same way: a want may hold one of those
	// files rather than this one.
	if err := RearmWantsHoldingFiles(ctx, transaction,
		append([]uuid.UUID{fileID}, fallen...), db.RearmedByADecisionSummary); err != nil {
		return err
	}
	if err := transaction.Commit(ctx); err != nil {
		return err
	}
	return service.reconcileAll(ctx, append([]uuid.UUID{fileID}, fallen...))
}

// FileAsAcquired files a library file under the recording a want's copy was
// proven to be.
//
// It is called at one moment: a want's copy is on the disc and the library does
// not call the file the recording the want names. Three things reach it, and the
// proof says which. MusicBrainz merged the two rows. Or something proved the
// copy is the want's recording and the row the library used turned out to be the
// same registered track (ADR 0025, 0031). Or the library wrote nothing
// down at all, and the hole is filled with the recording the copy was proven to
// be. The caller establishes all three with the resolver, which is the only thing
// entitled to; this writes the answer down.
//
// Nothing is decided here, and nothing is loosened. The evidence is the caller's
// and is copied rather than composed: the old version wrote "the published sample
// of this recording" and "ISRC" onto every row it made, which was true on 0025's
// code branch and invented on the branch where the wanted row carries no code at
// all.
//
// A file somebody decided about by hand is refused with ErrDecidedByHand. Their
// answer is permanent, and a want that both rows satisfy is settled against
// their file rather than over their decision.
func (service *Service) FileAsAcquired(
	ctx context.Context, fileID, recordingID uuid.UUID, proof FilingProof,
) error {
	recording, err := service.resolver.Recording(ctx, recordingID)
	if err != nil {
		return fmt.Errorf("look up recording %s: %w", recordingID, err)
	}
	described := describe(Evidence{}, recording)
	filed := Identity{
		RecordingID:    described.RecordingID,
		ReleaseGroupID: described.ReleaseGroupID,
		ArtistName:     described.ArtistName,
		ReleaseTitle:   described.ReleaseTitle,
		TrackTitle:     described.TrackTitle,
		DurationMS:     described.DurationMS,
		ISRC:           described.ISRC,
		Method:         MethodSameRegistration,
		Confidence:     1,
		Evidence:       filingEvidence(proof),
	}
	filed.Summary = fmt.Sprintf("Filed as %q by %s, the recording a wishlist entry's copy was proven to be.",
		filed.TrackTitle, credited(filed.ArtistName))

	transaction, err := service.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return err
	}
	defer func() { _ = transaction.Rollback(ctx) }()

	// Asked inside the transaction rather than by the caller, because a person
	// deciding between the read and the write would otherwise have their answer
	// replaced. There is nothing to lose by refusing: the two rows are one
	// registration, so their file answers the want either way.
	var manual bool
	err = transaction.QueryRow(ctx, `
		SELECT EXISTS (
			SELECT 1 FROM library_file_identities
			WHERE library_file_id = $1 AND is_manual
		)
	`, fileID).Scan(&manual)
	if err != nil {
		return err
	}
	if manual {
		return ErrDecidedByHand
	}

	// Written as an identity nobody decided by hand. A person's answer is theirs
	// alone, and recording this as one would put a decision in front of them that
	// they never made and could not withdraw as their own.
	fallen, err := replaceIdentity(
		ctx, transaction, fileID, &filed, false, StatusResolved, filed.Summary)
	if err != nil {
		return err
	}
	if err := queueReleaseGroupIngest(ctx, transaction, filed.ReleaseGroupID); err != nil {
		return err
	}
	// Every want that stopped on one of these files is entitled to look again.
	// The one this was done for is settled by its own sweep a moment later; the
	// rest find out what the library says now, which is what a stopped want was
	// waiting for.
	if err := RearmWantsHoldingFiles(ctx, transaction,
		append([]uuid.UUID{fileID}, fallen...), db.RearmedByARefilingSummary); err != nil {
		return err
	}
	if err := transaction.Commit(ctx); err != nil {
		return err
	}
	return service.reconcileAll(ctx, append([]uuid.UUID{fileID}, fallen...))
}

// filingEvidence lists what re-filed a library file, and never more than the
// caller established. A proof carrying neither a method nor a second row writes
// nothing, because a file the library lost its answer for has nothing to show:
// an empty list is the truthful reading, and the sentence that used to stand
// there was not.
func filingEvidence(proof FilingProof) []string {
	var lines []string
	if line := provenEvidence(proof.Method); line != "" {
		lines = append(lines, line)
	}
	switch {
	case proof.Merged:
		lines = append(lines,
			"MusicBrainz merged the recording your library filed this under into this one")
	case proof.SecondRow:
		lines = append(lines,
			"Schall read the recording your library filed this under as the same registered track")
	}
	return lines
}

// provenEvidence names what admitted the copy, in the words the grader's own
// evidence lists use. A method with no wording of its own is named as it stands
// rather than dressed as something it is not, and an empty method says nothing.
func provenEvidence(method string) string {
	base, _ := strings.CutSuffix(method, singledOutByRelease)
	if trimmed, cut := strings.CutSuffix(base, "-and-"+singledOutByExplicitEdition); cut {
		base = trimmed
	}
	switch base {
	case "":
		return ""
	case "published-sample":
		return "audio (matches the published sample of this recording)"
	case "library-witness":
		return "audio (the same music as a file you already own)"
	case "fingerprint", "fingerprint-and-identifier":
		return "audio (identified as this recording)"
	case "manual":
		return "a person's decision about the copy"
	}
	return "what admitted the copy: " + base
}

// MarkLocalOnly records that this file is owned music with no external identity.
// It is the answer to a question the providers cannot settle, and it stops the
// question from being asked again.
func (service *Service) MarkLocalOnly(ctx context.Context, fileID uuid.UUID) error {
	transaction, err := service.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return err
	}
	defer func() { _ = transaction.Rollback(ctx) }()
	const summary = "Recorded by hand as owned music with no external identity."
	fallen, err := replaceIdentity(
		ctx, transaction, fileID, nil, true, StatusLocalOnly, summary)
	if err != nil {
		return err
	}
	// This is an answer about the file too, and a want stopped on it is entitled
	// to be looked at again: it will find the library still unable to name the
	// recording and stop again, saying so, which is a queue that tells the truth
	// rather than one that went quiet. Everything that fell with the old
	// decision is rearmed the same way.
	if err := RearmWantsHoldingFiles(ctx, transaction,
		append([]uuid.UUID{fileID}, fallen...), db.RearmedByADecisionSummary); err != nil {
		return err
	}
	if err := transaction.Commit(ctx); err != nil {
		return err
	}
	return service.reconcileAll(ctx, fallen)
}

// Clear withdraws a manual decision and puts the file back in the queue, so the
// providers are asked about it again from scratch.
func (service *Service) Clear(ctx context.Context, fileID uuid.UUID) error {
	transaction, err := service.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return err
	}
	defer func() { _ = transaction.Rollback(ctx) }()
	tag, err := transaction.Exec(ctx, `
		DELETE FROM library_file_identities
		WHERE library_file_id = $1 AND is_manual
	`, fileID)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrNoDecision
	}
	// Everything this decision admitted goes back with it. A copy was let into
	// the library because its audio agreed with this file, so the decision being
	// gone leaves that copy with nothing proving what it is.
	fallen, err := WithdrawAdmittedBy(ctx, transaction, fileID)
	if err != nil {
		return err
	}
	if _, err := transaction.Exec(ctx, `
		UPDATE library_files
		SET resolution_status = 'pending', resolution_summary = NULL,
		    resolution_error = NULL, updated_at = now()
		WHERE id = $1
	`, fileID); err != nil {
		return err
	}
	// Everything this decision admitted is rearmed the same way: a want may
	// hold one of those files rather than this one.
	if err := RearmWantsHoldingFiles(ctx, transaction,
		append([]uuid.UUID{fileID}, fallen...), db.RearmedByADecisionSummary); err != nil {
		return err
	}
	if err := transaction.Commit(ctx); err != nil {
		return err
	}
	return service.reconcileAll(ctx, append([]uuid.UUID{fileID}, fallen...))
}

// RearmWantsHoldingFiles gives a next look back to every want that stopped
// because its accepted copy became one of these files.
//
// A file that borrowed its identity from another may itself have been the file
// a want's copy landed as: the want held for it because the library called that
// copy a different recording, and it has no time to be looked at again on its
// own. When the file's identity falls — its own decision withdrawn, or the
// decision it was borrowed from withdrawn — that want is exactly as stuck as
// one holding the file whose decision changed directly, and it must be rearmed
// the same way. db.RearmWantsHoldingFile only reads one file's wants, so this
// walks every file the caller touched: the file the decision was about and
// everything that fell with it.
func RearmWantsHoldingFiles(
	ctx context.Context, transaction pgx.Tx, fileIDs []uuid.UUID, summary string,
) error {
	for _, fileID := range fileIDs {
		if err := db.RearmWantsHoldingFile(ctx, transaction, fileID, summary); err != nil {
			return err
		}
	}
	return nil
}

// withdrawnWithItsAncestor is what a file says once the decision it was
// identified on the strength of has been taken back. It is one sentence: what
// happened to this file, and that it is a question again.
const withdrawnWithItsAncestor = "The decision that identified this file was taken back, " +
	"so what this file is has to be answered again."

// WithdrawAdmittedBy takes back every identity that rested on one file, and
// every identity that rested on those, all the way down.
//
// A file can be identified by a file the library already holds: the audio agrees
// with an owned file whose identity was settled, so the copy is the recording
// that file was proven to be. The copy's identity is therefore borrowed, and the
// row it was written into names the file it was borrowed from. When that older
// decision is taken back, what it lent has to go back with it — otherwise a
// person who corrected one mistake would leave every file admitted by it still
// wearing the wrong recording.
//
// The walk is recursive because a file admitted this way may have gone on to
// admit another. Manual decisions are left alone wherever they are met: somebody
// answered for that file themselves, and a manual decision is permanent.
//
// The files are put back into the review queue rather than into the queue of
// files nobody has asked about yet. Nothing has gone wrong with them and nothing
// about them has changed; what changed is that the proof behind them is gone, and
// that is a question for a person.
func WithdrawAdmittedBy(ctx context.Context, transaction pgx.Tx, fileID uuid.UUID) ([]uuid.UUID, error) {
	rows, err := transaction.Query(ctx, `
		WITH RECURSIVE fallen AS (
		    SELECT library_file_id
		    FROM library_file_identities
		    WHERE admitted_by_file_id = $1 AND NOT is_manual
		    UNION
		    SELECT identities.library_file_id
		    FROM library_file_identities identities
		    JOIN fallen ON identities.admitted_by_file_id = fallen.library_file_id
		    WHERE NOT identities.is_manual
		),
		withdrawn AS (
		    DELETE FROM library_file_identities
		    WHERE library_file_id IN (SELECT library_file_id FROM fallen)
		    RETURNING library_file_id
		)
		UPDATE library_files
		SET resolution_status = 'needs_review', resolution_summary = $2,
		    resolution_error = NULL, updated_at = now()
		WHERE id IN (SELECT library_file_id FROM withdrawn)
		RETURNING id
	`, fileID, withdrawnWithItsAncestor)
	if err != nil {
		return nil, fmt.Errorf("withdraw what file %s admitted: %w", fileID, err)
	}
	defer rows.Close()

	var fallen []uuid.UUID
	for rows.Next() {
		var admitted uuid.UUID
		if err := rows.Scan(&admitted); err != nil {
			return nil, err
		}
		fallen = append(fallen, admitted)
	}
	return fallen, rows.Err()
}

// replaceIdentity writes a settled answer over whatever an attempt concluded —
// a person's decision, or the recording a want's copy was proven to be. The
// candidates go with it: they were the question, and the question has been
// answered.
//
// It returns the files that were identified on the strength of the identity it
// replaced, where the answer has changed. Those have lost their proof and are
// questions again — see WithdrawAdmittedBy.
// manual says who settled it. Everything the review screen writes is a person's
// answer and permanent; a file re-filed under the recording a want's copy was
// proven to be is not, and must stay something a later resolution may revisit.
func replaceIdentity(
	ctx context.Context, transaction pgx.Tx, fileID uuid.UUID,
	identity *Identity, manual bool, status, summary string,
) ([]uuid.UUID, error) {
	var exists bool
	if err := transaction.QueryRow(ctx, `
		SELECT EXISTS (SELECT 1 FROM library_files WHERE id = $1 AND missing_at IS NULL)
	`, fileID).Scan(&exists); err != nil {
		return nil, err
	}
	if !exists {
		return nil, pgx.ErrNoRows
	}

	// What this file was said to be until now. A person choosing the same
	// recording again has changed nothing, and nothing that rested on it falls;
	// any other answer withdraws what the old one lent.
	var replaced uuid.NullUUID
	if err := transaction.QueryRow(ctx, `
		SELECT musicbrainz_recording_id FROM library_file_identities
		WHERE library_file_id = $1
	`, fileID).Scan(&replaced); err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return nil, err
	}
	var fallen []uuid.UUID
	if replaced.Valid && (identity == nil || identity.RecordingID != replaced.UUID) {
		withdrawn, err := WithdrawAdmittedBy(ctx, transaction, fileID)
		if err != nil {
			return nil, err
		}
		fallen = withdrawn
	}

	if _, err := transaction.Exec(ctx, `
		DELETE FROM library_file_identities WHERE library_file_id = $1
	`, fileID); err != nil {
		return nil, err
	}
	if _, err := transaction.Exec(ctx, `
		DELETE FROM library_file_candidates WHERE library_file_id = $1
	`, fileID); err != nil {
		return nil, err
	}
	if identity != nil {
		if err := insertIdentity(ctx, transaction, fileID, *identity, manual); err != nil {
			return nil, err
		}
	} else if _, err := transaction.Exec(ctx, `
		INSERT INTO library_file_identities (
			library_file_id, kind, method, confidence, is_manual, summary
		)
		VALUES ($1, 'local_only', 'manual', 1, true, $2)
	`, fileID, summary); err != nil {
		return nil, err
	}
	if _, err := transaction.Exec(ctx, `
		UPDATE library_files
		SET resolution_status = $2, resolution_summary = $3,
		    resolution_error = NULL, resolution_attempted_at = now(), updated_at = now()
		WHERE id = $1
	`, fileID, status, summary); err != nil {
		return nil, err
	}
	return fallen, nil
}

// queueReleaseGroupIngest asks for the release behind a proven identity to be
// brought into the catalogue.
//
// It is what makes resolution worth doing for music nobody follows an artist
// for: until the release is in the catalogue there is no track to map the file
// to, and the proof sits unused. Only a proven identity queues this. Candidates
// are a question, and a question must not put an artist in the catalogue.
//
// A release group the catalogue already holds is not asked about, because
// ingesting it would cost a rate-limited request to learn what is already
// known. The insert is also guarded by a unique partial index, so two files
// from the same album resolving at once queue one ingest between them.
func queueReleaseGroupIngest(ctx context.Context, transaction pgx.Tx, releaseGroupID uuid.UUID) error {
	if releaseGroupID == uuid.Nil {
		return nil
	}
	// The identifier travels as text, because it is compared against a uuid
	// column and stored in a jsonb payload the unique index reads as text.
	_, err := transaction.Exec(ctx, `
		INSERT INTO jobs (kind, payload)
		SELECT 'ingest_release_group', jsonb_build_object('releaseGroupId', $1::text)
		WHERE NOT EXISTS (
		    SELECT 1 FROM albums WHERE albums.musicbrainz_release_group_id = $1::uuid
		)
		ON CONFLICT (kind, (payload->>'releaseGroupId'))
		    WHERE kind = 'ingest_release_group' AND status IN ('queued', 'running')
		DO NOTHING
	`, releaseGroupID.String())
	if err != nil {
		return fmt.Errorf("queue release group ingest %s: %w", releaseGroupID, err)
	}
	return nil
}

func insertIdentity(
	ctx context.Context, transaction pgx.Tx, fileID uuid.UUID, identity Identity, manual bool,
) error {
	evidence, err := json.Marshal(identity.Evidence)
	if err != nil {
		return fmt.Errorf("encode identity evidence: %w", err)
	}
	_, err = transaction.Exec(ctx, `
		INSERT INTO library_file_identities (
			library_file_id, kind, musicbrainz_recording_id,
			musicbrainz_release_group_id, artist_name, release_title, track_title,
			duration_ms, isrc, method, confidence, is_manual, summary, evidence
		)
		VALUES (
			$1, 'external', $2, $3, nullif($4, ''), nullif($5, ''), nullif($6, ''),
			$7, nullif($8, ''), $9, $10, $11, $12, $13::jsonb
		)
	`,
		fileID, identity.RecordingID, nullableUUID(identity.ReleaseGroupID),
		identity.ArtistName, identity.ReleaseTitle, identity.TrackTitle,
		identity.DurationMS, identity.ISRC, identity.Method, identity.Confidence,
		manual, identity.Summary, evidence,
	)
	return err
}

func insertCandidate(ctx context.Context, transaction pgx.Tx, fileID uuid.UUID, candidate Candidate) error {
	_, err := transaction.Exec(ctx, `
		INSERT INTO library_file_candidates (
			library_file_id, musicbrainz_recording_id, musicbrainz_release_group_id,
			artist_name, release_title, track_title, duration_ms, isrc, rank,
			agrees, differs, summary
		)
		VALUES ($1, $2, $3, $4, nullif($5, ''), $6, $7, nullif($8, ''), $9, $10, $11, $12)
		ON CONFLICT (library_file_id, musicbrainz_recording_id) DO NOTHING
	`,
		fileID, candidate.RecordingID, nullableUUID(candidate.ReleaseGroupID),
		candidate.ArtistName, candidate.ReleaseTitle, candidate.TrackTitle,
		candidate.DurationMS, candidate.ISRC, candidate.Rank,
		candidate.Agrees, candidate.Differs, candidate.Summary,
	)
	return err
}

// reconcile lets the matcher look at a file whose identity has just changed. A
// failure here is worth logging rather than undoing the resolution: the
// identity is correct, and matching runs again on the next scan.
// reconcileAll lets the matcher look at every file whose identity has just
// changed: the one that was decided about, and any that lost their identity
// with it.
func (service *Service) reconcileAll(ctx context.Context, fileIDs []uuid.UUID) error {
	for _, fileID := range fileIDs {
		if err := service.reconcile(ctx, fileID); err != nil {
			return err
		}
	}
	return nil
}

func (service *Service) reconcile(ctx context.Context, fileID uuid.UUID) error {
	if service.matcher == nil {
		return nil
	}
	if err := service.matcher.ReconcileFile(ctx, fileID); err != nil {
		return fmt.Errorf("re-evaluate matching for %s: %w", fileID, err)
	}
	return nil
}

func nullableUUID(value uuid.UUID) *uuid.UUID {
	if value == uuid.Nil {
		return nil
	}
	return &value
}

// FileResolution is everything known about one file's external identity.
type FileResolution struct {
	FileID      uuid.UUID
	Status      string
	Summary     string
	Error       string
	AttemptedAt *time.Time
	MatchStatus string
	Identity    *StoredIdentity
	Candidates  []Candidate
}

// StoredIdentity is an identity as it was recorded, including who decided it.
type StoredIdentity struct {
	Identity
	Kind      string
	IsManual  bool
	DecidedAt time.Time
}

// File reads back what is known about one file's identity, so a question can be
// shown together with the evidence behind it.
func (service *Service) File(ctx context.Context, fileID uuid.UUID) (FileResolution, error) {
	result := FileResolution{FileID: fileID, Candidates: []Candidate{}}
	var summary, failure pgtype.Text
	var attemptedAt pgtype.Timestamptz
	err := service.pool.QueryRow(ctx, `
		SELECT resolution_status, resolution_summary, resolution_error,
		       resolution_attempted_at, match_status
		FROM library_files
		WHERE id = $1
	`, fileID).Scan(&result.Status, &summary, &failure, &attemptedAt, &result.MatchStatus)
	if err != nil {
		return FileResolution{}, err
	}
	result.Summary, result.Error = summary.String, failure.String
	if attemptedAt.Valid {
		value := attemptedAt.Time
		result.AttemptedAt = &value
	}

	identity, err := service.identityOf(ctx, fileID)
	if err != nil {
		return FileResolution{}, err
	}
	result.Identity = identity

	rows, err := service.pool.Query(ctx, `
		SELECT musicbrainz_recording_id, musicbrainz_release_group_id, artist_name,
		       release_title, track_title, duration_ms, isrc, rank, agrees, differs,
		       summary
		FROM library_file_candidates
		WHERE library_file_id = $1
		ORDER BY rank
	`, fileID)
	if err != nil {
		return FileResolution{}, err
	}
	defer rows.Close()
	for rows.Next() {
		var candidate Candidate
		var releaseGroup uuid.NullUUID
		var release, isrc pgtype.Text
		var duration pgtype.Int4
		if err := rows.Scan(
			&candidate.RecordingID, &releaseGroup, &candidate.ArtistName,
			&release, &candidate.TrackTitle, &duration, &isrc, &candidate.Rank,
			&candidate.Agrees, &candidate.Differs, &candidate.Summary,
		); err != nil {
			return FileResolution{}, err
		}
		if releaseGroup.Valid {
			candidate.ReleaseGroupID = releaseGroup.UUID
		}
		candidate.ReleaseTitle, candidate.ISRC = release.String, isrc.String
		if duration.Valid {
			value := int(duration.Int32)
			candidate.DurationMS = &value
		}
		result.Candidates = append(result.Candidates, candidate)
	}
	return result, rows.Err()
}

func (service *Service) identityOf(ctx context.Context, fileID uuid.UUID) (*StoredIdentity, error) {
	var stored StoredIdentity
	var recording, releaseGroup uuid.NullUUID
	var artist, release, title, isrc pgtype.Text
	var duration pgtype.Int4
	var confidence pgtype.Float4
	var evidence []byte
	err := service.pool.QueryRow(ctx, `
		SELECT kind, musicbrainz_recording_id, musicbrainz_release_group_id,
		       artist_name, release_title, track_title, duration_ms, isrc,
		       method, confidence, is_manual, summary, evidence, decided_at
		FROM library_file_identities
		WHERE library_file_id = $1
	`, fileID).Scan(
		&stored.Kind, &recording, &releaseGroup, &artist, &release, &title,
		&duration, &isrc, &stored.Method, &confidence, &stored.IsManual,
		&stored.Summary, &evidence, &stored.DecidedAt,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if recording.Valid {
		stored.RecordingID = recording.UUID
	}
	if releaseGroup.Valid {
		stored.ReleaseGroupID = releaseGroup.UUID
	}
	stored.ArtistName, stored.ReleaseTitle = artist.String, release.String
	stored.TrackTitle, stored.ISRC = title.String, isrc.String
	if duration.Valid {
		value := int(duration.Int32)
		stored.DurationMS = &value
	}
	if confidence.Valid {
		stored.Confidence = confidence.Float32
	}
	stored.Evidence = []string{}
	if len(evidence) > 0 {
		if err := json.Unmarshal(evidence, &stored.Evidence); err != nil {
			return nil, fmt.Errorf("decode identity evidence: %w", err)
		}
	}
	return &stored, nil
}

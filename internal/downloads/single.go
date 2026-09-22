package downloads

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/pxldi/schall/internal/chromaprint"
	"github.com/pxldi/schall/internal/db"
	"github.com/pxldi/schall/internal/identity"
	"github.com/pxldi/schall/internal/library"
	"github.com/pxldi/schall/internal/sources"
	"github.com/pxldi/schall/internal/spectrum"
	"github.com/pxldi/schall/internal/tagging"
)

// Verifier answers whether one file is the recording a want is waiting for.
//
// downloads does not know how that is decided and deliberately cannot loosen it:
// the rules live in one grader, and this package only asks. Without a verifier
// nothing fetched for a want is admitted at all, which is the safe direction.
type Verifier interface {
	Verify(
		ctx context.Context, evidence identity.Evidence, recordingID uuid.UUID,
	) (identity.Verification, error)
}

// WithVerifier registers what checks a fetched candidate against the recording it
// was fetched for.
func (importer *Importer) WithVerifier(verifier Verifier) *Importer {
	importer.verifier = verifier
	return importer
}

// Fingerprinter turns a file of audio into the fingerprint it is compared by.
//
// A stated length is the whole reason this is not the listener's fingerprint.
// The listener reads the opening two minutes, which is all AcoustID's answer
// needs; a distributor's excerpt is cut from wherever in the song it liked, so a
// copy has to be read far enough in for the excerpt to be inside it at all.
type Fingerprinter interface {
	Available() bool
	Compute(ctx context.Context, file string, lengthSeconds int) (string, int, error)
}

// WithFingerprinter registers what computes the fingerprint a copy is measured
// against its want's anchor by. Without one no copy is measured, and a copy that
// was not measured is judged exactly as it was before anchors existed.
func (importer *Importer) WithFingerprinter(prints Fingerprinter) *Importer {
	importer.prints = prints
	return importer
}

// AudioMeasurer measures what a copy's audio is, as opposed to what its name
// claims: where the sound stops, and what the file says made it.
//
// It is quality and never identity, and this path treats it as such. Nothing it
// reports is passed to the grader, admits a copy, or refuses one — a transcode
// of the wanted recording is still the wanted recording. It is written into the
// evidence so that a person deciding between two copies in the review queue can
// see which of them is the lesser.
type AudioMeasurer interface {
	Available() bool
	Read(ctx context.Context, path string, sampleRateHz, durationMS int) (spectrum.Reading, error)
}

// WithAudioMeasurer registers what measures a fetched copy's audio. Without one
// the review queue shows exactly what it showed before.
func (importer *Importer) WithAudioMeasurer(measurer AudioMeasurer) *Importer {
	importer.quality = measurer
	return importer
}

// WaveformReader draws the shape of a copy's audio: two hundred numbers the
// review queue's card plots so a person can see where the music is before
// pressing play.
//
// It is a picture and nothing else. Nothing it returns is passed to the grader,
// admits a copy, or refuses one, and a copy with no waveform is judged exactly
// as it was before.
type WaveformReader interface {
	Available() bool
	Peaks(ctx context.Context, path string) ([]byte, error)
}

// WithWaveforms registers what draws a held copy's shape. Without one the card
// shows what it showed before.
func (importer *Importer) WithWaveforms(reader WaveformReader) *Importer {
	importer.shapes = reader
	return importer
}

// The sentences a fetched candidate explains itself with. A file nobody can
// explain is a file nobody trusts, and these are what the review queue will read
// long after the peer's copy is gone.
const (
	acceptedSummary = "Proven to be the wanted recording and imported."
	// A person's acceptance says who decided and does not borrow the word proven.
	// Nothing proved this; somebody listened and said so, which is a different and
	// permanent kind of answer.
	acceptedByHandSummary = "You listened to this copy and said it is the wanted recording. Imported."
	audioRefusedSummary   = "The audio is a different recording. This copy will not be offered for this want again."
	// The anchor's own refusal. It says which witness spoke, because a person
	// reading it may well be able to hear that the copy is something else, and
	// "the audio is a different recording" with AcoustID knowing nothing about
	// the music would read as a check that never ran.
	anchorRefusedSummary = "This copy does not match the published sample of the wanted recording. It will not be offered for this want again."
	tagsRefusedSummary   = "This file's own identifiers contradict the wanted recording. It will not be offered for this want again."
	unlistenedSummary    = "The tags match, but nothing checked the audio, and tags alone cannot admit a file from a stranger. Listen to it and decide."
	// What a copy is told when the only witness that spoke was an upload found by
	// the want's name. Such an upload can be another version of the song, so this
	// is never a refusal, and the sentence says so: a reader who has just been
	// told the audio is different will otherwise read the copy as thrown away.
	uploadUnmatchedSummary = "This copy does not reproduce the upload this want is anchored to, which does not refuse it: an upload found by name can be another version. Listen to it and decide."
	// What a copy is told when the audio named another MusicBrainz row and
	// MusicBrainz could not be asked whether the two rows are one track entered
	// twice. The refusal rests on that answer, so it is not reached.
	registrationUncheckedSummary = "The audio was identified as another recording, and MusicBrainz could not be asked whether that is this track entered twice. Kept as a question."
	// What the want says once it has asked as often as it is going to and still
	// has no answer. It is the want's own sentence, and it names the one thing
	// left to do.
	registrationUnanswerableSummary = "The audio names another recording and MusicBrainz will not say whether that is this track entered twice. Listen to the copy and decide."
	undecidedSummary                = "Nothing could decide whether this copy is the wanted recording. It is kept as a question."
	// What a copy for a want with no recording is told when its own tags
	// disagree with the entry. The audio may match; a contradiction still holds
	// it (ADR 0037 §3).
	entryContradictedSummary = "This file's own tags disagree with the entry. Listen to it and decide."
	// What a copy for a want with no recording is told when it carries a
	// MusicBrainz recording ID. MusicBrainz knows the file, and resolution has
	// not caught up with it yet.
	unresolvedRecordingSummary = "MusicBrainz knows this file, but this entry has no recording yet. Kept as a question."
	// What a copy the audio proved for a keyed want is told when it falls below
	// the want's minimum bit rate (ADR 0038 §7). The want keeps searching.
	belowWantFloorSummary = "This copy is the track, but below this want's minimum bit rate. Accept it, or wait for a better copy."
	undeliveredSummary    = "The bytes did not arrive as offered, so there was nothing to check."
	waitingNextSummary    = "Not in your library yet. The next copy will be tried."
	// What a copy is told when MusicBrainz has deleted or merged away the
	// recording it was fetched to be checked against. Not a judgement about the
	// file: verification never ran, because there was nothing left to check it
	// against.
	recordingGoneSummary = "MusicBrainz no longer has the recording this entry named. The entry will be resolved again."
	// What the want itself says once it has been sent back to resolution for
	// that reason, and the same sentence for an entry a person resolved by
	// hand: their choice is not wrong, MusicBrainz has simply stopped carrying
	// the recording they chose.
	wantRecordingGoneSummary       = "MusicBrainz no longer has the recording this was resolved to. It will be resolved again."
	wantRecordingGoneManualSummary = "You resolved this entry by hand, but MusicBrainz no longer has that recording. It will be resolved again."
	// What the want itself says once a copy has been let in. It is the want's
	// sentence rather than the file's: the music has arrived and only the scan
	// that gives it a library row is still to come.
	importedTargetSummary = "A copy arrived and was proven. It is being taken into your library."
)

// witnessesConsidered is how many settled library files one copy is compared
// against. Comparing costs no request and no network, only a few thousand word
// comparisons, but a recording the library holds fifty copies of would otherwise
// be compared fifty times for nothing: the first file that recognises the copy
// answers the question, and the rest are the same music again.
const witnessesConsidered = 8

// ImportOne validates one file fetched for a want and imports it only when the
// file is proven to be the recording that want names.
//
// It is the other shape of import, beside a whole release validated against its
// edition. The two are kept apart rather than generalised because they ask
// different questions: a release asks which track each file is and demands that
// every track be covered, while a want asks one question about one file and has
// no edition to measure against.
//
// Nothing here is admitted on tags. A peer's file carries no provenance at all,
// and consistently wrong tags on the wrong audio pass every check a tag can
// answer, so the audio has to agree (ADR 0002). A copy nothing could decide about
// is neither imported nor discarded: it is held, with its evidence, and the want
// carries on looking for a copy that can be proven.
func (importer *Importer) ImportOne(ctx context.Context, requestID uuid.UUID) error {
	row, err := importer.store.ClaimTargetImport(ctx, requestID)
	if err != nil {
		return err
	}

	// A person may already have answered this. Manual decisions win and are
	// permanent, so the question is not asked again — grading it here could only
	// produce the same "nothing could decide" that sent it to them in the first
	// place, and acting on that would overturn their answer with a shrug.
	byHand, err := importer.store.UserAcceptedCopy(ctx, requestID)
	if err != nil {
		return fmt.Errorf("read whether %s was decided by hand: %w", requestID, err)
	}

	file, evidence, err := importer.inspectCandidate(row)
	if err != nil {
		return err
	}
	if byHand && len(evidence.Problems) == 0 {
		return importer.admitByHand(ctx, row, file, evidence)
	}
	if len(evidence.Problems) > 0 {
		// Not a judgement about the music: nothing arrived to judge. It is recorded
		// as a failed attempt, exactly as a transfer that failed outright is.
		return importer.settle(ctx, row, evidence, nil, db.AcquiredFileUndelivered,
			undeliveredSummary, strings.Join(evidence.Problems, "; "),
			"discarded", "failed")
	}

	// The copy's own fingerprint, computed once here and read by every comparison
	// below and kept on the copy itself (ROADMAP 10c: it is what lets the review
	// queue show the copies of one piece of audio as one row). It costs a decode
	// of the audio, and it is the only cost any of them has: the excerpt and the
	// library files were fingerprinted long ago.
	audio := importer.fingerprintCopy(ctx, file.source, evidence)

	// A want MusicBrainz has no recording for was searched on its anchor, and
	// its copies are judged on that anchor alone (ADR 0037).
	if row.MusicBrainzRecordingID == uuid.Nil {
		return importer.importOnAnchor(ctx, row, file, evidence, audio)
	}

	// The library speaks first, and it is asked before anything leaves the
	// machine. A copy that holds the same music as a file already proven to be
	// the wanted recording is that recording, so asking AcoustID afterwards
	// would spend a request on a question already answered.
	importer.measureAgainstLibrary(ctx, row, audio, evidence)
	importer.measureAgainstAnchor(ctx, row, audio, evidence)
	importer.measureQuality(ctx, file.source, evidence)

	measuredMS := audio.milliseconds()
	if !witnessAdmits(evidence) {
		if listened := importer.listen(ctx, file.source, evidence); listened > 0 {
			measuredMS = listened
		}
	}
	verification, err := importer.verifier.Verify(
		ctx, candidateEvidence(
			file, evidence, measuredMS, row.MusicBrainzRecordingID, row.AnchorSource),
		row.MusicBrainzRecordingID)
	if errors.Is(err, identity.ErrRecordingUnknown) {
		// The recording this want names is gone from MusicBrainz — deleted or
		// merged into another row. Nothing about this file is at fault, and
		// fetching another copy would only fail at the same step: the want
		// itself needs a new answer, so it goes back to resolution rather than
		// back into the search.
		return importer.recordingGone(ctx, row, evidence)
	}
	if err != nil {
		// A provider nobody can reach says nothing about the want or the file.
		// It is retried, and an exhausted retry says so on the request rather
		// than asking somebody about a file Schall never managed to check.
		return fmt.Errorf("verify %q against recording %s: %w",
			row.File.Name, row.MusicBrainzRecordingID, err)
	}

	evidence.Agrees, evidence.Differs = verification.Agrees, verification.Differs
	// What the file was measured against, written down whatever the verdict. A
	// copy nobody could decide about is the one somebody will read, and reading it
	// without the recording beside it leaves every field the entry never carried —
	// the release, the length, the ISRC — looking like a question the check never
	// asked. It did ask; this is where the answer is kept.
	evidence.Wanted = recordingTags(row, verification.Recording)
	// Whether the audio named the wanted recording, recorded only where the
	// question could actually be asked: a check that did not run, and a database
	// that had never heard the music, have not answered it.
	if evidence.Acoustic != nil && evidence.Acoustic.Unavailable == "" &&
		!verification.AudioSetAside &&
		len(evidence.Acoustic.RecordingIDs) > 0 {
		agrees := !verification.AudioSaysOtherwise
		evidence.Acoustic.Agrees = &agrees
	}
	// The same again for the other witness, and kept apart from it. A copy the
	// anchor refuses may be one AcoustID named correctly, and writing that down as
	// AcoustID disagreeing would be a wrong permanent fact about what AcoustID
	// said. A distance between the two thresholds concludes nothing and is left
	// unanswered here rather than rounded to the nearer answer.
	if evidence.Anchor != nil && (verification.AnchorSaysOtherwise || verification.AnchorAgrees) {
		agrees := verification.AnchorAgrees
		evidence.Anchor.Agrees = &agrees
	}
	switch {
	case verification.Proven() && verification.Audio:
		return importer.admit(ctx, row, file, evidence, verification)
	case verification.AnchorSaysOtherwise:
		// Before the acoustic refusal, because when both speak this is the one
		// that says what happened: the wanted recording's own publisher supplied
		// the audio this copy failed to reproduce.
		return importer.refuse(ctx, row, evidence,
			db.AcquiredFileDiscardedAudio, anchorRefusedSummary, verification.Summary)
	case verification.SameRegistrationUnchecked:
		// The audio named another MusicBrainz row and nobody could ask whether that
		// row is this track entered twice. The refusal below rests on that answer,
		// so without it there is a question and not a verdict
		// (ADR 0031).
		//
		// The want is waiting for evidence rather than for a person, and it is
		// asked again on a schedule that runs out. The hold is written first
		// because settling the copy writes the want's summary too, and the
		// sentence a spent schedule leaves has to be the one that stands.
		if err := importer.hold(ctx, row, file.source, evidence,
			registrationUncheckedSummary, verification.Summary); err != nil {
			return err
		}
		return importer.recheckMusicBrainz(ctx, row.AcquisitionTargetID)
	case verification.AudioSaysOtherwise:
		return importer.refuse(ctx, row, evidence,
			db.AcquiredFileDiscardedAudio, audioRefusedSummary, verification.Summary)
	case verification.Contradicted:
		return importer.refuse(ctx, row, evidence,
			db.AcquiredFileDiscardedTags, tagsRefusedSummary, verification.Summary)
	case verification.Proven():
		// The tags identify it and nothing listened. That is not a refusal — the
		// file may well be right — and it is not an admission either.
		return importer.hold(ctx, row, file.source, evidence,
			unlistenedSummary, verification.Summary)
	}
	if uploadSaidOtherAudio(evidence.Anchor) {
		return importer.hold(ctx, row, file.source, evidence,
			uploadUnmatchedSummary, verification.Summary)
	}
	return importer.hold(ctx, row, file.source, evidence, undecidedSummary, verification.Summary)
}

// uploadSaidOtherAudio reports a comparison that ran against an upload found by
// name and came back reading other audio.
//
// The grader draws no conclusion from that (ADR 0029 §3), so the copy
// is held exactly as one nothing spoke about is. This is only what the person
// reading it is told, and it is asked here rather than in the grader because the
// grader's job was to conclude nothing.
func uploadSaidOtherAudio(anchor *db.ImportAnchor) bool {
	return anchor != nil && anchor.Measured &&
		(anchor.Source == identity.AnchorByName || anchor.Source == identity.AnchorByNameTopic) &&
		chromaprint.Read(anchor.Rate) == chromaprint.OtherAudio
}

// inspectCandidate reads what the one fetched file has to say about itself. Only
// the bytes are decided here: whether the file is there, whole, and readable.
func (importer *Importer) inspectCandidate(
	row db.TargetImportRow,
) (inspected, *db.AcquiredFileEvidence, error) {
	evidence := &db.AcquiredFileEvidence{
		Name: row.File.Name, Agrees: []string{}, Differs: []string{},
		Problems: []string{}, SizeBytes: row.File.SizeBytes, BitRate: row.File.BitRate,
	}
	sourceRoot, err := importer.deliveredFolder(row.Provider, row.SourceDirectory)
	if err != nil {
		evidence.Problems = append(evidence.Problems, err.Error())
		return inspected{}, evidence, nil
	}
	// Untagged files are read rather than refused: on this path tags admit
	// nothing anyway, and a WAV with no metadata chunk is still audio the
	// listener can identify. What it turns out to be is decided below, by the
	// same rules as any other copy.
	file, err := importer.readUntagged(sourceRoot, row.File)
	if err != nil {
		return inspected{}, evidence, err
	}
	evidence.Observed = file.tags
	evidence.Problems = append(evidence.Problems, file.evidence.Problems...)
	return file, evidence, nil
}

// listen records what the audio turns out to be, and reports how long that audio
// actually ran. A check that could not run says why and refuses nothing; a check
// that ran and recognised nothing is an answer about the database rather than
// about the file. Neither of them admits anything, because only an
// identification can.
//
// The length comes back with the verdict because fpcalc measured it while
// decoding the file, and a decode is the only thing on this path that knows how
// long a peer's copy is. It is rounded to the second, which is a tenth of the
// five seconds two durations may sit apart, so nothing it decides turns on the
// rounding. Zero means nothing decoded the file, and the caller then has only
// what the file claims.
func (importer *Importer) listen(
	ctx context.Context, source string, evidence *db.AcquiredFileEvidence,
) int {
	if importer.acoustic == nil || !importer.acoustic.Configured(ctx) {
		evidence.Acoustic = &db.ImportAcoustic{
			RecordingIDs: []string{},
			Unavailable:  "identifying files by their audio is switched off",
		}
		return 0
	}
	identified, err := importer.acoustic.Identify(ctx, source)
	if err != nil {
		evidence.Acoustic = &db.ImportAcoustic{
			RecordingIDs: []string{}, Unavailable: truncateReason(err.Error()),
		}
		importer.logger.Warn().Err(err).Str("file", evidence.Name).
			Msg("could not identify a fetched candidate by its audio")
		return 0
	}
	evidence.Acoustic = acousticEvidence(identified)
	return identified.DurationSeconds * 1000
}

// copyAudio is the fetched copy's own fingerprint, computed once for every
// comparison that reads it.
//
// It is one struct rather than three return values because the comparisons
// that read it have to be able to say why they could not run, and they say it
// in their own words: "switched off" is one sentence about the machine and a
// failed decode is another about this file.
type copyAudio struct {
	// frames is the fingerprint as numbers, empty when none could be computed.
	frames []uint32
	// seconds is how long the audio fpcalc read actually ran.
	seconds int
	// switchedOff means no fingerprinting program is configured, so nothing was
	// attempted at all.
	switchedOff bool
	// failure is why the fingerprint could not be computed, when one was tried.
	failure string
}

// milliseconds is how long the copy runs, as the decode measured it, and zero
// when nothing measured it.
//
// A decode that reached the length limit is not a measurement of the file: it is
// where fpcalc was told to stop. Reporting it would hand the grader a length
// fifteen minutes long for every longer track, and a length that disagrees by
// more than five seconds voids a pair — so a copy longer than the limit is
// reported as unmeasured and judged on what its tags claim, exactly as it was
// before anything decoded it.
func (audio copyAudio) milliseconds() int {
	if audio.seconds <= 0 || audio.seconds >= chromaprint.CandidateLengthSeconds {
		return 0
	}
	return audio.seconds * 1000
}

// fingerprintCopy measures what this copy sounds like, once, and keeps the
// number on the copy itself.
//
// A fingerprint is about eight numbers a second computed from the decoded
// audio, the same form AcoustID uses. Two copies of one master produce nearly
// the same numbers even after re-encoding, so two copies can be compared
// against each other with no service and no catalogue entry at all — which is
// what lets the review queue show the copies of one piece of audio as one row
// instead of eleven (ROADMAP 10c) — and it is the same measurement the library
// and the want's published sample are compared against below (ROADMAP 10b).
// One decode pays for all three.
//
// It is read at CandidateLengthSeconds rather than at the two minutes the
// listener reads, because a distributor cuts its excerpt from wherever in the
// song it likes: a copy read only to 2:00 would not contain an excerpt taken
// from 3:10, and a correct copy would then measure like a stranger. The same
// whole read is what makes two copies comparable with each other.
//
// Everything that can go wrong is silence, kept in the struct returned rather
// than raised: no fingerprinting program, audio nothing could read, a file
// that would not decode. None of them is evidence about the music.
func (importer *Importer) fingerprintCopy(
	ctx context.Context, source string, evidence *db.AcquiredFileEvidence,
) copyAudio {
	if importer.prints == nil || !importer.prints.Available() {
		return copyAudio{switchedOff: true}
	}
	value, seconds, err := importer.prints.Compute(ctx, source, chromaprint.CandidateLengthSeconds)
	if err != nil {
		importer.logger.Warn().Err(err).Str("file", evidence.Name).
			Msg("could not fingerprint a fetched candidate")
		return copyAudio{failure: truncateReason(err.Error())}
	}
	if value != "" && seconds > 0 {
		// Kept on the copy in the vocabulary ROADMAP 10c already stores it in,
		// so the review queue's grouping reads the same measurement this import
		// used rather than a second one computed later by a backfill sweep.
		evidence.AudioFingerprint, evidence.AudioFingerprintSeconds = value, seconds
	}
	frames, err := chromaprint.Decode(value)
	if err != nil {
		return copyAudio{failure: truncateReason(err.Error()), seconds: seconds}
	}
	return copyAudio{frames: frames, seconds: seconds}
}

// witnessAdmits reports the library having recognised this copy. It is read
// before the audio is sent anywhere, because a copy the library has already
// answered for needs no question put to a service.
func witnessAdmits(evidence *db.AcquiredFileEvidence) bool {
	return evidence.Witness != nil && evidence.Witness.Agrees != nil && *evidence.Witness.Agrees
}

// measureAgainstLibrary compares this copy against the files the library already
// holds and has already proven to be the wanted recording, and writes down what
// came of it.
//
// This is the library used as a witness. A want names one recording; the library
// may already hold a file proven to be that recording — proven by a person's
// decision, by AcoustID, by an ISRC or by a MusicBrainz identifier. A copy that
// holds the same music as that file is that recording too, on the strength of the
// older proof, which is why the file and its proof are both written down here and
// travel with the identity afterwards.
//
// Both fingerprints are read from the start of their files, over as much audio as
// both hold. A new library witness covers the whole file; an older one covers the
// opening two minutes and remains usable only as a partial witness. The copy
// starts in the same place, so the comparison is of one beginning
// against another. It is deliberately not the sliding comparison the excerpt uses:
// sliding answers "does this file contain that audio", which an hour-long mix of a
// hundred tracks would satisfy.
//
// Reading only a shared prefix means a low rate alone is not enough: an hour-long
// mix that opens with the wanted recording shares its first two minutes with a
// settled library file just as a true copy would. So the two files' own decoded
// lengths — never a tag — travel to the grader beside the rate, and it is the
// grader, not this function, that refuses to agree unless both are known and
// agree within the same tolerance a duration tag would (identity.DurationToleranceMS).
// A copy that hit fingerprintCopy's length limit while decoding has no measured
// length at all, and is judged the same way.
//
// It can only admit. A copy far from every owned file says nothing — a remaster or
// another pressing of one recording measures far from the copy in the library and
// is still that recording — so nothing here is ever read as a refusal.
//
// Everything that can go wrong is silence, and is written down as such: a library
// with no settled file for this recording, a machine with no fingerprinting
// program, a file too short to compare, a stored fingerprint nothing can read.
func (importer *Importer) measureAgainstLibrary(
	ctx context.Context, row db.TargetImportRow, audio copyAudio,
	evidence *db.AcquiredFileEvidence,
) {
	settled, err := importer.store.SettledWitnesses(
		ctx, row.MusicBrainzRecordingID, witnessesConsidered)
	if err != nil {
		// Not being able to read the library is a fault at home. It says nothing
		// about the copy, so the copy is judged as it was before the library could
		// be asked.
		importer.logger.Error().Err(err).
			Str("musicbrainz_recording_id", row.MusicBrainzRecordingID.String()).
			Msg("could not read the library's settled files for a want")
		return
	}
	if len(settled) == 0 {
		return
	}

	witness := &db.ImportWitness{Considered: len(settled)}
	evidence.Witness = witness
	switch {
	case audio.switchedOff:
		witness.Unavailable = "comparing audio against your library is switched off"
		return
	case len(audio.frames) == 0:
		witness.Unavailable = audio.failure
		if witness.Unavailable == "" {
			witness.Unavailable = "the copy could not be fingerprinted"
		}
		return
	}

	best := -1.0
	var closest db.LibraryWitnessRow
	var closestComparison chromaprint.Comparison
	unreadable := 0
	for _, owned := range settled {
		// The library keeps a fingerprint as "<seconds>:<base64>", the shape
		// readKeptFingerprint reads; the decoder wants the base64 alone.
		var frames []uint32
		kept, err := readKeptFingerprint(owned.Fingerprint)
		if err == nil {
			frames, err = chromaprint.Decode(kept.Value)
		}
		if err != nil {
			// A fingerprint on record that is not a fingerprint is a fault at
			// home, and it is this file's own, so it is named in the log.
			unreadable++
			importer.logger.Error().Err(err).Str("library_file_id", owned.FileID.String()).
				Msg("a library file's kept fingerprint is not a fingerprint")
			continue
		}
		comparison, err := chromaprint.CompareFromStart(frames, audio.frames)
		if err != nil {
			// Too little audio in common to compare. It is never a disagreement.
			unreadable++
			continue
		}
		if best < 0 || comparison.GlobalBER < best {
			best, closest, closestComparison = comparison.GlobalBER, owned, comparison
		}
	}
	if best < 0 {
		witness.Unavailable = "no file in your library could be compared with this copy"
		if unreadable == 0 {
			witness.Unavailable = "the copy could not be compared with your library"
		}
		return
	}

	witness.Rate, witness.Measured = best, true
	witness.Comparison = &closestComparison
	witness.ReferenceCoverageSeconds = closest.CoverageSeconds
	witness.CandidateCoverageSeconds = audio.seconds
	witness.FileID = closest.FileID.String()
	witness.Path, witness.Proof = closest.Path, closest.Proof
	// Both decoded lengths, never a tag: the settled file's own, as it was last
	// measured, and the copy's, from the fingerprint just computed. They travel
	// to the grader with the rate, because a low rate over a shared prefix
	// proves nothing by itself about a copy many times the settled file's
	// length — see the doc comment above.
	witness.FileDurationMS = closest.DurationMS
	witness.CandidateDurationMS = audio.milliseconds()
	agreesOnLength := witness.CandidateDurationMS > 0 && witness.FileDurationMS > 0 &&
		abs(witness.CandidateDurationMS-witness.FileDurationMS) <= identity.DurationToleranceMS
	referenceCovered := closest.CoverageSeconds > 0 &&
		closest.CoverageSeconds*1000+identity.DurationToleranceMS >= witness.FileDurationMS
	candidateCovered := audio.seconds > 0 &&
		audio.seconds*1000+identity.DurationToleranceMS >= witness.CandidateDurationMS
	if chromaprint.Read(best) == chromaprint.SameAudio &&
		closestComparison.WindowsAgree() && referenceCovered && candidateCovered && agreesOnLength {
		agrees := true
		witness.Agrees = &agrees
	}
}

// measureAgainstAnchor compares this copy against the audio the want is anchored
// to, and writes down what came of it.
//
// The anchor is the thirty-second excerpt the distributor publishes for the
// want's own ISRC, fetched when the want was created and kept on it. This
// measures how much of that excerpt the copy fails to reproduce at the best
// alignment of the two fingerprints. It decides nothing here: the number goes to
// the grader with the rest of the evidence, and the grader applies the rule.
//
// The copy's own fingerprint is the one fingerprintCopy already computed, so the
// audio is decoded once per copy rather than once per question asked about it.
// whyUnmeasured is that step's reason for having none, which is recorded here
// because a check that silently did nothing is worse than an absent one.
//
// Everything that can go wrong is silence. A want with no anchor, a machine with
// no fingerprinting program, a file too short to hold the excerpt, audio nothing
// could read — none of them is evidence about the music, and a copy that was not
// measured is judged exactly as it was before anchors existed.
func (importer *Importer) measureAgainstAnchor(
	_ context.Context, row db.TargetImportRow, audio copyAudio,
	evidence *db.AcquiredFileEvidence,
) {
	if row.AnchorFingerprint == "" {
		return
	}
	anchor := &db.ImportAnchor{
		Source: row.AnchorSource, Reference: row.AnchorReference, Seconds: row.AnchorSeconds,
		Label: row.AnchorLabel, Views: row.AnchorViews,
	}
	evidence.Anchor = anchor

	if audio.switchedOff {
		anchor.Unavailable = "comparing audio against a published sample is switched off"
		return
	}
	reference, err := chromaprint.Decode(row.AnchorFingerprint)
	if err != nil {
		// The want's own stored anchor is unreadable. That is a fault at home
		// rather than anything about this copy, so it is logged where somebody
		// maintaining Schall will see it.
		anchor.Unavailable = "the sample stored for this want could not be read"
		importer.logger.Error().Err(err).
			Str("acquisition_target_id", row.AcquisitionTargetID.String()).
			Msg("a want's stored anchor is not a fingerprint")
		return
	}
	if len(audio.frames) == 0 {
		anchor.Unavailable = audio.failure
		if anchor.Unavailable == "" {
			anchor.Unavailable = "the copy could not be fingerprinted"
		}
		return
	}
	comparison, err := chromaprint.Compare(reference, audio.frames)
	if err != nil {
		// A copy shorter than the excerpt has no position the excerpt sits wholly
		// inside, so there is nothing to measure. It is never evidence the two
		// disagree.
		anchor.Unavailable = truncateReason(err.Error())
		return
	}
	anchor.Rate, anchor.Measured = comparison.GlobalBER, true
	anchor.Comparison = &comparison
	anchor.ReferenceCoverageSeconds = row.AnchorSeconds
	anchor.CandidateCoverageSeconds = audio.seconds
}

// measureQuality writes down what this copy's audio is, as opposed to what its
// name claims: the frequency its music stops at, what the file says made it, and
// whether the two together look like audio that was squeezed before it reached
// the container it arrived in.
//
// It is written after the grader's evidence is gathered and is deliberately not
// part of it. Nothing here admits a copy and nothing here refuses one — a
// transcode of the wanted recording is still the wanted recording, and a copy
// this says nothing about is judged exactly as it was before. It exists because
// a person in the review queue choosing between two copies of one recording
// could not otherwise tell a real FLAC from an MP3 wearing the extension.
//
// Everything that can go wrong is silence. No measurer, no decoder, audio the
// decoder could not read, a stretch of silence in the middle of the file: none
// of them is a fact about the music, and none of them is written down as one.
func (importer *Importer) measureQuality(
	ctx context.Context, source string, evidence *db.AcquiredFileEvidence,
) {
	if importer.quality == nil || !importer.quality.Available() {
		return
	}
	properties, _ := tagging.ReadProperties(source)
	if !properties.Known() {
		return
	}
	reading, err := importer.quality.Read(
		ctx, source, properties.SampleRateHz, properties.DurationMS)
	if err != nil {
		importer.logger.Debug().Err(err).Str("file", evidence.Name).
			Msg("could not measure what a fetched copy's audio is")
		return
	}
	if !reading.Judged {
		return
	}
	// The bit rate is the one the file itself reports rather than the one the
	// peer advertised, because this asks whether the audio and the container
	// agree and only the file can answer for both.
	evidence.Audio = &db.ImportAudioQuality{
		CutoffHz:           reading.CutoffHz,
		Encoder:            reading.Encoder,
		TranscodeSuspected: spectrum.Suspected(reading.Measurement, source, properties.BitRateKbps),
	}
}

// drawWaveform reads the shape of a copy's audio so the review queue can plot it
// on the card, and reads it once: the peaks are stored on the copy and the file
// they came from leaves the inbox.
//
// Everything that can go wrong is silence, exactly as it is for measureQuality.
// No reader, no decoder, audio the decoder could not read: none of them is a
// fact about the music, and a copy with no waveform is judged exactly as it was
// before.
func (importer *Importer) drawWaveform(ctx context.Context, source, name string) []byte {
	if importer.shapes == nil || !importer.shapes.Available() {
		return nil
	}
	peaks, err := importer.shapes.Peaks(ctx, source)
	if err != nil {
		importer.logger.Debug().Err(err).Str("file", name).
			Msg("could not read the shape of a fetched copy's audio")
		return nil
	}
	return peaks
}

// candidateEvidence is what the file says about itself, in the vocabulary the
// grader reads. The audio travels with it rather than beside it, so one rule set
// weighs all of it at once.
//
// measuredMS is how long the audio ran, when something decoded it, and 0 when
// nothing did.
//
// anchoredTo is the recording the want's stored excerpt was published for, which
// is the want's own recording: the excerpt was fetched by that recording's ISRC.
// It travels with the measurement because a distance from an excerpt says
// nothing until it is known which music the excerpt was of.
//
// anchorSource is who published that excerpt. It travels too, because how the
// excerpt was found decides what the number may answer: an excerpt found by
// searching for the want's name never refuses a copy (ADR 0029), and
// only one fetched by the recording's ISRC stands in for an identification where
// the credit disagrees (ADR 0030).
func candidateEvidence(
	file inspected, evidence *db.AcquiredFileEvidence, measuredMS int,
	anchoredTo uuid.UUID, anchorSource string,
) identity.Evidence {
	result := identity.Evidence{Subject: identity.SubjectFile}
	if file.tags != nil {
		result.ISRC = file.tags.ISRC
		result.Artist = file.tags.Artist
		result.Album = file.tags.Album
		result.Title = file.tags.Title
		result.DurationMS = file.tags.DurationMS
		if recordingID, err := uuid.Parse(file.tags.MusicBrainzRecordingID); err == nil {
			result.RecordingID = recordingID
		}
	}
	// The measurement beats the claim. Duration is one of the things a match may
	// rest on, and a difference of more than five seconds voids a pair whatever
	// else agrees — so on the one path where a stranger wrote every tag being
	// compared, the length compared is the length the audio turned out to be.
	// It can only make an agreement harder to reach and a contradiction easier.
	if measuredMS > 0 {
		result.DurationMS = measuredMS
	}
	// Nil means nobody listened. An empty answer means somebody did and the
	// database had never heard the music, which is a different thing and is graded
	// as such.
	//
	// The clusters go across with their boundaries intact, because the boundary is
	// what the grader decides on: within one, AcoustID is saying these recordings
	// are the same audio; between two, it is saying the second is other audio that
	// resembled the file. The similarity travels with each, so a weak cluster and a
	// certain one never arrive looking alike.
	//
	// An answer with no clusters in it is silence and never one cluster holding
	// everything. Nothing re-grades stored evidence today — this is always called
	// with what the pass just gathered — but a flat list from before clusters were
	// recorded, read as a single cluster, would admit copies the rule that wrote it
	// had refused.
	if evidence.Acoustic != nil && evidence.Acoustic.Unavailable == "" {
		clusters := make([]identity.AcousticCluster, 0, len(evidence.Acoustic.Clusters))
		for _, cluster := range evidence.Acoustic.Clusters {
			named := make([]uuid.UUID, 0, len(cluster.RecordingIDs))
			for _, text := range cluster.RecordingIDs {
				if recordingID, err := uuid.Parse(text); err == nil {
					named = append(named, recordingID)
				}
			}
			clusters = append(clusters, identity.AcousticCluster{
				ID: cluster.ID, Recordings: named, Similarity: cluster.Score,
			})
		}
		result.Acoustic = &identity.Acoustic{Clusters: clusters}
	}
	// The distance from the want's own published sample, and only where the
	// comparison actually ran. A measurement that could not be made is silence,
	// and silence must not arrive at the grader as a distance of zero — which is
	// the best score there is.
	if evidence.Anchor != nil && evidence.Anchor.Measured {
		result.Anchor = &identity.AnchorComparison{
			RecordingID: anchoredTo, Rate: evidence.Anchor.Rate, Source: anchorSource,
			Comparison:               evidence.Anchor.Comparison,
			ReferenceCoverageSeconds: evidence.Anchor.ReferenceCoverageSeconds,
			CandidateCoverageSeconds: evidence.Anchor.CandidateCoverageSeconds,
			CandidateDurationMS:      measuredMS,
		}
	}
	// The distance from the nearest file the library has already proven to be
	// this recording, and only where the comparison ran. The recording is the
	// want's own, because the library was asked for files proven to be that one
	// and no other.
	if evidence.Witness != nil && evidence.Witness.Measured {
		result.Witness = &identity.WitnessComparison{
			RecordingID: anchoredTo, Rate: evidence.Witness.Rate,
			FileDurationMS:           evidence.Witness.FileDurationMS,
			CandidateDurationMS:      evidence.Witness.CandidateDurationMS,
			Comparison:               evidence.Witness.Comparison,
			ReferenceCoverageSeconds: evidence.Witness.ReferenceCoverageSeconds,
			CandidateCoverageSeconds: evidence.Witness.CandidateCoverageSeconds,
		}
	}
	return result
}

// admit copies the proven file into the managed library and records the verdict
// that let it in, in one transaction with the import itself.
// admitByHand imports a copy somebody decided about, without asking again.
//
// It is the same import as admit with one thing missing: any claim that Schall
// proved anything. The evidence keeps whatever was read from the file, the method
// says a person decided, and nothing is invented to fill the gap where a proof
// would be. The verdict row is already 'accepted' by 'user' — written before this
// ran, so a crash here leaves a decision to be honoured rather than a file nobody
// decided about — and recordAcquiredFile will not overwrite it.
func (importer *Importer) admitByHand(
	ctx context.Context, row db.TargetImportRow, file inspected, evidence *db.AcquiredFileEvidence,
) error {
	evidence.Method = "manual"
	evidence.Observed = file.tags
	// A want with no recording has its anchor's address to write instead
	// (ADR 0037 §5). ClaimTargetImport returns such a want only when its
	// anchor names one.
	anchored := row.MusicBrainzRecordingID == uuid.Nil
	if anchored {
		key, ok := db.SourceKeyOf(row)
		if !ok {
			return fmt.Errorf("%w: want %s names no recording and its anchor names no track",
				db.ErrNotATargetImport, row.AcquisitionTargetID)
		}
		evidence.Source = &key
	}

	files := []validatedFile{{request: row.File, source: file.source}}
	// Filed by the release, exactly as a proven copy is. A copy somebody
	// accepted by hand is the same music in the same album as one nothing had to
	// ask about, and filing the two differently — this path used to name the
	// folder after the track — put one song's two arrivals in two folders.
	destination := importer.releaseFolder(ctx, wantFields(row, nil))
	localPaths, placed, err := importer.copyRelease(ctx, destination, files)
	if err != nil {
		return err
	}
	// Written before the import is completed, because completing it is what
	// queues the scan that turns this path into a library row.
	importer.recordPlacements(ctx, row.RequestID, placed, localPaths, files)
	if err := importer.store.CompleteTargetImport(ctx, db.RecordAcquiredFileParams{
		AcquisitionTargetID: row.AcquisitionTargetID,
		Provider:            row.Provider,
		SourceUsername:      row.SourceUsername,
		RemotePath:          row.File.Path,
		FileName:            row.File.Name,
		SizeBytes:           row.File.SizeBytes,
		Verdict:             db.AcquiredFileAccepted,
		Summary:             acceptedByHandSummary,
		Evidence:            evidence,
		DownloadRequestID:   uuid.NullUUID{UUID: row.RequestID, Valid: true},
	}, importedTargetSummary, importer.libraryPath, destination, localPaths); err != nil {
		return err
	}
	importer.logger.Info().
		Str("request_id", row.RequestID.String()).
		Str("acquisition_target_id", row.AcquisitionTargetID.String()).
		Str("path", destination).
		Msg("a held candidate was accepted by hand and imported")
	importer.releaseSource(ctx, row.RequestID, files, placed, localPaths)
	// Nothing proved the names of a track MusicBrainz does not know, so the file
	// keeps the tags it arrived with (ADR 0037).
	if !anchored {
		importer.tagCopy(ctx, row, nil, localPaths)
	}
	return nil
}

func (importer *Importer) admit(
	ctx context.Context,
	row db.TargetImportRow,
	file inspected,
	evidence *db.AcquiredFileEvidence,
	verification identity.Verification,
) error {
	evidence.Method = verification.Identity.Method
	evidence.Confidence = verification.Identity.Confidence
	evidence.Wanted = wantedTags(verification.Identity)

	files := []validatedFile{{request: row.File, source: file.source}}
	destination := importer.releaseFolder(ctx, wantFields(row, verification.Identity))
	localPaths, placed, err := importer.copyRelease(ctx, destination, files)
	if err != nil {
		return err
	}
	// Written before the import is completed, because completing it is what
	// queues the scan that turns this path into a library row.
	importer.recordPlacements(ctx, row.RequestID, placed, localPaths, files)
	if err := importer.store.CompleteTargetImport(ctx, db.RecordAcquiredFileParams{
		AcquisitionTargetID: row.AcquisitionTargetID,
		Provider:            row.Provider,
		SourceUsername:      row.SourceUsername,
		RemotePath:          row.File.Path,
		FileName:            row.File.Name,
		SizeBytes:           row.File.SizeBytes,
		Verdict:             db.AcquiredFileAccepted,
		Summary:             acceptedSummary + " " + verification.Summary,
		Evidence:            evidence,
		DownloadRequestID:   uuid.NullUUID{UUID: row.RequestID, Valid: true},
	}, importedTargetSummary, importer.libraryPath, destination, localPaths); err != nil {
		return err
	}
	importer.logger.Info().
		Str("request_id", row.RequestID.String()).
		Str("acquisition_target_id", row.AcquisitionTargetID.String()).
		Str("musicbrainz_recording_id", row.MusicBrainzRecordingID.String()).
		Str("method", verification.Identity.Method).
		Str("path", destination).
		Msg("a fetched candidate was proven and imported")
	importer.releaseSource(ctx, row.RequestID, files, placed, localPaths)
	importer.tagCopy(ctx, row, verification.Identity, localPaths)
	// Admitting does not go through settle, and a copy let in is the strongest
	// answer the question could have had.
	return importer.endTheProviderWait(ctx, row.AcquisitionTargetID)
}

// refuse records a candidate that will not be imported and puts the want back in
// the queue for the next copy. Nothing is deleted: the bytes are the provider's,
// the inbox is read-only, and a refusal is a decision about what Schall will use
// rather than about what somebody else may keep.
func (importer *Importer) refuse(
	ctx context.Context,
	row db.TargetImportRow,
	evidence *db.AcquiredFileEvidence,
	verdict, summary, detail string,
) error {
	importer.logger.Info().
		Str("request_id", row.RequestID.String()).
		Str("acquisition_target_id", row.AcquisitionTargetID.String()).
		Str("verdict", verdict).
		Msg("a fetched candidate was refused: " + detail)
	return importer.settle(ctx, row, evidence, nil, verdict, summary, detail, "discarded", "rejected")
}

// hold keeps a copy nothing could decide about, with everything the decision
// would have been made from. The want stays in the queue: a copy another peer is
// sharing may be provable, and finding one costs nobody's attention.
//
// This is the one verdict whose file somebody will play, so it is the one that
// draws the waveform. An accepted copy is in the library and a refused one is
// answered, and neither is offered to a player.
func (importer *Importer) hold(
	ctx context.Context, row db.TargetImportRow, source string,
	evidence *db.AcquiredFileEvidence, summary, detail string,
) error {
	importer.logger.Info().
		Str("request_id", row.RequestID.String()).
		Str("acquisition_target_id", row.AcquisitionTargetID.String()).
		Msg("a fetched candidate is a question: " + detail)
	return importer.settle(ctx, row, evidence, importer.drawWaveform(ctx, source, evidence.Name),
		db.AcquiredFileHeld, summary, detail, "needs_review", "inconclusive")
}

// recheckMusicBrainz asks for this want's copies to be judged again once the
// provider has had time to come back, and stops asking when the patience for it
// runs out.
//
// A question nobody can answer is not a question, and until this existed the ask
// repeated every thirty minutes for ever. What the want gets now is an hour, six
// hours, a day and a day, and then it says so and waits for a person. The store
// counts one of those per pass however many copies of the want ask.
func (importer *Importer) recheckMusicBrainz(ctx context.Context, targetID uuid.UUID) error {
	queued, err := importer.store.RecheckAfterMusicBrainzSilence(
		ctx, targetID, importer.now(), registrationUnanswerableSummary)
	if err != nil {
		return fmt.Errorf("queue another check for want %s: %w", targetID, err)
	}
	if !queued {
		importer.logger.Info().
			Str("acquisition_target_id", targetID.String()).
			Msg("a want has asked MusicBrainz as often as it will; it is a question for a person now")
	}
	return nil
}

// recordingGone discards a fetched copy that could not be checked because
// MusicBrainz no longer knows the recording its want named, and sends the want
// back to be resolved again instead of back into the search for another copy.
//
// Another copy would fail at the same step: the want's answer is gone, not the
// file. The want was pending, waiting for a copy, or resolved by a person's own
// hand — either way the recording it points at no longer exists, so it is
// cleared and the want is unresolved again, exactly as
// acquisition.RejectResolution reopens a want whose resolution a person
// rejected. Unlike that path, nobody rejected anything: nothing is recorded
// against the recording, because MusicBrainz changed and not the person.
func (importer *Importer) recordingGone(
	ctx context.Context, row db.TargetImportRow, evidence *db.AcquiredFileEvidence,
) error {
	importer.logger.Info().
		Str("request_id", row.RequestID.String()).
		Str("acquisition_target_id", row.AcquisitionTargetID.String()).
		Str("musicbrainz_recording_id", row.MusicBrainzRecordingID.String()).
		Msg("a want's recording is gone from MusicBrainz; sending it back to resolution")
	if evidence.Wanted == nil {
		evidence.Wanted = &db.ImportTags{
			Artist: row.EntryArtist, Title: row.EntryTitle,
			MusicBrainzRecordingID: row.MusicBrainzRecordingID.String(),
		}
	}
	return importer.store.SettleAcquiredFileForGoneRecording(ctx, db.RecordingGoneParams{
		RecordAcquiredFileParams: db.RecordAcquiredFileParams{
			AcquisitionTargetID: row.AcquisitionTargetID,
			Provider:            row.Provider,
			SourceUsername:      row.SourceUsername,
			RemotePath:          row.File.Path,
			FileName:            row.File.Name,
			SizeBytes:           row.File.SizeBytes,
			Verdict:             db.AcquiredFileUndelivered,
			Summary:             recordingGoneSummary,
			Evidence:            evidence,
			DownloadRequestID:   uuid.NullUUID{UUID: row.RequestID, Valid: true},
		},
		RecordingID:   row.MusicBrainzRecordingID,
		ImportError:   recordingGoneSummary,
		Summary:       wantRecordingGoneSummary,
		ManualSummary: wantRecordingGoneManualSummary,
		NextAttemptAt: importer.now(),
	})
}

func (importer *Importer) settle(
	ctx context.Context,
	row db.TargetImportRow,
	evidence *db.AcquiredFileEvidence,
	waveform []byte,
	verdict, summary, detail, importStatus, outcome string,
) error {
	if evidence.Wanted == nil {
		evidence.Wanted = entryTags(row)
	}
	if err := importer.store.SettleAcquiredFile(ctx, db.SettleAcquiredFileParams{
		RecordAcquiredFileParams: db.RecordAcquiredFileParams{
			AcquisitionTargetID: row.AcquisitionTargetID,
			Provider:            row.Provider,
			SourceUsername:      row.SourceUsername,
			RemotePath:          row.File.Path,
			FileName:            row.File.Name,
			SizeBytes:           row.File.SizeBytes,
			Verdict:             verdict,
			Summary:             strings.TrimSpace(summary + " " + detail),
			DownloadRequestID:   uuid.NullUUID{UUID: row.RequestID, Valid: true},
			Waveform:            waveform,
		},
		ImportStatus:  importStatus,
		ImportError:   strings.TrimSpace(summary + " " + detail),
		Evidence:      evidence,
		TargetSummary: waitingNextSummary,
		NextAttemptAt: time.Now(),
		Outcome:       outcome,
	}); err != nil {
		return err
	}
	return importer.endTheProviderWait(ctx, row.AcquisitionTargetID)
}

// endTheProviderWait takes the wait off a want once no copy of it is stopped on
// the registration question any more.
//
// It is asked after every decision rather than only after the ones that look
// like an answer, because the store reads the state and not this verdict: a want
// still holding one of those copies is left waiting, and one that is not has
// stopped waiting whatever happened to the copy in hand. The
// still-unaskable branch reaches it too, and it is a no-op there.
func (importer *Importer) endTheProviderWait(ctx context.Context, targetID uuid.UUID) error {
	if err := importer.store.EndTheMusicBrainzWaitIfAnswered(ctx, targetID); err != nil {
		return fmt.Errorf("end the provider wait on want %s: %w", targetID, err)
	}
	return nil
}

// entryTags is the want as its entry states it, in the shape the evidence
// stores. A want with no recording names none, rather than the zero UUID.
func entryTags(row db.TargetImportRow) *db.ImportTags {
	tags := &db.ImportTags{Artist: row.EntryArtist, Title: row.EntryTitle}
	if row.MusicBrainzRecordingID != uuid.Nil {
		tags.MusicBrainzRecordingID = row.MusicBrainzRecordingID.String()
	}
	return tags
}

// importOnAnchor judges a copy for a want MusicBrainz has no recording for,
// against the want's anchor and its entry (ADR 0037 §3). The library holds no
// file proven to be a recording nobody has named, and AcoustID answers in
// recordings, so neither is asked. Only the anchor admits.
func (importer *Importer) importOnAnchor(
	ctx context.Context, row db.TargetImportRow, file inspected,
	evidence *db.AcquiredFileEvidence, audio copyAudio,
) error {
	importer.measureAgainstAnchor(ctx, row, audio, evidence)
	importer.measureQuality(ctx, file.source, evidence)

	measuredMS := audio.milliseconds()
	verification := identity.VerifyAgainstAnchor(
		candidateEvidence(file, evidence, measuredMS, uuid.Nil, row.AnchorSource),
		identity.Evidence{
			Subject: identity.SubjectEntry, ISRC: row.EntryISRC, Artist: row.EntryArtist,
			Title: row.EntryTitle, DurationMS: row.EntryDurationMS,
		})
	evidence.Agrees, evidence.Differs = verification.Agrees, verification.Differs
	evidence.Wanted = &db.ImportTags{
		Artist: row.EntryArtist, Album: row.EntryAlbum, Title: row.EntryTitle,
		ISRC: row.EntryISRC, DurationMS: row.EntryDurationMS,
	}
	if evidence.Anchor != nil && (verification.AnchorSaysOtherwise || verification.AnchorAgrees) {
		agrees := verification.AnchorAgrees
		evidence.Anchor.Agrees = &agrees
	}
	key, keyed := db.SourceKeyOf(row)
	switch {
	case verification.Admitted && keyed:
		if below, reason := importer.belowFloor(row, file.source); below {
			return importer.hold(ctx, row, file.source, evidence, belowWantFloorSummary, reason)
		}
		return importer.admitOnAnchor(ctx, row, file, evidence, key, verification.Summary)
	case verification.Refused:
		return importer.refuse(ctx, row, evidence,
			db.AcquiredFileDiscardedAudio, anchorRefusedSummary, verification.Summary)
	case verification.NamesARecording:
		return importer.hold(ctx, row, file.source, evidence,
			unresolvedRecordingSummary, verification.Summary)
	case verification.Contradicted:
		return importer.hold(ctx, row, file.source, evidence,
			entryContradictedSummary, verification.Summary)
	}
	if uploadSaidOtherAudio(evidence.Anchor) {
		return importer.hold(ctx, row, file.source, evidence,
			uploadUnmatchedSummary, verification.Summary)
	}
	return importer.hold(ctx, row, file.source, evidence, undecidedSummary, verification.Summary)
}

// belowFloor reports whether a copy falls below its want's minimum bit rate,
// and says so with both numbers (ADR 0038 §7). Only a keyed want has a floor.
//
// The bit rate is the one the file reports, never what the peer claimed. A
// lossless file meets any floor. A file whose bit rate cannot be read has not
// shown it meets the floor, so it is held too.
func (importer *Importer) belowFloor(row db.TargetImportRow, source string) (bool, string) {
	floor := row.MinimumBitrate
	if floor <= 0 || sources.Lossless(filepath.Ext(source)) {
		return false, ""
	}
	properties, err := importer.properties(source)
	if err != nil || properties.BitRateKbps <= 0 {
		return true, fmt.Sprintf("The audio is the track at the keyed address. Its bit rate could not "+
			"be read, so it is not shown to meet this want's %d kbit/s minimum.", floor)
	}
	if properties.BitRateKbps < floor {
		return true, fmt.Sprintf("The audio is the track at the keyed address. It is %d kbit/s, "+
			"below this want's %d kbit/s minimum.", properties.BitRateKbps, floor)
	}
	return false, ""
}

// admitOnAnchor imports a copy its want's anchor proved, and records the
// anchor's address as what the copy is. Completing the copy once the scan has
// made a library file of it writes that address as a source identity
// (ADR 0037 §4).
//
// The file keeps the tags it arrived with. Nothing proved the names, and
// tagging writes only what was proven.
func (importer *Importer) admitOnAnchor(
	ctx context.Context, row db.TargetImportRow, file inspected,
	evidence *db.AcquiredFileEvidence, key db.ImportSource, summary string,
) error {
	evidence.Method = identity.MethodPublishedSample
	evidence.Confidence = 1
	evidence.Source = &key

	files := []validatedFile{{request: row.File, source: file.source}}
	destination := importer.releaseFolder(ctx, wantFields(row, nil))
	localPaths, placed, err := importer.copyRelease(ctx, destination, files)
	if err != nil {
		return err
	}
	importer.recordPlacements(ctx, row.RequestID, placed, localPaths, files)
	if err := importer.store.CompleteTargetImport(ctx, db.RecordAcquiredFileParams{
		AcquisitionTargetID: row.AcquisitionTargetID,
		Provider:            row.Provider,
		SourceUsername:      row.SourceUsername,
		RemotePath:          row.File.Path,
		FileName:            row.File.Name,
		SizeBytes:           row.File.SizeBytes,
		Verdict:             db.AcquiredFileAccepted,
		Summary:             acceptedSummary + " " + summary,
		Evidence:            evidence,
		DownloadRequestID:   uuid.NullUUID{UUID: row.RequestID, Valid: true},
	}, importedTargetSummary, importer.libraryPath, destination, localPaths); err != nil {
		return err
	}
	importer.logger.Info().
		Str("request_id", row.RequestID.String()).
		Str("acquisition_target_id", row.AcquisitionTargetID.String()).
		Str("anchor_source", row.AnchorSource).
		Str("external_id", key.ExternalID).
		Str("path", destination).
		Msg("a fetched candidate was proven by its want's anchor and imported")
	importer.releaseSource(ctx, row.RequestID, files, placed, localPaths)
	return importer.endTheProviderWait(ctx, row.AcquisitionTargetID)
}

// wantFields is the release one fetched copy belongs to, in the shape the
// layout renders.
//
// Which release that is comes from the audio: the release group the copy was
// identified against, exactly as it did before the catalogue was consulted at
// all. A want is resolved through a release group, but the file that arrives is
// whatever the peer sent, and what it turned out to be outranks what was asked
// for. Only where nothing identified it does the want's own release group
// answer. The layout migration reads the release the same way round, so an
// import and a migration file one file in one folder.
//
// The name of that release comes from the catalogue wherever the catalogue
// holds an album for it, and each source is used whole. Half a name from one
// beside an identifier from another files a release under a folder no source
// ever described.
//
//  1. The album row, which is the same row a whole release's import files its
//     folder under — the same artist, the same title, the same identifier — so
//     one album is one folder however its tracks arrived. It is also the only
//     source that spells an artist one way: a credit is written per track, so
//     it reads "Ufo361 feat. KC Rebell" on a guest appearance and "RIN" where
//     the album row says "Rin".
//  2. What the audio turned out to be, for a release group the catalogue holds
//     no album for. Tracks of that release still land together, under the
//     credit each of them carries.
//  3. What the want said, when nothing identified the audio and the catalogue
//     answers for the want's release group either. The folder is then named for
//     the music rather than for the release, which is the best a want on its
//     own can do.
func wantFields(row db.TargetImportRow, found *identity.Identity) library.Fields {
	// The album the catalogue holds for the release the audio named. The claim
	// carries one album — the one the want was resolved through — so it answers
	// only where the audio agrees that is the release, which is the ordinary
	// case: a want names a recording on that release, and a copy of it is
	// identified there.
	if strings.TrimSpace(row.AlbumArtist) != "" && namesTheWantsRelease(row, found) {
		return catalogueFields(
			row.AlbumArtist, row.AlbumTitle, row.MusicBrainzReleaseGroupID, uuid.Nil,
		)
	}
	fields := library.Fields{
		Artist: row.EntryArtist,
		// A want with no title at all still has a file, and its name is the only
		// thing left to call the folder. Nothing is left to the layout to invent
		// here because two unnamed wants would then share one folder.
		Album: firstNamed(row.EntryTitle, row.File.Name),
		// No identifier: a want on its own knows a recording, and a recording is
		// unique to one track. Rendering it beside an album's name would give
		// every track of that album a folder of its own — the very thing filing
		// by the release is for. Silence here files them together instead.
	}
	if found == nil {
		return fields
	}
	fields.Artist = firstNamed(found.ArtistName, fields.Artist)
	fields.Album = firstNamed(found.ReleaseTitle, fields.Album)
	// The release the recording belongs to, which is what the album folder is
	// about: every track of it renders the same name, so the thirteenth to
	// arrive joins the twelve already there.
	if found.ReleaseGroupID != uuid.Nil {
		fields.ID = found.ReleaseGroupID.String()[:8]
	}
	return fields
}

// namesTheWantsRelease reports whether the release the audio was identified
// against is the one the want was resolved through, which is what makes the
// catalogue's album the album this file is on.
//
// A copy nobody identified is the want's own copy to file: nothing contradicts
// the release it was fetched for. A copy identified on another release is filed
// as that release instead, because a folder says which album the music is on and
// the audio is what knows.
func namesTheWantsRelease(row db.TargetImportRow, found *identity.Identity) bool {
	if found == nil || found.ReleaseGroupID == uuid.Nil {
		return true
	}
	return row.MusicBrainzReleaseGroupID.Valid &&
		row.MusicBrainzReleaseGroupID.UUID == found.ReleaseGroupID
}

// recordingTags is the recording a file was checked against, in the shape the
// evidence stores. The want's own words stand in wherever the provider said
// nothing, so the comparison never reads as though the recording had no artist.
func recordingTags(row db.TargetImportRow, recording *identity.Candidate) *db.ImportTags {
	tags := &db.ImportTags{
		Artist: row.EntryArtist, Title: row.EntryTitle,
		MusicBrainzRecordingID: row.MusicBrainzRecordingID.String(),
	}
	if recording == nil {
		return tags
	}
	if strings.TrimSpace(recording.ArtistName) != "" {
		tags.Artist = recording.ArtistName
	}
	if strings.TrimSpace(recording.TrackTitle) != "" {
		tags.Title = recording.TrackTitle
	}
	tags.Album = recording.ReleaseTitle
	tags.ISRC = recording.ISRC
	if recording.DurationMS != nil {
		tags.DurationMS = *recording.DurationMS
	}
	return tags
}

// wantedTags copies the recording a file was checked against onto the evidence,
// so a question read months later still says what the answer was supposed to be
// even if MusicBrainz has changed its mind since.
func wantedTags(proven *identity.Identity) *db.ImportTags {
	tags := &db.ImportTags{
		Artist: proven.ArtistName, Album: proven.ReleaseTitle, Title: proven.TrackTitle,
		MusicBrainzRecordingID: proven.RecordingID.String(), ISRC: proven.ISRC,
	}
	if proven.DurationMS != nil {
		tags.DurationMS = *proven.DurationMS
	}
	return tags
}

// firstNamed is the first of these that says anything, and the same word the
// release path uses when nothing does.
func firstNamed(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return "Unknown"
}

// ErrNoVerifier reports an installation that fetched a candidate and has nothing
// to check it with. It is a configuration failure rather than a fact about the
// file, so it is retried and never recorded as a verdict.
var ErrNoVerifier = errors.New("nothing is configured to verify a fetched candidate")

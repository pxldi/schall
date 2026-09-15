package downloads

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/pxldi/schall/internal/acoustid"
	"github.com/pxldi/schall/internal/db"
	"github.com/pxldi/schall/internal/identity"
	"github.com/pxldi/schall/internal/library"
	"github.com/pxldi/schall/internal/transcode"
	"github.com/rs/zerolog"
)

type fakeImportStore struct {
	row         db.DownloadImportRow
	review      string
	evidence    *db.ImportEvidence
	root        string
	destination string
	localPaths  map[string]db.ImportedDownloadFile
	// retention is the stored policy; removed and retentionDetail are what the
	// importer reported about the provider's copy afterwards.
	retention       string
	retentionSet    bool
	removed         bool
	retentionDetail string
	// transcoded is every file written into the library smaller than it
	// arrived; transcodeRefused is every re-encode the policy asked for that
	// did not happen.
	transcoded       []db.RecordTranscodeParams
	transcodeRefused []string
	// acceptedByHand is a person's decision already recorded about this copy.
	acceptedByHand bool
	// What a second pass over copies already decided about sees and does.
	judgeAgain    []db.JudgeAgainRow
	judgeAgainErr error
	// The copies refused on the artist tag alone, offered by the other pass.
	creditRefusals    []db.JudgeAgainRow
	creditRefusalsErr error
	answeredWants     map[uuid.UUID]bool
	reopened          []uuid.UUID
	released          []uuid.UUID
	reopenErr         error
	// What a copy whose bytes have gone was measured to be against the want's
	// anchor, and the sentence written under it. Neither moves a verdict.
	anchorReadings  map[uuid.UUID]db.ImportAnchor
	anchorSummaries map[uuid.UUID]string
	// rearmed is every want a judging pass asked to look again, and rearms is
	// what the store answers about each of them.
	rearmed       []uuid.UUID
	rearms        bool
	judgingQueued []struct {
		targetID uuid.UUID
		runAfter time.Time
	}
	// recheckExhausted makes the store answer that the want has asked the
	// provider as often as it is going to, and recheckSpentSummary is the
	// sentence it was handed for that case.
	recheckExhausted    bool
	recheckSpentSummary string
	// waitEnded is every want the importer asked to stop waiting on the
	// provider, in order.
	waitEnded []uuid.UUID
	// onComplete runs inside the transaction the importer treats as the point
	// the managed copy becomes durable, so a test can disturb the library
	// exactly between the copy and the retention policy acting on it.
	onComplete func(map[string]db.ImportedDownloadFile)
	// The want side of the same store: one file fetched for one recording, and
	// what it came to.
	servesWant bool
	targetRow  db.TargetImportRow
	accepted   *db.RecordAcquiredFileParams
	// What the want was left saying once the copy was let in.
	targetSummary string
	settled       *db.SettleAcquiredFileParams
	// settledGoneRecording is what was recorded when a fetched copy could not be
	// checked because MusicBrainz no longer knows the recording its want named.
	settledGoneRecording *db.RecordingGoneParams
	settleGoneErr        error
	// What the database refuses. Each stands for the store being unreachable at
	// exactly that call, which is what decides whether an import is retried or
	// left recorded as an answer.
	servesWantErr  error
	claimErr       error
	reviewErr      error
	completeErr    error
	settingsErr    error
	retentionErr   error
	targetClaimErr error
	acceptedErr    error
	settleErr      error
	// witnesses is what the library holds that is already proven to be a
	// recording, and witnessAsked is which recordings it was asked about.
	witnesses    map[uuid.UUID][]db.LibraryWitnessRow
	witnessAsked []uuid.UUID
	witnessErr   error
	// The backfill pass over the held copies whose audio was never measured.
	unfingerprinted    []db.UnfingerprintedCopyRow
	fingerprinted      map[uuid.UUID]string
	fingerprintSeconds int
	unfingerprintedErr error
	fingerprintErr     error
}

func (store *fakeImportStore) DownloadRequestServesWant(
	context.Context, uuid.UUID,
) (bool, error) {
	return store.servesWant, store.servesWantErr
}

func (store *fakeImportStore) ClaimTargetImport(
	context.Context, uuid.UUID,
) (db.TargetImportRow, error) {
	return store.targetRow, store.targetClaimErr
}

func (store *fakeImportStore) CompleteTargetImport(
	_ context.Context, verdict db.RecordAcquiredFileParams, targetSummary string,
	root, destination string, paths map[string]db.ImportedDownloadFile,
) error {
	if store.completeErr != nil {
		return store.completeErr
	}
	store.accepted = &verdict
	store.targetSummary = targetSummary
	store.root, store.destination, store.localPaths = root, destination, paths
	if store.onComplete != nil {
		store.onComplete(paths)
	}
	return nil
}

func (store *fakeImportStore) UserAcceptedCopy(
	_ context.Context, _ uuid.UUID,
) (bool, error) {
	return store.acceptedByHand, store.acceptedErr
}

// What a second pass over copies already decided about reads and writes. The
// offers are whatever a test put there; reopened records which requests the pass
// put back into validation, in order, because that order is what stops a want
// taking two copies.
func (store *fakeImportStore) CopiesToJudgeAgain(
	_ context.Context,
) ([]db.JudgeAgainRow, error) {
	return store.judgeAgain, store.judgeAgainErr
}

// The copies refused on the artist tag alone, kept apart from the offers above
// because the two passes offer different sets.
func (store *fakeImportStore) CopiesRefusedOnCreditAlone(
	_ context.Context,
) ([]db.JudgeAgainRow, error) {
	return store.creditRefusals, store.creditRefusalsErr
}

// The two a pass over one newly anchored want uses. The offers are filtered by
// want here the way the query filters them, and anchorReadings records what a
// copy with no bytes left was measured to be.
func (store *fakeImportStore) CopiesToJudgeAgainForWant(
	_ context.Context, targetID uuid.UUID,
) ([]db.JudgeAgainRow, error) {
	if store.judgeAgainErr != nil {
		return nil, store.judgeAgainErr
	}
	var forWant []db.JudgeAgainRow
	for _, row := range store.judgeAgain {
		if row.AcquisitionTargetID == targetID {
			forWant = append(forWant, row)
		}
	}
	return forWant, nil
}

// The real store reads whether any copy of the want is still stopped on the
// registration question; this records that it was asked and, when the test says
// the question is answered, forgets the wait.
func (store *fakeImportStore) EndTheMusicBrainzWaitIfAnswered(
	_ context.Context, targetID uuid.UUID,
) error {
	store.waitEnded = append(store.waitEnded, targetID)
	return nil
}

// The real store keeps the schedule and the cap; this records what it was asked
// and answers with whatever the test says the want has left.
func (store *fakeImportStore) RecheckAfterMusicBrainzSilence(
	_ context.Context, targetID uuid.UUID, now time.Time, spentSummary string,
) (bool, error) {
	store.recheckSpentSummary = spentSummary
	if store.recheckExhausted {
		return false, nil
	}
	store.judgingQueued = append(store.judgingQueued, struct {
		targetID uuid.UUID
		runAfter time.Time
	}{targetID, now})
	return true, nil
}

func (store *fakeImportStore) RecordCopyAnchorReading(
	_ context.Context, copyID uuid.UUID, anchor db.ImportAnchor, summary string,
) error {
	if store.anchorReadings == nil {
		store.anchorReadings = map[uuid.UUID]db.ImportAnchor{}
		store.anchorSummaries = map[uuid.UUID]string{}
	}
	store.anchorReadings[copyID] = anchor
	store.anchorSummaries[copyID] = summary
	return nil
}

func (store *fakeImportStore) WantHasAcceptedCopy(
	_ context.Context, targetID uuid.UUID,
) (bool, error) {
	return store.answeredWants[targetID], nil
}

func (store *fakeImportStore) RearmWantNowAnchored(
	_ context.Context, targetID uuid.UUID, _ string,
) (bool, error) {
	store.rearmed = append(store.rearmed, targetID)
	return store.rearms, nil
}

func (store *fakeImportStore) ReopenCopyForJudging(
	_ context.Context, requestID uuid.UUID,
) error {
	if store.reopenErr != nil {
		return store.reopenErr
	}
	store.reopened = append(store.reopened, requestID)
	return nil
}

func (store *fakeImportStore) ReleaseCopyFromJudging(
	_ context.Context, requestID uuid.UUID,
) error {
	store.released = append(store.released, requestID)
	return nil
}

// What the backfill pass over held copies reads and writes. unfingerprinted is
// whatever a test put there; fingerprinted records what the pass measured, by
// copy, because a pass that wrote nothing and a pass that wrote the wrong thing
// look the same from the outside.
func (store *fakeImportStore) CopiesMissingFingerprint(
	_ context.Context, limit int32,
) ([]db.UnfingerprintedCopyRow, error) {
	if store.unfingerprintedErr != nil {
		return nil, store.unfingerprintedErr
	}
	if int32(len(store.unfingerprinted)) > limit {
		return store.unfingerprinted[:limit], nil
	}
	return store.unfingerprinted, nil
}

func (store *fakeImportStore) RecordCopyFingerprint(
	_ context.Context, copyID uuid.UUID, print string, seconds int,
) error {
	if store.fingerprintErr != nil {
		return store.fingerprintErr
	}
	if store.fingerprinted == nil {
		store.fingerprinted = map[uuid.UUID]string{}
	}
	store.fingerprinted[copyID] = print
	store.fingerprintSeconds = seconds
	return nil
}

func (store *fakeImportStore) SettleAcquiredFile(
	_ context.Context, params db.SettleAcquiredFileParams,
) error {
	if store.settleErr != nil {
		return store.settleErr
	}
	store.settled = &params
	return nil
}

func (store *fakeImportStore) SettleAcquiredFileForGoneRecording(
	_ context.Context, params db.RecordingGoneParams,
) error {
	if store.settleGoneErr != nil {
		return store.settleGoneErr
	}
	store.settledGoneRecording = &params
	return nil
}

// What the library has to say about a recording. A test that puts no files here
// is a library holding nothing settled for the want, which is the ordinary case
// for music nobody owns a copy of yet.
func (store *fakeImportStore) SettledWitnesses(
	_ context.Context, recordingID uuid.UUID, limit int32,
) ([]db.LibraryWitnessRow, error) {
	if store.witnessErr != nil {
		return nil, store.witnessErr
	}
	store.witnessAsked = append(store.witnessAsked, recordingID)
	offered := store.witnesses[recordingID]
	if int32(len(offered)) > limit {
		offered = offered[:limit]
	}
	return offered, nil
}

func (store *fakeImportStore) ClaimDownloadImport(context.Context, uuid.UUID) (db.DownloadImportRow, error) {
	return store.row, store.claimErr
}
func (store *fakeImportStore) MarkDownloadImportNeedsReview(
	_ context.Context, _ uuid.UUID, detail string, evidence *db.ImportEvidence,
) error {
	if store.reviewErr != nil {
		return store.reviewErr
	}
	store.review, store.evidence = detail, evidence
	return nil
}
func (store *fakeImportStore) CompleteDownloadImport(
	_ context.Context, _ uuid.UUID, root, destination string, paths map[string]db.ImportedDownloadFile,
) error {
	if store.completeErr != nil {
		return store.completeErr
	}
	store.root, store.destination, store.localPaths = root, destination, paths
	if store.onComplete != nil {
		store.onComplete(paths)
	}
	return nil
}
func (store *fakeImportStore) ImportSettings(context.Context) (db.ImportSettingsRow, error) {
	if store.settingsErr != nil {
		return db.ImportSettingsRow{}, store.settingsErr
	}
	retention := store.retention
	if retention == "" {
		retention = db.SourceRetentionKeep
	}
	return db.ImportSettingsRow{SourceRetention: retention}, nil
}
func (store *fakeImportStore) RecordSourceRetention(
	_ context.Context, _ uuid.UUID, removed bool, detail string,
) error {
	if store.retentionErr != nil {
		return store.retentionErr
	}
	store.retentionSet, store.removed, store.retentionDetail = true, removed, detail
	return nil
}

func (store *fakeImportStore) RecordTranscode(
	_ context.Context, _ uuid.UUID, params db.RecordTranscodeParams,
) error {
	store.transcoded = append(store.transcoded, params)
	return nil
}

func (store *fakeImportStore) RecordTranscodeRefused(
	_ context.Context, _ uuid.UUID, detail string,
) error {
	store.transcodeRefused = append(store.transcodeRefused, detail)
	return nil
}

// The catalogue album these fixtures are downloads of. The folder a release is
// filed under is named after the release, so the fixtures need one.
var fixtureAlbum = uuid.MustParse("aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa")

func TestImporterValidatesAndCopiesACompleteRelease(t *testing.T) {
	inbox, libraryPath := t.TempDir(), t.TempDir()
	folder := filepath.Join(inbox, "Album")
	if err := os.Mkdir(folder, 0o755); err != nil {
		t.Fatal(err)
	}
	files := []db.DownloadRequestFile{
		{Path: `Music\Album\01.flac`, Name: "01.flac", SizeBytes: 3},
		{Path: `Music\Album\02.flac`, Name: "02.flac", SizeBytes: 4},
	}
	if err := os.WriteFile(filepath.Join(folder, "01.flac"), []byte("one"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(folder, "02.flac"), []byte("four"), 0o644); err != nil {
		t.Fatal(err)
	}
	requestID := uuid.MustParse("11111111-1111-1111-1111-111111111111")
	store := &fakeImportStore{row: db.DownloadImportRow{
		RequestID: requestID, AlbumID: fixtureAlbum, ArtistName: "Artist", AlbumTitle: "Album",
		SourceDirectory: `Remote\Album`, Files: files,
		Tracks: []db.ImportTrack{
			{Title: "First", DiscNumber: 1, TrackNumber: 1, DurationMS: 100_000},
			{Title: "Second", DiscNumber: 1, TrackNumber: 2, DurationMS: 200_000},
		},
	}}
	importer := NewImporter(store, inbox, libraryPath, zerolog.Nop())
	importer.inspect = func(path string) library.AudioMetadata {
		if filepath.Base(path) == "01.flac" {
			return library.AudioMetadata{
				Artist: "Artist", Album: "Album", Title: "First",
				DiscNumber: 1, TrackNumber: 1, DurationMS: 101_000,
			}
		}
		return library.AudioMetadata{
			Artist: "Artist", Album: "Album", Title: "Second",
			DiscNumber: 1, TrackNumber: 2, DurationMS: 199_000,
		}
	}

	if err := importer.Import(context.Background(), requestID); err != nil {
		t.Fatal(err)
	}
	if store.review != "" || store.root != libraryPath {
		t.Fatalf("review = %q, root = %q", store.review, store.root)
	}
	// Named for the release rather than for the download, so a second import of
	// this album lands where the first one is instead of beside it.
	if !strings.HasSuffix(store.destination, filepath.Join("Artist", "Album [aaaaaaaa]")) {
		t.Fatalf("destination = %q", store.destination)
	}
	for _, file := range files {
		local := store.localPaths[file.Path].LocalPath
		info, err := os.Stat(local)
		if err != nil {
			t.Fatal(err)
		}
		if info.Size() != file.SizeBytes {
			t.Fatalf("%s size = %d", local, info.Size())
		}
	}
}

// A file that names a different release is not refused for that alone, but
// nothing else here corroborates it: no duration on either side, and a title
// and position that another edition could just as easily share. That is a
// question for review rather than a match.
func TestImporterPausesConflictingTagsForReview(t *testing.T) {
	inbox, libraryPath := t.TempDir(), t.TempDir()
	folder := filepath.Join(inbox, "Album")
	if err := os.Mkdir(folder, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(folder, "01.flac"), []byte("one"), 0o644); err != nil {
		t.Fatal(err)
	}
	requestID := uuid.New()
	store := &fakeImportStore{row: db.DownloadImportRow{
		RequestID: requestID, AlbumID: fixtureAlbum, ArtistName: "Artist", AlbumTitle: "Album",
		SourceDirectory: "Album",
		Files:           []db.DownloadRequestFile{{Path: "01.flac", Name: "01.flac", SizeBytes: 3}},
		Tracks:          []db.ImportTrack{{Title: "Expected", DiscNumber: 1, TrackNumber: 1}},
	}}
	importer := NewImporter(store, inbox, libraryPath, zerolog.Nop())
	importer.inspect = func(string) library.AudioMetadata {
		return library.AudioMetadata{
			Artist: "Artist", Album: "Different album", Title: "Expected", TrackNumber: 1,
		}
	}

	if err := importer.Import(context.Background(), requestID); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(store.review, "is not conclusively any track of this edition") ||
		!strings.Contains(store.review, "album") {
		t.Fatalf("review = %q", store.review)
	}
	if store.destination != "" {
		t.Fatalf("copied an invalid release to %q", store.destination)
	}
	// Review is given the answer it would have to choose anyway.
	candidates := store.evidence.Files[0].Candidates
	if len(candidates) != 1 || candidates[0].Track.Title != "Expected" {
		t.Fatalf("candidates = %#v", candidates)
	}
}

// The first real pause in production: a single whose only source was a rip of
// the album that later collected it. Validation used to look for a catalogue
// track at position 1-4, find nothing, and stop — for a file that agreed with
// the catalogue on artist, title and length to the second.
func TestImporterImportsARecordingCollectedOnAnotherRelease(t *testing.T) {
	inbox, libraryPath := t.TempDir(), t.TempDir()
	folder := filepath.Join(inbox, "Squids")
	if err := os.Mkdir(folder, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(folder, "04 - Squids.flac"), []byte("audio"), 0o644); err != nil {
		t.Fatal(err)
	}
	trackID := uuid.New()
	requestID := uuid.New()
	store := &fakeImportStore{row: db.DownloadImportRow{
		RequestID: requestID, AlbumID: fixtureAlbum, ArtistName: "Sewerslvt", AlbumTitle: "Squids",
		SourceDirectory: "Squids",
		Files: []db.DownloadRequestFile{
			{Path: `Music\Squids\04 - Squids.flac`, Name: "04 - Squids.flac", SizeBytes: 5},
		},
		Tracks: []db.ImportTrack{
			{ID: trackID, Title: "Squids", DiscNumber: 1, TrackNumber: 1, DurationMS: 344_000},
		},
	}}
	importer := NewImporter(store, inbox, libraryPath, zerolog.Nop())
	importer.inspect = func(string) library.AudioMetadata {
		return library.AudioMetadata{
			Artist: "Sewerslvt", Album: "Drowning in the Sewer", Title: "Squids",
			DiscNumber: 1, TrackNumber: 4, DurationMS: 344_000,
		}
	}

	if err := importer.Import(context.Background(), requestID); err != nil {
		t.Fatal(err)
	}
	if store.review != "" || store.destination == "" {
		t.Fatalf("review = %q, destination = %q", store.review, store.destination)
	}
	// The track it was imported as is the provenance matching will trust later,
	// so it has to be the catalogue track rather than the tagged one.
	if store.localPaths[`Music\Squids\04 - Squids.flac`].TrackID != trackID {
		t.Fatalf("provenance = %#v", store.localPaths)
	}
}

// A release where several things are wrong must be reviewable as a whole. The
// old behaviour reported only the first reason, which hid the fact that the
// downloaded folder was a different edition entirely.
func TestImporterReportsEveryDisagreementAsEvidence(t *testing.T) {
	inbox, libraryPath := t.TempDir(), t.TempDir()
	folder := filepath.Join(inbox, "Album")
	if err := os.Mkdir(folder, 0o755); err != nil {
		t.Fatal(err)
	}
	for name, content := range map[string]string{"01.flac": "one", "02.flac": "two"} {
		if err := os.WriteFile(filepath.Join(folder, name), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	requestID := uuid.New()
	store := &fakeImportStore{row: db.DownloadImportRow{
		RequestID: requestID, AlbumID: fixtureAlbum, ArtistName: "Artist", AlbumTitle: "Album",
		SourceDirectory: "Album",
		Files: []db.DownloadRequestFile{
			{Path: "01.flac", Name: "01.flac", SizeBytes: 3},
			{Path: "02.flac", Name: "02.flac", SizeBytes: 3},
			// Promised by the source but absent from the folder.
			{Path: "03.flac", Name: "03.flac", SizeBytes: 3},
		},
		Tracks: []db.ImportTrack{
			{Title: "First", DiscNumber: 1, TrackNumber: 1, DurationMS: 100_000},
			{Title: "Second", DiscNumber: 1, TrackNumber: 2, DurationMS: 200_000},
			{Title: "Third", DiscNumber: 1, TrackNumber: 3},
		},
	}}
	importer := NewImporter(store, inbox, libraryPath, zerolog.Nop())
	importer.inspect = func(path string) library.AudioMetadata {
		if filepath.Base(path) == "01.flac" {
			return library.AudioMetadata{
				Artist: "Artist", Album: "Album", Title: "First",
				DiscNumber: 1, TrackNumber: 1, DurationMS: 100_500,
			}
		}
		// A different recording of the second track: right position, wrong
		// title, and far too long for the catalogue duration.
		return library.AudioMetadata{
			Artist: "Artist", Album: "Album", Title: "Second (live)",
			DiscNumber: 1, TrackNumber: 2, DurationMS: 400_000,
		}
	}

	if err := importer.Import(context.Background(), requestID); err != nil {
		t.Fatal(err)
	}
	if store.destination != "" {
		t.Fatalf("copied an invalid release to %q", store.destination)
	}
	evidence := store.evidence
	if evidence == nil {
		t.Fatal("no evidence was recorded")
	}
	if len(evidence.Files) != 3 {
		t.Fatalf("files = %#v", evidence.Files)
	}
	// The file that matched is reported too, so review sees the whole release.
	if len(evidence.Files[0].Problems) != 0 || evidence.Files[0].Expected == nil ||
		evidence.Files[0].Expected.Title != "First" {
		t.Fatalf("first file = %#v", evidence.Files[0])
	}
	// The second file is not the second track: the title and the duration both
	// say so, and review is told which track it came closest to and why that
	// was not enough.
	problems := strings.Join(evidence.Files[1].Problems, " | ")
	if !strings.Contains(problems, "is not conclusively any track of this edition") ||
		!strings.Contains(problems, "title") || !strings.Contains(problems, "duration") {
		t.Fatalf("second file problems = %q", problems)
	}
	if evidence.Files[1].Observed == nil || evidence.Files[1].Observed.Title != "Second (live)" ||
		evidence.Files[1].Expected != nil {
		t.Fatalf("second file = %#v", evidence.Files[1])
	}
	closest := evidence.Files[1].Candidates
	if len(closest) != 1 || closest[0].Track.Title != "Second" ||
		strings.Join(closest[0].Differs, ", ") == "" {
		t.Fatalf("second file candidates = %#v", closest)
	}
	if len(evidence.Files[2].Problems) != 1 ||
		!strings.Contains(evidence.Files[2].Problems[0], "not in the provider folder") {
		t.Fatalf("third file = %#v", evidence.Files[2])
	}
	// Neither the live recording nor the file that never arrived covers its
	// track, so both tracks are still missing from this release.
	if len(evidence.UnmatchedTracks) != 2 || evidence.UnmatchedTracks[0].Title != "Second" ||
		evidence.UnmatchedTracks[1].Title != "Third" {
		t.Fatalf("unmatched = %#v", evidence.UnmatchedTracks)
	}
	if !strings.Contains(store.review, "2 of 3 downloaded files") {
		t.Fatalf("review = %q", store.review)
	}
}

// A file count that cannot cover the edition used to stop validation before
// anything was compared, leaving review with a number and no evidence.
func TestImporterComparesFilesEvenWhenTheCountIsWrong(t *testing.T) {
	inbox, libraryPath := t.TempDir(), t.TempDir()
	folder := filepath.Join(inbox, "Album")
	if err := os.Mkdir(folder, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(folder, "01.flac"), []byte("one"), 0o644); err != nil {
		t.Fatal(err)
	}
	requestID := uuid.New()
	store := &fakeImportStore{row: db.DownloadImportRow{
		RequestID: requestID, AlbumID: fixtureAlbum, ArtistName: "Artist", AlbumTitle: "Album",
		SourceDirectory: "Album",
		Files:           []db.DownloadRequestFile{{Path: "01.flac", Name: "01.flac", SizeBytes: 3}},
		Tracks: []db.ImportTrack{
			{Title: "First", DiscNumber: 1, TrackNumber: 1},
			{Title: "Second", DiscNumber: 1, TrackNumber: 2},
		},
	}}
	importer := NewImporter(store, inbox, libraryPath, zerolog.Nop())
	importer.inspect = func(string) library.AudioMetadata {
		return library.AudioMetadata{
			Artist: "Artist", Album: "Album", Title: "First", DiscNumber: 1, TrackNumber: 1,
		}
	}

	if err := importer.Import(context.Background(), requestID); err != nil {
		t.Fatal(err)
	}
	if store.destination != "" {
		t.Fatalf("copied an incomplete release to %q", store.destination)
	}
	evidence := store.evidence
	if evidence == nil || len(evidence.Files) != 1 || len(evidence.Files[0].Problems) != 0 {
		t.Fatalf("evidence = %#v", evidence)
	}
	if len(evidence.UnmatchedTracks) != 1 || evidence.UnmatchedTracks[0].Title != "Second" {
		t.Fatalf("unmatched = %#v", evidence.UnmatchedTracks)
	}
	if !strings.Contains(store.review, "1 files but the catalogue edition has 2 tracks") {
		t.Fatalf("review = %q", store.review)
	}
}

// A recorded judgement settles the identity question it was made about, and
// nothing else: the release still has to be complete and the bytes still have
// to be what the source promised.
func TestImporterAppliesARecordedDecision(t *testing.T) {
	inbox, libraryPath := t.TempDir(), t.TempDir()
	folder := filepath.Join(inbox, "Album")
	if err := os.Mkdir(folder, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(folder, "01.flac"), []byte("one"), 0o644); err != nil {
		t.Fatal(err)
	}
	trackID := uuid.New()
	requestID := uuid.New()
	observed := library.AudioMetadata{
		Artist: "Artist", Album: "Album", Title: "First (alternate mix)",
		DiscNumber: 1, TrackNumber: 1,
	}
	row := db.DownloadImportRow{
		RequestID: requestID, AlbumID: fixtureAlbum, ArtistName: "Artist", AlbumTitle: "Album",
		SourceDirectory: "Album",
		Files:           []db.DownloadRequestFile{{Path: "01.flac", Name: "01.flac", SizeBytes: 3}},
		Tracks:          []db.ImportTrack{{ID: trackID, Title: "First", DiscNumber: 1, TrackNumber: 1}},
	}
	store := &fakeImportStore{row: row}
	importer := NewImporter(store, inbox, libraryPath, zerolog.Nop())
	importer.inspect = func(string) library.AudioMetadata { return observed }

	// Without a decision the title disagreement pauses the import, and the
	// evidence says a person could settle it.
	if err := importer.Import(context.Background(), requestID); err != nil {
		t.Fatal(err)
	}
	if store.destination != "" || !store.evidence.Files[0].Resolvable {
		t.Fatalf("destination = %q, evidence = %#v", store.destination, store.evidence.Files[0])
	}

	fingerprint := observedTags(observed, 1).Fingerprint()
	store.row.Decisions = map[string]db.ImportDecision{
		"01.flac": {FileName: "01.flac", TrackID: trackID, TrackTitle: "First", Fingerprint: fingerprint},
	}
	store.review, store.evidence = "", nil
	if err := importer.Import(context.Background(), requestID); err != nil {
		t.Fatal(err)
	}
	if store.review != "" || store.destination == "" {
		t.Fatalf("review = %q, destination = %q", store.review, store.destination)
	}
	if store.localPaths["01.flac"].TrackID != trackID {
		t.Fatalf("provenance = %#v", store.localPaths)
	}
}

// A remembered decision must never carry over to different audio, and it can
// never admit a file that is damaged rather than merely disputed.
func TestImporterRefusesADecisionThatNoLongerDescribesTheFile(t *testing.T) {
	inbox, libraryPath := t.TempDir(), t.TempDir()
	folder := filepath.Join(inbox, "Album")
	if err := os.Mkdir(folder, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(folder, "01.flac"), []byte("longer"), 0o644); err != nil {
		t.Fatal(err)
	}
	trackID := uuid.New()
	requestID := uuid.New()
	store := &fakeImportStore{row: db.DownloadImportRow{
		RequestID: requestID, AlbumID: fixtureAlbum, ArtistName: "Artist", AlbumTitle: "Album",
		SourceDirectory: "Album",
		// The source promised three bytes; the folder holds six.
		Files:  []db.DownloadRequestFile{{Path: "01.flac", Name: "01.flac", SizeBytes: 3}},
		Tracks: []db.ImportTrack{{ID: trackID, Title: "First", DiscNumber: 1, TrackNumber: 1}},
		Decisions: map[string]db.ImportDecision{
			"01.flac": {FileName: "01.flac", TrackID: trackID, Fingerprint: "decided against other tags"},
		},
	}}
	importer := NewImporter(store, inbox, libraryPath, zerolog.Nop())
	importer.inspect = func(string) library.AudioMetadata {
		return library.AudioMetadata{Artist: "Artist", Album: "Album", Title: "Something else",
			DiscNumber: 1, TrackNumber: 1}
	}

	if err := importer.Import(context.Background(), requestID); err != nil {
		t.Fatal(err)
	}
	if store.destination != "" {
		t.Fatalf("imported a file the decision no longer describes to %q", store.destination)
	}
	file := store.evidence.Files[0]
	problems := strings.Join(file.Problems, " | ")
	if !strings.Contains(problems, "no longer says what it said") {
		t.Fatalf("problems = %q", problems)
	}
	// The size disagreement is not a judgement call, so no decision can settle
	// this file until it is downloaded again.
	if !strings.Contains(problems, "promised 3") || file.Resolvable {
		t.Fatalf("file = %#v", file)
	}
}

// completeRelease sets up a folder that validates cleanly, so a test about
// what happens after an import is not also a test of matching. The extra file
// is something Schall never validated and must therefore never remove.
func completeRelease(t *testing.T) (*fakeImportStore, *Importer, string, uuid.UUID) {
	t.Helper()
	inbox, libraryPath := t.TempDir(), t.TempDir()
	folder := filepath.Join(inbox, "Album")
	if err := os.Mkdir(folder, 0o755); err != nil {
		t.Fatal(err)
	}
	files := []db.DownloadRequestFile{
		{Path: `Music\Album\01.flac`, Name: "01.flac", SizeBytes: 3},
		{Path: `Music\Album\02.flac`, Name: "02.flac", SizeBytes: 4},
	}
	if err := os.WriteFile(filepath.Join(folder, "01.flac"), []byte("one"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(folder, "02.flac"), []byte("four"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(folder, "cover.jpg"), []byte("art"), 0o644); err != nil {
		t.Fatal(err)
	}
	requestID := uuid.MustParse("22222222-2222-2222-2222-222222222222")
	store := &fakeImportStore{row: db.DownloadImportRow{
		RequestID: requestID, AlbumID: fixtureAlbum, ArtistName: "Artist", AlbumTitle: "Album",
		SourceDirectory: `Remote\Album`, Files: files,
		Tracks: []db.ImportTrack{
			{Title: "First", DiscNumber: 1, TrackNumber: 1, DurationMS: 100_000},
			{Title: "Second", DiscNumber: 1, TrackNumber: 2, DurationMS: 200_000},
		},
	}}
	importer := NewImporter(store, inbox, libraryPath, zerolog.Nop())
	importer.inspect = func(path string) library.AudioMetadata {
		if filepath.Base(path) == "01.flac" {
			return library.AudioMetadata{
				Artist: "Artist", Album: "Album", Title: "First",
				DiscNumber: 1, TrackNumber: 1, DurationMS: 101_000,
			}
		}
		return library.AudioMetadata{
			Artist: "Artist", Album: "Album", Title: "Second",
			DiscNumber: 1, TrackNumber: 2, DurationMS: 199_000,
		}
	}
	return store, importer, folder, requestID
}

func TestImporterKeepsTheProviderCopyByDefault(t *testing.T) {
	store, importer, folder, requestID := completeRelease(t)

	if err := importer.Import(context.Background(), requestID); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"01.flac", "02.flac", "cover.jpg"} {
		if _, err := os.Stat(filepath.Join(folder, name)); err != nil {
			t.Fatalf("%s was removed under the default policy: %v", name, err)
		}
	}
	// Keeping is the absence of an action, so it is not history worth writing.
	if store.retentionSet {
		t.Fatalf("retention recorded under the default policy: %q", store.retentionDetail)
	}
}

func TestImporterRemovesOnlyTheImportedFilesWhenAskedTo(t *testing.T) {
	store, importer, folder, requestID := completeRelease(t)
	store.retention = db.SourceRetentionDelete

	if err := importer.Import(context.Background(), requestID); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"01.flac", "02.flac"} {
		if _, err := os.Stat(filepath.Join(folder, name)); !os.IsNotExist(err) {
			t.Fatalf("%s survived the delete policy: %v", name, err)
		}
	}
	// Artwork was never validated and is therefore not Schall' to delete, which
	// also means the folder is not empty and stays.
	if _, err := os.Stat(filepath.Join(folder, "cover.jpg")); err != nil {
		t.Fatalf("a file Schall never imported was removed: %v", err)
	}
	if !store.retentionSet || !store.removed {
		t.Fatalf("retention = %#v", store)
	}
	if !strings.Contains(store.retentionDetail, "2 imported file(s) removed") {
		t.Fatalf("detail = %q", store.retentionDetail)
	}
	// The managed copies are the point of the whole exercise.
	for _, imported := range store.localPaths {
		if _, err := os.Stat(imported.LocalPath); err != nil {
			t.Fatal(err)
		}
	}
}

// A managed copy that is not there at all is the same refusal as one that is
// the wrong size: nothing was confirmed, so nothing is removed.
func TestImporterRemovesNothingWhenAManagedCopyIsNotThere(t *testing.T) {
	store, importer, folder, requestID := completeRelease(t)
	store.retention = db.SourceRetentionDelete
	store.onComplete = func(paths map[string]db.ImportedDownloadFile) {
		if err := os.Remove(paths[`Music\Album\02.flac`].LocalPath); err != nil {
			t.Fatal(err)
		}
	}

	if err := importer.Import(context.Background(), requestID); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"01.flac", "02.flac"} {
		if _, err := os.Stat(filepath.Join(folder, name)); err != nil {
			t.Fatalf("%s was removed despite a managed copy that is not there: %v", name, err)
		}
	}
	if !strings.Contains(store.retentionDetail, "could not be confirmed") {
		t.Fatalf("detail = %q", store.retentionDetail)
	}
}

func TestImporterRemovesNothingWhenAManagedCopyCannotBeConfirmed(t *testing.T) {
	store, importer, folder, requestID := completeRelease(t)
	store.retention = db.SourceRetentionDelete
	store.onComplete = func(paths map[string]db.ImportedDownloadFile) {
		// One managed copy is no longer the file that was validated. Removing
		// the provider's copy of the other one would leave a release that can be
		// completed from neither side.
		if err := os.Truncate(paths[`Music\Album\02.flac`].LocalPath, 1); err != nil {
			t.Fatal(err)
		}
	}

	if err := importer.Import(context.Background(), requestID); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"01.flac", "02.flac", "cover.jpg"} {
		if _, err := os.Stat(filepath.Join(folder, name)); err != nil {
			t.Fatalf("%s was removed despite an unconfirmed managed copy: %v", name, err)
		}
	}
	if !store.retentionSet || store.removed {
		t.Fatalf("retention = %#v", store)
	}
	if !strings.Contains(store.retentionDetail, "nothing was removed") {
		t.Fatalf("detail = %q", store.retentionDetail)
	}
}

// oneFileRelease is the smallest folder that validates: one file, one track,
// tags that agree. Tests about a single refusal change one thing about it.
func oneFileRelease(t *testing.T) (*fakeImportStore, *Importer, string, uuid.UUID) {
	t.Helper()
	inbox, libraryPath := t.TempDir(), t.TempDir()
	folder := filepath.Join(inbox, "Album")
	if err := os.Mkdir(folder, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(folder, "01.flac"), []byte("one"), 0o644); err != nil {
		t.Fatal(err)
	}
	requestID := uuid.MustParse("44444444-4444-4444-4444-444444444444")
	store := &fakeImportStore{row: db.DownloadImportRow{
		RequestID: requestID, AlbumID: fixtureAlbum, ArtistName: "Artist", AlbumTitle: "Album",
		SourceDirectory: "Album",
		Files:           []db.DownloadRequestFile{{Path: "01.flac", Name: "01.flac", SizeBytes: 3}},
		Tracks: []db.ImportTrack{
			{ID: uuid.New(), Title: "First", DiscNumber: 1, TrackNumber: 1, DurationMS: 100_000},
		},
	}}
	importer := NewImporter(store, inbox, libraryPath, zerolog.Nop())
	importer.inspect = func(string) library.AudioMetadata {
		return library.AudioMetadata{
			Artist: "Artist", Album: "Album", Title: "First",
			DiscNumber: 1, TrackNumber: 1, DurationMS: 100_000,
		}
	}
	return store, importer, inbox, requestID
}

// The two shapes of import ask different questions — one file against one
// recording, or a whole folder against an edition — so an import that cannot
// tell which it is does neither rather than guessing.
func TestNoImportRunsWhenTheKindOfRequestCannotBeRead(t *testing.T) {
	store, importer, _, requestID := oneFileRelease(t)
	store.servesWantErr = errors.New("the requests table is unreachable")

	err := importer.Import(context.Background(), requestID)
	if !errors.Is(err, store.servesWantErr) {
		t.Fatalf("error = %v, want the read failure reported", err)
	}
	if store.review != "" || store.destination != "" {
		t.Fatalf("review = %q, destination = %q", store.review, store.destination)
	}
}

// A release import that could not be claimed is one another worker may already
// be validating, so nothing is read and nothing is recorded.
func TestNothingIsValidatedForAReleaseImportThatCannotBeClaimed(t *testing.T) {
	store, importer, _, requestID := oneFileRelease(t)
	store.claimErr = errors.New("the requests table is unreachable")

	err := importer.Import(context.Background(), requestID)
	if !errors.Is(err, store.claimErr) {
		t.Fatalf("error = %v, want the claim failure reported", err)
	}
	if store.review != "" {
		t.Fatalf("review = %q, want nothing recorded", store.review)
	}
}

// An edition with no tracks cannot be matched against, and no amount of
// retrying will give it any. It is an answer rather than a failure.
func TestImporterRefusesAnEditionWithNoTracks(t *testing.T) {
	store, importer, _, requestID := oneFileRelease(t)
	store.row.Tracks = nil

	if err := importer.Import(context.Background(), requestID); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(store.review, "no tracks") {
		t.Fatalf("review = %q", store.review)
	}
	if store.evidence != nil {
		t.Fatalf("evidence = %#v, want none where nothing could be compared", store.evidence)
	}
}

// The provider's folder name is what the inbox path is built from, so a name
// that is nothing at all has nowhere to look.
func TestImporterRefusesAProviderFolderWithNoSafeLocalName(t *testing.T) {
	store, importer, _, requestID := oneFileRelease(t)
	store.row.SourceDirectory = "  "

	if err := importer.Import(context.Background(), requestID); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(store.review, "no safe local name") {
		t.Fatalf("review = %q", store.review)
	}
}

// The inbox is the boundary of what an import may read. A folder that resolves
// outside it is refused as a failure rather than recorded as a review decision:
// nothing about the download is wrong, the configuration is.
func TestImporterRefusesAFolderThatResolvesOutsideTheInbox(t *testing.T) {
	store, importer, inbox, requestID := oneFileRelease(t)
	elsewhere := t.TempDir()
	if err := os.WriteFile(filepath.Join(elsewhere, "01.flac"), []byte("one"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(elsewhere, filepath.Join(inbox, "Escape")); err != nil {
		t.Fatal(err)
	}
	store.row.SourceDirectory = "Escape"

	err := importer.Import(context.Background(), requestID)
	if err == nil || !strings.Contains(err.Error(), "escapes the configured inbox") {
		t.Fatalf("error = %v, want the escape refused", err)
	}
	if store.destination != "" || store.review != "" {
		t.Fatalf("destination = %q, review = %q", store.destination, store.review)
	}
}

func TestImporterRefusesAProviderFolderThatIsAFile(t *testing.T) {
	store, importer, inbox, requestID := oneFileRelease(t)
	if err := os.WriteFile(filepath.Join(inbox, "NotAFolder"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	store.row.SourceDirectory = "NotAFolder"

	err := importer.Import(context.Background(), requestID)
	if err == nil || !strings.Contains(err.Error(), "not a directory") {
		t.Fatalf("error = %v, want a file refused as a provider folder", err)
	}
}

// A position the edition uses twice identifies nothing, but that is a defect in
// the catalogue rather than in the download, so it is reported and matching
// carries on with the rest of the evidence.
func TestImporterReportsAnEditionThatUsesOnePositionTwice(t *testing.T) {
	store, importer, _, requestID := oneFileRelease(t)
	store.row.Tracks = append(store.row.Tracks, db.ImportTrack{
		ID: uuid.New(), Title: "Also first", DiscNumber: 1, TrackNumber: 1, DurationMS: 300_000,
	})

	if err := importer.Import(context.Background(), requestID); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(store.review, "lists position 1-1 more than once") {
		t.Fatalf("review = %q", store.review)
	}
	// The file was still compared, which is the point of carrying on.
	if store.evidence == nil || len(store.evidence.Files) != 1 {
		t.Fatalf("evidence = %#v", store.evidence)
	}
}

// A name with a path in it would read from somewhere else in the inbox, or from
// outside it. Only a plain filename is ever joined to the provider folder.
func TestImporterRefusesARequestedNameThatIsNotAPlainFilename(t *testing.T) {
	store, importer, _, requestID := oneFileRelease(t)
	store.row.Files[0].Name = filepath.Join("..", "01.flac")

	if err := importer.Import(context.Background(), requestID); err != nil {
		t.Fatal(err)
	}
	problems := strings.Join(store.evidence.Files[0].Problems, " | ")
	if !strings.Contains(problems, "unsafe") {
		t.Fatalf("problems = %q", problems)
	}
	if store.destination != "" {
		t.Fatalf("imported from an unsafe name to %q", store.destination)
	}
}

func TestImporterRefusesAnEntryThatIsNotARegularFile(t *testing.T) {
	store, importer, inbox, requestID := oneFileRelease(t)
	if err := os.Remove(filepath.Join(inbox, "Album", "01.flac")); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(inbox, "Album", "01.flac"), 0o755); err != nil {
		t.Fatal(err)
	}

	if err := importer.Import(context.Background(), requestID); err != nil {
		t.Fatal(err)
	}
	problems := strings.Join(store.evidence.Files[0].Problems, " | ")
	if !strings.Contains(problems, "not a regular file") {
		t.Fatalf("problems = %q", problems)
	}
}

// A name the filesystem cannot even look up is not the download disagreeing
// with the catalogue, so it is a failure rather than a review decision.
func TestImporterFailsOnANameTheFilesystemCannotLookUp(t *testing.T) {
	store, importer, _, requestID := oneFileRelease(t)
	store.row.Files[0].Name = strings.Repeat("n", 300) + ".flac"

	err := importer.Import(context.Background(), requestID)
	if err == nil || !strings.Contains(err.Error(), "inspect downloaded file") {
		t.Fatalf("error = %v, want the lookup failure reported", err)
	}
	if store.review != "" {
		t.Fatalf("review = %q, want nothing recorded about the music", store.review)
	}
}

// An import that is being shut down stops before it reads anything, rather than
// recording a half-gathered folder as a review decision.
func TestImporterStopsWhenTheImportIsCancelled(t *testing.T) {
	store, importer, _, requestID := oneFileRelease(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	if err := importer.Import(ctx, requestID); !errors.Is(err, context.Canceled) {
		t.Fatalf("error = %v, want the cancellation reported", err)
	}
	if store.review != "" || store.destination != "" {
		t.Fatalf("review = %q, destination = %q", store.review, store.destination)
	}
}

// A decision naming a track this edition does not hold is a decision about
// something else. It settles the file — nothing else may claim it — and says so.
func TestImporterRefusesADecisionForATrackTheEditionDoesNotContain(t *testing.T) {
	store, importer, _, requestID := oneFileRelease(t)
	store.row.Decisions = map[string]db.ImportDecision{
		"01.flac": {FileName: "01.flac", TrackID: uuid.New(), TrackTitle: "Something else"},
	}

	if err := importer.Import(context.Background(), requestID); err != nil {
		t.Fatal(err)
	}
	problems := strings.Join(store.evidence.Files[0].Problems, " | ")
	if !strings.Contains(problems, "the selected edition does not contain") {
		t.Fatalf("problems = %q", problems)
	}
	if !store.evidence.Files[0].Resolvable {
		t.Fatal("a decision about the wrong track is one a person can make again")
	}
}

// A recording ID is proof of identity, so what the file carries reaches review
// and matching rather than being read and dropped.
func TestImporterRecordsTheRecordingIDAFileCarries(t *testing.T) {
	store, importer, _, requestID := oneFileRelease(t)
	recordingID := uuid.New()
	importer.inspect = func(string) library.AudioMetadata {
		return library.AudioMetadata{
			Artist: "Artist", Album: "Album", Title: "Something else",
			DiscNumber: 1, TrackNumber: 9, MusicBrainzRecordingID: &recordingID,
		}
	}

	if err := importer.Import(context.Background(), requestID); err != nil {
		t.Fatal(err)
	}
	observed := store.evidence.Files[0].Observed
	if observed == nil || observed.MusicBrainzRecordingID != recordingID.String() {
		t.Fatalf("observed = %+v, want the recording ID the file carries", observed)
	}
}

// A release the catalogue has no artist for is still imported; it goes under a
// name rather than under an empty path component.
func TestImporterFilesAReleaseWithNoArtistUnderAName(t *testing.T) {
	store, importer, _, requestID := oneFileRelease(t)
	store.row.ArtistName = "  "
	importer.inspect = func(string) library.AudioMetadata {
		return library.AudioMetadata{
			Album: "Album", Title: "First", DiscNumber: 1, TrackNumber: 1, DurationMS: 100_000,
		}
	}

	if err := importer.Import(context.Background(), requestID); err != nil {
		t.Fatal(err)
	}
	if store.destination == "" {
		t.Fatalf("review = %q, want the release imported", store.review)
	}
	if filepath.Base(filepath.Dir(store.destination)) != "Unknown" {
		t.Fatalf("destination = %q, want a named folder", store.destination)
	}
}

// The audio is only asked about files there is something to ask about. A file
// that never matched a track has nothing to be compared with, and one whose
// bytes are wrong is already settled on other grounds.
func TestOnlyMatchedFilesAreListenedTo(t *testing.T) {
	inbox, libraryPath := t.TempDir(), t.TempDir()
	folder := filepath.Join(inbox, "Album")
	if err := os.Mkdir(folder, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"01.flac", "02.flac"} {
		if err := os.WriteFile(filepath.Join(folder, name), []byte("one"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	recordingID := uuid.New()
	requestID := uuid.New()
	store := &fakeImportStore{row: db.DownloadImportRow{
		RequestID: requestID, AlbumID: fixtureAlbum, ArtistName: "Artist", AlbumTitle: "Album",
		SourceDirectory: "Album",
		Files: []db.DownloadRequestFile{
			{Path: "01.flac", Name: "01.flac", SizeBytes: 3},
			{Path: "02.flac", Name: "02.flac", SizeBytes: 3},
		},
		Tracks: []db.ImportTrack{{
			ID: uuid.New(), Title: "First", DiscNumber: 1, TrackNumber: 1,
			DurationMS: 100_000, MusicBrainzRecordingID: &recordingID,
		}},
	}}
	importer := NewImporter(store, inbox, libraryPath, zerolog.Nop())
	importer.inspect = func(path string) library.AudioMetadata {
		if filepath.Base(path) == "01.flac" {
			return library.AudioMetadata{
				Artist: "Artist", Album: "Album", Title: "First",
				DiscNumber: 1, TrackNumber: 1, DurationMS: 100_000,
			}
		}
		return library.AudioMetadata{Artist: "Artist", Album: "Album", Title: "Unlisted"}
	}
	identifier := &fakeIdentifier{configured: true}
	importer = importer.WithAcousticIdentifier(identifier)

	if err := importer.Import(context.Background(), requestID); err != nil {
		t.Fatal(err)
	}
	if len(identifier.asked) != 1 || identifier.asked[0] != "01.flac" {
		t.Fatalf("listened to %v, want only the file that matched a track", identifier.asked)
	}
}

// A provider folder that held nothing but the imported files is removed with
// them, because a folder Schall emptied is not a folder the provider is using.
func TestImporterRemovesAProviderFolderItsRemovalsEmptied(t *testing.T) {
	store, importer, inbox, requestID := oneFileRelease(t)
	store.retention = db.SourceRetentionDelete

	if err := importer.Import(context.Background(), requestID); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(inbox, "Album")); !os.IsNotExist(err) {
		t.Fatalf("the emptied provider folder survived: %v", err)
	}
	if !strings.Contains(store.retentionDetail, "empty and removed as well") {
		t.Fatalf("detail = %q", store.retentionDetail)
	}
}

// A removal that fails partway says how far it got. A policy that quietly left
// half a folder behind would leave a release that can be replayed from neither
// side without anybody knowing why.
func TestImporterReportsASourceFileItCouldNotRemove(t *testing.T) {
	store, importer, folder, requestID := completeRelease(t)
	store.retention = db.SourceRetentionDelete
	store.onComplete = func(map[string]db.ImportedDownloadFile) {
		// The second file becomes a folder holding something, which os.Remove
		// refuses — the same refusal a read-only provider folder gives.
		blocked := filepath.Join(folder, "02.flac")
		if err := os.Remove(blocked); err != nil {
			t.Fatal(err)
		}
		if err := os.Mkdir(blocked, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(blocked, "held"), []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	if err := importer.Import(context.Background(), requestID); err != nil {
		t.Fatal(err)
	}
	if !store.retentionSet || store.removed {
		t.Fatalf("retention = %#v, want the failure recorded", store)
	}
	if !strings.Contains(store.retentionDetail, "could not be removed") ||
		!strings.Contains(store.retentionDetail, "1 of 2 removed") {
		t.Fatalf("detail = %q, want it to say how far the removal got", store.retentionDetail)
	}
}

// A file with no recorded managed copy is one this import cannot prove it saved,
// and nothing is removed on the strength of a copy nobody can point at.
func TestRetentionRemovesNothingWhenAFileHasNoRecordedManagedCopy(t *testing.T) {
	importer := NewImporter(&fakeImportStore{}, t.TempDir(), t.TempDir(), zerolog.Nop())
	files := []validatedFile{{
		request: db.DownloadRequestFile{Path: `Music\01.flac`, Name: "01.flac", SizeBytes: 3},
		source:  filepath.Join(t.TempDir(), "01.flac"),
	}}

	placed := []transcode.Placement{{Source: files[0].source, Name: "01.flac", SizeBytes: 3}}
	removed, detail := importer.removeSource(files, placed, map[string]db.ImportedDownloadFile{})

	if removed {
		t.Fatal("files were removed without a managed copy to point at")
	}
	if !strings.Contains(detail, "no recorded managed copy") {
		t.Fatalf("detail = %q", detail)
	}
}

// A retention policy nobody could read is not a licence to delete. The provider
// copy is kept and the import stands.
func TestImporterKeepsTheProviderCopyWhenThePolicyCannotBeRead(t *testing.T) {
	store, importer, folder, requestID := completeRelease(t)
	store.settingsErr = errors.New("the settings table is unreachable")

	if err := importer.Import(context.Background(), requestID); err != nil {
		t.Fatalf("a policy that could not be read failed the import: %v", err)
	}
	if store.destination == "" {
		t.Fatal("the release was not imported")
	}
	for _, name := range []string{"01.flac", "02.flac"} {
		if _, err := os.Stat(filepath.Join(folder, name)); err != nil {
			t.Fatalf("%s was removed under an unreadable policy: %v", name, err)
		}
	}
	if store.retentionSet {
		t.Fatalf("retention recorded without a policy: %q", store.retentionDetail)
	}
}

// Nothing after the import committed may fail it. The managed copy and its
// provenance are durable by then, and a second attempt would import them twice.
func TestAnImportStandsWhenWhatBecameOfTheSourceCannotBeRecorded(t *testing.T) {
	store, importer, _, requestID := completeRelease(t)
	store.retention = db.SourceRetentionDelete
	store.retentionErr = errors.New("the history table is unreachable")

	if err := importer.Import(context.Background(), requestID); err != nil {
		t.Fatalf("an unrecordable retention failed a committed import: %v", err)
	}
	if store.destination == "" {
		t.Fatal("the release was not imported")
	}
}

// A review decision that could not be stored must not read as an import that
// quietly succeeded: the job fails and is tried again.
func TestAReviewDecisionThatCannotBeRecordedFailsTheImport(t *testing.T) {
	store, importer, _, requestID := oneFileRelease(t)
	store.row.Tracks = nil
	store.reviewErr = errors.New("the requests table is unreachable")

	err := importer.Import(context.Background(), requestID)
	if !errors.Is(err, store.reviewErr) {
		t.Fatalf("error = %v, want the failure to store the decision reported", err)
	}
}

// The import transaction is what makes the managed copy durable, so nothing is
// removed from the provider folder until it has committed.
func TestNothingIsRemovedFromTheProviderFolderWhenTheImportCannotCommit(t *testing.T) {
	store, importer, folder, requestID := completeRelease(t)
	store.retention = db.SourceRetentionDelete
	store.completeErr = errors.New("the imports table is unreachable")

	err := importer.Import(context.Background(), requestID)
	if !errors.Is(err, store.completeErr) {
		t.Fatalf("error = %v, want the commit failure reported", err)
	}
	for _, name := range []string{"01.flac", "02.flac"} {
		if _, err := os.Stat(filepath.Join(folder, name)); err != nil {
			t.Fatalf("%s was removed for an import that never committed: %v", name, err)
		}
	}
	if store.retentionSet {
		t.Fatal("retention was applied to an import that never committed")
	}
}

// An import replayed over a folder that is already there adopts the files that
// were copied rather than copying them again. The job queue retries, and a
// second copy under a new name would be a duplicate nobody asked for.
func TestAReplayedImportAdoptsTheFilesItAlreadyCopied(t *testing.T) {
	store, importer, _, requestID := completeRelease(t)

	if err := importer.Import(context.Background(), requestID); err != nil {
		t.Fatal(err)
	}
	first := store.destination
	store.destination, store.localPaths = "", nil

	if err := importer.Import(context.Background(), requestID); err != nil {
		t.Fatal(err)
	}
	if store.destination != first {
		t.Fatalf("second import went to %q, want the folder it had already filled %q",
			store.destination, first)
	}
	for _, imported := range store.localPaths {
		if _, err := os.Stat(imported.LocalPath); err != nil {
			t.Fatal(err)
		}
	}
}

// A managed copy that is no longer the file that was validated must not be
// recorded as provenance. The import fails and is looked at rather than
// silently pointing the catalogue at something else.
func TestAReplayedImportFailsWhenAnImportedFileIsNotWhatWasValidated(t *testing.T) {
	store, importer, _, requestID := completeRelease(t)

	if err := importer.Import(context.Background(), requestID); err != nil {
		t.Fatal(err)
	}
	for _, imported := range store.localPaths {
		if err := os.Truncate(imported.LocalPath, 1); err != nil {
			t.Fatal(err)
		}
		break
	}

	err := importer.Import(context.Background(), requestID)
	if err == nil || !strings.Contains(err.Error(), "does not match the validated source") {
		t.Fatalf("error = %v, want the altered copy refused", err)
	}
}

// A copy the folder does not hold is copied again rather than reported missing.
//
// It used to be reported: the folder was the import's own, so a file absent
// from it had been taken away by something outside. A release is one folder now
// and a file that is not in it is the ordinary case — the next track of the
// album — so the two cannot be told apart, and the answer that finishes the
// import is the one that puts the validated bytes back.
func TestAReplayedImportCopiesAnImportedFileThatIsGoneAgain(t *testing.T) {
	store, importer, _, requestID := completeRelease(t)

	if err := importer.Import(context.Background(), requestID); err != nil {
		t.Fatal(err)
	}
	var removed string
	for _, imported := range store.localPaths {
		if err := os.Remove(imported.LocalPath); err != nil {
			t.Fatal(err)
		}
		removed = imported.LocalPath
		break
	}

	if err := importer.Import(context.Background(), requestID); err != nil {
		t.Fatalf("error = %v, want the missing copy replaced", err)
	}
	if _, err := os.Stat(removed); err != nil {
		t.Fatalf("stat = %v, want the file back in the library", err)
	}
}

// A destination that exists and is not a folder is a name collision nobody can
// resolve from here, and copying into it would be copying over somebody's file.
func TestImporterRefusesADestinationThatIsNotADirectory(t *testing.T) {
	store, importer, _, requestID := completeRelease(t)
	occupied := filepath.Join(importer.libraryPath, "Artist")
	if err := os.MkdirAll(occupied, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(occupied, "Album [aaaaaaaa]"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}

	err := importer.Import(context.Background(), requestID)
	if err == nil || !strings.Contains(err.Error(), "is not a directory") {
		t.Fatalf("error = %v, want the occupied name refused", err)
	}
	if store.destination != "" {
		t.Fatalf("recorded an import to %q", store.destination)
	}
}

// A library path that cannot hold the folder at all is a failure to report, not
// a decision about the download.
func TestImporterFailsWhenTheLibraryPathCannotHoldTheRelease(t *testing.T) {
	store, importer, _, requestID := completeRelease(t)
	if err := os.WriteFile(filepath.Join(importer.libraryPath, "Artist"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}

	if err := importer.Import(context.Background(), requestID); err == nil {
		t.Fatal("an import into a blocked library path reported success")
	}
	if store.review != "" {
		t.Fatalf("review = %q, want nothing recorded about the music", store.review)
	}
}

// A copy that failed halfway leaves nothing behind. The staging folder is what
// makes that possible: the managed folder appears whole or not at all.
func TestACopyThatFailsLeavesNoStagingFolderBehind(t *testing.T) {
	libraryPath := t.TempDir()
	importer := NewImporter(&fakeImportStore{}, t.TempDir(), libraryPath, zerolog.Nop())
	destination := filepath.Join(libraryPath, "Artist", "Album")

	_, _, err := importer.copyRelease(context.Background(), destination, []validatedFile{{
		request: db.DownloadRequestFile{Path: "01.flac", Name: "01.flac", SizeBytes: 3},
		source:  filepath.Join(t.TempDir(), "never-written.flac"),
	}})
	if err == nil {
		t.Fatal("copying a source that is not there reported success")
	}
	entries, readErr := os.ReadDir(filepath.Join(libraryPath, "Artist"))
	if readErr != nil {
		t.Fatal(readErr)
	}
	if len(entries) != 0 {
		t.Fatalf("left %v behind", entries)
	}
}

// A copy that is cancelled midway leaves nothing behind either. Shutting Schall
// down must not leave a half-filled folder that the next scan indexes.
func TestACancelledCopyLeavesNoStagingFolderBehind(t *testing.T) {
	inbox, libraryPath := t.TempDir(), t.TempDir()
	source := filepath.Join(inbox, "01.flac")
	if err := os.WriteFile(source, []byte("one"), 0o644); err != nil {
		t.Fatal(err)
	}
	importer := NewImporter(&fakeImportStore{}, inbox, libraryPath, zerolog.Nop())
	destination := filepath.Join(libraryPath, "Artist", "Album")

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, _, err := importer.copyRelease(ctx, destination, []validatedFile{{
		request: db.DownloadRequestFile{Path: "01.flac", Name: "01.flac", SizeBytes: 3},
		source:  source,
	}})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("error = %v, want the cancellation reported", err)
	}
	entries, readErr := os.ReadDir(filepath.Join(libraryPath, "Artist"))
	if readErr != nil {
		t.Fatal(readErr)
	}
	if len(entries) != 0 {
		t.Fatalf("left %v behind", entries)
	}
}

// A release is one folder however many times music arrives, so the thirteenth
// track of an album has to be able to join the twelve already filed. The folder
// existing used to mean the import was a repeat, and everything after the first
// arrival failed on a file the folder did not hold yet.
func TestAFileJoinsTheReleaseFolderAnEarlierArrivalMade(t *testing.T) {
	inbox, libraryPath := t.TempDir(), t.TempDir()
	importer := NewImporter(&fakeImportStore{}, inbox, libraryPath, zerolog.Nop())
	destination := filepath.Join(libraryPath, "Artist", "Album [aaaaaaaa]")
	first := stagedFile(t, inbox, "01.flac", "one")
	second := stagedFile(t, inbox, "02.flac", "two!")

	if _, _, err := importer.copyRelease(context.Background(), destination, []validatedFile{first}); err != nil {
		t.Fatal(err)
	}
	localPaths, _, err := importer.copyRelease(context.Background(), destination, []validatedFile{second})
	if err != nil {
		t.Fatalf("copying into the release folder = %v", err)
	}
	if got := localPaths[second.request.Path].LocalPath; got != filepath.Join(destination, "02.flac") {
		t.Fatalf("local path = %q, want the file beside the first one", got)
	}
	for _, name := range []string{"01.flac", "02.flac"} {
		if _, err := os.Stat(filepath.Join(destination, name)); err != nil {
			t.Fatalf("stat %q = %v, want both arrivals in the folder", name, err)
		}
	}
}

// The answer the folder-exists path has always given: an import arriving twice
// keeps the copy that landed first rather than writing it again.
func TestAnImportArrivingTwiceKeepsTheCopyThatLanded(t *testing.T) {
	inbox, libraryPath := t.TempDir(), t.TempDir()
	importer := NewImporter(&fakeImportStore{}, inbox, libraryPath, zerolog.Nop())
	destination := filepath.Join(libraryPath, "Artist", "Album [aaaaaaaa]")
	file := stagedFile(t, inbox, "01.flac", "one")

	if _, _, err := importer.copyRelease(context.Background(), destination, []validatedFile{file}); err != nil {
		t.Fatal(err)
	}
	localPaths, _, err := importer.copyRelease(context.Background(), destination, []validatedFile{file})
	if err != nil {
		t.Fatalf("importing the same release twice = %v", err)
	}
	if got := localPaths[file.request.Path].LocalPath; got != filepath.Join(destination, "01.flac") {
		t.Fatalf("local path = %q, want the copy that is already there", got)
	}
}

// Which of two files under one name the library keeps is a question for a
// person, and a copy never settles it by overwriting.
func TestAnArrivalDoesNotWriteOverADifferentFileOfTheSameName(t *testing.T) {
	inbox, libraryPath := t.TempDir(), t.TempDir()
	importer := NewImporter(&fakeImportStore{}, inbox, libraryPath, zerolog.Nop())
	destination := filepath.Join(libraryPath, "Artist", "Album [aaaaaaaa]")
	landed := stagedFile(t, inbox, "01.flac", "one")
	if _, _, err := importer.copyRelease(context.Background(), destination, []validatedFile{landed}); err != nil {
		t.Fatal(err)
	}

	other := stagedFile(t, inbox, "other.flac", "different")
	other.request.Name = "01.flac"
	if _, _, err := importer.copyRelease(
		context.Background(), destination, []validatedFile{other},
	); err == nil {
		t.Fatal("a file already in the library was written over")
	}
	kept, err := os.ReadFile(filepath.Join(destination, "01.flac"))
	if err != nil {
		t.Fatal(err)
	}
	if string(kept) != "one" {
		t.Fatalf("the file in the library reads %q, want the copy that was there", kept)
	}
}

// Two different recordings can be the same length, and a shared folder means
// the name may be held by a file another import placed. Accepting it would
// record provenance pointing at somebody else's copy and import nothing.
func TestAnArrivalDoesNotTakeAFileOfTheSameSizeForItsOwn(t *testing.T) {
	inbox, libraryPath := t.TempDir(), t.TempDir()
	importer := NewImporter(&fakeImportStore{}, inbox, libraryPath, zerolog.Nop())
	destination := filepath.Join(libraryPath, "Artist", "Album [aaaaaaaa]")
	landed := stagedFile(t, inbox, "01.flac", "one")
	if _, _, err := importer.copyRelease(context.Background(), destination, []validatedFile{landed}); err != nil {
		t.Fatal(err)
	}

	other := stagedFile(t, inbox, "other.flac", "two")
	other.request.Name = "01.flac"
	if _, _, err := importer.copyRelease(
		context.Background(), destination, []validatedFile{other},
	); err == nil {
		t.Fatal("a file of the same size was taken for this import's own copy")
	}
}

// A folder that already holds music cannot be filled atomically as a whole, so
// the file is what arrives at once — and a copy that did not finish leaves
// nothing a scan would index.
func TestACopyIntoAReleaseFolderLeavesNoHalfWrittenFileBehind(t *testing.T) {
	inbox, libraryPath := t.TempDir(), t.TempDir()
	importer := NewImporter(&fakeImportStore{}, inbox, libraryPath, zerolog.Nop())
	destination := filepath.Join(libraryPath, "Artist", "Album [aaaaaaaa]")
	if _, _, err := importer.copyRelease(context.Background(), destination,
		[]validatedFile{stagedFile(t, inbox, "01.flac", "one")}); err != nil {
		t.Fatal(err)
	}

	missing := validatedFile{
		request: db.DownloadRequestFile{Path: `Remote\02.flac`, Name: "02.flac", SizeBytes: 4},
		source:  filepath.Join(inbox, "never-written.flac"),
	}
	if _, _, err := importer.copyRelease(
		context.Background(), destination, []validatedFile{missing},
	); err == nil {
		t.Fatal("copying a source that is not there reported success")
	}
	entries, err := os.ReadDir(destination)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 {
		t.Fatalf("the folder holds %v, want only the file that landed", entries)
	}
}

// A file is compared to its end rather than to the end of the first chunk read
// of it. Two rips of one track share far more than the opening of a frame.
func TestAnArrivalComparesAFilePastItsFirstChunk(t *testing.T) {
	inbox, libraryPath := t.TempDir(), t.TempDir()
	importer := NewImporter(&fakeImportStore{}, inbox, libraryPath, zerolog.Nop())
	destination := filepath.Join(libraryPath, "Artist", "Album [aaaaaaaa]")
	long := strings.Repeat("a", 200*1024)
	if _, _, err := importer.copyRelease(context.Background(), destination,
		[]validatedFile{stagedFile(t, inbox, "01.flac", long)}); err != nil {
		t.Fatal(err)
	}

	diverging := stagedFile(t, inbox, "other.flac", long[:150*1024]+"b"+long[150*1024+1:])
	diverging.request.Name = "01.flac"
	if _, _, err := importer.copyRelease(
		context.Background(), destination, []validatedFile{diverging},
	); err == nil {
		t.Fatal("two files that differ past the first chunk were taken for one")
	}
}

// Shutting Schall down midway through filling a folder that already holds
// music must leave nothing half written there either.
func TestACancelledCopyIntoAReleaseFolderLeavesNothingBehind(t *testing.T) {
	inbox, libraryPath := t.TempDir(), t.TempDir()
	importer := NewImporter(&fakeImportStore{}, inbox, libraryPath, zerolog.Nop())
	destination := filepath.Join(libraryPath, "Artist", "Album [aaaaaaaa]")
	if _, _, err := importer.copyRelease(context.Background(), destination,
		[]validatedFile{stagedFile(t, inbox, "01.flac", "one")}); err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, _, err := importer.copyRelease(ctx, destination,
		[]validatedFile{stagedFile(t, inbox, "02.flac", "two!")}); !errors.Is(err, context.Canceled) {
		t.Fatalf("error = %v, want the cancellation reported", err)
	}
	entries, err := os.ReadDir(destination)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 {
		t.Fatalf("the folder holds %v, want only the file that landed", entries)
	}
}

// stagedFile writes one file into the inbox and describes it the way validation
// hands it to the copy.
func stagedFile(t *testing.T, inbox, name, content string) validatedFile {
	t.Helper()
	source := filepath.Join(inbox, name)
	if err := os.WriteFile(source, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	return validatedFile{
		request: db.DownloadRequestFile{
			Path: `Remote\` + name, Name: name, SizeBytes: int64(len(content)),
		},
		source: source,
	}
}

// Two remote files under one local name cannot both be the managed copy, and
// the second must not silently overwrite the first.
func TestTwoFilesUnderOneNameAreNotBothCopied(t *testing.T) {
	inbox, libraryPath := t.TempDir(), t.TempDir()
	for _, name := range []string{"a.flac", "b.flac"} {
		if err := os.WriteFile(filepath.Join(inbox, name), []byte("one"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	importer := NewImporter(&fakeImportStore{}, inbox, libraryPath, zerolog.Nop())

	_, _, err := importer.copyRelease(context.Background(),
		filepath.Join(libraryPath, "Artist", "Album"), []validatedFile{
			{request: db.DownloadRequestFile{Path: `Remote\a.flac`, Name: "01.flac", SizeBytes: 3},
				source: filepath.Join(inbox, "a.flac")},
			{request: db.DownloadRequestFile{Path: `Remote\b.flac`, Name: "01.flac", SizeBytes: 3},
				source: filepath.Join(inbox, "b.flac")},
		})
	if err == nil {
		t.Fatal("two files were copied under one name")
	}
}

// storedSettings is the settings row an identifier is built from on every use,
// which is what lets the check be switched on without a restart.
func storedSettings(enabled bool, key string) func(context.Context) (db.ImportSettingsRow, error) {
	return func(context.Context) (db.ImportSettingsRow, error) {
		return db.ImportSettingsRow{AcoustIDEnabled: enabled, AcoustIDAPIKey: key}, nil
	}
}

// A key that was never entered means no check, and no evidence: a check that
// did not run must not leave a trace suggesting it did.
func TestTheAcousticCheckIsOffWithoutAStoredKey(t *testing.T) {
	identifier := NewSettingsIdentifier(storedSettings(true, "   "), "fpcalc")

	if identifier.Configured(context.Background()) {
		t.Fatal("the check reported itself configured with no key")
	}
}

func TestTheAcousticCheckIsOffWhileItIsSwitchedOff(t *testing.T) {
	identifier := NewSettingsIdentifier(storedSettings(false, "a-key"), "fpcalc")

	if identifier.Configured(context.Background()) {
		t.Fatal("the check reported itself configured while switched off")
	}
}

// Settings nobody could read are not permission to fingerprint. Silence is the
// safe answer here, because the check may only ever object.
func TestTheAcousticCheckIsOffWhenTheSettingsCannotBeRead(t *testing.T) {
	identifier := NewSettingsIdentifier(func(context.Context) (db.ImportSettingsRow, error) {
		return db.ImportSettingsRow{}, errors.New("the settings table is unreachable")
	}, "fpcalc")

	if identifier.Configured(context.Background()) {
		t.Fatal("the check reported itself configured on settings nobody could read")
	}
}

// Nothing is fingerprinted while the check is off, and the empty answer is not
// an error: there is nothing wrong, the question was simply not asked.
func TestNothingIsIdentifiedWhileTheAcousticCheckIsOff(t *testing.T) {
	identifier := NewSettingsIdentifier(storedSettings(false, "a-key"), "fpcalc")

	identified, err := identifier.Identify(context.Background(), "/music/01.flac")
	if err != nil {
		t.Fatalf("error = %v, want a check that is off to fail nothing", err)
	}
	if len(identified.Clusters) != 0 {
		t.Fatalf("identified = %+v, want nothing heard", identified)
	}
}

func TestIdentifyingReportsSettingsItCouldNotRead(t *testing.T) {
	identifier := NewSettingsIdentifier(func(context.Context) (db.ImportSettingsRow, error) {
		return db.ImportSettingsRow{}, errors.New("the settings table is unreachable")
	}, "fpcalc")

	if _, err := identifier.Identify(context.Background(), "/music/01.flac"); err == nil {
		t.Fatal("settings nobody could read were treated as a check that heard nothing")
	}
}

// A fingerprinter that is not installed is a failure to report, never an empty
// answer: an empty answer means the audio was heard and the database had never
// heard it, which is a statement about the music.
func TestIdentifyingReportsAFingerprinterItCannotRun(t *testing.T) {
	identifier := NewSettingsIdentifier(storedSettings(true, "a-key"),
		filepath.Join(t.TempDir(), "fpcalc-that-is-not-installed"))

	identified, err := identifier.Identify(context.Background(), "/music/01.flac")
	if err == nil {
		t.Fatalf("identified = %+v, want a missing fingerprinter reported", identified)
	}
}

// The reason a check did not run is stored against the request, so a failure
// that stopped nothing is still visible afterwards.
func TestFailRecordsAReasonWithNoEvidence(t *testing.T) {
	store := &fakeImportStore{}
	importer := NewImporter(store, t.TempDir(), t.TempDir(), zerolog.Nop())

	if err := importer.Fail(context.Background(), uuid.New(), "the provider went away"); err != nil {
		t.Fatal(err)
	}
	if store.review != "the provider went away" {
		t.Fatalf("review = %q", store.review)
	}
	if store.evidence != nil {
		t.Fatalf("evidence = %#v, want none where nothing was compared", store.evidence)
	}
}

func TestSafeComponentCannotCreateNestedImportPaths(t *testing.T) {
	if got := safeComponent("../Artist/Name"); got != "_Artist_Name" {
		t.Fatalf("safeComponent() = %q", got)
	}
}

// A fingerprint is kept so that the audio need not be decoded twice, which is
// worth nothing unless what comes back is what a lookup needs: the fingerprint
// and the length of the audio it was computed from.
func TestAKeptFingerprintCarriesEverythingALookupNeeds(t *testing.T) {
	print := acoustid.Fingerprint{DurationSeconds: 302, Value: "AQADtEmSJEmSJEmS"}
	read, err := readKeptFingerprint(keptFingerprint(print))
	if err != nil {
		t.Fatalf("readKeptFingerprint() error = %v", err)
	}
	if read.DurationSeconds != print.DurationSeconds || read.Value != print.Value {
		t.Fatalf("read = %+v, want %+v", read, print)
	}
}

func TestAFingerprintNobodyCanReadSaysSoRatherThanGuessing(t *testing.T) {
	if _, err := readKeptFingerprint("not a fingerprint"); !errors.Is(
		err, identity.ErrFingerprintUnreadable,
	) {
		t.Fatalf("error = %v, want %v", err, identity.ErrFingerprintUnreadable)
	}
}

func TestIdentityTextTreatsTypographyAsEquivalent(t *testing.T) {
	if !sameText(
		"J.ust keeps getting worse doesn't it? [introshit]",
		"J.ust keeps getting worse doesn’t it? [introshit]",
	) {
		t.Fatal("equivalent apostrophe styles did not match")
	}
}

package db

import (
	"context"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/pxldi/schall/internal/dbtest"
)

// minimalLibraryFile lays down just enough of a library file for these tests:
// something the upgrade_of_library_file_id and acquired_library_file_id
// foreign keys can point at.
func minimalLibraryFile(t *testing.T, pool *pgxpool.Pool) uuid.UUID {
	t.Helper()
	ctx := context.Background()
	var rootID, fileID uuid.UUID
	if err := pool.QueryRow(ctx,
		`INSERT INTO library_roots (path) VALUES ($1) RETURNING id`, t.TempDir(),
	).Scan(&rootID); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `
		INSERT INTO library_files (library_root_id, path, size_bytes, modified_at)
		VALUES ($1, $2, 14, now())
		RETURNING id
	`, rootID, t.TempDir()+"/track.mp3").Scan(&fileID); err != nil {
		t.Fatal(err)
	}
	return fileID
}

func TestCreateUpgradeTargetRaisesAFreshWant(t *testing.T) {
	pool := dbtest.Setup(t)
	queries := New(pool)
	fileID := minimalLibraryFile(t, pool)
	recordingID := uuid.New()

	id, created, err := queries.CreateUpgradeTarget(context.Background(), CreateUpgradeTargetParams{
		LibraryFileID: fileID, RecordingID: recordingID,
		EntryArtist: "An Artist", EntryTitle: "A Title", Summary: "below the floor",
	})
	if err != nil {
		t.Fatal(err)
	}
	if !created {
		t.Fatal("a want for a recording with no existing target was not reported as new")
	}
	target, err := queries.AcquisitionTarget(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	if target.Origin != "upgrade" || target.Status != "pending" {
		t.Fatalf("origin = %s, status = %s, want upgrade/pending", target.Origin, target.Status)
	}
	if !target.UpgradeOfLibraryFileID.Valid || target.UpgradeOfLibraryFileID.UUID != fileID {
		t.Fatalf("upgrade_of_library_file_id = %v, want %v", target.UpgradeOfLibraryFileID, fileID)
	}
}

// A live want already covers this recording — reused, not duplicated, and not
// linked to the file: only a want this feature itself raised may go on to
// replace a file, and a want somebody else is pursuing must not start
// deleting music on this feature's say-so.
func TestCreateUpgradeTargetReusesALiveWant(t *testing.T) {
	pool := dbtest.Setup(t)
	queries := New(pool)
	ctx := context.Background()
	recordingID := uuid.New()

	existingID, _, err := queries.CreateAcquisitionTarget(ctx, CreateAcquisitionTargetParams{
		Origin: "manual", EntryTitle: "A Title",
		MusicBrainzRecordingID: uuid.NullUUID{UUID: recordingID, Valid: true},
		Summary:                "wanted",
	})
	if err != nil {
		t.Fatal(err)
	}

	id, created, err := queries.CreateUpgradeTarget(ctx, CreateUpgradeTargetParams{
		LibraryFileID: minimalLibraryFile(t, pool), RecordingID: recordingID,
		EntryArtist: "An Artist", EntryTitle: "A Title", Summary: "below the floor",
	})
	if err != nil {
		t.Fatal(err)
	}
	if created {
		t.Fatal("a want for a recording already being pursued was reported as new")
	}
	if id != existingID {
		t.Fatalf("id = %v, want the existing want %v", id, existingID)
	}
	target, err := queries.AcquisitionTarget(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	if target.Origin != "manual" {
		t.Fatalf("origin = %s, want the reused want's own origin untouched", target.Origin)
	}
	if target.UpgradeOfLibraryFileID.Valid {
		t.Fatal("a want this sweep did not raise was linked to a file to replace")
	}
}

// The ordinary case this feature exists for: a recording obtained before the
// floor existed. The acquired target is superseded rather than left standing,
// so the recording is never wanted by two live rows at once.
func TestCreateUpgradeTargetSupersedesAnAcquiredWant(t *testing.T) {
	pool := dbtest.Setup(t)
	queries := New(pool)
	ctx := context.Background()
	recordingID := uuid.New()
	fileID := minimalLibraryFile(t, pool)

	acquiredID, _, err := queries.CreateAcquisitionTarget(ctx, CreateAcquisitionTargetParams{
		Origin: "manual", EntryTitle: "A Title",
		MusicBrainzRecordingID: uuid.NullUUID{UUID: recordingID, Valid: true},
		Summary:                "wanted",
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := queries.SettleAcquiredTarget(ctx, acquiredID, fileID, "acquired", "acquired"); err != nil {
		t.Fatal(err)
	}

	id, created, err := queries.CreateUpgradeTarget(ctx, CreateUpgradeTargetParams{
		LibraryFileID: fileID, RecordingID: recordingID,
		EntryArtist: "An Artist", EntryTitle: "A Title", Summary: "below the floor",
	})
	if err != nil {
		t.Fatal(err)
	}
	if !created {
		t.Fatal("superseding an acquired want was not reported as raising a new one")
	}
	if id == acquiredID {
		t.Fatal("the new want was not given its own row")
	}

	old, err := queries.AcquisitionTarget(ctx, acquiredID)
	if err != nil {
		t.Fatal(err)
	}
	if old.Status != "superseded" || !old.SupersededByID.Valid || old.SupersededByID.UUID != id {
		t.Fatalf("old target = %+v, want superseded by %v", old, id)
	}
	fresh, err := queries.AcquisitionTarget(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	if fresh.Origin != "upgrade" || fresh.Status != "pending" {
		t.Fatalf("origin = %s, status = %s, want upgrade/pending", fresh.Origin, fresh.Status)
	}
	if !fresh.UpgradeOfLibraryFileID.Valid || fresh.UpgradeOfLibraryFileID.UUID != fileID {
		t.Fatalf("upgrade_of_library_file_id = %v, want %v", fresh.UpgradeOfLibraryFileID, fileID)
	}
}

// A manual decision to stop pursuing a recording wins, permanently.
func TestCreateUpgradeTargetRefusesANotWantedRecording(t *testing.T) {
	pool := dbtest.Setup(t)
	queries := New(pool)
	ctx := context.Background()
	recordingID := uuid.New()

	targetID, _, err := queries.CreateAcquisitionTarget(ctx, CreateAcquisitionTargetParams{
		Origin: "manual", EntryTitle: "A Title",
		MusicBrainzRecordingID: uuid.NullUUID{UUID: recordingID, Valid: true},
		Summary:                "wanted",
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := queries.StopPursuingAcquisitionTarget(ctx, targetID, "not wanted"); err != nil {
		t.Fatal(err)
	}

	_, _, err = queries.CreateUpgradeTarget(ctx, CreateUpgradeTargetParams{
		LibraryFileID: minimalLibraryFile(t, pool), RecordingID: recordingID,
		EntryArtist: "An Artist", EntryTitle: "A Title", Summary: "below the floor",
	})
	if err != ErrRecordingNotWanted {
		t.Fatalf("err = %v, want ErrRecordingNotWanted", err)
	}
}

// The LOW-severity fix: a sweep-resolved want must not be mistaken for a
// person's own decision. resolution_method is read by undo/restore to say
// whose decision is being taken back, and 'manual' there means a person typed
// or accepted a recording — never true of a want the sweep raised.
func TestCreateUpgradeTargetRecordsItsOwnResolutionMethod(t *testing.T) {
	pool := dbtest.Setup(t)
	queries := New(pool)
	ctx := context.Background()

	id, _, err := queries.CreateUpgradeTarget(ctx, CreateUpgradeTargetParams{
		LibraryFileID: minimalLibraryFile(t, pool), RecordingID: uuid.New(),
		EntryArtist: "An Artist", EntryTitle: "A Title", Summary: "below the floor",
	})
	if err != nil {
		t.Fatal(err)
	}
	target, err := queries.AcquisitionTarget(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	if !target.ResolutionMethod.Valid || target.ResolutionMethod.String != "upgrade" {
		t.Fatalf(`resolution_method = %v, want "upgrade"`, target.ResolutionMethod)
	}
}

// candidateFile lays down a settled, below-floor file the way UpgradeCandidates
// expects to find one: a resolved status, a stated bit rate, and a manual
// identity naming the given recording.
func candidateFile(t *testing.T, pool *pgxpool.Pool, recordingID uuid.UUID) uuid.UUID {
	t.Helper()
	ctx := context.Background()
	var rootID, fileID uuid.UUID
	if err := pool.QueryRow(ctx,
		`INSERT INTO library_roots (path) VALUES ($1) RETURNING id`, t.TempDir(),
	).Scan(&rootID); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `
		INSERT INTO library_files (
			library_root_id, path, size_bytes, modified_at, bit_rate_kbps, resolution_status
		)
		VALUES ($1, $2, 14, now(), 128, 'resolved')
		RETURNING id
	`, rootID, t.TempDir()+"/track.mp3").Scan(&fileID); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO library_file_identities (
			library_file_id, kind, musicbrainz_recording_id, method, is_manual, summary
		) VALUES ($1, 'external', $2, 'manual', true, 'a decision you made')
	`, fileID, recordingID); err != nil {
		t.Fatal(err)
	}
	return fileID
}

func candidateFileIDs(rows []UpgradeCandidateRow) []uuid.UUID {
	ids := make([]uuid.UUID, len(rows))
	for index, row := range rows {
		ids[index] = row.FileID
	}
	return ids
}

func containsID(ids []uuid.UUID, id uuid.UUID) bool {
	for _, candidate := range ids {
		if candidate == id {
			return true
		}
	}
	return false
}

// The HIGH-severity fix: once a fetched copy for a recording turns out no
// better, that file stands back from being offered as a candidate again for a
// while — otherwise the same recording is searched again on every single
// pass.
func TestUpgradeCandidatesExcludesAFileRefusedWithinTheStandBackWindow(t *testing.T) {
	pool := dbtest.Setup(t)
	queries := New(pool)
	ctx := context.Background()
	recordingID := uuid.New()

	fileID := candidateFile(t, pool, recordingID)
	if _, err := pool.Exec(ctx,
		`UPDATE library_files SET upgrade_refused_at = now() WHERE id = $1`, fileID,
	); err != nil {
		t.Fatal(err)
	}

	rows, err := queries.UpgradeCandidates(ctx, uuid.Nil, 100)
	if err != nil {
		t.Fatal(err)
	}
	if containsID(candidateFileIDs(rows), fileID) {
		t.Fatal("a file refused moments ago was offered as a candidate again")
	}
}

// And the other half of the same guarantee: the stand-back expires, because a
// recording with no better copy today may have one next month.
func TestUpgradeCandidatesIncludesAFileRefusedBeforeTheStandBackWindow(t *testing.T) {
	pool := dbtest.Setup(t)
	queries := New(pool)
	ctx := context.Background()
	recordingID := uuid.New()

	fileID := candidateFile(t, pool, recordingID)
	if _, err := pool.Exec(ctx, `
		UPDATE library_files SET upgrade_refused_at = now() - interval '31 days'
		WHERE id = $1
	`, fileID); err != nil {
		t.Fatal(err)
	}

	rows, err := queries.UpgradeCandidates(ctx, uuid.Nil, 100)
	if err != nil {
		t.Fatal(err)
	}
	if !containsID(candidateFileIDs(rows), fileID) {
		t.Fatal("a file refused over a month ago was still standing back")
	}
}

// The regression this closes: superseding a want to pursue a different file
// of the same recording must not unblock the file that want used to name.
// f0's own want gets superseded in favour of a want raised for f1 — a
// different file of the same recording — and f0 must stay excluded for as
// long as that replacement want is still in flight, because the search for
// the recording never really stopped.
func TestUpgradeCandidatesExcludesAFileWhoseRecordingIsPursuedThroughAnotherFile(t *testing.T) {
	pool := dbtest.Setup(t)
	queries := New(pool)
	ctx := context.Background()
	recordingID := uuid.New()

	f0 := candidateFile(t, pool, recordingID)
	f1 := candidateFile(t, pool, recordingID)

	acquiredID, _, err := queries.CreateAcquisitionTarget(ctx, CreateAcquisitionTargetParams{
		Origin: "manual", EntryTitle: "A Title",
		MusicBrainzRecordingID: uuid.NullUUID{UUID: recordingID, Valid: true},
		Summary:                "wanted",
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := queries.SettleAcquiredTarget(ctx, acquiredID, f0, "acquired", "acquired"); err != nil {
		t.Fatal(err)
	}

	// Raising an upgrade want for f1 supersedes the want that used to name f0.
	if _, _, err := queries.CreateUpgradeTarget(ctx, CreateUpgradeTargetParams{
		LibraryFileID: f1, RecordingID: recordingID,
		EntryArtist: "An Artist", EntryTitle: "A Title", Summary: "below the floor",
	}); err != nil {
		t.Fatal(err)
	}

	rows, err := queries.UpgradeCandidates(ctx, uuid.Nil, 100)
	if err != nil {
		t.Fatal(err)
	}
	ids := candidateFileIDs(rows)
	if containsID(ids, f0) {
		t.Fatal("f0 was offered as a candidate while its recording is still being pursued through f1")
	}
	if containsID(ids, f1) {
		t.Fatal("f1 was offered as a candidate while its own want for it is in flight")
	}
}

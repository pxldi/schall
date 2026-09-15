package db

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/pxldi/schall/internal/dbtest"
)

// The library can speak for a copy, but only through the files it has already
// proven. These tests are about which files those are, and about the debt an
// identity admitted that way records.

// ownedFile puts one file in the library with the identity described, and
// returns it.
func ownedFile(
	ctx context.Context, t *testing.T, pool *pgxpool.Pool,
	recordingID uuid.UUID, method string, manual bool, fingerprint string,
) uuid.UUID {
	t.Helper()
	fileID := uuid.New()
	if _, err := pool.Exec(ctx, `
		INSERT INTO library_files (
		    id, path, size_bytes, modified_at, title_tag, fingerprint, resolution_status
		) VALUES ($1, $2, 1024, now(), 'Salsed Punk', nullif($3, ''), 'resolved')
	`, fileID, "/music/"+fileID.String()+".flac", fingerprint); err != nil {
		t.Fatal(err)
	}
	kind, recording := "external", any(recordingID)
	if method == "local-only" {
		kind, recording, method = "local_only", nil, "manual"
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO library_file_identities (
		    library_file_id, kind, musicbrainz_recording_id, method, confidence,
		    is_manual, summary
		) VALUES ($1, $2, $3, $4, 1, $5, 'a summary')
	`, fileID, kind, recording, method, manual); err != nil {
		t.Fatal(err)
	}
	return fileID
}

// Only a file something proved may speak. The list is enumerated rather than
// "anything resolved": three tags agreeing identified a file well enough to file
// it in the library, and not well enough to admit a stranger's copy on.
func TestOnlyASettledFileSpeaksForARecording(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	pool := dbtest.Setup(t)
	queries := New(pool)
	recordingID := uuid.New()
	const print = "AQAAS47CSxLaY_vB5yK2H-0yC1fs4zmuI0wd"

	byHand := ownedFile(ctx, t, pool, recordingID, "manual", true, print)
	byAudio := ownedFile(ctx, t, pool, recordingID, "fingerprint", false, print)
	byIdentifier := ownedFile(ctx, t, pool, recordingID, "recording-id", false, print)
	byTags := ownedFile(ctx, t, pool, recordingID, "title-artist-duration", false, print)
	bySample := ownedFile(ctx, t, pool, recordingID, "published-sample", false, print)
	unprinted := ownedFile(ctx, t, pool, recordingID, "fingerprint", false, "")
	elsewhere := ownedFile(ctx, t, pool, uuid.New(), "fingerprint", false, print)

	witnesses, err := queries.SettledWitnesses(ctx, recordingID, 10)
	if err != nil {
		t.Fatalf("SettledWitnesses() error = %v", err)
	}
	offered := map[uuid.UUID]string{}
	for _, witness := range witnesses {
		offered[witness.FileID] = witness.Proof
	}
	for name, fileID := range map[string]uuid.UUID{
		"the file somebody decided by hand": byHand,
		"the file its audio proved":         byAudio,
	} {
		if _, found := offered[fileID]; !found {
			t.Errorf("%s was not offered as a witness", name)
		}
	}
	for name, fileID := range map[string]uuid.UUID{
		"a file three tags agreed about":         byTags,
		"a file identified by its recording ID":  byIdentifier,
		"a file the distributor's sample proved": bySample,
		"a file with no fingerprint kept":        unprinted,
		"a file proven to be other music":        elsewhere,
	} {
		if _, found := offered[fileID]; found {
			t.Errorf("%s was offered as a witness, want it left out", name)
		}
	}
	if offered[byHand] != "manual" {
		t.Errorf("proof = %q, want a person's decision named as such", offered[byHand])
	}
}

// A manual decision remains a witness even when AcoustID never produced a prefix.
func TestASettledFileWithoutAnAcoustIDFingerprintGetsAFullLengthWitnessJob(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	pool := dbtest.Setup(t)
	queries := New(pool)
	recordingID := uuid.New()
	fileID := ownedFile(ctx, t, pool, recordingID, "manual", true, "")

	queued, err := queries.QueueLibraryWitnessFingerprintJobs(ctx, 10)
	if err != nil {
		t.Fatalf("QueueLibraryWitnessFingerprintJobs() error = %v", err)
	}
	if queued != 1 {
		t.Fatalf("witness jobs queued = %d, want 1", queued)
	}
	var queuedFileID uuid.UUID
	if err := pool.QueryRow(ctx, `SELECT (payload->>$q$fileId$q$)::uuid
		FROM jobs WHERE kind = $q$fingerprint_library_witness$q$`).Scan(&queuedFileID); err != nil {
		t.Fatal(err)
	}
	if queuedFileID != fileID {
		t.Fatalf("queued file = %s, want %s", queuedFileID, fileID)
	}

	const fullFingerprint = "7:AQAAS47CSxLaY_vB5yK2H-0yC1fs4zmuI0wd"
	if _, err := pool.Exec(ctx, `
		UPDATE library_files
		SET witness_fingerprint = $q$7:AQAAS47CSxLaY_vB5yK2H-0yC1fs4zmuI0wd$q$,
		    witness_fingerprint_seconds = 7
		WHERE id = $1
	`, fileID); err != nil {
		t.Fatal(err)
	}
	witnesses, err := queries.SettledWitnesses(ctx, recordingID, 10)
	if err != nil {
		t.Fatalf("SettledWitnesses() error = %v", err)
	}
	if len(witnesses) != 1 {
		t.Fatalf("witnesses = %+v, want the manually settled file", witnesses)
	}
	if witnesses[0].FileID != fileID || witnesses[0].Fingerprint != fullFingerprint {
		t.Fatalf("witness = %+v, want file %s with %q", witnesses[0], fileID, fullFingerprint)
	}
}

// A file that is not there cannot be compared with anything, and a decision that
// no external identity exists names no recording to speak for.
func TestAFileThatIsGoneOrNamesNothingSpeaksForNobody(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	pool := dbtest.Setup(t)
	queries := New(pool)
	recordingID := uuid.New()
	const print = "AQAAS47CSxLaY_vB5yK2H-0yC1fs4zmuI0wd"

	missing := ownedFile(ctx, t, pool, recordingID, "fingerprint", false, print)
	if _, err := pool.Exec(ctx, `
		UPDATE library_files SET missing_at = now() WHERE id = $1
	`, missing); err != nil {
		t.Fatal(err)
	}
	ownedFile(ctx, t, pool, recordingID, "local-only", true, print)

	witnesses, err := queries.SettledWitnesses(ctx, recordingID, 10)
	if err != nil {
		t.Fatalf("SettledWitnesses() error = %v", err)
	}
	if len(witnesses) != 0 {
		t.Fatalf("witnesses = %+v, want none", witnesses)
	}
}

// An identity the library proved records which file proved it and what settled
// that file. Without the debt written down, taking the older decision back would
// leave this file wearing a recording nothing stands behind.
func TestAnIdentityAdmittedByTheLibraryRecordsWhatAdmittedIt(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	pool := dbtest.Setup(t)
	queries, targetID, requestID := fetchedForAWant(ctx, t, pool)
	ancestor := uuid.New()
	if _, err := pool.Exec(ctx, `
		INSERT INTO library_files (id, path, size_bytes, modified_at)
		VALUES ($1, '/music/Anetha/owned.flac', 1024, now())
	`, ancestor); err != nil {
		t.Fatal(err)
	}
	agrees := true
	fileID := scannedAcceptedCopy(ctx, t, pool, queries, targetID, requestID,
		&AcquiredFileEvidence{
			Name: "03.flac", Method: "library-witness", Confidence: 1,
			Wanted: &ImportTags{Artist: "Anetha", Title: "Salsed Punk"},
			Witness: &ImportWitness{
				FileID: ancestor.String(), Path: "/music/Anetha/owned.flac",
				Proof: "manual", Rate: 0.03, Measured: true, Agrees: &agrees,
			},
			Agrees:  []string{"audio (the same music as a file you already own)"},
			Differs: []string{}, Problems: []string{},
		})

	if _, err := queries.CompleteAcquiredFile(ctx, targetID,
		"A copy was imported.", "Proven by a file you own.",
	); err != nil {
		t.Fatalf("CompleteAcquiredFile() error = %v", err)
	}

	var (
		method     string
		admittedBy uuid.NullUUID
		proof      *string
	)
	if err := pool.QueryRow(ctx, `
		SELECT method, admitted_by_file_id, admitted_by_proof
		FROM library_file_identities WHERE library_file_id = $1
	`, fileID).Scan(&method, &admittedBy, &proof); err != nil {
		t.Fatalf("read the identity written for the copy: %v", err)
	}
	if method != "library-witness" {
		t.Fatalf("method = %q, want the library recorded as what proved it", method)
	}
	if !admittedBy.Valid || admittedBy.UUID != ancestor {
		t.Fatalf("admitted by = %v, want the owned file %s", admittedBy, ancestor)
	}
	if proof == nil || *proof != "manual" {
		t.Fatalf("proof = %v, want what settled the owned file", proof)
	}
}

// A copy AcoustID proved owes the library nothing, even where a library file
// agreed as well. Recording a debt it does not have would make it fall when that
// file's decision is taken back, and its own proof would still be standing.
func TestACopyProvenByItsOwnAudioRecordsNoDebt(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	pool := dbtest.Setup(t)
	queries, targetID, requestID := fetchedForAWant(ctx, t, pool)
	agrees := true
	fileID := scannedAcceptedCopy(ctx, t, pool, queries, targetID, requestID,
		&AcquiredFileEvidence{
			Name: "03.flac", Method: "fingerprint", Confidence: 1,
			Wanted: &ImportTags{Artist: "Anetha", Title: "Salsed Punk"},
			Witness: &ImportWitness{
				FileID: uuid.New().String(), Path: "/music/Anetha/owned.flac",
				Proof: "manual", Rate: 0.03, Measured: true, Agrees: &agrees,
			},
			Agrees: []string{"audio"}, Differs: []string{}, Problems: []string{},
		})

	if _, err := queries.CompleteAcquiredFile(ctx, targetID,
		"A copy was imported.", "Proven by its audio.",
	); err != nil {
		t.Fatalf("CompleteAcquiredFile() error = %v", err)
	}

	var admittedBy uuid.NullUUID
	if err := pool.QueryRow(ctx, `
		SELECT admitted_by_file_id FROM library_file_identities WHERE library_file_id = $1
	`, fileID).Scan(&admittedBy); err != nil {
		t.Fatal(err)
	}
	if admittedBy.Valid {
		t.Fatalf("admitted by = %v, want no debt recorded", admittedBy)
	}
}

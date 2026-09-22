package acquisition

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/pxldi/schall/internal/db"
	"github.com/pxldi/schall/internal/dbtest"
	"github.com/pxldi/schall/internal/identity"
	"github.com/pxldi/schall/internal/musicbrainz"
	"github.com/pxldi/schall/internal/tagmatch"
	"github.com/rs/zerolog"
)

// catalogueProvider answers about the recordings a test wrote into MusicBrainz.
type catalogueProvider struct {
	recordings map[uuid.UUID]musicbrainz.Recording
}

func (provider *catalogueProvider) Recording(
	_ context.Context, id uuid.UUID,
) (musicbrainz.Recording, error) {
	found, ok := provider.recordings[id]
	if !ok {
		return musicbrainz.Recording{}, musicbrainz.ErrNotFound
	}
	return found, nil
}

func (provider *catalogueProvider) ResolveRecordingID(
	_ context.Context, id uuid.UUID,
) (uuid.UUID, error) {
	if _, ok := provider.recordings[id]; !ok {
		return uuid.Nil, musicbrainz.ErrNotFound
	}
	return id, nil
}

func (provider *catalogueProvider) SearchRecordings(
	context.Context, musicbrainz.RecordingQuery, int,
) ([]musicbrainz.Recording, error) {
	return nil, nil
}

// twoRowsOf is the case ADR 0025 is about: one performance entered
// twice under one registration code, differing only in which release each row
// hangs off. Neither row carries an artist list, so the code is the only thing
// that can speak about them: 0031's test compares the set of artists a row
// names, and these name none.
func twoRowsOf(wanted, filed uuid.UUID, isrc string) *catalogueProvider {
	return &catalogueProvider{recordings: map[uuid.UUID]musicbrainz.Recording{
		wanted: recording(wanted, "Glory Box", "Portishead", 301000, isrc),
		filed:  recording(filed, "Glory Box", "Portishead", 301000, isrc),
	}}
}

// credited gives a row the artist list MusicBrainz sends beside the printed
// credit. The name-and-length test compares the set of artists a row names, and
// a row that arrived without the list names none.
//
// The identifier is derived from the name because MusicBrainz holds one artist
// under one identifier: two rows crediting the same artist have to carry the
// same one.
func credited(base musicbrainz.Recording, artists ...string) musicbrainz.Recording {
	for _, name := range artists {
		base.Credits = append(base.Credits, musicbrainz.Credit{
			ArtistID: uuid.NewSHA1(uuid.NameSpaceOID, []byte(tagmatch.Text(name))),
			Name:     name, ArtistName: name,
		})
	}
	return base
}

// twoRowsOfOneName is the case ADR 0031 is about: one performance
// entered twice with a registration code on one of the rows only, which is how
// MusicBrainz holds about half of these pairs. Both rows of "Robber" by Playboi
// Carti run 163 seconds.
func twoRowsOfOneName(wanted, filed uuid.UUID) *catalogueProvider {
	return &catalogueProvider{recordings: map[uuid.UUID]musicbrainz.Recording{
		wanted: credited(
			recording(wanted, "Robber", "Playboi Carti", 163000), "Playboi Carti"),
		filed: credited(
			recording(filed, "Robber", "Playboi Carti", 163000, "QZES71982801"),
			"Playboi Carti"),
	}}
}

// mergedRowsOf is the pair after MusicBrainz merged them: a lookup on either
// identifier arrives at the surviving row, because the provider answers a
// merged-away identifier with the recording it was merged into. The rows print
// nothing in common — no artist list on either — so nothing but the redirect can
// speak about them.
func mergedRowsOf(wanted, filed uuid.UUID) *catalogueProvider {
	surviving := recording(filed, "Glory Box", "Portishead", 301000)
	return &catalogueProvider{recordings: map[uuid.UUID]musicbrainz.Recording{
		wanted: surviving, filed: surviving,
	}}
}

// provenBy rewrites what admitted the want's copy, which acceptedCopy seeds as
// the audio having named the recording.
func provenBy(
	ctx context.Context, t *testing.T, pool *pgxpool.Pool, targetID uuid.UUID, method string,
) {
	t.Helper()
	if _, err := pool.Exec(ctx, `
		UPDATE acquisition_target_files
		SET evidence = jsonb_set(evidence, '{method}', to_jsonb($2::text))
		WHERE acquisition_target_id = $1
	`, targetID, method); err != nil {
		t.Fatalf("seed what admitted the copy for want %s: %v", targetID, err)
	}
}

// filingService is the acquisition service with the two things this path needs:
// the resolver that reads two MusicBrainz rows, and the identity service that
// writes what the library says a file is.
//
// The resolver is given somewhere to remember merges, as cmd/schall gives it
// one. A resolver without one reads a redirect and forgets it, so every later
// comparison would ask MusicBrainz the same question again.
func filingService(pool *pgxpool.Pool, provider *catalogueProvider) *Service {
	return newTestService(pool).
		WithResolver(identity.NewResolver(provider).WithAliases(db.New(pool))).
		WithFiler(identity.NewService(pool, provider, zerolog.Nop()))
}

// anchorOf writes what the copy came to beside the excerpt the want's own
// distributor published, exactly as the judge records it.
func anchorOf(
	ctx context.Context, t *testing.T, pool *pgxpool.Pool, targetID uuid.UUID, rate float64,
) {
	t.Helper()
	measured := fmt.Sprintf(`{"rate":%v,"measured":true,"agrees":true,"source":"deezer"}`, rate)
	if _, err := pool.Exec(ctx, `
		UPDATE acquisition_target_files
		SET evidence = jsonb_set(evidence, '{anchor}', $2::jsonb)
		WHERE acquisition_target_id = $1
	`, targetID, measured); err != nil {
		t.Fatalf("seed the anchor of the copy for want %s: %v", targetID, err)
	}
	// The excerpt was fetched for the recording this want names, which is what
	// makes the number above evidence about that recording.
	if _, err := pool.Exec(ctx, `
		UPDATE acquisition_targets
		SET anchor_fingerprint = 'AQAB', anchor_source = 'deezer',
		    anchor_fetched_at = now()
		WHERE id = $1
	`, targetID); err != nil {
		t.Fatalf("seed the anchor of want %s: %v", targetID, err)
	}
}

// filedAs is the library having decided the imported file is some recording.
func filedAs(
	ctx context.Context, t *testing.T, pool *pgxpool.Pool,
	fileID, recordingID uuid.UUID, manual bool,
) {
	t.Helper()
	if _, err := pool.Exec(ctx, `
		INSERT INTO library_file_identities (
			library_file_id, kind, musicbrainz_recording_id, artist_name, track_title,
			method, confidence, is_manual, summary
		)
		VALUES ($1, 'external', $2, 'Portishead', 'Glory Box', $3, 1, $4, 'the tags said so')
	`, fileID, recordingID, map[bool]string{true: "manual", false: "title-artist-duration"}[manual],
		manual); err != nil {
		t.Fatalf("seed the identity of file %s: %v", fileID, err)
	}
}

// alreadyCompleted is the world after a copy has been accounted for: the offer
// carries the library file it became, so completion has nothing left to do and
// every later pass reads the file the want holds rather than writing one.
func alreadyCompleted(
	ctx context.Context, t *testing.T, pool *pgxpool.Pool, targetID, fileID uuid.UUID,
) {
	t.Helper()
	if _, err := pool.Exec(ctx, `
		UPDATE acquisition_target_files SET library_file_id = $2
		WHERE acquisition_target_id = $1
	`, targetID, fileID); err != nil {
		t.Fatalf("link the copy of want %s to file %s: %v", targetID, fileID, err)
	}
}

func targetRow(
	ctx context.Context, t *testing.T, service *Service, id uuid.UUID,
) db.AcquisitionTargetRow {
	t.Helper()
	target, err := service.Target(ctx, id)
	if err != nil {
		t.Fatalf("read want %s: %v", id, err)
	}
	return target
}

func identityOfFile(
	ctx context.Context, t *testing.T, pool *pgxpool.Pool, fileID uuid.UUID,
) (uuid.UUID, string, bool) {
	t.Helper()
	var recordingID uuid.UUID
	var method string
	var manual bool
	if err := pool.QueryRow(ctx, `
		SELECT musicbrainz_recording_id, method, is_manual
		FROM library_file_identities WHERE library_file_id = $1
	`, fileID).Scan(&recordingID, &method, &manual); err != nil {
		t.Fatalf("read the identity of file %s: %v", fileID, err)
	}
	return recordingID, method, manual
}

// evidenceOfFile is what the library wrote down as the reason a file carries
// the identity it does.
func evidenceOfFile(
	ctx context.Context, t *testing.T, pool *pgxpool.Pool, fileID uuid.UUID,
) []string {
	t.Helper()
	var evidence []string
	if err := pool.QueryRow(ctx, `
		SELECT evidence FROM library_file_identities WHERE library_file_id = $1
	`, fileID).Scan(&evidence); err != nil {
		t.Fatalf("read the evidence on file %s: %v", fileID, err)
	}
	return evidence
}

// The re-filed identity says what actually re-filed it. It used to say the copy
// had matched this recording's published sample and that the two rows shared an
// ISRC, whatever had happened: on this branch the wanted row carries no code at
// all, so a person reading the row back was reading evidence nobody ever had.
func TestARefiledIdentityRecordsWhatProvedTheCopy(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	pool := dbtest.Setup(t)

	wanted, filed := uuid.New(), uuid.New()
	service := filingService(pool, twoRowsOfOneName(wanted, filed))
	target := createWant(ctx, t, service, wanted)
	fileID := acceptedCopy(ctx, t, pool, target.ID, wanted)
	provenBy(ctx, t, pool, target.ID, "library-witness")
	filedAs(ctx, t, pool, fileID, filed, false)

	if err := service.Sweep(ctx); err != nil {
		t.Fatalf("Sweep() error = %v", err)
	}

	evidence := evidenceOfFile(ctx, t, pool, fileID)
	joined := strings.Join(evidence, " | ")
	if !strings.Contains(joined, "a file you already own") {
		t.Errorf("evidence = %v, want the witness that proved the copy named", evidence)
	}
	for _, invented := range []string{"ISRC", "published sample"} {
		if strings.Contains(joined, invented) {
			t.Errorf("evidence = %v, want no claim about %s on a pair that has none",
				evidence, invented)
		}
	}
}

// MusicBrainz enters one performance as two recordings and does not merge them.
// The want is keyed to one row, the copy's audio proved it against the excerpt
// its distributor publishes, and the library then filed the imported file under
// the other row from the peer's tags. Both rows carry the same registration
// code, so the file is the music that was asked for and the want is finished
// against it (ADR 0025).
func TestAWantIsSettledByACopyFiledUnderTheSameRegistration(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	pool := dbtest.Setup(t)

	wanted, filed := uuid.New(), uuid.New()
	service := filingService(pool, twoRowsOf(wanted, filed, "GBAAA9400001"))
	target := createWant(ctx, t, service, wanted)
	fileID := acceptedCopy(ctx, t, pool, target.ID, wanted)
	anchorOf(ctx, t, pool, target.ID, 0.07)
	filedAs(ctx, t, pool, fileID, filed, false)

	if err := service.Sweep(ctx); err != nil {
		t.Fatalf("Sweep() error = %v", err)
	}

	settled := targetRow(ctx, t, service, target.ID)
	if settled.Status != "acquired" {
		t.Fatalf("status = %s (%q), want acquired", settled.Status, settled.Summary)
	}
	stored, method, manual := identityOfFile(ctx, t, pool, fileID)
	if stored != wanted {
		t.Errorf("filed under %s, want %s", stored, wanted)
	}
	if method != identity.MethodSameRegistration {
		t.Errorf("method = %q, want %q", method, identity.MethodSameRegistration)
	}
	if manual {
		t.Error("the re-filed identity is manual, want a decision nobody made by hand")
	}
}

// The wanted row carries no registration code, which is how MusicBrainz holds
// about half of these pairs, so nothing about a code can speak. The two rows
// name one artist, hold one title and run one length, and the file on the disc
// was proven and imported before the library filed it under the second row. It
// is the music the want asked for (ADR 0031), and no excerpt is
// asked for on this branch.
func TestAWantIsSettledByACopyFiledUnderARowOfOneNameAndOneLength(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	pool := dbtest.Setup(t)

	wanted, filed := uuid.New(), uuid.New()
	service := filingService(pool, twoRowsOfOneName(wanted, filed))
	target := createWant(ctx, t, service, wanted)
	fileID := acceptedCopy(ctx, t, pool, target.ID, wanted)
	filedAs(ctx, t, pool, fileID, filed, false)

	if err := service.Sweep(ctx); err != nil {
		t.Fatalf("Sweep() error = %v", err)
	}

	settled := targetRow(ctx, t, service, target.ID)
	if settled.Status != "acquired" {
		t.Fatalf("status = %s (%q), want acquired", settled.Status, settled.Summary)
	}
	stored, method, _ := identityOfFile(ctx, t, pool, fileID)
	if stored != wanted {
		t.Errorf("filed under %s, want %s", stored, wanted)
	}
	if method != identity.MethodSameRegistration {
		t.Errorf("method = %q, want %q", method, identity.MethodSameRegistration)
	}
}

// The same two rows with nothing having proved the copy. The library file was
// admitted on its own tags, which is what a stranger writes into a file, so
// nothing says the audio on the disc is this want's music. Two rows of one name
// and one length are the catalogue's bookkeeping and cannot supply that, so the
// want stops for a person and the file keeps the recording the library gave it.
func TestAWantStopsWhenNothingProvedTheCopyUnderItsOwnRow(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	pool := dbtest.Setup(t)

	wanted, filed := uuid.New(), uuid.New()
	service := filingService(pool, twoRowsOfOneName(wanted, filed))
	target := createWant(ctx, t, service, wanted)
	fileID := acceptedCopy(ctx, t, pool, target.ID, wanted)
	provenBy(ctx, t, pool, target.ID, "title-artist-duration")
	filedAs(ctx, t, pool, fileID, filed, false)

	if err := service.Sweep(ctx); err != nil {
		t.Fatalf("Sweep() error = %v", err)
	}

	stopped := targetRow(ctx, t, service, target.ID)
	if stopped.Status != "pending" || stopped.NextAttemptAt.Valid {
		t.Fatalf("status = %s next attempt %v, want the want stopped for a person",
			stopped.Status, stopped.NextAttemptAt.Time)
	}
	stored, _, _ := identityOfFile(ctx, t, pool, fileID)
	if stored != filed {
		t.Errorf("filed under %s, want the library's own answer %s left alone", stored, filed)
	}
}

// MusicBrainz merged the two rows. A lookup on the want's identifier arrives at
// the row the library filed the copy under, which is the catalogue's own
// statement that they name one recording, so the want settles whatever else is
// known about the copy — here, only its tags.
func TestAWantIsSettledByRowsMusicBrainzMerged(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	pool := dbtest.Setup(t)

	wanted, filed := uuid.New(), uuid.New()
	service := filingService(pool, mergedRowsOf(wanted, filed))
	target := createWant(ctx, t, service, wanted)
	fileID := acceptedCopy(ctx, t, pool, target.ID, wanted)
	provenBy(ctx, t, pool, target.ID, "title-artist-duration")
	filedAs(ctx, t, pool, fileID, filed, false)

	if err := service.Sweep(ctx); err != nil {
		t.Fatalf("Sweep() error = %v", err)
	}

	settled := targetRow(ctx, t, service, target.ID)
	if settled.Status != "acquired" {
		t.Fatalf("status = %s (%q), want acquired", settled.Status, settled.Summary)
	}
	var surviving uuid.UUID
	if err := pool.QueryRow(ctx, `
		SELECT surviving_id FROM recording_aliases WHERE retired_id = $1
	`, wanted).Scan(&surviving); err != nil {
		t.Fatalf("read the merge learned about %s: %v", wanted, err)
	}
	if surviving != filed {
		t.Errorf("merge recorded as %s, want %s", surviving, filed)
	}
}

// A person's answer is permanent. Where the row they chose and the row the want
// names are one registration, their file is still the music that was asked for,
// so the want finishes against it and their decision is left exactly as it is.
func TestAManualFilingUnderTheSameRegistrationSettlesTheWantUntouched(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	pool := dbtest.Setup(t)

	wanted, filed := uuid.New(), uuid.New()
	service := filingService(pool, twoRowsOf(wanted, filed, "GBAAA9400001"))
	target := createWant(ctx, t, service, wanted)
	fileID := acceptedCopy(ctx, t, pool, target.ID, wanted)
	anchorOf(ctx, t, pool, target.ID, 0.07)
	filedAs(ctx, t, pool, fileID, filed, true)

	if err := service.Sweep(ctx); err != nil {
		t.Fatalf("Sweep() error = %v", err)
	}

	settled := targetRow(ctx, t, service, target.ID)
	if settled.Status != "acquired" {
		t.Fatalf("status = %s (%q), want acquired", settled.Status, settled.Summary)
	}
	stored, _, manual := identityOfFile(ctx, t, pool, fileID)
	if stored != filed || !manual {
		t.Errorf("identity = %s manual=%v, want the person's answer %s left alone",
			stored, manual, filed)
	}
}

// Two recordings that share their audio and no registration code are two pieces
// of music, and nothing here may choose between them. The want stops, and says
// which two they are so somebody can.
func TestAWantStopsWhenTheFiledRecordingSharesNoRegistration(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	pool := dbtest.Setup(t)

	wanted, filed := uuid.New(), uuid.New()
	provider := &catalogueProvider{recordings: map[uuid.UUID]musicbrainz.Recording{
		wanted: recording(wanted, "Glory Box", "Portishead", 301000, "GBAAA9400001"),
		filed:  recording(filed, "Glory Box (Live)", "Portishead", 305000, "GBAAA9400002"),
	}}
	service := filingService(pool, provider)
	target := createWant(ctx, t, service, wanted)
	fileID := acceptedCopy(ctx, t, pool, target.ID, wanted)
	anchorOf(ctx, t, pool, target.ID, 0.07)
	filedAs(ctx, t, pool, fileID, filed, false)

	if err := service.Sweep(ctx); err != nil {
		t.Fatalf("Sweep() error = %v", err)
	}

	stopped := targetRow(ctx, t, service, target.ID)
	if stopped.Status != "pending" || stopped.NextAttemptAt.Valid {
		t.Fatalf("status = %s next = %v, want a pending want with no next look",
			stopped.Status, stopped.NextAttemptAt)
	}
	for _, want := range []string{"Glory Box", "cannot tell the two apart"} {
		if !strings.Contains(stopped.Summary, want) {
			t.Errorf("summary = %q, want it to carry %q", stopped.Summary, want)
		}
	}
	stored, _, _ := identityOfFile(ctx, t, pool, fileID)
	if stored != filed {
		t.Errorf("identity = %s, want the library's own answer %s untouched", stored, filed)
	}
}

// The registration code alone never settles it. MusicBrainz puts one ISRC on
// genuinely different audio too, so without the excerpt saying this copy is the
// wanted music there is nothing here that could establish one registration, and
// the want waits for a person (ADR 0025).
func TestASharedRegistrationCodeAloneDoesNotSettleTheWant(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	pool := dbtest.Setup(t)

	wanted, filed := uuid.New(), uuid.New()
	service := filingService(pool, twoRowsOf(wanted, filed, "GBAAA9400001"))
	target := createWant(ctx, t, service, wanted)
	fileID := acceptedCopy(ctx, t, pool, target.ID, wanted)
	filedAs(ctx, t, pool, fileID, filed, false)

	if err := service.Sweep(ctx); err != nil {
		t.Fatalf("Sweep() error = %v", err)
	}

	stopped := targetRow(ctx, t, service, target.ID)
	if stopped.Status != "pending" || stopped.NextAttemptAt.Valid {
		t.Fatalf("status = %s next = %v, want a pending want with no next look",
			stopped.Status, stopped.NextAttemptAt)
	}
	stored, _, _ := identityOfFile(ctx, t, pool, fileID)
	if stored != filed {
		t.Errorf("identity = %s, want the library's own answer %s untouched", stored, filed)
	}
}

// A file nobody has asked about yet says nothing at all. The want stands back
// and the library is asked, rather than the want stopping on silence and
// nothing ever putting the question.
func TestAWantIsNotStoppedWhileItsCopyHasNoIdentityYet(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	pool := dbtest.Setup(t)

	wanted := uuid.New()
	service := filingService(pool, &catalogueProvider{
		recordings: map[uuid.UUID]musicbrainz.Recording{
			wanted: recording(wanted, "Glory Box", "Portishead", 301000, "GBAAA9400001"),
		},
	})
	target := createWant(ctx, t, service, wanted)
	fileID := acceptedCopy(ctx, t, pool, target.ID, wanted)
	alreadyCompleted(ctx, t, pool, target.ID, fileID)

	if err := service.Sweep(ctx); err != nil {
		t.Fatalf("Sweep() error = %v", err)
	}

	waiting := targetRow(ctx, t, service, target.ID)
	if waiting.Status != "pending" || !waiting.NextAttemptAt.Valid {
		t.Fatalf("status = %s next = %v, want a pending want with another look coming",
			waiting.Status, waiting.NextAttemptAt)
	}
	var queued int
	if err := pool.QueryRow(ctx, `
		SELECT count(*) FROM jobs
		WHERE kind = 'resolve_library_file' AND payload->>'fileId' = $1::text
	`, fileID).Scan(&queued); err != nil {
		t.Fatal(err)
	}
	if queued != 1 {
		t.Errorf("resolution jobs = %d, want the library asked what the file is", queued)
	}
}

// The library finished with the file and wrote nothing down for it. The copy was
// admitted because its audio proved it, so what is missing is a row something
// deleted rather than an answer, and it is written back.
func TestAResolvedFileWithNoIdentityIsFiledUnderTheWantsRecording(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	pool := dbtest.Setup(t)

	wanted := uuid.New()
	service := filingService(pool, &catalogueProvider{
		recordings: map[uuid.UUID]musicbrainz.Recording{
			wanted: recording(wanted, "Glory Box", "Portishead", 301000, "GBAAA9400001"),
		},
	})
	target := createWant(ctx, t, service, wanted)
	fileID := acceptedCopy(ctx, t, pool, target.ID, wanted)
	alreadyCompleted(ctx, t, pool, target.ID, fileID)
	if _, err := pool.Exec(ctx, `
		UPDATE library_files SET resolution_status = 'resolved' WHERE id = $1
	`, fileID); err != nil {
		t.Fatal(err)
	}

	if err := service.Sweep(ctx); err != nil {
		t.Fatalf("Sweep() error = %v", err)
	}

	settled := targetRow(ctx, t, service, target.ID)
	if settled.Status != "acquired" {
		t.Fatalf("status = %s (%q), want acquired", settled.Status, settled.Summary)
	}
	stored, _, _ := identityOfFile(ctx, t, pool, fileID)
	if stored != wanted {
		t.Errorf("filed under %s, want %s", stored, wanted)
	}
}

// The library finished with the file and wrote nothing down, and the copy's own
// row records no proof of this recording either: it was judged when the want
// named another recording. There is nothing to write back, so the want stops
// and asks instead of filing on silence.
func TestAResolvedFileWithNoIdentityIsNotFiledWithoutTheCopysProof(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	pool := dbtest.Setup(t)

	wanted := uuid.New()
	service := filingService(pool, &catalogueProvider{
		recordings: map[uuid.UUID]musicbrainz.Recording{
			wanted: recording(wanted, "Glory Box", "Portishead", 301000, "GBAAA9400001"),
		},
	})
	target := createWant(ctx, t, service, wanted)
	fileID := acceptedCopy(ctx, t, pool, target.ID, wanted)
	alreadyCompleted(ctx, t, pool, target.ID, fileID)
	if _, err := pool.Exec(ctx, `
		UPDATE library_files SET resolution_status = 'resolved' WHERE id = $1
	`, fileID); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `
		UPDATE acquisition_target_files
		SET evidence = jsonb_set(evidence, '{method}', '"title-artist-duration"')
		WHERE acquisition_target_id = $1
	`, target.ID); err != nil {
		t.Fatal(err)
	}

	if err := service.Sweep(ctx); err != nil {
		t.Fatalf("Sweep() error = %v", err)
	}

	stopped := targetRow(ctx, t, service, target.ID)
	if stopped.Status != "pending" || stopped.NextAttemptAt.Valid {
		t.Fatalf("status = %s next = %v, want a stopped want", stopped.Status, stopped.NextAttemptAt)
	}
	if stopped.Summary != unprovenFilingSummary {
		t.Errorf("summary = %q, want %q", stopped.Summary, unprovenFilingSummary)
	}
	var identities int
	if err := pool.QueryRow(ctx, `
		SELECT count(*) FROM library_file_identities WHERE library_file_id = $1
	`, fileID).Scan(&identities); err != nil {
		t.Fatal(err)
	}
	if identities != 0 {
		t.Errorf("identities = %d, want none written on a copy nothing proved", identities)
	}
}

// A want that stopped has no next attempt, so no sweep reaches it. The first
// sweep of a process reads them once, which is the only way one stopped under a
// rule that has since changed ever gets looked at again.
func TestTheFirstSweepLooksAgainAtAStoppedWant(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	pool := dbtest.Setup(t)

	wanted, filed := uuid.New(), uuid.New()
	service := filingService(pool, twoRowsOf(wanted, filed, "GBAAA9400001"))
	target := createWant(ctx, t, service, wanted)
	fileID := acceptedCopy(ctx, t, pool, target.ID, wanted)
	anchorOf(ctx, t, pool, target.ID, 0.07)
	filedAs(ctx, t, pool, fileID, filed, false)
	// Exactly the state the deployed instance left eight wants in: still wanted,
	// with no time to be looked at again and a copy on the disc.
	if _, err := pool.Exec(ctx, `
		UPDATE acquisition_targets SET next_attempt_at = NULL, summary = $2 WHERE id = $1
	`, target.ID, disagreedSummary); err != nil {
		t.Fatal(err)
	}
	alreadyCompleted(ctx, t, pool, target.ID, fileID)

	if err := service.Sweep(ctx); err != nil {
		t.Fatalf("Sweep() error = %v", err)
	}

	settled := targetRow(ctx, t, service, target.ID)
	if settled.Status != "acquired" {
		t.Fatalf("status = %s (%q), want acquired", settled.Status, settled.Summary)
	}
	stored, _, _ := identityOfFile(ctx, t, pool, fileID)
	if stored != wanted {
		t.Errorf("filed under %s, want %s", stored, wanted)
	}
}

// Somebody saying a want names the wrong recording sends it back to be resolved
// again, and leaves both the copies fetched for the old recording and the
// excerpt fetched by its ISRC where they are. Neither says anything about the
// recording the want names now, so neither may settle it.
func TestAnExcerptFetchedBeforeTheWantWasResolvedAgainSettlesNothing(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	pool := dbtest.Setup(t)

	wanted, filed := uuid.New(), uuid.New()
	service := filingService(pool, twoRowsOf(wanted, filed, "GBAAA9400001"))
	target := createWant(ctx, t, service, wanted)
	fileID := acceptedCopy(ctx, t, pool, target.ID, wanted)
	anchorOf(ctx, t, pool, target.ID, 0.07)
	filedAs(ctx, t, pool, fileID, filed, false)
	// The want was resolved again after that copy was judged and after that
	// excerpt was fetched.
	if _, err := pool.Exec(ctx, `
		UPDATE acquisition_targets SET resolved_at = now() + interval '1 hour' WHERE id = $1
	`, target.ID); err != nil {
		t.Fatal(err)
	}

	if err := service.Sweep(ctx); err != nil {
		t.Fatalf("Sweep() error = %v", err)
	}

	stopped := targetRow(ctx, t, service, target.ID)
	if stopped.Status != "pending" || stopped.NextAttemptAt.Valid {
		t.Fatalf("status = %s next = %v, want a pending want with no next look",
			stopped.Status, stopped.NextAttemptAt)
	}
	stored, _, _ := identityOfFile(ctx, t, pool, fileID)
	if stored != filed {
		t.Errorf("identity = %s, want the library's own answer %s untouched", stored, filed)
	}
}

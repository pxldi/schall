package db

import (
	"context"
	"fmt"

	"github.com/google/uuid"
)

// LibraryWitnessRow is one file the library already holds that can speak for a
// recording: where it is, the fingerprint kept for it, and what proved it.
type LibraryWitnessRow struct {
	FileID uuid.UUID
	// Path is where the file is on disk. It is carried so the question a person
	// is shown can name the file the copy sounds like, rather than an identifier.
	Path string
	// Fingerprint is the full-length witness fingerprint when the backfill has
	// written one. Before that it is the stored AcoustID prefix fingerprint and
	// CoverageSeconds is zero, which marks the evidence as partial.
	Fingerprint     string
	CoverageSeconds int
	// Proof is the method that settled this file's identity. It travels with the row
	// because a copy admitted by this file inherits it and records it.
	Proof string
	// DurationMS is how long this file itself runs, as it was last measured or
	// tagged. A fingerprint agreeing over its own opening two minutes proves
	// nothing about a copy many times its length — an hour-long mix can share a
	// two-minute prefix with the wanted recording — so a copy is only admitted
	// by this file when their lengths agree too. Zero means the length is not
	// known, which admits nothing.
	DurationMS int
}

// settledProofs is the whole list of proofs a library file may speak on the
// strength of. It is the enumerated set and it is deliberately short.
//
// Each of these is something that proved the file is one particular recording:
// a person's own decision, AcoustID naming it at its leading cluster, or audio
// compared against a trusted file.
// `library-witness` is here as well, which is a file admitted by an
// older file — the chain is safe because every link records which file admitted
// it, so withdrawing the decision at the head of a chain withdraws the whole
// chain.
//
// What is left out matters as much. 'title-artist-duration' is three tags
// agreeing and nothing more, and a file identified by its paperwork must not go
// on to admit a stranger's copy. 'published-sample' is left out too: it is a
// proof of the same standing as these, but the set a library file may speak on
// was enumerated deliberately and widening it is a decision of its own rather
// than a detail of this one. A local-only decision names no recording and can
// speak for none.
var settledProofs = []string{
	"manual", "fingerprint", "fingerprint-and-identifier", "library-witness",
}

// SettledWitnesses lists the files the library already holds that are proven to
// be this recording and can be compared against.
//
// It is the library asked to speak about a copy somebody is about to be asked a
// question about. Only settled identities are returned — see settledProofs — and
// only files that are still on disk and have a fingerprint kept, because a
// witness with nothing to compare is not a witness.
//
// A manual decision is offered first. Where several owned files could answer,
// the one a person settled is the one whose withdrawal a person would expect to
// carry the copy with it.
func (q *Queries) SettledWitnesses(
	ctx context.Context, recordingID uuid.UUID, limit int32,
) ([]LibraryWitnessRow, error) {
	rows, err := q.db.Query(ctx, `
		SELECT library_files.id, library_files.path,
		       coalesce(library_files.witness_fingerprint, library_files.fingerprint),
		       coalesce(library_files.witness_fingerprint_seconds, 0),
		       CASE WHEN library_file_identities.is_manual THEN 'manual'
		            ELSE library_file_identities.method END,
		       coalesce(library_files.duration_ms, 0)
		FROM library_files
		JOIN library_file_identities
		    ON library_file_identities.library_file_id = library_files.id
		WHERE library_files.missing_at IS NULL
		  AND library_files.resolution_status = 'resolved'
		  AND (
		      library_files.witness_fingerprint IS NOT NULL
		      OR coalesce(library_files.fingerprint, '') <> ''
		  )
		  AND library_file_identities.kind = 'external'
		  AND library_file_identities.musicbrainz_recording_id = $1
		  AND (
		      library_file_identities.is_manual
		      -- The release only ever singles one recording out of several that
		      -- were each already proven, so the proof underneath it is what
		      -- counts here.
		      OR regexp_replace(library_file_identities.method, '-and-release$', '')
		         = ANY ($2::text[])
		  )
		ORDER BY library_file_identities.is_manual DESC,
		         library_files.created_at, library_files.id
		LIMIT $3
	`, recordingID, settledProofs, limit)
	if err != nil {
		return nil, fmt.Errorf("read the settled files for recording %s: %w", recordingID, err)
	}
	defer rows.Close()

	witnesses := make([]LibraryWitnessRow, 0, limit)
	for rows.Next() {
		var witness LibraryWitnessRow
		if err := rows.Scan(
			&witness.FileID, &witness.Path, &witness.Fingerprint,
			&witness.CoverageSeconds, &witness.Proof, &witness.DurationMS,
		); err != nil {
			return nil, err
		}
		witnesses = append(witnesses, witness)
	}
	return witnesses, rows.Err()
}

// ProofSentence says, in plain words, what settled the file a copy was admitted
// by. The names are Schall's own and none of them may reach the screen.
//
// It is deliberately not identity.MethodSentence: that sentence is about the
// file it describes ("Schall listened to the audio and it matches this
// recording"), and this one is read inside a sentence about another file, where
// it has to be a fragment.
func ProofSentence(proof string) string {
	switch proof {
	case "manual":
		return "a decision you made"
	case "fingerprint", "fingerprint-and-identifier":
		return "its audio"
	case "recording-id":
		return "its MusicBrainz identifier"
	case "isrc":
		return "its ISRC"
	case "published-sample":
		return "the sample its distributor publishes"
	case "library-witness":
		return "another file you own"
	}
	return "the evidence Schall had"
}

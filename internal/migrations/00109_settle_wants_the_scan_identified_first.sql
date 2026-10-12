-- +goose Up
-- A want with no recording whose proven copy the scan identified as a
-- MusicBrainz recording before the copy was completed stopped as a
-- disagreement. Nothing contradicts such a want: it names only its anchor, and
-- the anchor proved the copy. This settles the wants that stopped that way, as
-- completion now does. The file keeps the identity the scan gave it.
WITH stopped AS (
    SELECT DISTINCT ON (targets.id) targets.id, files.library_file_id
    FROM acquisition_targets targets
    JOIN acquisition_target_files files ON files.acquisition_target_id = targets.id
    WHERE targets.status = 'unresolved'
      AND targets.musicbrainz_recording_id IS NULL
      AND targets.next_search_at IS NULL
      AND targets.external_id IS NULL
      AND targets.anchor_fingerprint IS NOT NULL
      AND targets.anchor_source IN ('deezer', 'youtube-topic', 'source')
      AND files.verdict = 'accepted'
      AND files.library_file_id IS NOT NULL
      AND files.evidence->>'method' = 'published-sample'
      AND files.evidence->'source'->>'externalId' IS NOT NULL
      AND NOT EXISTS (
          SELECT 1 FROM library_file_identities identities
          WHERE identities.library_file_id = files.library_file_id
            AND (identities.kind IN ('source', 'local_only') OR identities.is_manual)
      )
      AND EXISTS (
          SELECT 1 FROM library_file_identities identities
          WHERE identities.library_file_id = files.library_file_id
            AND identities.kind = 'external'
      )
    ORDER BY targets.id, files.decided_at
),
settled AS (
    UPDATE acquisition_targets targets
    SET status = 'acquired',
        acquired_at = now(),
        acquired_library_file_id = stopped.library_file_id,
        attempts = targets.attempts + 1,
        last_attempt_at = now(),
        next_attempt_at = NULL,
        next_search_at = NULL,
        last_error = NULL,
        summary = 'In your library.',
        recheck_reason = NULL,
        recheck_after = NULL,
        updated_at = now()
    FROM stopped
    WHERE targets.id = stopped.id
    RETURNING targets.id, targets.attempts
)
INSERT INTO acquisition_target_attempts (acquisition_target_id, attempt, outcome, detail)
SELECT id, attempts, 'acquired',
       'Proven by its anchor. The library had already identified the file.'
FROM settled;

-- A copy held because AcoustID's leading cluster named the want beside another
-- row can now be admitted when the file's own MusicBrainz recording ID or ISRC
-- picks the want out of that cluster. One pass over the copies already decided
-- about applies that rule to them, through the same validation any copy gets.
INSERT INTO jobs (kind, payload, max_attempts, run_after)
SELECT 'judge_copies_again', '{}'::jsonb, 1, now()
WHERE NOT EXISTS (
    SELECT 1 FROM jobs
    WHERE kind = 'judge_copies_again' AND status IN ('queued', 'running')
);

-- +goose Down
-- A want that settled holds its music, and a verdict reached by the rules in
-- force is not this migration's to undo.
DELETE FROM jobs WHERE kind = 'judge_copies_again' AND status = 'queued';

-- +goose Up
-- An import used to fail when an earlier download still named the path it
-- wrote, which is every replacement of a damaged copy and every song fetched
-- again after its first copy was removed. Those imports ran out of attempts and
-- were set aside, and the copy then read as a download that ended without the
-- file. The import now lets the old claim go, so they are sent back through
-- validation the way a person's retry sends them, and the boot queues every
-- pending import.
--
-- Only a want still looking is retried. One that has since let in another
-- copy, or has another copy on its way, keeps its answer: retrying this one
-- could only add the same song twice. One somebody removed stays removed.
-- download_requests holds one row per transfer, so this stays well inside
-- startup.
CREATE TEMPORARY TABLE refused_by_an_old_path ON COMMIT DROP AS
SELECT requests.id
FROM download_requests requests
WHERE requests.status = 'completed'
  AND requests.import_status = 'needs_review'
  AND strpos(requests.import_error, 'downloads_local_path_idx') > 0
  AND (requests.acquisition_target_id IS NULL OR (
      EXISTS (
          SELECT 1 FROM acquisition_targets wants
          WHERE wants.id = requests.acquisition_target_id
            AND wants.status IN ('pending', 'searching', 'awaiting_review')
      )
      AND NOT EXISTS (
          SELECT 1 FROM acquisition_target_files accepted
          WHERE accepted.acquisition_target_id = requests.acquisition_target_id
            AND accepted.verdict = 'accepted'
            AND accepted.file_removed_at IS NULL
      )
      AND NOT EXISTS (
          SELECT 1 FROM download_requests other
          WHERE other.acquisition_target_id = requests.acquisition_target_id
            AND other.id <> requests.id
            AND (other.status IN ('requested', 'started')
                 OR (other.status = 'completed'
                     AND other.import_status IN ('pending', 'validating')))
      )
  ));

UPDATE download_requests
SET import_status = 'pending', import_error = NULL, updated_at = now()
WHERE id IN (SELECT id FROM refused_by_an_old_path);

INSERT INTO download_import_reviews (download_request_id, kind)
SELECT id, 'revalidated' FROM refused_by_an_old_path;

-- The copy goes back to being fetched, which is what it is again until the
-- import decides about it.
UPDATE acquisition_target_files
SET verdict = 'fetching', summary = 'Downloading.', decided_at = now(), updated_at = now()
WHERE download_request_id IN (SELECT id FROM refused_by_an_old_path)
  AND verdict = 'undelivered'
  AND decided_by = 'schall';

-- +goose Down
-- Nothing to undo: a retry that has run is an import that has been tried again.
SELECT 1;

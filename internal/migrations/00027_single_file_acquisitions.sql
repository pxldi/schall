-- +goose Up
-- A want is acquired one file at a time. Migration 00024 already recorded which
-- want a download request serves; what it could not do on its own was let a
-- request exist that is not about a catalogue release. A resolved entry names a
-- recording, and the release that recording belongs to may not be in the
-- catalogue and must never have to be: a want does not wait for somebody to
-- follow an artist.
--
-- The alternative was a second table, and with it a second transfer start, a
-- second poller, a second retry, a second cancel and a second Downloads view.
-- One file out of a peer's folder is the same transfer as eleven of them.
ALTER TABLE download_requests ALTER COLUMN album_id DROP NOT NULL;

-- Every request is about something: a release somebody chose a folder for, or a
-- want somebody is looking for a file for. Never neither.
--
-- The target reference is ON DELETE SET NULL from 00024, which would leave a
-- request about nothing, and this refuses it. Nothing deletes a target — not
-- wanted is a state a target keeps rather than a row that disappears — so this
-- is the formality it ought to be. If a delete is ever written, it stops here
-- rather than quietly leaving provenance with no subject.
ALTER TABLE download_requests
    ADD CONSTRAINT download_requests_scope_present
        CHECK (album_id IS NOT NULL OR acquisition_target_id IS NOT NULL);

-- One open request per want, for the same reason a release may have only one
-- open request per folder: a want is pursued one candidate at a time, and a
-- second attempt while one is in flight has to find the open one rather than
-- starting a second transfer of the same music.
CREATE UNIQUE INDEX download_requests_open_target_idx
    ON download_requests (acquisition_target_id)
    WHERE acquisition_target_id IS NOT NULL AND status IN ('requested', 'started');

-- A transfer row copies its album from the request that asked for it, and a
-- request serving a want has none. The row is accounted for by that request,
-- which is what downloads.download_request_id has said since 00007; before it
-- existed, a transfer could only be explained by the release or the track it was
-- for, and this constraint was the only thing saying so.
ALTER TABLE downloads
    DROP CONSTRAINT downloads_target_present,
    ADD CONSTRAINT downloads_target_present
        CHECK (album_id IS NOT NULL OR track_id IS NOT NULL
               OR download_request_id IS NOT NULL);

-- +goose Down
DROP INDEX IF EXISTS download_requests_open_target_idx;

ALTER TABLE download_requests
    DROP CONSTRAINT IF EXISTS download_requests_scope_present;

-- A request recorded for a want has no release to belong to, so restoring the
-- NOT NULL means letting those go. They are provenance about transfers the
-- rolled-back code can no longer read anyway. Their transfer rows go with them,
-- which is also what lets the stricter constraint below be restored: those rows
-- are exactly the ones it refuses.
DELETE FROM download_requests WHERE album_id IS NULL;

ALTER TABLE download_requests ALTER COLUMN album_id SET NOT NULL;

ALTER TABLE downloads
    DROP CONSTRAINT IF EXISTS downloads_target_present,
    ADD CONSTRAINT downloads_target_present
        CHECK (album_id IS NOT NULL OR track_id IS NOT NULL);

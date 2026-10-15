-- +goose Up
-- An AIFF or WAV with no tags is silence, not damage (TagsAbsent), but the
-- scanner stored the tag reader's "no tags found" as a scan error, so those
-- files were listed as unreadable although they play. The scanner no longer
-- writes it; this clears what it wrote before. The scan only rewrites a file
-- whose size or time changed, so these rows would otherwise keep it.
UPDATE library_files SET scan_error = NULL, updated_at = now()
WHERE scan_error = 'no tags found';

-- +goose Down
SELECT 1;

-- +goose Up
-- The offer summaries and source reasons were reworded. Rows written before
-- keep the old text, so the exact constants are rewritten to match.
UPDATE acquisition_target_files SET summary = 'Being fetched. Name and length match.'
WHERE summary = 'Being fetched. Named and timed like the wanted recording.';
UPDATE acquisition_target_files SET summary = 'Being fetched. Name or length matches, not both.'
WHERE summary = 'Being fetched. Either the name or the length fits, not both.';
UPDATE acquisition_target_files SET summary = 'Being fetched. Name matches, length differs.'
WHERE summary = 'Being fetched. Named like the wanted recording, but the peer says it runs to another length.';
UPDATE acquisition_target_files SET summary = 'Being fetched. Length differs.'
WHERE summary = 'Being fetched. The peer says it runs to another length.';
UPDATE acquisition_target_files SET summary = 'Being fetched. No match by name or length.'
WHERE summary = 'Being fetched. Nothing the peer disclosed fits; it has not been heard either.';

UPDATE download_requests SET reasons = array_replace(reasons,
    'Named and timed like the wanted recording', 'Name and length match')
WHERE 'Named and timed like the wanted recording' = ANY(reasons);
UPDATE download_requests SET reasons = array_replace(reasons,
    'Either the name or the length fits, not both', 'Name or length matches, not both')
WHERE 'Either the name or the length fits, not both' = ANY(reasons);
UPDATE download_requests SET reasons = array_replace(reasons,
    'Named like the wanted recording, but the peer says it runs to another length', 'Name matches, length differs')
WHERE 'Named like the wanted recording, but the peer says it runs to another length' = ANY(reasons);
UPDATE download_requests SET reasons = array_replace(reasons,
    'The peer says it runs to another length', 'Length differs')
WHERE 'The peer says it runs to another length' = ANY(reasons);
UPDATE download_requests SET reasons = array_replace(reasons,
    'Nothing the peer disclosed fits; it has not been heard either', 'No match by name or length')
WHERE 'Nothing the peer disclosed fits; it has not been heard either' = ANY(reasons);
UPDATE download_requests SET reasons = array_replace(reasons,
    'Peer upload speed is unknown', 'Upload speed unknown')
WHERE 'Peer upload speed is unknown' = ANY(reasons);
UPDATE download_requests SET reasons = array_replace(reasons,
    'Folder mixes several audio formats', 'Mixed audio formats')
WHERE 'Folder mixes several audio formats' = ANY(reasons);
UPDATE download_requests SET reasons = array_replace(reasons,
    'Audio format could not be determined', 'Unknown audio format')
WHERE 'Audio format could not be determined' = ANY(reasons);
UPDATE download_requests SET reasons = array_replace(reasons,
    'No free slot, but the queue is empty', 'No free slot, empty queue')
WHERE 'No free slot, but the queue is empty' = ANY(reasons);

-- +goose Down
-- The old wording is not restored.

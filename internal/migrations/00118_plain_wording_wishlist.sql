-- +goose Up
-- Wishlist and download status lines were rewritten in plain words. Rows
-- written before keep the old text, and the acquisition loop compares a few
-- of these summaries exactly (a stranded want, a copy a peer deferred or
-- never sent, a want MusicBrainz knew nothing of), so the stored text moves
-- with the constants.

UPDATE acquisition_targets SET summary = CASE summary
    WHEN 'No peer is sharing a copy yet. It will be looked for again.'
        THEN 'Nobody shares it yet. Retrying later.'
    WHEN 'Add an AcoustID key in Settings — nothing here can prove a copy is this recording.'
        THEN 'Can''t check audio. Add an AcoustID key in Settings.'
    WHEN 'There is nothing in this entry that could be searched for.'
        THEN 'Nothing to search for'
    WHEN 'A copy is on its way. What it turns out to be decides what happens next.'
        THEN 'Downloading a copy'
    WHEN 'The download source never sent this search out. It will be looked for again in half an hour.'
        THEN 'Search never went out. Retrying in 30 min.'
    WHEN 'The download source is not logged in to Soulseek. It will be looked for again in half an hour.'
        THEN 'Not logged in to Soulseek. Retrying in 30 min.'
    WHEN 'Soulseek has stopped answering this account''s searches. It will be looked for again in half an hour.'
        THEN 'Soulseek isn''t answering searches. Retrying in 30 min.'
    WHEN 'Peers answered, but the download source had not stored their replies. It will be looked for again shortly.'
        THEN 'Replies arrived too late to read. Retrying soon.'
    WHEN 'This search was made moments ago. It will be made again shortly.'
        THEN 'Same search just ran. Retrying soon.'
    WHEN 'Copies are on offer, but none in a format you accept. Change your format preferences to take one.'
        THEN 'No copy in a format you accept. Change format preferences.'
    WHEN 'Copies are on offer, but all below your minimum bit rate. Lower it in Settings to take one.'
        THEN 'All copies below your bit rate floor. Lower it in Settings.'
    WHEN 'Every peer sharing a copy is already sending as much as it will take at once. They will be asked again shortly.'
        THEN 'Everyone sharing it is busy. Retrying soon.'
    WHEN 'The peer that accepted this download never sent it. Schall will look elsewhere.'
        THEN 'Peer never sent it. Trying elsewhere.'
    WHEN 'The peer accepted this download and never sent it.'
        THEN 'Peer accepted but never sent it'
    WHEN 'Keyed to an address. Looking for a copy that matches its audio.'
        THEN 'Linked. Looking for a copy that sounds the same.'
    WHEN 'Keyed to an address. The library already holds this track.'
        THEN 'Linked. Already in your library.'
    WHEN 'Already wanted. Another entry is keyed to the same address and carries it now.'
        THEN 'Another wishlist song has the same link.'
    WHEN 'Another entry keyed to this address was removed from your wishlist. It has not been added again.'
        THEN 'You removed a song with this link. Not added again.'
    WHEN 'No peer is sharing a copy. The track was fetched from its address and is being checked.'
        THEN 'Nobody shares it. Checking the copy from the link.'
    WHEN 'Sounds the same as a better copy kept for this want. Set aside, and its file is removed from the download folder.'
        THEN 'Same audio as a better copy. File removed from downloads.'
    WHEN 'Waiting to be resolved to a MusicBrainz recording.'
        THEN 'Looking up on MusicBrainz.'
    WHEN 'No recording MusicBrainz knows of matches this entry yet. It will be asked about again.'
        THEN 'Not on MusicBrainz yet. Retrying later.'
    WHEN 'MusicBrainz could not be reached, so this entry has not been resolved yet.'
        THEN 'MusicBrainz unreachable. Retrying soon.'
    WHEN 'MusicBrainz is busy. This entry will be asked about again shortly.'
        THEN 'MusicBrainz is busy. Retrying soon.'
    WHEN 'Already wanted. This entry names the same recording as another target, which carries it now.'
        THEN 'Already on your wishlist.'
    WHEN 'This entry resolved to a recording you said you did not want. It has not been added again.'
        THEN 'You removed this song before. Not added again.'
    WHEN 'Wanted. Waiting for a copy to be found.'
        THEN 'Looking for a copy.'
    WHEN 'The download source is busy. It will be asked again shortly.'
        THEN 'Download source busy. Retrying soon.'
    WHEN 'The download source cannot search right now. It will be asked again shortly.'
        THEN 'Download source can''t search now. Retrying soon.'
    WHEN 'A copy arrived and was proven. It is being taken into your library.'
        THEN 'Copy verified. Adding it to your library.'
    WHEN 'A copy was imported, but it never appeared in your library. Nothing is being fetched for this wishlist entry while that copy sits there.'
        THEN 'Imported copy missing from your library. Downloads paused.'
    WHEN 'No longer wanted. This recording only; the release and the artist are untouched.'
        THEN 'Removed. Album and artist untouched.'
    WHEN 'The recording this entry was resolved to was the wrong one. Being looked up again, without it.'
        THEN 'Wrong MusicBrainz song. Looking it up again.'
    WHEN 'You chose which recording this entry names. Wanted. Waiting for a copy to be found.'
        THEN 'You chose the song. Looking for a copy.'
    WHEN 'You listened to this copy and said it is the wanted recording.'
        THEN 'You listened and accepted this copy.'
    WHEN 'You listened and said this copy is not the wanted recording. It will not be offered for this wishlist entry again.'
        THEN 'You listened and rejected this copy. It won''t be offered again.'
    WHEN 'The decision about this copy was taken back. It is waiting for one again.'
        THEN 'Decision undone. Listen to decide.'
    WHEN 'A copy is waiting for your decision again.'
        THEN 'A copy is waiting again. Listen to decide.'
    WHEN 'You took back saying this entry names the wrong recording. Wanted. Waiting for a copy to be found.'
        THEN 'You undid "wrong song". Looking for a copy.'
    WHEN 'A copy arrived and was proven, but your library calls that file a different recording. Nothing more is being fetched — match the file to this track, or stop looking.'
        THEN 'Library files this copy as another song. Match the file, or remove the song.'
    WHEN 'A copy arrived and was proven, but your library records that file as owned music with no catalogue recording. Nothing more is being fetched — match the file to this track, or stop looking.'
        THEN 'Library files this copy as local music. Match the file, or remove the song.'
    WHEN 'A copy arrived, but your library has no identity for that file and nothing on record proves the copy is this recording. Nothing more is being fetched — match the file to this track, or stop looking.'
        THEN 'Library can''t tell what this copy is. Match the file, or remove the song.'
    WHEN 'Every copy found so far carries a different artist credit. Nothing more is being fetched until you decide whether these copies are the wanted recording.'
        THEN 'Every copy credits another artist. Paused. Listen to decide.'
    WHEN 'A copy is waiting for your answer. Nothing more is fetched until you decide.'
        THEN 'A copy is waiting. Listen to decide.'
    WHEN 'Not in your library yet. The next copy will be tried.'
        THEN 'Not in your library yet. Trying the next copy.'
    WHEN 'Removed. Not looked for any more.'
        THEN 'Removed from wishlist.'
    WHEN 'the copy the library holds is below the bit rate you asked for'
        THEN 'Your copy is below your bit rate floor.'
    WHEN 'wanted for the weekly playlist'
        THEN 'For the weekly playlist'
    WHEN 'You decided what that file is. This wishlist entry is being looked at again.'
        THEN 'You decided what the file is. Checking again.'
    WHEN 'The copy in your library was deleted. This recording is being looked for again.'
        THEN 'Your copy was deleted. Looking for it again.'
    WHEN 'Your library files that copy under this recording now. This wishlist entry is being looked at again.'
        THEN 'Library now files the copy as this song. Checking again.'
    WHEN 'There is a sample of this recording to compare copies against now. This wishlist entry is being looked at again.'
        THEN 'Sample found. Checking copies again.'
    WHEN 'Your library has said what that file is. This wishlist entry is being looked at again.'
        THEN 'Library identified the file. Checking again.'
    WHEN 'The peer is already sending as much as it will take at once, so this copy was not sent. Nothing about the music was learned.'
        THEN 'Peer busy. Copy not sent.'
    WHEN 'The transfer settled without the copy arriving.'
        THEN 'Download ended without the file.'
    WHEN 'A peer has a copy and is busy. It will be asked again shortly.'
        THEN 'Peer busy. Retrying soon.'
    WHEN 'The copy being fetched did not arrive. It will be looked for again.'
        THEN 'Download failed. Looking again.'
    ELSE summary END
WHERE summary IN (
    'No peer is sharing a copy yet. It will be looked for again.',
    'Add an AcoustID key in Settings — nothing here can prove a copy is this recording.',
    'There is nothing in this entry that could be searched for.',
    'A copy is on its way. What it turns out to be decides what happens next.',
    'The download source never sent this search out. It will be looked for again in half an hour.',
    'The download source is not logged in to Soulseek. It will be looked for again in half an hour.',
    'Soulseek has stopped answering this account''s searches. It will be looked for again in half an hour.',
    'Peers answered, but the download source had not stored their replies. It will be looked for again shortly.',
    'This search was made moments ago. It will be made again shortly.',
    'Copies are on offer, but none in a format you accept. Change your format preferences to take one.',
    'Copies are on offer, but all below your minimum bit rate. Lower it in Settings to take one.',
    'Every peer sharing a copy is already sending as much as it will take at once. They will be asked again shortly.',
    'The peer that accepted this download never sent it. Schall will look elsewhere.',
    'The peer accepted this download and never sent it.',
    'Keyed to an address. Looking for a copy that matches its audio.',
    'Keyed to an address. The library already holds this track.',
    'Already wanted. Another entry is keyed to the same address and carries it now.',
    'Another entry keyed to this address was removed from your wishlist. It has not been added again.',
    'No peer is sharing a copy. The track was fetched from its address and is being checked.',
    'Sounds the same as a better copy kept for this want. Set aside, and its file is removed from the download folder.',
    'Waiting to be resolved to a MusicBrainz recording.',
    'No recording MusicBrainz knows of matches this entry yet. It will be asked about again.',
    'MusicBrainz could not be reached, so this entry has not been resolved yet.',
    'MusicBrainz is busy. This entry will be asked about again shortly.',
    'Already wanted. This entry names the same recording as another target, which carries it now.',
    'This entry resolved to a recording you said you did not want. It has not been added again.',
    'Wanted. Waiting for a copy to be found.',
    'The download source is busy. It will be asked again shortly.',
    'The download source cannot search right now. It will be asked again shortly.',
    'A copy arrived and was proven. It is being taken into your library.',
    'A copy was imported, but it never appeared in your library. Nothing is being fetched for this wishlist entry while that copy sits there.',
    'No longer wanted. This recording only; the release and the artist are untouched.',
    'The recording this entry was resolved to was the wrong one. Being looked up again, without it.',
    'You chose which recording this entry names. Wanted. Waiting for a copy to be found.',
    'You listened to this copy and said it is the wanted recording.',
    'You listened and said this copy is not the wanted recording. It will not be offered for this wishlist entry again.',
    'The decision about this copy was taken back. It is waiting for one again.',
    'A copy is waiting for your decision again.',
    'You took back saying this entry names the wrong recording. Wanted. Waiting for a copy to be found.',
    'A copy arrived and was proven, but your library calls that file a different recording. Nothing more is being fetched — match the file to this track, or stop looking.',
    'A copy arrived and was proven, but your library records that file as owned music with no catalogue recording. Nothing more is being fetched — match the file to this track, or stop looking.',
    'A copy arrived, but your library has no identity for that file and nothing on record proves the copy is this recording. Nothing more is being fetched — match the file to this track, or stop looking.',
    'Every copy found so far carries a different artist credit. Nothing more is being fetched until you decide whether these copies are the wanted recording.',
    'A copy is waiting for your answer. Nothing more is fetched until you decide.',
    'Not in your library yet. The next copy will be tried.',
    'Removed. Not looked for any more.',
    'the copy the library holds is below the bit rate you asked for',
    'wanted for the weekly playlist',
    'You decided what that file is. This wishlist entry is being looked at again.',
    'The copy in your library was deleted. This recording is being looked for again.',
    'Your library files that copy under this recording now. This wishlist entry is being looked at again.',
    'There is a sample of this recording to compare copies against now. This wishlist entry is being looked at again.',
    'Your library has said what that file is. This wishlist entry is being looked at again.',
    'The peer is already sending as much as it will take at once, so this copy was not sent. Nothing about the music was learned.',
    'The transfer settled without the copy arriving.',
    'A peer has a copy and is busy. It will be asked again shortly.',
    'The copy being fetched did not arrive. It will be looked for again.'
);

UPDATE acquisition_targets SET summary = regexp_replace(summary, '^Being fetched\. (.*)$', 'Downloading. \1')
WHERE summary ~ '^Being fetched\. (.*)$';

UPDATE acquisition_targets SET summary = regexp_replace(summary, '^The transfer could not be started: (.*)$', 'Download didn''t start: \1')
WHERE summary ~ '^The transfer could not be started: (.*)$';

UPDATE acquisition_targets SET summary = regexp_replace(summary, '^Only copies below your ([0-9]+) kbps minimum were found\. Looked at again weekly\.$', 'Only copies under \1 kbps. Retrying weekly.')
WHERE summary ~ '^Only copies below your ([0-9]+) kbps minimum were found\. Looked at again weekly\.$';

UPDATE acquisition_targets SET summary = regexp_replace(summary, '^Fetched from (.*)\. Being checked against the excerpt\.$', 'Downloaded from \1. Checking against the sample.')
WHERE summary ~ '^Fetched from (.*)\. Being checked against the excerpt\.$';

UPDATE acquisition_targets SET summary = regexp_replace(summary, '^Your library files that copy as (.*)\. This entry wants (.*), and the audio cannot tell the two apart\. Match the file to this track, or stop looking\.$', 'Library files this copy as \1, not \2. Same audio. Match the file, or remove the song.')
WHERE summary ~ '^Your library files that copy as (.*)\. This entry wants (.*), and the audio cannot tell the two apart\. Match the file to this track, or stop looking\.$';

UPDATE acquisition_targets SET summary = regexp_replace(summary, '^obtained for the weekly playlist and removed on (.*), unkept$', 'From the weekly playlist. Removed \1, not kept.')
WHERE summary ~ '^obtained for the weekly playlist and removed on (.*), unkept$';

UPDATE acquisition_target_files SET summary = CASE summary
    WHEN 'No peer is sharing a copy yet. It will be looked for again.'
        THEN 'Nobody shares it yet. Retrying later.'
    WHEN 'Add an AcoustID key in Settings — nothing here can prove a copy is this recording.'
        THEN 'Can''t check audio. Add an AcoustID key in Settings.'
    WHEN 'There is nothing in this entry that could be searched for.'
        THEN 'Nothing to search for'
    WHEN 'A copy is on its way. What it turns out to be decides what happens next.'
        THEN 'Downloading a copy'
    WHEN 'The download source never sent this search out. It will be looked for again in half an hour.'
        THEN 'Search never went out. Retrying in 30 min.'
    WHEN 'The download source is not logged in to Soulseek. It will be looked for again in half an hour.'
        THEN 'Not logged in to Soulseek. Retrying in 30 min.'
    WHEN 'Soulseek has stopped answering this account''s searches. It will be looked for again in half an hour.'
        THEN 'Soulseek isn''t answering searches. Retrying in 30 min.'
    WHEN 'Peers answered, but the download source had not stored their replies. It will be looked for again shortly.'
        THEN 'Replies arrived too late to read. Retrying soon.'
    WHEN 'This search was made moments ago. It will be made again shortly.'
        THEN 'Same search just ran. Retrying soon.'
    WHEN 'Copies are on offer, but none in a format you accept. Change your format preferences to take one.'
        THEN 'No copy in a format you accept. Change format preferences.'
    WHEN 'Copies are on offer, but all below your minimum bit rate. Lower it in Settings to take one.'
        THEN 'All copies below your bit rate floor. Lower it in Settings.'
    WHEN 'Every peer sharing a copy is already sending as much as it will take at once. They will be asked again shortly.'
        THEN 'Everyone sharing it is busy. Retrying soon.'
    WHEN 'The peer that accepted this download never sent it. Schall will look elsewhere.'
        THEN 'Peer never sent it. Trying elsewhere.'
    WHEN 'The peer accepted this download and never sent it.'
        THEN 'Peer accepted but never sent it'
    WHEN 'Keyed to an address. Looking for a copy that matches its audio.'
        THEN 'Linked. Looking for a copy that sounds the same.'
    WHEN 'Keyed to an address. The library already holds this track.'
        THEN 'Linked. Already in your library.'
    WHEN 'Already wanted. Another entry is keyed to the same address and carries it now.'
        THEN 'Another wishlist song has the same link.'
    WHEN 'Another entry keyed to this address was removed from your wishlist. It has not been added again.'
        THEN 'You removed a song with this link. Not added again.'
    WHEN 'No peer is sharing a copy. The track was fetched from its address and is being checked.'
        THEN 'Nobody shares it. Checking the copy from the link.'
    WHEN 'Sounds the same as a better copy kept for this want. Set aside, and its file is removed from the download folder.'
        THEN 'Same audio as a better copy. File removed from downloads.'
    WHEN 'Waiting to be resolved to a MusicBrainz recording.'
        THEN 'Looking up on MusicBrainz.'
    WHEN 'No recording MusicBrainz knows of matches this entry yet. It will be asked about again.'
        THEN 'Not on MusicBrainz yet. Retrying later.'
    WHEN 'MusicBrainz could not be reached, so this entry has not been resolved yet.'
        THEN 'MusicBrainz unreachable. Retrying soon.'
    WHEN 'MusicBrainz is busy. This entry will be asked about again shortly.'
        THEN 'MusicBrainz is busy. Retrying soon.'
    WHEN 'Already wanted. This entry names the same recording as another target, which carries it now.'
        THEN 'Already on your wishlist.'
    WHEN 'This entry resolved to a recording you said you did not want. It has not been added again.'
        THEN 'You removed this song before. Not added again.'
    WHEN 'Wanted. Waiting for a copy to be found.'
        THEN 'Looking for a copy.'
    WHEN 'The download source is busy. It will be asked again shortly.'
        THEN 'Download source busy. Retrying soon.'
    WHEN 'The download source cannot search right now. It will be asked again shortly.'
        THEN 'Download source can''t search now. Retrying soon.'
    WHEN 'A copy arrived and was proven. It is being taken into your library.'
        THEN 'Copy verified. Adding it to your library.'
    WHEN 'A copy was imported, but it never appeared in your library. Nothing is being fetched for this wishlist entry while that copy sits there.'
        THEN 'Imported copy missing from your library. Downloads paused.'
    WHEN 'No longer wanted. This recording only; the release and the artist are untouched.'
        THEN 'Removed. Album and artist untouched.'
    WHEN 'The recording this entry was resolved to was the wrong one. Being looked up again, without it.'
        THEN 'Wrong MusicBrainz song. Looking it up again.'
    WHEN 'You chose which recording this entry names. Wanted. Waiting for a copy to be found.'
        THEN 'You chose the song. Looking for a copy.'
    WHEN 'You listened to this copy and said it is the wanted recording.'
        THEN 'You listened and accepted this copy.'
    WHEN 'You listened and said this copy is not the wanted recording. It will not be offered for this wishlist entry again.'
        THEN 'You listened and rejected this copy. It won''t be offered again.'
    WHEN 'The decision about this copy was taken back. It is waiting for one again.'
        THEN 'Decision undone. Listen to decide.'
    WHEN 'A copy is waiting for your decision again.'
        THEN 'A copy is waiting again. Listen to decide.'
    WHEN 'You took back saying this entry names the wrong recording. Wanted. Waiting for a copy to be found.'
        THEN 'You undid "wrong song". Looking for a copy.'
    WHEN 'A copy arrived and was proven, but your library calls that file a different recording. Nothing more is being fetched — match the file to this track, or stop looking.'
        THEN 'Library files this copy as another song. Match the file, or remove the song.'
    WHEN 'A copy arrived and was proven, but your library records that file as owned music with no catalogue recording. Nothing more is being fetched — match the file to this track, or stop looking.'
        THEN 'Library files this copy as local music. Match the file, or remove the song.'
    WHEN 'A copy arrived, but your library has no identity for that file and nothing on record proves the copy is this recording. Nothing more is being fetched — match the file to this track, or stop looking.'
        THEN 'Library can''t tell what this copy is. Match the file, or remove the song.'
    WHEN 'Every copy found so far carries a different artist credit. Nothing more is being fetched until you decide whether these copies are the wanted recording.'
        THEN 'Every copy credits another artist. Paused. Listen to decide.'
    WHEN 'A copy is waiting for your answer. Nothing more is fetched until you decide.'
        THEN 'A copy is waiting. Listen to decide.'
    WHEN 'Not in your library yet. The next copy will be tried.'
        THEN 'Not in your library yet. Trying the next copy.'
    WHEN 'Removed. Not looked for any more.'
        THEN 'Removed from wishlist.'
    WHEN 'the copy the library holds is below the bit rate you asked for'
        THEN 'Your copy is below your bit rate floor.'
    WHEN 'wanted for the weekly playlist'
        THEN 'For the weekly playlist'
    WHEN 'You decided what that file is. This wishlist entry is being looked at again.'
        THEN 'You decided what the file is. Checking again.'
    WHEN 'The copy in your library was deleted. This recording is being looked for again.'
        THEN 'Your copy was deleted. Looking for it again.'
    WHEN 'Your library files that copy under this recording now. This wishlist entry is being looked at again.'
        THEN 'Library now files the copy as this song. Checking again.'
    WHEN 'There is a sample of this recording to compare copies against now. This wishlist entry is being looked at again.'
        THEN 'Sample found. Checking copies again.'
    WHEN 'Your library has said what that file is. This wishlist entry is being looked at again.'
        THEN 'Library identified the file. Checking again.'
    WHEN 'The peer is already sending as much as it will take at once, so this copy was not sent. Nothing about the music was learned.'
        THEN 'Peer busy. Copy not sent.'
    WHEN 'The transfer settled without the copy arriving.'
        THEN 'Download ended without the file.'
    WHEN 'A peer has a copy and is busy. It will be asked again shortly.'
        THEN 'Peer busy. Retrying soon.'
    WHEN 'The copy being fetched did not arrive. It will be looked for again.'
        THEN 'Download failed. Looking again.'
    ELSE summary END
WHERE summary IN (
    'No peer is sharing a copy yet. It will be looked for again.',
    'Add an AcoustID key in Settings — nothing here can prove a copy is this recording.',
    'There is nothing in this entry that could be searched for.',
    'A copy is on its way. What it turns out to be decides what happens next.',
    'The download source never sent this search out. It will be looked for again in half an hour.',
    'The download source is not logged in to Soulseek. It will be looked for again in half an hour.',
    'Soulseek has stopped answering this account''s searches. It will be looked for again in half an hour.',
    'Peers answered, but the download source had not stored their replies. It will be looked for again shortly.',
    'This search was made moments ago. It will be made again shortly.',
    'Copies are on offer, but none in a format you accept. Change your format preferences to take one.',
    'Copies are on offer, but all below your minimum bit rate. Lower it in Settings to take one.',
    'Every peer sharing a copy is already sending as much as it will take at once. They will be asked again shortly.',
    'The peer that accepted this download never sent it. Schall will look elsewhere.',
    'The peer accepted this download and never sent it.',
    'Keyed to an address. Looking for a copy that matches its audio.',
    'Keyed to an address. The library already holds this track.',
    'Already wanted. Another entry is keyed to the same address and carries it now.',
    'Another entry keyed to this address was removed from your wishlist. It has not been added again.',
    'No peer is sharing a copy. The track was fetched from its address and is being checked.',
    'Sounds the same as a better copy kept for this want. Set aside, and its file is removed from the download folder.',
    'Waiting to be resolved to a MusicBrainz recording.',
    'No recording MusicBrainz knows of matches this entry yet. It will be asked about again.',
    'MusicBrainz could not be reached, so this entry has not been resolved yet.',
    'MusicBrainz is busy. This entry will be asked about again shortly.',
    'Already wanted. This entry names the same recording as another target, which carries it now.',
    'This entry resolved to a recording you said you did not want. It has not been added again.',
    'Wanted. Waiting for a copy to be found.',
    'The download source is busy. It will be asked again shortly.',
    'The download source cannot search right now. It will be asked again shortly.',
    'A copy arrived and was proven. It is being taken into your library.',
    'A copy was imported, but it never appeared in your library. Nothing is being fetched for this wishlist entry while that copy sits there.',
    'No longer wanted. This recording only; the release and the artist are untouched.',
    'The recording this entry was resolved to was the wrong one. Being looked up again, without it.',
    'You chose which recording this entry names. Wanted. Waiting for a copy to be found.',
    'You listened to this copy and said it is the wanted recording.',
    'You listened and said this copy is not the wanted recording. It will not be offered for this wishlist entry again.',
    'The decision about this copy was taken back. It is waiting for one again.',
    'A copy is waiting for your decision again.',
    'You took back saying this entry names the wrong recording. Wanted. Waiting for a copy to be found.',
    'A copy arrived and was proven, but your library calls that file a different recording. Nothing more is being fetched — match the file to this track, or stop looking.',
    'A copy arrived and was proven, but your library records that file as owned music with no catalogue recording. Nothing more is being fetched — match the file to this track, or stop looking.',
    'A copy arrived, but your library has no identity for that file and nothing on record proves the copy is this recording. Nothing more is being fetched — match the file to this track, or stop looking.',
    'Every copy found so far carries a different artist credit. Nothing more is being fetched until you decide whether these copies are the wanted recording.',
    'A copy is waiting for your answer. Nothing more is fetched until you decide.',
    'Not in your library yet. The next copy will be tried.',
    'Removed. Not looked for any more.',
    'the copy the library holds is below the bit rate you asked for',
    'wanted for the weekly playlist',
    'You decided what that file is. This wishlist entry is being looked at again.',
    'The copy in your library was deleted. This recording is being looked for again.',
    'Your library files that copy under this recording now. This wishlist entry is being looked at again.',
    'There is a sample of this recording to compare copies against now. This wishlist entry is being looked at again.',
    'Your library has said what that file is. This wishlist entry is being looked at again.',
    'The peer is already sending as much as it will take at once, so this copy was not sent. Nothing about the music was learned.',
    'The transfer settled without the copy arriving.',
    'A peer has a copy and is busy. It will be asked again shortly.',
    'The copy being fetched did not arrive. It will be looked for again.'
);

UPDATE acquisition_target_files SET summary = regexp_replace(summary, '^Being fetched\. (.*)$', 'Downloading. \1')
WHERE summary ~ '^Being fetched\. (.*)$';

UPDATE acquisition_target_files SET summary = regexp_replace(summary, '^The transfer could not be started: (.*)$', 'Download didn''t start: \1')
WHERE summary ~ '^The transfer could not be started: (.*)$';

UPDATE acquisition_target_files SET summary = regexp_replace(summary, '^Only copies below your ([0-9]+) kbps minimum were found\. Looked at again weekly\.$', 'Only copies under \1 kbps. Retrying weekly.')
WHERE summary ~ '^Only copies below your ([0-9]+) kbps minimum were found\. Looked at again weekly\.$';

UPDATE acquisition_target_files SET summary = regexp_replace(summary, '^Fetched from (.*)\. Being checked against the excerpt\.$', 'Downloaded from \1. Checking against the sample.')
WHERE summary ~ '^Fetched from (.*)\. Being checked against the excerpt\.$';

UPDATE acquisition_target_files SET summary = regexp_replace(summary, '^Your library files that copy as (.*)\. This entry wants (.*), and the audio cannot tell the two apart\. Match the file to this track, or stop looking\.$', 'Library files this copy as \1, not \2. Same audio. Match the file, or remove the song.')
WHERE summary ~ '^Your library files that copy as (.*)\. This entry wants (.*), and the audio cannot tell the two apart\. Match the file to this track, or stop looking\.$';

UPDATE acquisition_target_files SET summary = regexp_replace(summary, '^obtained for the weekly playlist and removed on (.*), unkept$', 'From the weekly playlist. Removed \1, not kept.')
WHERE summary ~ '^obtained for the weekly playlist and removed on (.*), unkept$';

UPDATE acquisition_targets SET last_error = CASE last_error
    WHEN 'Identifying files by their audio is switched off, and nothing else may admit a file a stranger sent.'
        THEN 'Audio identification is off.'
    WHEN 'Every word of the entry was punctuation a source search cannot use.'
        THEN 'Title and artist are only punctuation.'
    WHEN 'the download source is rate limiting requests: it refused another wishlist entry in the same pass'
        THEN 'download source is rate limiting searches'
    WHEN 'the download source cannot search at the moment: it refused another wishlist entry in the same pass'
        THEN 'download source can''t search right now'
    WHEN 'the download source is not logged in to Soulseek: it refused another wishlist entry in the same pass'
        THEN 'download source not logged in to Soulseek'
    WHEN 'the download source''s searches are going unanswered: it refused another wishlist entry in the same pass'
        THEN 'Soulseek isn''t answering this account''s searches'
    WHEN 'every peer offering this recording already has as many files open with Schall as it will accept'
        THEN 'every peer with a copy is at its upload limit'
    WHEN 'the peer had accepted the download and reported nothing for over two hours: no queue position, no bytes'
        THEN 'peer accepted, then sent nothing for 2 hours'
    WHEN 'MusicBrainz is refusing requests for now: it refused another entry in the same pass'
        THEN 'MusicBrainz is refusing requests for now'
    WHEN 'An accepted copy has been waiting to be scanned for longer than any scan takes.'
        THEN 'The copy has waited for a library scan far too long.'
    WHEN 'A person said this entry does not name that recording.'
        THEN 'You said this isn''t the song.'
    WHEN 'A person chose this recording from the ones the entry could have named.'
        THEN 'You picked this song from the matches.'
    WHEN 'A file the library already holds is proven to be this recording.'
        THEN 'Already in your library.'
    WHEN 'A copy was fetched, proven by its audio, and imported.'
        THEN 'Downloaded, audio verified, imported.'
    WHEN 'You said the library file this copy became is what this entry asked for.'
        THEN 'You said the library file is this song.'
    WHEN 'A copy was fetched, proven by its audio, and imported. Your library files it as the explicit edition of this track, which is the one Schall keeps.'
        THEN 'Downloaded, audio verified, imported. Kept as the explicit version.'
    WHEN 'No file in the library is proven to be this recording.'
        THEN 'No library file proven to be this song.'
    WHEN 'A copy was fetched and imported. MusicBrainz has since merged the recording your library filed it under with this one.'
        THEN 'Downloaded and imported. MusicBrainz merged the song it was filed under into this one.'
    ELSE last_error END
WHERE last_error IN (
    'Identifying files by their audio is switched off, and nothing else may admit a file a stranger sent.',
    'Every word of the entry was punctuation a source search cannot use.',
    'the download source is rate limiting requests: it refused another wishlist entry in the same pass',
    'the download source cannot search at the moment: it refused another wishlist entry in the same pass',
    'the download source is not logged in to Soulseek: it refused another wishlist entry in the same pass',
    'the download source''s searches are going unanswered: it refused another wishlist entry in the same pass',
    'every peer offering this recording already has as many files open with Schall as it will accept',
    'the peer had accepted the download and reported nothing for over two hours: no queue position, no bytes',
    'MusicBrainz is refusing requests for now: it refused another entry in the same pass',
    'An accepted copy has been waiting to be scanned for longer than any scan takes.',
    'A person said this entry does not name that recording.',
    'A person chose this recording from the ones the entry could have named.',
    'A file the library already holds is proven to be this recording.',
    'A copy was fetched, proven by its audio, and imported.',
    'You said the library file this copy became is what this entry asked for.',
    'A copy was fetched, proven by its audio, and imported. Your library files it as the explicit edition of this track, which is the one Schall keeps.',
    'No file in the library is proven to be this recording.',
    'A copy was fetched and imported. MusicBrainz has since merged the recording your library filed it under with this one.'
);

UPDATE acquisition_targets SET last_error = regexp_replace(last_error, '^no copy on offer met the stored format preferences\. Every copy offered for (.*) was in a format your preferences refuse: (.*)\.$', 'No copy in a format you accept. Searched \1: \2.')
WHERE last_error ~ '^no copy on offer met the stored format preferences\. Every copy offered for (.*) was in a format your preferences refuse: (.*)\.$';

UPDATE acquisition_targets SET last_error = regexp_replace(last_error, '^every copy on offer was below the minimum bit rate you set\. Every copy offered for (.*) was under it: (.*)\.$', 'All copies below your bit rate floor. Searched \1: \2.')
WHERE last_error ~ '^every copy on offer was below the minimum bit rate you set\. Every copy offered for (.*) was under it: (.*)\.$';

UPDATE acquisition_targets SET last_error = regexp_replace(last_error, '^A copy was fetched and imported\. (.*) The recording your library filed it under is the same registered track\.$', 'Downloaded and imported. \1 Filed under the same registered track.')
WHERE last_error ~ '^A copy was fetched and imported\. (.*) The recording your library filed it under is the same registered track\.$';

UPDATE acquisition_target_attempts SET detail = CASE detail
    WHEN 'Identifying files by their audio is switched off, and nothing else may admit a file a stranger sent.'
        THEN 'Audio identification is off.'
    WHEN 'Every word of the entry was punctuation a source search cannot use.'
        THEN 'Title and artist are only punctuation.'
    WHEN 'the download source is rate limiting requests: it refused another wishlist entry in the same pass'
        THEN 'download source is rate limiting searches'
    WHEN 'the download source cannot search at the moment: it refused another wishlist entry in the same pass'
        THEN 'download source can''t search right now'
    WHEN 'the download source is not logged in to Soulseek: it refused another wishlist entry in the same pass'
        THEN 'download source not logged in to Soulseek'
    WHEN 'the download source''s searches are going unanswered: it refused another wishlist entry in the same pass'
        THEN 'Soulseek isn''t answering this account''s searches'
    WHEN 'every peer offering this recording already has as many files open with Schall as it will accept'
        THEN 'every peer with a copy is at its upload limit'
    WHEN 'the peer had accepted the download and reported nothing for over two hours: no queue position, no bytes'
        THEN 'peer accepted, then sent nothing for 2 hours'
    WHEN 'MusicBrainz is refusing requests for now: it refused another entry in the same pass'
        THEN 'MusicBrainz is refusing requests for now'
    WHEN 'An accepted copy has been waiting to be scanned for longer than any scan takes.'
        THEN 'The copy has waited for a library scan far too long.'
    WHEN 'A person said this entry does not name that recording.'
        THEN 'You said this isn''t the song.'
    WHEN 'A person chose this recording from the ones the entry could have named.'
        THEN 'You picked this song from the matches.'
    WHEN 'A file the library already holds is proven to be this recording.'
        THEN 'Already in your library.'
    WHEN 'A copy was fetched, proven by its audio, and imported.'
        THEN 'Downloaded, audio verified, imported.'
    WHEN 'You said the library file this copy became is what this entry asked for.'
        THEN 'You said the library file is this song.'
    WHEN 'A copy was fetched, proven by its audio, and imported. Your library files it as the explicit edition of this track, which is the one Schall keeps.'
        THEN 'Downloaded, audio verified, imported. Kept as the explicit version.'
    WHEN 'No file in the library is proven to be this recording.'
        THEN 'No library file proven to be this song.'
    WHEN 'A copy was fetched and imported. MusicBrainz has since merged the recording your library filed it under with this one.'
        THEN 'Downloaded and imported. MusicBrainz merged the song it was filed under into this one.'
    ELSE detail END
WHERE detail IN (
    'Identifying files by their audio is switched off, and nothing else may admit a file a stranger sent.',
    'Every word of the entry was punctuation a source search cannot use.',
    'the download source is rate limiting requests: it refused another wishlist entry in the same pass',
    'the download source cannot search at the moment: it refused another wishlist entry in the same pass',
    'the download source is not logged in to Soulseek: it refused another wishlist entry in the same pass',
    'the download source''s searches are going unanswered: it refused another wishlist entry in the same pass',
    'every peer offering this recording already has as many files open with Schall as it will accept',
    'the peer had accepted the download and reported nothing for over two hours: no queue position, no bytes',
    'MusicBrainz is refusing requests for now: it refused another entry in the same pass',
    'An accepted copy has been waiting to be scanned for longer than any scan takes.',
    'A person said this entry does not name that recording.',
    'A person chose this recording from the ones the entry could have named.',
    'A file the library already holds is proven to be this recording.',
    'A copy was fetched, proven by its audio, and imported.',
    'You said the library file this copy became is what this entry asked for.',
    'A copy was fetched, proven by its audio, and imported. Your library files it as the explicit edition of this track, which is the one Schall keeps.',
    'No file in the library is proven to be this recording.',
    'A copy was fetched and imported. MusicBrainz has since merged the recording your library filed it under with this one.'
);

UPDATE acquisition_target_attempts SET detail = regexp_replace(detail, '^no copy on offer met the stored format preferences\. Every copy offered for (.*) was in a format your preferences refuse: (.*)\.$', 'No copy in a format you accept. Searched \1: \2.')
WHERE detail ~ '^no copy on offer met the stored format preferences\. Every copy offered for (.*) was in a format your preferences refuse: (.*)\.$';

UPDATE acquisition_target_attempts SET detail = regexp_replace(detail, '^every copy on offer was below the minimum bit rate you set\. Every copy offered for (.*) was under it: (.*)\.$', 'All copies below your bit rate floor. Searched \1: \2.')
WHERE detail ~ '^every copy on offer was below the minimum bit rate you set\. Every copy offered for (.*) was under it: (.*)\.$';

UPDATE acquisition_target_attempts SET detail = regexp_replace(detail, '^A copy was fetched and imported\. (.*) The recording your library filed it under is the same registered track\.$', 'Downloaded and imported. \1 Filed under the same registered track.')
WHERE detail ~ '^A copy was fetched and imported\. (.*) The recording your library filed it under is the same registered track\.$';

UPDATE library_file_keep_reads SET detail = 'no player connected' WHERE detail = 'no player is connected, so nothing can say you kept a song';
UPDATE library_file_keep_reads SET detail = 'player didn''t answer' WHERE detail = 'the player did not answer about this song';
UPDATE library_file_leases SET extended_reason = 'no player connected' WHERE extended_reason = 'no player is connected, so nothing can say you kept a song';
UPDATE library_file_leases SET extended_reason = 'player didn''t answer' WHERE extended_reason = 'the player did not answer about this song';
UPDATE library_file_leases SET extended_reason = 'not kept; removals are off' WHERE extended_reason = 'nobody kept this song, and removals are switched off';
UPDATE library_file_leases SET extended_reason = 'not kept; Schall can''t remove files here' WHERE extended_reason = 'nobody kept this song, and nothing here can remove a file';
UPDATE weekly_playlist_runs SET detail = 'Schall restarted mid-refresh' WHERE detail = 'Schall stopped in the middle of this refresh';

-- +goose Down
-- The old wording is not restored. It was only ever text for a person to read.

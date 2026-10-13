-- +goose Up
-- The sentences Schall stores about copies, wants, samples and file identities
-- were rewritten as short plain fragments. Stored rows keep the text they were
-- written with, and the review queue and the judge-again queries match some of
-- it by its opening words, so every row is rewritten to the new text here.
--
-- Each rule replaces one old sentence, or one fmt-built shape of one, wherever
-- it stands in a column: a copy's summary is a verdict sentence followed by
-- the grader's, and an anchor reason can be two sentences joined. Rules run in
-- order. Migration 00117 rewrote the want sentences internal/acquisition
-- shares with internal/downloads. Release import reasons
-- (internal/downloads/match.go) keep the text they were written with.
-- +goose StatementBegin
DO $$
DECLARE
    target RECORD;
    rule RECORD;
BEGIN
    FOR target IN SELECT * FROM (VALUES
        ('acquisition_targets', 'summary'),
        ('acquisition_targets', 'anchor_unavailable'),
        ('acquisition_target_files', 'summary'),
        ('acquisition_target_candidates', 'summary'),
        ('acquisition_target_rejected_recordings', 'summary'),
        ('acquisition_target_attempts', 'detail'),
        ('download_requests', 'import_error'),
        ('library_files', 'resolution_summary'),
        ('library_file_identities', 'summary'),
        ('library_file_candidates', 'summary')
    ) AS columns_to_rewrite (table_name, column_name) LOOP
        FOR rule IN SELECT * FROM (VALUES
            (1, false, 'Proven to be the wanted recording and imported.', 'Proven and imported.'),
            (2, false, 'You listened to this copy and said it is the wanted recording. Imported.', 'You listened and accepted this copy.'),
            (3, false, 'The audio is a different recording. This copy will not be offered for this want again.', 'Audio is a different song. Won''t be offered again.'),
            (4, false, 'This copy does not match the published sample of the wanted recording. It will not be offered for this want again.', 'Doesn''t match the song''s sample. Won''t be offered again.'),
            (5, false, 'This file''s own identifiers contradict the wanted recording. It will not be offered for this want again.', 'Tags name a different song. Won''t be offered again.'),
            (6, false, 'The tags match, but nothing checked the audio, and tags alone cannot admit a file from a stranger. Listen to it and decide.', 'Tags match, audio unchecked. Listen to decide.'),
            (7, false, 'This copy does not reproduce the upload this want is anchored to, which does not refuse it: an upload found by name can be another version. Listen to it and decide.', 'Doesn''t match the upload found by name. Listen to decide.'),
            (8, false, 'The audio was identified as another recording, and MusicBrainz could not be asked whether that is this track entered twice. Kept as a question.', 'Sounds like another MusicBrainz song. MusicBrainz unreachable.'),
            (9, false, 'The audio names another recording and MusicBrainz will not say whether that is this track entered twice. Listen to the copy and decide.', 'Sounds like another MusicBrainz song. MusicBrainz never answered. Listen to decide.'),
            (10, false, 'Nothing could decide whether this copy is the wanted recording. It is kept as a question.', 'Unclear if this is the song. Listen to decide.'),
            (11, false, 'This file''s own tags disagree with the entry. Listen to it and decide.', 'Tags disagree with this song. Listen to decide.'),
            (12, false, 'MusicBrainz knows this file, but this entry has no recording yet. Kept as a question.', 'MusicBrainz knows this file, not this song yet.'),
            (13, false, 'This copy is the track, but below this want''s minimum bit rate. Accept it, or wait for a better copy.', 'Right song, below the bit rate floor. Accept it or wait for a better copy.'),
            (14, false, 'The bytes did not arrive as offered, so there was nothing to check.', 'File didn''t arrive as offered.'),
            (15, false, 'MusicBrainz no longer has the recording this entry named. The entry will be resolved again.', 'Gone from MusicBrainz. Looking it up again.'),
            (16, false, 'MusicBrainz no longer has the recording this was resolved to. It will be resolved again.', 'Gone from MusicBrainz. Looking it up again.'),
            (17, false, 'You resolved this entry by hand, but MusicBrainz no longer has that recording. It will be resolved again.', 'Your pick is gone from MusicBrainz. Looking it up again.'),
            (18, true, 'The audio is the track at the keyed address\. Its bit rate could not be read, so it is not shown to meet this want''s ([0-9]+) kbit/s minimum\.', 'Bit rate unreadable, floor is \1 kbit/s.'),
            (19, true, 'The audio is the track at the keyed address\. It is ([0-9]+) kbit/s, below this want''s ([0-9]+) kbit/s minimum\.', '\1 kbit/s, under the \2 kbit/s floor.'),
            (20, false, 'This copy is the wanted recording. Its file has gone from the download folder, so there is nothing left to import and the want is still looking.', 'Right song, but its file is gone from the download folder. Still looking.'),
            (21, false, 'This copy is not the wanted recording, and its file has gone from the download folder.', 'Wrong song. Its file is gone from the download folder.'),
            (22, false, 'This copy was measured against the wanted recording''s sample and it settled nothing. Its file has gone from the download folder.', 'Sample check unclear. Its file is gone from the download folder.'),
            (23, false, 'Deezer publishes no preview for this recording''s ISRC.', 'Deezer has no preview for this ISRC.'),
            (24, false, 'This entry carries no ISRC, and a preview is only ever fetched by ISRC.', 'No ISRC, so no preview.'),
            (25, false, 'This entry''s ISRC is not a registration code a preview can be fetched by.', 'ISRC is invalid, so no preview.'),
            (26, false, 'The preview Deezer returned could not be read as audio.', 'Deezer''s preview is unreadable.'),
            (27, false, 'This entry carries no length, and an upload is only ever chosen by name and length.', 'No length, so no upload search.'),
            (28, false, 'No upload matched this entry''s name and length.', 'No matching upload.'),
            (29, false, 'The excerpt taken from the upload could not be read as audio.', 'The upload''s audio is unreadable.'),
            (30, true, '^The preview could not be fetched: ', 'Couldn''t fetch the preview: '),
            (31, true, '^No upload could be fetched for this entry: ', 'Couldn''t fetch an upload: '),
            (32, false, 'The audio is not the sample Deezer publishes for this track.', 'Audio doesn''t match the Deezer preview.'),
            (33, false, 'The audio is not the track at the address this want is keyed to.', 'Audio doesn''t match the linked track.'),
            (34, false, 'The file carries a MusicBrainz recording ID, and this entry has not been resolved to a recording yet.', 'File carries a MusicBrainz recording ID.'),
            (35, false, 'The audio matches the sample Deezer publishes for this track. MusicBrainz has no recording for it.', 'Audio matches the Deezer preview. Not on MusicBrainz.'),
            (36, false, 'The audio matches the artist''s Topic upload on YouTube. MusicBrainz has no recording for it.', 'Audio matches the artist''s YouTube Topic upload. Not on MusicBrainz.'),
            (37, false, 'The audio matches the track at the address this want is keyed to.', 'Audio matches the linked track.'),
            (38, false, 'Nothing could decide whether this copy is the track the entry names.', 'No sample proves it.'),
            (39, false, ' — identified by its audio together with its own identifier', ', matched by audio and its ID'),
            (40, false, ' — identified by its audio', ', matched by audio'),
            (41, false, ' — identified by the sample its distributor publishes', ', matched by the published sample'),
            (42, false, ' — identified by a file your library already holds', ', matched by a file in your library'),
            (43, false, ' — identified by MusicBrainz recording ID', ', matched by MusicBrainz ID'),
            (44, false, ' — identified by ISRC', ', matched by ISRC'),
            (45, false, ' — identified by artist, title, and duration together', ', matched by artist, title and length'),
            (46, false, ', then taken over the clean edition MusicBrainz holds of it.', ', picked over the clean version.'),
            (47, true, ', then singled out from the recordings that fit equally by the ([a-z]+) this (file|entry) names\.', ', picked by its \1.'),
            (48, true, 'AcoustID identified the audio as (.+?), which MusicBrainz holds as another row of this same registered track\.', 'AcoustID names \1, a duplicate MusicBrainz entry for this song.'),
            (49, true, 'AcoustID identified the audio as (.+?)\. MusicBrainz marks the recording this wants as the clean edition of that track, and Schall keeps the explicit one\.', 'AcoustID names \1. MusicBrainz lists this song as its clean version.'),
            (50, false, 'AcoustID identified the audio as another MusicBrainz recording, and MusicBrainz could not be asked whether that row holds this same registered track.', 'AcoustID names another MusicBrainz song. MusicBrainz didn''t say if it''s the same.'),
            (51, false, 'MusicBrainz has the same ISRC on another recording, so it does not name this one alone', 'another song has the same ISRC'),
            (52, false, 'another recording fits this entry exactly as well', 'another song fits just as well'),
            (53, true, 'Its ([^.]+?) agree, which is not enough to identify it on its own\.', 'Matches on \1, not enough to be sure.'),
            (54, true, 'Its ([^.]+?) agree, but ', 'Matches on \1, but '),
            (55, true, 'Nothing about this recording contradicts the (file|entry), and nothing identifies it either\.', 'No evidence for or against.'),
            (56, true, '([0-9]+) recordings fit this (file|entry) equally well; the evidence does not choose between them\.', '\1 songs fit equally well. Pick one.'),
            (57, true, 'The (file|entry) carries no identifier and no title to search for, so nothing can be asked about it\.', 'No IDs or title to search by.'),
            (58, false, 'No recording MusicBrainz knows of matches this file.', 'No MusicBrainz match for this file.'),
            (59, false, 'No recording MusicBrainz knows of matches this entry.', 'No MusicBrainz match for this song.'),
            (60, false, ' It is owned music all the same.', ' Kept as your own music.'),
            (61, false, 'The file''s own identifiers disagree with what the provider knows.', 'The file''s IDs disagree with MusicBrainz.'),
            (62, false, 'The entry''s own identifiers disagree with what the provider knows.', 'The song''s IDs disagree with MusicBrainz.'),
            (63, false, 'The file''s identifiers name ', 'The file''s IDs name '),
            (64, false, 'The entry''s identifiers name ', 'The song''s IDs name '),
            (65, false, ', but the evidence does not settle it.', ', but it isn''t confirmed.'),
            (66, true, '([0-9]+) recordings could be this (file|entry); the closest is ', '\1 possible matches. Closest is '),
            (67, true, 'The closest recording is (.+?), but the evidence does not identify it conclusively\.', 'Closest match is \1. Not confirmed.'),
            (68, false, 'The MusicBrainz recording ID this file carries is not known to MusicBrainz.', 'MusicBrainz doesn''t know this file''s recording ID.'),
            (69, false, 'The MusicBrainz recording ID this entry carries is not known to MusicBrainz.', 'MusicBrainz doesn''t know this song''s recording ID.'),
            (70, false, 'Nothing else identifies it either.', 'No other match.'),
            (71, false, 'The closest recording found instead is ', 'Closest match is '),
            (72, false, 'AcoustID named this recording and another MusicBrainz row for the same audio, and the file''s own MusicBrainz recording ID names this one.', 'AcoustID named this song and a duplicate. The file''s MusicBrainz ID picks this one.'),
            (73, false, 'AcoustID named this recording and another MusicBrainz row for the same audio, and the file''s own ISRC belongs to this one alone.', 'AcoustID named this song and a duplicate. The file''s ISRC picks this one.'),
            (74, false, 'AcoustID named this recording and another MusicBrainz row for the same audio. The upload found under this recording''s name sounds the same, and the file''s title, artist and length fit this row and not the other.', 'AcoustID named this song and a duplicate. The upload found by name sounds the same, and the tags fit only this one.'),
            (75, false, 'The upload found under this recording''s name sounds the same, and the file''s title, artist and length agree.', 'The upload found by name sounds the same, and the tags agree.'),
            (76, true, 'The file resolves to (.+?), and its entry to another recording\.', 'The file is \1, not the wishlist song.'),
            (77, true, 'MusicBrainz names (.+?), which does not carry the ISRC ([^ ]+)\.', 'MusicBrainz''s \1 lacks ISRC \2.'),
            (78, false, 'The file is matched to a catalogue track, which proves its identity.', 'Matched to a catalogue track.'),
            (79, false, 'The metadata provider could not be reached, so this file has not been asked about yet.', 'MusicBrainz unreachable. Not checked yet.'),
            (80, true, '^Resolved by hand to (.+), the recording a wishlist entry asked for\.$', 'You matched it to \1, from your wishlist.'),
            (81, true, '^Resolved by hand to (.+)\.$', 'You matched it to \1.'),
            (82, true, '^Filed as (.+), the recording a wishlist entry''s copy was proven to be\.$', 'Filed as \1, proven by a wishlist copy.'),
            (83, false, 'Recorded by hand as owned music with no external identity.', 'You marked this as your own music, not on MusicBrainz.'),
            (84, false, 'The decision that identified this file was taken back, so what this file is has to be answered again.', 'The match this rested on was undone. Needs matching again.'),
            (85, false, 'MusicBrainz links this recording to no page with a preview that fits it.', 'No linked preview fits.'),
            (86, false, 'A page MusicBrainz links this recording to could not be read: ', 'Couldn''t read a linked page: ')
        ) AS rules (position, is_regex, old_text, new_text) ORDER BY position LOOP
            IF rule.is_regex THEN
                EXECUTE format(
                    'UPDATE %1$I SET %2$I = regexp_replace(%2$I, $1, $2, ''g'') WHERE %2$I ~ $1',
                    target.table_name, target.column_name)
                USING rule.old_text, rule.new_text;
            ELSE
                EXECUTE format(
                    'UPDATE %1$I SET %2$I = replace(%2$I, $1, $2) WHERE strpos(%2$I, $1) > 0',
                    target.table_name, target.column_name)
                USING rule.old_text, rule.new_text;
            END IF;
        END LOOP;
    END LOOP;
END
$$;
-- +goose StatementEnd

-- +goose Down
-- The old wording is not restored.

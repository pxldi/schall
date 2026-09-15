# 0013 — A confirmed removal deletes the playlist entry

**Status:** accepted, 2026-08-02. Says what 0004's "deliberate removal" does,
once 0010 and 0012 have established that it is one.

## Context

0004 makes a track present in the snapshot and absent from Navidrome a
deliberate removal. 0010 established when that reading is allowed at all — the
id it was pushed under still reports the path it was pushed for — and 0012 said
what happens when it is not. None of the three says what *acting* on a
confirmed removal means, and there are two candidates: take the entry off the
want list, or record a suppression that leaves the entry standing and stops it
being pushed again.

The question has teeth because a want list is not only what the user curates.
It is also what an import writes: a Spotify-sourced list is reconciled against
Spotify on every import, and `PrunePlaylistEntries` deletes the entries whose
source track the remote list no longer carries.

## Decision

**A confirmed removal deletes the `playlist_entries` row the track was pushed
for.** Nothing else: the want it created stays, for the reason
`PrunePlaylistEntries` leaves one — taking a track off a playlist is not the
statement "stop trying to obtain this recording" — and the file stays in the
library, because Schall does not delete music on the strength of a playlist
edit.

On a manual list and on a Navidrome-adopted one, that is permanent. Nothing
re-adds an entry nobody re-adds.

**On a Spotify-sourced list it is not permanent, and that is accepted.** The
next import finds the track still on the Spotify playlist and writes the entry
back, because Spotify is authoritative for the contents of its own list (0008).
The pass after that finds an acquired want with no snapshot row, pushes it, and
the track reappears in Navidrome. The user removed it; the import brought it
back.

## Why

The suppression alternative — a column saying "removed in Navidrome, do not
push again" — is the right long-term answer and is deliberately not built here.
It is a third decision store on a row that already carries an import's opinion
and a user's, it has to answer what happens when the same track is removed in
Navidrome and then re-added in Spotify, and it needs a way to be undone in an
interface that does not exist yet. Building it as part of the first sync pass
would be deciding all of that from a position where nobody has yet run a sync
against a real library.

Deleting the entry is the smaller statement and the one that is honest today:
the entry is the record of a track being on this list, and the user has just
said it is not.

What that costs is written down rather than left to be discovered. The
tug-of-war is bounded — a removal survives until the next import of that list,
and it survives forever on every list Spotify does not own — and nothing is
lost when it loses: the music is on disc, the recording is still wanted, and
the entry is a want rather than a file.

## What breaks if reversed

If a confirmed removal left the entry standing with no suppression, the next
pass would find an acquired want the snapshot no longer covers and push it
straight back. The user's removal would last until the following sync, which is
worse than losing it to an import: it would look like sync ignoring them.

If a removal deleted the want as well, one edit on a phone would cancel the
pursuit of a recording that sits on four other playlists.

## What is not decided here

The suppression column. It is named as the fix for the tug-of-war and belongs
with the work that gives it an interface — somewhere to see that a track is
suppressed and to say it no longer is — rather than with the pass that first
made the conflict reachable.

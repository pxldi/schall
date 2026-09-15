# 0008 — A playlist is a want list with a source

**Status:** accepted, 2026-07-30. The schema below is
`internal/migrations/00026_playlists.sql`, which also carries the
`spotify_settings` row the credentials section describes.

## Context

Roadmap 3 imports Spotify playlists. The objects it needs already half exist:
`acquisition_targets` (0006) is the want, and its migration says outright that
"a playlist reference joins this when playlists exist". What does not exist is
the list itself, or anything that remembers which remote track became which
want.

Three things make this more than a table with rows in it.

A playlist and a want have different lifetimes. Removing a track from a Spotify
playlist is not the same statement as "stop trying to obtain this recording",
and the same recording can sit on five playlists while being one want.

An entry is evidence, and evidence is what a re-resolution runs on. 0006 keeps
the entry text verbatim on the target for exactly that reason.

And Spotify is a metadata source that will be wrong sometimes. Its titles carry
"- Remastered 2011", its durations differ across masters, and its ISRC coverage
is not total. 0007 widened what the grader is willing to *ask* about because of
this; nothing here may widen what it is willing to *conclude*.

## Decision

**Three tables**, in one migration.

`playlists` — the want list. `source` ('spotify' | 'manual'), `source_id`,
`source_revision`, `name`, `description`, `owner_name`, `track_count`,
`imported_at`, unique on `(source, source_id)` where `source_id` is not null.

`playlist_entries` — one row per track, in order. `playlist_id`, `position`,
`source_track_id`, and the same verbatim evidence a target holds:
`entry_artist`, `entry_title`, `entry_album`, `entry_duration_ms`, `entry_isrc`.
Unique on `(playlist_id, source_track_id)`.

`playlist_entry_targets` — the join 0006 anticipated. Many-to-many in both
directions: an entry outlives a superseded target, and one target legitimately
serves the same recording on several playlists.

`source_revision` holds what Spotify calls `snapshot_id`. It is deliberately
not named "snapshot": 0004 uses that word for the last state pushed to
Navidrome, and the two must never be read as the same thing.

**Entry evidence is duplicated onto the target, not referenced.** The target is
the unit a re-resolution runs on, and it must not go blank because a playlist
was deleted.

**Authorization Code with a stored refresh token**, client ID and secret in a
settings row as the slskd key already is, callback served by the API. Client
Credentials needs no redirect round-trip but reads only public playlists — not
the user's own private ones, and not Liked Songs, which is most of the point.

**Idempotency on `source_revision`.** Unchanged revision means the import is a
no-op costing one request. Changed means entries are reconciled by
`source_track_id`; an entry that disappeared drops its join row and never
touches the target.

**Per entry:** already owned — the recording is mapped to a present file — is
recorded as owned and creates nothing. Everything else becomes an acquisition
target with `origin = 'playlist'`, which the existing sweep resolves through B.
No new matching rules and no new decision stores.

**No new dependency.** `net/http` and `encoding/json`, as `internal/musicbrainz`
does. One token refresh does not justify an OAuth library.

## Why

The join table rather than a column on either side is the whole design in one
choice. A `playlist_entry_id` on the target would make the want a property of
the list that happened to mention it first, and deleting that list would either
orphan the want or delete music the user still wants. A `target_id` on the entry
would break the moment two entries resolve to one recording and one supersedes
the other — the case 0006 already handles by pointing the loser at the survivor.

Evidence duplication looks like denormalisation and is not. The entry text is
not a description of the playlist row; it is the input to a decision, and 0006
stores it on the target because a target must be able to be re-resolved years
later, from a source that may no longer exist, without changing what it was
originally asked about.

Ownership is checked before a target is created rather than after, because
creating a want for music already in the library and then resolving it away is
several rate-limited MusicBrainz requests spent to reach the answer we started
with.

## What breaks if reversed

If a Spotify removal deletes the want, the user loses the record of wanting
music by editing a playlist — the same quiet, cumulative, unrecoverable failure
0004 exists to prevent, arriving from the other direction.

If the entry evidence lives only on the playlist row, a deleted playlist leaves
targets that cannot be re-resolved and cannot say what they were ever about.

If import is made idempotent on entry text rather than `source_track_id`, a
track that Spotify re-titles becomes a second entry and a second want for music
already being pursued.

## What is not decided here

The ⚠ RISK in the roadmap is still open: **the grade rules have not been run
against a real playlist**. 0007 proved the refusals behave, using a synthetic
corpus of streaming-shaped titles. It did not measure the *rate* — if most of a
real playlist lands in review, the queue floods and reflexive-accept destroys
the guarantee this project exists to provide.

That measurement comes before ingestion code, not after, and it can invalidate
parts of this ADR. It needs one real playlist and nothing else.

Spotify's own algorithmic and editorial playlists (Discover Weekly, the Daily
Mixes, the editorial charts) are unreachable to an app in development mode,
which a self-hosted install permanently is. Only user-created playlists are in
scope, and no amount of design here changes that.

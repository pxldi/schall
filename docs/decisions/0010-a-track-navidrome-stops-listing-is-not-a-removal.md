# 0010 — A track Navidrome stops listing is not a track the user removed

**Status:** accepted, 2026-08-01. Completed by 0012, 2026-08-02: a pairing this
finds broken is repaired from the path inside the sync pass, or left alone.

## Context

0004 makes playlist sync a three-way merge over a stored snapshot, and its
first rule is that a track in the snapshot which Navidrome no longer lists was
deliberately removed. Roadmap item 6 named the identity model that rule rests
on as one of the three riskiest assumptions in the design, and asked for a
prototype before anything downstream was built.

The prototype ran against Navidrome 0.63.2 — the deployed version — over three
tagged FLAC files, a playlist created through the Subsonic API, and a library
changed underneath it between scans. What it found:

- **A media file's id survives more than expected.** An incremental rescan, a
  full rescan, a rename in place and a move to a different folder all keep the
  same id. Navidrome matches a scanned file to an existing row by path first
  and by `pid` — a hash over tag values, by default the MusicBrainz track id or
  else album, disc, track and title — second, so either key surviving is
  enough.
- **Rewriting tags in place also keeps the id**, because the path still
  matches. The `pid` changes and the id does not.
- **Moving a file and rewriting its tags between two scans breaks it.** Neither
  key matches, the file enters as a new row under a new id, and the old row
  stays behind with `missing = 1`.
- **A broken track leaves the playlist without a trace over the API.**
  `getPlaylist` stopped listing it and `songCount` fell from 3 to 2, while
  `playlist_tracks` still held the id at its original position.
- **`getSong` on the broken id answers `ok` and returns the song.** There is no
  `missing` anywhere in the Subsonic response. So the two cases 0004 has to
  tell apart — the user removed this track, and this track's identity broke —
  produce identical evidence: an id that still resolves, an entry that is no
  longer listed.
- **The absence is recoverable, until it is not.** Restoring the file to its
  old path and tags un-marks the row, and the entry reappears at the position
  it held. With `Scanner.PurgeMissing` set to `always` the same break deletes
  the old row, and Navidrome deletes the playlist row with it: the remaining
  tracks close the gap and nothing brings the entry back.
- **The path a song reports is not its path.** By default Navidrome answers
  with a display path composed from the album's first folder and the track's
  own metadata, which is right only while the file happens to be named that
  way — after a move it kept reporting a path the file had not occupied for
  two scans. Real paths need `Subsonic.DefaultReportRealPath`, and that setting
  is copied onto a *player* row the first time a client calls, so turning it on
  does nothing for a client that has called before.

## Decision

**An entry's absence is read as a removal only when the id it was pushed under
still points at the file it was pushed for.** The snapshot stores, per pushed
track, both the Navidrome id and the library path Schall pushed it at. Every
absent entry is then checked with `getSong`:

- The id resolves and reports the real path Schall pushed → the user removed
  it. Act on it, against the acquired subset only, as 0004 says.
- The id resolves and reports a path Schall no longer holds, or does not
  resolve at all (error 70) → the identity broke. **Not a removal.** The track
  stays in the Schall playlist and the pairing is re-established from the file
  rather than from the id.

Reading a real path is a precondition, not a preference: sync refuses to run
against a Navidrome that reports display paths rather than falling back to
them, because a display path can equal the real one and later stop equalling
it, which is the shape of a wrong answer that looks right in testing.

**Two deployment contracts, beside the read-only mount:**

- `Scanner.PurgeMissing` stays `never`. It is the whole difference between an
  identity break that heals and one that silently deletes the user's playlist.
- `Subsonic.DefaultReportRealPath` is `true` *before* the client that reads
  paths first calls. The flag is copied onto a player row at its creation and
  not re-read afterwards, and the rescan trigger has already created one under
  `c=schall`, so sync introduces itself under its own client name rather than
  inheriting a row that predates the setting.

Neither is applied to the deployment yet. They belong with the sync slice, not
before it: today nothing reads a path back, and `never` is already the default.

**And one contract Schall keeps itself:** it never moves a file and rewrites
its tags without a scan between the two. Schall owns its tree and writes no
tags today, so the break is not reachable from here; roadmap 8's layout
migration is where it becomes reachable, and it is what has to keep this.

## Why

The rule 0004 wrote down is the correct rule and it was checkable in the wrong
place. Absence in the current listing is a statement about what Navidrome can
show, and Navidrome stops showing a track for two unrelated reasons. The id is
the thing that was actually agreed with the player, so asking whether the id
still means what it meant is the question that separates them — and it is a
question the API can answer, once real paths are readable.

Nothing here is a similarity judgement or a threshold. Either the path read
back is the path Schall pushed or it is not, and the ambiguous case is resolved
by refusing to act rather than by guessing which of the two it was.

## What breaks if reversed

If absence alone is read as removal, one library reorganisation empties the
user's playlists of everything it touched — quietly, in the direction that
destroys data, and with the acquired tracks going first because they are the
only ones that were ever pushed. The music would still be on disk and the want
list would no longer mention it.

If `Scanner.PurgeMissing` is set to `always`, the recovery this rests on is
gone: the old row and its playlist entry are deleted together during the scan,
before any sync gets to look, and no later scan restores either.

If sync reads display paths, it agrees with itself for as long as every file is
named after its own metadata and sits in its album's first folder, and starts
mis-joining on the first file that is not — which is to say, on the first
release Schall lays out differently or the first album whose files land in two
folders.

## What is not decided here

How the pairing is re-established after a break. Re-resolving the file to its
new id is the obvious move and the snapshot has what it needs for it; whether
that happens inside the sync pass or as a repair the user confirms is a
question for the sync slice, and it is a question about work, not about
correctness — nothing is removed either way.

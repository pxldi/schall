# 0014 — A move Schall makes carries the row

**Status:** accepted, 2026-08-02

## Context

`library_files.path` is the identity. It is `UNIQUE`, and both scanner paths
find a file by `WHERE path = $1`. Nothing anywhere deletes a `library_files`
row: a file that disappears has `missing_at` set and keeps everything attached
to it, and a file that appears at a path nobody has seen is a fresh
`INSERT ... ON CONFLICT (path)` with a new identifier and nothing attached.

So a file that moves is not one row changing. It is a dead row keeping the
history and a live row starting from zero, and the consequences differ in
severity:

- **A manual identity is discarded.** `library_file_identities` is
  `UNIQUE (library_file_id)` on the dead row; the new row starts
  `resolution_status = 'pending'` and is resolved again from nothing. For an
  automatic identity that is a wasted provider round trip. For `is_manual` it is
  a decision the user made being thrown away by a rename, which contradicts
  *manual decisions always win and are permanent*.
- **A manual mapping becomes a conflict rather than a loss.** `ReconcileFile`
  clears a missing file's mappings only `WHERE NOT is_manual`, so the dead row
  keeps holding `track_mappings.UNIQUE (track_id)` and the live row cannot be
  matched to that track at all.
- **Automatic mappings are re-derived**, which is cost rather than loss.
- **Provenance is orphaned**: `downloads.library_file_id` and
  `downloads.local_path`, `acquisition_targets.acquired_library_file_id`,
  `acquisition_target_files.library_file_id`,
  `download_requests.import_path`.

Roadmap 8 asks for a configurable layout and a layout change as a supported
migration, and roadmap 8a's remaining half — a better copy replacing a worse one
— cannot land until a file can change place without losing what is attached to
it. Both were blocked on what the issue called "path-continuity design
(content- or identity-keyed re-link)".

## Decision

**A move Schall performs updates the row. Nothing is re-linked, because nothing
was ever unlinked.**

```
BEGIN
  UPDATE library_files SET path = $new WHERE id = $old
  UPDATE downloads SET local_path = $new WHERE local_path = $old
  rename(old, new)
COMMIT
```

The identifier does not change, so the identity, the mappings, the provenance
and the acquisition links are not carried across — they were never disturbed.
The scanner then finds the file where it now is, and `touchUnchanged` reports it
unchanged on size and mtime.

`path` is a lookup key and not a foreign key: it appears in five statements
outside the migrations. `navidrome_playlist_snapshot_tracks.library_path` is the
one that deliberately does **not** follow, because 0010 records it verbatim as
what was agreed at push time.

**Re-linking a file somebody else moved is out of scope**, and is a separate
question rather than a deferred part of this one. Both candidate mechanisms have
a defect worth writing down before anyone reaches for them again:

- A hash over the file changes when a tag is edited — the common case it would
  have to survive.
- Re-linking by resolved recording ID cannot tell two copies of the same
  recording apart, and would therefore have to guess between them. That is
  precisely the guess the governing rule forbids.

A file that vanishes and one that appears elsewhere stay what they are today: a
missing row and a new one.

**A layout migration is journalled, not transactional.** Moving a library is
tens of thousands of renames. `library_file_moves` records a row before its
rename and settles it after, so an interrupted run leaves a library that can be
described — every file is either where it was or where it was going, and which
is which is written down — rather than one silently in two layouts. `from_path`
is also the whole undo instruction, which is why there is no separate table of
runs.

**The default template is what the importers already build.** There is no column
default in `library_layout_settings`; an absent row means `library.DefaultTemplate`,
so the answer has one home. An installation that never opens the setting keeps
filing music where it always has, and a first migration run against an untouched
template moves nothing.

## Consequences

The `Done when:` of roadmap 8 is met by construction rather than by a heuristic:
not *usually* zero lost mappings, but zero, because there is nothing to lose.

Replacement — 8a's remaining half — is the same operation plus deleting the
loser's bytes. The loser is marked `missing_at` as any vanished file is, never
deleted, because deleting the row would cascade its identity and mappings away.
Where the loser holds a **manual** mapping, the replacement inherits it: the
decision was about the music, and the replacement is the same music proven by
the same audio rather than assumed to be. This is the one place a decision a
person made moves to a file they did not decide about, and it is deliberate —
the alternative is a hand-matched track that can never be upgraded.

The layout still decides nothing. No part of matching or identity reads a path;
naming a file after a song has never made it one, and moving it does not stop it
being one.

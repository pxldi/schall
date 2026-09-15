# 0004 — Playlist sync is a three-way merge over a stored snapshot

**Status:** accepted, 2026-07-29. Qualified by 0010, 2026-08-01: absence from
Navidrome is only evidence of removal once the id it was pushed under is shown
still to mean the same file.

## Context

A Schall playlist is a want list and may contain unacquired tracks; a
Navidrome playlist can only contain files that exist. A 50-track want list
with 30 acquired appears in Navidrome as 30 tracks. Two-way sync between
these unequal objects has no way to tell "the user removed this in
Navidrome" from "this was never pushed because it isn't acquired yet".

## Decision

Each playlist stores a snapshot of its **last pushed state**. Every sync is a
three-way comparison: snapshot vs. current Navidrome state vs. current Schall
state.

- In the snapshot, absent from Navidrome now → deliberate removal, but only
  once 0010's check has ruled out the other reason a track stops being listed.
- Never pushed (unacquired) → not a removal; untouched by definition.
- Navidrome-side removals only ever affect the acquired subset, never an
  unacquired want.
- Navidrome-side additions are added to the Schall playlist; Navidrome-created
  playlists can be adopted.

## Why

The snapshot is the only witness to what Navidrome was actually told. Without
it, every diff against Navidrome's current state is ambiguous for exactly the
tracks the user is still waiting on.

## What breaks if reversed

A snapshotless two-way sync reads every not-yet-acquired want as a
Navidrome-side deletion and removes it from the want list — silently deleting
precisely the music the user left streaming to obtain. This failure is quiet,
cumulative, and unrecoverable without an audit trail.

# 0002 — "Could not verify" is not "verified"

**Status:** accepted, 2026-07-29

## Context

AcoustID has never heard much of the music this tool exists for — obscure,
self-released, live recordings. When fingerprint verification cannot run, or
returns nothing, something must decide whether the file proceeds.

## Decision

Verification absence is handled by source:

- **slskd-sourced files fail closed.** No AcoustID identification of the
  audio → the file routes to the review queue. Tags alone never admit a
  downloaded file.
- **User-supplied files fail open** to the tag-conclusive matching rules.
  The user's own provenance is evidence, and AcoustID coverage is worst
  exactly for the music imported by hand.

In both cases the absence is recorded — a check that could not run says why,
and is never written down as a pass.

## Why

A peer's file carries no trust: consistently wrong tags on the wrong audio
pass every tag check, and the fingerprint is the only independent witness.
Requiring that witness for the user's own rips would instead hold their
legitimately owned music hostage to a database that has never heard it.

## What breaks if reversed

Fail-open for downloads re-opens the one corruption path tag-matching cannot
close (see 0001). Fail-closed for user files floods the review queue with the
user's own correct music, teaching them to accept reflexively — which kills
the zero-false-positive guarantee everywhere else too.

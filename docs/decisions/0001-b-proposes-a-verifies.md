# 0001 — B proposes, A verifies

**Status:** accepted, 2026-07-29

## Context

Two matching problems exist: resolving a playlist entry (text/ISRC, no file)
to a MusicBrainz recording ID (B), and identifying what a file is from its
audio and tags (A). It is tempting to treat a downloaded file's tags agreeing
with the search terms as success, or to let B's resolution stand once a
download "looks right".

## Decision

They compose in one direction only. B emits a concrete recording ID — never a
search string — which becomes the acquisition target. After every download, A
runs on the file independently and its result is compared to B's target.
Equal → accept. Not equal → discard and try the next candidate. Neither side
may certify the other's job.

## Why

B works from the weakest evidence in the system (streaming-service text). A
wrong B resolution followed by a faithful download is a wrong file with
perfectly consistent tags — the one error tag-checking cannot catch.
Independent verification against the audio is the only gate that stops a
wrong proposal from reaching the library.

## What breaks if reversed

Accepting downloads on tag-vs-search-term agreement lets every mistagged or
mislabeled Soulseek file through as long as it is *consistently* wrong.
Letting B's output be a search phrase instead of an ID makes verification
impossible — there is nothing concrete to verify against — and the
zero-false-positive rule becomes unenforceable exactly where evidence is
weakest.

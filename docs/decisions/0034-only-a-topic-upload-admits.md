# 0034 — Only an artist's Topic upload admits a copy

**Status:** accepted, 2026-09-09, by the owner.

## Context

0029 lets a want with no ISRC use a YouTube upload found by name and length as
its audio anchor. The search can return a cover or another version with the
same title and length. The old rule let any such upload admit a matching copy.

YouTube generates an artist's Topic channel from the distributor's delivery.
Its channel name ends in ` - Topic`, with the artist before the suffix. That
channel is a stronger source for the registered recording than an arbitrary
uploader.

## Decision

An upload is a Topic upload only when its trimmed channel name ends in ` -
Topic`, case-insensitive, and the channel name without that suffix agrees with
the want's artist under `tagmatch.Credit`. A different artist's Topic channel
does not qualify.

Among uploads that pass 0029's existing title and length gates, a Topic upload
wins over every other upload. Views still choose among uploads of the same kind.

The stored source is `youtube-topic` for a Topic upload and `youtube` for every
other name-found upload. `youtube` leaves an agreeing copy as a question and
never refuses on disagreement. `youtube-topic` keeps 0029's admission and
never-refuses behavior. Other evidence and the contradiction rule are
unchanged.

Existing anchors with source `youtube` keep that source. They do not admit new
copies after this change. Schall does not backfill them.

## Consequences

Non-Topic uploads remain visible as evidence with their title, channel and
views, but a person decides whether an agreeing copy is the recording. An
artist's Topic upload can admit the same copies that the old name-found rule
could admit. This change admits nothing it could not admit before.

The search can still choose a wrong sibling when no Topic upload fits. It stays
a question or a non-refusing anchor according to its stored source.

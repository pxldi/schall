# 0035 — A standing want on a followed artist, and main releases by default

**Status:** accepted, 2026-09-10, by the owner.

## Context

Following an artist saves their release groups and queues one
`refresh_album_metadata` job per release. Those land one at a time behind
MusicBrainz's rate limit. For the first hours after a follow, most releases have
no tracklist.

"Want N missing" walked the tracks that existed. On the live instance the artist
yaego showed "0 of 22 main releases owned" beside "Want 13 missing": 9 releases
had no tracklist yet, so the press wanted 13 and skipped the other 9 in silence.
The person had to come back later and press again, once per artist.

Following also defaulted to the monitor level `everything`, which counts every
live album, compilation, remix and DJ mix MusicBrainz credits to the artist. The
label default has been `main` since migration 00070.

## Decision

An artist carries `want_missing_since` (migration 00099). NULL means off.

While it is set, Schall wants every missing track of every monitored release of
that artist. Two places do it, and they run the same walk
(`acquisition.WantMissing`):

- setting it walks the discography now, exactly as the one-off press did;
- saving a tracklist walks that release, if the artist has the mark and the
  release is monitored under their level.

The release rule is the one the discography counts by, so the header, the chips
and the standing want agree about which releases count. At `owned` both halves
want nothing.

Clearing it stops future wanting only. Every want made under it stays, because
those were asked for. This is the rule changing the monitor level already
follows.

New follows default to the monitor level `main`. Existing rows keep the level
they have.

## Consequences

An artist followed with "Want what's missing" needs one press. The releases whose
tracklists arrive over the following hours are wanted as they land, and a
release group MusicBrainz adds later goes through the same album refresh, so
there is no second path.

The album refresh job wants before it completes. A failure there retries the job
rather than losing the wants; the MusicBrainz fetch, the catalogue save and want
creation are all idempotent, so the retry costs one request.

This admits nothing. Wants are created, and every copy the loop fetches for them
is still proven by its audio before it enters the library.

A person following an artist now watches main releases unless they pick another
level. Compilations, live albums and remixes stay browsable and wantable by
hand, and stop counting as missing.

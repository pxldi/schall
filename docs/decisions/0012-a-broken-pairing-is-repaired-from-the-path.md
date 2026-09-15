# 0012 — A broken pairing is repaired from the path, or left alone

**Status:** accepted, 2026-08-02. Completes the question 0010 left open.

## Context

0010 established how sync tells the two reasons a track stops being listed
apart: the id it was pushed under is asked what file it points at now, and only
an id that still reports the path Schall pushed it for is read as a deliberate
removal. Everything else is an identity break.

It stopped there deliberately. What to do about a break was named as a question
about work rather than about correctness — nothing is removed either way — and
left to the slice that would have to answer it. This is that slice.

## Decision

**A break is repaired inside the sync pass, from the path, or it is left
alone.** When an entry's pushed id no longer means the file it was pushed for,
sync looks the file up by its exact library path:

- Exactly one song reports exactly that path → the snapshot's id is rewritten
  to that song. Nothing is removed, nothing is asked, and the next pass sees an
  ordinary pairing.
- Nothing reports that path, or what comes back is not an exact match → the
  pairing is left broken. The track stays in the Schall playlist, the snapshot
  keeps the id it had, and the pass reports it.

"Exact" is the whole of the rule. Not the nearest path, not the same basename
in another folder, not the same path in another case. A song is the file or it
is not one, and the second answer is a refusal rather than a second-best.

Reading a real path is what makes the question answerable at all, which is why
0010's `Subsonic.DefaultReportRealPath` contract is a precondition of sync
rather than a preference: against a server reporting display paths the lookup
finds nothing, every break stays broken, and sync says so instead of falling
back to a comparison it cannot trust.

**The two sides may name the same folder differently, and that is configured
rather than inferred.** Schall and Navidrome read one directory through their
own mounts — the deployment this was written against has the library at
`/music` in Schall and `/music-schall` in Navidrome — so comparing absolute
paths across the boundary would agree with nothing, ever. `SCHALL_NAVIDROME_PATH_MAP`
names the mounts as `local=remote` pairs, applied longest-prefix-first and only
where the prefix ends on a separator, so `/music` never claims a file under
`/music-extra`. The comparison itself is unchanged: exact string equality, made
after both sides have been put in the same terms. A path under no configured
prefix passes through untouched, an unset map is the identity, and a mapping
that does not apply produces no match rather than a looser one. The snapshot
stores the Schall path — the path Schall wrote and owns — because a stored
remote path would silently become wrong the day a mount is renamed.

## Why

The alternative was to surface a break as something a person confirms, in the
shape of the review queue. It was rejected because the question it would ask is
not a question. A review item exists where evidence ran out and a person knows
something the machine does not; here the evidence is complete — this is the
file at this path, and Navidrome is reporting that path for that song — and the
only honest answer a person could give is yes. A queue of those trains the
reflex the review queue exists to avoid, and it would arrive by the hundred
after a single library reorganisation.

Nothing here resolves an ambiguity. The repair concludes on an exact string
equality between a path Schall wrote and a path Navidrome read off the disc,
and the ambiguous case — no match, or an inexact one — is answered by doing
nothing rather than by choosing.

## What breaks if reversed

If a break were repaired on a near match instead of an exact one, sync would
re-pair a playlist entry to a different file that happened to be named
similarly, and every later removal check would then be asking about the wrong
song. The failure would be silent and would look exactly like success.

If a break were left permanently broken with no repair at all, a library
reorganisation would freeze every affected entry: still in the Schall playlist,
never pushed again, never removable, and never reported as anything but absent.

## Consequences

**A playlist deleted in Navidrome is created again by a later pass.** The same
reasoning that makes a break healable makes this one unavoidable. When
`getPlaylist` answers not-found for the id the snapshot holds, everything the
snapshot describes is gone, so nothing in it can be read as a removal: the
snapshot is dropped, the want list is untouched — leaving Navidrome is not the
statement "stop wanting this music" — and the pass ends without recreating
anything. What the next pass then sees is a want list with no snapshot, which
is exactly the state a list that has never been pushed is in, and it pushes it.

That is accepted rather than worked around. The alternative is a record of
"this list was deleted on purpose", which is a second decision store keyed on
an object that no longer exists, and which would have to be told apart from a
Navidrome restored from a backup. Deleting the *Schall* playlist is the way to
stop it, and nothing is lost either way: the music is on disc and the entries
are wants.

## What is not decided here

Whether a repair that could not be made should eventually become a report
somebody acts on rather than a line in a sync result. It is a question about
how much noise a full reorganisation makes, and it is answerable only once one
has been run.

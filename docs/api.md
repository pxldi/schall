# API and behaviour

Reference for the HTTP surface and the rules behind the decisions Schall makes.
See the [README](../README.md) for setup.

## API

Every route the server registers, in the order `internal/server/api.go`
registers them:

- `GET /api/v1/events` — server-sent events; invalidations only, never state
- `GET /api/v1/health`
- `GET /api/v1/me` — the actor and how the request was authenticated
- `GET /api/v1/dashboard`
- `GET /api/v1/search/artists?q=...`
- `GET /api/v1/artists`
- `POST /api/v1/artists`
- `GET /api/v1/artists/{id}`
- `POST /api/v1/artists/{id}/follow`
- `DELETE /api/v1/artists/{id}/follow`
- `POST /api/v1/artists/{id}/refresh`
- `GET /api/v1/albums`
- `GET /api/v1/albums/{id}`
- `GET /api/v1/albums/{id}/tracks`
- `GET /api/v1/albums/{id}/editions`
- `POST /api/v1/albums/{id}/edition`
- `GET /api/v1/albums/{id}/sources`
- `POST /api/v1/acquisition-targets`
- `GET /api/v1/acquisition-targets`
- `GET /api/v1/acquisition-targets/{targetId}`
- `POST /api/v1/acquisition-targets/{targetId}/not-wanted`
- `DELETE /api/v1/acquisition-targets/{targetId}/not-wanted`
- `POST /api/v1/source-searches`
- `GET /api/v1/source-searches/{runId}`
- `POST /api/v1/source-searches/{runId}/cancel`
- `GET /api/v1/albums/{id}/duplicates`
- `GET /api/v1/albums/{id}/downloads`
- `POST /api/v1/albums/{id}/downloads`
- `GET /api/v1/downloads`
- `GET /api/v1/downloads/{id}/source`
- `POST /api/v1/downloads/{id}/start`
- `POST /api/v1/downloads/{id}/retry`
- `POST /api/v1/downloads/{id}/revalidate`
- `POST /api/v1/downloads/{id}/resolutions`
- `DELETE /api/v1/downloads/{id}/resolutions/{decisionId}`
- `DELETE /api/v1/downloads/{id}`
- `GET /api/v1/playlists`
- `POST /api/v1/playlists`
- `GET /api/v1/playlists/{playlistId}`
- `POST /api/v1/playlists/{playlistId}/import`
- `POST /api/v1/playlists/{playlistId}/entries/{entryId}/wanted` — want one entry that has no want yet
- `DELETE /api/v1/playlists/{playlistId}`
- `GET /api/v1/settings/phones` — the phones that hold a token
- `POST /api/v1/settings/phones` — mints one; the plain token is in this answer and nowhere else
- `DELETE /api/v1/settings/phones/{tokenId}` — revokes one
- `GET /api/v1/settings/spotify`
- `PUT /api/v1/settings/spotify`
- `POST /api/v1/settings/spotify/authorize`
- `GET /api/v1/spotify/callback`
- `GET /api/v1/settings/slskd`
- `PUT /api/v1/settings/slskd`
- `POST /api/v1/settings/slskd/test`
- `GET /api/v1/settings/navidrome`
- `PUT /api/v1/settings/navidrome`
- `POST /api/v1/settings/navidrome/test`
- `GET /api/v1/settings/imports`
- `PUT /api/v1/settings/imports`
- `GET /api/v1/inbox/cleanups` — the last ten passes over the download inbox
- `POST /api/v1/inbox/cleanups` — `{"dryRun": true}` counts, `false` deletes
- `GET /api/v1/inbox/cleanups/{cleanupId}` — one pass with its counts
- `GET /api/v1/library`
- `GET /api/v1/library/roots`
- `POST /api/v1/library/roots`
- `GET /api/v1/library/files?status=ambiguous&resolution=needs_review&q=...&limit=25&offset=0`
- `GET /api/v1/library/files/{id}/matches`
- `GET /api/v1/library/files/{id}/tracks?q=...`
- `POST /api/v1/library/files/{id}/match`
- `DELETE /api/v1/library/files/{id}/match`
- `POST /api/v1/library/files/{id}/reconcile`
- `GET /api/v1/library/files/{id}/identity`
- `POST /api/v1/library/files/{id}/identity`
- `DELETE /api/v1/library/files/{id}/identity`
- `POST /api/v1/library/files/{id}/resolve`
- `POST /api/v1/library/scan`
- `POST /api/v1/scan`

Search for the intended artist, then submit the selected MusicBrainz identity:

```sh
curl 'http://localhost:8080/api/v1/search/artists?q=Massive%20Attack'

curl -X POST http://localhost:8080/api/v1/artists \
  -H 'content-type: application/json' \
  -d '{
    "musicbrainzId":"10adbe5c-5328-4f8c-8a5c-5e5f6d4d6e89",
    "name":"Massive Attack",
    "sortName":"Massive Attack"
  }'
```

The MusicBrainz ID, canonical name, and sort name are required. Following an
artist also queues its initial metadata refresh. Set
`SCHALL_MUSICBRAINZ_USER_AGENT` to a meaningful application identifier with
maintainer contact information when publishing a customized build.

List the discovered release groups for every followed artist, or filter them
to one artist:

```sh
curl http://localhost:8080/api/v1/albums
curl 'http://localhost:8080/api/v1/albums?artistId=ARTIST_UUID'
```

Queue a metadata refresh from the API:

```sh
curl -X POST http://localhost:8080/api/v1/artists/ARTIST_UUID/refresh
```

If that artist already has a queued or running refresh, Schall returns the
existing job instead of creating duplicate work.

Artist refreshes enqueue bounded per-release jobs. For each release group,
Schall deterministically selects the earliest official edition (falling back
to the earliest available edition), stores that choice, and reuses it on later
refreshes. Release details explain the choice, expose imported track metadata,
and allow selecting a different MusicBrainz edition. Manual edition choices
are retained across later refreshes.

Configure and scan a local music folder:

```sh
curl -X POST http://localhost:8080/api/v1/library/roots \
  -H 'content-type: application/json' \
  -d '{"path":"/music"}'

curl -X POST http://localhost:8080/api/v1/library/scan
curl http://localhost:8080/api/v1/library
curl http://localhost:8080/api/v1/library/files
```

Search a release for sources, record the folder you chose, and start it:

```sh
curl 'http://localhost:8080/api/v1/albums/ALBUM_UUID/sources'
curl -X POST http://localhost:8080/api/v1/albums/ALBUM_UUID/downloads \
  -H 'content-type: application/json' -d @candidate.json
curl -X POST http://localhost:8080/api/v1/downloads/REQUEST_UUID/start
```

Searching one release at a time is the slow part, so several can be searched in
one run. A run records candidates and chooses none of them:

```sh
curl -X POST http://localhost:8080/api/v1/source-searches \
  -H 'content-type: application/json' \
  -d '{"albumIds":["ALBUM_UUID","ANOTHER_UUID"]}'
curl 'http://localhost:8080/api/v1/source-searches/RUN_UUID'
```

The run reports `pending` until every release has been searched, and each item
carries its own `status`: `found`, `none` when no peer was sharing it, `failed`
when the provider could not be searched at all, or `cancelled` when the run was
stopped before reaching it — these are kept apart because they are different
answers, and only some of them are worth trying again. Requesting a download from
a run is still `POST /api/v1/albums/{id}/downloads`, one release at a time. A run
is capped at fifty releases and is refused outright when no provider is enabled,
rather than queueing fifty searches that cannot happen.

Every candidate carries two independent judgements, and they answer different
questions. `score` is how good a copy is — format, completeness, the peer's
queue and speed — and knows nothing about which release the files hold. `match`
is the evidence that they hold the release that was searched for:

```json
{"checked": true, "expected": 12, "confirmed": 12, "partial": 0,
 "absent": 0, "complete": true,
 "summary": "All 12 tracks confirmed by name and length"}
```

A track counts as `confirmed` only when a file carries its name *and* runs to
its catalogue length, and each file may stand for at most one track, so a
discography dump cannot corroborate against an album inside it. `checked` is
false when the catalogue had no tracks to compare against, which is not evidence
against the candidate. Only `complete` means the release is accounted for.

A run that is no longer wanted can be stopped:

```sh
curl -X POST http://localhost:8080/api/v1/source-searches/RUN_UUID/cancel
```

This matters because a run's jobs are claimed one at a time in creation order, so
a second run cannot overtake the first: correcting a run started with the wrong
`autoRequest` means getting the first one out of the way. Cancelling stops the
releases nobody has searched yet, which come back with `status: "cancelled"` and
are counted by `cancelledCount`. It withdraws nothing: a release already searched
keeps exactly the answer it had, because that is a result somebody may act on,
and a release whose job is already in flight is left to finish rather than
interrupted mid-search. A run with nothing left waiting — every release searched,
or stopped by an earlier call — is refused with `409` rather than reported as a
cancellation that cancelled nothing. Cancelling does not re-queue anything;
`GET /albums/{id}/sources` searches a stopped release on its own.

An empty search is asked a second time before `none` is recorded: Soulseek
collects replies for a bounded window, so finding nothing can mean nobody
answered in time. A provider answering `429` is backpressure rather than a
result — `GET /albums/{id}/sources` retries once and then reports `503`, and a
run's job backs off a minute rather than spending its attempts on the refusal.

A run can be given permission to record the download itself:

```sh
curl -X POST http://localhost:8080/api/v1/source-searches \
  -H 'content-type: application/json' \
  -d '{"albumIds":["ALBUM_UUID"],"autoRequest":true}'
```

`autoRequest` defaults to false and is asked per run rather than stored as a
setting, because it is permission given by somebody about to look at these
results. It acts only where `match.complete` is true — every catalogue track
confirmed by name and length — and never on the `score`, which measures copy
quality and says nothing about which release the files hold. A release the
library already holds part of is always left for review however well
corroborated, because that question is about the user's own files. Items the run
acted on carry `autoRequestedAt`, so a download nobody remembers asking for can
say where it came from. Everything else still goes through
`POST /api/v1/albums/{id}/downloads`, and starting a transfer remains
`POST /api/v1/downloads/{id}/start` either way.

A download is an acquisition, so Schall asks what the library already holds
before it records one. A release that is partly owned, or that unresolved local
files point at, is refused until the request says the question was answered:

```sh
curl 'http://localhost:8080/api/v1/albums/ALBUM_UUID/duplicates'
```

The refusal is a `409` carrying the same evidence under `duplicates`: every
local file that counts against the request, whether it is already matched to a
track of this release or merely looks like it, and a sentence for each saying
why. Repeating the request with `"acknowledgeDuplicates": true` records it,
together with the evidence exactly as it stood when the answer was given.
`POST /api/v1/downloads/{id}/start` accepts the same field and applies the same
check, because the library moves between recording a request and starting it —
which is also what holds a request recorded before duplicate protection existed
to the same rule. A request that was already answered for is never asked again.

An unresolved file is deliberately treated as evidence rather than ignored. A
file Schall could not tie to a catalogue track is not proof that the music is
missing, and downloading a second copy because its identity is unproven is the
one mistake duplicate protection exists to prevent. What counts as looking like
the release is held to the identifiers and the exact tag comparison the matcher
uses, so the question is never raised by a guess.

A request stores the evidence it was made on, so a later search that ranks
differently cannot rewrite the decision. Starting hands the folder to slskd,
which downloads into its own directory; Schall follows the progress and stops
polling once every transfer has settled. Schall then asks two separate
questions about the folder: whether each file is present, whole, and readable,
and which catalogue track each file actually is. Only a folder where every file
was identified and every track was covered is copied into the managed library
and scanned; uncertainty is shown as needing review and never counts as
ownership.

Identity is decided by graded evidence rather than by track numbers. Every file
is compared with every track of the edition, one kind of evidence at a time —
MusicBrainz recording ID, ISRC, artist, album, title, disc and track position,
and duration. A tag neither side carries is silence, never agreement. A pair is
conclusive only when an identifying combination agrees *and* nothing
contradicts it, in this order:

1. MusicBrainz recording ID
2. ISRC
3. disc and track position together with the title, when the album agrees or
   the duration corroborates the position
4. title together with a duration inside five seconds

A disagreeing recording ID, ISRC, artist, or duration makes a pair
inconclusive whatever else agrees — a recording ID that matches while the
length is a minute out describes a mistagged file, not an identified one. A
different album, position, or title is ordinary between editions and never
decides anything on its own, so a single ripped from the album that later
collected it is imported on its own merits and the tags it carries are recorded
as notes. An absent album is silence, so position and title need either an
agreeing album or an agreeing duration. Finally, a conclusive pair is only used
when the choice runs both ways: one track for that file and one file for that
track. Two files that fit one track equally well identify nothing, and neither
is imported.

A paused import carries the comparison behind it. Validation checks the whole
release in one pass, so review shows every downloaded file beside the catalogue
track it matched — its tags, its position, its length, how it was matched, and
each disagreement — together with the catalogue tracks nothing matched. A file
Schall would not identify carries the tracks it might be, best first, with what
agrees and what stands against each. The files that matched are listed as well,
because the point of review is to tell a wrong edition apart from a genuinely
bad file. The Downloads view renders this under the pause; the API returns it
as `importEvidence` on the request and as `evidence` on each entry of the
import history, so a later attempt never rewrites what an earlier one observed.

When a file is genuinely the right recording under a different title, or sits
at a position the selected edition does not use, that can be recorded per
track:

```sh
curl -X POST http://localhost:8080/api/v1/downloads/REQUEST_UUID/resolutions \
  -H 'content-type: application/json' \
  -d '{"fileName": "03.flac", "trackId": "TRACK_UUID", "note": "alternate title"}'
```

A resolution is a judgement about which track a file is, and nothing else. Only
a disputed identity can be decided: a file that is missing, the wrong size, or
unreadable has to be downloaded again. The decision is stored against what the
reviewer was shown, so it stops applying if the file later says something else,
and it names a track of the requested release only. Recording it imports
nothing — validation still decides, and every other check still runs.

A paused import can be sent back through validation once tracks were resolved,
the tags were corrected, or a different representative edition was selected:

```sh
curl -X POST http://localhost:8080/api/v1/downloads/REQUEST_UUID/revalidate
```

Retrying runs exactly the same checks. It is not a way to accept a rejected
folder: an import that is still uncertain pauses again with a new reason, and
every earlier reason is kept as import history on the request.

### What happens to the download afterwards

By default, nothing. An import copies proven audio into the managed library and
leaves the provider's folder exactly as it found it, so a release can always be
imported again from the same files. Removing them instead is a policy, set in
Settings or over the API:

```sh
curl http://localhost:8080/api/v1/settings/imports
curl -X PUT http://localhost:8080/api/v1/settings/imports \
  -H 'content-type: application/json' -d '{"sourceRetention":"delete"}'
```

Deletion happens after the import is recorded, never as part of it, because
that is the point at which the managed copy and its provenance become durable
and a removed source file could no longer be imported again. Every managed copy
is confirmed to be present and exactly the size that was validated before
anything is deleted, and only the files Schall imported are deleted — artwork
and anything else the folder holds were never validated and are not Schall' to
remove. The folder itself goes only if that left it empty.

Nothing there can fail an import that already succeeded, so the outcome is
appended to the import history rather than raised: `source_removed` with what
was deleted, or `source_retained` with the reason nothing was.

The policy is refused rather than stored when Schall could not carry it out. By
default the completed-download folder is mounted read-only — Compose mounts it
that way, and validation is never allowed to mutate source files — so `delete`
is rejected with the reason until that mount is made writable. Under Compose
that means setting `SCHALL_DOWNLOADS_MODE=rw`. Schall decides
this by writing a probe file and removing it again, because a read-only mount
grants the permission bits and refuses the write anyway. That probe is the only
thing Schall ever writes to the provider's folder.

### Cleaning what the inbox already holds

The retention policy applies to imports from now on. What the read-only years
left behind is deleted by a pass of its own, under the rule in
docs/decisions/0036:

```sh
curl -X POST http://localhost:8080/api/v1/inbox/cleanups \
  -H 'content-type: application/json' -d '{"dryRun":true}'
curl http://localhost:8080/api/v1/inbox/cleanups
```

A dry run classifies every file in the inbox and deletes none of them; sending
`"dryRun": false` deletes the four classes marked `deletes` in the answer. A
file is kept when it is a held copy of a want still being looked for, when it
belongs to an open download request, or when it changed in the last 24 hours.
Every file the pass looked at is recorded with its class before anything is
unlinked, and the rule is asked again for each file at the moment it would go.
One pass runs at a time and a second is refused with 409. A pass over an inbox
that overlaps a music folder is refused before it walks anything, and `error`
carries the reason. `failedFiles` is what the pass chose and could not remove.

Searches are normalized before they are sent, because Soulseek matches token by
token and punctuation matches nothing: a title such as `Don't Be Afraid of
Dying` finds no peers until the apostrophe is removed.

When an exact catalogue search returns nothing, a title ending in the standalone
label `EP` or `Single` is retried once without that label. Schall does not
otherwise shorten searches, and never broadens a query the user entered.

Scans recognize MP3, FLAC, Ogg/Opus, MP4/M4A, and DSF files. Core tags,
MusicBrainz recording IDs, and ISRCs are read with a pure-Go parser. FLAC
duration and tag-provided durations are stored when available. Unchanged files
are touched without re-reading their tags; missing files remain in the database
so manual mappings are never deleted by a scan.

After a changed file is scanned or an edition is refreshed, Schall evaluates
only affected files and tracks. It tries MusicBrainz recording IDs first, then
the identity resolution proved for the file, then ISRCs, then exact
artist/album/disc/track positions. Automatic mappings
store their method and confidence. A match is not created when the
highest-priority evidence is ambiguous, and a manual mapping always wins.
The Library view can search the entire imported catalogue when safe candidates
are insufficient. Reassigning a track that belongs to another manual mapping
requires explicit confirmation; the displaced file is then re-evaluated.
Clearing a manual decision also immediately reruns conservative automatic
matching.

### Acquiring one file for a want

A wanted recording is pursued without anybody choosing a folder. Schall
searches for it, fetches one copy per pass, and decides what that copy is —
which means the questions a release import asks about a whole folder are asked
here about a single file against a single recording.

Nothing is admitted on tags. A peer's file carries no provenance, and
consistently wrong tags on the wrong audio pass every check a tag can answer,
so the audio has to agree. Two combinations conclude: the identification names
exactly one recording and it is the wanted one, or it names the wanted one
among others while the file's own MusicBrainz recording ID or ISRC names it
too. An installation with acoustic identification switched off does not search
at all, and says so, rather than fetching files it could never admit.

Every copy offered for a want is recorded with what it came to: `accepted` and
imported, `discarded_audio` because the audio is something else,
`discarded_tags` because the file's own identifiers contradict the recording,
or `held` because nothing could decide. The first three are permanent and take
that offer out of the running; a held copy is kept as a question with its
evidence while the want carries on looking. `undelivered` — bytes that never
arrived or arrived truncated — is none of those: it is a fact about the
transfer, so the same offer may be tried another day.

A refusal never deletes anything. The bytes belong to the provider, the inbox
is mounted read-only, and a discard is a decision about what Schall will use.
`GET /api/v1/acquisition-targets/{targetId}` returns every copy tried for that
want with the sentence and the evidence behind each verdict.

A want follows one copy at a time, so `POST /api/v1/downloads/{id}/retry` on a
request that serves one is refused with `409` once the loop has moved on to
another. It is a refusal that costs nothing: a transfer that failed rules
nothing out about that peer's file, so the offer stays in the running and the
want asks for it again itself rather than two copies arriving for one recording.

### The review queue

`GET /api/v1/review-queue` lists the wants holding a copy nothing could decide
about, oldest question first, each with the recording that was asked for and the
copies that arrived claiming to be it. Only `held` copies are offered: a copy the
loop refused on the audio or on contradicting identifiers was answered, and
re-offering it would be inviting somebody to overturn a proof. `ruledOut` says
how many offers have already been struck off, which is the difference between
nobody having looked and this being the eleventh attempt.

`GET /api/v1/review-queue/copies/{copyId}/audio` plays one held copy. The whole
track is served, transcoded, with range requests — a copy reaches this queue
precisely because no identifier could settle it, so the audio is the last
evidence there is, and somebody who is unsure needs to move around inside it
rather than hear a fixed window. Without ffmpeg the endpoint answers `503` with a
sentence: the preview is lost and nothing else is. Only a `held` copy is served;
an accepted one is a library file with its own path and a refused one is answered.
It answers `Range` and `If-None-Match`, so a phone can seek and does not
re-download a preview it already holds. `GET .../waveform` beside it does the
same for the peaks: a `304` when the shape has not changed.

`POST /api/v1/review-queue/copies/{copyId}/accept` is the accept outcome. It
writes the decision and does not bring the file in: copying into the library
reads tags, writes to disk and queues a scan, which is not work to put behind an
HTTP timeout. The import that follows honours the decision instead of grading the
copy again — grading it could only reach the same "nothing could decide" that
sent it to a person, and acting on that would overturn their answer. The verdict
is written first, so a crash before the import leaves a decision to be honoured
rather than a file nobody decided about. `202` means the decision is recorded and
the import is queued; `409` means something already answered that copy.

`POST /api/v1/acquisition-targets/{targetId}/none-of-these` refuses every copy
held for a want and sends the want back to look for another. It is the same
sentence as a discard — this file is not that recording — so it goes into
`acquisition_target_files` with `decided_by = 'user'` rather than into a second
store, which is what stops the loop from offering back a file somebody has just
rejected.

`POST /api/v1/acquisition-targets/{targetId}/wrong-recording` is the outcome that
is about the question rather than about any copy: this entry does not name the
recording it was resolved to. The rejected recording is remembered permanently
and the entry goes back to being looked up without it, so the next resolution
cannot return the answer that was just refused. Only a person may rule a
recording out — the store's check says so — because an exclusion narrows what may
be concluded, and one Schall wrote itself would be it discarding its own
candidates and then concluding from what it had left. `409` means there was no
resolution to reject.

### What a review row carries

`GET /api/v1/review-queue` and `GET /api/v1/downloads?view=review` are the two
requests the review screen makes. Everything a row needs to render — the
question, its evidence, the audio and waveform to play, the cover to show, and
the routes its answers post to — is in those two responses. Nothing about a
row needs a third request; the audio, waveform and cover routes are all built
from an id the row already has.

**Downloaded** (`kind: "copies"` or `"stopped"`) asks whether one of the
copies fetched for a want is the recording. Its evidence:

- `target`: the recording asked for — `artist`, `title`, `album`,
  `durationMs`, `isrc`, and `originAlbumId` when the want traces back to a
  release Schall knows, which is what a cover comes from
  (`GET /api/v1/albums/{originAlbumId}/cover`).
- `copies`: every copy tried, each with `id`, `name`, `sizeBytes`, `verdict`,
  and `evidence` (`observed`, `wanted`, `agrees`, `differs`, `bitRate`). A
  copy's audio is `GET /api/v1/review-queue/copies/{copyId}/audio`; its
  waveform is `GET /api/v1/review-queue/copies/{copyId}/waveform`, answering
  `{"peaks":[...]}` or 404 if none has been generated yet. Both routes are
  built from the copy's own `id`.
- `copyGroups`: which copies are the same audio, so several rips of one
  master read as one card.
- on a stopped want whose copy is already a library file under another
  recording: `fileRecordingId`, `fileTitle`, `fileArtist`, `fileReleaseTitle`,
  the identity already recorded for that file.

Its answers: `POST /api/v1/review-queue/copies/{copyId}/accept` (or, for a
stopped want's filed copy, `POST /api/v1/review-queue/wants/{targetId}/accept-file`),
`POST /api/v1/acquisition-targets/{targetId}/none-of-these`, and
`POST /api/v1/acquisition-targets/{targetId}/wrong-recording`.
`POST /api/v1/acquisition-targets/{targetId}/not-wanted` (Remove from
Wishlist) is common to every kind that carries a `target`.

**Version** (`kind: "resolution"`) asks which MusicBrainz recording a
playlist entry means. Its evidence is the same `target` as above, for the
cover, plus `candidates`: `recordingId`, `releaseGroupId`, `artistName`,
`releaseTitle`, `trackTitle`, `durationMs`, `isrc`, `agrees`, `differs` — the
recordings the entry could name and what stands for and against each.

Its answer: `POST /api/v1/acquisition-targets/{targetId}/resolution` with
`{"recordingId":"..."}`.

**Folder** (from `downloads?view=review`, one entry per request with
`importEvidence` set) asks what a downloaded album folder's odd files belong
to. Its evidence is the request itself: `albumId` (`GET
/api/v1/albums/{albumId}/cover` for the cover), `entryArtist`, `entryTitle`,
`username`, `directory`, `importError`, and `importEvidence` — the files
tried, each with what it disagreed on, and the catalogue tracks nothing
matched. No copy is played here; a folder is compared file by file against a
catalogue rather than heard.

Its answers: `POST /api/v1/downloads/{id}/revalidate` (Check again),
`POST /api/v1/downloads/{id}/resolutions` with
`{"fileName":"...","trackId":"..."}` (resolve one file to a track), and
`DELETE /api/v1/downloads/{id}/resolutions/{decisionId}` (undo a resolution).

Every field above was already in the two responses; this audit added none.
`originAlbumId` was already sent on `target` (`omitempty`, so a want with no
traceable release sends none), just never declared on the web's `AcquisitionTarget`
type because nothing there reads it.

### Independently acquired files

Files bought from Bandcamp, ripped from a disc, or simply copied into a watched
folder are first-class owned music. Matching alone could not describe them: a
file whose artist nobody follows has no catalogue track to map to, and until it
had one it was indistinguishable from a file whose matching had failed.

Resolution asks the metadata providers what a file is, directly, without
requiring anyone to follow anything first. A scan queues the files nobody has
asked about yet, and each answer is one of:

| State | Meaning |
| --- | --- |
| Identity proven | One recording is proven for this file. |
| Needs review | Several recordings fit, or one fits but not conclusively. |
| Evidence disagrees | The file's own identifiers contradict what was found. |
| Local only | No provider knows this music. It is owned all the same. |
| Not asked yet | The question is queued, or the provider was unreachable. |

An identity is accepted automatically only when the evidence identifies exactly
one recording and nothing contradicts it: a MusicBrainz recording ID, an ISRC,
or artist, title, and duration agreeing together. Title and duration alone
settle a track within a known release, but across the whole provider database
they are a coincidence between two three-minute songs, so the artist is
required as well. A cover version, a remaster of the same length, or a duration
a minute out therefore becomes a question rather than a link.

Unresolved files are listed with the candidates behind them and what stands for
and against each, so the question can be answered rather than merely reported.
Accepting a candidate, or recording that a file has no external identity at
all, is permanent: later attempts leave a decision alone, and only withdrawing
it asks again. Changed tags put a file back in the queue; a decision a person
made survives the retag, because they decided about the music, not the tags.

A resolved identity is an identifier the matcher may act on, and it no longer
waits for anyone. Proving what a file is now brings the release behind it into
the catalogue, so the file maps to a track by itself rather than sitting on
unused proof until somebody happens to follow the artist. Duplicate protection
counts the file against a download of that release throughout.

Only a proven identity ingests a release. Candidates are a question, and a
question must not put an artist in the catalogue. A release the catalogue
already holds is not fetched again, and two files from the same album ask once
between them.

Resolution costs one rate-limited provider request per file, so it is queued in
bounded batches after a scan and at startup rather than all at once. A single
file can be asked about on demand from the Library view.

### Followed artists and held artists

An ingested release needs an artist to belong to, and that artist is held rather
than followed. The two are different claims:

| | Followed | Held |
| --- | --- | --- |
| What it says | Keep this artist's releases complete. | Your library contains this artist's music. |
| Who decided | You did, explicitly. | Nobody. It follows from a file you own. |
| Releases known | The full discography, refreshed. | Whatever is already known — one release if ingestion held the artist, the full discography if you unfollowed them. |
| Counted as missing | Yes. | No. |

The distinction is what keeps "what is my library missing?" answerable. Owning
one track bought from Bandcamp would otherwise report the album's other ten as
missing music you never asked for, and a dashboard that inflates itself that way
stops being worth reading. Held artists are listed separately under *In your
library*, with a sentence saying why each is there.

Following a held artist promotes it: the artist keeps every mapping and release
already known, gains the full discography on the next refresh, and its releases
start counting toward completeness. Following from a MusicBrainz search result
does the same thing, so an artist Schall already holds is never reported as a
conflict you cannot act on.

Unfollowing is the reverse, and it never deletes music you own. It withdraws
the intent to keep an artist complete, and nothing else:

- If your library still reaches the artist — a matched file, a file already
  identified as their music, or a download of theirs still in progress — the
  artist is **kept**, demoted to held. Every release stays, including the ones
  you own nothing from, every mapping stays, and following again is one click
  and no refetch.
- If your library reaches nothing of theirs, the artist is **removed** from the
  catalogue. That is safe only because there was nothing left to lose, which is
  exactly what the check established.

Schall tells you which of the two happened. Their tracks stop counting as
missing either way.

### Telling the player

Navidrome is the playback layer and reads the same directory Schall writes.
Schall owns that directory and is its only writer; Navidrome only ever reads,
and its scanner must never move, rename or rewrite tags on those files.

What Schall asks of it is one thing: look at the library again, now. Every path
that writes music ends by queueing a library scan, so the telling hangs off the
end of that one job rather than being repeated at each of the writers. A scan
that found files written, rewritten or gone tells the player; a scan that found
the library exactly as it left it says nothing, so an idle installation never
has the player rescanning on a timer.

```sh
curl http://localhost:8080/api/v1/settings/navidrome
curl -X PUT http://localhost:8080/api/v1/settings/navidrome \
  -H 'content-type: application/json' \
  -d '{"baseUrl":"http://navidrome:4533","username":"schall","password":"…","enabled":true}'
curl -X POST http://localhost:8080/api/v1/settings/navidrome/test
```

The account needs to be allowed to start a scan, which in Navidrome means an
administrator; the connection test says so by name when it is not. The password
is never returned — the interface learns only that one is stored, and saving
with an empty one keeps it. Talking to the player is authenticated per request
with a hash salted afresh, never the password itself, because that is what the
Subsonic protocol defines.

A player that could not be told is a line in the log and nothing more. The
music is on the disc and in the catalogue either way, and the player finds it
on its own schedule regardless, so a courtesy that failed never fails a scan
that succeeded — and nothing is retried, because the next import tells it
again. An installation with no player configured, or one switched off, is not a
failure either: there is nothing to tell and nothing wrong.

Two things this deliberately does not do. It does not sync playlists — that is
the other half of roadmap item 6, and it is unbuilt. And a file that went
missing and came back byte for byte tells the player nothing, because the
scanner counts it as unchanged and it leaves no trace in the scan result.

## Authentication

`SCHALL_AUTH` selects who Schall believes. `open` is the default and serves
every request; the actor is `local`. `make dev`, the browser tests and the seed
run open. `proxy` is what a deployment behind Authentik sets, and it covers
`/api/` alone: the frontend files are served without a credential, because in
production Traefik's forward-auth stands in front of every request that is not a
Bearer call to `/api/`.

In `proxy` mode a request is served on one of two credentials.

- **The browser.** Traefik chains a `headers` middleware after
  `authentik-forward-auth` that sets `X-Schall-Proxy-Key` to the value of
  `SCHALL_AUTH_PROXY_KEY`. Schall reads `X-authentik-username` only when that
  key matches, because the identity headers are ordinary request headers that
  anything reaching the pod can write. `SCHALL_AUTH_TRUSTED_PROXIES` is the
  weaker alternative: CIDR blocks checked against the TCP peer address, which
  makes every host on those networks a trusted proxy.
- **A phone.** `Authorization: Bearer schall_…`, where the token is the one
  minted in Settings → Phone. Only its SHA-256 is stored, so the lookup is by
  hash and nothing can read a token back. `last_used_at` is written at most
  once a minute per token. A revoked or unknown token is 401.

Anything else is 401. A token cannot mint or revoke tokens: `POST` and `DELETE`
on `/api/v1/settings/phones` answer 403 to a request authenticated by one, so a
stolen phone cannot give itself a second credential.

```sh
curl http://localhost:8080/api/v1/me
curl -X POST http://localhost:8080/api/v1/settings/phones \
  -H 'content-type: application/json' \
  -d '{"name":"Pixel"}'
```

`/me` answers `{"actor":"local","auth":"open"}` on an installation that asks
nobody, and the phone's name with `"auth":"token"` on a request that carried
one. Minting answers with the row, the plain token, and `server` — the address
the request arrived on — so the QR code the browser draws carries
`{"server":"https://schall.example.com","token":"schall_…"}` and the app needs
nothing typed.

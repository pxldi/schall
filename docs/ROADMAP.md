# Schall — roadmap

Ordered by how much each item unblocks, from the 2026-07-29 gap analysis
against docs/PRODUCT.md. Prefer thin end-to-end slices over completing any
single layer. Items marked **⚠ RISK** are the three places the design is most
likely to be wrong once real data hits it — validate those aspects first,
regardless of position.

A shipped item keeps its entry and gains a **Status:** line. Its `Done when:`
clause is the only written record of what it was signed off against, so it is
not deleted once it is met.

The proving slice that tied the top of the list together — one hand-entered
target → B resolves it to a recording ID → automatic search → existing
request/transfer path → single-file verification → the file lands in the
library — is built, across items 1, 2, 3 and 4. What it has not done is run
against real music for long enough to say how often it decides, which is what
the risk under item 4 now asks for.

Items 1 to 12 are shipped. What is next starts at item 13, from the
2026-09-20 gap analysis at the end of this file.

## 1. Acquisition-target entity and lifecycle

**Status:** shipped 2026-07-29 (#67).

**What:** A first-class "pending acquisition target" — one wanted recording —
with states pending / searching / awaiting-review / acquired / not-wanted and
a `next_attempt_at` for requeueing. Today nothing represents "we want this
recording"; the closest objects are a download request (exists only after a
folder was chosen) and the Missing view (computed on the fly). The Wishlist
now stores each requested recording as an acquisition target.

**Unblocks:** Matching B needs somewhere to put its answer (2); the retry
loop needs something to requeue (4); review outcomes "not wanted" and "wrong
target" need something to attach to (5); the follow feed needs something to
create (7).

**Depends on:** Schema approval (STOP list). Retry cadence can stay open —
the column exists before the rule does.

**Done when:** A target can be created, searched for, transition through its
states, and survive an exhausted search as pending-with-a-next-attempt
rather than failed. Migration merged, integration-tested.

## 2. Matching B — entry → recording ID

**Status:** shipped 2026-07-30 (#69). Since 2026-09-06 an entry that fits both
the explicit master and the clean edition of one track resolves to the explicit
row instead of asking ([0032](decisions/0032-an-explicit-edition-is-preferred-over-a-clean-one.md)).
The ⚠ RISK below was measured on
2026-08-29 against the two real playlists this installation holds. **The
queue-flooding case it describes is retired: no entry reached the review queue
at all.** What the measurement found instead was a MusicBrainz coverage limit,
written up under *Measured* below.

**What:** Resolve a text/ISRC entry to a concrete MusicBrainz recording ID,
decoupled from library files. ISRC agreement concludes alone; text evidence
(artist/title/duration) concludes only when exactly one recording qualifies
and nothing contradicts, reusing the grading rules in `internal/identity/`.
A credit is compared as the set of artists it names, and a credit that disagrees
voids a pair only where nothing listened. Where AcoustID's leading cluster names only one recording, or the copy reproduces the excerpt fetched by that recording's
ISRC, the disagreement is recorded and shown and does not refuse the copy
(`docs/decisions/0030`).
Inconclusive → review. Output is an ID, never a search string.

**Unblocks:** Playlist ingestion (3), verified acquisition (4).

**Depends on:** 1 (the target entity persists the answer).

**Done when:** Given entry evidence, B emits exactly one of: a recording ID,
or a review item with candidates. Tests pin the refusals (two conclusive
candidates, contradicted ISRC, duration outside tolerance).

**⚠ RISK — text-grade rules vs. real streaming metadata.** Spotify titles
carry "- Remastered 2011", credits move between fields, durations differ
across masters. If most of a real playlist lands in review, the queue floods
and reflexive-accept kills the guarantee. Validate the grade rules against
one real playlist export before building anything downstream. The ISRC path
shrinks this risk; the text fallback is where it lives.

**How to read the rate off a run.** A want is a row in `acquisition_targets`.
`resolution_method` says which rule concluded it, `status` says whether
anything did, and `review_reason = 'resolution'` is the queue holding the
question "which recording is this entry". Nothing has to be added to measure
this: importing two or three real playlists and running the query below is the
whole measurement.

```sql
-- One line per outcome, for the wants a playlist import created.
SELECT coalesce(targets.resolution_method, '(none)') AS concluded_by,
       targets.status,
       targets.review_reason,
       count(*)
FROM acquisition_targets targets
WHERE targets.origin = 'playlist'
  AND targets.created_at >= now() - interval '1 day'
GROUP BY 1, 2, 3
ORDER BY 4 DESC;
```

Read it as four numbers: how many the ISRC path concluded (`resolution_method`
naming ISRC), how many the text path concluded, how many are waiting on a
person (`review_reason = 'resolution'`), and how many resolved to the *wrong*
recording — the one that must be zero, and the only one a query cannot answer,
because it is read by opening a sample of the concluded wants and checking the
recording against the entry.

If the review rate turns out high, what to do about it is a matching-rule
question with its own proposal and its own safety argument. This measurement
only buys the numbers that would justify one, or retire the risk.

**Measured, 2026-08-29 (#379).** Measured against the two Spotify playlists
the owner has connected, as they stood after weeks of running: *777*, 38 songs
of club techno, imported 2026-07-30, and *Angelcore 🪽 Worry type songs*, 313
songs of slowed and sped-up edits, imported 2026-08-15. That is **347 entry
resolutions**. Three further wants behind those entries are upgrades of files
already held. They never went through entry resolution, so they are excluded.

| | 777 | Angelcore | both |
| --- | --- | --- | --- |
| ISRC concluded | 16 (42.1%) | 14 (4.5%) | 30 (8.6%) |
| text concluded | 15 (39.5%) | 28 (9.1%) | 43 (12.4%) |
| nothing matched | 7 (18.4%) | 267 (86.4%) | 274 (79.0%) |
| **waiting on a person** | **0** | **0** | **0** |
| **resolved to the wrong recording** | **0** | **0** | **0** |

**No entry reached the review queue.** Zero of 347. The risk assumed a full
review queue was the danger, and that is not how the rules behave. When
MusicBrainz has never heard of an entry's artist, the text search returns rows
that all grade `noMatch`, and `noMatch` rows are dropped before they can become
candidates. The outcome is "no recording MusicBrainz knows of matches this
entry", which asks nobody anything. The live review queue holds one item and it
is not an entry.

**No entry resolved to the wrong recording.** All 73 concluded entries were
checked by hand against the MusicBrainz recording they named. Every artist and
title agrees. Every duration is within one second. One title differs in words:
Spotify's *Courtship Unleashed - Mixed* against MusicBrainz's *Courtship
Unleashed*. ISRC agreement concluded that pair at a nil duration gap, so it is
the same recording under a version label Spotify added.

The case the risk named held every time. *whiteout* and *whiteout - Slowed*,
*glamorgeddon* and *glamorgeddon - Slowed*, *jessie* and *jessie - Slowed*, and
*addiction* and *addiction - Slowed* each resolved to **two different
recordings**. No edit collapsed onto its original.

**MusicBrainz coverage limits the rate, not the grading rules.** A playlist of
label releases resolves four songs in five. A playlist of bedroom edits
resolves one in seven. 25 of the 274 unmatched entries were put to MusicBrainz
twice each, once by ISRC and once by artist and title. **All 50 searches
returned nothing.** MusicBrainz holds no row for this music, and no rule can be
loosened into a recording that does not exist. A low rate here is what 10a and
10b were built for, and it is the case a second catalogue key would address
(see the identity note in CLAUDE.md).

**Still unmeasured.** Both playlists are one person's taste and both are
electronic. The specific case the risk describes, a mainstream remaster-heavy
list whose titles carry "- Remastered 2011", was not measured. Spotify no
longer serves a playlist's contents to an application token: `/playlists/{id}
/items` answers 401 and the playlist object inlines no rows. Only a playlist
the owner has connected can be read, so measuring that case needs the owner to
connect one. It matters less than it did, because the failure mode it
describes did not occur across 347 entries.

## 3. Playlist ingestion — Spotify Web API

**Status:** shipped 2026-07-30 (#72), schema in `00026_playlists.sql`. See
docs/decisions/0008.

**What:** Import playlists through the Spotify Web API (entries with artist,
title, album, duration, ISRC), create a Schall playlist (a want list) and an
acquisition target per unowned entry via B.

**Unblocks:** The premise; Navidrome playlist sync (6) has nothing to sync
without it.

**Depends on:** 1, 2. OAuth app registration is user-supplied configuration.

**Done when:** A real playlist imports; every entry is either matched to an
owned file, target-created, or in review; re-import is idempotent.

## 4. Retry-until-verified acquisition loop

**Status:** shipped 2026-07-30 (#73–#77), schema in `00027` and `00028`. Three
things it left open are still open. Migration
`00098` now parks wants that receive only below-floor copies after three
answers and rechecks them weekly. The
measurement in the ⚠ RISK below is entirely owed.

**What:** Per-target candidate iteration: search, rank, download one file,
verify (A vs. the B target: tags + AcoustID), accept / discard-and-next /
route-to-review, requeue on exhaustion. Includes the new single-file
validation path beside whole-release import, fail-closed acoustic
verification for slskd-sourced files.

**Unblocks:** Automatic acquisition end to end; the review queue receives its
real traffic.

**Depends on:** 1; 2 for real targets. Changes import philosophy (automatic
discard of a downloaded file does not exist today) — STOP-list approval.

**Done when:** A target with a wrong first candidate ends up with the right
file imported, the wrong one discarded and remembered, with no human touch;
an unavailable target is requeued, not failed. A want that stays below the
configured floor is parked without lowering that floor, and a person can
waive the floor for that want only. A sibling harvested from the same folder
keeps its stored floor. Parked wants stay on the weekly rung after provider or
peer deferrals and when a search returns no usable disclosed candidate.

When every arrived copy has only an artist-credit contradiction, the target stops
with `next_attempt_at` empty and the discarded copies in the review queue. A
recording-ID, duration, or audio contradiction still requeues the target. Since
`docs/decisions/0030` a credit that disagrees refuses a copy only where nothing
listened, so this stops far fewer targets than it did.

**⚠ RISK — the discard/review boundary, measured 2026-08-01. One copy in ten,
and stuck for the opposite reason to the one this asked about.** The boundary
was built as a third answer rather than either bad one: a copy nobody could
identify is `held` with its evidence, neither imported nor thrown away. Of 321
copies that arrived and were judged on the live instance, 197 were admitted, 92
were refused and **32 were held — about one in ten**. Only ten of those thirty-two
are still a question: the other twenty-two sit on targets the loop went on to
acquire from a copy it could decide, which is the retry loop making a held copy
something other than a dead end.

The refusals are worth as much as the rate. Sixty-two of the ninety-two were
refused by the audio rather than by the tags — copies that got past every tag
comparison and were then named as a different recording by their fingerprint.
Each one is a file a tags-only rule would have admitted, which is ADR 0002
measured rather than argued.

The risk was written expecting patchy AcoustID coverage to be what held them.
It is not. Of the ten still open, nine are copies AcoustID *answered*, naming
the wanted recording alongside exactly one other, and only one is music it had
never heard. In four of those five cases the other name is the same performance
a second time — MusicBrainz holding two recording rows for one piece of audio —
and in the fifth it is a genuinely different remix riding in on a flattened
answer, which is the case any new rule has to keep refusing.

So the cause was nameable, and what to do about it was a matching-rule question
rather than a tuning one. Both halves shipped on 2026-08-02: the audio is read at
its leading fingerprint cluster (#146, docs/decisions/0011), which is what tells
two MusicBrainz rows for one performance apart from a resemblance, and the
duration a match rests on is the measurement rather than a tag (#147), which
refuses the fifth case on its own length as well. Neither backfills anything, so
the ten copies already held are still decided by hand. The rate the boundary
settles at now is unmeasured, and measuring it again is what item 5 draining the
queue makes possible; until then a held copy is a refusal in practice.

## 5. Unified review queue

**Status:** shipped 2026-07-30 (#61, built in #87), wrong-target store in
`00029_rejected_resolutions.sql`. Five corrections followed: an answered want
leaves the queue (#110), previews are encoded when the queue is read rather than
when play is pressed (#111), per-track toggles with completion that excludes
not-wanted (#130), every question a want stops on reaching the queue at all
(#132), a stopped want names both recording entries and their releases (#512),
and a want whose copy the library filed under a second MusicBrainz row of the
same registered track finishes against that file rather than stopping, where
MusicBrainz merged the two rows or something proved the copy is the recording the
want names; the rows reading alike files nothing on its own. The API
behind the redesigned screen landed next: a waveform per held copy
(`GET /review-queue/copies/{copyID}/waveform`, migration 00093) and one request
that answers a stopped want
(`POST /review-queue/wants/{targetID}/accept-file`). The 2026-09-01 UI audit also corrected the phone layout around the
decision context and answer bar, grouped repeated Overview questions by
release, and made empty and import states actionable. Identity and match questions now use the Library's Unidentified filter and inline
decision panels. Review keeps downloaded-copy, version and folder questions.
The queue now separates a want waiting for evidence from one waiting for a
person (migration 00096): a copy held because MusicBrainz could not be asked is
judged again after an hour, six hours, a day and a day, and then says it is a
question. One attempt is counted per pass, so a want holding several of those
copies spends one step rather than the whole schedule. A want stopped on what
the library calls its file is judged again
when that identity changes. The payload carries `waitingFor`, so a reader can
see which rows are theirs to answer.
The page's own `Done
when:` is met; the
*library's* is not, because ten `held` copies are still undecided —
`acquisition_target_files_held_idx` draining to empty is work at the queue, not
work on it.

**What:** One queue for everything ambiguous. Five persisted outcomes
(accept / none-of-these with per-(file,target) rejection memory / not-wanted,
track-scoped / wrong-target with bad-resolution memory / defer). Playable
30-second middle-of-track preview (transcoded, **with volume control**),
disagreements most prominent, fully keyboard-driven.

**Unblocks:** Makes zero-false-positives survivable at real volume; 4 at any
scale beyond a trickle.

**Depends on:** 1 (targets) and 4, which is what fills the queue. One of the
two negative-decision stores already exists: `acquisition_target_files` carries
`decided_by` precisely so that *none of these* is written into the same store
the loop writes its discards to — a second rejection table would let the loop
offer a file somebody had just rejected. Only the *wrong target* store still
needs schema approval. Also a transcoding dependency (ask first). The page
design comes from `DESIGN.md` at the root of this repository, which is the
committed design specification. It is never invented at the page.

**Done when:** A queue of 20 items is decidable end to end without the mouse;
every `held` copy is decidable and `acquisition_target_files_held_idx` drains
to empty; a rejected file is never re-offered for that target; a wrong-target
decision re-opens B with the bad ID excluded.

## 6. Navidrome integration

**Status:** shipped. The rescan trigger landed 2026-08-01 (#139, corrected by
#140), schema in `00036_navidrome_settings.sql`; the snapshot store landed
2026-08-02 in `00038_navidrome_playlist_snapshots.sql`, and playlist sync on top
of it in the same days — `sync_navidrome_playlists`, per list or as a sweep over
the player. The ⚠ RISK was prototyped 2026-08-01 and is closed as a question and
open as a constraint (docs/decisions/0010); what to do about a break is
docs/decisions/0012 and what a removal does is docs/decisions/0013.

Two things are owed to the deployment rather than to the code: the library has
to be a registered Navidrome library, and `Subsonic.DefaultReportRealPath` has
to be on before the `schall-sync` client first calls. Until both are done a pass
refuses rather than guessing, which is what it is for.

**What:** Thin slice first: Navidrome API client, post-import rescan trigger,
read-only contract on the library directory. Then playlist sync: three-way
snapshot comparison (last-pushed vs. Navidrome vs. Schall), push acquired
subsets, adopt Navidrome-side additions and playlists, never touch unacquired
wants.

**Unblocks:** The playback half of the premise.

**Depends on:** Rescan trigger: nothing above — can ship early. Sync: 3.

**Done when:** An import is visible in Navidrome without waiting for a
scheduled scan — **met**: a scan that found the library changed asks the player
to look again, and a player that could not be told never fails the scan; a
track removed in Navidrome disappears from the Schall playlist's acquired
subset while unacquired wants survive a full sync cycle untouched — **met**: a
pushed track the player stops listing is a removal only once its id is shown
still to mean the file it was pushed for, and acting on one deletes that entry
and nothing else, while a want nobody has acquired has no snapshot row to be
missing from and is never touched by any of it.

**⚠ RISK — Navidrome's ID model. Prototyped 2026-08-01; the assumption held
and the rule built on it did not.** A media file's id survives an incremental
rescan, a full rescan, a rename and a move — Navidrome matches by path first
and by a tag hash second, so one key surviving is enough. It breaks only when a
file is moved *and* retagged between two scans, and then the track leaves the
playlist with no trace over the API: `getPlaylist` stops listing it, `songCount`
falls, and `getSong` on the id it was pushed under still answers `ok`. So the
two cases 0004 has to separate look identical, and the separator is the real
path read back through `getSong`. Full findings and the three contracts that
follow — `Scanner.PurgeMissing` stays `never`,
`Subsonic.DefaultReportRealPath` is on, and Schall never moves and retags a
file in one scan window — are in docs/decisions/0010.

What is still unprototyped is the other half of the sentence: the race-free
import → rescan → push sequence. Sync is asked for rather than triggered by an
import — a per-playlist pass and a sweep, both queued through the API — so a
copy that has landed reaches the player when somebody asks for a pass after the
scan that found it, and joining those two automatically is its own decision.

## 7. Follow-feed automation

**Status:** the feed shipped 2026-08-02 (#150), index in
`00037_follow_feed_sweep.sql`. The suppression half of the pair is owed and
cannot be built yet: it belongs to item 9, which does not exist. Shipping the
feed first is the safe order — suppressing an artist from recommendations is
only honest once something else is covering them.

**What:** Periodic watch for new releases by followed artists (a recurring
self-rescheduling sweep; the transfer poller is the precedent) and automatic
target creation for them. Built as a pair with the recommendation-suppression
rule: followed artists are suppressed from recommendations *because* the feed
covers them.

**Unblocks:** The feed half of the premise; recommendations (9) can suppress
safely.

**Depends on:** 1. MusicBrainz rate budget shapes the sweep cadence.

**Done when:** A release published after the follow shows up as targets
without a manual refresh — **met**: `sweep_follow_feed` asks about a followed
artist whose catalogue has gone a day stale and wants what the monitor level
counts and the library does not hold, published after the follow and never
before it. A release with no date is not "new" and is left alone, and a
recording somebody stopped pursuing is passed over rather than re-opened.

The back catalogue is the other half, and it is a standing want rather than a
press (migration `00099`, docs/decisions/0035). While `want_missing_since` is set
on an artist, every missing track of every monitored release is wanted: on the
press, and again as each tracklist arrives from `refresh_album_metadata`. New
follows watch main releases.

## 8. Library layout configuration and migration

**Status:** the design is settled (docs/decisions/0014) and the template shipped
2026-08-02, schema in `00039_library_layout.sql`. The mover followed (#167), and
every import path now files into the same template: the two download importers
in #187/#188, the upload importer in #195.

The second clause of the `Done when:` shipped 2026-08-28 in #439, schema in
`00072_upgrade_low_quality_files.sql`. `internal/upgrade/sweep.go` finds
library files with a settled identity that sit under their format's bit-rate
floor and raises a want with `origin = 'upgrade'`. That origin is the one thing
that gets a want past the `heldAlready` check, so a recording the library
already holds is searched for anyway. When the fetched copy is proven and
imported, `internal/library/replace.go` compares the two, writes the
`library_file_removals` licence row and deletes the `library_files` row in one
transaction, and unlinks the bytes only after the commit. Lossless beats lossy,
higher bit rate wins between two lossy files, and a tie is not better. The
sweep ships off by default.

Duplicate resolution also has an unattended sweep. It passes its own actor to
the removal licence, while the duplicates screen passes the person at the
screen. The recorded actor distinguishes the two paths.

Two things this entry used to say are no longer true, and are kept here because
the reasoning was recorded rather than the conclusion. Replace-and-delete was
not held back for #191: a person still confirms every deletion they ask for,
and this path is licensed by the recorded removal row instead, which is the
same rule `00065_library_file_removals.sql` states. And the dependency below
turned out to be smaller than it reads. A re-link mechanism is only needed for
a move *somebody else* made; every move in this item's `Done when:` is one
Schall performs, where it knows both paths and can carry the row instead of
recognising it afterwards. Re-linking a hand-moved file is explicitly out of
scope, with the reasons in 0014.

**What:** User-configurable layout with a sensible default and safe escaping;
layout change as a supported migration (move files, repair every path
reference, keep identities and mappings attached — today a moved file is a
new row and loses both). Duplicate resolution per PRODUCT.md: better copy
replaces, loser deleted; quality-vs-metadata conflicts go to review.

**Unblocks:** Long-term library ownership; format-preference configuration.

**Depends on:** Path-continuity design (content- or identity-keyed re-link)
— a schema decision. Largely independent of items 1–7.

**Done when:** Changing the layout template moves an existing library with
zero lost mappings, identities, or provenance links; importing a better copy
of an owned recording replaces the worse file and deletes it.

## 8a. Uploading music the user already owns

**Status:** the upload half shipped 2026-08-02, no schema; replacement shipped
2026-08-30 (#89), schema in `00083_upload_replacement.sql`. A browser upload
streams into `SCHALL_UPLOAD_STAGING_PATH` — proven at startup to be outside
every music folder and to contain none — is validated there by an
`import_upload` job, and is copied into `SCHALL_IMPORT_LIBRARY_PATH`, which is
then registered as a music folder and scanned so the resolver identifies the
files the way it does every other file on the volume. Validation asks only about
the bytes: an upload is not a claim about what its files are, so there is
nothing for a catalogue to disagree with and nothing for AcoustID to object to.

Issue #490 part two shipped 2026-08-30. A multi-file upload now reports each
file's import state, including failed, and keeps the files that need a decision
in one Uploads pass. Failed jobs do not keep the safety poll open. The pass
reuses SoundCloud naming and manual matching. Setting a file aside records a
row that suppresses the upload and review questions. Review reports the count
and links to the filtered Library list. Library offers Return to review.
The row never enters matching evidence, so later evidence can still prove the
file and the row remains if the file becomes ambiguous again.

An upload that duplicates an owned recording now replaces it. The check runs
when resolution proves the uploaded file, not when it is imported: an upload
has no identity at import time, so there is nothing to compare. It replaces
exactly one other present copy, and only when the upload is strictly better by
the same rule an acquisition upgrade uses. A worse or equal upload is kept as a
second copy for the duplicates screen, because nobody asked for an uploaded
file to be deleted. `upgrade_settings.enabled` is not read on this path: that
switch governs Schall fetching upgrades on its own, and an upload is a person
handing Schall a file.

Two smaller things were left out by choice: an upload is a flat set of files
rather than a folder, and an upload in flight does not survive a restart. Resuming one is a schema proposal.

**What:** A browser upload that lands files in a staging directory, validates
them there with the existing folder-import path, and imports what it can prove
into the library. The user buys from Bandcamp and rips their own discs, and
today there is no way to get those files in without a shell on the server —
the library is on the cluster's volume and the purchase is on a laptop.

Validation needs nothing new. The folder-import path already treats acoustic
identification as something that can only object, which is the right rule for
a file whose provenance is the user's own word, and the wrong one for a peer's
copy (ADR 0002). Reusing it keeps this off the matching STOP list entirely.

**Unblocks:** The second of the two acquisition routes PRODUCT.md names.
Nothing else waits on it.

**Depends on:** 8, and only for the last step. Upload, staging and validation
stand alone; what an upload cannot do until 8 lands is replace a worse copy
already in the library, because a moved file is still a new row that loses its
identity and its mappings, and deleting the loser would delete something other
rows point at.

That reading was wrong about which change closed it. The replacement does not
rest on path continuity: `internal/library/replace.go` deletes a row and
records a licence, and the playlist entries that pointed at the doomed file are
carried to the survivor in the same transaction. What it rested on was somewhere
to hang the question, and identity resolution is that place.

**Done when:** A FLAC album uploaded through the browser is staged, validated,
and imported into the library without touching the filesystem by hand, and an
upload of a recording already owned replaces the worse copy and deletes it.

## 9. Recommendations

**Status:** Part 1 — the recommendation list — shipped 2026-08-11 (#295–#311) and
its `Done when:` below is met. ListenBrainz is the source, decided in
docs/decisions/0015 over the Last.fm this entry names, because a source that
answers with names instead of MusicBrainz IDs cannot be suppressed honestly.
The stores are docs/decisions/0016 (`00047_recommendation_stores.sql`), a
recurring sweep keeps the candidate snapshot current, the list renders on
Playlists → Recommended with all six product entries applied at read time, and a
review-queue rejection provably changes nothing
(`TestReviewQueueRejectionDoesNotAffectRecommendations`). The list stopped
being read-only on 2026-08-12: a suggestion can be turned into a want, stamped
with the origin `recommendation` so a want made from a suggestion can always
say where it came from (#312, `00049_recommendation_origin.sql`).

Part 3 — the `More like this` and `Less like this` taste signals — shipped
2026-08-30 (#387, ADR 0028). Feedback is append-only, copies the current
candidate context, changes the next sweep's rank preparation, and never enters
candidate expansion or the hard suppression path. `Clear feedback` removes the
source's feedback events.

Part 2 of #65 — the weekly playlist — shipped on 2026-08-14, after the owner
accepted its schema proposal (docs/decisions/0021) and answered the seven
questions in it. Once a week `internal/weekly` picks twenty suggestions, asks
for them through the ordinary acquisition loop, puts each obtained file on trial
for a week under a lease, and removes the ones nothing said to keep. Two signals
keep a song: the star the user presses in their player, and a Keep button in
Schall. A song nobody kept is announced one week and removed the next, and a
signal that could not be read keeps it — silence keeps, which is the inverse of
the rule everywhere else in Schall and is deliberate here. The removal pass
checks the lease again immediately before the unlink and stops if it is no
longer `leaving` for that file. The feature ships
switched off. A removed recording is held back from the list for 180 days, which
is the sixth suppression rule.

Part 4 of #65, issue #383 — the source mix and replacement picks — shipped on
2026-08-30, schema in `00082_weekly_playlist_mix.sql`. Two settings change what
the week asks for and nothing about what a file has to prove. `libraryShare` is
the percentage of the week taken from music the collection already holds: those
songs join the playlist with no want and no lease, so no refresh can ever delete
one, and the rotation that takes them off the list next week deletes a playlist
row and nothing on disc. `onePerArtist` stops one artist appearing twice in a
week, counting the songs already on the list. Schall holds no play history, so
the library pool spreads over the whole collection rather than favouring songs
nobody has heard lately; a recording it offers waits 180 days before it is
offered again (`weekly_library_picks`).

A want that has had a whole trial week and is still being searched for stops
holding a slot, so the week ships full instead of short. The want is left
standing and the acquisition loop keeps trying: making it `not_wanted` would
suppress the recording from the recommendation list for good over a slow week on
Soulseek. A want with a transfer in flight and a want somebody was asked about
both keep their slots. Past three times the week's number in open wants, a
refresh asks for nothing new and logs why, because every open want is a Soulseek
search and Soulseek bans a client that searches quickly.

**What:** External sources first (Last.fm / ListenBrainz over scrobbling
history), with the six product entries from PRODUCT.md, dismissal, impression
and feedback stores, and strict separation from review-queue decision stores.

**Unblocks:** Discovery. Nothing else waits on it.

**Depends on:** 7 (the suppression pair), dismissal/impression/feedback schema.

**Done when:** A recommendation list renders with all six product entries
demonstrably applied, feedback changes rank without changing eligibility, and a
review-queue rejection provably does not affect it.

## 10. Fewer questions — four proofs that do not need MusicBrainz

**Status:** 10a shipped 2026-08-16 (#405, #406, #408, #409, #410; see its
*State* paragraph); 10b, 10c and 10d shipped 2026-08-28. All four phases are
built. What is left of the item is rule 4 of 10a, written at the end of that
phase's *State* paragraph. Written from a verification pipeline the owner
brought, judged against the zero-false-positive rule, and calibrated against
the live queue the same day.

**The problem, in one number.** On 2026-08-16 the review queue held 44 questions
about copies, and behind them 129 files a peer had sent. Schall could not decide
about a single one. **AcoustID returned no recording ID for any of the 129.** The
one witness Schall has that can speak about audio has nothing to say about this
music, so every one of those files becomes a question for a person. The queue
does not get shorter by asking better. It gets shorter by having more things that
can prove an answer.

**What was refused, and why it is written here.** The proposal ends with a
scoring table: weight each signal, add them up, route by band, and accept at
`CORROBORATED` or `PROVISIONAL`. That is refused whole. Weights let three signals
that each prove nothing add up to an admission, which is the exact failure the
zero-false-positive rule exists to prevent. Three further items are refused with
it: repairing a contradiction automatically by rewriting tags or stripping an
ISRC, because a contradiction is surfaced and never resolved by guessing which
side lied; reading AcoustID's numeric score, because Schall reads the leading
cluster and ignores every lower one ([0018](decisions/0018-one-cluster-is-one-answer.md));
and treating a quality flag as an identity verdict, because a transcode of the
right recording is still the right recording.

Everything kept below either **proves** or **objects**. Nothing scores.

### 10a. The preview gate

**What:** [0024](decisions/0024-a-distributors-preview-is-an-audio-anchor.md), as
written there. A want with an ISRC fetches the distributor's thirty-second
preview once, at creation, and stores its fingerprint. Every copy a peer sends is
compared against that stored fingerprint. Below 0.20 admits, above 0.30 discards,
between holds.

**Measured, not assumed.** Of the 44 wants, 33 have an ISRC and 33 of 33 returned
a Deezer preview; MusicBrainz recovered one more from a recording ID and it has a
preview too, so 34 of 44 are anchorable. All 76 copies behind those 33 wants
scored between 0.005 and 0.140 against their own preview, and 380 mismatched
pairs scored 0.3521 and worse. Two slowed edits were told apart from their
originals at six times the separation.

The same test was then run where the answer was already known: eleven library
files identified by AcoustID rather than by a person, against the preview for the
recording AcoustID named. True pairs 0.0040 to 0.0631, false pairs 0.3903 and
worse. **The preview agrees with AcoustID everywhere AcoustID has an opinion**,
which is the claim 10a rests on.

**Unblocks:** every other phase here is smaller than this one. It answers the
questions that exist today rather than the ones that might.

**Depends on:** a Deezer client, which is a new external boundary and therefore
on the STOP list. Deezer needs no key. Spotify is listed in the proposal as the
fallback and is **not** verified to still return `preview_url`; the ladder should
be written Deezer then iTunes, with Spotify added only if it is tested first.

**Done when:** a copy is admitted or discarded by its want's stored preview
fingerprint, with the anchor recorded as the evidence; refusal runs alone first
and admission is a separate switch; a want whose preview could not be fetched
records a check that could not run and never a pass.

**State, 2026-08-16.** The gate is built and both directions are on. A want with
an ISRC fetches its preview once and keeps the fingerprint (`internal/anchor`);
a copy is measured against it, **discarded** above 0.30 and **imported** below
0.20, with the measurement recorded as the evidence either way. A copy between
the two thresholds is held, as is a copy nothing could measure.

The preview is read against the recording it was published for and no other, so
one measurement can never answer for a list of candidates.

Refusal alone was to run for a period before admission — the third precondition
of 0024. The owner shortened that period to a watched rollout on the same day.
It is written down in 0024 rather than left to be inferred.

What is left of 10a is rule 4: a want that holds an anchor may be searched
without a MusicBrainz recording. It is not a code-only change — a want with no
recording sits on the resolution schedule and the search schedule at once, and
they share one `next_attempt_at` column — so it is proposed separately.

**State, 2026-09-05.** The ISRC key leaves out the music the queue is actually
full of: 71 of the 75 wants holding questions that day have no ISRC anywhere.
[0029](decisions/0029-a-popular-upload-found-by-name-is-an-audio-anchor.md)
anchors those to a YouTube upload chosen by the want's name and length, and the
choice, the storage, the re-judging pass and the never-refuses rule are built
(`internal/anchor/choose.go`, `identity.ReadAnchor`, `judge_want_copies`). The
search runs through yt-dlp (`internal/youtube`), and a machine without yt-dlp
leaves such a want on the retry ladder rather than recording it as having no
anchor. The preview-anchor sweep and the three copy judges now have separate
one-worker lanes, so long anchor and judging passes do not hold library scans,
lyrics, covers, or other general work.

**State, 2026-09-09.** [0034](decisions/0034-only-a-topic-upload-admits.md)
limits admission to the artist's matching Topic upload. Other name-found uploads
remain questions, and stored `youtube` anchors are not backfilled.

**State, 2026-09-06.** A want keyed to the clean edition of a track settles on a
copy the library filed as the explicit one, and the audio naming the explicit row
is set aside instead of refusing the copy for good
([0032](decisions/0032-an-explicit-edition-is-preferred-over-a-clean-one.md)).
11 of the stopped wants that day are that pair.

### 10b. The library is its own witness

**What:** before asking anybody anything, compare a copy against the library's
own files. Schall already stores a Chromaprint fingerprint for all 1,609 library
files. A copy whose fingerprint matches a file whose identity is settled by manual
decision or measured audio is that recording, proven by a decision that was already
proven once.

**The compressed form still decodes back to raw frames exactly.** It remains useful
for AcoustID and for old library witnesses, but a prefix cannot certify an unseen
ending. Migration 00097 adds a separate full-length witness fingerprint and its
coverage seconds. Existing prefixes remain usable as explicitly partial
witnesses until the per-file backfill replaces them.

The backfill is queued at startup and runs through one job per library file. It
uses the existing 900-second fingerprint cap and never blocks startup. A witness
comparison aligns the files from their starts, checks overlapping five-second
windows across the compared span, and records the global rate and local worst
window for diagnosis. A full witness must cover the candidate within the
existing duration tolerance; a partial witness can object to a mismatch but
cannot admit the unseen remainder. An ancestor's proof is still recorded with
every admission, and withdrawing a manual decision still withdraws every
identity admitted on its strength.

**Unblocks:** this is the only proof here that compounds. Every file admitted
makes the next question easier to answer.

**Depends on:** 10a shipping first, because a fingerprint comparison wants one
implementation and not two.

**Done when:** a copy matching a settled library file is admitted without a
question, carries the proof that admitted its ancestor, and a withdrawn manual
decision withdraws every admission that rested on it.

**Status:** shipped 2026-08-28, strengthened 2026-09-09 by migration `00097_full_length_library_witnesses.sql`. See
docs/decisions/0027. Before a copy is sent anywhere, it is compared against the
files the library already holds and has already proven to be the wanted
recording; a copy that holds the same music is admitted on the older file's
proof, and the identity records which file admitted it and what settled that
file. Withdrawing a decision withdraws every identity admitted on the strength
of it, and every identity admitted on the strength of those.

The comparison is read from the start of both files rather than slid across
them, because sliding answers "does this file contain that audio" and an
hour-long mix would satisfy it. It can only admit: a copy far from every owned
file says nothing, since a second master of one recording is still that
recording. A contradiction from AcoustID or from the published sample voids the
pair whatever the library says.

The set of proofs a library file may speak on is enumerated and excludes
`published-sample`, which is a proof of the same standing and was not in the
owner's list. Widening it is one line and a decision of its own.

### 10c. Copies that agree are one question

**Status:** shipped 2026-08-28. Every copy is fingerprinted while it is judged
and the number is kept on the copy (`acquisition_target_files.audio_fingerprint`,
migration 00060); a backfill sweep measures the copies judged before there was
anywhere to keep it. The review queue sends which copies are one piece of audio
and the page draws one row per distinct audio, with the count and the copies
under the row. Nothing about what is admitted or discarded changed.

**What:** presentation, not proof. Group the copies of one want by fingerprint
and show the groups instead of the files. This changes nothing about what is
admitted; it changes how much reading a question costs.

**Measured.** 129 copies sit behind 44 questions. One want, *worry*, holds eleven
copies which fall into exactly two groups — five copies of one master and six of
another — and nothing in the file names says so. Eleven rows becomes two.

Being the odd copy out is an **objection**: it can hold a file back, never admit
one. Copies with identical encoder headers and byte sizes are one rip that
spread, not independent agreement, and must be weighed as one.

**Unblocks:** nothing. It makes the remaining questions cheap to answer, which is
the point once 10a has removed the ones that need not be asked.

**Depends on:** nothing. No STOP-list surface — it neither admits nor discards.

**Done when:** a want with several copies shows one row per distinct audio, with
the count and the best copy in each, and answering a group answers every copy in
it.

### 10d. What the audio is, as opposed to what it claims

**Shipped 2026-08-28.** A sweep decodes six seconds from the middle of every
library file and finds the frequency its music stops at, alongside what the file
says made it. A lossless container that stops below 16 kHz — or a bit rate wide
enough to carry the whole range spent on audio that stops well short of it — is
recorded as a suspected transcode, but only where the stop is a cliff, which is
what keeps an old recording that simply fades out at the top from being accused.
The same measurement is taken of each fetched copy while it is being validated
and kept in its evidence. It is shown as one chip, with the measurement behind a
disclosure, on the duplicates page and in the review queue's copy rows. Nothing
that admits or refuses a file reads any of it, and a test reads the source of
every grading package to keep it that way. The full decode for truncation is
not built: the length a decode measures is already stored and already compared.

**What:** the cheap intake checks from the proposal, as quality facts that never
decide identity: a full decode to catch truncation, the spectral cutoff, and the
encoder header. `00045_audio_properties` already stores bit rate, sample rate,
channels and a measured duration for exactly this reason.

A lossless container with nothing above 16 kHz is a transcode wearing a FLAC
extension. That is worth knowing when choosing between two copies of the right
recording, and it is never a reason to call a file the wrong recording.

**Unblocks:** duplicate resolution, which today compares bit rate and has no way
to see a transcode.

**Depends on:** nothing.

**Done when:** a copy carries a transcode flag derived from its own audio, the
flag is shown where two copies are compared, and no admission or refusal anywhere
reads it.

### Not planned, and why

- **AccurateRip and CUETools.** Genuinely the strongest proof in the proposal —
  bit-identity with other people's rips of the same pressing, no acoustic
  judgement at all. It needs an EAC or XLD log beside the file. Schall fetches
  single files from peers, which almost never carry one. Real proof, near-zero
  yield here. Revisit if album folders become the common case.
- **Whisper and lyrics, stem separation, melodic contour, cover-song
  embeddings, Panako.** Each is a new heavy dependency, several are models that
  want gigabytes of memory, and the machine ran out of memory on 2026-08-15.
  None of them proves a **recording** — lyrics cannot tell a live take from a
  studio one. They are corroboration, and corroboration cannot admit anything
  under the governing rule, so the cost buys nothing that shortens the queue.
- **Submitting fingerprints back to AcoustID.** Sound, courteous, and it helps
  everybody later. It does not answer a question open now.

**Done when (10 as a whole):** the number of copies-questions a person is asked
falls, and no file enters the library that was not proven — measured by
re-running the calibration above against the queue as it stands after each phase.

## 11. Job schedule visibility

**Status:** shipped 2026-08-30 (#502).

**What:** The Jobs view lists every job kind that keeps one queued or running
row as its schedule. Event-triggered scans, transfer polling and player
notifications stay out of the schedule list.

**Done when:** the view reports all database-defined scheduled kinds, each with
its work and an idle explanation, and a unit test fails when a new kind is not
listed.

## 12. A phone app that decides and watches

**Status:** the backend list is done and live (#623–#627; the Traefik rules
went in through the flux repository on 2026-09-07, so production runs
`SCHALL_AUTH=proxy` and a request without a credential is a 302 to Authentik
or, with a Bearer header, a 401). The first app is under `app/` since
2026-09-07: sign in by QR or typed token, Review with the preview player,
Downloads, Wants, Search with Follow, Settings, a Folder question that
resolves one file to a track, live updates over the event stream, and the
ntfy notification opens the Review tab. The plan is
[docs/app-plan.md](app-plan.md); the authentication decision is
[ADR 0033](decisions/0033-an-app-token-is-minted-behind-the-forward-auth.md).

**What:** An Expo app for the review queue, downloads, wants and artist
search. The browser keeps Authentik forward-auth; the phone uses an app token
minted in Settings. Before the app: the auth middleware and token table, a
`/me` route, the API types split out of `web/src/lib/api.ts`, caching headers
on covers and review audio, and a deep link in the ntfy notification.

**Done when:** the owner answers a review question on the phone with the
audio preview, and a request without a credential gets 401 in production.

## 2026-09-20 gap analysis

Every item above is shipped, so this is the second ordering of the gap between
docs/PRODUCT.md and what exists. Four decisions were taken the same day and
PRODUCT.md was changed to match: the web keeps no phone layouts and the Expo
app under `app/` is replaced by a native Android app in its own repository;
the own recommendation engine is built; the source identity of
[0026](decisions/0026-a-source-is-an-identity.md) is extended to wants and to
more services, because MusicBrainz holds none of the music the queue is full
of; and the six smaller features under items 18 to 23 are in the target.

The order is the order of dependency. Item 15 is the first code change,
because item 16 cannot search a want without it. The Android app runs beside
the rest in its own repository and blocks nothing here.

## 13. The web stops serving phones, the Expo app goes

**Status:** shipped 2026-09-20.

**What:** Delete `app/` and `docs/app-plan.md`, take the app job out of CI and
`make check`, and remove the phone layouts from `web/src/routes` and the
responsive rules in `DESIGN.md` that only served them. The backend built for
item 12 stays: app tokens ([0033](decisions/0033-an-app-token-is-minted-behind-the-forward-auth.md)),
`/me`, the event stream, caching headers on covers and review audio, the ntfy
deep link.

**Unblocks:** 14, which needs one phone client to maintain and not two.

**Depends on:** nothing.

**Done when:** CI is green with no app job, `make check` passes without an
`app/` install, `web/src/lib/design-system.test.ts` passes, and PRODUCT.md no
longer promises a phone browser.

## 14. A native Android app

**What:** Kotlin, Jetpack Compose, Material 3, in a separate public
repository so Gradle stays out of this one's CI. The same screens item 12
built: sign in by QR or typed token against `/me`, Review with the preview
player and the web's three answers in the web's words, Downloads, Wants with
Stop looking and Look again, Search with Follow, Settings. Live updates over
the event stream with the Bearer header and the 15 s refetch as the safety
net. Material's own colour and type; the Schall tokens are not carried over.

**Unblocks:** answering review questions away from a desk, which item 12 did
and 13 removes.

**Depends on:** 13 for the deep link scheme, nothing else here.

**Done when:** the owner answers a review question on the phone with the audio
preview from an APK built from the new repository, and the ntfy notification
opens Review there.

## 15. A want with an anchor and no recording is searched

**What:** Rule 4 of item 10a. A want that holds an anchor
([0024](decisions/0024-a-distributors-preview-is-an-audio-anchor.md),
[0029](decisions/0029-a-popular-upload-found-by-name-is-an-audio-anchor.md))
but no MusicBrainz recording is searched, and its copies are judged against
the anchor alone. It starts as an ADR, because a recording-less want sits on
the resolution schedule and the search schedule at once and they share one
`next_attempt_at` column.

**Unblocks:** 16. Without it a source-keyed want has nothing to run on.

**Depends on:** nothing; 10a to 10d are built.

**Done when:** a want with an anchor and no recording gets searched, a copy
reproducing a Topic upload or a distributor preview is admitted and one
reproducing another upload is held, and a test pins that a copy failing the
anchor is never admitted on tags.

## 16. A want keyed by a source

**What:** Phases 3 and 4 of [0026](decisions/0026-a-source-is-an-identity.md).
`acquisition_targets` can carry a source identity instead of a recording: the
track at an address on a service, under that service's own identifier. One
source interface, each service saying what the track at an address is and
handing over thirty seconds of its audio as the want's anchor;
`internal/soundcloud` and `internal/youtube` exist, Bandcamp is new. The
loop looks on Soulseek first and fetches from the source itself through
yt-dlp when nothing there proves out. A schema decision, so it starts as an
ADR.

**Unblocks:** the 252 of 314 entries [0024](decisions/0024-a-distributors-preview-is-an-audio-anchor.md)
measured as unresolvable; the review queue's Version question gains an answer
that is an address rather than a recording.

**Depends on:** 15.

**Done when:** a playlist entry MusicBrainz holds nothing for can be turned
into a source-keyed want from the review queue, is acquired, and lands in the
library carrying the source identity; a test pins that a copy is admitted only
by reproducing the source's audio.

## 17. The own recommendation engine

**What:** Source 2 of the Recommendations section. It reads the `listens`
table (`00094_listens.sql`), the library, the wants and the More/Less
feedback ([0028](decisions/0028-more-like-this.md)), walks MusicBrainz
relationships, ListenBrainz artist similarity and co-occurrence in the owner's
listens and playlists, and writes recording IDs with reason codes into the
candidate stores of [0016](decisions/0016-recommendation-signals-and-candidates-have-their-own-lifetimes.md).
The graph it walks is chosen in a design note first.

**Unblocks:** suggestions that do not depend on ListenBrainz being up or
knowing the account.

**Depends on:** nothing; the sweep, the six rules and the weekly playlist
apply unchanged.

**Done when:** Playlists → Recommended shows rows whose reason names the own
engine, and `TestReviewQueueRejectionDoesNotAffectRecommendations` still
passes.

## 18. Bandcamp purchases import themselves

**What:** Read the owner's Bandcamp collection with their own credentials,
fetch each purchase's download, and import it through the upload path, where
AcoustID can only object.

**Depends on:** nothing.

**Done when:** a new purchase appears in the library after the next sweep
with its Bandcamp address as provenance.

## 19. Album wants from a playlist entry

**What:** A control on a playlist entry raises a want for the entry's whole
release, acquired through the whole-release path.

**Depends on:** nothing.

**Done when:** the release is acquired and the entry's own want is satisfied
by the file that lands.

## 20. Playlist export

**What:** A playlist written as M3U over library paths, and pushed to a
Spotify playlist by ISRC, with the entries that could not be written listed
rather than dropped.

**Depends on:** nothing.

**Done when:** both exports exist and an unresolved entry is listed, not
silently left out.

## 21. Label follows

**What:** Follow a label from a release page; its new releases join the feed
and its recordings are suppressed from recommendations with the label reason
the sweep already names. `internal/labels` exists.

**Depends on:** nothing.

**Done when:** a followed label's new release raises a want and its
recordings carry the label suppression reason.

## 22. Listening analytics on the Overview

**What:** From the local `listens` table: what was listened to most by
period, how much of it the library owns, what was listened to and is not
owned, each with a control to raise a want.

**Depends on:** nothing.

**Done when:** the three answers are on the Overview and the control raises a
want with origin `listens`.

## 23. A second person

**What:** Two people on one installation, each signed in through the
forward-auth, each with their own playlists, follows and recommendations,
every decision naming who took it, the library shared. A schema decision, so
it starts as an ADR.

**Depends on:** nothing, though 17 and 22 are simpler if built per person from
the start.

**Done when:** two people sign in, each sees their own lists, and a review
decision records who took it.

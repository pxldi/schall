# 0036 — The download inbox is cleaned by rule

**Status:** accepted, 2026-09-10, by the owner.

## Context

`SCHALL_DOWNLOAD_INBOX_PATH` is the folder slskd writes completed downloads
into. It was mounted read-only until 2026-09-10, so every import copied its
source and left it. The inbox now holds 922 GB in 36,082 files across 12,650
top-level folders, and the library holds a second copy of everything that was
ever imported from it.

The source-retention setting (`internal/server/retention.go`, `removeSource` in
`internal/downloads/importer.go`) removes each import's source from now on. It
does not touch the backlog, the copies a want refused, the copies a want held,
or the folders somebody fetched by hand in slskd and Schall never recorded.

0009 says a refusal deletes nothing. 0021 says a lease or a person's recorded
decision is the only licence to delete music, and it covers library files.
Nothing covered the inbox.

## Decision

The owner decided on 2026-09-10 that the inbox is cleaned, by the rule below,
including the deletion of the folders Schall never recorded.

### What is kept

An inbox file is kept when any one of these holds.

1. **A question.** It is a copy (`acquisition_target_files`) with verdict `held`
   whose want is `pending`, `awaiting_review`, `searching` or `unresolved`. These
   are the review questions and the wants still being looked for.
2. **An open download.** It belongs to a `download_requests` row that is still
   open. Exactly these combinations count as open:
   - `status` `requested` or `started`: the transfer is in flight.
   - `status` `failed`: the downloads view offers Retry on the files that never
     arrived, and a retry asks the peer for this folder again.
   - `status` `completed` with `import_status` `pending`, `validating` or
     `needs_review`: the import has not settled, or it is a person's question.

   Not open: any `cancelled` request, and a `completed` one whose
   `import_status` is `imported` or `discarded`.
3. **Recently changed.** Its modification time is inside the last 24 hours. A
   transfer slskd has not reported yet is indistinguishable from a file nothing
   needs, and this is what keeps it. No slskd configuration in this repository
   puts partial transfers under the inbox; a folder named `incomplete` directly
   under it is skipped anyway, with everything in it.

### What is deleted

Everything else, in four classes, and every file is recorded with its class
before it is unlinked.

- **`imported`** — the source of a download request whose import completed. The
  managed copy `downloads.local_path` names is confirmed on disc first, the way
  `removeSource` confirms it: the path exists and holds ordinary bytes. Its size
  is not compared, because nothing here knows what was validated and a
  re-encoded copy is deliberately not the size of its source.
- **`refused`** — a copy with verdict `discarded_audio` or `discarded_tags`. The
  verdict stays; only the bytes go.
- **`settled`** — a `held` or `accepted` copy whose want is `acquired`,
  `not_wanted` or `superseded`. An `accepted` copy's library file is confirmed
  first, as above.
- **`unknown`** — no download request and no copy resolves to it. `!Soulseek`,
  `!likes` and the albums fetched by hand in slskd are here, and the owner asked
  for them to go too.

A file some record resolves to that none of the rules above covers is kept and
counted as `kept_undecided`: a cancelled request's leftovers, or a copy still
being fetched. A file whose import completed and whose library copy could not be
confirmed is kept and counted as `kept_unconfirmed`. Both are conservative on
purpose. A rule that says nothing about a file is not permission to delete it.

### How a record becomes a path

slskd writes each transfer under the inbox as `<last folder of the remote
directory>/<file name>`. That derivation is `inboxFolder` in
`internal/downloads/inbox.go`, and the import path, the copy validator, the
fingerprint backfill, the re-judge pass and this pass all go through it. A second
derivation that disagreed by one component would look in a folder that is not
there and read every file it could not find as bytes that never arrived.

A record that resolves to no file on disc is skipped. Two records that resolve to
one path are both applied, and a keep beats a delete.

### The rule is asked twice

The walk over 36,082 files takes minutes, and the records and the files both move
inside them. Every keep set is read from the database a second time immediately
before the first unlink, and every file is stat'ed again at the moment it would
go. A file whose want was pursued again, whose import was undone, or that was
written to since the walk is taken out of its delete class and recorded under the
class it moved to.

The path is checked again there too. The file must still be an ordinary file, and
the folder holding it must still resolve to somewhere inside the resolved inbox
root. A folder swapped for a symlink between the walk and the unlink would
otherwise take `os.Remove` outside the inbox. A file refused for either reason is
kept and recorded as `kept_unsafe_path`.

### The inbox may not overlap the library

The pass refuses to run at all when the inbox is inside the managed library or
any allowed library root, or when it holds one. Library files resolve to no
download record, so an inbox inside a music folder would classify the collection
itself as `unknown`. The refusal is recorded on the cleanup row and nothing is
walked. Allowed roots bound every music folder that can ever be added, because
`library.PathValidator` refuses a root outside them.

### The record

`inbox_cleanups` is one pass: who asked, whether it was a dry run, when it
started and finished, the per-class counts, and the error if it failed.
`inbox_cleanup_files` is one row per file the pass looked at, with its class, its
size, and `deleted_at` — null for a dry run and for every kept file. The row is
written before the unlink, never after. A crash between the two leaves a row
saying a file was doomed and is still there, which is readable; the other order
would delete music the record cannot name.

One pass runs at a time, held by a partial unique index on the rows that have not
finished. The repository asks first so the ordinary refusal reads as a refusal;
the index is what stops two requests a millisecond apart.

A file the pass chose and could not unlink keeps its class and its empty
`deleted_at`, and is counted apart in the class's `failed`. The pass then finishes
with one sentence on the row saying how many are still there, which is what the
settings screen shows. A pass that reported deletions it did not perform would be
the one account nobody could act on.

Empty folders go afterwards, deepest first, and only through `os.Remove`, which
refuses a folder with anything at all in it. A folder still holding artwork, a
log, or a file that would not go stays where it is.

## Consequences

This admits nothing. No copy is admitted, no verdict changes, and nothing here
reads a similarity, a name, a score or a threshold. It deletes files by asking
what the records say about them.

A dry run answers the same question and deletes nothing, and the settings screen
will not offer the deletion until one has answered.

The first real pass is expected to delete most of the 922 GB. What it cannot get
back is a copy of a recording the library already holds, or a copy something
already decided against. What would be lost if the rule is wrong is a review
question, which is why rule 1 is read from the want's own state rather than from
the copy alone, and why the 24-hour grace applies to every class including
`unknown`.

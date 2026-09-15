# 0011 — The audio is read at its leading cluster

**Status:** accepted, 2026-08-02

## Context

AcoustID does not answer with a list of recordings. It answers with a ranked
list of *fingerprint clusters*, each scored, each carrying the MusicBrainz
recordings that have been linked to it. `internal/acoustid` collapsed that into
one deduped list, copying each cluster's score onto its recordings, and every
rule downstream then read membership of one undifferentiated set.

So two entirely different statements arrived looking identical:

- one cluster naming two recordings — AcoustID saying *this is one piece of
  audio and MusicBrainz has entered it twice*, which is ordinary;
- two clusters — AcoustID saying *the second is other audio that resembled the
  fingerprint*, which is the case that must never admit a file.

Because the flattened set could not tell them apart, the rule had to refuse
both: an identification naming more than one recording identified nothing, and
only the file's own recording ID or ISRC picking one out of the set could
conclude. That was correct and it was expensive. Measured on the live instance
on 2026-08-01, nine of the ten copies still waiting in the review queue were
copies AcoustID had *answered*, naming the wanted recording alongside exactly
one other name; in four of those five pairs the other name was the same
performance a second time. Only one of the ten was music the database had never
heard, which is what the boundary had been designed around.

## Decision

**The clusters are kept apart at the client boundary**, in the order AcoustID
ranked them, each with its own score and its own recordings. Duplicates are
dropped within a cluster and never across one.

**Identification is read at the leading cluster and nowhere else**, for
agreement and for contradiction alike:

- A leading cluster with one recording, or rows established as one registration,
  identifies the file. Distinct registrations in that cluster remain unknown.
  Conflicting near-tied clusters also remain unknown.
- Names in any lower cluster are not part of the answer. They neither agree nor
  contradict.
- A leading cluster that names recordings, none of which this one answers to,
  voids the pair at any similarity.
- `acousticFloor` (0.90) is unchanged and now plainly applies to the cluster it
  was always about. No other number is introduced.

**Where several rows fit, the question survives.** Resolving a library file
against the whole database, a leading cluster naming distinct registrations leaves
the answer unknown, and established same-registration rows remain a question — the release may still single one
out, and nothing else may. What the file *is* has been answered; which of
MusicBrainz's rows to key the answer on has not, and that is a question about
identifiers rather than about music. Verifying one fetched copy against one
wanted recording asks a narrower question and concludes.

**Validating a folder somebody chose keeps reading across every cluster.** There
the audio may only object, never admit, so narrowing it would block folders that
are imported today and would buy no guarantee at all.

**Storage keeps both readings.** `db.ImportAcoustic` gains `clusters`; the flat
`recordingIds` and the leading cluster's `score` stay exactly as they were, so
`/review` goes on working and evidence written before this change stays
readable. It is a jsonb column, so nothing is migrated and nothing is
backfilled.

## Why

The unit the answer is read at was wrong, not the rule. Reading membership of a
flattened set as identification would have borrowed a threshold Schall does not
own — that is why it was refused, and it stays refused. Reading the leading
cluster borrows nothing: the boundary is AcoustID's own, drawn by the same
service on the same audio, and the floor over it is Schall's.

Everything the change refuses, it refuses on the audio rather than on anybody's
tags. The 卒業ですね copy — `Get Your Wish (Sewerslvt remix)` returned beside a
different remix sharing a compilation — is refused now on its own cluster
boundary, where before it was one member of a flat set.

The exposure it adds is named rather than hidden: a cluster's recordings are
user-contributed links, so a cluster carrying a mis-linked recording admits a
file for a want it is not. That exposure already existed for single-name
clusters; this widens it to every name in the leading one. It was approved
knowing that.

## What was rejected, and why

**Letting the tags arbitrate.** The obvious cheaper rule is to let the audio
narrow to a set and exact tag agreement pick the one member nothing
contradicts. It resolves most of the queue and looks safe. Give a peer audio X,
tags falsified to claim R, and an answer containing both: the tags void X and
leave R standing alone, and the rule admits the wrong file with a clean
conscience. That is 0002's whole scenario, and it is worst here, because every
copy in this queue is one the audio could not settle — precisely when tags are
the evidence that cannot be trusted.

**Re-sorting the clusters by score.** They arrive ranked, and taking the leading
one as given means the number the floor is applied to is the number it was
always applied to, and that there is no tie between equal scores for Schall to
break. Breaking one would be choosing between two pieces of audio by coin toss.

## What breaks if reversed

Flatten the clusters again and the two statements become one: a want is either
struck off by a resemblance or admitted by one, and which of the two happens
stops being knowable from the evidence.

Read a lower cluster as agreement and 0002 is reversed by the back door — a file
is admitted for being somewhere in the answer, which is the membership rule this
has always refused.

Read a lower cluster as contradiction and correct copies are refused
permanently for what some other audio happens to be linked to.

## What is not decided here

Which MusicBrainz row a mapping is keyed on when one cluster names several. That
stays a question for a person, asked through the review queue, and it is a
question about identifiers rather than about music.

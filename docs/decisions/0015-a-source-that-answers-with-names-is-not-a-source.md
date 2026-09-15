# 0015 — A source that answers with names is not a source

**Status:** proposed, 2026-08-09 — CAN-29. Awaiting the owner's confirmation of
where their scrobbles land (see *What the owner still has to answer*) and
approval on that issue.

## Context

Roadmap 9 asks for recommendations from an external source driven by listening
history, filtered through the five suppression rules of docs/PRODUCT.md:263-273.
Four of those five are lookups against identifiers Schall already holds:

- **owned** — the library, resolved to recording MBIDs;
- **requested** — `acquisition_targets.musicbrainz_recording_id`
  (internal/migrations/00024_acquisition_targets.sql);
- **by a followed artist** — `artists.musicbrainz_id`
  (internal/migrations/00001_initial.sql);
- **dismissed** and **shown-and-ignored** — the new stores, which the parallel
  schema proposal defines and which speak MBIDs by project decree.

So a suggestion Schall cannot name in MBIDs cannot be filtered at all. It can
only be filtered *after* a resolution step — search by artist and title, score,
threshold — and a wrong resolution there does not produce a missing
recommendation. It produces one that silently escapes a suppression rule: a
track already owned, or already dismissed, shown anyway, with a reason attached
that belongs to a different recording. Nothing looks broken; the list just
quietly stops obeying its own rules.

docs/PRODUCT.md:259 names "Last.fm / MusicBrainz" and docs/ROADMAP.md:336 names
"Last.fm / ListenBrainz". Both were written before the identity model settled.
This decides between them, names the calls, fixes the client boundary, drafts
the settings table, and says what happens when the source is not there.

All API behaviour recorded below was observed against the live service on
2026-08-09 and is marked as such; everything else is cited to documentation or
to source.

## Decision

### 1. The source is ListenBrainz

**Schall asks ListenBrainz, and asks it for recording MBIDs.** Last.fm is not a
fallback and not a second source; if ListenBrainz has nothing for this user, the
answer is "nothing", not "something less exact".

The argument is one line long: `artist.getSimilar` returns `name` and `mbid`,
where `mbid` is documented as a field of the entry and is empty for a large part
of the catalogue, so a Last.fm answer is a name plus a *maybe*. Turning a name
into an MBID is a search-and-score.

Note carefully what is and is not being claimed. CLAUDE.md:12-31's
zero-false-positives rule governs **matching**, and matching is not what this
is: a recommendation that is wrong costs an eye-roll, not a polluted library,
and taste-space is allowed its guesses. What is not allowed a guess is the
**suppression** side, because every one of the five rules is a promise phrased
in identifiers — *this recording will not appear again* — and a promise kept by
a resolver is kept at the resolver's accuracy rather than absolutely.
ListenBrainz's recommendation endpoint returns nothing *but* recording MBIDs,
so the resolution step does not exist and therefore cannot be wrong.

The secondary argument is that Schall already pays MetaBrainz's tolls: it has a
paced MusicBrainz client (internal/musicbrainz/client.go:591-607) and a
User-Agent policy (internal/config/config.go:66), and recording MBIDs from
ListenBrainz expand through exactly that client into the artist and
release-group MBIDs rules 1 and 3 need.

### 2. The endpoints

**Primary — the collaborative-filtered list.**

```
GET https://api.listenbrainz.org/1/cf/recommendation/user/<username>/recording
    ?count=<n>&offset=<n>
```

No auth. Observed 2026-08-09: `200` returns
`payload.mbids[] = {recording_mbid, score, latest_listened_at}` alongside
`payload.last_updated` (epoch seconds), `payload.model_id`,
`payload.total_mbid_count` and `payload.offset`. The set is precomputed in
batch and capped: `count=2000` came back clamped, `total_mbid_count: 1000`.
An unknown user is `404 {"code":404,"error":"Cannot find user: …"}`; a *known*
user with no computed model is `204`, carrying `last_updated` in the payload
(`listenbrainz/webserver/views/recommendations_cf_recording_api.py`, the
`APINotFound` at :72 and the `APINoContent` at :81 and :125).

Two properties of this list matter to the design and are easy to miss:

- `last_updated` and `model_id` say *which* batch this is. They are the
  provenance of a recommendation and belong on the candidate row, not in a log
  line — "suggested by model X computed on date Y" is the difference between an
  explainable list and a magic one.
- `latest_listened_at` is frequently **not** null: the raw CF list includes
  recordings the user has already scrobbled. That is not a bug and not a sixth
  suppression rule — PRODUCT.md's rule 1 is about the *library*, and a
  recording the user has heard and does not own is exactly what this feature is
  for — but it does mean the list must never be described to the user as "music
  you have not heard".

The endpoint's own documentation says it "is experimental and probably will
change in the future". That warning is the reason for the next paragraph rather
than a reason to avoid it.

**Secondary — seeds and one similarity hop.** The user's own top entities are
stable, documented, non-experimental, and MBID-bearing:

```
GET https://api.listenbrainz.org/1/stats/user/<username>/artists?range=…
GET https://api.listenbrainz.org/1/stats/user/<username>/recordings?range=…
```

then one hop per seed against the labs dataset host:

```
GET https://labs.api.listenbrainz.org/similar-recordings/json
    ?recording_mbids=<mbid>&algorithm=<name>
```

Observed 2026-08-09: the labs call returns a flat array of
`{recording_mbid, recording_name, artist_credit_name, artist_credit_mbids,
release_mbid, release_name, score, reference_mbid, caa_id, caa_release_mbid}`.
`reference_mbid` is the seed, which is the reason code for free.

The stats endpoints answer `204` when the statistic has not been computed for
that account and range — observed 2026-08-09 on `…/artists?range=month` — which
is the same "no data yet" shape as the CF endpoint and is handled the same way.

Two cautions about the labs host, both observed rather than documented:

- **`artist_credit_mbids` came back `null`** on a well-known recording. The
  labs answer is therefore authoritative for `recording_mbid` and for nothing
  else. Artist and release-group MBIDs for a candidate are resolved through
  `internal/musicbrainz`, never lifted from this payload — a suppression
  decision must not rest on a field the source leaves empty at its own
  discretion.
- **`algorithm` is a closed, unversioned enum.** An unlisted value is rejected
  with `400` and the permitted list in the body; the permitted list is a
  deployment detail of the labs host, not an API contract. The chosen algorithm
  name therefore lives in the settings row, not in a Go constant, so a rename
  upstream is a settings edit rather than a release.

**Not used in Part 1, deliberately:** `/1/user/<u>/fresh_releases` (it is
new-releases-by-artists-you-listen-to, which rule 3 routes to the follow feed
instead), `/1/explore/lb-radio` (a playlist generator — Part 2's shape, and
Part 2 is out of scope), and `/1/recommendation/feedback/submit` (a *write* to
an external service; dismissals are Schall's own store and stay there).

**Rate limits, observed 2026-08-09.** `api.listenbrainz.org` answers every
request with `X-RateLimit-Limit: 30`, `X-RateLimit-Remaining`,
`X-RateLimit-Reset` and `X-RateLimit-Reset-In`; the observed window was 10
seconds, so roughly three requests a second unauthenticated. The documentation
states the client "must use these headers to determine the rate" and that a
token raises the limit. Three a second is generous next to MusicBrainz's one,
and the expansion of candidates into artist and release-group MBIDs — a
MusicBrainz call each, at one per second — is what will actually bound a
refresh.

### 3. The client boundary

**A new `internal/listenbrainz` package, in the shape of `internal/musicbrainz`
and `internal/spotify`, and nothing else in the tree talks HTTP to
ListenBrainz.**

- `Client`, `Options{BaseURL, LabsURL, UserAgent, UserToken, HTTPClient}`,
  `NewClient` validating a non-empty User-Agent as MusicBrainz's does
  (internal/musicbrainz/client.go:190-193). `net/http` and `encoding/json`; no
  new dependency, as `internal/spotify/client.go:1-3` sets out.
- One `HTTPError{StatusCode}` with an `Unwrap` that names backpressure without
  losing the status, copied from internal/musicbrainz/client.go:626-635, plus
  `ErrThrottled` for 429/503, `ErrNotFound` for the unknown user, and
  `ErrNoRecommendations` for the 204. **The 204 is not an error condition
  dressed as one** — it is the service saying the model has not been computed
  for this account yet, which is a thing to tell the user, not a thing to
  retry.
- **The pacer is driven by the response headers, not by a fixed interval.**
  MusicBrainz's `wait` sleeps to a constant one-per-second
  (internal/musicbrainz/client.go:591-607); ListenBrainz publishes a per-window
  budget instead, so the client records `X-RateLimit-Remaining` and
  `X-RateLimit-Reset-In` from every response and holds the next request until
  the window turns over when the remaining budget is spent. Same discipline,
  read off the wire rather than guessed.
- **No cache in the client.** MusicBrainz's in-process merge cache
  (internal/musicbrainz/recordings.go) exists because merges are permanent
  facts; a recommendation is not. Candidates are persisted by the store the
  parallel schema proposal defines, and that store is the only cache.
- **The client knows nothing about suppression.** It returns MBIDs, scores and
  reason material. Everything in PRODUCT.md's five rules happens above it,
  against Schall's own tables — which is what keeps ADR 0005's separation
  structural rather than a matter of care.

The service layer reads settings per use and builds a client from them, exactly
as `playlists.Service.client` does at internal/playlists/service.go:104-123,
with a `WithEndpoints(apiURL, labsURL, client)` test seam like
internal/playlists/service.go:89-95. Every test points both URLs at
`httptest.NewServer`; no test reaches the real service.

### 4. The settings

One singleton table in the house shape of `slskd_settings`
(internal/migrations/00005_slskd_settings.sql) and `navidrome_settings`
(internal/migrations/00036_navidrome_settings.sql). **Draft DDL — it lands in
the stage-2 issue, at whatever the next free migration number is then (00046 as
of this writing), not here:**

```sql
-- +goose Up
-- Where the listening history lives, in the shape navidrome_settings set (00036).
--
-- Unlike slskd and Navidrome this is a hosted public service, so the address has
-- a right answer and is defaulted: the row exists to hold the *account*, and an
-- installation that never touches base_url is the normal one.
--
-- The token is optional because nothing here needs authentication — every call
-- Schall makes is a public read. It buys a higher rate limit and nothing else,
-- and Schall never writes to ListenBrainz: a dismissal is Schall's own record of
-- the user's taste, not something to publish under their name.
CREATE TABLE listenbrainz_settings (
    singleton BOOLEAN PRIMARY KEY DEFAULT true,
    base_url TEXT NOT NULL DEFAULT 'https://api.listenbrainz.org',
    labs_url TEXT NOT NULL DEFAULT 'https://labs.api.listenbrainz.org',
    username TEXT NOT NULL,
    user_token TEXT,
    -- The labs similarity algorithm is a closed enum the dataset host may rename
    -- between deployments; storing it keeps a rename a settings edit.
    similarity_algorithm TEXT NOT NULL DEFAULT
        'session_based_days_7500_session_300_contribution_5_threshold_15_limit_50_skip_30',
    enabled BOOLEAN NOT NULL DEFAULT true,
    connection_status TEXT NOT NULL DEFAULT 'unknown',
    connection_error TEXT,
    connection_detail TEXT,
    last_checked_at TIMESTAMPTZ,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    CONSTRAINT listenbrainz_settings_single_row CHECK (singleton),
    CONSTRAINT listenbrainz_settings_base_url_present CHECK (btrim(base_url) <> ''),
    CONSTRAINT listenbrainz_settings_labs_url_present CHECK (btrim(labs_url) <> ''),
    CONSTRAINT listenbrainz_settings_username_present CHECK (btrim(username) <> ''),
    CONSTRAINT listenbrainz_settings_token_present
        CHECK (user_token IS NULL OR btrim(user_token) <> ''),
    CONSTRAINT listenbrainz_settings_algorithm_present
        CHECK (btrim(similarity_algorithm) <> ''),
    CONSTRAINT listenbrainz_settings_status_valid
        CHECK (connection_status IN ('unknown', 'ok', 'failed'))
);

-- +goose Down
DROP TABLE IF EXISTS listenbrainz_settings;
```

Accessors are hand-written in internal/db/settings.go beside
`NavidromeSettings` (:148) and `SaveNavidromeSettings` (:167) — that file is not
sqlc-generated — including the same "an empty secret keeps the stored one"
treatment for `user_token` and the same reset of `connection_status` when the
account changes. Routes sit with their siblings at internal/server/api.go:452-461.

**No other table is proposed here.** Candidates, impressions and dismissals are
the parallel schema proposal's business, and this ADR deliberately stores no
per-run state — not even the last `last_updated` seen — so that the two
proposals cannot both allocate the same column.

### 5. Degradation

**A refresh that cannot reach the source changes nothing.** The previous
candidate set stays exactly as it was, stamped with when it was built, and the
UI says so. This is the taste-space form of the rule the rest of Schall already
runs on: *could not verify is not verified* (ADR 0002). An empty list and a
stale list are different answers, and the user is told which one they are
looking at.

**Failures are classified, never averaged into one "error".**

| Condition | Meaning | Behaviour |
|---|---|---|
| no settings row / `enabled = false` | not configured | list is empty with a "connect ListenBrainz" prompt; no request made |
| `404` on the username | the account does not exist | `connection_status = 'failed'`, the username is wrong, and it is a settings problem — say so |
| `204` on the CF endpoint | the account exists; no model yet | not a failure. "ListenBrainz has not built a model for this account yet"; the seed-and-hop path still runs |
| `429` / `503` / `ErrThrottled` | asked too often | hold to the reset window and continue; if the run cannot finish inside its budget it ends partial |
| transport error / `5xx` | the service is unreachable | keep the previous set, record the error, retry on the next scheduled refresh |

**Fan-out shrinks rather than degrading quality.** The expensive half of a
refresh is one similarity call per seed plus one MusicBrainz expansion per
candidate. A run carries a request budget and a failure count; when the failure
ratio crosses its threshold the run *reduces the number of seeds it still
intends to visit* and finishes with fewer, well-formed candidates, rather than
retrying its way to a timeout or filling the gap with lower-confidence hits.
This is Aurral v2's one genuinely borrowable operational idea
(`discovery/helpers.js:143-195`, read in 2026-07). A partial run
is recorded as partial and says why.

**A candidate that cannot be expanded is dropped, not shown.** If MusicBrainz
will not say which artist and release group a recording MBID belongs to, rules
1 and 3 cannot be evaluated for it, and a recommendation that has not passed all
five rules is not a recommendation. Dropping it costs one suggestion; showing it
costs the list its meaning.

### 6. If the owner scrobbles nowhere

If neither service holds the history, **the fallback seed is the library
itself**: the most-played or most-recently-imported artists and release groups
Schall already knows, expanded through the same labs similarity hop. Same
endpoints from `/similar-recordings` onward, same suppression, same reason
codes; only the seed changes, and the reason shown reads "because you have *X*"
rather than "because you listen to *X*". It is a weaker signal and worth saying
out loud in the UI, but it needs no new source, no new client and no new
settings beyond `enabled` — which is why it is a documented fallback rather than
a second design.

## Why

Because the whole feature is the *reason*, not the list. PRODUCT.md asks for
recommendations that are explainable and controllable; every one of the five
suppression rules is a promise that a specific thing will not appear. A source
that answers in names cannot keep those promises — it can only keep them
*usually*, at whatever rate the name resolver happens to be right, and nobody
will ever notice the cases where it was wrong, because a recommendation that
should have been suppressed looks exactly like one that should not.

ListenBrainz is chosen for being an identifier service that happens to make
recommendations rather than a recommendation service that happens to emit
identifiers.

## What breaks if reversed

Take Last.fm, and the resolver becomes load-bearing for the suppression engine.
A dismissal the user set on a recording stops working the moment the resolver
maps a suggestion to a different MBID for the same song — a remaster, a live
version, a compilation entry. The user dismisses it again. And again. The
feature's one permanent, user-visible promise — *dismissed is permanent* — is
broken by an invisible component whose failures are indistinguishable from the
recommender simply having many opinions. Meanwhile the "already owned" rule
leaks the same way, so the list recommends the user's own library back to them
at a low rate forever.

Drop the classification of failures instead, and the smaller version of the same
damage: a source outage renders as an empty list, the user reads "no
recommendations" as "the recommender has nothing for me", and a rate limit
becomes a statement about their taste.

## What the owner still has to answer

CAN-29 cannot close on this document alone. Two facts are the owner's:

1. **Where do your scrobbles actually go — ListenBrainz, Last.fm, or nowhere?**
   Navidrome ships scrobbling agents for both and can send to ListenBrainz
   natively, so "nowhere" may be one setting away rather than a real
   constraint. If the answer is Last.fm only, the honest options are to enable
   ListenBrainz scrobbling from today and accept a cold model for a few weeks
   (the CF endpoint gives `204` until then), or to run the library-seeded
   fallback of §6 meanwhile — **not** to adopt Last.fm as the source, for the
   reasons above.
2. **The ListenBrainz username**, and whether a user token should be stored at
   all. Nothing Schall does needs one; it only raises the rate limit. Leaving
   `user_token` NULL is the default this ADR recommends.

## Consequences

- Part 1 needs one new client package, one settings table, and no schema beyond
  what the parallel proposal approves.
- Every candidate carries a reason that is itself an identifier — `cf_raw` with
  a `model_id`, or `similar_to:<seed recording MBID>` — so "why was this
  suggested" is answered from a stored fact rather than reconstructed.
- Rule 3 (followed artist) and rule 1 (owned) both depend on expanding a
  recording MBID to its artist and release group through `internal/musicbrainz`
  at one request per second. That expansion, not ListenBrainz, is the rate
  limit of a refresh, and it is the reason candidates are persisted rather than
  computed per page view.
- Swapping the recommender later — PRODUCT.md's "own engine, later, larger" —
  changes what fills the candidate store and nothing else. The suppression
  engine, the reason codes and the UI never learn where a recording MBID came
  from.
- Schall writes nothing to ListenBrainz. The account is read-only from here,
  which is also why a token is optional.

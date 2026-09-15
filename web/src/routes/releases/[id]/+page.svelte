<script lang="ts">
  import { onMount } from 'svelte';
  import { toStore } from 'svelte/store';
  import { page } from '$app/state';
  import { createMutation, createQuery, useQueryClient } from '@tanstack/svelte-query';
  import {
    Ban,
    Check,
    ChevronDown,
    Download,
    ListPlus,
    LoaderCircle,
    Search,
    Users
  } from '@lucide/svelte';
  import {
    api,
    DuplicateProtection,
    type DuplicateEvidence,
    type SourceCandidate,
    type Track
  } from '$lib/api';
  import { formatBytes, wantedSummary } from '$lib/utils';
  import { progress, quiet } from '$lib/vocabulary';
  import BackLink from '$lib/components/BackLink.svelte';
  import Button from '$lib/components/Button.svelte';
  import Cover from '$lib/components/Cover.svelte';
  import Chip from '$lib/components/Chip.svelte';
  import DuplicateNotice from '$lib/components/DuplicateNotice.svelte';
  import OwnedBar from '$lib/components/OwnedBar.svelte';
  import ReleaseEditions from '$lib/components/ReleaseEditions.svelte';
  import SetCoverDialog from '$lib/components/SetCoverDialog.svelte';
  import StateMark from '$lib/components/StateMark.svelte';
  import StateTag from '$lib/components/StateTag.svelte';
  import ErrorNote from '$lib/components/ErrorNote.svelte';
  import Settle from '$lib/components/Settle.svelte';

  const releaseID = page.params.id ?? '';
  // Schall serves the picture itself, from a cache it filled once by the
  // MusicBrainz release this page is already about. A release nobody has
  // pictured answers 404, which is why the image hides itself rather than
  // leaving a broken frame.
  let coverMissing = $state(false);
  // Whether the dialog that sets a cover is open, and what the picture is asked
  // for under. The address of a cover never changes and Schall serves it with a
  // day of caching, so a cover just replaced would be the old one until
  // tomorrow; the version is the moment a new one was set, and it makes the
  // browser ask again without weakening the caching for everybody else.
  let settingCover = $state(false);
  let coverVersion = $state(0);
  const coverSrc = $derived(
    `/api/v1/albums/${releaseID}/cover${coverVersion ? `?v=${coverVersion}` : ''}`
  );

  function coverSet() {
    coverMissing = false;
    coverVersion = Date.now();
  }

  const queryClient = useQueryClient();
  // Neither of these asks again on a timer.
  //
  // The refresh that fills them in is one job: it writes the chosen edition and
  // every track of it in one transaction, and it publishes a notice when it
  // settles. The stream invalidates every key under ['releases'], so both of
  // these are asked again the moment there is a new answer.
  //
  // What they used to do was ask every 15 seconds until a release had a
  // MusicBrainz edition and a track list. That is not a wait — a release with no
  // MusicBrainz link is what music somebody ripped themselves looks like, and it
  // stays that way — so the page asked forever, for as long as the tab was open,
  // and the condition it was waiting on could never come true.
  //
  // A timer belongs here only while a refresh is actually running, and the
  // endpoint now says whether one is: `trackRefreshStatus`, the same figure the
  // releases list has carried per row all along.
  //
  // Only `queued` and `running` are waits. `completed` is finished, `failed` has
  // stopped, and `pending` means no refresh job was ever made — which is what a
  // release somebody ripped themselves looks like, and is the state that used to
  // poll forever. Reading "not completed" as a wait is the original bug.
  const waiting = (status: string | undefined) => status === 'queued' || status === 'running';
  const release = createQuery({
    queryKey: ['releases', releaseID],
    queryFn: () => api.release(releaseID),
    refetchInterval: (query) => (waiting(query.state.data?.trackRefreshStatus) ? 15_000 : false)
  });
  const tracks = createQuery({
    queryKey: ['releases', releaseID, 'tracks'],
    queryFn: () => api.releaseTracks(releaseID),
    // The track list has no status of its own; it is the thing the refresh
    // writes, so it follows the release's.
    refetchInterval: () => (waiting($release.data?.trackRefreshStatus) ? 15_000 : false)
  });
  // The library holds a track when a file maps onto it, and equally when the
  // same recording is proven on a file filed under another release — a single,
  // an EP, a compilation. Only one of those rows can carry the mapping, so
  // counting the mapping alone would call music missing that is on the disk.
  const held = (track: Track) => track.owned || track.heldElsewhere;
  const ownedCount = $derived($tracks.data?.items.filter(held).length ?? 0);
  // A dismissed track is not missing: the user decided not to want it, and the
  // arithmetic says so rather than counting the decision as a gap forever.
  const dismissedCount = $derived(
    $tracks.data?.items.filter((track) => !held(track) && track.wantStatus === 'not_wanted')
      .length ?? 0
  );
  const missingCount = $derived(($tracks.data?.items.length ?? 0) - ownedCount - dismissedCount);
  // Tracks somebody has asked Schall to go and find. A dismissed track is a
  // decision the other way and an acquired one is already settled, so neither
  // is still being looked for.
  const wantedCount = $derived(
    $tracks.data?.items.filter(
      (track) =>
        !held(track) &&
        track.wantStatus &&
        track.wantStatus !== 'not_wanted' &&
        track.wantStatus !== 'acquired'
    ).length ?? 0
  );
  // Wanting a release creates one want per track the library does not have and
  // nothing else: no transfer starts here, and every copy the loop later fetches
  // is still proven by its audio before it enters the library. Pressing twice is
  // safe — creation is idempotent on the recording — so the button stays
  // available and simply reports that the wants already existed.
  const wantRelease = createMutation({
    mutationFn: () => api.wantRelease(releaseID),
    onSuccess: async () => {
      await Promise.all([
        queryClient.invalidateQueries({ queryKey: ['releases'] }),
        queryClient.invalidateQueries({ queryKey: ['acquisition-targets'] }),
        queryClient.invalidateQueries({ queryKey: ['dashboard'] })
      ]);
    }
  });
  // Dismissing the remainder records a track-scoped not-wanted for every
  // missing track: one press for an EP that turned out to be a live dump.
  // Every decision it records can be taken back per track.
  const dismissRemainder = createMutation({
    mutationFn: () => api.dismissReleaseRemainder(releaseID),
    onSuccess: async () => {
      await Promise.all([
        queryClient.invalidateQueries({ queryKey: ['releases'] }),
        queryClient.invalidateQueries({ queryKey: ['artists'] }),
        queryClient.invalidateQueries({ queryKey: ['acquisition-targets'] }),
        queryClient.invalidateQueries({ queryKey: ['dashboard'] })
      ]);
    }
  });
  // One mutation for the per-track toggles, so a row can say it is the one
  // being decided about. Wanting a dismissed track goes through pursue-again:
  // taking the decision back is explicit, never a side effect.
  type TrackToggle = { kind: 'want' | 'dismiss' | 'pursue'; trackId: string; targetId?: string };
  const toggleTrack = createMutation({
    mutationFn: (toggle: TrackToggle) => {
      if (toggle.kind === 'want') return api.wantTrack(toggle.trackId);
      if (toggle.kind === 'dismiss') return api.dismissTrack(toggle.trackId);
      return api.pursueTargetAgain(toggle.targetId ?? '');
    },
    onSuccess: async () => {
      await Promise.all([
        queryClient.invalidateQueries({ queryKey: ['releases'] }),
        queryClient.invalidateQueries({ queryKey: ['artists'] }),
        queryClient.invalidateQueries({ queryKey: ['acquisition-targets'] }),
        queryClient.invalidateQueries({ queryKey: ['dashboard'] })
      ]);
    }
  });
  const togglingTrack = $derived(
    $toggleTrack.isPending ? ($toggleTrack.variables?.trackId ?? null) : null
  );
  // A track already proven in the library, or one whose acquisition already
  // settled: both draw the tick and nothing else. `held` alone is what the
  // arithmetic above counts by; this is only for choosing what the row draws.
  function trackDone(track: Track) {
    return held(track) || track.wantStatus === 'acquired';
  }
  // The tag a track's want status draws in the row: the same words the rest of
  // the product uses for what is happening to a thing, read quietly because
  // they sit inside a row rather than beside a heading. "needs review" is the
  // one that asks for a person, so it is the one tone that stands out.
  function trackTag(status: string): { label: string; tone: 'neutral' | 'attention' } | null {
    switch (status) {
      case 'awaiting_review':
        return { label: quiet(progress.needsReview), tone: 'attention' };
      case 'not_wanted':
        return { label: quiet(progress.dismissed), tone: 'neutral' };
      case 'searching':
        return { label: quiet(progress.searching), tone: 'neutral' };
      case 'acquired':
        return null;
      default:
        return { label: quiet(progress.wanted), tone: 'neutral' };
    }
  }

  const selectEdition = createMutation({
    mutationFn: (musicbrainzReleaseId: string) =>
      api.selectReleaseEdition(releaseID, musicbrainzReleaseId),
    onSuccess: async () => {
      await Promise.all([
        queryClient.invalidateQueries({ queryKey: ['releases', releaseID] }),
        queryClient.invalidateQueries({ queryKey: ['releases', releaseID, 'tracks'] }),
        queryClient.invalidateQueries({ queryKey: ['releases'] })
      ]);
    }
  });

  // Whether the full list of pressings is open. It used to be every block on
  // screen at once, because choosing between pressings is a comparison and a
  // comparison behind a control was not one; now the pressing in use says
  // itself in one line and the comparison is a press away.
  let editionsOpen = $state(false);

  // The pressings MusicBrainz knows about, fetched when the page opens rather
  // than when a control is pressed, because the reader may want the comparison
  // the moment they open it.
  //
  // Two things about this key. It is not under `['releases']`: the event stream
  // invalidates every key beneath that prefix, and this request leaves the
  // installation for musicbrainz.org, so a release-wide notice would send it
  // out again for nothing. And it holds its answer for five minutes, because a
  // release group gains a pressing about once a decade.
  //
  // A release with no MusicBrainz release group is music somebody ripped
  // themselves. It has no pressings to ask about, so nothing is asked.
  const editionList = createQuery(
    toStore(() => ({
      queryKey: ['editions', releaseID],
      queryFn: () => api.releaseEditions(releaseID),
      enabled: Boolean($release.data?.musicbrainzReleaseGroupId),
      staleTime: 300_000,
      retry: false,
      refetchInterval: false as const
    }))
  );
  const editions = $derived($editionList.data?.items ?? []);
  // The pressing Schall is working from, described by the release endpoint
  // rather than by MusicBrainz. It is the same four facts in the same shape, so
  // the block that draws a fetched edition draws this one too, and the page can
  // always say which pressing it is on even when musicbrainz.org does not
  // answer.
  const selectedEdition = $derived.by(() => {
    const data = $release.data;
    if (!data?.musicbrainzReleaseId) return null;
    return {
      musicbrainzReleaseId: data.musicbrainzReleaseId,
      title: data.title,
      status: data.editionStatus,
      country: data.editionCountry,
      releaseDate: data.editionReleaseDate
    };
  });

  let sourcesOpen = $state(false);
  let sourcesLoading = $state(false);
  // The failure itself, not a sentence flattened out of it.
  let sourcesError = $state<unknown>(null);
  let sources = $state<SourceCandidate[]>([]);
  let sourceQuery = $state('');
  let searchedQuery = $state('');
  let expandedSource = $state<string | null>(null);

  // Source searches reach out to the Soulseek network, so they only run when
  // the user asks for them.
  async function findSources(override?: string) {
    sourcesOpen = true;
    sourcesLoading = true;
    sourcesError = null;
    expandedSource = null;
    try {
      const results = await api.releaseSources(releaseID, override?.trim() || undefined);
      sources = results.items;
      searchedQuery = results.query;
      sourceQuery = results.query;
    } catch (error) {
      sources = [];
      sourcesError = error;
    } finally {
      sourcesLoading = false;
    }
  }

  function sourceKey(candidate: SourceCandidate) {
    return `${candidate.username}/${candidate.directory}`;
  }

  // Arriving from the Missing page's "Find sources" is the user asking, so the
  // search runs on landing rather than making them press the button again.
  onMount(() => {
    if (page.url.searchParams.get('sources') === '1') findSources();
  });

  const downloads = createQuery({
    queryKey: ['releases', releaseID, 'downloads'],
    queryFn: () => api.releaseDownloads(releaseID),
    // Follows running transfers, then stops asking. Every step a transfer takes
    // publishes a notice, so this is the net under a stream that never arrived
    // and it runs at the rate every other net in Schall runs at.
    refetchInterval: (query) =>
      query.state.data?.items.some((item) => item.status === 'started') ? 15_000 : false
  });
  const openRequests = $derived(
    $downloads.data?.items.filter(
      (item) => item.status === 'requested' || item.status === 'started'
    ) ?? []
  );
  // A folder already requested is shown as such rather than offered again.
  const requestedKeys = $derived(
    new Set(openRequests.map((item) => `${item.username}/${item.directory}`))
  );
  // Schall refuses to acquire music the library may already hold until someone
  // answers for it. The refusal is kept beside what it refused, so the answer
  // can be given without searching for the source again.
  let duplicateQuestion = $state<{
    evidence: DuplicateEvidence;
    candidate?: SourceCandidate;
    requestId?: string;
  } | null>(null);

  // A duplicate refusal is a question, not a failure, so it is shown as the
  // evidence panel rather than as a red line under the button.
  function asked(error: unknown, about: { candidate?: SourceCandidate; requestId?: string }) {
    if (error instanceof DuplicateProtection) {
      duplicateQuestion = { evidence: error.evidence, ...about };
    }
  }

  function isDuplicateQuestion(error: unknown) {
    return error instanceof DuplicateProtection;
  }

  function answerDuplicates() {
    const question = duplicateQuestion;
    if (!question) return;
    if (question.candidate) {
      $requestDownload.mutate({ candidate: question.candidate, acknowledged: true });
    } else if (question.requestId) {
      $startDownload.mutate({ requestId: question.requestId, acknowledged: true });
    }
  }

  const requestDownload = createMutation({
    mutationFn: ({
      candidate,
      acknowledged = false
    }: {
      candidate: SourceCandidate;
      acknowledged?: boolean;
    }) => api.requestDownload(releaseID, candidate, acknowledged),
    onError: (error, variables) => asked(error, { candidate: variables.candidate }),
    onSuccess: async () => {
      duplicateQuestion = null;
      await Promise.all([
        queryClient.invalidateQueries({ queryKey: ['releases', releaseID, 'downloads'] }),
        queryClient.invalidateQueries({ queryKey: ['dashboard'] })
      ]);
    }
  });
  const startDownload = createMutation({
    mutationFn: ({
      requestId,
      acknowledged = false
    }: {
      requestId: string;
      acknowledged?: boolean;
    }) => api.startDownload(requestId, acknowledged),
    onError: (error, variables) => asked(error, { requestId: variables.requestId }),
    onSuccess: async () => {
      duplicateQuestion = null;
      await Promise.all([
        queryClient.invalidateQueries({ queryKey: ['releases', releaseID, 'downloads'] }),
        queryClient.invalidateQueries({ queryKey: ['dashboard'] })
      ]);
    }
  });
  const cancelDownload = createMutation({
    mutationFn: (requestId: string) => api.cancelDownload(requestId),
    onSuccess: async () => {
      await Promise.all([
        queryClient.invalidateQueries({ queryKey: ['releases', releaseID, 'downloads'] }),
        queryClient.invalidateQueries({ queryKey: ['dashboard'] })
      ]);
    }
  });

  function quality(candidate: SourceCandidate) {
    const format = candidate.format ? candidate.format.toUpperCase() : 'Unknown format';
    return candidate.averageBitRate ? `${format} · ${candidate.averageBitRate} kbps` : format;
  }

  function duration(milliseconds: number | null) {
    if (milliseconds === null) return '—';
    const totalSeconds = Math.round(milliseconds / 1000);
    return `${Math.floor(totalSeconds / 60)}:${String(totalSeconds % 60).padStart(2, '0')}`;
  }

  // The line under the title, in the order somebody looking at a record reads
  // it: who made it, when, how much of it there is, how long it lasts, and how
  // many discs it spans. Every part is left out rather than guessed at — a
  // release MusicBrainz has no date for says nothing about its year instead of
  // saying an em dash.
  const runtimeMs = $derived(
    ($tracks.data?.items ?? []).reduce((total, track) => total + (track.durationMs ?? 0), 0)
  );

  function runtime(milliseconds: number) {
    const minutes = Math.round(milliseconds / 60000);
    if (minutes < 60) return `${minutes} min`;
    const hours = Math.floor(minutes / 60);
    return `${hours} hr ${minutes % 60} min`;
  }

  const heroFacts = $derived.by(() => {
    const facts: string[] = [];
    const year = $release.data?.firstReleaseDate?.slice(0, 4);
    if (year) facts.push(year);
    const songs = $release.data?.trackCount || ($tracks.data?.items.length ?? 0);
    if (songs) facts.push(songs === 1 ? '1 song' : `${songs} songs`);
    if (runtimeMs > 0) facts.push(runtime(runtimeMs));
    if (($release.data?.mediaCount ?? 0) > 1) facts.push(`${$release.data?.mediaCount} discs`);
    return facts;
  });
</script>

<svelte:head><title>{$release.data?.title ?? 'Release'} · Schall</title></svelte:head>

{#if $release.isError}
  <div class="px-4 sm:px-6 py-6">
    <ErrorNote error={$release.error} retry={() => void $release.refetch()} />
  </div>
{:else}
  <Settle pending={$release.isPending}>
    {#snippet placeholder()}
      <div role="status" aria-label="Loading release">
        <div class="px-4 sm:px-6 pt-6 pb-3">
          <span class="block h-4 w-4 animate-pulse rounded-row bg-surface-regular" aria-hidden="true"
          ></span>
        </div>
        <section class="flex flex-col gap-5 px-4 sm:px-6 pb-2 sm:flex-row sm:items-start" aria-hidden="true">
          <div class="size-40 shrink-0 animate-pulse rounded-row bg-surface-regular"></div>
          <div class="flex min-w-0 flex-1 flex-col gap-2">
            <span class="h-3 w-16 animate-pulse rounded-row bg-surface-regular"></span>
            <span class="h-7 w-72 animate-pulse rounded-row bg-surface-regular"></span>
            <span class="h-4 w-56 animate-pulse rounded-row bg-surface-regular"></span>
            <span class="h-4 w-40 animate-pulse rounded-row bg-surface-regular"></span>
          </div>
        </section>
      </div>
    {/snippet}
  {#if $release.data}
  <div class="px-4 sm:px-6 pt-6 pb-3">
    <BackLink fallback="/library" label="Back to the library" />
  </div>

  <!-- The record, at the size a record is looked at. Cover, then who made it,
       when, how much of it there is, how much of it the library holds, and
       which pressing that is — in that order, because that is the order a
       reader answers "is this the record I want" in. -->
  <section class="flex flex-col gap-5 px-4 sm:px-6 pb-2 sm:flex-row sm:items-start">
    <!-- The sleeve, and the one way to put one there by hand. Some records have
         no picture anywhere — the archives are asked by identifier and answer
         about the release they were asked about — so a release can be blank for
         good, and the person looking at it usually has the sleeve. The control
         sits on the picture and appears on hover, or stands in the empty frame
         when there is none. -->
    <span class="shrink-0">
      {#if !coverMissing}
        <!-- The control is pinned to the picture and not to the block, so the
             caption under an iTunes cover does not push it down. -->
        <span class="group relative block w-fit">
          <Cover
            eager
            onmissing={() => (coverMissing = true)}
            src={coverSrc}
            class="size-40 rounded-row border border-line-thin bg-surface-regular object-cover"
          />
          <span
            class="absolute bottom-2 left-2 opacity-0 transition focus-within:opacity-100 group-hover:opacity-100"
          >
            <Button size="sm" variant="outline" onclick={() => (settingCover = true)}>
              Set cover
            </Button>
          </span>
        </span>
        <!-- A cover found by name is a guess and says so. One found by the
             release's own identifier says nothing, because there is nothing to
             qualify. -->
        <span class="mt-1.5 block h-4 max-w-40 text-meta leading-tight text-ink-4">
          {#if $release.data.coverSource === 'itunes'}
            Cover matched by name
          {:else if $release.data.coverSource === 'embedded'}
            Cover from your copy
          {/if}
        </span>
      {:else}
        <span
          class="grid size-40 place-items-center rounded-row border border-dashed border-line-regular bg-surface-regular"
        >
          <Button size="sm" variant="outline" onclick={() => (settingCover = true)}>
            Set cover
          </Button>
        </span>
      {/if}
    </span>

    <div class="flex min-w-0 flex-1 flex-col gap-1.5">
      <span class="label h-4">{$release.data.albumType}</span>
      <h1 class="min-w-0 break-words text-quiet-display font-semibold text-ink">
        {$release.data.title}
      </h1>
      <p class="flex min-h-5 flex-wrap items-center gap-x-1.5 text-body text-ink-2">
        <a
          href={`/artists/${$release.data.artistId}`}
          class="font-semibold text-ink transition hover:text-accent-soft"
        >
          {$release.data.artistName}
        </a>
        {#each heroFacts as fact (fact)}
          <span class="text-ink-4">·</span>
          <span class="numeric">{fact}</span>
        {/each}
      </p>

      <!-- What MusicBrainz's community voted this release is. A tag rather
           than a filter chip, because a genre is information, not something to
           narrow by. A release with no votes shows nothing. -->
      {#if $release.data.genres?.length}
        <ul class="flex flex-wrap items-center gap-1.5">
          {#each $release.data.genres as genre (genre)}
            <li><StateTag>{genre}</StateTag></li>
          {/each}
        </ul>
      {/if}

      <!-- How much of the record the library holds, over what is missing,
           wanted and dismissed — the same arithmetic the track list below
           counts by, said once at the top rather than read off the rows. -->
      {#if $tracks.isPending}
        <span
          class="mt-1 block h-4 w-40 animate-pulse rounded-row bg-surface-regular"
          role="status"
          aria-label="Loading track summary"
          aria-hidden="true"
        ></span>
      {:else if $tracks.data?.items.length}
        <span class="mt-1 flex flex-wrap items-center gap-x-2 gap-y-1">
          <OwnedBar owned={ownedCount} total={$tracks.data.items.length} width={120} />
          {#if missingCount > 0}
            <span class="numeric text-meta text-ink-3">
              · {missingCount} {$release.data.artistFollowed ? 'missing' : 'not owned'}
            </span>
          {/if}
          {#if wantedCount > 0}
            <span class="numeric text-meta text-ink-3">· {wantedCount} wanted</span>
          {/if}
          {#if dismissedCount > 0}
            <span class="numeric text-meta text-ink-3">· {dismissedCount} dismissed</span>
          {/if}
        </span>
      {/if}

      <!-- The pressing in use, collapsed to the four facts a comparison needs.
           Every other pressing MusicBrainz knows of used to sit here as a block
           each; now they sit behind "Change", because most visits never
           question which one this is. -->
      {#if $release.data.musicbrainzReleaseGroupId}
        <p class="mt-0.5 text-meta text-ink-3">
          {#if selectedEdition}
            Edition · {selectedEdition.releaseDate || '—'} · {selectedEdition.country || '—'} · {selectedEdition.status ||
              '—'} ·
          {:else}
            No pressing selected ·
          {/if}
          <button
            type="button"
            class="text-accent transition hover:text-accent-soft"
            onclick={() => (editionsOpen = !editionsOpen)}
          >
            {editionsOpen ? 'Hide' : 'Change'}
          </button>
        </p>
      {:else}
        <p class="mt-0.5 text-meta text-ink-3">
          This release is not linked to MusicBrainz, so there are no other pressings to compare.
        </p>
      {/if}
    </div>

    <!-- The action row: one primary press, two quiet ones. Find sources used to
         open the "Sources" section further down the page; it now stands beside
         the actions it belongs with. -->
    <div class="flex shrink-0 flex-wrap items-center gap-2 pt-1">
      {#if $tracks.isPending}
        <span class="flex gap-2" role="status" aria-label="Loading track actions">
          <span class="invisible h-8 w-36 rounded-row bg-surface-regular" aria-hidden="true"></span>
          <span class="invisible h-8 w-28 rounded-row bg-surface-regular" aria-hidden="true"></span>
        </span>
      {:else if missingCount > 0}
        <Button disabled={$wantRelease.isPending} onclick={() => $wantRelease.mutate()}>
          {#if $wantRelease.isPending}
            <LoaderCircle size={16} class="animate-spin" />
          {:else}
            <ListPlus size={16} />
          {/if}
          Want {missingCount} missing
        </Button>
        <Button
          variant="ghost"
          disabled={$dismissRemainder.isPending}
          onclick={() => $dismissRemainder.mutate()}
        >
          {#if $dismissRemainder.isPending}
            <LoaderCircle size={16} class="animate-spin" />
          {/if}
          Dismiss {missingCount === 1 ? 'it' : 'the rest'}
        </Button>
      {/if}
      <Button
        variant="ghost"
        onclick={() => findSources(sourcesOpen ? sourceQuery : undefined)}
        disabled={sourcesLoading}
      >
        {#if sourcesLoading}
          <LoaderCircle size={16} class="animate-spin" />
        {:else}
          <Search size={16} />
        {/if}
        Find sources
      </Button>
    </div>
  </section>

  <!-- The answer sits under the button that asked, because "wanted 3 of 12" is
       what somebody pressing it needs to see and a page-level toast would say
       it somewhere else. -->
  {#if $wantRelease.isError}
    <div class="px-4 sm:px-6"><ErrorNote error={$wantRelease.error} /></div>
  {:else if $wantRelease.data}
    <p class="px-4 sm:px-6 text-meta text-ink-3">{wantedSummary($wantRelease.data)}.</p>
  {/if}
  {#if $dismissRemainder.isError}
    <div class="px-4 sm:px-6"><ErrorNote error={$dismissRemainder.error} /></div>
  {:else if $dismissRemainder.data}
    <p class="px-4 sm:px-6 text-meta text-ink-3">
      Dismissed {$dismissRemainder.data.dismissed}
      {$dismissRemainder.data.dismissed === 1 ? 'track' : 'tracks'}.
    </p>
  {/if}

  <div class="flex flex-col gap-4 px-4 sm:px-6 pb-5">
    <!-- Mounted only for a release MusicBrainz knows a release group for —
         the one case with a "Change" to reveal it — and hidden with a class
         rather than an {#if} once it is: the query that fills this in
         fetches on page open, and a component torn down between presses of
         "Change" would ask musicbrainz.org again every time it is reopened. -->
    {#if $release.data.musicbrainzReleaseGroupId}
      <div class:hidden={!editionsOpen}>
        <ReleaseEditions
          {editions}
          selectedId={$release.data.musicbrainzReleaseId}
          selected={selectedEdition}
          loading={$editionList.isLoading}
          error={$editionList.error}
          retryEditions={() => void $editionList.refetch()}
          linked
          barcode={$release.data.barcode ?? ''}
          selectionReason={$release.data.selectionReason ?? ''}
          pending={$selectEdition.isPending}
          selectError={$selectEdition.error}
          onselect={(musicbrainzReleaseId) => $selectEdition.mutate(musicbrainzReleaseId)}
        />
      </div>
    {/if}

    <section>
      {#if $tracks.isError}
        <ErrorNote error={$tracks.error} retry={() => void $tracks.refetch()} />
      {:else if $tracks.isPending}
        <div
          class="max-h-[calc(100dvh-27rem)] overflow-hidden rounded-panel border border-line-thin"
          role="status"
          aria-label="Loading tracks"
        >
          <!-- Fill about 2160px of rows so a 4K viewport does not outgrow the wait. -->
          {#each Array(60) as _, placeholderIndex (placeholderIndex)}
            <div class="h-9 border-b border-line-thin px-3 py-2 last:border-b-0" aria-hidden="true">
              <div class="h-full animate-pulse rounded-row bg-surface-regular"></div>
            </div>
          {/each}
        </div>
      {:else if $tracks.data?.items.length}
        <div class="overflow-hidden rounded-panel border border-line-thin">
          <div class="flex h-7 items-center gap-3.5 border-b border-line-thin px-3">
            <span class="label w-7 shrink-0 text-center">#</span>
            <span class="label min-w-0 flex-1"></span>
            <span class="label w-12 shrink-0 text-right">Length</span>
            <span class="label w-24 shrink-0"></span>
          </div>
          {#each $tracks.data.items as track, index (track.id)}
            <!-- A track the library does not hold is drawn as absent rather than
                 labelled with a state word: the number and the title stand back
                 a step of ink, and nothing on the row says "missing". The words
                 that do appear are decisions somebody took — dismissed, wanted,
                 needs review — and those stay. -->
            <div
              class="flex h-9 items-center gap-3.5 px-3 {index ? 'border-t border-line-thin' : ''}"
            >
              <span
                class="numeric w-7 shrink-0 text-center text-meta {held(track)
                  ? 'text-ink-3'
                  : 'text-ink-4'}"
              >
                {#if $release.data.mediaCount > 1}{track.discNumber}.{/if}{track.trackNumber ?? '—'}
              </span>
              <!-- The title is the thing being identified, so it truncates last;
                   every other column is metadata. -->
              <p
                class="min-w-0 flex-1 truncate text-body font-medium {held(track)
                  ? 'text-ink'
                  : 'text-ink-3'}"
                data-tone={held(track) ? 'present' : 'absent'}
              >
                {track.title}
              </p>
              <span class="numeric w-12 shrink-0 text-right text-meta text-ink-3">
                {duration(track.durationMs)}
              </span>
              <span class="flex w-24 shrink-0 items-center justify-end gap-1.5">
                {#if trackDone(track)}
                  <StateMark role="ok"><Check size={10} strokeWidth={3.2} /></StateMark>
                {:else}
                  {@const tag = track.wantStatus ? trackTag(track.wantStatus) : null}
                  {#if tag}<StateTag tone={tag.tone}>{tag.label}</StateTag>{/if}
                  {#if track.musicbrainzRecordingId}
                    {@const busy = togglingTrack === track.id}
                    {#if track.wantStatus === 'not_wanted'}
                      <Button
                        icon
                        size="xs"
                        variant="ghost"
                        tall
                        disabled={busy}
                        title="Want it after all"
                        aria-label="Want it after all"
                        onclick={() =>
                          $toggleTrack.mutate({
                            kind: 'pursue',
                            trackId: track.id,
                            targetId: track.wantId
                          })}
                      >
                        {#if busy}
                          <LoaderCircle size={13} class="animate-spin" />
                        {:else}
                          <ListPlus size={14} />
                        {/if}
                      </Button>
                    {:else if track.wantStatus}
                      <Button
                        icon
                        size="xs"
                        variant="ghost"
                        tall
                        disabled={busy}
                        title="Not wanted"
                        aria-label="Not wanted"
                        onclick={() => $toggleTrack.mutate({ kind: 'dismiss', trackId: track.id })}
                      >
                        {#if busy}
                          <LoaderCircle size={13} class="animate-spin" />
                        {:else}
                          <Ban size={14} />
                        {/if}
                      </Button>
                    {:else}
                      <Button
                        size="xs"
                        variant="ghost"
                        tall
                        disabled={busy}
                        onclick={() => $toggleTrack.mutate({ kind: 'want', trackId: track.id })}
                      >
                        {#if busy}
                          <LoaderCircle size={13} class="animate-spin" />
                        {:else}
                          <ListPlus size={13} />
                        {/if}
                        Want
                      </Button>
                      <Button
                        icon
                        size="xs"
                        variant="ghost"
                        tall
                        disabled={busy}
                        title="Not wanted"
                        aria-label="Not wanted"
                        onclick={() => $toggleTrack.mutate({ kind: 'dismiss', trackId: track.id })}
                      >
                        <Ban size={14} />
                      </Button>
                    {/if}
                  {/if}
                {/if}
              </span>
            </div>
          {/each}
        </div>
        {#if $toggleTrack.isError}
          <ErrorNote error={$toggleTrack.error} class="mt-3" />
        {/if}
      {:else}
        <div
          class="flex min-h-56 flex-col items-center justify-center rounded-panel border border-line-thin text-center"
        >
          <LoaderCircle size={25} class="animate-spin text-busy" />
          <p class="mt-4 text-body text-ink-2">Importing the track list…</p>
        </div>
      {/if}
    </section>

    <section class="mt-2">
      <span class="label">Sources</span>

      {#if duplicateQuestion}
        <div class="mt-5">
          <DuplicateNotice
            evidence={duplicateQuestion.evidence}
            pending={$requestDownload.isPending || $startDownload.isPending}
            onconfirm={answerDuplicates}
            oncancel={() => (duplicateQuestion = null)}
          />
        </div>
      {/if}

      {#if openRequests.length}
        <div class="mt-5 rounded-panel border border-line-regular bg-ok/14 px-5 py-4">
          <p class="text-body font-semibold text-ok">
            {openRequests.length === 1 ? '1 open request' : `${openRequests.length} open requests`}
          </p>
          <!-- One bordered surface, not two: an open request is a row inside the
               panel that reports it. -->
          <div class="mt-2">
            {#each openRequests as item (item.id)}
              <div class="flex flex-wrap items-center justify-between gap-3 border-b border-line-thin px-3 py-2 last:border-b-0">
                <span class="min-w-0">
                  <span class="block truncate text-body text-ink">{item.directory || item.username}</span>
                  <span class="numeric mt-0.5 block text-meta text-ink-3">
                    {item.username} · {item.fileCount} files · {item.format.toUpperCase() || 'Unknown format'} · {formatBytes(item.totalSizeBytes)}
                    {#if item.status === 'started'}
                      · {item.progress.completedCount}/{item.progress.transferCount} transferred
                    {/if}
                  </span>
                </span>
                <span class="flex items-center gap-2">
                  {#if item.startable}
                    <Button
                      size="sm"
                      disabled={$startDownload.isPending}
                      onclick={() => $startDownload.mutate({ requestId: item.id })}
                    >
                      <Download size={13} /> Start
                    </Button>
                  {/if}
                  <Button
                    variant="ghost"
                    size="sm"
                    disabled={$cancelDownload.isPending}
                    onclick={() => $cancelDownload.mutate(item.id)}
                  >
                    Cancel
                  </Button>
                </span>
              </div>
            {/each}
          </div>
          {#if $cancelDownload.isError}
            <ErrorNote error={$cancelDownload.error} class="mt-3" />
          {/if}
          {#if $startDownload.isError && !isDuplicateQuestion($startDownload.error)}
            <ErrorNote error={$startDownload.error} class="mt-3" />
          {/if}
        </div>
      {/if}

      {#if sourcesOpen}
        <div
          class="mt-5 rounded-panel border border-line-thin p-5"
          style="min-height: 20rem"
        >
          <form class="flex flex-wrap gap-3" onsubmit={(event) => { event.preventDefault(); findSources(sourceQuery); }}>
            <input
              bind:value={sourceQuery}
              aria-label="Search terms"
              placeholder="Search terms"
              class="field min-w-0 w-full flex-1 sm:min-w-56"
            />
            <Button type="submit" variant="ghost" disabled={sourcesLoading}>Search</Button>
          </form>

          {#if sourcesLoading}
            <div
              class="mt-5 max-h-[calc(100dvh-31rem)] flex flex-col gap-2 overflow-hidden"
              role="status"
              aria-label="Loading source results"
            >
              {#each Array(40) as _, placeholderIndex (placeholderIndex)}
                <div class="h-24 animate-pulse rounded-card bg-surface-thick" aria-hidden="true"></div>
              {/each}
              <p class="flex items-center gap-2 text-body text-ink-3">
                <LoaderCircle size={15} class="animate-spin" /> Searching peers for “{sourceQuery}”…
              </p>
            </div>
          {:else if sourcesError}
            <ErrorNote error={sourcesError} class="mt-5" />
          {:else if sources.length}
            <p class="mt-5 text-meta text-ink-4">
              {sources.length} folders matched “{searchedQuery}”
            </p>
            <!-- Rows in the panel, not cards inside it: the panel already is the
                 bordered surface. -->
            <div class="mt-3">
              {#each sources as candidate (sourceKey(candidate))}
                <div class="min-h-24 border-b border-line-thin last:border-b-0">
                  <button
                    class="flex w-full items-center gap-3 px-3 py-2 text-left"
                    onclick={() => (expandedSource = expandedSource === sourceKey(candidate) ? null : sourceKey(candidate))}
                  >
                    <!-- The mark carries corroboration, not the score. The score
                         measures how good a copy is and knows nothing about which
                         release it holds, so showing it here — in a green badge,
                         whatever its value — read as a verdict it cannot give. -->
                    <Chip
                      role={candidate.match.complete ? 'ok' : undefined}
                      dot={false}
                      class="shrink-0"
                      title={candidate.match.summary}
                    >
                      {#if candidate.match.complete}
                        <Check size={11} strokeWidth={3} />
                      {:else if candidate.match.checked}
                        {candidate.match.confirmed}/{candidate.match.expected}
                      {:else}
                        —
                      {/if}
                    </Chip>
                    <span class="min-w-0 flex-1">
                      <span class="block truncate text-body font-medium text-ink">{candidate.directory || candidate.username}</span>
                      <span class="mt-0.5 flex flex-wrap items-center gap-x-3 gap-y-1 text-meta text-ink-3">
                        <span class="flex items-center gap-1"><Users size={12} /> {candidate.username}</span>
                        <span>{quality(candidate)}</span>
                        <span>{candidate.trackCount} tracks</span>
                        <span>{formatBytes(candidate.totalSizeBytes)}</span>
                        <span class={candidate.match.complete ? 'text-ok' : ''}>
                          {candidate.match.summary}
                        </span>
                        {#if candidate.freeUploadSlot}<span class="text-ok">Free slot</span>{/if}
                      </span>
                    </span>
                    <ChevronDown size={16} class="shrink-0 text-ink-4 transition {expandedSource === sourceKey(candidate) ? 'rotate-180' : ''}" />
                  </button>

                  {#if expandedSource === sourceKey(candidate)}
                    <div class="px-3 pb-3">
                      <div class="flex flex-wrap gap-1.5">
                        {#each candidate.reasons as reason (reason)}
                          <Chip>{reason}</Chip>
                        {/each}
                      </div>
                      <div class="mt-3 max-h-64 space-y-1 overflow-auto">
                        {#each candidate.files as file (file.name)}
                          <div class="numeric flex items-center justify-between gap-4 text-meta text-ink-3">
                            <span class="min-w-0 truncate">{file.name}</span>
                            <span class="shrink-0">{formatBytes(file.sizeBytes)}</span>
                          </div>
                        {/each}
                      </div>

                      <div class="mt-3 flex flex-wrap items-center gap-3">
                        {#if requestedKeys.has(sourceKey(candidate))}
                          <span class="flex items-center gap-1.5 text-meta text-ok">
                            <Check size={13} strokeWidth={3} /> Requested
                          </span>
                        {:else}
                          <Button
                            variant="ghost"
                            size="sm"
                            disabled={$requestDownload.isPending}
                            onclick={() => $requestDownload.mutate({ candidate })}
                          >
                            <Download size={13} /> Request
                          </Button>
                        {/if}
                      </div>
                    </div>
                  {/if}
                </div>
              {/each}
            </div>
            {#if $requestDownload.isError && !isDuplicateQuestion($requestDownload.error)}
              <ErrorNote error={$requestDownload.error} class="mt-3" />
            {/if}
          {:else}
            <p class="mt-5 text-body text-ink-2">
              No peer offered a folder matching “{searchedQuery}”. Try different search terms.
            </p>
          {/if}
        </div>
      {/if}
    </section>
  </div>

  <SetCoverDialog
    bind:open={settingCover}
    {releaseID}
    settable={$release.data.coverSource === 'user'}
    onsaved={coverSet}
  />
  {/if}
  </Settle>
{/if}

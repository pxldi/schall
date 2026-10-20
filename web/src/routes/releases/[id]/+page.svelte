<script lang="ts">
  import Skeleton from '$lib/components/Skeleton.svelte';
  import { onMount, tick } from 'svelte';
  import { toStore } from 'svelte/store';
  import { page } from '$app/state';
  import { createMutation, createQuery, useQueryClient } from '@tanstack/svelte-query';
  import {
    api,
    DuplicateProtection,
    type DuplicateEvidence,
    type SourceCandidate,
    type Track
  } from '$lib/api';
  import { calendarDate, formatBytes, removedSummary, wantedSummary } from '$lib/utils';
  import { holding, progress, quiet } from '$lib/vocabulary';
  import { goto } from '$app/navigation';
  import { useKeys } from '$lib/keys.svelte';
  import { glide } from '$lib/motion.svelte';
  import { STATE_GLYPH } from '$lib/states';
  import ConfirmDialog from '$lib/components/ConfirmDialog.svelte';
  import Keycap from '$lib/components/Keycap.svelte';
  import PageHeader from '$lib/components/PageHeader.svelte';
  import Button from '$lib/components/Button.svelte';
  import Icon from '$lib/components/Icon.svelte';
  import WantToggle from '$lib/components/WantToggle.svelte';
  import Cover from '$lib/components/Cover.svelte';
  import DuplicateNotice from '$lib/components/DuplicateNotice.svelte';
  import OwnedBar from '$lib/components/OwnedBar.svelte';
  import ReleaseEditions from '$lib/components/ReleaseEditions.svelte';
  import SetCoverDialog from '$lib/components/SetCoverDialog.svelte';
  import StateIcon from '$lib/components/StateIcon.svelte';
  import type { IconName } from '$lib/icons';
  import EmptyPanel from '$lib/components/EmptyPanel.svelte';
  import ErrorNote from '$lib/components/ErrorNote.svelte';
  import Settle from '$lib/components/Settle.svelte';
  import ArtistReleases from './ArtistReleases.svelte';
  import { motionMs } from '$lib/motion.svelte';

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
  // The skeleton draws the release's own track count where the release has one,
  // capped so a box set does not draw hundreds of boxes.
  const trackSkeletonCount = $derived(Math.min($release.data?.trackCount || 12, 40));
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
  // Tracks whose file is waiting to be deleted because somebody removed them,
  // and the first day one goes.
  const leaving = $derived($tracks.data?.items.filter((track) => track.removesAt) ?? []);
  const firstLeaving = $derived(leaving.map((track) => track.removesAt ?? '').sort()[0]);
  // Missing tracks nobody has asked for yet. Wanting the release only adds
  // these, so the button counts them: after wanting the last missing track it
  // said "Want 1 missing" for a track that was already wanted.
  // A missing track whose want already settled is not asked for again by the
  // button either: the server answers it with the settled want.
  const unwantedCount = $derived(
    missingCount -
      wantedCount -
      ($tracks.data?.items.filter((track) => !held(track) && track.wantStatus === 'acquired')
        .length ?? 0)
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
  // Removing takes the wants away at once. A file Schall downloaded for the
  // release is deleted 30 days later, and wanting it again before then keeps
  // it. Files somebody added by hand stay.
  const removeRelease = createMutation({
    mutationFn: () => api.removeRelease(releaseID),
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
  // being decided about. Wanting a removed track takes the removal back, and
  // keeps its file if one was waiting to be deleted.
  type TrackToggle = { kind: 'want' | 'remove'; trackId: string };
  const toggleTrack = createMutation({
    mutationFn: async (toggle: TrackToggle): Promise<unknown> =>
      toggle.kind === 'want' ? api.wantTrack(toggle.trackId) : api.removeTrack(toggle.trackId),
    onSuccess: async () => {
      await Promise.all([
        queryClient.invalidateQueries({ queryKey: ['releases'] }),
        queryClient.invalidateQueries({ queryKey: ['artists'] }),
        queryClient.invalidateQueries({ queryKey: ['acquisition-targets'] }),
        queryClient.invalidateQueries({ queryKey: ['dashboard'] })
      ]);
    }
  });
  // Wanted for the toggle: somebody asked for it and it is still being looked
  // for. A dismissal is the other answer, and an acquired one is settled.
  function isWanted(track: Track) {
    return Boolean(
      track.wantStatus && track.wantStatus !== 'not_wanted' && track.wantStatus !== 'acquired'
    );
  }
  // A track proven in the library draws the tick. It used to be drawn for a
  // settled want too, and a want can settle on a file the library no longer
  // maps: 88GLAM 2.5 said "2 missing" in its header over sixteen ticks.
  function trackDone(track: Track) {
    return held(track);
  }
  // That case on its own: Schall fetched the track once and holds no file for
  // it now. Wanting it again answers with the settled want and changes
  // nothing, so the row offers no + and says what it knows instead.
  function lost(track: Track) {
    return !held(track) && track.wantStatus === 'acquired';
  }
  // The tag a track's want status draws in the row: the same words the rest of
  // the product uses for what is happening to a thing, read quietly because
  // they sit inside a row rather than beside a heading. "needs review" is the
  // one that asks for a person, so it is the one tone that stands out.
  //
  // Every state but "needs review" is an icon, with the word as its tooltip:
  // a column of words in boxes was more text than the track list.
  function trackTag(
    status: string
  ): { label: string; tone: 'neutral' | 'attention'; icon?: IconName } | null {
    switch (status) {
      case 'awaiting_review':
        return { label: quiet(progress.needsReview), tone: 'attention' };
      case 'not_wanted':
        return { label: quiet(progress.dismissed), tone: 'neutral', icon: 'dismiss' };
      case 'searching':
        return { label: quiet(progress.searching), tone: 'neutral', icon: 'search' };
      case 'acquired':
        return null;
      default:
        return { label: quiet(progress.wanted), tone: 'neutral', icon: 'want' };
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
  let sourcesPanel = $state<HTMLElement>();

  // Source searches reach out to the Soulseek network, so they only run when
  // the user asks for them.
  async function findSources(override?: string) {
    sourcesOpen = true;
    sourcesLoading = true;
    sourcesError = null;
    expandedSource = null;
    // The panel sits under the whole track list, so a press in the header
    // changed nothing in sight until the panel was brought to it.
    void tick().then(() =>
      sourcesPanel?.scrollIntoView?.({
        block: 'start',
        behavior: motionMs('surface') > 0 ? 'smooth' : 'auto'
      })
    );
    try {
      const results = await api.releaseSources(releaseID, override?.trim() || undefined);
      // An empty answer is no answer. Read as an empty list it would say that
      // no peer has the release, which nobody asked.
      if (!results) throw new Error('The search ended without an answer. Try again later.');
      sources = results.items ?? [];
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

  // The track list's cursor (ADR 0042): none until the reader presses J or K,
  // then one inverted row that glides between tracks.
  let cursor = $state(-1);
  let trackList = $state<HTMLElement>();
  const trackItems = $derived($tracks.data?.items ?? []);

  function moveCursor(by: number) {
    if (!trackItems.length) return;
    cursor = cursor < 0 ? 0 : Math.min(Math.max(cursor + by, 0), trackItems.length - 1);
    void tick().then(() =>
      trackList?.querySelector('[data-cursor="true"]')?.scrollIntoView?.({ block: 'nearest' })
    );
  }

  // Removing from the keyboard asks first: Backspace is pressed by accident
  // far more often than the Remove button is.
  let confirmingRemove = $state(false);
  const removable = $derived(ownedCount - leaving.length > 0 || wantedCount > 0);

  useKeys(() => [
    {
      keys: ['j', 'k'],
      caps: ['j', 'k'],
      verb: 'move',
      order: 0,
      enabled: () => trackItems.length > 0,
      run: (event) => moveCursor(event.key.toLowerCase() === 'j' ? 1 : -1)
    },
    {
      keys: ['backspace'],
      verb: 'remove',
      order: 40,
      enabled: () => !$tracks.isPending && removable && !$removeRelease.isPending,
      run: () => (confirmingRemove = true)
    }
  ]);
</script>

<svelte:head><title>{$release.data?.title ?? 'Release'} · Schall</title></svelte:head>

{#if $release.isError}
  <div class="px-5 py-6">
    <ErrorNote error={$release.error} retry={() => void $release.refetch()} />
  </div>
{:else}
  <Settle pending={$release.isPending}>
    {#snippet placeholder()}
      <!-- The frame the release draws: the title spot and the list on the
           left, the inspector on the right. -->
      <div role="status" aria-label="Loading release" class="grid lg:grid-cols-[minmax(0,1fr)_21.25rem]">
        <div class="flex flex-col gap-2 px-5 pt-3.5" aria-hidden="true">
          <Skeleton class="h-3 w-40" />
          <Skeleton class="h-8 w-[24rem] max-w-full" />
          <Skeleton class="h-4 w-64" />
          <div class="mt-6 flex flex-col">
            {#each Array(8) as _, placeholderIndex (placeholderIndex)}
              <div class="h-9 border-b border-rule py-2"><Skeleton class="h-full" /></div>
            {/each}
          </div>
        </div>
        <div class="hidden border-l border-rule bg-panel p-5 lg:block" aria-hidden="true">
          <Skeleton shape="cover" class="w-[18.75rem]" />
        </div>
      </div>
    {/snippet}
    {#if $release.data}
      {@const data = $release.data}
      <div class="grid items-start lg:grid-cols-[minmax(0,1fr)_21.25rem]">
        <div class="flex min-w-0 flex-col pb-8">
          <PageHeader
            title={data.title}
            path={[
              { label: 'Library', href: '/library' },
              { label: data.artistName, href: `/artists/${data.artistId}` }
            ]}
          >
            {#snippet sub()}
              <a href={`/artists/${data.artistId}`} class="text-ink transition hover:underline">
                {data.artistName}
              </a>
              <span class="mono text-meta text-ink-3">
                {[data.albumType ? data.albumType[0].toUpperCase() + data.albumType.slice(1) : '', ...heroFacts]
                  .filter(Boolean)
                  .join(' · ')}
              </span>
            {/snippet}
          </PageHeader>

          <!-- The actions, each with the key that presses it. The one primary
               press is wanting what is missing. -->
          <div class="flex min-h-[1.875rem] flex-wrap items-center gap-1.5 px-5 pt-4">
            {#if $tracks.isPending}
              <span class="flex gap-1.5" role="status" aria-label="Loading track actions">
                <span class="invisible h-[1.875rem] w-24" aria-hidden="true"></span>
              </span>
            {:else}
              {#if missingCount > 0 && unwantedCount > 0}
                <Button
                  key="w"
                  verb="want"
                  aria-label={`Want ${unwantedCount} missing`}
                  disabled={$wantRelease.isPending}
                  onclick={() => $wantRelease.mutate()}
                >
                  <Icon
                    name={$wantRelease.isPending ? 'busy' : 'plus'}
                    size="sm"
                    class={$wantRelease.isPending ? 'animate-spin' : ''}
                  />
                  Want {unwantedCount}
                </Button>
              {/if}
            {/if}
            <Button
              variant="outline"
              key="f"
              verb="search sources"
              aria-label="Find sources"
              title="Find sources"
              onclick={() => findSources(sourcesOpen ? sourceQuery : undefined)}
              disabled={sourcesLoading}
            >
              <Icon
                name={sourcesLoading ? 'busy' : 'search'}
                size="sm"
                class={sourcesLoading ? 'animate-spin' : ''}
              />
              Search
            </Button>
            {#if !$tracks.isPending}
              {#if missingCount > 0}
                <Button
                  variant="outline"
                  key="n"
                  verb="not wanted"
                  title={`Dismiss the ${missingCount} missing`}
                  aria-label={missingCount === 1 ? 'Dismiss it' : 'Dismiss the rest'}
                  disabled={$dismissRemainder.isPending}
                  onclick={() => $dismissRemainder.mutate()}
                >
                  <Icon
                    name={$dismissRemainder.isPending ? 'busy' : 'dismiss'}
                    size="sm"
                    class={$dismissRemainder.isPending ? 'animate-spin' : ''}
                  />
                  Not wanted
                </Button>
              {/if}
              {#if removable}
                <!-- Pressed, it removes at once. Its key asks first. -->
                <Button
                  variant="outline"
                  aria-label="Remove"
                  title="Remove"
                  aria-keyshortcuts="Backspace"
                  disabled={$removeRelease.isPending}
                  onclick={() => $removeRelease.mutate()}
                >
                  <Icon
                    name={$removeRelease.isPending ? 'busy' : 'delete'}
                    size="sm"
                    class={$removeRelease.isPending ? 'animate-spin' : ''}
                  />
                  Remove
                  <Keycap key="backspace" class="-mr-1" />
                </Button>
              {/if}
            {/if}
          </div>

          <!-- The answer sits under the button that asked. -->
          <div class="flex flex-col gap-2 px-5 empty:hidden [&:not(:empty)]:pt-3">
            {#if $wantRelease.isError}
              <ErrorNote error={$wantRelease.error} />
            {:else if $wantRelease.data}
              <p class="text-meta text-ink-3">{wantedSummary($wantRelease.data)}.</p>
            {/if}
            {#if $removeRelease.isError}
              <ErrorNote error={$removeRelease.error} />
            {:else if $removeRelease.data}
              <p class="text-meta text-ink-3">{removedSummary($removeRelease.data)}.</p>
            {/if}
            {#if $dismissRemainder.isError}
              <ErrorNote error={$dismissRemainder.error} />
            {:else if $dismissRemainder.data}
              <p class="text-meta text-ink-3">
                Dismissed {$dismissRemainder.data.dismissed}
                {$dismissRemainder.data.dismissed === 1 ? 'track' : 'tracks'}.
              </p>
            {/if}
          </div>

          <!-- Mounted only for a release MusicBrainz knows a release group for,
               and hidden with a class rather than an {#if} once it is: the
               pressings are fetched on page open, and a component torn down
               between presses of "Change" would ask musicbrainz.org again. -->
          {#if data.musicbrainzReleaseGroupId}
            <div class="px-5" class:hidden={!editionsOpen}>
              <ReleaseEditions
                {editions}
                selectedId={data.musicbrainzReleaseId}
                selected={selectedEdition}
                loading={$editionList.isLoading}
                error={$editionList.error}
                retryEditions={() => void $editionList.refetch()}
                linked
                barcode={data.barcode ?? ''}
                selectionReason={data.selectionReason ?? ''}
                pending={$selectEdition.isPending}
                selectError={$selectEdition.error}
                onselect={(musicbrainzReleaseId) => $selectEdition.mutate(musicbrainzReleaseId)}
              />
            </div>
          {/if}

          <section class="px-5 pt-5" aria-label="Tracks">
            {#if $tracks.isError}
              <ErrorNote error={$tracks.error} retry={() => void $tracks.refetch()} class="mb-3" />
            {/if}
            <!-- The list polls while a refresh runs, so a failed poll keeps the
                 rows it already drew under the note. -->
            {#if !$tracks.isError || $tracks.data}
              <Settle pending={$tracks.isPending}>
                {#snippet placeholder()}
                  <div class="max-h-[calc(100dvh-20rem)] overflow-hidden" role="status" aria-label="Loading tracks">
                    {#each Array(trackSkeletonCount) as _, placeholderIndex (placeholderIndex)}
                      <div class="h-9 border-b border-rule py-2" aria-hidden="true">
                        <Skeleton class="h-full" />
                      </div>
                    {/each}
                  </div>
                {/snippet}
                {#if $tracks.data?.items.length}
                  <div class="relative" bind:this={trackList} use:glide={{ key: cursor }}>
                    <table class="w-full border-collapse">
                      <thead>
                        <tr class="border-b border-rule-strong">
                          <th class="label h-7 w-12 px-2.5 text-left font-normal">#</th>
                          <th class="label h-7 px-2.5 text-left font-normal">Title</th>
                          <th class="w-8"><span class="sr-only">Want</span></th>
                          <th class="label h-7 w-20 px-2.5 text-right font-normal">Length</th>
                          <th class="label h-7 w-28 px-2.5 text-left font-normal">State</th>
                        </tr>
                      </thead>
                      <tbody>
                        {#each $tracks.data.items as track, index (track.id)}
                          <!-- A track the library does not hold stands back a step
                               of ink, and nothing on the row says "missing". The
                               words that do appear are decisions somebody took. -->
                          <tr
                            data-track={track.id}
                            data-cursor={index === cursor}
                            class="row want-row {index === cursor ? 'on-ink' : ''}"
                            onclick={() => (cursor = index)}
                          >
                            <td
                              class="mono h-9 px-2.5 text-meta {held(track) ? 'text-ink-3' : 'text-ink-4'}"
                            >
                              {#if data.mediaCount > 1}{track.discNumber}.{/if}{String(track.trackNumber ?? '—').padStart(2, '0')}
                            </td>
                            <td class="max-w-0 px-2.5">
                              <p
                                class="truncate text-body {held(track) ? 'text-ink' : 'text-ink-3'}"
                                data-tone={held(track) ? 'present' : 'absent'}
                              >
                                {track.title}
                              </p>
                            </td>
                            <!-- The + shows on the hovered, focused or cursor row;
                                 a wanted track keeps its filled tick. -->
                            <td class="px-1">
                              <span class="flex items-center justify-center">
                                {#if track.musicbrainzRecordingId && !lost(track)}
                                  <WantToggle
                                    reveal={trackDone(track) ? 'all' : true}
                                    wanted={trackDone(track) ? !track.removesAt : isWanted(track)}
                                    title={track.title}
                                    onwant={() => $toggleTrack.mutateAsync({ kind: 'want', trackId: track.id })}
                                    onunwant={() => $toggleTrack.mutateAsync({ kind: 'remove', trackId: track.id })}
                                  />
                                {/if}
                              </span>
                            </td>
                            <td class="mono px-2.5 text-right text-meta text-ink-3">
                              {duration(track.durationMs)}
                            </td>
                            <td class="px-2.5">
                              <span class="flex items-center gap-1.5">
                                {#if track.removesAt}
                                  <!-- Removed: the file is still here and goes on this day. -->
                                  <StateIcon
                                    icon="delete"
                                    label={`Deleted on ${calendarDate(track.removesAt)}`}
                                    note="Want it again to keep it"
                                  />
                                  <span class="mono whitespace-nowrap text-meta text-ink-3">
                                    {calendarDate(track.removesAt)}
                                  </span>
                                {:else if trackDone(track)}
                                  <StateIcon icon={STATE_GLYPH.ok} label={quiet(holding.complete)} role="ok" />
                                {:else if lost(track)}
                                  <StateIcon
                                    icon="alert"
                                    label="Fetched once, file gone"
                                    note="Find sources to get it again"
                                  />
                                {:else}
                                  {@const tag = track.wantStatus ? trackTag(track.wantStatus) : null}
                                  {#if tag?.icon}
                                    <StateIcon
                                      icon={tag.icon}
                                      label={tag.label}
                                      role={tag.icon === 'search' ? 'busy' : 'idle'}
                                    />
                                  {:else if tag}
                                    <StateIcon icon={STATE_GLYPH.decide} label={tag.label} role="decide" />
                                    <span class="text-meta text-decide">{tag.label}</span>
                                  {/if}
                                {/if}
                              </span>
                            </td>
                          </tr>
                        {/each}
                      </tbody>
                    </table>
                  </div>
                  {#if $toggleTrack.isError}
                    <ErrorNote error={$toggleTrack.error} class="mt-3" />
                  {/if}
                {:else if waiting(data.trackRefreshStatus)}
                  <div class="flex min-h-40 items-center justify-center gap-3 bg-panel">
                    <Icon name="busy" size="md" class="animate-spin text-busy" />
                    <p class="text-body text-ink-2">Importing the track list…</p>
                  </div>
                {:else}
                  <!-- No refresh is waiting or running, so nothing will fill
                       this in by itself. -->
                  <EmptyPanel
                    role={data.trackRefreshStatus === 'failed' ? 'fail' : 'idle'}
                    heading="No track list"
                  >
                    <p class="text-body text-ink-2">
                      {data.trackRefreshStatus === 'failed'
                        ? 'Reading the track list from MusicBrainz failed.'
                        : data.trackRefreshStatus === 'completed'
                          ? 'MusicBrainz lists no tracks for this release.'
                          : 'This release has no MusicBrainz track list.'}
                    </p>
                  </EmptyPanel>
                {/if}
              </Settle>
            {/if}
          </section>

          <!-- While sources are open the section is at least a screen tall, so
               the page keeps its length when a short answer replaces the
               placeholders. -->
          <section
            bind:this={sourcesPanel}
            class="scroll-mt-12 px-5 pt-6"
            style={sourcesOpen ? 'min-height: calc(100dvh - 4.5rem)' : undefined}
          >
            <!-- The heading names what is under it, so it waits until there is
                 something: alone, it read as a section that failed to load. -->
            {#if duplicateQuestion || openRequests.length || sourcesOpen || $downloads.isError}
              <h2 class="flex items-baseline gap-2.5 border-b border-rule-strong pb-1.5">
                <span class="label">Sources</span>
                {#if openRequests.length}
                  <span class="mono text-micro text-ink-3">{openRequests.length} open</span>
                {/if}
              </h2>
            {/if}

            {#if $downloads.isError}
              <!-- A failed read would otherwise look like a release with no open
                   requests, and offer folders that may already be requested. -->
              <ErrorNote error={$downloads.error} retry={() => void $downloads.refetch()} class="mt-3" />
            {/if}

            {#if duplicateQuestion}
              <div class="mt-3">
                <DuplicateNotice
                  evidence={duplicateQuestion.evidence}
                  pending={$requestDownload.isPending || $startDownload.isPending}
                  onconfirm={answerDuplicates}
                  oncancel={() => (duplicateQuestion = null)}
                />
              </div>
            {/if}

            {#each openRequests as item (item.id)}
              {@const transferred = item.progress.transferCount
                ? item.progress.completedCount / item.progress.transferCount
                : 0}
              <div class="grid grid-cols-[1.25rem_minmax(0,1fr)_auto] items-center gap-3 border-b border-rule py-2.5">
                <Icon
                  name={item.status === 'started' ? STATE_GLYPH.busy : 'download'}
                  size="sm"
                  class={item.status === 'started' ? 'animate-spin text-busy' : 'text-ink-3'}
                />
                <span class="min-w-0">
                  <span class="mono block truncate text-meta text-ink">{item.directory || item.username}</span>
                  <span class="mt-0.5 block text-meta text-ink-3">
                    {item.username} · {item.fileCount} files · {item.format.toUpperCase() || 'Unknown format'} ·
                    <span class="mono">{formatBytes(item.totalSizeBytes)}</span>
                  </span>
                </span>
                <span class="flex items-center gap-3">
                  {#if item.status === 'started'}
                    <span class="flex flex-col items-end gap-1">
                      <span class="mono text-micro text-busy">
                        {item.progress.completedCount}/{item.progress.transferCount}
                      </span>
                      <span class="block h-0.5 w-20 bg-rule-strong" aria-hidden="true">
                        <span
                          class="block h-full origin-left bg-busy transition-transform"
                          style="transform: scaleX({transferred})"
                        ></span>
                      </span>
                    </span>
                  {/if}
                  {#if item.startable}
                    <Button
                      size="sm"
                      disabled={$startDownload.isPending}
                      onclick={() => $startDownload.mutate({ requestId: item.id })}
                    >
                      <Icon name="download" size="sm" /> Start
                    </Button>
                  {/if}
                  <Button
                    variant="outline"
                    size="sm"
                    disabled={$cancelDownload.isPending}
                    onclick={() => $cancelDownload.mutate(item.id)}
                  >
                    Cancel
                  </Button>
                </span>
              </div>
            {/each}
            {#if $cancelDownload.isError}
              <ErrorNote error={$cancelDownload.error} class="mt-3" />
            {/if}
            {#if $startDownload.isError && !isDuplicateQuestion($startDownload.error)}
              <ErrorNote error={$startDownload.error} class="mt-3" />
            {/if}

            {#if sourcesOpen}
              <div class="pt-3" style={sourcesLoading ? 'min-height: 20rem' : undefined}>
                <form
                  class="flex flex-wrap gap-1.5"
                  onsubmit={(event) => {
                    event.preventDefault();
                    findSources(sourceQuery);
                  }}
                >
                  <input
                    bind:value={sourceQuery}
                    aria-label="Search terms"
                    placeholder="Search terms"
                    class="field w-full min-w-0 flex-1 sm:min-w-56"
                  />
                  <Button type="submit" variant="outline" disabled={sourcesLoading}>Search</Button>
                </form>

                {#if sourcesLoading}
                  <div class="mt-4 flex flex-col" role="status" aria-label="Loading source results">
                    <p class="flex items-center gap-2 pb-2 text-body text-ink-3">
                      <Icon name="busy" size="sm" class="animate-spin text-busy" /> Searching peers for {sourceQuery
                        ? `“${sourceQuery}”`
                        : `${data.artistName ?? ''} – ${data.title ?? ''}`}…
                    </p>
                    {#each Array(4) as _, placeholderIndex (placeholderIndex)}
                      <div class="h-14 border-b border-rule py-2.5"><Skeleton class="h-full" /></div>
                    {/each}
                  </div>
                {:else if sourcesError}
                  <ErrorNote error={sourcesError} class="mt-4" />
                {:else if sources.length}
                  <p class="mt-4 pb-1.5 text-meta text-ink-4">
                    {sources.length === 1 ? '1 folder' : `${sources.length} folders`} matched “{searchedQuery}”
                  </p>
                  <div class="border-t border-rule">
                    {#each sources as candidate (sourceKey(candidate))}
                      {@const open = expandedSource === sourceKey(candidate)}
                      <div class="border-b border-rule">
                        <button
                          class="grid w-full grid-cols-[2.5rem_minmax(0,1fr)_auto] items-center gap-3 py-2.5 text-left transition hover:bg-raised"
                          aria-expanded={open}
                          onclick={() => (expandedSource = open ? null : sourceKey(candidate))}
                        >
                          <!-- The mark carries corroboration, not the score: the
                               score measures how good a copy is and knows nothing
                               about which release it holds. -->
                          <span class="flex items-center justify-center" title={candidate.match.summary}>
                            {#if candidate.match.complete}
                              <Icon name={STATE_GLYPH.ok} size="sm" class="text-ok" aria-label="Every track found" />
                            {:else if candidate.match.checked}
                              <span class="mono text-micro text-ink-3">
                                {candidate.match.confirmed}/{candidate.match.expected}
                              </span>
                            {:else}
                              <Icon name={STATE_GLYPH.idle} size="sm" class="text-idle" aria-label="Not checked" />
                            {/if}
                          </span>
                          <span class="min-w-0">
                            <span class="mono block truncate text-meta text-ink">
                              {candidate.directory || candidate.username}
                            </span>
                            <span class="mt-0.5 flex flex-wrap items-center gap-x-3 gap-y-1 text-meta text-ink-3">
                              <span class="flex items-center gap-1"><Icon name="peer" size="sm" /> {candidate.username}</span>
                              <span>{quality(candidate)}</span>
                              <span>{candidate.trackCount === 1 ? '1 track' : `${candidate.trackCount} tracks`}</span>
                              <span class="mono">{formatBytes(candidate.totalSizeBytes)}</span>
                              <span class={candidate.match.complete ? 'text-ok' : ''}>{candidate.match.summary}</span>
                              {#if candidate.freeUploadSlot}
                                <span class="flex items-center gap-1 text-ok">
                                  <Icon name={STATE_GLYPH.ok} size={12} /> Free slot
                                </span>
                              {/if}
                            </span>
                          </span>
                          <Icon
                            name="chevron-down"
                            size="sm"
                            class="mr-1 shrink-0 text-ink-4 transition {open ? 'rotate-180' : ''}"
                          />
                        </button>

                        {#if open}
                          <div class="pb-3 pl-[3.25rem]">
                            {#if candidate.reasons.length}
                              <p class="text-meta text-ink-3">{candidate.reasons.join(' · ')}</p>
                            {/if}
                            <div class="mt-2 max-h-64 overflow-auto">
                              {#each candidate.files as file (file.name)}
                                <div class="mono flex items-center justify-between gap-4 text-micro text-ink-3">
                                  <span class="min-w-0 truncate">{file.name}</span>
                                  <span class="shrink-0">{formatBytes(file.sizeBytes)}</span>
                                </div>
                              {/each}
                            </div>
                            <div class="mt-3 flex flex-wrap items-center gap-3">
                              {#if requestedKeys.has(sourceKey(candidate))}
                                <span class="flex items-center gap-1.5 text-meta text-ok">
                                  <Icon name={STATE_GLYPH.ok} size="sm" /> Requested
                                </span>
                              {:else}
                                <Button
                                  variant="outline"
                                  size="sm"
                                  disabled={$requestDownload.isPending}
                                  onclick={() => $requestDownload.mutate({ candidate })}
                                >
                                  <Icon name="download" size="sm" /> Request
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
                  <p class="mt-4 text-body text-ink-2">
                    No peer offered a folder matching “{searchedQuery}”. Try different search terms.
                  </p>
                {/if}
              </div>
            {/if}
          </section>
        </div>

        <!-- The inspector: the sleeve, the facts as keys and values, and the
             artist's other records one press away. -->
        <aside
          class="flex min-w-0 flex-col gap-5 border-rule bg-panel p-5 max-lg:border-t lg:sticky lg:top-10 lg:max-h-[calc(100dvh-4.5rem)] lg:min-h-[calc(100dvh-4.5rem)] lg:overflow-y-auto lg:border-l"
          aria-label="About this release"
        >
          <!-- Some records have no picture anywhere, and the person looking at
               one usually has the sleeve, so a cover can be set by hand. -->
          <span class="group relative block aspect-square w-full max-w-[18.75rem] overflow-hidden">
            <Cover
              eager
              onmissing={() => (coverMissing = true)}
              src={coverSrc}
              seed={releaseID}
              class="size-full object-cover"
            />
          </span>

          <dl class="grid grid-cols-[5.5rem_minmax(0,1fr)] gap-x-3 gap-y-2.5 text-body">
            <dt class="label pt-0.5">Owned</dt>
            <dd class="text-ink-2">
              {#if $tracks.isPending}
                <span class="skeleton block h-4 w-32" role="status" aria-label="Loading track summary"></span>
              {:else if $tracks.data?.items.length}
                <span class="flex flex-wrap items-center gap-x-2 gap-y-1">
                  <OwnedBar owned={ownedCount} total={$tracks.data.items.length} />
                  {#if missingCount > 0}
                    <span class="mono whitespace-nowrap text-meta text-ink-3">
                      · {missingCount} {data.artistFollowed ? 'missing' : 'not owned'}
                    </span>
                  {/if}
                  {#if wantedCount > 0}
                    <span class="mono whitespace-nowrap text-meta text-ink-3">· {wantedCount} wanted</span>
                  {/if}
                  {#if dismissedCount > 0}
                    <span class="mono whitespace-nowrap text-meta text-ink-3">· {dismissedCount} dismissed</span>
                  {/if}
                  {#if leaving.length > 0}
                    <span class="mono whitespace-nowrap text-meta text-ink-3">
                      · {leaving.length} deleted on {calendarDate(firstLeaving)}
                    </span>
                  {/if}
                </span>
              {:else}
                <span class="text-ink-4">No tracks</span>
              {/if}
            </dd>

            <dt class="label pt-0.5">Edition</dt>
            <dd class="text-ink-2">
              {#if data.musicbrainzReleaseGroupId}
                <span class="flex flex-wrap items-center gap-x-2 gap-y-1.5">
                  {#if selectedEdition}
                    <span class="mono text-meta">
                      {selectedEdition.releaseDate || '—'} · {selectedEdition.country || '—'} · {selectedEdition.status ||
                        '—'}
                    </span>
                  {:else}
                    <span class="text-meta">No pressing selected</span>
                  {/if}
                  <Button
                    variant="outline"
                    size="xs"
                    key="e"
                    verb="change edition"
                    onclick={() => (editionsOpen = !editionsOpen)}
                  >
                    {editionsOpen ? 'Hide' : 'Change'}
                  </Button>
                </span>
              {:else}
                <span class="text-meta text-ink-3">
                  This release is not linked to MusicBrainz, so there are no other pressings to compare.
                </span>
              {/if}
            </dd>

            <dt class="label pt-0.5">Cover</dt>
            <dd class="flex flex-wrap items-center gap-x-2 gap-y-1.5 text-meta text-ink-2">
              <!-- A cover found by name is a guess and says so. -->
              <span>
                {#if coverMissing}
                  No cover yet
                {:else if data.coverSource === 'itunes'}
                  Matched by name
                {:else if data.coverSource === 'embedded'}
                  From your copy
                {/if}
              </span>
              <Button variant="ghost" size="xs" title="Set cover" aria-label="Set cover" onclick={() => (settingCover = true)}>
                <Icon name="image" size="sm" /> Set
              </Button>
            </dd>

            {#if data.genres?.length}
              <!-- What MusicBrainz's community voted this release is. -->
              <dt class="label pt-0.5">Genre</dt>
              <dd class="text-meta text-ink-2">{data.genres.join(', ')}</dd>
            {/if}

            <dt class="label pt-0.5">Artist</dt>
            <dd class="flex flex-wrap items-center gap-x-2 gap-y-1.5 text-ink-2">
              <span class="truncate">{data.artistName}</span>
              <Button variant="outline" size="xs" key="a" verb="artist" href={`/artists/${data.artistId}`}>
                Artist
              </Button>
            </dd>
          </dl>

          <ArtistReleases artistID={data.artistId} artistName={data.artistName} {releaseID} />
        </aside>
      </div>

      <SetCoverDialog
        bind:open={settingCover}
        {releaseID}
        settable={data.coverSource === 'user'}
        onsaved={coverSet}
      />
      <ConfirmDialog
        bind:open={confirmingRemove}
        title="Remove this release?"
        description="Its wants stop now. Files Schall downloaded for it are deleted in 30 days unless you want them again."
        action="Remove"
        danger
        onconfirm={() => $removeRelease.mutate()}
      />
    {/if}
  </Settle>
{/if}

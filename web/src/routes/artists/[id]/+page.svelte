<script lang="ts">
  import { page } from '$app/state';
  import { goto } from '$app/navigation';
  import {
    createMutation,
    createQuery,
    keepPreviousData,
    queryOptions,
    useQueryClient
  } from '@tanstack/svelte-query';
  import { toStore } from 'svelte/store';
  import { ChevronRight, ListPlus, LoaderCircle, RefreshCw, UserRoundMinus } from '@lucide/svelte';
  import { api, type MonitorLevel, type Release, type ReleaseCompleteness } from '$lib/api';
  import { calendarDate, keepInUrl, urlChoice, wantedSummary } from '$lib/utils';
  import BackLink from '$lib/components/BackLink.svelte';
  import Button from '$lib/components/Button.svelte';
  import EmptyPanel from '$lib/components/EmptyPanel.svelte';
  import { holding, progress, quiet } from '$lib/vocabulary';
  import Chip from '$lib/components/Chip.svelte';
  import Cover from '$lib/components/Cover.svelte';
  import ControlRail from '$lib/components/ControlRail.svelte';
  import PillSelect from '$lib/components/PillSelect.svelte';
  import Segmented from '$lib/components/Segmented.svelte';
  import StateTag from '$lib/components/StateTag.svelte';
  import ErrorNote from '$lib/components/ErrorNote.svelte';
  import Settle from '$lib/components/Settle.svelte';

  type Tile = 'owned' | 'partial' | 'missing' | 'dismissed' | 'unknown' | 'busy' | 'failed';
  // What the header's follow control offers: every monitor level, plus the one
  // value that means the artist is not followed at all.
  type MonitorChoice = MonitorLevel | 'off';

  const queryClient = useQueryClient();
  const artistID = page.params.id ?? '';

  // The two halves of the fraction in the header, and everything. Narrowing is
  // the server's now, and it narrows by the same rule the header counts by —
  // monitor level and all — so a chip cannot offer a release under "Missing"
  // that the header is not counting as missing. The chips' own counts are the
  // header's numbers for that reason: they are the same question.
  const filters: { key: '' | ReleaseCompleteness; name: string }[] = [
    { key: '', name: 'All' },
    { key: 'missing', name: holding.missing },
    { key: 'owned', name: holding.complete }
  ];

  // Read once, before anything is tracked, so that following the address bar
  // back into it later cannot become a dependency of the effect that writes it.
  const opened = page.url;

  let completeness = $state<'' | ReleaseCompleteness>(
    urlChoice(
      opened,
      'show',
      filters.map((entry) => entry.key),
      ''
    )
  );
  let confirmUnfollow = $state(false);
  // Forces the follow control to redraw with the real value after "Keep
  // following" cancels an unfollow. Picking "Not following" moves the native
  // select to that option immediately, and nothing else in the component's
  // props changes when the choice is declined — so a fresh key is what makes
  // it snap back rather than sit on an option the artist never left.
  let followControlReset = $state(0);
  // A discography arrives whole, and a prolific artist's singles run to
  // hundreds. Each section shows its first eighteen — three rows on a wide
  // screen — and says how many it is holding back. Which sections are open is
  // the reader's, and it is not remembered: opening one is a glance, not a
  // place to come back to.
  const sectionCap = 18;
  let expanded = $state<string[]>([]);

  const artist = createQuery({
    queryKey: ['artists', artistID],
    queryFn: () => api.artist(artistID),
    // The refresh announces itself over the event stream; this is the fallback
    // for a stream that never arrived.
    refetchInterval: (query) =>
      ['pending', 'queued', 'running'].includes(query.state.data?.refreshStatus ?? '')
        ? 15_000
        : false
  });

  // The address bar follows the grid rather than leading it, so a reload and a
  // shared link open the releases that were being read rather than the first
  // page of everything.
  $effect(() => {
    keepInUrl(opened.pathname, { show: completeness }, { show: '' });
  });

  // Narrowed on the server, and no longer paged.
  //
  // It was forty-eight at a time, and the sections under it are album, EPs and
  // singles — so page one ended somewhere inside the albums and page two opened
  // with the rest of them under a second heading of the same name. Every
  // heading told the truth about the rows beneath it and the discography still
  // could not be read, because a page is a walk of the order the index holds
  // and album type is not one of those orders.
  //
  // Asking for the whole discography is what makes the grouping honest, and it
  // costs little: a release is a small row, the longest discography anybody has
  // is a few hundred of them, and each section shows its first eighteen until
  // somebody asks for the rest. Giving the index an album-type order instead
  // would be a schema decision, and it is not needed for this.
  //
  // The filter is part of the key rather than a refetch of one key, so that
  // moving between Missing and All reads two answers instead of overwriting
  // one, and moving back is instant.
  const releaseOptions = $derived(
    queryOptions({
      queryKey: ['releases', 'artist', artistID, completeness],
      queryFn: () =>
        api.releases({
          artistId: artistID,
          completeness: completeness || undefined,
          sort: 'year'
        }),
      placeholderData: keepPreviousData,
      refetchInterval: () =>
        ['pending', 'queued', 'running'].includes($artist.data?.refreshStatus ?? '') ? 15_000 : false
    })
  );
  const releases = createQuery(toStore(() => releaseOptions));

  function show(next: '' | ReleaseCompleteness) {
    completeness = next;
  }

  const refresh = createMutation({
    mutationFn: () => api.refreshArtist(artistID),
    onSuccess: async () => {
      await Promise.all([
        queryClient.invalidateQueries({ queryKey: ['artists'] }),
        queryClient.invalidateQueries({ queryKey: ['releases'] }),
        queryClient.invalidateQueries({ queryKey: ['dashboard'] })
      ]);
    }
  });

  // Wanting a discography creates one want per track the library does not have,
  // across every release this artist has, and nothing else: no transfer starts
  // here, and every copy the loop later fetches is still proven by its audio
  // before it enters the library. Pressing twice is safe — creation is
  // idempotent on the recording — so it reports the wants that already existed
  // rather than making them again.
  //
  // It is a standing want rather than one press. Following an artist queues one
  // release refresh per release group and they arrive one at a time behind
  // MusicBrainz's rate limit, so a press reached only the releases whose tracks
  // were already here and the reader had to come back and press again. Stopping
  // it stops future wanting and leaves every want it made.
  const wantMissing = createMutation({
    mutationFn: (on: boolean) => api.setArtistWantMissing(artistID, on),
    onSuccess: async () => {
      await Promise.all([
        queryClient.invalidateQueries({ queryKey: ['artists'] }),
        queryClient.invalidateQueries({ queryKey: ['acquisition-targets'] }),
        queryClient.invalidateQueries({ queryKey: ['dashboard'] })
      ]);
    }
  });

  // The monitor level filters counting and auto-wanting, never data: the full
  // discography stays browsable, unmonitored releases just stop being missing.
  // Changing it deletes nothing and re-opens no per-track decision.
  const setMonitor = createMutation({
    mutationFn: (level: MonitorLevel) => api.setArtistMonitorLevel(artistID, level),
    onSuccess: async () => {
      await Promise.all([
        queryClient.invalidateQueries({ queryKey: ['artists'] }),
        queryClient.invalidateQueries({ queryKey: ['releases'] }),
        queryClient.invalidateQueries({ queryKey: ['dashboard'] })
      ]);
    }
  });

  // What the completion fraction is a fraction of, said out loud so 100% is
  // never mistaken for "everything MusicBrainz has ever catalogued".
  const monitorBasis: Record<MonitorLevel, string> = {
    everything: 'releases',
    main: 'main releases',
    albums_eps: 'albums & EPs',
    owned: 'owned releases'
  };
  const basis = $derived(monitorBasis[$artist.data?.monitorLevel ?? 'everything']);

  // Following an artist Schall already holds is a promotion: the artist is in
  // the catalogue because the library contains their music, and following them
  // is the separate decision to keep their releases complete. Picking a level
  // above "Not following" follows at that level in one step, rather than
  // following at the default and asking again for the level a moment later.
  const follow = createMutation({
    // The level is always sent. It used to be skipped for 'everything' because
    // that was the column default; new follows watch main releases now, and
    // skipping it would have followed at 'main' while the control said
    // "Everything".
    mutationFn: async (level: MonitorLevel) => {
      await api.followHeldArtist(artistID);
      await api.setArtistMonitorLevel(artistID, level);
    },
    onSuccess: async () => {
      await Promise.all([
        queryClient.invalidateQueries({ queryKey: ['artists'] }),
        queryClient.invalidateQueries({ queryKey: ['releases'] }),
        queryClient.invalidateQueries({ queryKey: ['dashboard'] })
      ]);
    }
  });

  // Unfollowing withdraws the intent to keep an artist complete, and nothing
  // else. It is confirmed rather than instant because it can end two ways, and
  // which one it is depends on something the user cannot see from this page:
  // whether the library still reaches any of this artist's music.
  const unfollow = createMutation({
    mutationFn: () => api.unfollowArtist(artistID),
    onSuccess: async (result) => {
      confirmUnfollow = false;
      await Promise.all([
        queryClient.invalidateQueries({ queryKey: ['artists'] }),
        queryClient.invalidateQueries({ queryKey: ['releases'] }),
        queryClient.invalidateQueries({ queryKey: ['dashboard'] })
      ]);
      // A released artist no longer exists, so this page has nothing left to
      // show. A held one does, and stays put to show what it became.
      if (result.outcome === 'released') await goto('/artists');
    }
  });

  // Follow, unfollow and the monitor level are one control: a single pill
  // select whose value is either the artist's current monitor level or "off"
  // when nothing is followed. "Not following" asks for confirmation rather
  // than acting immediately, the way the standalone Following button used to.
  const monitorOptions: { value: MonitorChoice; name: string }[] = [
    { value: 'off', name: 'Not following' },
    { value: 'everything', name: 'Following · Everything' },
    { value: 'main', name: 'Following · Main releases' },
    { value: 'albums_eps', name: 'Following · Albums & EPs' },
    { value: 'owned', name: 'Following · Owned only' }
  ];
  const monitorValue = $derived($artist.data?.followed ? $artist.data.monitorLevel : 'off');

  function chooseMonitor(next: string) {
    if (next === 'off') {
      confirmUnfollow = true;
      return;
    }
    const level = next as MonitorLevel;
    if (!$artist.data?.followed) {
      $follow.mutate(level);
      return;
    }
    if (level !== $artist.data.monitorLevel) $setMonitor.mutate(level);
  }

  function keepFollowing() {
    confirmUnfollow = false;
    followControlReset += 1;
  }

  const shown = $derived($releases.data?.items ?? []);
  // 'pending' means a refresh has never run, not that one is under way: showing
  // a spinner for it would claim work that nothing is doing.
  const refreshing = $derived(['queued', 'running'].includes($artist.data?.refreshStatus ?? ''));

  function initialsOf(name: string): string {
    const words = name
      .split(/[\s.]+/)
      .filter((word) => word && !['of', 'the', 'and', 'a'].includes(word.toLowerCase()));
    if (words.length < 2) return (words[0] ?? name).slice(0, 2).toUpperCase();
    return words
      .slice(0, 3)
      .map((word) => word[0])
      .join('')
      .toUpperCase();
  }

  // What is still worth counting: the tracks nobody dismissed — except at
  // 'owned', where the denominator is what the library already maps and a
  // reached release is complete by construction. The same rule the server's
  // completeness queries apply.
  function countable(release: Release) {
    if (release.artistMonitorLevel === 'owned') return release.ownedTrackCount;
    return release.trackCount - release.dismissedTrackCount;
  }

  function tile(release: Release): Tile {
    if (release.trackRefreshStatus === 'failed') return 'failed';
    if (['queued', 'running'].includes(release.trackRefreshStatus)) return 'busy';
    if (release.trackCount === 0) return 'unknown';
    if (release.ownedTrackCount === 0 && countable(release) === 0) {
      // At 'owned' an unreached release is unmonitored, not dismissed; the
      // dismissed reading is for gaps the user closed one decision at a time.
      return release.artistMonitorLevel === 'owned' ? 'missing' : 'dismissed';
    }
    if (release.ownedTrackCount === 0) return 'missing';
    if (release.ownedTrackCount < countable(release)) return 'partial';
    return 'owned';
  }

  // The fraction is the artist's, counted over every release they have, and it
  // arrives with the artist rather than being worked out from the releases on
  // screen. Counting it here would mean counting it over the forty-eight that
  // happened to load, and a completeness bar quietly describing one page is a
  // far worse answer than a slow one. The rule it is counted by is tile() below
  // written in SQL — `albumTile` in internal/db/catalogue.go — so the two say
  // the same thing about the same release. Only monitored releases enter it;
  // unmonitored ones stay on the page but stop being missing, and releases whose
  // every gap was dismissed leave both halves.
  const discography = $derived($artist.data?.discography);
  const releaseCount = $derived(discography?.releases ?? 0);
  const ownedCount = $derived(discography?.owned ?? 0);
  const missingCount = $derived(discography?.missing ?? 0);
  const dismissedReleaseCount = $derived(discography?.dismissed ?? 0);
  const countedReleases = $derived(discography?.counted ?? 0);
  // Releases the catalogue knows about and has no tracklist for yet. They are
  // neither owned nor missing, so the header says how many are on the way
  // rather than letting them look like nothing at all.
  const loadingReleases = $derived(discography?.loading ?? 0);
  const standing = $derived($artist.data?.wantMissing ?? false);

  // Each chip counts what pressing it would show, over the whole discography
  // rather than over the page it would open on — which is the same number the
  // header states, because the server narrows by the rule the header counts by.
  //
  // A chip has no figure at all until the discography answers. The counts above
  // fall back to nought so the arithmetic on this page has something to divide
  // by; a chip is read by a person, and to them a nought is a statement that
  // this artist has nothing.
  const tabs = $derived(
    filters.map((entry) => ({
      value: entry.key,
      name: entry.name,
      count: !discography
        ? undefined
        : entry.key === 'missing'
          ? missingCount
          : entry.key === 'owned'
            ? ownedCount
            : releaseCount
    }))
  );
  const filterName = $derived(filters.find((entry) => entry.key === completeness)?.name ?? 'All');

  // Grouping by type keeps a long discography readable: an artist's albums are
  // a different question from their singles, even though both can be missing.
  //
  // The sections divide the page, not the discography, and that is a trade the
  // paging makes rather than an oversight. A page is a walk of the order an
  // index holds, and album type is not one of those orders — giving it one is a
  // new index, which is a schema decision and not this change's to take. So an
  // artist long enough to need a second page can show the same heading again on
  // it, with that type's releases split at whatever year the page ended on.
  // Everything a heading claims is still true of what is under it; it is the
  // reading order that suffers, and only past forty-eight releases.
  const order = ['album', 'ep', 'single'];
  const sections = $derived(
    Array.from(new Set(shown.map((release) => release.albumType)))
      .sort((a, b) => {
        const byOrder = index(a) - index(b);
        return byOrder || a.localeCompare(b);
      })
      .map((type) => ({
        type,
        items: shown
          .filter((release) => release.albumType === type)
          .sort((a, b) => (a.firstReleaseDate ?? '').localeCompare(b.firstReleaseDate ?? ''))
      }))
  );

  function index(type: string) {
    const at = order.indexOf(type);
    return at === -1 ? order.length : at;
  }

  function label(type: string) {
    if (type === 'ep') return 'EPs';
    if (type === 'single') return 'Singles';
    return `${type[0].toUpperCase()}${type.slice(1)}s`;
  }

  // The card's second line and the colour it reads in: ink-3 for a plain fact,
  // accent for a partial release, fail for one missing outright. One function
  // rather than two, so the word and its colour can never disagree about which
  // state a release is in.
  function describe(release: Release): { text: string; tone: string } {
    const year = release.firstReleaseDate ? release.firstReleaseDate.slice(0, 4) : '—';
    const state = tile(release);
    if (state === 'unknown') return { text: `${year} · no track list`, tone: 'text-ink-3' };
    if (state === 'failed') return { text: `${year} · ${quiet(progress.failed)}`, tone: 'text-fail' };
    if (state === 'busy') return { text: `${year} · ${quiet(progress.importing)}`, tone: 'text-busy' };
    if (state === 'dismissed') {
      return { text: `${year} · ${quiet(progress.dismissed)}`, tone: 'text-ink-3' };
    }
    if (state === 'missing') return { text: `${year} · ${quiet(holding.missing)}`, tone: 'text-fail' };
    // The tile stays plain when nothing is watching the release, and on its
    // own that says only that this one differs from the ones beside it. The
    // word is what says how.
    if (!release.monitored) return { text: `${year} · unmonitored`, tone: 'text-ink-3' };
    if (state === 'partial') {
      return {
        text: `${year} · ${release.ownedTrackCount} of ${countable(release)}`,
        tone: 'text-accent'
      };
    }
    return { text: year, tone: 'text-ink-3' };
  }
</script>

<svelte:head><title>{$artist.data?.name ?? 'Artist'} · Schall</title></svelte:head>

{#if $artist.isError}
  <div class="px-4 sm:px-6 py-6">
    <!-- The artist is read, not written, so the note asks again by itself. -->
    <ErrorNote error={$artist.error} retry={() => $artist.refetch()} />
  </div>
{:else}
  <Settle pending={$artist.isPending}>
    {#snippet placeholder()}
      {@render ArtistSkeleton()}
    {/snippet}
  {#if $artist.data}
  <div class="px-4 sm:px-6 pt-6 pb-3">
    <BackLink fallback="/artists" label="Back to artists" />
  </div>

  <!-- The artist, at the size an artist is looked at. Pictured by the sweep,
       from the image MusicBrainz holds against this artist's own identifier.
       An artist nobody has a picture of keeps their initials in the same
       circle instead. -->
  <section class="flex items-start gap-5 px-4 sm:px-6 pb-2">
    <Cover
      eager
      src={`/api/v1/artists/${artistID}/image`}
      fallback={initialsOf($artist.data.name)}
      class="size-24 shrink-0 rounded-full border border-line-thin bg-surface-regular text-3xl font-semibold object-cover"
    />

    <div class="flex min-w-0 flex-1 flex-col gap-2 pt-1">
      <h1 class="text-quiet-display font-semibold text-ink">{$artist.data.name}</h1>

      <p class="text-body text-ink-2">
        {#if countedReleases}
          <span class="numeric font-semibold text-ink">{ownedCount}</span>
          of <span class="numeric font-semibold text-ink">{countedReleases}</span>
          {basis} owned
          {#if dismissedReleaseCount}
            · {dismissedReleaseCount} dismissed
          {/if}
        {:else if dismissedReleaseCount}
          everything left was dismissed
        {:else}
          <span class="numeric font-semibold text-ink-3">—</span> {basis} owned
        {/if}
      </p>

      <!-- Releases the catalogue knows about and has no tracklist for yet.
           They are neither owned nor missing, and without this line the
           header's denominator looks smaller than the grid below it. -->
      {#if loadingReleases > 0}
        <p class="numeric text-meta text-ink-3">
          {loadingReleases}
          {loadingReleases === 1 ? 'release' : 'releases'} still loading
        </p>
      {/if}

      <!-- What MusicBrainz's community voted this artist is. A tag rather
           than a filter chip, because a genre here is not something to narrow
           by. An artist with no votes shows nothing, and the row disappears
           rather than standing empty. -->
      {#if $artist.data.genres?.length}
        <ul class="flex flex-wrap items-center gap-1.5">
          {#each $artist.data.genres as genre (genre)}
            <li><StateTag>{genre}</StateTag></li>
          {/each}
        </ul>
      {/if}

      <!-- Who this is, from Wikipedia. Collapsed, because somebody on this
           page is usually here for the music rather than the reading; and
           behind a disclosure rather than trimmed, so the words stay in the
           product without being the first thing on it.
           The attribution is not chrome. Wikipedia text is CC BY-SA and the
           line under it satisfies the licence. An artist no encyclopaedia has
           heard of shows nothing at all. -->
      {#if $artist.data.biography}
        <details class="group mt-1">
          <summary
            class="tap-tall inline-flex w-fit cursor-pointer list-none items-center gap-1 text-meta text-ink-3 transition hover:text-ink-2 [&::-webkit-details-marker]:hidden"
          >
            About
            <ChevronRight size={12} class="transition-transform group-open:rotate-90" />
          </summary>
          <p class="reveal mt-2 max-w-prose text-body leading-relaxed text-ink-2">
            {$artist.data.biography}
          </p>
          {#if $artist.data.biographySourceUrl}
            <p class="mt-1.5 text-meta text-ink-4">
              From
              <a
                href={$artist.data.biographySourceUrl}
                target="_blank"
                rel="noreferrer"
                class="underline transition hover:text-accent-soft">Wikipedia</a
              >
            </p>
          {/if}
        </details>
      {/if}
    </div>

    <!-- Follow, unfollow and how much to monitor are one control: a single
         select rather than a Following button beside a separate level picker
         competing for the same decision. -->
    <div class="flex shrink-0 items-center gap-2 pt-1">
      {#key `${monitorValue}:${followControlReset}`}
        <PillSelect
          options={monitorOptions}
          value={monitorValue}
          onchange={chooseMonitor}
          label="Follow this artist"
        />
      {/key}

      <Button
        variant="outline"
        size="sm"
        disabled={$refresh.isPending || refreshing}
        onclick={() => $refresh.mutate()}
      >
        <RefreshCw size={13} strokeWidth={2.2} class={refreshing ? 'animate-spin' : ''} />
        {refreshing ? 'Refreshing' : 'Refresh'}
      </Button>

      <!-- Once the standing want is on there is nothing left to press: the
           button is replaced by what it did, and by the one control that
           undoes it. -->
      {#if standing}
        <Chip role="ok">Wanting missing</Chip>
        <Button
          variant="ghost"
          size="sm"
          disabled={$wantMissing.isPending}
          onclick={() => $wantMissing.mutate(false)}
        >
          {$wantMissing.isPending ? 'Stopping…' : 'Stop'}
        </Button>
      {:else if missingCount > 0}
        <Button size="sm" disabled={$wantMissing.isPending} onclick={() => $wantMissing.mutate(true)}>
          {#if $wantMissing.isPending}
            <LoaderCircle size={13} class="animate-spin" />
          {:else}
            <ListPlus size={13} strokeWidth={2.2} />
          {/if}
          Want {missingCount} missing
        </Button>
      {/if}
    </div>
  </section>

  <!-- The answer sits under the button that asked, because "wanted 3 across 7
       releases" is what somebody pressing it needs to see and a page-level
       toast would say it somewhere else. -->
  {#if $wantMissing.isError || $wantMissing.data?.wantMissing}
    <div class="max-w-prose border-b border-line-thin px-4 sm:px-6 py-2.5">
      {#if $wantMissing.isError}
        <!-- Bare: this strip is already the band under the button, and a tinted
             badge inside it would be a second surface in the same row. -->
        <ErrorNote error={$wantMissing.error} bare />
      {:else if $wantMissing.data}
        <p class="max-w-prose text-body text-ink-3">
          {wantedSummary($wantMissing.data)}
          {#if $wantMissing.data.releases}
            · across {$wantMissing.data.releases}
            {$wantMissing.data.releases === 1 ? 'release' : 'releases'}
          {/if}
        </p>
      {/if}
    </div>
  {/if}

  <ControlRail label="Which releases to show">
    <Segmented
      options={tabs}
      value={completeness}
      onchange={(value) => show(value as '' | ReleaseCompleteness)}
      label="Which releases to show"
      pending={$artist.isPending}
    />

    <span class="ml-auto text-meta font-medium text-ink-4">
      {$artist.data.lastRefreshedAt
        ? `updated ${calendarDate($artist.data.lastRefreshedAt)}`
        : 'not refreshed yet'}
    </span>
  </ControlRail>

  <div class="flex flex-col gap-4 px-4 sm:px-6 py-5">
    {#if confirmUnfollow && $artist.data.followed}
      <div class="flex flex-col gap-3 rounded-panel border border-line-regular bg-surface-regular p-4">
        <h2 class="font-display text-lead font-bold text-ink">
          Unfollow {$artist.data.name}?
        </h2>
        <p class="text-body leading-relaxed text-ink-2">
          Their tracks stop counting as missing. Nothing you own is touched.
        </p>
        <div class="flex flex-wrap items-center gap-2">
          <Button disabled={$unfollow.isPending} onclick={() => $unfollow.mutate()}>
            <UserRoundMinus size={13} strokeWidth={2.2} />
            {$unfollow.isPending ? 'Unfollowing…' : 'Unfollow'}
          </Button>
          <Button variant="ghost" disabled={$unfollow.isPending} onclick={keepFollowing}>
            Keep following
          </Button>
        </div>
      </div>
    {/if}

    {#each [$unfollow.error, $follow.error, $setMonitor.error, $refresh.error] as error, errorIndex (errorIndex)}
      {#if error}
        <!-- Follow, unfollow, monitor and refresh are all things a person
             pressed, so none of them asks again by itself. -->
        <ErrorNote {error} />
      {/if}
    {/each}

    {#if !$artist.data.followed}
      <div class="flex flex-col gap-1.5 rounded-panel border border-line-regular bg-idle/14 p-4">
        <span class="text-body font-semibold text-ink">
          {$artist.data.catalogueSummary ?? 'This artist is here because your library has their music.'}
        </span>
        <span class="text-meta text-ink-2">nothing counts as missing</span>
      </div>
    {/if}

    {#if $artist.data.refreshStatus === 'failed'}
      <!-- `refreshError` is the text the last metadata refresh left behind: a
           Go sentence stored on the artist, not something thrown at a request.
           It reads the same way as any other failure, so it goes through the
           same note. -->
      <ErrorNote error={$artist.data.refreshError ?? 'The metadata refresh failed.'} />
    {/if}

    {#if $releases.isError}
      <ErrorNote error={$releases.error} retry={() => $releases.refetch()} />
    {:else if $releases.isPending}
      {@render ReleaseSkeleton()}
    {:else if sections.length}
      {#each sections as section (section.type)}
        {@const opened = expanded.includes(section.type)}
        {@const visible = opened ? section.items : section.items.slice(0, sectionCap)}
        <section class="flex flex-col gap-2">
          <div class="flex items-baseline justify-between gap-3">
            <span class="text-[15px] font-semibold text-ink">
              {label(section.type)}
              <span class="numeric text-meta font-normal text-ink-4">{section.items.length}</span>
            </span>
            {#if section.items.length > sectionCap}
              <button
                class="tap w-fit text-meta font-medium text-ink-3 transition hover:text-ink"
                onclick={() =>
                  (expanded = opened
                    ? expanded.filter((type) => type !== section.type)
                    : [...expanded, section.type])}
              >
                {opened
                  ? 'Show less'
                  : `Show all ${section.items.length} ${label(section.type).toLocaleLowerCase()}`}
              </button>
            {/if}
          </div>

          <div class="grid grid-cols-[repeat(auto-fill,minmax(7.5rem,1fr))] gap-x-[22px] gap-y-3.5">
            {#each visible as release (release.id)}
              {@const info = describe(release)}
              <a href={`/releases/${release.id}`} class="flex w-[7.5rem] min-w-0 flex-col gap-1.5">
                <!-- Only what Schall already holds: a page of tiles asks this
                     installation and never sets an archive working. A release
                     nobody has pictured yet keeps the hatched square it always
                     had, and the sweep fills it in its own time. -->
                <div
                  class="aspect-square w-[7.5rem] overflow-hidden rounded-row border border-line-thin bg-[repeating-linear-gradient(135deg,rgba(255,255,255,.05)_0_1px,transparent_1px_8px)]"
                >
                  <Cover
                    src={`/api/v1/albums/${release.id}/cover?cached=1`}
                    fallback="♪"
                    class="size-full object-cover"
                  />
                </div>
                <span class="block truncate text-[13.5px] font-medium text-ink" title={release.title}>
                  {release.title}
                </span>
                <!-- Never cut off. The line is a year and then how much of the
                     release is held — `2018 · 13 of 14` — and truncation took
                     the denominator, which is the half that makes the other
                     half mean anything. A second line is cheaper than a fact
                     that ends in an ellipsis. -->
                <span class="numeric block min-w-0 truncate text-meta {info.tone}" title={info.text}
                  >{info.text}</span
                >
              </a>
            {/each}
          </div>
        </section>
      {/each}

    {:else}
      <EmptyPanel
        role={refreshing ? 'busy' : 'idle'}
        heading={refreshing
          ? 'Building the discography'
          : releaseCount
            ? 'Nothing in this filter'
            : 'Nothing scanned yet'}
      >
        {#if !refreshing && releaseCount}
          <p class="text-body leading-relaxed text-ink-2">
            {releaseCount} releases known · none {filterName.toLocaleLowerCase()}
          </p>
        {/if}
        {#if !refreshing && !releaseCount}
          <Button
            class="w-fit"
            disabled={$refresh.isPending}
            onclick={() => $refresh.mutate()}
          >
            <RefreshCw size={13} strokeWidth={2.2} /> Refresh discography
          </Button>
        {/if}
      </EmptyPanel>
    {/if}
  </div>
  {/if}
  </Settle>
{/if}

{#snippet ReleaseSkeleton()}
  <section
    class="max-h-[calc(100dvh-30rem)] flex flex-col gap-3 overflow-hidden"
    role="status"
    aria-label="Loading releases"
  >
    <span class="h-3 w-16 animate-pulse rounded-row bg-surface-regular" aria-hidden="true"></span>
    <div class="grid grid-cols-[repeat(auto-fill,minmax(7.5rem,1fr))] gap-x-[22px] gap-y-3.5">
      {#each Array(48) as _, placeholderIndex (placeholderIndex)}
        <div class="flex w-[7.5rem] flex-col gap-1.5" aria-hidden="true">
          <div class="aspect-square w-[7.5rem] animate-pulse rounded-row bg-surface-regular"></div>
          <span class="h-4 w-full animate-pulse rounded-row bg-surface-regular"></span>
          <span class="h-3 w-2/3 animate-pulse rounded-row bg-surface-regular"></span>
        </div>
      {/each}
    </div>
  </section>
{/snippet}

{#snippet ArtistSkeleton()}
  <div role="status" aria-label="Loading artist page">
    <div class="px-4 sm:px-6 pt-6 pb-3">
      <span class="block h-4 w-4 animate-pulse rounded-row bg-surface-regular" aria-hidden="true"></span>
    </div>

    <section class="flex items-start gap-5 px-4 sm:px-6 pb-2" aria-hidden="true">
      <div class="size-24 shrink-0 animate-pulse rounded-full bg-surface-regular"></div>
      <div class="flex min-w-0 flex-1 flex-col gap-2 pt-1">
        <span class="h-8 w-64 animate-pulse rounded-row bg-surface-regular"></span>
        <span class="h-4 w-48 animate-pulse rounded-row bg-surface-regular"></span>
        <span class="h-5 w-32 animate-pulse rounded-row bg-surface-regular"></span>
      </div>
      <div class="flex shrink-0 items-center gap-2 pt-1">
        <span class="h-7 w-36 animate-pulse rounded-full bg-surface-regular"></span>
        <span class="h-7 w-24 animate-pulse rounded-row bg-surface-regular"></span>
      </div>
    </section>

    <ControlRail label="Which releases to show">
      <Segmented
        options={filters.map((entry) => ({ value: entry.key, name: entry.name }))}
        value=""
        onchange={() => {}}
        label="Which releases to show"
        pending
      />
    </ControlRail>

    <div class="flex flex-col gap-4 px-4 sm:px-6 py-5">
      {@render ReleaseSkeleton()}
    </div>
  </div>
{/snippet}

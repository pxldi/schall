<script lang="ts">
  import Skeleton from '$lib/components/Skeleton.svelte';
  import { page } from '$app/state';
  import { goto } from '$app/navigation';
  import {
    createMutation,
    createQuery,
    keepPreviousData,
    queryOptions,
    useQueryClient
  } from '@tanstack/svelte-query';
  import { tick } from 'svelte';
  import { toStore } from 'svelte/store';
  import Icon from '$lib/components/Icon.svelte';
  import {
    api,
    type MonitorLevel,
    type Release,
    type ReleaseCompleteness,
    type RemovedTracks
  } from '$lib/api';
  import { coverSrc } from '$lib/cover-size';
  import { calendarDate, keepInUrl, removedSummary, urlChoice, wantedSummary } from '$lib/utils';
  import { useKeys } from '$lib/keys.svelte';
  import { glide } from '$lib/motion.svelte';
  import Button from '$lib/components/Button.svelte';
  import EmptyPanel from '$lib/components/EmptyPanel.svelte';
  import { holding, progress, quiet } from '$lib/vocabulary';
  import Chip from '$lib/components/Chip.svelte';
  import Cover from '$lib/components/Cover.svelte';
  import PageHeader from '$lib/components/PageHeader.svelte';
  import ControlRail from '$lib/components/ControlRail.svelte';
  import OwnedBar from '$lib/components/OwnedBar.svelte';
  import PillSelect from '$lib/components/PillSelect.svelte';
  import Segmented from '$lib/components/Segmented.svelte';
  import ErrorNote from '$lib/components/ErrorNote.svelte';
  import Settle from '$lib/components/Settle.svelte';
  import SkeletonRows from '$lib/components/SkeletonRows.svelte';
  import PopularTracks, { popularOptions } from './PopularTracks.svelte';
  import ReleaseRow from './ReleaseRow.svelte';

  type Tile = 'owned' | 'partial' | 'missing' | 'dismissed' | 'unknown' | 'busy' | 'failed';

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
  // hundreds. Each kind of release shows its newest eight, and "Show all"
  // opens the rest of that kind in place. Which sections are open is the
  // reader's, and it is not remembered: opening one is a glance, not a place to
  // come back to.
  const rowLength = 8;
  let expanded = $state<string[]>([]);

  const artist = createQuery({
    queryKey: ['artists', artistID],
    queryFn: () => api.artist(artistID),
    // The refresh announces itself over the event stream; this is the fallback
    // for a stream that never arrived. 'pending' is not on the list: it means
    // no refresh job exists, which for a held artist is for good.
    refetchInterval: (query) =>
      ['queued', 'running'].includes(query.state.data?.refreshStatus ?? '') ? 15_000 : false
  });

  // The popular list is drawn only once the artist and the releases are in,
  // and it waits on ListenBrainz, so its read starts here beside theirs.
  void queryClient.prefetchQuery(popularOptions(artistID));
  // The same read the list below makes, so the cursor knows how many rows the
  // popular list has.
  const popular = createQuery(toStore(() => popularOptions(artistID)));

  // The address bar follows the list rather than leading it, so a reload and a
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
  // is a few hundred of them, and each section shows its first eight until
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
        ['queued', 'running'].includes($artist.data?.refreshStatus ?? '') ? 15_000 : false
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
      // The release rows count the tracks still wanted, so they are read again too.
      await Promise.all([
        queryClient.invalidateQueries({ queryKey: ['artists'] }),
        queryClient.invalidateQueries({ queryKey: ['releases'] }),
        queryClient.invalidateQueries({ queryKey: ['wants'] }),
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
  // is the separate decision to keep their releases complete.
  const follow = createMutation({
    // The level is always sent. It used to be skipped for 'everything' because
    // that was the column default; new follows watch main releases now, and
    // skipping it would have followed at 'main' while the control said
    // "Everything".
    mutationFn: async (level: MonitorLevel) => {
      await api.followHeldArtist(artistID);
      await api.setArtistMonitorLevel(artistID, level);
    },
    // Settled rather than succeeded: when the level fails after the follow
    // landed, the artist is followed and the page has to read that back.
    onSettled: async () => {
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

  // Follow, unfollow and the monitor level are one control: a single select
  // whose value is either the artist's current monitor level or "off" when
  // nothing is followed. "Not following" asks for confirmation rather than
  // acting immediately. The Follow and Unfollow buttons in the title row are
  // the quick way to the same two outcomes, at the default level.
  type MonitorChoice = MonitorLevel | 'off';
  const monitorOptions: { value: MonitorChoice; name: string }[] = [
    { value: 'off', name: 'Not following' },
    { value: 'everything', name: 'Following · Everything' },
    { value: 'main', name: 'Following · Main releases' },
    { value: 'albums_eps', name: 'Following · Albums & EPs' },
    { value: 'owned', name: 'Following · Owned only' }
  ];
  const monitorValue = $derived($artist.data?.followed ? $artist.data.monitorLevel : 'off');

  // Picking a level above "Not following" follows at that level in one step,
  // rather than following at the default and asking again for the level a
  // moment later.
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

  // Grouping by kind keeps a long discography readable: an artist's albums are
  // a different question from their singles, even though both can be missing.
  // An album MusicBrainz qualifies as a compilation, a mixtape, a live record
  // or anything else goes under Other with broadcasts and the rest, so Albums
  // holds the records an artist made as albums. A kind with nothing in it is
  // not drawn.
  //
  // Newest first in every section, whether or not the library holds it, because
  // the section is read as a timeline and its eight are the latest.
  const kinds = ['Albums', 'EPs', 'Singles', 'Other'] as const;
  type Kind = (typeof kinds)[number];
  function kindOf(release: Release): Kind {
    if (release.albumType === 'album' && !release.secondaryTypes?.length) return 'Albums';
    if (release.albumType === 'ep') return 'EPs';
    if (release.albumType === 'single') return 'Singles';
    return 'Other';
  }
  const sections = $derived(
    kinds
      .map((kind) => ({
        kind,
        items: shown
          .filter((release) => kindOf(release) === kind)
          .sort(
            (a, b) =>
              (b.firstReleaseDate ?? '').localeCompare(a.firstReleaseDate ?? '') ||
              a.title.localeCompare(b.title)
          )
      }))
      .filter((section) => section.items.length > 0)
  );
  const visibleOf = (section: { kind: Kind; items: Release[] }) =>
    expanded.includes(section.kind) ? section.items : section.items.slice(0, rowLength);

  // What a release in Other is, said after its year, since the heading no
  // longer does.
  const otherNames: Record<string, string> = {
    compilation: 'Compilation',
    live: 'Live',
    'mixtape/street': 'Mixtape',
    remix: 'Remixes',
    soundtrack: 'Soundtrack',
    'dj-mix': 'DJ mix',
    demo: 'Demo',
    interview: 'Interview',
    spokenword: 'Spoken word',
    audiobook: 'Audiobook',
    broadcast: 'Broadcast',
    // MusicBrainz's own primary type "Other". It holds music videos and the
    // like, and Kendrick Lamar's "luther" sits there although it reads like a
    // single. Naming it says the type came from MusicBrainz; it is not guessed.
    other: 'Other'
  };
  function otherName(release: Release) {
    const qualifier = release.secondaryTypes?.find((type) => otherNames[type]);
    if (qualifier) return otherNames[qualifier];
    return otherNames[release.albumType] ?? '';
  }

  function meta(release: Release, kind: Kind) {
    const year = release.firstReleaseDate ? release.firstReleaseDate.slice(0, 4) : '—';
    const name = kind === 'Other' ? otherName(release) : '';
    return name ? `${year} · ${name}` : year;
  }

  // A release's + is filled when every countable track is held or wanted. A
  // held track counts as wanted until somebody removes it, as on the release
  // page, so a release the library holds shows its tick on hover.
  function releaseWanted(release: Release) {
    const kept = Math.max(0, release.ownedTrackCount - (release.leavingTrackCount ?? 0));
    const all = countable(release);
    return all > 0 && kept + (release.wantedTrackCount ?? 0) >= all;
  }

  // The + on a release wants every track the library does not have, and takes
  // back a removal, as the release page's button does. Pressed again it removes
  // the release: the wants go at once, and a file Schall downloaded for it is
  // deleted 30 days later unless the release is wanted again first.
  type ReleaseToggle = { kind: 'want' | 'remove'; id: string };
  const toggleRelease = createMutation({
    mutationFn: async ({ kind, id }: ReleaseToggle): Promise<unknown> =>
      kind === 'want' ? api.wantRelease(id) : api.removeRelease(id),
    onSuccess: async () => {
      await Promise.all([
        queryClient.invalidateQueries({ queryKey: ['artists'] }),
        queryClient.invalidateQueries({ queryKey: ['releases'] }),
        queryClient.invalidateQueries({ queryKey: ['wants'] }),
        queryClient.invalidateQueries({ queryKey: ['dashboard'] })
      ]);
    }
  });
  const removed = $derived(
    $toggleRelease.data && $toggleRelease.variables?.kind === 'remove'
      ? removedSummary($toggleRelease.data as RemovedTracks)
      : ''
  );

  // The tooltip of a release's row: the year and the state in words, in the
  // colour-free form, since the row itself draws only the bar or a glyph.
  function describe(release: Release): string {
    const year = release.firstReleaseDate ? release.firstReleaseDate.slice(0, 4) : '—';
    const state = tile(release);
    if (state === 'unknown') return `${year} · no track list`;
    if (state === 'failed') return `${year} · ${quiet(progress.failed)}`;
    if (state === 'busy') return `${year} · ${quiet(progress.importing)}`;
    if (state === 'dismissed') return `${year} · ${quiet(progress.dismissed)}`;
    if (state === 'missing') return `${year} · ${quiet(holding.missing)}`;
    // The row stays plain when nothing is watching the release, and on its own
    // that says only that this one differs from the ones beside it. The word
    // is what says how.
    if (!release.monitored) return `${year} · unmonitored`;
    if (state === 'partial') return `${year} · ${release.ownedTrackCount} of ${countable(release)}`;
    return year;
  }

  // The cursor (ADR 0042): one inverted block that glides down the releases and
  // on into the popular tracks, so J and K read the whole page. It is kept by
  // an id with a kind in front, because a release and a recording are
  // different things that could share one.
  let cursorId = $state('');
  $effect(() => {
    void completeness;
    cursorId = '';
  });
  const popularItems = $derived($popular.data?.unavailable ? [] : ($popular.data?.items ?? []));
  const stops = $derived<{ id: string; href: string | null }[]>([
    ...sections.flatMap((section) =>
      visibleOf(section).map((release) => ({
        id: `release:${release.id}`,
        href: `/releases/${release.id}`
      }))
    ),
    ...popularItems.map((track) => ({
      id: `track:${track.recordingId}`,
      href: track.albumId ? `/releases/${track.albumId}` : null
    }))
  ]);
  const cursorStop = $derived(stops.find((stop) => stop.id === cursorId));

  function moveCursor(by: number) {
    if (!stops.length) return;
    const at = stops.findIndex((stop) => stop.id === cursorId);
    const next = at < 0 ? 0 : Math.min(Math.max(at + by, 0), stops.length - 1);
    cursorId = stops[next].id;
    void tick().then(() =>
      document
        .querySelector('[data-release][data-cursor="true"], [data-recording][data-cursor="true"]')
        ?.scrollIntoView?.({ block: 'nearest' })
    );
  }

  useKeys(() => [
    {
      keys: ['j', 'k', 'arrowdown', 'arrowup'],
      caps: ['j', 'k'],
      verb: 'move',
      order: 0,
      enabled: () => stops.length > 0,
      run: (event) => moveCursor(event.key === 'j' || event.key === 'ArrowDown' ? 1 : -1)
    },
    ...(cursorStop?.href
      ? [
          {
            keys: ['enter'],
            verb: 'open',
            order: 1,
            run: () => void goto(cursorStop.href ?? '')
          }
        ]
      : [])
  ]);

  // The artist's picture, or the cover of the release they hold most of when
  // there is none. The picture is asked for, and the cover only once it turns
  // out there is no picture.
  let portraitMissing = $state(false);
</script>

<svelte:head><title>{$artist.data?.name ?? 'Artist'} · Schall</title></svelte:head>

{#if $artist.isError}
  <div class="px-5 py-6">
    <!-- The artist is read, not written, so the note asks again by itself. -->
    <ErrorNote error={$artist.error} retry={() => $artist.refetch()} />
  </div>
{:else}
  <Settle pending={$artist.isPending}>
    {#snippet placeholder()}
      {@render ArtistSkeleton()}
    {/snippet}
    {#if $artist.data}
      {@const data = $artist.data}
      <div class="grid items-start lg:grid-cols-[minmax(0,1fr)_21.25rem]">
        <div class="flex min-w-0 flex-col pb-8">
          <PageHeader title={data.name} path={[{ label: 'Artists', href: '/artists' }]}>
            {#snippet sub()}
              <span>
                {#if countedReleases}
                  <b class="numeric font-semibold text-ink">{ownedCount}</b>
                  of <b class="numeric font-semibold text-ink">{countedReleases}</b>
                  {basis} owned
                {:else if dismissedReleaseCount}
                  everything else dismissed
                {:else}
                  <span class="numeric text-ink-3">—</span> {basis} owned
                {/if}
              </span>
              {#if countedReleases && dismissedReleaseCount}
                <span>{dismissedReleaseCount} dismissed</span>
              {/if}
              <!-- Releases the catalogue knows about and has no tracklist for yet.
                   They are neither owned nor missing, and without this the header's
                   denominator looks smaller than the list below it. -->
              {#if loadingReleases > 0}
                <span class="numeric">
                  {loadingReleases}
                  {loadingReleases === 1 ? 'release' : 'releases'} still loading
                </span>
              {/if}
            {/snippet}
            {#snippet actions()}
              <!-- The one primary press is following, for an artist who is not
                   followed, and adding what is missing once they are. -->
              {#if !data.followed}
                <Button
                  variant="primary"
                  key="f"
                  verb="follow"
                  disabled={$follow.isPending}
                  onclick={() => $follow.mutate('main')}
                >
                  <Icon
                    name={$follow.isPending ? 'busy' : 'follow'}
                    size="sm"
                    class={$follow.isPending ? 'animate-spin' : ''}
                  />
                  Follow
                </Button>
              {:else if confirmUnfollow}
                <Button variant="danger" disabled={$unfollow.isPending} onclick={() => $unfollow.mutate()}>
                  <Icon name="unfollow" size="sm" />
                  {$unfollow.isPending ? 'Unfollowing…' : 'Unfollow'}
                </Button>
                <Button variant="ghost" disabled={$unfollow.isPending} onclick={keepFollowing}>
                  Keep following
                </Button>
              {:else}
                <Button variant="outline" key="u" verb="unfollow" onclick={() => (confirmUnfollow = true)}>
                  <Icon name="unfollow" size="sm" />
                  Unfollow
                </Button>
              {/if}

              <Button
                variant="outline"
                key="r"
                verb="refresh"
                disabled={$refresh.isPending || refreshing}
                onclick={() => $refresh.mutate()}
              >
                <Icon name="refresh" size="sm" class={refreshing ? 'animate-spin' : ''} />
                {refreshing ? 'Refreshing' : 'Refresh'}
              </Button>

              <!-- Once the standing want is on there is nothing left to press: the
                   button is replaced by what it did, and by the one control that
                   undoes it. -->
              {#if standing}
                <Chip role="ok">Missing on wishlist</Chip>
                <Button
                  variant="ghost"
                  key="w"
                  verb="stop adding"
                  disabled={$wantMissing.isPending}
                  onclick={() => $wantMissing.mutate(false)}
                >
                  {$wantMissing.isPending ? 'Removing…' : 'Remove'}
                </Button>
              {:else if missingCount > 0}
                <Button
                  variant={data.followed ? 'primary' : 'outline'}
                  key="w"
                  verb="add missing"
                  disabled={$wantMissing.isPending}
                  onclick={() => $wantMissing.mutate(true)}
                >
                  {#if $wantMissing.isPending}
                    <Icon name="busy" size="sm" class="animate-spin" />
                  {:else}
                    <Icon name="want" size="sm" />
                  {/if}
                  Add {missingCount} missing
                </Button>
              {/if}
            {/snippet}
          </PageHeader>

          {#if confirmUnfollow && data.followed}
            <!-- The question sits under the title row, where the buttons that
                 answer it have taken the place of Unfollow. -->
            <div class="flex flex-col gap-1 border-b border-rule px-5 py-3">
              <h2 class="text-lead font-semibold text-ink">Unfollow {data.name}?</h2>
              <p class="text-body text-ink-2">
                Schall stops watching for their new releases. Your files stay.
              </p>
            </div>
          {/if}

          <!-- The answer sits under the button that asked, because "wanted 3 across 7
               releases" is what somebody pressing it needs to see and a page-level
               toast would say it somewhere else. -->
          {#if $wantMissing.isError || $wantMissing.data?.wantMissing}
            <div class="max-w-prose border-b border-rule px-5 py-2.5">
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

          <div class="pt-3.5">
            <ControlRail label="Which releases to show">
              <Segmented
                options={tabs}
                value={completeness}
                onchange={(value) => show(value as '' | ReleaseCompleteness)}
                label="Which releases to show"
                pending={$artist.isPending}
              />
            </ControlRail>
          </div>

          <div class="flex flex-col gap-4 pt-4">
            {#each [$unfollow.error, $follow.error, $setMonitor.error, $refresh.error] as error, errorIndex (errorIndex)}
              {#if error}
                <!-- Follow, unfollow, monitor and refresh are all things a person
                     pressed, so none of them asks again by itself. -->
                <div class="px-5"><ErrorNote {error} /></div>
              {/if}
            {/each}

            {#if data.refreshStatus === 'failed'}
              <!-- `refreshError` is the text the last metadata refresh left behind: a
                   Go sentence stored on the artist, not something thrown at a request.
                   It reads the same way as any other failure, so it goes through the
                   same note. -->
              <div class="px-5"><ErrorNote error={data.refreshError ?? 'Refresh failed.'} /></div>
            {/if}

            {#if $releases.isError}
              <div class="px-5"><ErrorNote error={$releases.error} retry={() => $releases.refetch()} /></div>
            {/if}
            <!-- The list polls while a refresh runs, so one failed poll leaves the
                 releases already drawn under the note rather than wiping them. -->
            {#if !$releases.isError || $releases.data}
              <Settle pending={$releases.isPending}>
                {#snippet placeholder()}
                  {@render ReleaseSkeleton()}
                {/snippet}
                {#if sections.length}
                  <div class="flex flex-col gap-6">
                    {#each sections as section (section.kind)}
                      {@const open = expanded.includes(section.kind)}
                      <section class="flex flex-col" aria-label={section.kind}>
                        <div class="mx-5 flex items-baseline gap-2.5 border-b border-rule-strong pb-1.5">
                          <h2 class="label">{section.kind}</h2>
                          <span class="mono text-micro text-ink-4">{section.items.length}</span>
                          {#if section.items.length > rowLength}
                            <button
                              class="tap ml-auto inline-flex w-fit items-center gap-1 text-meta font-medium text-ink-3 transition hover:text-ink"
                              aria-expanded={open}
                              onclick={() =>
                                (expanded = open
                                  ? expanded.filter((kind) => kind !== section.kind)
                                  : [...expanded, section.kind])}
                            >
                              {open ? 'Show less' : `Show all ${section.items.length}`}
                              <Icon name={open ? 'chevron-up' : 'chevron-right'} size="sm" />
                            </button>
                          {/if}
                        </div>

                        <div class="relative" use:glide={{ key: cursorId }}>
                          {#each visibleOf(section) as release (release.id)}
                            <ReleaseRow
                              {release}
                              meta={meta(release, section.kind)}
                              state={release.albumType === 'other'
                                ? `${describe(release)} · listed as Other`
                                : describe(release)}
                              tile={tile(release)}
                              owned={release.ownedTrackCount}
                              countable={countable(release)}
                              wanted={releaseWanted(release)}
                              wantable={release.trackCount > 0 && tile(release) !== 'busy'}
                              here={cursorId === `release:${release.id}`}
                              onfocus={() => (cursorId = `release:${release.id}`)}
                              onwant={() => $toggleRelease.mutateAsync({ kind: 'want', id: release.id })}
                              onunwant={() => $toggleRelease.mutateAsync({ kind: 'remove', id: release.id })}
                            />
                          {/each}
                        </div>
                      </section>
                    {/each}
                    {#if $toggleRelease.isError}
                      <div class="px-5"><ErrorNote error={$toggleRelease.error} /></div>
                    {:else if removed}
                      <p class="px-5 text-meta text-ink-3">{removed}.</p>
                    {/if}
                  </div>
                {:else}
                  <div class="px-5">
                    <EmptyPanel
                      role={refreshing ? 'busy' : 'idle'}
                      heading={refreshing
                        ? 'Building the discography'
                        : releaseCount
                          ? 'Nothing in this filter'
                          : 'No releases yet'}
                    >
                      {#if !refreshing && releaseCount}
                        <p class="text-body leading-relaxed text-ink-2">
                          {releaseCount} releases known · none {filterName.toLocaleLowerCase()}
                        </p>
                      {/if}
                      {#if !refreshing && !releaseCount}
                        <Button class="w-fit" disabled={$refresh.isPending} onclick={() => $refresh.mutate()}>
                          <Icon name="refresh" size="sm" /> Refresh discography
                        </Button>
                      {/if}
                    </EmptyPanel>
                  </div>
                {/if}
              </Settle>
            {/if}

            <div class="pt-2">
              <PopularTracks
                {artistID}
                artistName={data.name}
                cursorId={cursorId.startsWith('track:') ? cursorId.slice(6) : ''}
                onpick={(recordingId) => (cursorId = `track:${recordingId}`)}
              />
            </div>
          </div>
        </div>

        <!-- The inspector: the picture, whether the artist is followed, and the
             facts as keys and values. -->
        <aside
          class="flex min-w-0 flex-col gap-5 border-rule bg-panel p-5 max-lg:border-t lg:sticky lg:top-10 lg:max-h-[calc(100dvh-4.5rem)] lg:min-h-[calc(100dvh-4.5rem)] lg:overflow-y-auto lg:border-l"
          aria-label="About this artist"
        >
          <span class="block aspect-square w-full max-w-[18.75rem] overflow-hidden rounded-cover">
            {#if !portraitMissing}
              <Cover
                eager
                src={`/api/v1/artists/${artistID}/image`}
                seed={artistID}
                onmissing={() => (portraitMissing = true)}
                class="size-full object-cover"
              />
            {:else}
              <Cover
                eager
                src={data.leadAlbumId ? coverSrc(data.leadAlbumId) : undefined}
                seed={artistID}
                class="size-full object-cover"
              />
            {/if}
          </span>

          <dl class="grid grid-cols-[5.5rem_minmax(0,1fr)] gap-x-3 gap-y-2.5 text-body">
            <dt class="label pt-0.5">Following</dt>
            <dd class="text-ink-2">
              <!-- Not following is said here once. The summary under it says why
                   the artist is in the catalogue at all. -->
              {#key `${monitorValue}:${followControlReset}`}
                <PillSelect
                  options={monitorOptions}
                  value={monitorValue}
                  onchange={chooseMonitor}
                  label="Follow this artist"
                />
              {/key}
              {#if !data.followed}
                <span class="mt-1.5 block text-meta text-ink-3">
                  {data.catalogueSummary ?? 'Here because you have their music.'}
                </span>
              {/if}
            </dd>

            <dt class="label pt-0.5">Owned</dt>
            <dd class="text-ink-2">
              {#if countedReleases}
                <OwnedBar owned={ownedCount} total={countedReleases} noun={basis} />
              {:else}
                <span class="text-ink-4">No releases counted</span>
              {/if}
            </dd>

            <dt class="label pt-0.5">Updated</dt>
            <dd class="flex items-center gap-1.5 text-meta text-ink-2">
              {#if refreshing}
                <Icon name="busy" size="sm" class="animate-spin text-busy" /> Refreshing
              {:else if data.lastRefreshedAt}
                <span class="mono">{calendarDate(data.lastRefreshedAt)}</span>
              {:else}
                not refreshed yet
              {/if}
            </dd>

            {#if data.genres?.length}
              <!-- What MusicBrainz's community voted this artist is. Display only,
                   so it is words rather than chips to press. -->
              <dt class="label pt-0.5">Genre</dt>
              <dd class="text-meta text-ink-2">{data.genres.slice(0, 3).join(', ')}</dd>
            {/if}
          </dl>

          <!-- Who this is, from Wikipedia. Collapsed, because somebody on this page is
               usually here for the music rather than the reading; and behind a
               disclosure rather than trimmed, so the words stay in the product without
               being the first thing on it. The attribution is not chrome: Wikipedia
               text is CC BY-SA and the line under it satisfies the licence. An artist
               no encyclopaedia has heard of shows nothing at all. -->
          {#if data.biography}
            <details class="group border-t border-rule pt-3">
              <summary
                class="tap-tall inline-flex w-fit cursor-pointer list-none items-center gap-1 text-meta text-ink-3 transition hover:text-ink-2 [&::-webkit-details-marker]:hidden"
              >
                About
                <Icon name="chevron-right" size="sm" class="transition-transform group-open:rotate-90" />
              </summary>
              <p class="reveal mt-2 text-body leading-relaxed text-ink-2">
                {data.biography}
              </p>
              {#if data.biographySourceUrl}
                <p class="mt-1.5 text-meta text-ink-4">
                  From
                  <a
                    href={data.biographySourceUrl}
                    target="_blank"
                    rel="noreferrer"
                    class="underline transition hover:text-ink">Wikipedia</a
                  >
                </p>
              {/if}
            </details>
          {/if}
        </aside>
      </div>
    {/if}
  </Settle>
{/if}

{#snippet ReleaseSkeleton()}
  <div class="flex max-h-[calc(100dvh-30rem)] flex-col overflow-hidden" role="status" aria-label="Loading releases">
    <div class="mx-5 pb-1.5"><Skeleton class="h-3 w-16" /></div>
    {#each Array(10) as _, placeholderIndex (placeholderIndex)}
      <div class="h-9 border-b border-rule px-5 py-2" aria-hidden="true">
        <Skeleton class="h-full" />
      </div>
    {/each}
  </div>
{/snippet}

{#snippet ArtistSkeleton()}
  <div role="status" aria-label="Loading artist page" class="grid lg:grid-cols-[minmax(0,1fr)_21.25rem]">
    <!-- The frame the artist draws: the title spot and the list on the left, the
         inspector on the right. -->
    <div class="flex min-w-0 flex-col gap-2 pt-3.5" aria-hidden="true">
      <div class="flex flex-col gap-2 px-5">
        <Skeleton class="h-3 w-40" />
        <Skeleton class="h-8 w-96 max-w-full" />
        <Skeleton class="h-4 w-56" />
      </div>
      <div class="mt-4">
        {@render ReleaseSkeleton()}
      </div>
    </div>
    <div class="hidden border-l border-rule bg-panel p-5 lg:block" aria-hidden="true">
      <Skeleton shape="cover" class="w-[18.75rem]" />
    </div>
  </div>
{/snippet}

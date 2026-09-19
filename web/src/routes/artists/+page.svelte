<script lang="ts">
  import {
    createMutation,
    createQuery,
    keepPreviousData,
    queryOptions,
    useQueryClient
  } from '@tanstack/svelte-query';
  import { toStore } from 'svelte/store';
  import { Check, Download, Ear, LoaderCircle, Plus, Search, X } from '@lucide/svelte';
  import { page } from '$app/state';
  import { api, type ArtistListItem } from '$lib/api';
  import { isAuthError } from '$lib/errors';
  import { keepInUrl, urlChoice, urlText } from '$lib/utils';
  import Button from '$lib/components/Button.svelte';
  import Chip from '$lib/components/Chip.svelte';
  import ControlRail from '$lib/components/ControlRail.svelte';
  import Cover from '$lib/components/Cover.svelte';
  import EmptyPanel from '$lib/components/EmptyPanel.svelte';
  import ErrorNote from '$lib/components/ErrorNote.svelte';
  import FollowArtistModal from '$lib/components/FollowArtistModal.svelte';
  import OwnedBar from '$lib/components/OwnedBar.svelte';
  import PillSelect from '$lib/components/PillSelect.svelte';
  import Segmented from '$lib/components/Segmented.svelte';
  import SetupStep from '$lib/components/SetupStep.svelte';
  import Settle from '$lib/components/Settle.svelte';

  type Tab = '' | 'incomplete' | 'complete' | 'attention';
  type Sort = 'name' | 'least-complete' | 'most-missing';

  const sorts: { value: Sort; label: string }[] = [
    { value: 'name', label: 'Name' },
    { value: 'least-complete', label: 'Least complete' },
    { value: 'most-missing', label: 'Most missing' }
  ];

  // The scope choices, written as data because the picker needs the same list
  // twice: once to draw the chips, once to answer a letter typed while the
  // list is shut.
  const scopeChoices: { value: '' | 'followed' | 'held'; label: string }[] = [
    { value: '', label: 'Everyone' },
    { value: 'followed', label: 'Followed' },
    { value: 'held', label: 'In your library' }
  ];

  const scopes = ['', 'followed', 'held'] as const;
  const completenesses = ['', 'incomplete', 'complete', 'attention'] as const;

  // Read once, before anything is tracked, so that following the address bar
  // back into it later cannot become a dependency of the effect that writes it.
  const opened = page.url;

  let showAddArtist = $state(false);
  let searchInput = $state(urlText(opened, 'q'));
  let search = $state(urlText(opened, 'q'));
  // Scope narrows the page to one of the two reasons an artist is listed. It
  // opens on everyone rather than on a default the user has to undo: unlike the
  // releases browser, whose catalogue holds releases nobody asked for, both
  // halves of this list are artists the user would recognise.
  let scope = $state<'' | 'followed' | 'held'>(urlChoice(opened, 'scope', scopes, ''));
  // Completeness is the other question, and a narrower one: how much of an
  // artist the library actually holds. It sits apart from scope because the two
  // do not answer each other — a followed artist can be complete and a held one
  // can want attention.
  let tab = $state<Tab>(urlChoice(opened, 'completeness', completenesses, ''));
  let sort = $state<Sort>(
    urlChoice(
      opened,
      'sort',
      sorts.map((option) => option.value),
      'name'
    )
  );
  // The third question: what kind of music. It is a whole genre from the list
  // MusicBrainz's community has voted the catalogue's artists into, never a
  // typed pattern, so an empty choice means every genre and any other choice
  // means exactly that one.
  let genre = $state(urlText(opened, 'genre'));

  // The genres the catalogue actually holds somebody under. Asked for once and
  // kept: it changes only when an artist refresh writes new votes, and a list
  // of words costs nothing to hold.
  const genres = createQuery(
    toStore(() => ({
      queryKey: ['catalogue-genres'],
      queryFn: () => api.catalogueGenres(),
      staleTime: 5 * 60 * 1000
    }))
  );

  const genreChoices = $derived([
    { value: '', label: 'Any genre' },
    ...($genres.data?.items ?? []).map((name) => ({ value: name, label: name }))
  ]);

  // The Labels chip's own count. Labels are not paged, so the same list the
  // labels page reads is small enough to fetch here too, and the two pages
  // share the cache entry.
  const labels = createQuery({ queryKey: ['labels'], queryFn: api.labels });

  // The address bar follows the list rather than leading it. Every one of these
  // is already set by a press that refetched, so this only records where the
  // reader got to — and that record is what a reload and a shared link restore.
  $effect(() => {
    keepInUrl(
      opened.pathname,
      { scope, q: search, completeness: tab, genre, sort },
      { scope: '', q: '', completeness: '', genre: '', sort: 'name' }
    );
  });

  // Scoped, searched, narrowed and sorted on the server.
  // The held half of the list grows with the library rather than with anything
  // the user asked for — a collection acquired album by album adds one artist
  // per album — so ordering by completeness has to happen where the whole list
  // is. Sorting a page that had already arrived would order one arbitrary
  // window of the catalogue and present it as the collection.
  //
  // The key carries every one of those, because the answer is different for
  // each and they are what the reader changed. A key that named only the list
  // would file five different questions under one answer: pressing a scope
  // while the unfiltered list was still in flight joins that request instead of
  // asking the new one, and the page settles showing everyone under a selected
  // tab.
  const artistOptions = $derived(
    queryOptions({
      queryKey: ['artists', scope, search, tab, genre, sort],
      queryFn: () =>
        api.artists({
          scope: scope || undefined,
          query: search || undefined,
          completeness: tab || undefined,
          genre: genre || undefined,
          sort
        }),
      // Each of those keys is its own cache entry, so without this the counts
      // and the list would empty out between one and the next and the page
      // would flash its loading state on every press.
      placeholderData: keepPreviousData,
      // Counted across the whole list. An artist being refreshed is still being
      // refreshed, and the list that stopped watching for them
      // would never show the result. The event stream says when a refresh
      // starts and settles, so this is the safety net rather than the
      // mechanism: it covers a stream blocked by a proxy or dropped without
      // reconnecting yet.
      refetchInterval: (query) =>
        isAuthError(query.state.error)
          ? false
          : (query.state.data?.refreshingCount ?? 0) > 0
            ? 15_000
            : false
    })
  );
  const artists = createQuery(toStore(() => artistOptions));
  // Enough tiles to fill a 4K screen (23 columns of 9 rows); the container
  // clips whatever the viewport cannot show, so the count only has to be big.
  const artistSkeletonCount = 210;

  // /artists is the landing page, so the first-run guide lives here. Both
  // queries are the ones the rest of the application already uses.
  const queryClient = useQueryClient();

  const dashboard = createQuery({ queryKey: ['dashboard'], queryFn: api.dashboard });

  const library = createQuery({
    queryKey: ['library'],
    queryFn: api.library,
    // A running scan announces its progress; this is the fallback for a stream
    // that never arrived.
    refetchInterval: (query) =>
      isAuthError(query.state.error)
        ? false
        : ['queued', 'running'].includes(query.state.data?.scanStatus ?? '')
          ? 15_000
          : false
  });

  const scanning = $derived(['queued', 'running'].includes($library.data?.scanStatus ?? ''));
  const rootCount = $derived($library.data?.rootCount ?? 0);
  const followedCount = $derived($dashboard.data?.artistCount ?? 0);
  const scanned = $derived(($library.data?.scanCompletedAt ?? null) !== null);
  const setUp = $derived(followedCount > 0 && rootCount > 0 && scanned);
  const step = $derived(followedCount === 0 ? 1 : rootCount === 0 ? 2 : 3);
  // Until both answers are in, setup is neither done nor undone, and a guide
  // shown on that guess would appear on every reload of a finished install.
  const setupKnown = $derived(Boolean($dashboard.data && $library.data));

  const scan = createMutation({
    mutationFn: api.scanLibrary,
    onSuccess: () => queryClient.invalidateQueries({ queryKey: ['library'] })
  });

  const shown = $derived($artists.data?.items ?? []);
  // An empty list under a search or a narrowed view is empty for a reason the
  // setup guide does not answer, so that panel still has its say.
  const narrowed = $derived(Boolean(search || tab || scope));
  // The guide answers an empty page better than the generic card does, so the
  // card stands down while the guide is up. Above the grid rather than instead
  // of it, because a root or a scan can still be missing once artists exist.
  const guiding = $derived(setupKnown && !setUp && !narrowed);
  const followedTotal = $derived($artists.data?.followedCount);
  const heldTotal = $derived($artists.data?.heldCount);
  // Every artist the catalogue holds a row for is either followed or held —
  // the two are the scope's whole partition (queries/artists.sql) — so their
  // sum is the Everyone chip's count without a query of its own.
  const everyoneTotal = $derived(
    followedTotal !== undefined && heldTotal !== undefined ? followedTotal + heldTotal : undefined
  );
  const labelsTotal = $derived($labels.data?.total);

  // The tab counts label the choices the user is picking between, so none of
  // them changes when a choice is made.
  //
  // Each one is left undefined until the list answers, rather than falling back
  // to nought. The fallback was read as a figure: the strip said every category
  // held nothing, and a second later said forty, thirty-three, seven and two.
  // Nobody can tell that first reading from a collection that is genuinely
  // empty, so for as long as the request takes, the screen was lying.
  const tabs = $derived<{ value: Tab; name: string; count?: number }[]>([
    { value: '', name: 'All', count: $artists.data?.allCount },
    { value: 'incomplete', name: 'Incomplete', count: $artists.data?.incompleteCount },
    { value: 'complete', name: 'Complete', count: $artists.data?.completeCount },
    { value: 'attention', name: 'Needs attention', count: $artists.data?.attentionCount }
  ]);

  // Four things a card can point at, in the order they are worth pointing at.
  // A copy waiting for an ear is the only one asking the user for something, so
  // it comes first; a discography never fetched is last because it explains an
  // empty card rather than reporting an event.
  type Badge = { label: string; role: 'decide' | 'busy' | 'fail' | 'idle' };
  function badgeOf(artist: ArtistListItem): Badge | null {
    // An uploader on another service has no discography and never will, so
    // "not scanned" would be a promise nothing is going to keep. Completeness
    // is not a question that applies to them.
    if (artist.source) return { label: 'not applicable', role: 'idle' };
    if (artist.reviewCount > 0) {
      return { label: artist.reviewCount === 1 ? 'review' : `review ${artist.reviewCount}`, role: 'decide' };
    }
    if (artist.refreshStatus === 'failed') return { label: 'failed', role: 'fail' };
    if (artist.inFlightCount > 0) return { label: 'downloading', role: 'busy' };
    if (artist.releaseCount === 0) return { label: 'not scanned', role: 'idle' };
    return null;
  }

  // Up to three initials, skipping the words nobody thinks of as part of a name.
  // A single word keeps two letters, so "Burial" reads BU rather than B.
  function initialsOf(name: string): string {
    const words = name
      .split(/[\s.]+/)
      .filter((word) => word && !['of', 'the', 'and', 'a'].includes(word.toLowerCase()));
    if (words.length === 0) return name.slice(0, 2).toUpperCase();
    if (words.length === 1) return words[0].slice(0, 2).toUpperCase();
    return words
      .slice(0, 3)
      .map((word) => word[0])
      .join('')
      .toUpperCase();
  }

  function applySearch(event: SubmitEvent) {
    event.preventDefault();
    search = searchInput.trim();
    // Submitting the term already shown changes no key, and a key is what
    // fetches now, so the press would do nothing at all. It is the only manual
    // refresh this page has — the one a reader uses when they think something
    // has changed underneath them — so it asks again rather than sit there.
    void queryClient.invalidateQueries({ queryKey: ['artists'] });
  }

  function setScope(value: '' | 'followed' | 'held') {
    scope = value;
  }

  function setTab(value: Tab) {
    tab = value;
  }

  function setGenre(value: string) {
    genre = value;
  }

  function setSort(value: Sort) {
    sort = value;
  }

  function clearFilters() {
    searchInput = '';
    search = '';
    scope = '';
    tab = '';
    genre = '';
    sort = 'name';
  }

  const hasFilters = $derived(
    Boolean(searchInput || search || scope || tab || genre || sort !== 'name')
  );
</script>

<svelte:head><title>Artists · Schall</title></svelte:head>

<!-- Hidden: the mast's highlight already says this is Artists, but that
     highlight is not a document heading. -->
<h1 class="sr-only">Artists</h1>

<ControlRail>
  <!-- Not a fourth scope: it leaves this list for the labels one, so it sits
       apart from the group it is not a member of. -->
  <a href="/artists/labels" class="flex h-8 shrink-0 items-center gap-1.5 text-body text-ink-2 hover:text-ink">
    Labels
    {#if labelsTotal !== undefined}
      <span class="numeric text-meta text-ink-3">{labelsTotal.toLocaleString()}</span>
    {:else}
      <span
        class="numeric inline-block h-2.5 w-[2ch] animate-pulse rounded-row bg-white/10"
        aria-hidden="true"
      ></span>
    {/if}
  </a>

  <Button class="ml-auto" onclick={() => (showAddArtist = true)}>
    <Plus size={13} strokeWidth={2.3} /> Follow artist
  </Button>
</ControlRail>

<ControlRail sticky={false}>
  <Segmented
    options={scopeChoices.map((choice) => ({
      value: choice.value,
      name: choice.label,
      count:
        choice.value === '' ? everyoneTotal : choice.value === 'followed' ? followedTotal : heldTotal
    }))}
    value={scope}
    onchange={(value) => setScope(value as '' | 'followed' | 'held')}
    label="Which artists to show"
    pending={$artists.isPending}
    variant="filter"
  />

  <Segmented
    options={tabs}
    value={tab}
    onchange={(value) => setTab(value as Tab)}
    label="How complete an artist is"
    pending={$artists.isPending}
    variant="filter"
  />

  <form class="ml-auto w-full sm:w-48" onsubmit={applySearch}>
    <label class="field flex w-full items-center gap-2">
      <Search size={13} strokeWidth={2} class="shrink-0 text-ink-4" />
      <input
        bind:value={searchInput}
        placeholder="Search artists, then Enter"
        aria-label="Search artists"
        enterkeyhint="search"
        class="min-w-0 flex-1 bg-transparent font-sans text-meta text-ink outline-none placeholder:text-ink-4"
      />
    </label>
  </form>

  <PillSelect
    options={genreChoices.map((choice) => ({ value: choice.value, name: choice.label }))}
    value={genre}
    onchange={setGenre}
    label="Which genre"
    quiet
    prefix="Genre"
  />

  <PillSelect
    options={sorts.map((option) => ({ value: option.value, name: option.label }))}
    value={sort}
    onchange={(value) => setSort(value as Sort)}
    label="How to sort artists"
    quiet
    prefix="Sort"
  />

  {#if hasFilters}
    <Button variant="ghost" size="sm" onclick={clearFilters}>Clear filters</Button>
  {/if}
</ControlRail>

<div class="flex flex-col gap-5 px-4 sm:px-6 py-5">
  {#if guiding}
    {@render Setup()}
  {/if}

  {#if $artists.isError}
    <!-- The list of artists is read, not written, so asking again is the whole
         answer: the note asks by itself rather than printing a button. -->
    <ErrorNote error={$artists.error} retry={() => $artists.refetch()} />
  {:else}
    <Settle pending={$artists.isPending}>
      {#snippet placeholder()}
        <div
          class="max-h-[calc(100dvh-16rem)] grid grid-cols-2 gap-x-4 gap-y-5 overflow-hidden sm:grid-cols-[repeat(auto-fill,minmax(9.25rem,1fr))]"
          role="status"
          aria-label="Loading artists"
        >
          {#each Array(artistSkeletonCount) as _, placeholderIndex (placeholderIndex)}
            <div class="flex flex-col gap-2" aria-hidden="true">
              <div
                class="aspect-square animate-pulse rounded-card border border-line-regular bg-surface-regular"
              ></div>
              <span class="flex flex-col gap-1">
                <span class="h-4 w-2/3 animate-pulse rounded-row bg-surface-regular"></span>
                <span class="h-4 w-full animate-pulse rounded-row bg-surface-regular"></span>
              </span>
            </div>
          {/each}
        </div>
      {/snippet}
      {#if shown.length}
        <!-- Narrowing the list is a change worth seeing happen. Pressing Complete
             swapped one set of cards for another between two frames, which reads as
             the page having been redrawn rather than as the list having answered.

             Keyed on what was narrowed and not on what was typed. A filter or a
             scope is one decision and gets one arrival; a search is a letter at a
             time, and animating that would put the list through twelve arrivals to
             spell one artist's name. `.rise` is the same arriving step a card uses
             anywhere else, and it holds still for anyone who asked for less
             motion. -->
        {#key `${tab}|${scope}|${sort}`}
          <div
            class="rise grid grid-cols-2 gap-x-4 gap-y-5 sm:grid-cols-[repeat(auto-fill,minmax(9.25rem,1fr))]"
          >
            {#each shown as artist (artist.id)}{@render Card(artist)}{/each}
          </div>
        {/key}
      {:else if !guiding}
        <!-- The heading is the whole message: it names what is absent, and the
             buttons under it are what to do about it. -->
        <EmptyPanel
          role="idle"
          heading={search
            ? 'No artists match that search'
            : tab === 'complete'
              ? 'No artist is complete yet'
              : tab === 'incomplete'
                ? 'Nothing incomplete'
                : tab === 'attention'
                  ? 'Nothing needs attention'
                  : scope === 'followed'
                    ? 'No artists followed'
                    : scope === 'held'
                      ? 'No artists in your library'
                      : 'No artists yet'}
        >
          <div class="flex flex-wrap gap-2">
            <Button class="w-fit" onclick={() => (showAddArtist = true)}>
              <Plus size={13} strokeWidth={2.3} /> Follow artist
            </Button>
            {#if tab}
              <Button variant="outline" class="w-fit" onclick={() => setTab('')}>Show all</Button>
            {/if}
            {#if scope}
              <Button variant="outline" class="w-fit" onclick={() => setScope('')}
                >Show everyone</Button
              >
            {/if}
          </div>
        </EmptyPanel>
      {/if}
    </Settle>
  {/if}
</div>

<FollowArtistModal bind:open={showAddArtist} />

<!-- The first-run guide. A label and a rule rather than a bordered card around
     it: each step already draws its own border because each is a surface you
     act on, and a panel around them would be the second one. -->
{#snippet Setup()}
  <section class="flex max-w-2xl flex-col gap-3">
    <div class="flex items-center gap-2.5">
      <span class="label">Setup · step {step} of 3</span>
      <span class="h-px flex-1 bg-line-thin"></span>
      <span class="text-meta font-medium text-ink-4">
        nothing is written to the folders you point at
      </span>
    </div>

    <div class="flex flex-col gap-2">
      <SetupStep number="1" title="Follow an artist" complete={followedCount > 0} active={step === 1}>
        adds an artist and their releases to your catalogue
        {#snippet action()}
          {#if followedCount === 0}
            <Button onclick={() => (showAddArtist = true)}>Follow artist</Button>
          {/if}
        {/snippet}
      </SetupStep>

      <SetupStep number="2" title="Add a music folder" complete={rootCount > 0} active={step === 2}>
        point Schall at your existing local collection
        {#snippet action()}
          {#if rootCount === 0}
            <Button href="/library" variant="outline" class="shrink-0">Choose folder</Button>
          {/if}
        {/snippet}
      </SetupStep>

      <SetupStep number="3" title="Run the first scan" complete={scanned} active={step === 3}>
        read tags and identifiers from local files
        {#snippet action()}
          {#if rootCount === 0}
            <span class="rounded-row bg-idle/14 px-2 py-1 text-meta font-medium text-idle">
              waiting on step 2
            </span>
          {:else if !scanned}
            <Button onclick={() => $scan.mutate()} disabled={scanning || $scan.isPending}>
              {scanning ? 'Scanning…' : 'Scan'}
            </Button>
          {/if}
        {/snippet}
      </SetupStep>
    </div>

    {#if $scan.isError}
      <!-- Scan is the button directly above, so the way out names it rather
           than the general sentence the error map would have written. -->
      <ErrorNote
        error={$scan.error}
        action="Press Scan again once your folders are reachable."
      />
    {/if}
  </section>
{/snippet}

{#snippet Card(artist: ArtistListItem)}
  {@const badge = badgeOf(artist)}
  <a
    href={`/artists/${artist.id}`}
    class="group flex min-w-0 flex-col gap-2"
    title={artist.releaseCount === 0
      ? `${artist.name} — discography not fetched`
      : `${artist.name} — ${artist.ownedReleaseCount} of ${artist.releaseCount} releases owned`}
  >
    <span
      class="relative grid aspect-square place-items-center overflow-hidden rounded-card border border-line-regular bg-surface-thin transition group-hover:border-line-thick"
    >
      <span class="font-display text-[2rem] font-semibold text-ink-4">
        {initialsOf(artist.name)}
      </span>
      <!-- Over the initials rather than instead of them: an artist nobody has a
           picture of keeps the letters, and one who has covers them. Only a
           card whose row says a picture is cached asks for it. -->
      <Cover
        src={artist.hasImage ? `/api/v1/artists/${artist.id}/image` : undefined}
        class="absolute inset-0 size-full object-cover"
      />

      {#if badge}
        <!-- A chip anywhere else in Schall sits on the application's own dark
             ground, and a tint of its role at 14% is enough to separate it from
             that. Here it sits on a photograph of the artist, which is any
             colour at all, and over a bright one the words disappeared. The
             chip brings its own ground: opaque, blurred, and dark enough that
             the role's colour reads on it whatever the picture beneath is
             doing. -->
        <Chip
          role={badge.role}
          dot={false}
          class="absolute left-2 top-2 bg-ground/85 backdrop-blur-sm"
        >
          <!-- Paired with a glyph, because no state may be readable by hue alone. -->
          {#if badge.role === 'decide'}
            <Ear size={9} strokeWidth={2.4} />
          {:else if badge.role === 'busy'}
            <Download size={9} strokeWidth={2.4} />
          {:else if badge.role === 'fail'}
            <X size={9} strokeWidth={3} />
          {:else}
            <LoaderCircle size={9} strokeWidth={2.4} />
          {/if}
          {badge.label}
        </Chip>
      {/if}

      {#if ['queued', 'running'].includes(artist.refreshStatus)}
        <span
          class="absolute right-2 top-2 grid size-[18px] place-items-center rounded-row bg-ground/85 text-busy backdrop-blur-sm"
          title="Refreshing this artist's discography"
        >
          <LoaderCircle size={10} class="animate-spin" />
        </span>
      {/if}
    </span>

    <span class="flex min-w-0 flex-col gap-1.5">
      <span class="truncate text-body font-medium text-ink">{artist.name}</span>
      <!-- Owned is always the bar and the fraction, never a percentage alone.
           An artist with no discography fetched yet, or one on a service
           MusicBrainz has never heard of, has no fraction to state — saying
           "0 of 0" would read as a complete answer to a question nothing has
           asked yet.

           The fraction is compact ("13/13") and the bar narrow, because the
           check, the bar and the figure all have to sit on the one line a
           card this narrow has room for. -->
      {#if artist.source}
        <span class="truncate text-meta text-ink-3">from SoundCloud</span>
      {:else if artist.releaseCount === 0}
        <span class="truncate text-meta text-ink-3">discography not fetched</span>
      {:else}
        <span class="flex items-center gap-1">
          {#if artist.followed && artist.ownedReleaseCount === artist.releaseCount}
            <Check size={9} strokeWidth={3} class="shrink-0 text-ok" />
          {/if}
          <OwnedBar
            owned={artist.ownedReleaseCount}
            total={artist.releaseCount}
            width={40}
            noun="releases"
            compact
          />
        </span>
      {/if}
    </span>
  </a>
{/snippet}

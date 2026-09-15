<script lang="ts">
  import { toStore } from 'svelte/store';
  import { createMutation, createQuery, useQueryClient } from '@tanstack/svelte-query';
  import {
    ArrowDown,
    ArrowUp,
    Check,
    EyeOff,
    LoaderCircle,
    RefreshCw,
    RotateCcw,
    Search,
    UserRound,
    X
  } from '@lucide/svelte';
  import { goto } from '$app/navigation';
  import { page } from '$app/state';
  import { api, type Release, type ReleaseStatus } from '$lib/api';
  import { keepInUrl, urlChoice, urlCount, urlId, urlText } from '$lib/utils';
  import Button from '$lib/components/Button.svelte';
  import { holding, progress, quiet } from '$lib/vocabulary';
  import Cover from '$lib/components/Cover.svelte';
  import ControlRail from '$lib/components/ControlRail.svelte';
  import EmptyPanel from '$lib/components/EmptyPanel.svelte';
  import ErrorNote from '$lib/components/ErrorNote.svelte';
  import OwnedBar from '$lib/components/OwnedBar.svelte';
  import Pager from '$lib/components/Pager.svelte';
  import PillSelect from '$lib/components/PillSelect.svelte';
  import Segmented from '$lib/components/Segmented.svelte';
  import Settle from '$lib/components/Settle.svelte';
  import StateMark from '$lib/components/StateMark.svelte';
  import StateTag from '$lib/components/StateTag.svelte';
  import * as Table from '$lib/components/ui/table';
  import StatusBadge from '$lib/components/StatusBadge.svelte';

  const queryClient = useQueryClient();

  type Sort = 'artist' | 'title' | 'year' | 'owned';

  // The same shapes the server filters by, named as the table names them. They
  // each carry a count now: a release holds its own track and owned-track
  // counts, so every shape is arithmetic in the one pass the scope total
  // already cost, and the counts arrive with the page.
  //
  // "No track list" has no chip of its own here: the board folds it into
  // Problems. The server still tells the two apart — this only stops offering
  // a way to ask for "no track list" on its own, and a release in that shape
  // still says so in its own Status cell.
  const filters: { key: '' | ReleaseStatus; name: string }[] = [
    { key: '', name: 'All' },
    { key: 'owned', name: 'Owned' },
    { key: 'partial', name: holding.partial },
    { key: 'missing', name: holding.missing },
    { key: 'failed', name: 'Problems' }
  ];

  const scopes = ['library', 'followed', ''] as const;
  const sortKeys = ['artist', 'title', 'year', 'owned'] as const;

  // Which of the library's views is showing. The catalogue is the one that
  // writes the address bar — `keepInUrl` replaces the query string rather than
  // merging into it, so a second writer would take turns wiping this record —
  // and it therefore has to carry the view along with everything else.
  let {
    view = 'releases',
    onviewfiles
  }: { view?: 'releases' | 'files' | 'duplicates'; onviewfiles?: () => void } = $props();

  // Read once, before anything is tracked, so that following the address bar
  // back into it later cannot become a dependency of the effect that writes it.
  const opened = page.url;

  let searchInput = $state(urlText(opened, 'q'));
  let search = $state(urlText(opened, 'q'));
  let sort = $state<Sort>(urlChoice(opened, 'sort', sortKeys, 'artist'));
  let descending = $state(urlChoice(opened, 'dir', ['asc', 'desc'] as const, 'asc') === 'desc');
  let status = $state<'' | ReleaseStatus>(
    urlChoice(
      opened,
      'status',
      filters.map((filter) => filter.key),
      ''
    )
  );
  // Unfollowing keeps an artist's whole discography, so the catalogue holds
  // releases nobody asked to complete and owns nothing from. They are real and
  // worth reaching, but they are not what this page is usually for.
  let scope = $state<'library' | 'followed' | ''>(urlChoice(opened, 'scope', scopes, 'library'));
  // One artist's part of the catalogue. Nobody types an id, so this arrives on
  // a link from that artist's page and is cleared here; it narrows by the row
  // the server keys on rather than by their name, which would reach every
  // artist whose own name contains theirs. The id the page opened at is kept
  // beside the filter because the filter can only be cleared, never set, so
  // that is the one artist this page ever has to name.
  const openedArtist = urlId(opened, 'artistId');
  let artistId = $state(openedArtist);
  let offset = $state(urlCount(opened, 'offset'));
  let searchBox = $state<HTMLInputElement | null>(null);
  const pageSize = 48;

  // What one run may cover, matching the limit the API enforces. Every release
  // is a separate provider search the worker runs one after another, so a run is
  // bounded by patience rather than by the size of the backlog. A page is 48, so
  // selecting everything shown already sits inside the cap; it is kept because a
  // selection survives paging and could otherwise climb past a run by walking.
  const maxRun = 50;

  // Browsing is what this page is for, so selection is off until it is asked
  // for: a checkbox on every row of a catalogue nobody is acting on is chrome in
  // the way. Turning it off drops the selection with it.
  let selecting = $state(false);
  let selected = $state<string[]>([]);
  // Permission for the run to record a download without asking, given here
  // rather than kept as a setting: it is granted by whoever is about to look at
  // these results, for these releases, and it resets with every run.
  let autoRequest = $state(false);

  // The address bar follows the list rather than leading it. Every one of these
  // is already in the list's own key, so this only records where the reader got
  // to — and that record is what a reload and a shared link restore.
  // The scope is the one whose default is not the empty value: everyone is a
  // deliberate widening, and a link to it has to say so.
  $effect(() => {
    keepInUrl(
      opened.pathname,
      { view, artistId, scope, q: search, status, sort, dir: descending ? 'desc' : 'asc', offset },
      {
        view: 'releases',
        artistId: '',
        scope: 'library',
        q: '',
        status: '',
        sort: 'artist',
        dir: 'asc',
        offset: 0
      }
    );
  });

  // Paged on the server, and scoped, searched, filtered and sorted there too.
  // The catalogue grows with every artist followed rather than with anything
  // asked for one release at a time, and narrowing a page that has already
  // arrived would narrow the page rather than the list — the count under the
  // filter would be of whatever happened to load, which is not an answer.
  //
  // The artist, the scope, the search, the filter, the order and the page are
  // therefore all in the key, because each of them changes the answer. Under one
  // key for the whole list a press had to be followed by a refetch of that key,
  // and a refetch while the first request is still in flight joins the request
  // already running rather than starting a new one — so the strip moved to
  // Missing and the rows under it stayed the ones the page opened with. The
  // search in the key is the submitted one, not what is in the box, so typing
  // asks the server nothing until the form is sent.
  const releases = createQuery(
    toStore(() => ({
      queryKey: ['releases', 'browser', artistId, scope, search, status, sort, descending, offset],
      queryFn: () =>
        api.releases({
          artistId: artistId || undefined,
          scope: scope || undefined,
          query: search || undefined,
          status: status || undefined,
          sort,
          direction: descending ? 'desc' : 'asc',
          limit: pageSize,
          offset
        })
    }))
  );

  // Whose releases these are, asked for rather than read off the first row: a
  // filter that matched nothing has no row to read a name from, and that is
  // exactly the list somebody arrives at when the artist's gaps are all in
  // releases this scope leaves out. The key is the artist page's own, so
  // arriving from there the name is already in hand and nothing is fetched
  // twice.
  const artist = createQuery({
    queryKey: ['artists', openedArtist],
    queryFn: () => api.artist(openedArtist),
    enabled: openedArtist !== ''
  });

  const shown = $derived($releases.data?.items ?? []);
  const total = $derived($releases.data?.total ?? 0);
  // What the scope and search reach before the status filter narrows them, so
  // an empty filter can say the list behind it is not empty.
  const scopeTotal = $derived($releases.data?.scopeTotal ?? 0);

  // A search that finds no release here can still find the file: an unmatched
  // upload belongs to no release, so it is invisible to this list whatever it
  // is called. This asks the files view's own endpoint, with no status or
  // resolution filter, because that is what the files view itself runs when it
  // is opened from a link with only a search on it — so the count and where
  // the link lands can never disagree. It only asks once the search has
  // already come up empty here.
  const noReleasesMatch = $derived(
    $releases.isSuccess && shown.length === 0 && scopeTotal === 0 && search.length > 0
  );
  const filesForSearch = createQuery(
    toStore(() => ({
      queryKey: ['library-files', 'search-count', search],
      queryFn: () => api.libraryFiles({ query: search || undefined, limit: 1 }),
      enabled: noReleasesMatch
    }))
  );
  const filesMatching = $derived($filesForSearch.data?.total ?? 0);

  // The rows are the fallback rather than the source: they name the same artist
  // and are already here, which covers the moment before the artist arrives and
  // an artist the catalogue no longer holds under that id.
  const artistName = $derived($artist.data?.name ?? shown[0]?.artistName ?? 'One artist');

  // The All chip counts the scope, which is what the other five are measured
  // against; the rest come back named for the shape they count.
  const tabs = $derived(
    filters.map((filter) => ({
      value: filter.key,
      name: filter.name,
      count: !$releases.data
        ? undefined
        : filter.key === ''
          ? scopeTotal
          : ($releases.data[`${filter.key}Count`] ?? 0)
    }))
  );

  // What the Status column draws: a tick for a release held in full, a dash
  // for one an "owned" artist tracks none of, nothing for an ordinary gap the
  // Owned column already draws, and a tag for what ownership cannot explain —
  // its tone says how that reads (neutral for a plain fact, warn for a data
  // problem, broken for a failure).
  type Standing =
    | { kind: 'tick' }
    | { kind: 'dash' }
    | { kind: 'none' }
    | { kind: 'tag'; tone: 'neutral' | 'attention' | 'broken' | 'warn'; label: string };

  function statusOf(release: Release): Standing {
    if (release.trackRefreshStatus === 'failed') {
      return { kind: 'tag', tone: 'broken', label: quiet(progress.failed) };
    }
    if (['queued', 'running'].includes(release.trackRefreshStatus)) {
      return { kind: 'tag', tone: 'neutral', label: quiet(progress.importing) };
    }
    if (release.trackCount === 0) return { kind: 'tag', tone: 'warn', label: 'no track list' };
    // A dismissal leaves the denominator: the user decided not to want the
    // track, and a release whose every gap was decided about is not missing.
    // At 'owned' the denominator is what the library maps — the same rule the
    // server's completeness queries apply.
    const countable =
      release.artistMonitorLevel === 'owned'
        ? release.ownedTrackCount
        : release.trackCount - release.dismissedTrackCount;
    // An "owned" artist tracks nothing beyond what the library already holds,
    // so a release it holds none of was never open to begin with — the column
    // says nothing rather than naming a gap that is not one.
    if (release.artistMonitorLevel === 'owned' && countable === 0) return { kind: 'dash' };
    // Every gap decided about by hand leaves nothing left to call missing. A
    // dismissal is not visible anywhere else in the row, so it still gets a
    // tag.
    if (countable === 0) return { kind: 'tag', tone: 'neutral', label: quiet(progress.dismissed) };
    // A plain gap, whole or partial, is exactly what the Owned column's bar
    // already draws in the same two figures. Repeating it as a tag beside the
    // bar said nothing the bar had not already said, so ownership alone draws
    // nothing here.
    if (release.ownedTrackCount < countable) return { kind: 'none' };
    return { kind: 'tick' };
  }

  const filterName = $derived(filters.find((filter) => filter.key === status)?.name ?? 'All');

  const selectable = $derived(shown.slice(0, maxRun).map((release) => release.id));
  const allSelected = $derived(
    selectable.length > 0 && selectable.every((id) => selected.includes(id))
  );
  const atCap = $derived(selected.length >= maxRun);

  function setSelecting(on: boolean) {
    selecting = on;
    if (!on) selected = [];
  }

  function toggleSelected(id: string) {
    if (selected.includes(id)) {
      selected = selected.filter((current) => current !== id);
      return;
    }
    if (atCap) return;
    selected = [...selected, id];
  }

  // Selecting everything selects what this page shows, and replaces the
  // selection rather than adding to it, so paging cannot accumulate past a run.
  function toggleAll() {
    selected = allSelected ? [] : selectable;
  }

  const sourceRun = createMutation({
    mutationFn: () => api.createSourceSearch(selected, autoRequest),
    onSuccess: (run) => {
      selected = [];
      void goto(`/releases/sources/${run.id}`);
    }
  });

  // What the last bulk press came to, in the sentence the reader needs: how much
  // of their selection it changed, and how much of it was already that way.
  // Cleared when the selection changes, because a count about a selection nobody
  // is looking at any more is a count about nothing.
  let outcome = $state('');

  function decided(message: string) {
    outcome = message;
    selected = [];
    void queryClient.invalidateQueries({ queryKey: ['releases'] });
  }

  function plural(count: number, one: string, many: string) {
    return `${count} ${count === 1 ? one : many}`;
  }

  const ignoring = createMutation({
    mutationFn: () => api.ignoreReleases(selected),
    onSuccess: (answer) => {
      const dismissed = answer.releases.reduce((total, row) => total + row.dismissed, 0);
      const already = answer.releases.reduce((total, row) => total + row.alreadyDismissed, 0);
      decided(
        [
          `Ignored ${plural(dismissed, 'track', 'tracks')}`,
          already ? `${already} already ignored` : '',
          answer.notFound.length ? `${answer.notFound.length} no longer in the catalogue` : ''
        ]
          .filter(Boolean)
          .join(' · ')
      );
    }
  });

  const rematching = createMutation({
    mutationFn: () => api.rematchReleases(selected),
    onSuccess: (answer) => {
      const queued = answer.releases.filter((row) => row.queued).length;
      const running = answer.releases.length - queued;
      decided(
        [
          `Matching ${plural(queued, 'release', 'releases')} again`,
          running ? `${running} already running` : '',
          answer.unaskable.length ? `${answer.unaskable.length} could not be asked about` : ''
        ]
          .filter(Boolean)
          .join(' · ')
      );
    }
  });

  const retrying = createMutation({
    mutationFn: () => api.retryReleases(selected),
    onSuccess: (answer) => {
      const retried = answer.releases.reduce((total, row) => total + row.retried, 0);
      const clear = answer.releases.filter((row) => row.retried === 0).length;
      decided(
        [
          `Trying ${plural(retried, 'job', 'jobs')} again`,
          clear ? `${clear} had nothing that failed` : ''
        ]
          .filter(Boolean)
          .join(' · ')
      );
    }
  });

  const deciding = $derived(
    $ignoring.isPending || $rematching.isPending || $retrying.isPending || $sourceRun.isPending
  );

  // Whichever of the three decisions was refused, as the error itself: the note
  // below is what turns it into words, and it reads the whole thing.
  const refused = $derived($ignoring.error ?? $rematching.error ?? $retrying.error ?? null);

  function applySearch(event: SubmitEvent) {
    event.preventDefault();
    search = searchInput.trim();
    offset = 0;
  }

  function setStatus(next: '' | ReleaseStatus) {
    status = next;
    offset = 0;
  }

  // Clearing the artist is the same move as changing any other filter: the
  // server is asked again, from the first page, because page 3 of one artist is
  // not page 3 of everybody.
  function clearArtist() {
    artistId = '';
    offset = 0;
  }

  function setScope(next: 'library' | 'followed' | '') {
    scope = next;
    offset = 0;
  }

  // How far the list reaches, written as data because the picker needs the same
  // list twice: once to draw the choices, once to answer a letter typed while
  // the list is shut.
  const scopeChoices: { value: 'library' | 'followed' | ''; name: string }[] = [
    { value: 'library', name: 'Followed or owned' },
    { value: 'followed', name: 'Followed artists' },
    { value: '', name: 'Everything in the catalogue' }
  ];

  // Sorting is the server's, so picking a column is a new page rather than a
  // reshuffle of this one. Going back to the first page is the honest move:
  // page 40 of one order is not page 40 of another.
  function pick(next: Sort) {
    descending = sort === next ? !descending : false;
    sort = next;
    offset = 0;
  }

  // The sort pill beside the column headers, for the same order rather than a
  // second one: each option is one of the four sortable columns in the
  // direction somebody reaches for first, and choosing one calls the same
  // `pick` a header click does. A combination nobody offers here — a column
  // sorted the other way round, reached by clicking its header twice — has no
  // matching option, so the pill shows none selected until the next choice.
  const sortChoices: { value: string; name: string; key: Sort; desc: boolean }[] = [
    { value: 'artist-asc', name: 'Artist A–Z', key: 'artist', desc: false },
    { value: 'title-asc', name: 'Title A–Z', key: 'title', desc: false },
    { value: 'year-desc', name: 'Year, newest first', key: 'year', desc: true },
    { value: 'owned-desc', name: 'Most owned first', key: 'owned', desc: true }
  ];
  const sortValue = $derived(`${sort}-${descending ? 'desc' : 'asc'}`);

  function applySort(value: string) {
    const choice = sortChoices.find((option) => option.value === value);
    if (!choice) return;
    sort = choice.key;
    descending = choice.desc;
    offset = 0;
  }

  function changePage(nextOffset: number) {
    offset = nextOffset;
  }

  function clearFilters() {
    searchInput = '';
    search = '';
    sort = 'artist';
    descending = false;
    status = '';
    scope = 'library';
    artistId = '';
    offset = 0;
  }

  const hasFilters = $derived(
    Boolean(
      searchInput ||
        search ||
        sort !== 'artist' ||
        descending ||
        status ||
        scope !== 'library' ||
        artistId ||
        offset
    )
  );

  function year(release: Release) {
    return release.firstReleaseDate ? release.firstReleaseDate.slice(0, 4) : '—';
  }

  // MusicBrainz writes the release type in lower case — `album`, `ep`, `single`,
  // `compilation`. Capitalising the first letter is right for every one of them
  // except `ep`, which is an abbreviation and reads as a misspelling when it is
  // drawn as `Ep`. The named ones are spelled the way the reader expects; the
  // rest take the first letter.
  const typeNames: Record<string, string> = { ep: 'EP' };

  function formatType(value: string) {
    if (!value) return 'Album';
    return typeNames[value.toLowerCase()] ?? value[0].toUpperCase() + value.slice(1);
  }

  function shortcut(event: KeyboardEvent) {
    const target = event.target as HTMLElement | null;
    if (event.key !== '/' || target?.tagName === 'INPUT' || target?.tagName === 'SELECT') return;
    event.preventDefault();
    searchBox?.focus();
  }

  // The A–Z rail: twenty-seven buttons beside the list that jump to the first
  // row starting with that letter.
  //
  // It is drawn under three conditions, and each of them is the difference
  // between a control that helps and one that lies.
  //
  // The list has to be in alphabetical order. The rail reads A at the top and Z
  // at the bottom, which is a claim about the order of the rows beside it; over
  // a list sorted by year it points at nothing. So it stands only while the
  // catalogue is sorted by artist or by title, ascending.
  //
  // The letters can only reach rows this page holds. The catalogue is paged on
  // the server — 48 releases at a time — and the browser cannot scroll to a row
  // it never asked for. Reaching the whole catalogue by letter would mean the
  // server answering "which page does M start on", which is a new question to
  // ask it and not one this change asks.
  //
  // And there have to be enough rows to make aiming worth it. Under about forty
  // rows the whole page is a screenful and a half; scrolling finds a row faster
  // than choosing one of twenty-seven small targets, and a rail that appears on
  // a list of nine is chrome. Forty is below a full page of 48 and above a part
  // page, so a full page has the rail and the last page of a search usually
  // does not.
  const jumpThreshold = 40;

  function initial(release: Release) {
    const name = sort === 'artist' ? release.artistName : release.title;
    const first = (name ?? '').trim().charAt(0).toUpperCase();
    // Anything that is not a letter — a number, a bracket, a non-Latin script —
    // gathers under one key rather than being dropped, so every row is
    // reachable from the rail.
    return first >= 'A' && first <= 'Z' ? first : '#';
  }

  const alphabet = ['#', ...Array.from({ length: 26 }, (_, step) => String.fromCharCode(65 + step))];

  const jumpLetters = $derived.by(() => {
    const ordered = sort === 'artist' || sort === 'title';
    if (!ordered || descending || shown.length < jumpThreshold) return [];
    // The first row for each letter, in the order the rows already stand in.
    const first = new Map<string, string>();
    for (const release of shown) {
      const key = initial(release);
      if (!first.has(key)) first.set(key, release.id);
    }
    // Every letter is drawn, so the rail keeps the same shape from one page to
    // the next; the ones this page has no row for are simply not pressable. A
    // rail that loses buttons as the list narrows is a rail whose letters move
    // under the reader's finger.
    return alphabet.map((key) => ({ key, id: first.get(key) ?? '' }));
  });

  function jumpTo(id: string) {
    if (!id) return;
    document.getElementById(`release-${id}`)?.scrollIntoView({ block: 'start' });
  }
</script>

<svelte:window onkeydown={shortcut} />

<!-- The page's own top row is the view chips alone, sticky, above this one.
     Everything a reader can turn about the catalogue itself — search, the
     artist filter, the status chips, scope, sort, Select and Find missing —
     is one band here, and it wraps onto a second line by itself when the
     window is too narrow to hold them all. -->
<ControlRail label="Which releases to show" sticky={false}>
  <form class="w-full sm:w-48" onsubmit={applySearch}>
    <label class="field flex w-full items-center gap-2">
      <Search size={13} strokeWidth={2} class="shrink-0 text-ink-4" />
      <input
        bind:this={searchBox}
        bind:value={searchInput}
        placeholder="Search releases"
        aria-label="Search releases"
        class="min-w-0 flex-1 bg-transparent font-sans text-meta text-ink outline-none placeholder:text-ink-4"
      />
      <span class="numeric rounded-row bg-surface-thick px-1.5 py-0.5 text-micro font-medium text-ink-3">
        /
      </span>
    </label>
  </form>

  <!-- Who the list is narrowed to, said first because it is the strongest
       narrowing on the page and because nobody standing here chose it: the
       filter comes in on a link, so it has to name the artist an id cannot and
       offer the way back out to everybody. -->
  {#if artistId}
    <div class="flex items-center gap-1.5 rounded-row bg-surface-thick py-1 pr-1 pl-2">
      <UserRound size={11} strokeWidth={2.2} class="shrink-0 text-ink-4" />
      <span class="max-w-44 truncate text-meta font-medium text-ink">{artistName}</span>
      <button
        type="button"
        class="grid size-4 shrink-0 place-items-center rounded-row text-ink-3 transition hover:bg-surface-thick hover:text-ink"
        aria-label="Show every artist"
        onclick={clearArtist}
      >
        <X size={11} strokeWidth={2.4} />
      </button>
    </div>
  {/if}

  <Segmented
    options={tabs}
    value={status}
    onchange={(next) => setStatus(next as '' | ReleaseStatus)}
    label="Which releases to show"
    pending={$releases.isPending}
    variant="filter"
  />

  <PillSelect
    options={scopeChoices}
    value={scope}
    onchange={(next) => setScope(next as 'library' | 'followed' | '')}
    label="How far the list reaches"
  />

  <PillSelect
    options={sortChoices}
    value={sortValue}
    onchange={applySort}
    label="Sort by"
    quiet
    prefix="Sort"
  />

  <Button
    variant="outline"
    size="sm"
    class={selecting ? 'border-line-thick bg-surface-regular' : ''}
    aria-pressed={selecting}
    onclick={() => setSelecting(!selecting)}
  >
    Select
  </Button>

  <!-- The count that stood here said the same number as the filter strip a few
       inches to its left. Every one of those chips carries its own count, and
       the chosen one is the count of what is on screen — "6 in view" beside a
       chip reading "All 6", and with a filter chosen, the filter's own figure
       and then the All chip's figure again. -->
  <div class="flex items-center gap-3">
    <!-- What the Missing page was an entry to, done in place: narrow to the
         releases with gaps, and start picking which of them one source search
         should cover. -->
    <Button
      class="shrink-0"
      onclick={() => {
        setStatus('missing');
        setSelecting(true);
      }}
    >
      <Search size={13} strokeWidth={2.3} /> Find missing
    </Button>
  </div>

  {#if selected.length}
    <div class="flex flex-wrap items-center gap-x-3 gap-y-2">
      <span class="numeric text-meta font-medium text-ink-2">{selected.length} selected</span>
      <button
        class="text-meta font-medium text-ink-3 transition hover:text-ink"
        onclick={() => (selected = [])}
      >
        Clear
      </button>
      <label
        class="flex cursor-pointer items-center gap-2 py-1 text-meta font-medium text-ink-2"
      >
        <input
          type="checkbox"
          bind:checked={autoRequest}
          class="check cursor-pointer"
        />
        Request confirmed matches
      </label>
      <!-- The three decisions a selection can be given, beside the one it
           already had. None of them is destructive: ignoring records the same
           track-scoped decision the release page records and is taken back one
           track at a time, and the other two only ask for work that already
           runs on its own. -->
      <Button variant="outline" size="sm" disabled={deciding} onclick={() => $rematching.mutate()}>
        {#if $rematching.isPending}
          <LoaderCircle size={13} class="animate-spin" />
        {:else}
          <RefreshCw size={13} strokeWidth={2.1} />
        {/if}
        Match again
      </Button>
      <Button variant="outline" size="sm" disabled={deciding} onclick={() => $retrying.mutate()}>
        {#if $retrying.isPending}
          <LoaderCircle size={13} class="animate-spin" />
        {:else}
          <RotateCcw size={13} strokeWidth={2.1} />
        {/if}
        Try again
      </Button>
      <Button variant="outline" size="sm" disabled={deciding} onclick={() => $ignoring.mutate()}>
        {#if $ignoring.isPending}
          <LoaderCircle size={13} class="animate-spin" />
        {:else}
          <EyeOff size={13} strokeWidth={2.1} />
        {/if}
        Ignore
      </Button>
      <Button size="sm" disabled={deciding} onclick={() => $sourceRun.mutate()}>
        {#if $sourceRun.isPending}
          <LoaderCircle size={13} class="animate-spin" />
        {:else}
          <Search size={13} strokeWidth={2.3} />
        {/if}
        Find sources for {selected.length}
      </Button>
    </div>
  {/if}

  <!-- Reported where it was caused rather than at the top of the page: the
       button that failed is in this bar, and the selection it would have run is
       still standing. -->
  <!-- The band stays and the note goes in it bare, so the reader keeps the one
       fact the band was drawn for: the press had no effect at all. -->
  {#if $sourceRun.isError}
    <StatusBadge class="basis-full items-start">
      <ErrorNote error={$sourceRun.error} bare class="min-w-0 flex-1" />
      <span class="shrink-0 text-meta text-ink-3">nothing was queued</span>
    </StatusBadge>
  {/if}

  {#if hasFilters}
    <Button variant="ghost" size="sm" onclick={clearFilters}>Clear filters</Button>
  {/if}

  {#if refused}
    <StatusBadge class="basis-full items-start">
      <ErrorNote error={refused} bare class="min-w-0 flex-1" />
      <span class="shrink-0 text-meta text-ink-3">nothing was decided</span>
    </StatusBadge>
  {/if}

  <!-- What the press came to, in the bar it was pressed in. It stays until the
       next selection, because the reader pressed a button about forty rows and
       the answer is the only place they learn how much of it was already
       decided. -->
  {#if outcome && !selected.length}
    <StatusBadge class="basis-full">
      <span class="text-meta leading-relaxed text-ink">{outcome}</span>
      <button
        class="ml-auto shrink-0 text-meta font-medium text-ink-3 transition hover:text-ink"
        onclick={() => (outcome = '')}
      >
        Dismiss
      </button>
    </StatusBadge>
  {/if}
</ControlRail>

{#if $releases.isError}
  <div class="px-6 py-6">
    <!-- Only a read, so the note asks again by itself. -->
    <ErrorNote error={$releases.error} retry={() => $releases.refetch()} />
  </div>
{:else}
  <Settle pending={$releases.isPending}>
    {#snippet placeholder()}
      <div
    class="layout-width px-3 py-1"
    role="status"
    aria-label="Loading releases"
  >
    <div class="max-h-[calc(100dvh-10rem)] overflow-hidden rounded-panel border border-line-thin">
      <div class="h-9 border-b border-line-thin" aria-hidden="true"></div>
      <!-- Fill about 2016px of rows so a 4K viewport does not outgrow the wait. -->
      {#each Array(60) as _, placeholderIndex (placeholderIndex)}
        <div
          class="h-[3.46875rem] border-b border-line-thin px-3 py-2 last:border-b-0"
          aria-hidden="true"
        >
          <div class="h-full animate-pulse rounded-row bg-surface-regular"></div>
        </div>
      {/each}
    </div>
      </div>
    {/snippet}
  {#if shown.length}
  <!-- The catalogue, and the letters that jump into it.

       A hairline between rows rather than a border around each: the rows are one
       list, not a stack of cards.

       The arriving step is drawn on the list and never on a row. A list is one
       object here in its motion as well as in its rules, and it says so once:
       five hundred rows arriving one behind another is nausea, and five hundred
       rows arriving on every background refresh is worse. This runs when the
       skeleton is replaced by rows and when a search that found nothing starts
       finding something, and at no other time — a refetch that already has rows
       to show never rebuilds this. -->
  <div class="layout-width flex items-start gap-4 px-3 py-1">
    <!-- The scroll happens in the table's own box rather than on the page. A
         sticky header sticks to the nearest thing that scrolls, and if that
         thing is the whole document the column names slide away with the rows.
         `scroll-pt-9` is the header's own height, so a row jumped to by a
         letter lands under the names rather than behind them. -->
    <Table.Root
      wrapperClass="layout-width rise max-h-[70dvh] min-w-0 flex-1 scroll-pt-9 scroll-smooth rounded-panel border border-line-thin"
    >
      <Table.Header>
        <!-- The row of column names, which are also the controls that sort by
             them. On a touch screen each name is given the height a fingertip
             needs and the row grows to hold it, rather than each name wearing
             an invisible 44px box: those boxes reached up out of this row and
             took presses aimed at the button above it. A control that is only
             sometimes the one you pressed is worse than a small one.

             The height is asked for on the buttons and not on the row. This is
             a real table row now, and `min-height` does nothing to one: the
             browser sizes a row from the cells inside it. `.sort-row`, which
             set that height when this was a flex box, is not used here. -->
        <Table.Row class="hover:bg-transparent">
          {#if selecting}
            <Table.Head class="w-9">
              <button
                type="button"
                class="flex h-full items-center pointer-coarse:min-h-11"
                onclick={toggleAll}
                aria-pressed={allSelected}
                aria-label={allSelected ? 'Clear the selection' : 'Select every release on this page'}
              >
                {@render Box(allSelected)}
              </button>
            </Table.Head>
          {/if}
          <!-- The artist stacks under the release title in the one row density
               the board draws, so it has no column of its own to be sorted
               from. The order is still the reader's to choose, so both
               controls stand in the one header the two names share. -->
          <Table.Head>
            <span class="flex h-full items-center gap-2">
              {@render Head('Release', 'title')}
              <span class="h-3 w-px bg-line-thin"></span>
              {@render Head('Artist', 'artist')}
            </span>
          </Table.Head>
          <Table.Head numeric>{@render Head('Year', 'year')}</Table.Head>
          <Table.Head>Type</Table.Head>
          <Table.Head>{@render Head('Owned', 'owned')}</Table.Head>
          <Table.Head>Status</Table.Head>
        </Table.Row>
      </Table.Header>

      <Table.Body>
        {#each shown as release (release.id)}
          {@const current = statusOf(release)}
          {@const picked = selected.includes(release.id)}
          <!-- Pointing at a row lifts it onto a raised surface and takes its
               hairline down, and the row below a hairline is the one that draws
               it, so the rule above a pointed-at row is taken down by its
               neighbour. The border turns transparent rather than being
               removed: a border that stops existing is a pixel of height the
               row loses, and the list under the pointer would step. -->
          <Table.Row
            id={`release-${release.id}`}
            selected={picked}
            class="group hover:border-transparent [&:has(+tr:hover)]:border-transparent"
          >
            {#if selecting}
              <Table.Cell class="w-9">
                <button
                  type="button"
                  class="tap flex items-center disabled:opacity-40"
                  disabled={!picked && atCap}
                  onclick={() => toggleSelected(release.id)}
                  aria-pressed={picked}
                  aria-label={`Select ${release.title}`}
                >
                  {@render Box(picked)}
                </button>
              </Table.Cell>
            {/if}

            <!-- The release title wraps. It is the thing being identified, and
                 a name cut off in the middle is a name the reader has to hover
                 to read. Everything beside it is metadata and takes the
                 ellipsis instead. -->
            <Table.Cell class="min-w-56">
              <a href={`/releases/${release.id}`} class="tap-tall flex min-w-0 items-center gap-2">
                <!-- Cached covers only: a page of a hundred rows asks this
                     installation and nobody else. A release with no picture
                     yet simply has none, and the slot stays the size and
                     border it would have held a picture in — a title beside a
                     missing cover does not creep left to fill the gap. -->
                <span
                  role="img"
                  aria-label={`Cover for ${release.title}`}
                  class="grid size-7 shrink-0 place-items-center overflow-hidden rounded-row border border-line-thin"
                >
                  <Cover
                    src={`/api/v1/albums/${release.id}/cover?cached=1`}
                    class="size-full object-cover"
                  />
                </span>
                <span class="min-w-0">
                  <span class="block text-body font-medium text-ink">{release.title}</span>
                  <!-- Ink 4 clears the contrast floor up to the regular
                       surface and no further, and a pointed-at or selected row
                       is drawn on a thicker one — so the quiet line promotes. -->
                  <span
                    class="mt-0.5 block max-w-64 truncate text-meta text-ink-4 transition group-hover:text-ink-3 group-data-selected:text-ink-3"
                  >
                    {release.artistName}
                  </span>
                </span>
              </a>
            </Table.Cell>

            <Table.Cell numeric class="text-meta font-medium text-ink-2">
              {year(release)}
            </Table.Cell>
            <Table.Cell class="text-meta font-medium text-ink-3">
              {formatType(release.albumType)}
            </Table.Cell>
            <Table.Cell>
              {#if (release.artistMonitorLevel === 'owned' ? release.ownedTrackCount : release.trackCount - release.dismissedTrackCount) > 0}
                {@const countable =
                  release.artistMonitorLevel === 'owned'
                    ? release.ownedTrackCount
                    : release.trackCount - release.dismissedTrackCount}
                <OwnedBar owned={release.ownedTrackCount} total={countable} width={48} noun="tracks" />
              {:else}
                <span
                  class="numeric text-meta text-ink-4 transition group-hover:text-ink-3 group-data-selected:text-ink-3"
                >
                  —
                </span>
              {/if}
            </Table.Cell>
            <Table.Cell>
              <!-- A release the library holds in full needs no word, only the
                   tick every board draws for "owned". An ordinary gap, whole
                   or partial, needs none either: the Owned column already
                   draws it. A hairline tag is left for what that column
                   cannot say — a data problem or a failed refresh — or for a
                   dismissal, which leaves no gap for that column to draw. A
                   release an "owned" artist tracks none of was never open to
                   begin with, so it is drawn as a gap rather than as a
                   word. -->
              {#if current.kind === 'tick'}
                <StateMark role="ok"><Check size={10} strokeWidth={3.2} /></StateMark>
              {:else if current.kind === 'dash'}
                <span
                  class="numeric text-meta text-ink-4 transition group-hover:text-ink-3 group-data-selected:text-ink-3"
                  aria-label="none held"
                >
                  —
                </span>
              {:else if current.kind === 'tag'}
                <StateTag
                  tone={current.tone}
                >{current.label}</StateTag>
              {/if}
            </Table.Cell>
          </Table.Row>
        {/each}
      </Table.Body>
    </Table.Root>

    <!-- The letters, beside the list rather than over it.
         Only drawn when they would tell the truth: the rail reads A at the top
         and Z at the bottom, so it stands only while the list is in that order,
         and it jumps within the rows that are here — the catalogue is paged on
         the server and a letter cannot reach a row this page never asked for. -->
    {#if jumpLetters.length > 0}
      <div
        class="sticky top-2 hidden w-7 shrink-0 flex-col items-center gap-px md:flex"
        role="group"
        aria-label="Jump to a letter"
      >
        {#each jumpLetters as letter (letter.key)}
          <button
            type="button"
            class="numeric grid h-5 w-7 place-items-center rounded-row text-meta font-semibold transition {letter.id
              ? 'text-ink-2 hover:bg-surface-thick hover:text-ink'
              : 'text-ink-3'}"
            disabled={!letter.id}
            aria-label={`Jump to ${letter.key}`}
            onclick={() => jumpTo(letter.id)}
          >
            {letter.key}
          </button>
        {/each}
      </div>
    {/if}
  </div>

  <div class="px-3">
    <Pager {total} {offset} {pageSize} onchange={(next) => changePage(next)} />
  </div>
  {:else}
  <div class="px-6 py-6">
    <EmptyPanel
      role="idle"
      heading={scopeTotal ? 'Nothing in this filter' : 'No releases match'}
    >
      <p class="text-body leading-relaxed text-ink-2">
        {#if scopeTotal}
          {scopeTotal.toLocaleString()} releases are in view but none are
          <span class="text-ink">{filterName.toLocaleLowerCase()}</span>. Try another filter.
        {:else if search}
          Nothing here matches that search. Try a shorter one, or widen the view.
        {:else if scope}
          You do not follow or own anything in this scope. Show everything in the catalogue to see the
          rest.
        {:else if artistId}
          The catalogue holds no releases by <span class="text-ink">{artistName}</span>. Refresh them
          from their page to fetch their discography.
        {:else}
          Follow an artist to build the catalogue.
        {/if}
      </p>
      {#if noReleasesMatch && filesMatching > 0}
        <p class="text-body leading-relaxed text-ink-2">
          {plural(filesMatching, 'file matches', 'files match')} this search.
          <a
            href="/library?view=files&q={encodeURIComponent(search)}"
            onclick={() => onviewfiles?.()}
            class="text-ink underline underline-offset-2"
          >
            Open Files
          </a>
        </p>
      {/if}
    </EmptyPanel>
  </div>
  {/if}
  </Settle>
{/if}

{#snippet Box(checked: boolean)}
  <span
    class="grid size-6 place-items-center"
  >
    <span
      class="grid size-[15px] place-items-center rounded-tight transition {checked
        ? 'bg-accent'
        : 'border border-line-thick hover:border-line-live'}"
    >
      {#if checked}<Check size={10} strokeWidth={3.6} class="text-accent-ink" />{/if}
    </span>
  </span>
{/snippet}

{#snippet Head(label: string, key: Sort)}
  <button
    class="label flex h-full items-center gap-1.5 transition hover:text-ink pointer-coarse:min-h-11"
    onclick={() => pick(key)}
  >
    {label}
    {#if sort === key}
      {#if descending}
        <ArrowDown size={11} strokeWidth={2.2} class="text-ink" />
      {:else}
        <ArrowUp size={11} strokeWidth={2.2} class="text-ink" />
      {/if}
    {/if}
  </button>
{/snippet}

<script lang="ts">
  import Icon from './Icon.svelte';
  import { tick } from 'svelte';
  import { toStore } from 'svelte/store';
  import { createMutation, createQuery, useQueryClient } from '@tanstack/svelte-query';
  import { page } from '$app/state';
  import { replaceState } from '$app/navigation';
  import {
    api,
    type FileResolution,
    type LibraryFile,
    type LibraryFileStatus,
    type ResolutionStatus
  } from '$lib/api';
  import { isAuthError } from '$lib/errors';
  import {
    calendarDate,
    elapsedInWords,
    fileFormat,
    fileStanding,
    formatBytes,
    formatDuration,
    matchMethodName,
    urlText
  } from '$lib/utils';
  import FileIdentity from '$lib/components/FileIdentity.svelte';
  import Skeleton from '$lib/components/Skeleton.svelte';
  import ReviewMatch from '$lib/components/ReviewMatch.svelte';
  import { listening } from '$lib/preview.svelte';
  import Button from '$lib/components/Button.svelte';
  import EmptyPanel from '$lib/components/EmptyPanel.svelte';
  import ErrorNote from '$lib/components/ErrorNote.svelte';
  import Keycap from '$lib/components/Keycap.svelte';
  import Pager from '$lib/components/Pager.svelte';
  import PillSelect from '$lib/components/PillSelect.svelte';
  import Segmented from '$lib/components/Segmented.svelte';
  import Settle from '$lib/components/Settle.svelte';
  import SkeletonRows from '$lib/components/SkeletonRows.svelte';
  import SoundCloudTrackModal from '$lib/components/SoundCloudTrackModal.svelte';
  import StateIcon from '$lib/components/StateIcon.svelte';
  import StatusBadge from '$lib/components/StatusBadge.svelte';
  import { useKeys } from '$lib/keys.svelte';
  import { glide } from '$lib/motion.svelte';
  import type { IconName } from '$lib/icons';

  // Where "Held twice" is. The library's three views are one page that hides two
  // of itself rather than three routes, and the view is read out of the address
  // bar once when the page mounts — so a link to it would change the address and
  // leave the reader looking at the same screen. The page owns which view is
  // showing, so the page is asked.
  //
  // All three views stay mounted, so `active` says whether this one is showing.
  // Only the one showing answers keys.
  let { onheldtwice, active = true }: { onheldtwice?: () => void; active?: boolean } = $props();

  const queryClient = useQueryClient();
  let path = $state('/music');
  // Read once, before anything is tracked. The catalogue view is what writes
  // this page's address bar; this only ever reads it, so that a link handing the
  // files view a search — the palette's escape into the library, or a link to
  // one file by its path — opens on that search rather than on everything.
  let fileSearchInput = $state(urlText(page.url, 'q'));
  let fileSearch = $state(urlText(page.url, 'q'));
  let unidentifiedOnly = $state(page.url.searchParams.get('unidentified') === 'true');
  // The file a link opened. It narrows the list to that one row until any
  // filter changes, so the reader can get back to the whole list.
  const openedFileID = page.url.searchParams.get('id') ?? '';
  let deepLinkFileID = $state(openedFileID);
  let expandedFileID = $state(openedFileID);
  let setAsideOnly = $state(page.url.searchParams.get('setAside') === 'true');
  let status = $state<LibraryFileStatus | ''>('');
  let resolution = $state<ResolutionStatus | ''>('');
  let identityFileID = $state<string | null>(null);
  let fileResolution = $state<FileResolution | null>(null);
  // What the identity read was refused with, kept as the thrown error rather
  // than as its text: `ErrorNote` is what turns a failure into words, and it
  // reads the whole thing.
  let identityError = $state<unknown>(null);
  let loadingIdentity = $state(false);
  let chosenIdentity = $state(-1);
  let offset = $state(0);
  let showFolders = $state(false);
  const pageSize = 25;

  const summary = createQuery({
    queryKey: ['library'],
    queryFn: api.library,
    // The scanner announces its progress over the event stream, so this is the
    // safety net rather than the mechanism: it covers a stream blocked by a
    // proxy or dropped without reconnecting yet.
    refetchInterval: (query) =>
      isAuthError(query.state.error)
        ? false
        : ['queued', 'running'].includes(query.state.data?.scanStatus ?? '')
          ? 15_000
          : false
  });
  const roots = createQuery({ queryKey: ['library-roots'], queryFn: api.libraryRoots });
  // How many recordings the library answers with more than one file. It is read
  // here only to say so when somebody presses "Already held" and finds nothing:
  // that filter is about a decision, this is about the music, and the reader who
  // pressed it meant the second one. The key is the Held twice tab's own, so
  // both read one answer rather than asking twice.
  const heldTwice = createQuery({
    queryKey: ['library-duplicates', 'to-decide'],
    queryFn: () => api.duplicateRecordings(false)
  });
  // The list is narrowed on the server, so every combination of the two filters,
  // the search and the page is a different answer and each is named in the key.
  // One key for all of them meant the press was followed by a refetch, and a
  // refetch while the first request is still in flight joins that request rather
  // than starting a new one: the filter moved and the rows under it did not.
  // The key carries the submitted search rather than what is in the box, so
  // typing asks the server nothing until Search is pressed.
  const files = createQuery(
    toStore(() => ({
      queryKey: ['library-files', status, resolution, fileSearch, offset, setAsideOnly, unidentifiedOnly, deepLinkFileID],
      queryFn: () =>
        api.libraryFiles({
          status: status || undefined,
          resolution: resolution || undefined,
          query: fileSearch || undefined,
          limit: pageSize,
          offset,
          id: deepLinkFileID || undefined,
          setAside: setAsideOnly ? true : undefined,
          unidentified: unidentifiedOnly || undefined,
        }),
      refetchInterval: () =>
        isAuthError($summary.error)
          ? false
          : ['queued', 'running'].includes($summary.data?.scanStatus ?? '')
            ? 15_000
            : false
    }))
  );

  $effect(() => {
    const target = $files.data?.items.find((file) => file.id === expandedFileID);
    if (target && (target.resolutionStatus === 'needs_review' || target.resolutionStatus === 'conflict') && identityFileID !== target.id && !loadingIdentity) {
      void showIdentity(target.id);
    }
  });


  const addRoot = createMutation({
    mutationFn: (value: string) => api.addLibraryRoot(value),
    onSuccess: async () => {
      await Promise.all([
        queryClient.invalidateQueries({ queryKey: ['library'] }),
        queryClient.invalidateQueries({ queryKey: ['library-roots'] })
      ]);
    }
  });

  const setAside = createMutation({
    mutationFn: ({ fileId, clear }: { fileId: string; clear: boolean }) =>
      clear ? api.clearLibraryFileSetAside(fileId) : api.setAsideLibraryFile(fileId),
    onSuccess: async () => {
      await Promise.all([
        queryClient.invalidateQueries({ queryKey: ['library'] }),
        queryClient.invalidateQueries({ queryKey: ['library-files'] }),
        queryClient.invalidateQueries({ queryKey: ['review-identities'] }),
        queryClient.invalidateQueries({ queryKey: ['review-matches'] }),
        queryClient.invalidateQueries({ queryKey: ['review-duplicates'] }),
        queryClient.invalidateQueries({ queryKey: ['review-set-aside'] })
      ]);
    }
  });

  const scan = createMutation({
    mutationFn: api.scanLibrary,
    onSuccess: async () => {
      await queryClient.invalidateQueries({ queryKey: ['library'] });
    }
  });

  // The file being named by its SoundCloud address, and its path, which the
  // dialog shows so the reader can see which file they are deciding about.
  let soundCloudFile = $state<LibraryFile | null>(null);
  let soundCloudOpen = $state(false);

  function nameFromSoundCloud(file: LibraryFile) {
    soundCloudFile = file;
    soundCloudOpen = true;
  }

  let identityRead = 0;

  async function showIdentity(fileId: string) {
    if (identityFileID === fileId) {
      identityFileID = null;
      return;
    }
    identityFileID = fileId;
    chosenIdentity = -1;
    expandedFileID = fileId;
    fileResolution = null;
    identityError = null;
    loadingIdentity = true;
    // Only the latest read may answer. An older one, shown under the next file,
    // offers its candidates to be accepted as that file's identity.
    const read = ++identityRead;
    try {
      const resolution = await api.fileIdentity(fileId);
      if (read === identityRead) fileResolution = resolution;
    } catch (error) {
      if (read === identityRead) identityError = error;
    } finally {
      if (read === identityRead) loadingIdentity = false;
    }
  }

  const identityDecision = createMutation({
    mutationFn: ({ fileId, recordingId }: { fileId: string; recordingId: string }) =>
      api.acceptIdentity(fileId, recordingId),
    onSuccess: (result) => {
      // The reader may have opened another file while this was sent.
      if (result.fileId === identityFileID) {
        fileResolution = result;
        chosenIdentity = -1;
      }
      void queryClient.invalidateQueries({ queryKey: ['library-files'] });
      void queryClient.invalidateQueries({ queryKey: ['library'] });
    }
  });
  const localOnly = createMutation({
    mutationFn: (fileId: string) => api.markLocalOnly(fileId),
    onSuccess: (result) => {
      // The reader may have opened another file while this was sent.
      if (result.fileId === identityFileID) {
        fileResolution = result;
        chosenIdentity = -1;
      }
      void queryClient.invalidateQueries({ queryKey: ['library-files'] });
      void queryClient.invalidateQueries({ queryKey: ['library'] });
    }
  });
  const identityPending = $derived($identityDecision.isPending || $localOnly.isPending);

  function applyFileSearch(event: SubmitEvent) {
    event.preventDefault();
    offset = 0;
    leaveDeepLink();
    fileSearch = fileSearchInput.trim();
  }

  function setStatus(value: LibraryFileStatus | '') {
    leaveDeepLink();
    status = value;
    offset = 0;
  }

  function toggleExpansion(file: LibraryFile) {
    if (expandedFileID === file.id) {
      expandedFileID = '';
      identityFileID = null;
      return;
    }
    expandedFileID = file.id;
    if (file.resolutionStatus === 'needs_review' || file.resolutionStatus === 'conflict') {
      void showIdentity(file.id);
    }
  }

  // What an empty list means, in the words of the question that came back empty.
  //
  // Every filter here is its own question, so "No audio files found" was an
  // answer to none of them: the library has fifteen hundred files. Each one now
  // names what it looked for, and the two that a reader most often reaches by
  // mistake say where the thing they meant actually lives.
  const empty = $derived.by((): {
    heading: string;
    detail: string;
    elsewhere?: { label: string };
  } => {
    if (fileSearch) {
      return {
        heading: 'No matches',
        detail: `No file path or tag contains ${fileSearch}.`
      };
    }
    if (setAsideOnly) {
      return { heading: 'No set-aside files', detail: '' };
    }
    if (resolution && !status) {
      return { heading: 'No files in that state', detail: '' };
    }
    switch (status) {
      case 'duplicate': {
        const held = $heldTwice.data?.total ?? 0;
        const base =
          'Files whose every possible track you already matched by hand.';
        return {
          heading: 'No files already held',
          detail:
            held > 0
              ? `${base} ${held} ${held === 1 ? 'song is' : 'songs are'} held twice.`
              : base,
          elsewhere: held > 0 && onheldtwice ? { label: 'Open Held twice' } : undefined
        };
      }
      case 'missing':
        return {
          heading: 'No missing files',
          detail: ''
        };
      case 'unreadable':
        return {
          heading: 'No unreadable files',
          detail: ''
        };
      case 'matched':
        return { heading: 'No matched files', detail: '' };
      case 'unmatched':
        return { heading: 'No unmatched files', detail: '' };
      case 'ambiguous':
        return { heading: 'No ambiguous files', detail: '' };
      default:
        return {
          heading: 'No audio files found',
          detail: 'Add a music folder below, then scan it.'
        };
    }
  });

  // A card is pressed to see what it counted, so it clears everything else that
  // is narrowing the list. Leaving a search or an identity filter standing would
  // answer with a number that is not the one on the card.
  function leaveDeepLink() {
    if (!deepLinkFileID) return;
    deepLinkFileID = '';
    const params = new URLSearchParams(page.url.searchParams);
    params.delete('id');
    const query = params.toString();
    replaceState(query ? `${page.url.pathname}?${query}` : page.url.pathname, {});
  }

  function setResolution(value: ResolutionStatus | '') {
    leaveDeepLink();
    unidentifiedOnly = false;
    resolution = value;
    offset = 0;
  }

  function setUnidentified(value: boolean) {
    leaveDeepLink();
    unidentifiedOnly = value;
    status = '';
    resolution = '';
    offset = 0;
  }

  function clearFilters() {
    fileSearchInput = '';
    fileSearch = '';
    setAsideOnly = false;
    unidentifiedOnly = false;
    deepLinkFileID = '';
    status = '';
    resolution = '';
    offset = 0;
    const params = new URLSearchParams(page.url.searchParams);
    params.delete('q');
    params.delete('setAside');
    params.delete('unidentified');
    params.delete('id');
    const query = params.toString();
    replaceState(query ? `${page.url.pathname}?${query}` : page.url.pathname, {});
  }

  const hasFilters = $derived(
    Boolean(fileSearchInput || fileSearch || setAsideOnly || status || resolution || unidentifiedOnly || deepLinkFileID || offset)
  );

  // The identity states a file can be narrowed to, written as data because the
  // picker needs the same list twice: once to draw the choices, once to answer
  // a letter typed while the list is shut.
  const resolutionChoices: { value: ResolutionStatus | ''; name: string }[] = [
    { value: '', name: 'Any identity' },
    { value: 'needs_review', name: 'Needs review' },
    { value: 'conflict', name: 'Evidence disagrees' },
    { value: 'local_only', name: 'Local only' },
    { value: 'source', name: 'From SoundCloud' },
    { value: 'pending', name: 'Not checked' }
  ];

  // What the Standing column draws: a tick for a file the catalogue can name
  // with certainty, and a glyph everywhere else. The glyph's word is its
  // tooltip, with the full sentence `fileStanding` already writes after it.
  type Standing =
    | { kind: 'tick'; title: string }
    | { kind: 'state'; icon: IconName; role: 'idle' | 'decide' | 'busy' | 'fail'; label: string; title: string };

  function standingOf(file: LibraryFile): Standing {
    const { text, state } = fileStanding(file);
    // A file gone from disk or one the scanner could not open are their own
    // shapes, ahead of anything matching found: neither is a matching
    // question, and both this page's own status filters ask for directly.
    if (file.missingAt) return { kind: 'state', icon: 'minus', role: 'idle', label: 'missing', title: text };
    if (file.scanError) return { kind: 'state', icon: 'close', role: 'fail', label: 'unreadable', title: file.scanError };
    if (state === 'ok') return { kind: 'tick', title: text };
    if (file.matchStatus === 'duplicate') return { kind: 'state', icon: 'minus', role: 'idle', label: 'held twice', title: text };
    if (file.matchStatus === 'ambiguous') return { kind: 'state', icon: 'alert', role: 'decide', label: 'ambiguous', title: text };
    switch (file.resolutionStatus) {
      case 'resolved':
        return { kind: 'state', icon: 'minus', role: 'idle', label: 'unmatched', title: text };
      case 'needs_review':
        return { kind: 'state', icon: 'alert', role: 'decide', label: 'unidentified', title: text };
      case 'conflict':
        return { kind: 'state', icon: 'close', role: 'fail', label: 'conflict', title: text };
      case 'local_only':
        return { kind: 'state', icon: 'minus', role: 'idle', label: 'local only', title: text };
      case 'failed':
        return { kind: 'state', icon: 'close', role: 'fail', label: 'lookup failed', title: text };
      default:
        return { kind: 'state', icon: 'minus', role: 'idle', label: 'pending', title: text };
    }
  }

  function changePage(nextOffset: number) {
    offset = nextOffset;
  }

  function addFolder(event: SubmitEvent) {
    event.preventDefault();
    const value = path.trim();
    if (value) $addRoot.mutate(value);
  }

  function changeSetAside(file: LibraryFile) {
    $setAside.mutate({ fileId: file.id, clear: Boolean(file.setAside) });
  }

  // Hearing a row. The row-number slot holds the play control as well as the
  // number, and pointing at a row swaps which of the two is showing.
  //
  // One <audio> element for the whole table, rather than one per row: a page is
  // twenty-five rows and twenty-five media elements is twenty-five things the
  // browser holds open to play one file. Which file it is pointed at is the
  // whole of the state.
  //
  // `listening` is the agreement every player on a page keeps: whoever starts
  // sets the name, and every player that is not it falls silent. The duplicate
  // question stands two players side by side further down this screen, so this
  // one has to keep the same agreement or two files sound at once.
  let audio = $state<HTMLAudioElement | null>(null);
  let sounding = $state('');

  $effect(() => {
    if (sounding && listening.sounding !== api.libraryFileAudioUrl(sounding)) silence();
  });

  function silence() {
    audio?.pause();
    sounding = '';
  }

  function hear(fileId: string) {
    if (!audio) return;
    if (sounding === fileId) {
      silence();
      return;
    }
    const src = api.libraryFileAudioUrl(fileId);
    sounding = fileId;
    listening.sounding = src;
    audio.src = src;
    audio.volume = listening.volume;
    void audio.play().catch(() => (sounding = ''));
  }

  // The row that opens under a file — its identity, or the error the scanner
  // recorded — is one cell stretched across the whole table, and a cell has to
  // be told how far that is: #, title, album, format, length, size, standing.
  const columnCount = 7;

  const scanning = $derived(
    ['queued', 'running'].includes($summary.data?.scanStatus ?? '') || $scan.isPending
  );
  const scanned = $derived(($summary.data?.scanCompletedAt ?? null) !== null);

  // The dim line beside the chips: what the last scan did, and how long ago.
  // "1 folder" is also the way into the folders disclosure — the paths
  // themselves used to sit here as their own row of chips, which repeated
  // what that disclosure already lists in full.
  const scanLine = $derived.by(() => {
    if (scanning) return 'Scanning';
    if ($summary.data?.scanStatus === 'failed') return 'Scan failed';
    if (!scanned) return 'Never scanned';
    return `Scanned ${elapsedInWords($summary.data?.scanCompletedAt)}`;
  });
  // Read off the roots list itself rather than the summary's own count. The
  // list is what the disclosure below draws, and reading it here too — from
  // the moment the page opens, not only once the disclosure is asked for —
  // is what keeps that later read live: a query nothing outside a closed
  // `{#if}` ever asks about stays on its first, empty answer once that `{#if}`
  // finally opens.
  const rootCount = $derived($roots.data?.items.length ?? 0);

  // The second row's counts. Matched, unmatched, duplicate, missing and
  // unreadable are each a straight read off the summary; unidentified has no
  // field of its own, because the filter it counts is a file that still needs
  // either an identity or a matching decision (`AND (resolution_status IN
  // ('needs_review', 'conflict') OR match_status = 'ambiguous')` in
  // `ListLibraryFiles`) minus whatever of that is set aside, which the summary
  // does not subtract. The figure is close rather than exact for that one chip.
  const fileCounts = $derived.by(() => {
    const data = $summary.data;
    if (!data) return undefined;
    return {
      '': data.fileCount + data.missingCount,
      matched: data.matchedCount,
      unmatched: data.unmatchedCount,
      ambiguous: data.ambiguousCount,
      unidentified: data.ambiguousCount + data.needsReviewCount + data.conflictCount,
      duplicate: data.duplicateCount,
      unreadable: data.errorCount,
      missing: data.missingCount
    } as Record<LibraryFileStatus | 'unidentified' | '', number>;
  });

  // The cursor (ADR 0042): the row J and K move, Enter opens and Space plays.
  // It starts on the first row and goes back there when the list is asked a new
  // question.
  const rows = $derived($files.data?.items ?? []);
  let at = $state(0);
  let list = $state<HTMLElement>();
  let searchForm = $state<HTMLElement>();
  let searchBox = $state<HTMLInputElement | null>(null);

  $effect(() => {
    void [status, resolution, fileSearch, offset, setAsideOnly, unidentifiedOnly, deepLinkFileID];
    at = 0;
  });

  $effect(() => {
    if (at > 0 && at >= rows.length) at = Math.max(0, rows.length - 1);
  });

  function move(by: number) {
    if (rows.length === 0) return;
    at = Math.min(Math.max(at + by, 0), rows.length - 1);
    void tick().then(() =>
      list?.querySelector('[data-cursor="true"]')?.scrollIntoView?.({ block: 'nearest' })
    );
  }

  useKeys(() =>
    active
      ? [
          {
            keys: ['j', 'k', 'arrowdown', 'arrowup'],
            caps: ['j', 'k'],
            verb: 'move',
            order: 0,
            enabled: () => rows.length > 0,
            run: (event) => move(['k', 'arrowup'].includes(event.key.toLowerCase()) ? -1 : 1)
          },
          {
            keys: ['enter'],
            verb: 'open',
            order: 1,
            enabled: () => Boolean(rows[at]),
            run: () => {
              const file = rows[at];
              if (file) toggleExpansion(file);
            }
          },
          {
            keys: [' '],
            verb: 'play',
            order: 2,
            enabled: () => Boolean(rows[at]) && !rows[at].missingAt,
            run: () => {
              const file = rows[at];
              if (file && !file.missingAt) hear(file.id);
            }
          },
          {
            keys: ['f'],
            verb: 'search',
            element: () => searchForm,
            run: () => searchBox?.focus()
          }
        ]
      : []
  );
</script>

<div
  role="group"
  aria-label="Scan the library and search its files"
  class="flex flex-wrap items-center gap-x-3 gap-y-2 border-y border-rule px-5 py-2"
>
  <!-- "Duplicates" used to be here, and it was the wrong word twice over. This
       filter never meant "the library holds this twice": that question is the
       Held twice tab, and it is asked about recordings rather than about
       files. It means the narrow thing the matcher records: every track this
       file could be is already held by a decision somebody made by hand, so
       there is nothing left to ask about it. -->
  <Segmented
    options={[
      { value: '', name: 'All', count: fileCounts?.[''] },
      { value: 'matched', name: 'Matched', count: fileCounts?.matched },
      { value: 'unmatched', name: 'Unmatched', count: fileCounts?.unmatched },
      { value: 'ambiguous', name: 'Ambiguous', count: fileCounts?.ambiguous },
      { value: 'unidentified', name: 'Unidentified', count: fileCounts?.unidentified },
      { value: 'duplicate', name: 'Already held', count: fileCounts?.duplicate },
      { value: 'unreadable', name: 'Unreadable', count: fileCounts?.unreadable },
      { value: 'missing', name: 'Missing', count: fileCounts?.missing }
    ]}
    value={unidentifiedOnly ? 'unidentified' : status}
    onchange={(value) => value === 'unidentified' ? setUnidentified(true) : (setUnidentified(false), setStatus(value as LibraryFileStatus | ''))}
    label="Filter by state"
    pending={$summary.isPending}
  />

  <PillSelect
    options={resolutionChoices}
    value={resolution}
    onchange={(next) => setResolution(next as ResolutionStatus | '')}
    label="Filter by identity state"
  />

  {#if hasFilters}
    <Button variant="ghost" size="sm" onclick={clearFilters}>Clear filters</Button>
  {/if}

  <!-- What the last scan did, and how long ago. "N folder(s)" is also the way
       into the folders disclosure below. -->
  <span class="ml-auto text-meta text-ink-3">
    {scanLine}
    <span class="text-ink-4">·</span>
    <button
      type="button"
      class="text-ink-2 underline decoration-ink-4 underline-offset-2 transition hover:text-ink"
      aria-expanded={showFolders}
      onclick={() => (showFolders = !showFolders)}
    >
      {rootCount} {rootCount === 1 ? 'folder' : 'folders'}
    </button>
  </span>

  <form bind:this={searchForm} class="w-56" onsubmit={applyFileSearch}>
    <label class="field flex h-7 w-full items-center gap-2">
      <Icon name="search" size={13} class="shrink-0 text-ink-4" />
      <input
        bind:this={searchBox}
        bind:value={fileSearchInput}
        aria-label="Search discovered files"
        placeholder="Search files"
        class="min-w-0 flex-1 bg-transparent font-sans text-meta text-ink outline-none placeholder:text-ink-4"
      />
      {#if active}<Keycap key="f" />{/if}
    </label>
  </form>

  <Button
    size="sm"
    key={active ? 'r' : undefined}
    verb="scan"
    onclick={() => $scan.mutate()}
    disabled={scanning || rootCount === 0}
  >
    {#if scanning}
      <Icon name="busy" size={13} class="animate-spin" /> Scanning
    {:else}
      <Icon name="refresh" size={13} /> {scanned ? 'Rescan' : 'Scan'}
    {/if}
  </Button>
</div>

<div class="flex flex-col gap-3 px-5 pt-3">
  <!-- Reads of the library: the figures and the folders here, the files where
       the table stands. Each one is only a read, so where asking again is the
       whole answer the note asks by itself rather than handing the reader a
       button that repeats what just failed. -->
  {#if $summary.error}
    <ErrorNote error={$summary.error} retry={() => $summary.refetch()} />
  {/if}
  {#if $roots.error}
    <ErrorNote error={$roots.error} retry={() => $roots.refetch()} />
  {/if}
  {#if $summary.data?.scanStatus === 'failed'}
    {@render Problem($summary.data.scanError ?? 'Last scan failed.')}
  {/if}

  {#if showFolders}
    <section class="flex flex-col gap-2.5 pb-2">
      <div class="flex items-center gap-2.5">
        <span class="label">Music folders</span>
        <span class="h-px flex-1 bg-rule"></span>
      </div>

      <form class="flex flex-wrap gap-2" onsubmit={addFolder}>
        <input
          bind:value={path}
          aria-label="Music folder path"
          placeholder="/music"
          class="field min-w-0 flex-1"
        />
        <Button type="submit" disabled={$addRoot.isPending}>
          <Icon name="folder-add" size={13} /> Add
        </Button>
      </form>
      <span class="text-meta leading-relaxed text-ink-3">
        a path inside the container, under an allowed root
      </span>
      {#if $addRoot.isError}<ErrorNote error={$addRoot.error} />{/if}

      {#if $roots.isPending}
        <SkeletonRows count={2} label="Loading music folders" />
      {:else if $roots.data?.items.length}
        <div class="flex flex-col">
          {#each $roots.data.items as root (root.id)}
            <div class="flex items-center gap-3 border-b border-rule px-3 py-2 last:border-b-0">
              <span class="mono min-w-0 flex-1 truncate text-body text-ink" title={root.path}>
                {root.path}
              </span>
              <span class="text-meta text-ink-4">
                {root.lastScannedAt
                  ? `scanned ${calendarDate(root.lastScannedAt)}`
                  : 'not scanned yet'}
              </span>
            </div>
          {/each}
        </div>
      {:else if !$roots.isError}
        <!-- A failed read is reported by the note at the top of the page; saying
             there are no folders under it would be a wrong answer. -->
        <EmptyPanel role="idle" heading="No music folders">
          <p class="text-meta leading-relaxed text-ink-3">Add the first one above.</p>
        </EmptyPanel>
      {/if}
    </section>
  {/if}

  {#if setAsideOnly}
    <div class="flex flex-wrap items-center gap-2.5">
      <span class="text-meta font-medium text-ink">Set-aside files</span>
      <Button variant="ghost" size="xs" onclick={() => (setAsideOnly = false)}>
        Show all files
      </Button>
    </div>
  {/if}
</div>

<!-- One <audio> element for the whole table. A page is twenty-five rows, and
     twenty-five media elements is twenty-five things the browser holds open in
     order to play one file. Where this one points is the row that is sounding. -->
<audio bind:this={audio} class="hidden" preload="none" onended={() => (sounding = '')}></audio>

<Settle pending={$files.isPending}>
  {#snippet placeholder()}
    <!-- A list still on its way is not an empty list. Each filter is its own
         question with its own answer, so pressing one leaves nothing on screen
         until that answer lands, and "No audio files found" in that gap is a
         wrong answer rather than a wait. The wait is drawn as the table it is
         waiting for. -->
    <div class="px-5" role="status" aria-label="Loading files">
      <div class="max-h-[calc(100dvh-9rem)] overflow-hidden">
        <div class="h-7 border-b border-rule-strong"></div>
        {#each Array(25) as _, placeholderIndex (placeholderIndex)}
          <div class="h-9 border-b border-rule py-1.5" aria-hidden="true">
            <Skeleton class="h-full" />
          </div>
        {/each}
      </div>
    </div>
  {/snippet}
  {#if $files.error}
    <!-- Where the table would be, so the failure is not read as an empty space
         under the filters. -->
    <div class="px-5 pt-3">
      <ErrorNote error={$files.error} retry={() => $files.refetch()} />
    </div>
  {/if}
  {#if rows.length}
    <div class="px-5">
      <div class="relative" bind:this={list} use:glide={{ key: at }}>
        <table class="w-full border-separate border-spacing-0 text-left text-body text-ink [&_td]:border-b [&_td]:border-rule [&_th]:border-b [&_th]:border-rule-strong">
          <thead>
            <tr>
              <th class="label sticky top-10 z-[2] h-7 w-10 bg-ground px-2 text-center">#</th>
              <th class="label sticky top-10 z-[2] h-7 bg-ground px-2.5 text-left">Title</th>
              <th class="label sticky top-10 z-[2] h-7 bg-ground px-2.5 text-left">Album</th>
              <!-- What kind of audio file this is, read off the extension. The
                   scanner stores no codec, no bit depth and no sample rate, so
                   this says FLAC or MP3 and stops there. -->
              <th class="label sticky top-10 z-[2] h-7 w-20 bg-ground px-2.5 text-left">Format</th>
              <th class="label sticky top-10 z-[2] h-7 w-24 bg-ground px-2.5 text-right">Length</th>
              <th class="label sticky top-10 z-[2] h-7 w-24 bg-ground px-2.5 text-right">Size</th>
              <th class="label sticky top-10 z-[2] h-7 w-56 bg-ground px-2.5 text-left">Standing</th>
            </tr>
          </thead>
          <tbody>
            {#each rows as file, index (file.id)}
              {@const standing = standingOf(file)}
              {@const open = expandedFileID === file.id}
              {@const here = index === at}
              <!-- The row number and the play control share one box and take
                   turns being the visible one, under the cursor or a pointer.
                   Nothing changes width or height while that happens. -->
              <tr
                data-cursor={here}
                data-selected={open ? '' : undefined}
                aria-current={here ? 'true' : undefined}
                class="row group scroll-mt-12 scroll-mb-10 {here ? 'on-ink' : open ? 'bg-panel' : ''} {file.missingAt
                  ? 'opacity-45'
                  : ''}"
                onclick={() => (at = index)}
              >
                <td class="h-9 w-10 px-2">
                  <span class="grid size-7 place-items-center">
                    <span
                      class="mono col-start-1 row-start-1 text-micro text-ink-4 transition {file.missingAt
                        ? ''
                        : 'md:group-hover:opacity-0 md:group-focus-within:opacity-0'} {sounding === file.id || (here && !file.missingAt)
                        ? 'md:opacity-0'
                        : ''}"
                    >
                      {offset + index + 1}
                    </span>
                    <!-- A file the last scan did not find on disk has nothing to
                         play, so it keeps its number and is offered no control.
                         Hidden behind a pointer, a keyboard or the cursor at `md`
                         and above, where the number is the useful default. -->
                    {#if !file.missingAt}
                      <button
                        type="button"
                        class="tap col-start-1 row-start-1 grid size-7 place-items-center text-ink-2 transition md:opacity-0 md:group-focus-within:opacity-100 md:group-hover:opacity-100 hover:text-ink {sounding ===
                          file.id || here
                          ? 'md:opacity-100'
                          : ''}"
                        aria-label={sounding === file.id
                          ? `Stop ${file.titleTag ?? 'this file'}`
                          : `Play ${file.titleTag ?? 'this file'}`}
                        onclick={() => hear(file.id)}
                      >
                        {#if sounding === file.id}
                          <Icon name="pause" size={13} />
                        {:else}
                          <Icon name="play" size={13} />
                        {/if}
                      </button>
                    {/if}
                  </span>
                </td>

                <!-- The title and the artist share one line, and both truncate:
                     the full path is the title's tooltip. -->
                <td class="w-[34%] max-w-0 px-2.5">
                  <span class="block truncate">
                    <span class="text-body" data-text-size="body" title={file.path}>
                      {file.titleTag ?? file.path.split('/').pop()}
                    </span>
                    <span
                      class="ml-2 text-meta text-ink-4 transition group-hover:text-ink-3 group-data-selected:text-ink-3"
                      data-truncated="true"
                      data-tone="quiet"
                      data-promotes-on-hover="true"
                    >
                      {file.artistTag ?? file.path}
                    </span>
                  </span>
                </td>

                <td class="w-[18%] max-w-0 px-2.5">
                  <!-- A matched file is on a catalogue release, and that release
                       is its album whether or not the file's own tags name one. -->
                  {#if file.mappedAlbumId && file.mappedAlbumTitle}
                    <a
                      href="/releases/{file.mappedAlbumId}"
                      class="block truncate text-meta text-ink-2 hover:text-ink"
                      data-truncated="true"
                    >
                      {file.mappedAlbumTitle}
                    </a>
                  {:else}
                    <span class="block truncate text-meta text-ink-2" data-truncated="true">
                      {file.albumTag ?? '—'}
                    </span>
                  {/if}
                </td>

                <td class="px-2.5">
                  <span
                    class="mono text-meta text-ink-4 transition group-hover:text-ink-3 group-data-selected:text-ink-3"
                    data-truncated="false"
                    data-tone="quiet"
                    data-promotes-on-hover="true"
                  >
                    {fileFormat(file.path) || '—'}
                  </span>
                </td>

                <td class="mono px-2.5 text-right text-meta text-ink-2">
                  {formatDuration(file.durationMs)}
                </td>
                <td class="mono px-2.5 text-right text-meta text-ink-2">
                  {formatBytes(file.sizeBytes)}
                </td>

                <!-- Where the file stands, and the row's own actions beside it: a
                     tick or a short tag says what it is, and a quiet button says
                     what to do about it. -->
                <td class="px-2.5">
                  <span class="flex items-center gap-1">
                    <!-- Damaged audio sits beside the standing: a damaged file can
                         still be matched, and the upgrade sweep replaces it. -->
                    {#if file.decodeError && !file.missingAt}
                      <StateIcon icon="alert" label="Damaged audio" role="fail" note={file.decodeError} />
                    {/if}
                    <!-- How it was matched is the tick's tooltip. As a line of
                         text it was cut off in a column this narrow. -->
                    {#if standing.kind === 'tick'}
                      <StateIcon
                        icon="check"
                        role="ok"
                        label={file.matchStatus !== 'matched'
                          ? 'Identified'
                          : file.mappingManual
                            ? 'Manual match'
                            : `Matched by ${matchMethodName(file.mappingMethod)}`}
                        note={standing.title}
                      />
                    {:else}
                      <StateIcon icon={standing.icon} role={standing.role} label={standing.label} note={standing.title} />
                    {/if}

                    {#if !file.missingAt}
                      {#if file.setAside}
                        <Button
                          variant="ghost"
                          size="xs"
                          class="shrink-0"
                          disabled={$setAside.isPending}
                          onclick={() => changeSetAside(file)}
                        >
                          Return to review
                        </Button>
                      {:else}
                        {#if file.matchStatus !== 'matched'}
                          <Button
                            variant="ghost"
                            size="xs"
                            icon
                            title="Identify"
                            aria-label="Identify"
                            onclick={() => showIdentity(file.id)}
                          >
                            <Icon name="fingerprint" size={13} />
                          </Button>
                        {/if}
                        <!-- A track MusicBrainz has never heard of is named by its
                             page instead. The control is only on files nothing has
                             placed: a file that answers a catalogue track already
                             has a name, and one already held by its address has
                             this one behind it. -->
                        {#if file.matchStatus !== 'matched' && file.resolutionStatus !== 'source'}
                          <Button
                            variant="ghost"
                            size="xs"
                            icon
                            title="Name from SoundCloud"
                            aria-label="Name from SoundCloud"
                            onclick={() => nameFromSoundCloud(file)}
                          >
                            <Icon name="link" size={13} />
                          </Button>
                        {:else if file.identityUrl}
                          <Button
                            variant="ghost"
                            size="xs"
                            icon
                            href={file.identityUrl}
                            title="Open on SoundCloud"
                            aria-label="Open on SoundCloud"
                          >
                            <Icon name="external" size={13} />
                          </Button>
                        {/if}
                        <!-- Every decision about a file is taken in Review, a
                             matched one included: withdrawing its manual match is
                             a decision too. The verb follows what the row is. A
                             file that is matched has no open question, and the
                             button is still there because a settled match can be
                             withdrawn. -->
                        <Button variant="ghost" size="xs" class="shrink-0" onclick={() => toggleExpansion(file)}>
                          Decide
                        </Button>
                      {/if}
                    {/if}
                  </span>
                </td>
              </tr>

              {#if open}
                <tr>
                  <td colspan={columnCount} class="px-2.5 pb-2 pt-0">
                    <div class="flex flex-col gap-3 bg-panel px-3 py-2.5">
                      {#if file.matchStatus === 'ambiguous'}
                        <ReviewMatch file={file} ondecided={() => (expandedFileID = '')} />
                      {:else if file.resolutionStatus === 'needs_review' || file.resolutionStatus === 'conflict'}
                        <FileIdentity resolution={fileResolution} fileId={file.id} loading={loadingIdentity} error={identityError} chosen={chosenIdentity} onchoose={(index) => (chosenIdentity = index)} expectedMs={file.durationMs} />
                        {#if fileResolution && !loadingIdentity}
                          <div class="flex flex-wrap gap-2">
                            <Button disabled={identityPending || chosenIdentity < 0 || fileResolution.candidates[chosenIdentity] === undefined} onclick={() => $identityDecision.mutate({ fileId: file.id, recordingId: fileResolution!.candidates[chosenIdentity].recordingId })}>Accept</Button>
                            <Button variant="outline" disabled={identityPending} onclick={() => $localOnly.mutate(file.id)}>Keep local</Button>
                          </div>
                        {/if}
                      {/if}
                    </div>
                  </td>
                </tr>
              {/if}
            {/each}
          </tbody>
        </table>
      </div>
    </div>
  {:else if !$files.isError}
    <!-- An empty list says which question came back empty. "No audio files
         found" under a filter is an answer to a question nobody asked: the
         library has 1,500 files, and what is empty is this filter. Where the
         reader almost certainly meant a different screen, it says which one.

         A failed request is not an empty list. The ErrorNote above reports the
         failure; this branch is skipped so "No audio files found" does not
         also appear. -->
    <div class="px-5 pt-3">
      <EmptyPanel
        role={scanning ? 'busy' : 'idle'}
        heading={scanning ? 'Scanning' : empty.heading}
      >
        {#if !scanning && empty.detail}
          <p class="text-meta leading-relaxed text-ink-3">{empty.detail}</p>
        {/if}
        {#if !scanning && empty.elsewhere}
          <button
            type="button"
            class="w-fit text-left text-meta font-medium text-ink-2 underline decoration-ink-4 underline-offset-4 transition hover:text-ink"
            onclick={() => onheldtwice?.()}
          >
            {empty.elsewhere.label}
          </button>
        {/if}
      </EmptyPanel>
    </div>
  {/if}

  <Pager total={$files.data?.total ?? 0} {offset} {pageSize} keys={active} onchange={(next) => changePage(next)} />
</Settle>

{#if soundCloudFile}
  <SoundCloudTrackModal
    bind:open={soundCloudOpen}
    fileId={soundCloudFile.id}
    path={soundCloudFile.path}
  />
{/if}

{#snippet Problem(message: string)}
  <StatusBadge>
    <span class="min-w-0 text-meta text-ink">{message}</span>
  </StatusBadge>
{/snippet}

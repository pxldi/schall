<script lang="ts">
  import { toStore } from 'svelte/store';
  import { createMutation, createQuery, useQueryClient } from '@tanstack/svelte-query';
  import {
    Check,
    ExternalLink,
    Fingerprint,
    FolderPlus,
    Link2,
    LoaderCircle,
    Pause,
    Play,
    RefreshCw,
    Search
  } from '@lucide/svelte';
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
  import ReviewMatch from '$lib/components/ReviewMatch.svelte';
  import { listening } from '$lib/preview.svelte';
  import Button from '$lib/components/Button.svelte';
  import ControlRail from '$lib/components/ControlRail.svelte';
  import EmptyPanel from '$lib/components/EmptyPanel.svelte';
  import ErrorNote from '$lib/components/ErrorNote.svelte';
  import Pager from '$lib/components/Pager.svelte';
  import PillSelect from '$lib/components/PillSelect.svelte';
  import Segmented from '$lib/components/Segmented.svelte';
  import Settle from '$lib/components/Settle.svelte';
  import SoundCloudTrackModal from '$lib/components/SoundCloudTrackModal.svelte';
  import StateMark from '$lib/components/StateMark.svelte';
  import StateTag from '$lib/components/StateTag.svelte';
  import * as Table from '$lib/components/ui/table';
  import StatusBadge from '$lib/components/StatusBadge.svelte';

  // Where "Held twice" is. The library's three views are one page that hides two
  // of itself rather than three routes, and the view is read out of the address
  // bar once when the page mounts — so a link to it would change the address and
  // leave the reader looking at the same screen. The page owns which view is
  // showing, so the page is asked.
  let { onheldtwice }: { onheldtwice?: () => void } = $props();

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
  let deepLinkFileID = $state(page.url.searchParams.get('id') ?? '');
  let expandedFileID = $state(deepLinkFileID);
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
    try {
      fileResolution = await api.fileIdentity(fileId);
    } catch (error) {
      identityError = error;
    } finally {
      loadingIdentity = false;
    }
  }

  const identityDecision = createMutation({
    mutationFn: ({ fileId, recordingId }: { fileId: string; recordingId: string }) =>
      api.acceptIdentity(fileId, recordingId),
    onSuccess: (result) => {
      fileResolution = result;
      chosenIdentity = -1;
      void queryClient.invalidateQueries({ queryKey: ['library-files'] });
      void queryClient.invalidateQueries({ queryKey: ['library'] });
    }
  });
  const localOnly = createMutation({
    mutationFn: (fileId: string) => api.markLocalOnly(fileId),
    onSuccess: (result) => {
      fileResolution = result;
      chosenIdentity = -1;
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
        heading: 'Nothing matched that search',
        detail: `No file's path or tags contain ${fileSearch}.`
      };
    }
    if (setAsideOnly) {
      return { heading: 'No files are set aside', detail: '' };
    }
    if (resolution && !status) {
      return { heading: 'No file is in that identity state', detail: '' };
    }
    switch (status) {
      case 'duplicate': {
        const held = $heldTwice.data?.total ?? 0;
        const base =
          'This is for a file whose every candidate track somebody has already ' +
          'matched by hand, so nothing can be asked about it.';
        return {
          heading: 'No file is blocked by a decision',
          detail:
            held > 0
              ? `${base} The library holds ${held} ${held === 1 ? 'recording' : 'recordings'} more than once.`
              : base,
          elsewhere: held > 0 && onheldtwice ? { label: 'Open Held twice' } : undefined
        };
      }
      case 'missing':
        return {
          heading: 'Every file is where the library left it',
          detail: 'A file here would be one the last scan did not find on disk.'
        };
      case 'unreadable':
        return {
          heading: 'Every file could be read',
          detail: 'A file here would be one on disk that the scanner could not open.'
        };
      case 'matched':
        return { heading: 'No file is matched to a catalogue track', detail: '' };
      case 'unmatched':
        return { heading: 'Every file is matched or being asked about', detail: '' };
      case 'ambiguous':
        return { heading: 'No file is waiting on a matching decision', detail: '' };
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
  // with certainty, and a short hairline tag everywhere else. The tag's own
  // words are one or two, because a table column of them is read at a glance;
  // the full sentence `fileStanding` already writes stays on the tag as its
  // title, for whoever rests a pointer on it.
  type Standing =
    | { kind: 'tick'; title: string }
    | { kind: 'tag'; tone: 'neutral' | 'attention' | 'broken' | 'warn'; label: string; title: string };

  function standingOf(file: LibraryFile): Standing {
    const { text, state } = fileStanding(file);
    // A file gone from disk or one the scanner could not open are their own
    // shapes, ahead of anything matching found: neither is a matching
    // question, and both this page's own status filters ask for directly.
    if (file.missingAt) return { kind: 'tag', tone: 'neutral', label: 'missing', title: text };
    if (file.scanError) return { kind: 'tag', tone: 'broken', label: 'unreadable', title: file.scanError };
    if (state === 'ok') return { kind: 'tick', title: text };
    if (file.matchStatus === 'duplicate') return { kind: 'tag', tone: 'neutral', label: 'held twice', title: text };
    if (file.matchStatus === 'ambiguous') return { kind: 'tag', tone: 'attention', label: 'ambiguous', title: text };
    switch (file.resolutionStatus) {
      case 'resolved':
        return { kind: 'tag', tone: 'neutral', label: 'unmatched', title: text };
      case 'needs_review':
        return { kind: 'tag', tone: 'attention', label: 'unidentified', title: text };
      case 'conflict':
        return { kind: 'tag', tone: 'broken', label: 'conflict', title: text };
      case 'local_only':
        return { kind: 'tag', tone: 'neutral', label: 'local only', title: text };
      case 'failed':
        return { kind: 'tag', tone: 'broken', label: 'lookup failed', title: text };
      default:
        return { kind: 'tag', tone: 'neutral', label: 'pending', title: text };
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
</script>

<ControlRail label="Scan the library and search its files" sticky={false}>
  <!-- The dim line the board draws beside the chips: what the last scan did,
       and how long ago. "N folder(s)" is also the way into the folders
       disclosure below — the paths themselves used to sit here as their own
       row of chips, which only repeated what that disclosure already lists in
       full. -->
  <span class="text-meta text-ink-3">
    {scanLine}
    <span class="text-ink-4">·</span>
    <button
      type="button"
      class="text-ink-2 underline decoration-white/24 underline-offset-2 transition hover:text-ink"
      aria-expanded={showFolders}
      onclick={() => (showFolders = !showFolders)}
    >
      {rootCount} {rootCount === 1 ? 'folder' : 'folders'}
    </button>
  </span>

  <form class="ml-auto flex min-w-0 items-center gap-2" onsubmit={applyFileSearch}>
    <input
      bind:value={fileSearchInput}
      aria-label="Search discovered files"
      placeholder="Search files"
      class="field min-w-0 max-w-44 flex-1"
    />
    <Button type="submit" variant="outline"><Search size={12} strokeWidth={2.2} /> Search</Button>
  </form>

  <Button onclick={() => $scan.mutate()} disabled={scanning || rootCount === 0}>
    {#if scanning}
      <LoaderCircle size={13} class="animate-spin" /> Scanning
    {:else}
      <RefreshCw size={13} strokeWidth={2.3} /> {scanned ? 'Rescan' : 'Scan'}
    {/if}
  </Button>
</ControlRail>

<div class="flex flex-col gap-4 px-6 py-4">
  <!-- Three reads of the library: the figures, the folders and the files. Each
       one is only a read, so where asking again is the whole answer the note
       asks by itself rather than handing the reader a button that repeats what
       just failed. -->
  {#if $summary.error}
    <ErrorNote error={$summary.error} retry={() => $summary.refetch()} />
  {/if}
  {#if $roots.error}
    <ErrorNote error={$roots.error} retry={() => $roots.refetch()} />
  {/if}
  {#if $files.error}
    <ErrorNote error={$files.error} retry={() => $files.refetch()} />
  {/if}
  {#if $summary.data?.scanStatus === 'failed'}
    {@render Problem($summary.data.scanError ?? 'The last library scan failed.')}
  {/if}

  {#if showFolders}
    <section class="flex flex-col gap-2.5">
      <div class="flex items-center gap-2.5">
        <span class="label">Music folders</span>
        <span class="h-px flex-1 bg-line-thin"></span>
      </div>

      <form class="flex flex-wrap gap-2" onsubmit={addFolder}>
        <input
          bind:value={path}
          aria-label="Music folder path"
          placeholder="/music"
          class="field min-w-0 flex-1"
        />
        <Button type="submit" disabled={$addRoot.isPending}>
          <FolderPlus size={13} strokeWidth={2.2} /> Add
        </Button>
      </form>
      <span class="text-meta leading-relaxed text-ink-3">
        must exist in the container, inside an allowed root
      </span>
      {#if $addRoot.isError}<ErrorNote error={$addRoot.error} />{/if}

      <div class="flex flex-col">
        {#each $roots.data?.items ?? [] as root (root.id)}
          <div class="flex items-center gap-3 border-b border-line-thin px-3 py-2 last:border-b-0">
            <span class="numeric min-w-0 flex-1 truncate text-body text-ink" title={root.path}>
              {root.path}
            </span>
            <span class="text-meta text-ink-4">
              {root.lastScannedAt
                ? `scanned ${calendarDate(root.lastScannedAt)}`
                : 'not scanned yet'}
            </span>
          </div>
        {:else}
          <div class="px-3 py-2">
            <span class="text-meta text-ink-3">add your first music folder</span>
          </div>
        {/each}
      </div>
    </section>
  {/if}

  <section class="flex flex-col gap-2.5">
    {#if setAsideOnly}
      <div class="flex flex-wrap items-center gap-2.5">
        <span class="text-meta font-medium text-ink">Set-aside files</span>
        <Button variant="ghost" size="xs" onclick={() => (setAsideOnly = false)}>
          Show all files
        </Button>
      </div>
    {/if}

    <div class="flex flex-wrap items-center gap-2">
      <!-- "Duplicates" used to be here, and it was the wrong word twice over.
           This filter never meant "the library holds this twice" — that question
           is the Held twice tab, and it is asked about recordings rather than
           about files. It means the narrow thing the matcher records: every
           track this file could be is already held by a decision somebody made
           by hand, so there is nothing left to ask about it. A reader looking
           for their duplicates pressed it, read "No audio files found", and
           concluded the library was clean while the tab beside it said
           eighteen. -->
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

      <!-- "Any identity" is a filter somebody chose, not an empty box. The
           picker calls any value of `''` unchosen and dims the trigger for it,
           so the dimming is said back off here. -->
      <PillSelect
        options={resolutionChoices}
        value={resolution}
        onchange={(next) => setResolution(next as ResolutionStatus | '')}
        label="Filter by identity state"
      />

      {#if hasFilters}
        <Button variant="ghost" size="sm" onclick={clearFilters}>Clear filters</Button>
      {/if}
    </div>

    <!-- One <audio> element for the whole table. A page is twenty-five rows,
         and twenty-five media elements is twenty-five things the browser holds
         open in order to play one file. Where this one points is the row that
         is sounding. -->
    <audio bind:this={audio} class="hidden" preload="none" onended={() => (sounding = '')}
    ></audio>

    <Settle pending={$files.isPending}>
      {#snippet placeholder()}
      <!-- A list still on its way is not an empty list. Each filter is its own
           question with its own answer, so pressing one leaves nothing on
           screen until that answer lands, and "No audio files found" in that
           gap is a wrong answer rather than a wait.

           The wait is drawn as the table it is waiting for: the same box, a
           strip where the column names will stand, and bars the height of a
           row. The list keeps its shape when the files land. -->
      <div
        class="layout-width max-h-[calc(100dvh-9rem)] overflow-hidden rounded-panel border border-line-thin"
        role="status"
        aria-label="Loading files"
      >
        <div class="h-9 border-b border-line-thin"></div>
        <!-- Fill about 2016px of rows so a 4K viewport does not outgrow the wait. -->
        {#each Array(60) as _, placeholderIndex (placeholderIndex)}
          <div
            class="h-[3.46875rem] border-b border-line-thin px-3 py-2 last:border-b-0"
            aria-hidden="true"
          >
            <div
              class="h-full animate-pulse rounded-row bg-surface-regular"
            ></div>
          </div>
        {/each}
      </div>
      {/snippet}
    {#if ($files.data?.items ?? []).length}
      <!-- The scroll happens in the table's own box rather than on the page,
           because that is what the column names are held against: a header
           sticks to the nearest thing that scrolls, and if that thing is the
           whole document the header slides away with the rows. Below the
           column names' own width the box scrolls sideways instead of
           reflowing the row — `Table.Root` scrolls by default. -->
      <Table.Root wrapperClass="layout-width max-h-[68dvh] scroll-pt-9 rounded-panel border border-line-thin">
        <Table.Header>
          <Table.Row class="hover:bg-transparent">
            <Table.Head class="w-10 text-center">#</Table.Head>
            <Table.Head>Title</Table.Head>
            <Table.Head>Album</Table.Head>
            <!-- What kind of audio file this is, read off the extension. The
                 scanner stores no codec, no bit depth and no sample rate, so
                 this says FLAC or MP3 and stops there. -->
            <Table.Head>Format</Table.Head>
            <Table.Head numeric>Length</Table.Head>
            <Table.Head numeric>Size</Table.Head>
            <Table.Head>Standing</Table.Head>
          </Table.Row>
        </Table.Header>
        <Table.Body>
          {#each $files.data?.items ?? [] as file, index (file.id)}
            {@const standing = standingOf(file)}
            {@const open = expandedFileID === file.id}
            <!-- Pointing at a row swaps its mode: it lifts onto a raised
                 surface, its hairline goes, and the row number is replaced in
                 place by the play control. Nothing changes width or height
                 while that happens — the number and the play control share one
                 box and take turns being the visible one, and the hairline
                 turns transparent rather than being removed.

                 The row below a hairline is the one that draws it, so the rule
                 above a pointed-at row is taken down by its neighbour. -->
            <Table.Row
              selected={open}
              class="group hover:border-transparent [&:has(+tr:hover)]:border-transparent {file.missingAt
                ? 'opacity-45'
                : ''}"
            >
              <Table.Cell class="w-10 shrink-0 px-2">
                <span class="grid size-7 place-items-center">
                  <span
                    class="numeric col-start-1 row-start-1 text-micro text-ink-4 transition group-hover:text-ink-3 group-data-selected:text-ink-3 {file.missingAt
                      ? ''
                      : 'md:group-hover:opacity-0 md:group-focus-within:opacity-0'} {sounding ===
                    file.id
                      ? 'opacity-0'
                      : ''}"
                  >
                    {offset + index + 1}
                  </span>
                  <!-- A file the last scan did not find on disk has nothing to
                       play, so it keeps its number and is offered no control.
                       Visible without a hover below `md`, where a touch screen
                       never sends one; hidden behind a pointer or a keyboard at
                       `md` and above, where the number is the useful default. -->
                  {#if !file.missingAt}
                    <button
                      type="button"
                      class="tap col-start-1 row-start-1 grid size-7 place-items-center rounded-control text-ink-2 opacity-100 transition md:opacity-0 md:group-focus-within:opacity-100 md:group-hover:opacity-100 hover:text-ink {sounding ===
                      file.id
                        ? 'opacity-100'
                        : ''}"
                      aria-label={sounding === file.id
                        ? `Stop ${file.titleTag ?? 'this file'}`
                        : `Play ${file.titleTag ?? 'this file'}`}
                      onclick={() => hear(file.id)}
                    >
                      {#if sounding === file.id}
                        <Pause size={13} strokeWidth={2.2} />
                      {:else}
                        <Play size={13} strokeWidth={2.2} />
                      {/if}
                    </button>
                  {/if}
                </span>
              </Table.Cell>

              <!-- The title wraps. It is the thing being identified, and a name
                   cut off in the middle is a name the reader has to hover to
                   read. Everything beside it is metadata and takes the
                   ellipsis instead. -->
              <Table.Cell class="tap-tall min-w-52">
                <span class="block text-body text-ink" data-text-size="body" title={file.path}>
                  {file.titleTag ?? file.path.split('/').pop()}
                </span>
                <!-- Ink 4 clears the contrast floor up to the regular surface
                     and no further, and a pointed-at or open row is drawn on a
                     thicker one — so the quiet line promotes. -->
                <span
                  class="mt-0.5 block max-w-72 truncate text-meta text-ink-4 transition group-hover:text-ink-3 group-data-selected:text-ink-3"
                  data-truncated="true"
                  data-tone="quiet"
                  data-promotes-on-hover="true"
                >
                  {file.artistTag ?? file.path}
                </span>
              </Table.Cell>

              <Table.Cell>
                <span class="block max-w-40 truncate text-meta text-ink-2" data-truncated="true">
                  {file.albumTag ?? '—'}
                </span>
              </Table.Cell>

              <Table.Cell>
                <span
                  class="numeric text-meta text-ink-4 transition group-hover:text-ink-3 group-data-selected:text-ink-3"
                  data-truncated="false"
                  data-tone="quiet"
                  data-promotes-on-hover="true"
                >
                  {fileFormat(file.path) || '—'}
                </span>
              </Table.Cell>

              <Table.Cell numeric class="text-meta text-ink-2">
                {formatDuration(file.durationMs)}
              </Table.Cell>
              <Table.Cell numeric class="text-meta text-ink-2">
                {formatBytes(file.sizeBytes)}
              </Table.Cell>

              <!-- Where the file stands, and the row's own actions beside it:
                   a tick or a short tag says what it is, and a quiet button
                   says what to do about it. The two used to sit at opposite
                   ends of the row; the board reads them as one statement. -->
              <Table.Cell>
                <span class="flex flex-wrap items-center gap-1.5">
                  {#if standing.kind === 'tick'}
                    <StateMark role="ok"><Check size={10} strokeWidth={3.2} /></StateMark>
                    {#if file.matchStatus === 'matched'}
                      <span class="max-w-44 truncate text-meta text-ink-2" title={standing.title}>
                        {file.mappingManual
                          ? 'Manual match'
                          : `Matched by ${matchMethodName(file.mappingMethod)}`}
                      </span>
                    {/if}
                  {:else}
                    <StateTag tone={standing.tone} title={standing.title}>{standing.label}</StateTag>
                  {/if}

                  {#if !file.missingAt}
                    {#if file.setAside}
                      <Button
                        variant="ghost"
                        size="sm"
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
                          size="sm"
                          icon
                          title="What is this file?"
                          onclick={() => showIdentity(file.id)}
                        >
                          <Fingerprint size={13} />
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
                          size="sm"
                          icon
                          title="This is a SoundCloud track"
                          onclick={() => nameFromSoundCloud(file)}
                        >
                          <Link2 size={13} />
                        </Button>
                      {:else if file.identityUrl}
                        <Button
                          variant="ghost"
                          size="sm"
                          icon
                          href={file.identityUrl}
                          title="Open on SoundCloud"
                        >
                          <ExternalLink size={13} />
                        </Button>
                      {/if}
                      <!-- Every decision about a file is taken in Review, a
                           matched one included: withdrawing its manual match is a
                           decision too, and it belongs in the same queue as the
                           rest. A duplicate goes to the same place under its own
                           question — which of the two copies to keep — including
                           one somebody already answered, because asking for it is
                           asking again. -->
                      <!-- The verb follows what the row is. `Decide` names a
                           question, and a file that is matched has no open
                           question: the button is still there, because a settled
                           match can be withdrawn and Review is where that is
                           done, but it offers a change rather than asking one. -->
                      <Button variant="ghost" size="sm" class="shrink-0" onclick={() => toggleExpansion(file)}>
                        Decide
                      </Button>
                    {/if}
                  {/if}
                </span>
              </Table.Cell>
            </Table.Row>

            {#if file.scanError}
              <Table.Row class="hover:bg-transparent">
                <Table.Cell colspan={columnCount} class="pt-0">
                  <span class="block max-w-160 truncate text-meta text-fail" title={file.scanError}>
                    {file.scanError}
                  </span>
                </Table.Cell>
              </Table.Row>
            {/if}

            {#if open}
              <Table.Row class="hover:bg-transparent">
                <Table.Cell colspan={columnCount} class="pt-0">
                  <div class="flex flex-col gap-3 rounded-panel bg-surface-regular px-3 py-2.5">
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
                </Table.Cell>
              </Table.Row>
            {/if}
          {/each}
        </Table.Body>
      </Table.Root>
    {:else if !$files.isError}
      <!-- An empty list says which question came back empty. "No audio files
           found" under a filter is an answer to a question nobody asked: the
           library has 1,500 files, and what is empty is this filter. Where the
           reader almost certainly meant a different screen, it says which one.

           A failed request is not an empty list. The ErrorNote above reports
           the failure; this branch is skipped so "No audio files found"
           does not also appear. -->
      <EmptyPanel
        role={scanning ? 'busy' : 'idle'}
        heading={scanning ? 'Scanning for audio files' : empty.heading}
      >
        {#if !scanning && empty.detail}
          <p class="text-meta leading-relaxed text-ink-3">{empty.detail}</p>
        {/if}
        {#if !scanning && empty.elsewhere}
          <button
            type="button"
            class="w-fit text-left text-meta font-medium text-ink-2 underline decoration-white/24 underline-offset-4 transition hover:text-ink"
            onclick={() => onheldtwice?.()}
          >
            {empty.elsewhere.label}
          </button>
        {/if}
      </EmptyPanel>
    {/if}

    <Pager total={$files.data?.total ?? 0} {offset} {pageSize} onchange={(next) => changePage(next)} />
    </Settle>
  </section>
</div>

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

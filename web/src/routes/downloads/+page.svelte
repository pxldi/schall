<script lang="ts">
  import { untrack } from 'svelte';
  import { toStore } from 'svelte/store';
  import {
    createMutation,
    createQuery,
    keepPreviousData,
    type Query,
    useQueryClient
  } from '@tanstack/svelte-query';
  import { Check, ChevronDown, LoaderCircle, Play, RotateCcw, Search } from '@lucide/svelte';
  import { page } from '$app/state';
  import {
    api,
    DuplicateProtection,
    SourceChanged,
    type DownloadRequest,
    type DownloadRequests,
    type DownloadView,
    type DuplicateEvidence,
    type ImportReview,
    lookingFor,
    type SourceOffer
  } from '$lib/api';
  import {
    downloadName,
    formatBytes,
    keepInUrl,
    relativeTime,
    urlChoice,
    urlText
  } from '$lib/utils';
  import Button from '$lib/components/Button.svelte';
  import Chip from '$lib/components/Chip.svelte';
  import EmptyPanel from '$lib/components/EmptyPanel.svelte';
  import ErrorNote from '$lib/components/ErrorNote.svelte';
  import DuplicateNotice from '$lib/components/DuplicateNotice.svelte';
  import { progress } from '$lib/vocabulary';
  import ControlRail from '$lib/components/ControlRail.svelte';
  import Segmented from '$lib/components/Segmented.svelte';
  import Settle from '$lib/components/Settle.svelte';
  import StateMark from '$lib/components/StateMark.svelte';
  import StateTag from '$lib/components/StateTag.svelte';
  import UploadsTab from '$lib/components/UploadsTab.svelte';
  import WantsTab from '$lib/components/WantsTab.svelte';

  type Role = 'ok' | 'idle' | 'decide' | 'busy' | 'fail';

  const queryClient = useQueryClient();
  let expanded = $state<string | null>(null);

  // The three tabs are one lifetime in order: music asked for, music pulled
  // from a peer, music pushed from the browser. The wants are here because they
  // are the same question one step earlier — what became of the thing I asked
  // for — and answering it on a screen of its own would have been a seventh
  // navigation item for a list nobody reads every day.
  const tabKeys = ['wanted', 'peers', 'uploads'] as const;

  // Read once, before anything is tracked, so that following the address bar
  // back into it later cannot become a dependency of the effect that writes it.
  const opened = page.url;
  let tab = $state<(typeof tabKeys)[number]>(urlChoice(opened, 'tab', tabKeys, 'peers'));

  // The screen opens on the transfers still under way, and the piles they end
  // up in are beside it rather than under an "everything else" button. Three
  // questions live in that pile — was my copy imported, was it discarded, did
  // it fail — and one button cannot answer them; it would also put every
  // request back in the page, which is the thing being fixed.
  const viewKeys = ['open', 'review', 'imported', 'discarded', 'failed', 'all'] as const;
  let view = $state<DownloadView>(urlChoice(opened, 'view', viewKeys, 'open'));

  // What the reader typed to narrow the list on screen. The server pages this
  // pile rather than searching it, so the search is read against the page it
  // already sent rather than asked of it again.
  let search = $state(urlText(opened, 'q'));

  $effect(() => {
    keepInUrl(opened.pathname, { tab, view, q: search }, { tab: 'peers', view: 'open', q: '' });
  });

  // Following the navigation badge lands here with the open pile named, from
  // whichever pile was last looked at. This depends on the address and on
  // nothing else: the pile it compares against is read untracked, so the effect
  // above, which writes the address, cannot make this one answer it.
  $effect(() => {
    const asked = urlChoice(page.url, 'view', viewKeys, 'open');
    if (asked !== untrack(() => view)) setView(asked);
  });

  // The pile and the page are what the key names, because each of them is a
  // different question with a different answer. Filing all six piles under one
  // key and refetching after the press did nothing at all while the first
  // request was still in flight — the refetch joined the request already
  // running, which had been built with the pile the screen opened on, so the
  // strip said Imported over the open transfers.
  const downloads = createQuery(
    toStore(() => ({
      queryKey: ['downloads', 'list', view],
      queryFn: () => api.downloads({ view }),
      placeholderData: keepPreviousData,
      // Transfers move on their own and the server says so over the event
      // stream, so this is the safety net rather than the mechanism: it covers a
      // stream blocked by a proxy or dropped without reconnecting yet. It still
      // stops entirely once all work has settled.
      refetchInterval: (query: Query<DownloadRequests>) =>
        query.state.data?.items.some(
          (item) =>
            item.status === 'started' ||
            (item.status === 'completed' &&
              (item.importStatus === 'pending' || item.importStatus === 'validating'))
        )
          ? 20_000
          : false
    }))
  );
  const items = $derived(
    [...($downloads.data?.items ?? [])].sort(
      (a, b) =>
        b.requestedAt.localeCompare(a.requestedAt) || a.id.localeCompare(b.id)
    )
  );
  const counts = $derived($downloads.data?.counts);

  // Release and artist, in the order the row draws them: the release named
  // plainly, the artist beside it in ink-3 only when there is a release to
  // set it apart from. A request with no release names the wanted recording
  // instead, and there is nothing left to dim beside it.
  function releaseTitle(item: DownloadRequest) {
    return item.albumTitle ?? item.entryTitle ?? '';
  }
  function releaseArtist(item: DownloadRequest) {
    return item.artistName ?? item.entryArtist ?? '';
  }
  function rowHeading(item: DownloadRequest) {
    return releaseTitle(item) || releaseArtist(item) || 'Unnamed download';
  }
  function rowSubject(item: DownloadRequest) {
    return releaseTitle(item) && releaseArtist(item) ? releaseArtist(item) : '';
  }

  // What the search box matches against: the words already on the row, so a
  // search never finds a row it could not also justify by pointing at it.
  const filteredItems = $derived(
    search.trim()
      ? items.filter((item) => {
          const needle = search.trim().toLowerCase();
          const haystack = [rowHeading(item), rowSubject(item), item.username]
            .join(' ')
            .toLowerCase();
          return haystack.includes(needle);
        })
      : items
  );

  function setView(next: DownloadView) {
    view = next;
  }

  // A tab badge has to be right on the tab you are not looking at, which is the
  // only reason it exists — so this asks for the list rather than reading
  // whatever happens to be cached. It is the same key the tab itself uses, so
  // the two share one request and one poll rather than doubling them.
  const uploads = createQuery({
    queryKey: ['uploads'],
    queryFn: api.uploads,
    // The safety net, matching the tab's own: it stops once nothing is moving.
    refetchInterval: (query) =>
      query.state.data?.items.some((item) =>
        ['staged', 'queued', 'validating'].includes(item.status)
      )
        ? 15_000
        : false
  });
  const openUploads = $derived(
    ($uploads.data?.items ?? []).filter((item) =>
      ['staged', 'queued', 'validating'].includes(item.status)
    ).length
  );

  // The same again for the wants: the figure has to be right on the tab nobody
  // is looking at, and it is the first page of the same list the tab itself
  // reads, so the two are one request.
  const looking = createQuery({
    queryKey: ['wants', 'looking', 'count'],
    queryFn: () => api.wants({ statuses: lookingFor, limit: 1 })
  });

  // A count of nothing is not news, so a tab with no open work carries no
  // figure at all rather than a zero. The peers figure is the server's count of
  // open requests, which is the figure the navigation badge carries and the
  // figure the open filter shows — one rule, counted once.
  const tabs = $derived([
    { value: 'wanted', name: 'Wishlist', count: $looking.data?.total || undefined },
    { value: 'peers', name: 'From peers', count: counts?.open || undefined },
    { value: 'uploads', name: 'Uploaded', count: openUploads || undefined }
  ]);

  // The name of each pile, in the words the rows in it already use. The counts
  // come from the same answer as the rows, so a figure here and the list under
  // it are never two different reads.
  const views = $derived([
    { value: 'open', name: 'Open', count: counts?.open },
    { value: 'review', name: 'Needs review', count: counts?.review },
    { value: 'imported', name: 'Imported', count: counts?.imported },
    { value: 'discarded', name: progress.discarded, count: counts?.discarded },
    { value: 'failed', name: 'Failed', count: counts?.failed },
    { value: 'all', name: 'All', count: counts?.all }
  ]);

  // What an empty pile means, which is never just "empty".
  const emptyHeadings: Record<DownloadView, string> = {
    open: 'Nothing downloading',
    review: 'Nothing waiting on you',
    imported: 'Nothing imported yet',
    discarded: 'Nothing discarded',
    failed: 'Nothing failed',
    all: 'Nothing requested yet'
  };
  // A library that has never asked a peer for anything is not an empty filter,
  // whichever filter it is read under.
  const nothingRequested = $derived(counts?.all === 0);
  // A search that finds nothing is its own reason, not the pile's.
  const nothingMatches = $derived(items.length > 0 && filteredItems.length === 0);
  const emptyHeading = $derived(
    nothingMatches
      ? 'Nothing matches that search'
      : nothingRequested
        ? emptyHeadings.all
        : emptyHeadings[view]
  );

  async function refresh() {
    await Promise.all([
      queryClient.invalidateQueries({ queryKey: ['downloads'] }),
      queryClient.invalidateQueries({ queryKey: ['dashboard'] }),
      queryClient.invalidateQueries({ queryKey: ['releases'] })
    ]);
  }

  // Starting is refused while the library may already hold the release. The
  // question is kept against the request it was raised for, so answering it
  // starts that request and no other.
  let duplicateQuestion = $state<{ requestId: string; evidence: DuplicateEvidence } | null>(null);
  // The peer still offers the folder but no longer offers what was recorded.
  // Kept against the request it was raised for, like the duplicate question.
  let sourceQuestion = $state<{ requestId: string; offer: SourceOffer } | null>(null);

  const startDownload = createMutation({
    mutationFn: ({
      requestId,
      acknowledged = false,
      sourceAcknowledged = false
    }: {
      requestId: string;
      acknowledged?: boolean;
      sourceAcknowledged?: boolean;
    }) => api.startDownload(requestId, acknowledged, sourceAcknowledged),
    onError: (error, variables) => {
      if (error instanceof DuplicateProtection) {
        duplicateQuestion = { requestId: variables.requestId, evidence: error.evidence };
      }
      if (error instanceof SourceChanged) {
        sourceQuestion = { requestId: variables.requestId, offer: error.offer };
      }
    },
    onSuccess: async () => {
      duplicateQuestion = null;
      sourceQuestion = null;
      await refresh();
    }
  });
  const cancelDownload = createMutation({
    mutationFn: (requestId: string) => api.cancelDownload(requestId),
    onSuccess: refresh
  });
  // What a person typed in answer to a peer, kept against the request the
  // question was shown on.
  let peerReplies = $state<Record<string, string>>({});
  // Schall answers the human checks it recognises by itself. This is for the
  // ones it does not: the message is shown as the peer wrote it, and what goes
  // back is what the person typed, unread by anything here.
  const replyToPeer = createMutation({
    mutationFn: (input: { requestId: string; username: string; message: string }) =>
      api.replyToPeer(input.requestId, { username: input.username, message: input.message }),
    onSuccess: async (_row, variables) => {
      peerReplies = { ...peerReplies, [variables.requestId]: '' };
      await refresh();
    }
  });
  // Retrying asks the peer only for what did not arrive. Passing no paths
  // retries every failed file of the request.
  const retryDownload = createMutation({
    mutationFn: (input: { requestId: string; paths?: string[] }) =>
      api.retryDownload(input.requestId, input.paths),
    onSuccess: refresh
  });
  // Sending an import that gave up back through validation. It sits here rather
  // than in Review because it answers nothing: the import recorded no comparison,
  // so there is no question, only a failure and the chance to run it again — the
  // same shape as retrying a transfer that did not arrive.
  const revalidateImport = createMutation({
    mutationFn: (requestId: string) => api.revalidateDownload(requestId),
    onSuccess: refresh
  });

  // What each line of the import history says happened. "Resolution" was the
  // field name rather than the event: what a person did was match a file to a
  // track by hand.
  const reviewLabels: Record<ImportReview['kind'], string> = {
    paused: 'Paused for review',
    revalidated: 'Validation retried',
    imported: 'Imported',
    resolved: 'File matched by hand',
    withdrawn: 'Hand match withdrawn',
    source_removed: 'Provider copy removed',
    source_retained: 'Provider copy kept',
    discarded: 'Copy discarded'
  };

  function quality(item: DownloadRequest) {
    const format = item.format ? item.format.toUpperCase() : 'Unknown format';
    return item.averageBitRate ? `${format} · ${item.averageBitRate} kbps` : format;
  }

  // How much of what was asked for this request holds. A single-track request
  // holds one track and that is the whole of what "1 of 1 tracks" says, which
  // is nothing: most rows on this page are one track fetched for one want, so
  // the phrase stood on nearly every row and told the reader apart from
  // nothing. It is said only where the two figures can differ.
  function completeness(item: DownloadRequest) {
    if (!item.expectedTrackCount) {
      return item.fileCount === 1 ? '' : `${item.fileCount} files`;
    }
    if (item.expectedTrackCount === 1 && item.fileCount === 1) return '';
    return `${item.fileCount} of ${item.expectedTrackCount} tracks`;
  }

  function transferred(item: DownloadRequest) {
    if (!item.totalSizeBytes) return 0;
    return Math.min(100, Math.round((item.progress.transferredBytes / item.totalSizeBytes) * 100));
  }

  const statusLabels: Record<DownloadRequest['status'], string> = {
    requested: 'Requested',
    started: 'Downloading',
    completed: 'Transferred',
    failed: 'Failed',
    cancelled: 'Cancelled'
  };

  // A started request whose every file is still sitting in the peer's upload
  // queue is not downloading, and saying so was the difference between "this is
  // slow" and "this is broken". Schall has always known — slskd reports
  // `Queued, Remotely` and it is recorded per file — the count simply never
  // reached here.
  function waiting(item: DownloadRequest) {
    return (
      item.status === 'started' &&
      item.progress.transferCount > 0 &&
      item.progress.queuedCount === item.progress.transferCount
    );
  }

  function statusLabel(item: DownloadRequest) {
    if (waiting(item)) return 'Queued at peer';
    if (item.status !== 'completed') return statusLabels[item.status];
    switch (item.importStatus) {
      case 'validating': return 'Validating';
      case 'imported': return 'Imported';
      case 'needs_review': return 'Needs review';
      case 'discarded': return progress.discarded;
      default: return 'Awaiting import';
    }
  }

  // The status decides the row's mark and its chip, so both are graded once.
  // A completed transfer is not yet an outcome: what happened to it afterwards
  // is what the row reports.
  function stateRole(item: DownloadRequest): Role {
    switch (item.status) {
      case 'failed': return 'fail';
      case 'started': return 'busy';
      case 'requested': return 'idle';
      case 'cancelled': return 'idle';
    }
    switch (item.importStatus) {
      case 'imported': return 'ok';
      case 'needs_review': return 'decide';
      case 'discarded': return 'idle';
      default: return 'busy';
    }
  }

  // The row's mark is graded a step dimmer than its status when the only thing
  // happening is a wait rather than work: a file sitting in a peer's own queue
  // is not the same fact as one actually moving, even though both are
  // `started` underneath.
  function dotRole(item: DownloadRequest): Role {
    if (item.status === 'started' && waiting(item)) return 'idle';
    return stateRole(item);
  }

  // Only "needs review" asks for a person; every other open state is
  // information rather than a question, so it gets the plain tag. A failure
  // gets the broken tone so a Failed row reads as broken at a glance, not just
  // by its word.
  function tagTone(item: DownloadRequest): 'neutral' | 'attention' | 'broken' {
    if (item.status === 'failed') return 'broken';
    if (item.status === 'completed' && item.importStatus === 'needs_review') return 'attention';
    return 'neutral';
  }

  // Where the row's own Decide button goes. Evidence is recorded only while an
  // import is paused for review, so this is the same address the disclosure
  // already sends the reader to for that case.
  function decideHref(item: DownloadRequest) {
    return item.importEvidence ? `/review?download=${item.id}` : undefined;
  }

  // How long a request has been waiting, not the clock time it was made at.
  // This line is read to decide whether a request that has not started yet is
  // waiting normally or is stuck, and `8/15/2026, 11:47:00 AM` makes the reader
  // do that subtraction themselves.
  function requestedOn(value: string) {
    return relativeTime(value);
  }

  function subLine(item: DownloadRequest) {
    const parts = [item.username, quality(item), completeness(item)].filter(Boolean);
    if (item.status === 'requested') parts.push(`requested ${requestedOn(item.requestedAt)}`);
    return parts.join(' · ');
  }

  // Every one of these failures was raised by a button on one row, so each is
  // kept against the request it was pressed for. Stacked at the top of the page
  // they said nothing about which transfer they belonged to.
  const rowErrors = $derived.by(() => {
    // The failure itself is kept, not the one string it used to be flattened
    // into. The row draws it through ErrorNote, which needs the status and the
    // server's own title to choose the sentence.
    const byRow: Record<string, unknown[]> = {};
    const add = (requestId: string | undefined, failure: unknown) => {
      if (!requestId || !failure) return;
      byRow[requestId] = [...(byRow[requestId] ?? []), failure];
    };
    // A duplicate or a changed folder is a question rather than a failure, and
    // is asked at the row as its own panel.
    if (
      $startDownload.isError &&
      !($startDownload.error instanceof DuplicateProtection) &&
      !($startDownload.error instanceof SourceChanged)
    ) {
      add($startDownload.variables?.requestId, $startDownload.error);
    }
    if ($cancelDownload.isError) add($cancelDownload.variables, $cancelDownload.error);
    if ($retryDownload.isError) add($retryDownload.variables?.requestId, $retryDownload.error);
    if ($revalidateImport.isError) {
      add($revalidateImport.variables, $revalidateImport.error);
    }
    if ($replyToPeer.isError) add($replyToPeer.variables?.requestId, $replyToPeer.error);
    return byRow;
  });

  // The peer's message on one line. The whole of it stays in the title, because
  // a message clipped in the middle is still what has to be answered.
  function clipped(message: string) {
    const collapsed = message.replace(/\s+/g, ' ').trim();
    return collapsed.length > 140 ? `${collapsed.slice(0, 139)}\u2026` : collapsed;
  }

  const transferTones: Record<string, string> = {
    completed: 'text-ok',
    failed: 'text-fail',
    cancelled: 'text-ink-4',
    queued: 'text-ink-2',
    downloading: 'text-busy'
  };

  // One name per transfer state, wherever the state is shown. The chip on a
  // request said `Queued at peer` while the file rows under it printed the
  // state as the wire records it — `queued` — and the reader had to work out
  // that the two were the same fact. That fact is the only one that matters
  // here: whether bytes are moving at all.
  const transferStates: Record<string, string> = {
    queued: 'Queued at peer',
    downloading: 'Downloading',
    completed: 'Transferred',
    failed: 'Failed',
    cancelled: 'Cancelled'
  };

  function transferState(status: string) {
    return transferStates[status] ?? status;
  }

  // The same five words inside a sentence about an earlier try, so they are
  // lowered rather than rewritten.
  function attemptOutcome(outcome: string) {
    return transferState(outcome).toLowerCase();
  }
</script>

<svelte:head>
  <title>{tab === 'uploads' ? 'Uploads' : tab === 'wanted' ? 'Wishlist' : 'Downloads'} · Schall</title>
</svelte:head>

<!-- Hidden: the mast highlight says where you are, but that highlight is not
     a document heading. Nothing visible here repeats it. -->
<h1 class="sr-only">Downloads</h1>

<ControlRail label="Incoming">
  <Segmented
    options={tabs}
    value={tab}
    onchange={(value) => (tab = value as (typeof tabKeys)[number])}
    label="Incoming"
    pending={$downloads.isPending || $looking.isPending || $uploads.isPending}
  />

  {#if tab === 'peers'}
    <!-- The search narrows the peers list, which is the one drawn on this
         page; the wishlist and uploads tabs are their own components with no
         search of their own to hand it to. -->
    <label class="field ml-auto flex w-full items-center gap-2 sm:w-56">
      <Search size={13} strokeWidth={2} class="shrink-0 text-ink-4" />
      <input
        bind:value={search}
        placeholder="Filter downloads"
        aria-label="Filter downloads"
        class="min-w-0 flex-1 bg-transparent font-sans text-meta text-ink outline-none placeholder:text-ink-4"
      />
    </label>
  {/if}
</ControlRail>

{#if tab === 'wanted'}
  <WantsTab />
{:else if tab === 'uploads'}
  <UploadsTab />
{:else}
  <ControlRail label="Filter by status" sticky={false}>
    <Segmented
      options={views}
      value={view}
      onchange={(value) => setView(value as DownloadView)}
      label="Filter by status"
      pending={$downloads.isPending}
    />
  </ControlRail>

  <div class="layout-width flex flex-col gap-4 px-4 sm:px-6 py-5">
    {#if $downloads.isError}
      <!-- It is the list that failed, so the failure stands where the list
           would have been rather than above it. -->
      <ErrorNote error={$downloads.error} retry={() => void $downloads.refetch()} />
    {:else}
      <Settle pending={$downloads.isPending}>
        {#snippet placeholder()}
      <!-- Fill the visible viewport. The progress slot below is part of every
           transfer row, so these placeholders match the loaded shape. -->
      <div class="flex flex-col overflow-hidden" style="max-height: calc(100dvh - 11rem)">
        {#each Array(40) as _, placeholderIndex (placeholderIndex)}
          <div class="border-b border-line-thin px-3 py-2 last:border-b-0">
            <div class="h-5 animate-pulse rounded-row bg-surface-regular"></div>
            <div class="mt-1 h-4 w-2/3 animate-pulse rounded-row bg-surface-regular"></div>
            <div class="mt-2 h-1.5 animate-pulse rounded-full bg-surface-regular"></div>
            <div class="mt-1 h-4 w-1/3 animate-pulse rounded-row bg-surface-regular"></div>
          </div>
        {/each}
      </div>
        {/snippet}
    {#if filteredItems.length}
      <div class="flex flex-col">
        {#each filteredItems as item (item.id)}
          {@const role = stateRole(item)}
          {@const mark = dotRole(item)}
          {@const decide = decideHref(item)}
          {@const failures = rowErrors[item.id] ?? []}
          {@const showProgress = item.status === 'started' && !waiting(item)}
          <article
            class="border-b border-line-thin last:border-b-0 {item.status === 'cancelled'
              ? 'opacity-55'
              : ''}"
          >
            <!-- Every column after the name is a fixed width. Each row is its
                 own grid, so an `auto` actions column resolved the columns
                 differently per row — `Retry 1 file` is wider than `Cancel`,
                 which pushed that row's chip and figure left of the row above
                 and left a state column that wandered down the page. Only the
                 name flexes now, so the other four line up. -->
            <div
              class="flex flex-wrap items-center gap-x-3 gap-y-2 rounded-row px-3 py-2 transition hover:bg-surface-thick md:grid md:grid-cols-[1rem_minmax(0,1fr)_128px_72px_184px] md:gap-y-0"
            >
              <!-- No badge for the score. It measures how good a copy is and knows
                   nothing about which release it holds, so a green number here read
                   as a verdict it cannot give; the state mark leads the row instead. -->
              <StateMark role={mark}>
                {#if mark === 'ok'}
                  <Check size={10} strokeWidth={3.2} />
                {:else}
                  {mark === 'decide' ? '?' : mark === 'fail' ? '!' : mark === 'busy' ? '·' : '–'}
                {/if}
              </StateMark>

              <!-- On a phone the name takes the whole line and everything after
                   it wraps underneath. Sharing one line with the state, the
                   size and the buttons left it eight characters wide, so a
                   screen of requests read as `Ninajirachi…` over and over with
                   no way to tell one from the next. -->
              <span class="min-w-0 flex-1 basis-full md:basis-auto">
                {#if item.albumId}
                  <a
                    href={`/releases/${item.albumId}`}
                    class="tap-tall block truncate text-body font-medium text-ink transition hover:text-accent-soft"
                  >
                    {rowHeading(item)}{#if rowSubject(item)}{' '}<span class="font-normal text-ink-3">· {rowSubject(item)}</span>{/if}
                  </a>
                {:else}
                  <span class="block truncate text-body font-medium text-ink">
                    {rowHeading(item)}{#if rowSubject(item)}{' '}<span class="font-normal text-ink-3">· {rowSubject(item)}</span>{/if}
                  </span>
                {/if}
                <span class="mt-0.5 block truncate text-meta text-ink-3" title={subLine(item)}>
                  {subLine(item)}
                </span>
              </span>

              <span class="flex min-w-0 items-center">
                {#if role !== 'ok'}
                  <!-- The tick above already said "in the library"; a tag
                       beside it would say the same thing twice. -->
                  <StateTag
                    tone={tagTone(item)}
                    class="shrink-0"
                    aria-live="polite"
                    aria-atomic="true"
                  >
                    {statusLabel(item)}
                  </StateTag>
                {/if}
              </span>

              <span class="numeric whitespace-nowrap text-right text-meta text-ink-2">
                {formatBytes(item.totalSizeBytes)}
              </span>

              <span class="ml-auto flex shrink-0 items-center gap-1.5 md:ml-0 md:justify-end">
                {#if item.startable}
                  <Button
                    variant="outline"
                    size="sm"
                    disabled={$startDownload.isPending}
                    onclick={() => $startDownload.mutate({ requestId: item.id })}
                  >
                    <Play size={12} /> Start
                  </Button>
                {:else if item.status === 'started'}
                  <!-- The counter is a second reading of the tag whenever it
                       has nothing of its own to add. A row queued at the peer
                       with one file to come reads "Queued at peer" and then
                       "0/1 files", which is the same sentence twice; on a page
                       of thirty-four such rows it is the same sentence
                       sixty-eight times. It speaks once some of the files have
                       arrived, or once there is more than one to count. -->
                  {@const counted =
                    item.progress.completedCount > 0 || item.progress.transferCount > 1}
                  <span class="flex items-center gap-1.5 text-meta text-busy">
                    <!-- A spinner beside a request nobody has started sending
                         claims motion there is none of. -->
                    {#if !waiting(item)}<LoaderCircle size={12} class="animate-spin" />{/if}
                    {#if counted}
                      <span class="numeric whitespace-nowrap">
                        {item.progress.completedCount}/{item.progress.transferCount} files
                      </span>
                    {/if}
                  </span>
                {:else if item.retryableCount}
                  <Button
                    variant="outline"
                    size="sm"
                    disabled={$retryDownload.isPending}
                    onclick={() => $retryDownload.mutate({ requestId: item.id })}
                  >
                    <RotateCcw size={12} />
                    Retry {item.retryableCount === 1 ? '1 file' : `${item.retryableCount} files`}
                  </Button>
                {/if}
                {#if role === 'decide' && decide}
                  <Button href={decide} variant="outline" size="sm">Decide</Button>
                {/if}
                {#if item.status === 'requested' || item.status === 'started'}
                  <Button
                    variant="ghost"
                    size="sm"
                    disabled={$cancelDownload.isPending}
                    onclick={() => $cancelDownload.mutate(item.id)}
                  >
                    Cancel
                  </Button>
                {/if}
                <Button
                  variant="ghost"
                  icon
                  size="xs"
                  title="Show requested files"
                  aria-label="Show requested files"
                  onclick={() => (expanded = expanded === item.id ? null : item.id)}
                >
                  <ChevronDown size={14} class={expanded === item.id ? 'rotate-180' : ''} />
                </Button>
              </span>

              <!-- Reserve the progress block on every row. A requested transfer
                   fills it after Start; a settled row keeps the same height.
                   Indented past the mark, so the fill reads as the row's own
                   rather than a bar for the whole width of the list. -->
              <span class="mt-2 w-full md:col-span-4 md:col-start-2" aria-hidden={!showProgress}>
                <span
                  role="progressbar"
                  aria-label={`Download progress for ${downloadName(item)}`}
                  aria-valuenow={transferred(item)}
                  aria-valuemin="0"
                  aria-valuemax="100"
                  class="block h-1.5 overflow-hidden rounded-full bg-surface-thick {showProgress
                    ? ''
                    : 'invisible'}"
                >
                  <span
                    class="block h-full rounded-full bg-busy transition-[width]"
                    style={`width: ${transferred(item)}%`}
                  ></span>
                </span>
                <span
                  class="numeric mt-1 block whitespace-nowrap text-meta text-ink-4 {showProgress ? '' : 'invisible'}"
                >
                  {#if showProgress}
                    {formatBytes(item.progress.transferredBytes)} of {formatBytes(
                      item.totalSizeBytes
                    )}{#if item.progress.failedCount}
                      · {item.progress.failedCount} failed{/if}
                  {/if}
                </span>
              </span>
            </div>

            {#each failures as failure, failureIndex (failureIndex)}
              {@render RowProblem(failure)}
            {/each}

            {#if duplicateQuestion && duplicateQuestion.requestId === item.id}
              <div class="mx-3 mb-2">
                <DuplicateNotice
                  evidence={duplicateQuestion.evidence}
                  pending={$startDownload.isPending}
                  onconfirm={() =>
                    duplicateQuestion &&
                    $startDownload.mutate({
                      requestId: duplicateQuestion.requestId,
                      acknowledged: true
                    })}
                  oncancel={() => (duplicateQuestion = null)}
                />
              </div>
            {/if}

            {#if sourceQuestion && sourceQuestion.requestId === item.id}
              <div class="mx-3 mb-2 rounded-row border border-line-thin bg-decide/14 px-3 py-2.5">
                <p class="text-body font-semibold text-decide">The peer's folder has changed</p>
                {#if sourceQuestion.offer.missing.length}
                  <p class="mt-1.5 text-meta leading-relaxed text-ink-2">
                    No longer offered: {sourceQuestion.offer.missing.join(', ')}
                  </p>
                {/if}
                {#if sourceQuestion.offer.changed.length}
                  <p class="mt-1.5 text-meta leading-relaxed text-ink-2">
                    Offered at a different size: {sourceQuestion.offer.changed.join(', ')}
                  </p>
                {/if}
                <p class="mt-1.5 text-meta text-ink-3">
                  Starting anyway takes the files that are still there; the rest will fail.
                </p>
                <div class="mt-2.5 flex gap-2">
                  <Button
                    size="sm"
                    disabled={$startDownload.isPending}
                    onclick={() =>
                      sourceQuestion &&
                      $startDownload.mutate({
                        requestId: sourceQuestion.requestId,
                        sourceAcknowledged: true
                      })}
                  >
                    Start anyway
                  </Button>
                  <Button variant="ghost" size="sm" onclick={() => (sourceQuestion = null)}>
                    Leave it
                  </Button>
                </div>
              </div>
            {/if}

            {#if item.peerQuestion}
              {@const question = item.peerQuestion}
              {@const draft = peerReplies[item.id] ?? ''}
              <!-- The peers that hold a transfer until a word is typed back are
                   answered by Schall where the message is one it knows. This is
                   what is left, and not all of it is a question — so the peer's
                   own words are what it says, and the answer is a person's. -->
              <div class="mx-3 mb-2 rounded-row border border-line-thin bg-decide/14 px-3 py-2.5">
                <p class="text-body text-ink-1" title={question.message}>
                  Peer {question.username} wrote: &ldquo;{clipped(question.message)}&rdquo;
                </p>
                <p class="mt-1.5 text-meta text-ink-3">
                  Schall could not read this. If it asks for something, type it and press Send.
                </p>
                <form
                  class="mt-2.5 flex gap-2"
                  onsubmit={(event) => {
                    event.preventDefault();
                    if (!draft.trim()) return;
                    $replyToPeer.mutate({
                      requestId: item.id,
                      username: question.username,
                      message: draft.trim()
                    });
                  }}
                >
                  <label class="sr-only" for={`peer-reply-${item.id}`}>
                    Reply to {question.username}
                  </label>
                  <input
                    id={`peer-reply-${item.id}`}
                    class="field w-full"
                    type="text"
                    maxlength="500"
                    autocomplete="off"
                    value={draft}
                    oninput={(event) =>
                      (peerReplies = {
                        ...peerReplies,
                        [item.id]: event.currentTarget.value
                      })}
                    disabled={$replyToPeer.isPending}
                  />
                  <Button
                    type="submit"
                    size="sm"
                    disabled={!draft.trim() || $replyToPeer.isPending}
                  >
                    Send
                  </Button>
                </form>
              </div>
            {/if}

            {#if expanded === item.id}
              <!-- The detail opens inside the row's own hairline and at its own
                   inset, so the row above it is the border. -->
              <div class="mx-3 mb-2 flex flex-col gap-3 rounded-row bg-surface-regular px-3 py-2.5">
                <!-- Why it stopped, and what to do about it, live behind the
                     chevron. A row that has settled states its outcome in the
                     chip; six open paragraphs down a list state nothing at all,
                     because nobody reads the seventh. -->
                {#if item.error}
                  <div class="flex items-start gap-2 rounded-row bg-fail/14 px-2.5 py-2">
                    <StateMark role="fail">!</StateMark>
                    <ErrorNote bare error={item.error} />
                  </div>
                {/if}

                <!-- Downloads reports what became of a transfer and stops
                     there. Every decision about an import is made in Review,
                     which is the whole point of there being one queue, so the
                     pause is stated here and handed over. -->
                {#if item.importError || item.revalidatable}
                  {@const unsettled = (item.importEvidence?.files ?? []).filter(
                    (file) => file.problems.length
                  ).length}
                  <div class="rounded-row bg-decide/14 px-3 py-2.5">
                    {#if item.importError}
                      <ErrorNote bare error={item.importError} />
                    {/if}
                    {#if item.importEvidence}
                      <!-- Decide is on the row itself now; this stays for what
                           the button does not say. -->
                      {#if unsettled}
                        <p class="text-meta text-decide/70">
                          {unsettled === 1 ? '1 file is' : `${unsettled} files are`} waiting on a decision.
                        </p>
                      {/if}
                    {:else if item.acquisitionTargetId}
                      <!-- One file fetched for a want. What it turned out to be is
                           recorded on the copy rather than on this request, so the
                           decision is asked under the want and this row does not
                           repeat it. Checking again is worth having here all the
                           same: the file is still in the download folder, so it
                           costs a re-read rather than another transfer. -->
                      <p class="mt-1.5 text-meta text-decide/70">
                        Schall fetched this copy for a recording you want.
                      </p>
                      <div class="mt-2.5 flex flex-wrap items-center gap-2">
                        <Button
                          href="/review?want={item.acquisitionTargetId}"
                          variant="outline"
                          size="xs"
                        >
                          Decide in Review
                        </Button>
                        {@render ValidateAgain(item)}
                      </div>
                    {:else}
                      <!-- Nothing was compared, so nobody is being asked anything.
                           The import ran out of attempts, and running it again is
                           the only thing left to try — which makes it an action on
                           a failure rather than a question, and it belongs here
                           beside the other retries. -->
                      <p class="mt-1.5 text-meta text-decide/70">
                        It stopped before it could compare any files, so there is nothing to
                        decide.
                      </p>
                      <div class="mt-2.5">{@render ValidateAgain(item)}</div>
                    {/if}
                  </div>
                {/if}

                <p class="numeric truncate text-meta text-ink-3">
                  {item.directory || 'No folder reported'}
                </p>

                {#if item.reasons.length}
                  <div class="flex flex-wrap gap-1.5">
                    {#each item.reasons as reason (reason)}
                      <Chip>{reason}</Chip>
                    {/each}
                  </div>
                {/if}

                {#if item.importReviews.length}
                  <div class="flex flex-col gap-1.5">
                    <div class="flex items-center gap-2.5">
                      <span class="label">Import history</span>
                      <span class="h-px flex-1 bg-line-thin"></span>
                    </div>
                    <ol class="flex flex-col gap-1">
                      {#each item.importReviews as review, reviewIndex (reviewIndex)}
                        <li class="flex flex-wrap items-baseline gap-x-2 text-meta text-ink-3">
                          <span
                            class="text-meta font-medium {review.kind === 'paused' ||
                            review.kind === 'source_retained'
                              ? 'text-decide'
                              : review.kind === 'imported'
                                ? 'text-ok'
                                : 'text-busy'}"
                          >
                            {reviewLabels[review.kind]}
                          </span>
                          <span class="numeric text-ink-4">{requestedOn(review.recordedAt)}</span>
                          {#if review.detail}<span class="w-full text-ink-2">{review.detail}</span>{/if}
                        </li>
                      {/each}
                    </ol>
                  </div>
                {/if}

                <div class="flex max-h-64 flex-col gap-1 overflow-auto">
                  {#each item.files as file (file.path ?? file.name)}
                    <div class="flex items-center gap-3 text-meta text-ink-3">
                      <span class="numeric min-w-0 flex-1 truncate text-meta text-ink-2">
                        {file.name}
                      </span>
                      {#if file.transfer}
                        <span class="shrink-0 {transferTones[file.transfer.status] ?? 'text-ink-3'}">
                          {transferState(file.transfer.status)}
                        </span>
                        {#if file.transfer.attempt > 1}
                          <span
                            class="numeric shrink-0 text-ink-4"
                            title="This file has been asked for {file.transfer.attempt} times"
                          >
                            attempt {file.transfer.attempt}
                          </span>
                        {/if}
                      {/if}
                      <span class="numeric shrink-0 whitespace-nowrap">{formatBytes(file.sizeBytes)}</span>
                      {#if file.transfer?.retryable}
                        {@const path = file.transfer.path}
                        <Button
                          variant="ghost"
                          size="xs"
                          tall
                          class="text-accent-soft hover:text-accent"
                          disabled={$retryDownload.isPending}
                          onclick={() => $retryDownload.mutate({ requestId: item.id, paths: [path] })}
                        >
                          Retry
                        </Button>
                      {/if}
                    </div>
                    {#if file.transfer?.status === 'failed' && file.transfer.error}
                      <ErrorNote bare error={file.transfer.error} />
                    {/if}
                    {#if file.transfer}
                      {@const earlier = file.transfer.attempts.filter(
                        (a) => a.attempt < file.transfer!.attempt
                      )}
                      {#if earlier.length}
                        <ol class="pl-3 text-meta text-ink-4">
                          {#each earlier as attempt (attempt.attempt)}
                            <li class="truncate">
                              Attempt {attempt.attempt}
                              {attemptOutcome(attempt.outcome)}{attempt.detail
                                ? ` — ${attempt.detail}`
                                : ''}
                            </li>
                          {/each}
                        </ol>
                      {/if}
                    {/if}
                  {/each}
                </div>
              </div>
            {/if}
          </article>
        {/each}
      </div>

    {:else if nothingMatches}
      <EmptyPanel role="idle" heading={emptyHeading}>
        <div>
          <Button variant="outline" onclick={() => (search = '')}>Clear search</Button>
        </div>
      </EmptyPanel>
    {:else}
      <!-- The empty pile has a direct route to the catalogue, where a reader
           can search for a release and start a download. -->
      <EmptyPanel role="idle" heading={emptyHeading}>
        <div>
          <Button href="/releases" variant="outline">Search releases</Button>
        </div>
      </EmptyPanel>
    {/if}
      </Settle>
    {/if}
  </div>
{/if}

<!-- A failure belongs to the row whose button raised it, under that row and
     inside its hairline. -->
{#snippet RowProblem(failure: unknown)}
  <!-- The band is the row's own, so the note is drawn bare inside it: the tint
       and the mark are already here, and a second band inside the first says
       nothing the first has not said. -->
  <div class="mx-3 mb-2 flex items-start gap-2 rounded-row border border-line-thin bg-fail/14 px-2.5 py-2">
    <StateMark role="fail">!</StateMark>
    <ErrorNote bare error={failure} />
  </div>
{/snippet}

<!-- Sending an import back through validation. The same control whether the
     import gave up or a fetched copy is waiting on somebody: it reads what is
     already in the download folder rather than asking a peer for it again. -->
{#snippet ValidateAgain(item: DownloadRequest)}
  <Button
    variant="outline"
    size="xs"
    disabled={!item.revalidatable || $revalidateImport.isPending}
    onclick={() => $revalidateImport.mutate(item.id)}
  >
    Check again
  </Button>
{/snippet}

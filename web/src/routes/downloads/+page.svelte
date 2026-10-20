<script lang="ts">
  import Skeleton from '$lib/components/Skeleton.svelte';
  import { tick, untrack } from 'svelte';
  import { toStore } from 'svelte/store';
  import {
    createInfiniteQuery,
    createMutation,
    createQuery,
    type InfiniteData,
    keepPreviousData,
    type Query,
    useQueryClient
  } from '@tanstack/svelte-query';
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
  import { formatBytes, keepInUrl, urlChoice, urlText } from '$lib/utils';
  import Button from '$lib/components/Button.svelte';
  import PageHeader from '$lib/components/PageHeader.svelte';
  import Icon from '$lib/components/Icon.svelte';
  import Chip from '$lib/components/Chip.svelte';
  import EmptyPanel from '$lib/components/EmptyPanel.svelte';
  import ErrorNote from '$lib/components/ErrorNote.svelte';
  import DuplicateNotice from '$lib/components/DuplicateNotice.svelte';
  import { progress } from '$lib/vocabulary';
  import Segmented from '$lib/components/Segmented.svelte';
  import Settle from '$lib/components/Settle.svelte';
  import StateMark from '$lib/components/StateMark.svelte';
  import UploadsTab from '$lib/components/UploadsTab.svelte';
  import WantsTab from '$lib/components/WantsTab.svelte';
  import { useKeys } from '$lib/keys.svelte';
  import { glide, slideInto } from '$lib/motion.svelte';
  import { arrivals, leaveMs, listCursor, settleDelays, strike } from '$lib/live-list.svelte';
  import DownloadRow from './DownloadRow.svelte';
  import { DOWNLOAD_COLUMNS, nextAction, requestedOn, rowHeading, rowSubject } from './download-rows';

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
  let tab = $state<(typeof tabKeys)[number]>(tabFrom(opened));

  // `wishlist` is what the tab is called on screen, so an address that names it
  // as a view or a tab opens it rather than falling back to the transfers.
  function tabFrom(url: URL): (typeof tabKeys)[number] {
    const named = url.searchParams.get('tab') ?? url.searchParams.get('view');
    if (named === 'wishlist' || named === 'wanted') return 'wanted';
    return urlChoice(url, 'tab', tabKeys, 'peers');
  }

  // The screen opens on the transfers still under way, and the piles they end
  // up in are beside it rather than under an "everything else" button. Three
  // questions live in that pile — was my copy imported, was it discarded, did
  // it fail — and one button cannot answer them; it would also put every
  // request back in the page, which is the thing being fixed.
  const viewKeys = ['open', 'review', 'imported', 'discarded', 'failed', 'all'] as const;
  const PAGE = 50;
  const SEARCH_PAUSE_MS = 250;
  let view = $state<DownloadView>(urlChoice(opened, 'view', viewKeys, 'open'));

  // What the reader typed to narrow the list. The pile is read a page at a
  // time, so the search is asked of the server once typing pauses; until then
  // the rows already loaded are narrowed in place.
  let search = $state(urlText(opened, 'q'));
  let asked = $state(urlText(opened, 'q').trim());
  $effect(() => {
    const next = search.trim();
    const timer = setTimeout(() => (asked = next), SEARCH_PAUSE_MS);
    return () => clearTimeout(timer);
  });

  $effect(() => {
    keepInUrl(opened.pathname, { tab, view, q: search }, { tab: 'peers', view: 'open', q: '' });
  });

  // Following the navigation badge lands here with the open pile named, from
  // whichever pile was last looked at. This depends on the address and on
  // nothing else: the pile it compares against is read untracked, so the effect
  // above, which writes the address, cannot make this one answer it.
  // The badge links to the transfers, so a view named with no tab beside it
  // brings the transfers forward from the wishlist or the uploads.
  $effect(() => {
    const asked = urlChoice(page.url, 'view', viewKeys, 'open');
    if (asked !== untrack(() => view)) setView(asked);
    const named = page.url.searchParams.get('view') ?? '';
    if (!page.url.searchParams.has('tab') && (viewKeys as readonly string[]).includes(named)) {
      if (untrack(() => tab) !== 'peers') tab = 'peers';
    }
  });

  // The pile and the page are what the key names, because each of them is a
  // different question with a different answer. Filing all six piles under one
  // key and refetching after the press did nothing at all while the first
  // request was still in flight — the refetch joined the request already
  // running, which had been built with the pile the screen opened on, so the
  // strip said Imported over the open transfers.
  // Imported and All hold thousands of rows, and reading a whole pile at once
  // sent megabytes for the first screenful. The pile is read PAGE rows at a
  // time, and the next page as the reader nears the end of the list.
  // "Needs review" is the pile Review asks about, read by Review's own rule
  // (the `questions` view): a paused import that recorded what it compared.
  // The wider `review` view also held every copy whose want Review shows as
  // one card, and a copy that stopped before comparing anything, so the chip
  // said 1,449 beside a Review of 230.
  const serverView = (name: DownloadView) => (name === 'review' ? 'questions' : name);
  const downloads = createInfiniteQuery(
    toStore(() => ({
      queryKey: ['downloads', 'list', view, asked],
      queryFn: ({ pageParam }: { pageParam: number }) =>
        api.downloads({ view: serverView(view), q: asked || undefined, limit: PAGE, offset: pageParam }),
      initialPageParam: 0,
      getNextPageParam: (last: DownloadRequests) =>
        last.items.length > 0 && last.offset + last.items.length < last.total
          ? last.offset + last.items.length
          : undefined,
      placeholderData: keepPreviousData,
      // Transfers move on their own and the server says so over the event
      // stream, so this is the safety net rather than the mechanism: it covers a
      // stream blocked by a proxy or dropped without reconnecting yet. It still
      // stops entirely once all work has settled.
      refetchInterval: (query: Query<DownloadRequests, Error, InfiniteData<DownloadRequests, number>>) =>
        query.state.data?.pages.some((loaded) =>
          loaded.items.some(
            (item) =>
              item.status === 'started' ||
              (item.status === 'completed' &&
                (item.importStatus === 'pending' || item.importStatus === 'validating'))
          )
        )
          ? 20_000
          : false
    }))
  );
  // Rows can move between pages while the pile changes under them, so one
  // that arrives twice is kept once.
  const items = $derived.by(() => {
    const seen = new Map<string, DownloadRequest>();
    for (const loaded of $downloads.data?.pages ?? []) {
      for (const item of loaded.items) seen.set(item.id, item);
    }
    // The piles still in motion are read by what each row needs next; the
    // settled ones by when they were asked for. Either way the newest request
    // comes first among equals.
    const byNext = view === 'open' || view === 'review';
    return [...seen.values()].sort(
      (a, b) =>
        (byNext ? nextAction(a) - nextAction(b) : 0) ||
        b.requestedAt.localeCompare(a.requestedAt) ||
        a.id.localeCompare(b.id)
    );
  });
  const moreToRead = $derived(Boolean($downloads.hasNextPage));
  function readMore() {
    if ($downloads.hasNextPage && !$downloads.isFetchingNextPage) void $downloads.fetchNextPage();
  }
  // Reads the next page when the end of the list comes into view.
  function readAhead(node: HTMLElement) {
    if (typeof IntersectionObserver === 'undefined') return;
    const observer = new IntersectionObserver(
      (entries) => {
        if (entries.some((entry) => entry.isIntersecting)) readMore();
      },
      { rootMargin: '600px 0px' }
    );
    observer.observe(node);
    return { destroy: () => observer.disconnect() };
  }
  const counts = $derived($downloads.data?.pages[0]?.counts);

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
    if (next !== view) replaceList();
    view = next;
  }

  // Rows that leave because the list was swapped for another go on the cut,
  // and rows that appear with it do not slide in: only a row finishing or
  // arriving in the list the reader is looking at is news.
  let replacing = $state(false);
  let replacingTimer: ReturnType<typeof setTimeout> | undefined;
  function replaceList() {
    replacing = true;
    clearTimeout(replacingTimer);
    replacingTimer = setTimeout(() => (replacing = false), leaveMs() + 60);
  }

  // Whether the list had rows a moment ago, so the empty panel waits for the
  // last one to leave instead of arriving over it.
  let hadRows = $state(false);
  $effect(() => {
    if (filteredItems.length > 0) hadRows = true;
    else if (replacing) hadRows = false;
  });
  const lastRowLeft = $derived(hadRows && !replacing);

  const ids = $derived(filteredItems.map((item) => item.id));
  const cursor = listCursor(() => ids);
  const arrived = arrivals(
    () => ids,
    () => replacing
  );

  let rows = $state<HTMLElement>();
  // Rows slide to their new places over the arriving step, and the cursor block
  // measures them where they are, so it is placed again once they have landed.
  let settled = $state(0);
  $effect(() => {
    void ids.join('|');
    // Once the slide has landed, and once a leaving row has folded away.
    const timers = settleDelays().map((ms) => setTimeout(() => settled++, ms));
    return () => timers.forEach(clearTimeout);
  });
  function moveCursor(by: number) {
    cursor.move(by);
    void tick().then(() =>
      rows?.querySelector('[data-cursor="true"]')?.scrollIntoView?.({ block: 'nearest' })
    );
  }

  const tabOrder = ['wanted', 'peers', 'uploads'] as const;
  function stepTab(by: number) {
    replaceList();
    tab = tabOrder[(tabOrder.indexOf(tab) + by + tabOrder.length) % tabOrder.length];
  }
  function stepView(by: number) {
    const keys = viewKeys as readonly DownloadView[];
    setView(keys[Math.min(Math.max(keys.indexOf(view) + by, 0), keys.length - 1)]);
  }

  // Downloads' keys (ADR 0042). The row buttons register their own while the
  // row holds the cursor; these are the keys with no single button.
  useKeys(() => [
    {
      keys: ['[', ']'],
      caps: ['[', ']'],
      verb: 'tab',
      order: 1,
      run: (event) => stepTab(event.key === ']' ? 1 : -1)
    },
    ...(tab === 'peers'
      ? [
          {
            keys: ['j', 'k'],
            caps: ['j', 'k'],
            verb: 'move',
            order: 0,
            enabled: () => filteredItems.length > 0,
            run: (event: KeyboardEvent) => moveCursor(event.key.toLowerCase() === 'j' ? 1 : -1)
          },
          {
            keys: ['enter'],
            verb: 'files',
            order: 2,
            enabled: () => Boolean(cursor.id),
            run: () => (expanded = expanded === cursor.id ? null : cursor.id)
          },
          {
            keys: ['h', 'l'],
            caps: ['h', 'l'],
            verb: 'pile',
            order: 3,
            run: (event: KeyboardEvent) => stepView(event.key.toLowerCase() === 'l' ? 1 : -1)
          }
        ]
      : [])
  ]);

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
    { value: 'wanted', name: 'Wishlist', count: $looking.data ? $looking.data.total || null : undefined },
    { value: 'peers', name: 'From peers', count: counts ? counts.open || null : undefined },
    { value: 'uploads', name: 'Uploaded', count: $uploads.data ? openUploads || null : undefined }
  ]);

  // The name of each pile, in the words the rows in it already use. The counts
  // come from the same answer as the rows, so a figure here and the list under
  // it are never two different reads.
  const views = $derived([
    { value: 'open', name: 'Open', count: counts?.open },
    { value: 'review', name: 'Needs review', count: counts?.questions },
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
  const nothingMatches = $derived(
    Boolean(search.trim()) && filteredItems.length === 0 && (counts?.[serverView(view)] ?? 0) > 0
  );
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
    revalidated: 'Checked again',
    imported: 'Imported',
    resolved: 'File matched by hand',
    withdrawn: 'Hand match withdrawn',
    source_removed: 'Source files removed',
    source_retained: 'Source files kept',
    discarded: 'Copy discarded'
  };

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
    completed: 'text-ink-2',
    failed: 'text-fail',
    cancelled: 'text-ink-4',
    queued: 'text-ink-3',
    downloading: 'text-ink'
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

<PageHeader title="Downloads" />

<!-- The tabs, and the filter that narrows the list under them. While the field
     has focus, typing filters here and does not open the global search. -->
<div class="flex flex-wrap items-center gap-x-4 gap-y-2 px-5 pt-3">
  <Segmented
    options={tabs}
    value={tab}
    onchange={(value) => {
      replaceList();
      tab = value as (typeof tabKeys)[number];
    }}
    label="Incoming"
    variant="tabs"
    pending={$downloads.isPending || $looking.isPending || $uploads.isPending}
  />

  {#if tab !== 'uploads'}
    <!-- The search narrows the transfers drawn on this page, and is handed to
         the wishlist, which asks the server: it is paged, and a filter over
         the 25 rows on screen would miss the rest. -->
    <label class="field ml-auto flex w-full items-center gap-2 sm:w-56">
      <Icon name="search" size={13} class="shrink-0 text-ink-4" />
      <input
        bind:value={search}
        oninput={replaceList}
        type="search"
        spellcheck="false"
        autocomplete="off"
        placeholder={tab === 'wanted' ? 'Filter wishlist' : 'Filter downloads'}
        aria-label={tab === 'wanted' ? 'Filter wishlist' : 'Filter downloads'}
        class="min-w-0 flex-1 bg-transparent font-sans text-meta text-ink outline-none placeholder:text-ink-4"
      />
    </label>
  {/if}
</div>

{#if tab === 'wanted'}
  <WantsTab query={search} />
{:else if tab === 'uploads'}
  <UploadsTab />
{:else}
  <div class="px-5 pt-2.5">
    <Segmented
      options={views}
      value={view}
      onchange={(value) => setView(value as DownloadView)}
      label="Filter by status"
      variant="filter"
      pending={$downloads.isPending}
    />
  </div>

  <div class="flex flex-col gap-4 px-5 pt-3 pb-6">
    {#if $downloads.isError && !$downloads.data}
      <!-- It is the list that failed, so the failure stands where the list
           would have been rather than above it. -->
      <ErrorNote error={$downloads.error} retry={() => void $downloads.refetch()} />
    {:else}
      {#if $downloads.isError}
        <!-- A failed poll keeps the rows it has. Wiping a list of transfers
             for one slow answer read as the downloads having gone. -->
        <ErrorNote error={$downloads.error} retry={() => void $downloads.refetch()} />
      {/if}
      <Settle pending={$downloads.isPending}>
        {#snippet placeholder()}
          <div class="flex flex-col overflow-hidden" style="max-height: calc(100dvh - 11rem)">
            {#each Array(12) as _, placeholderIndex (placeholderIndex)}
              <div class="flex h-9 items-center gap-3 border-b border-rule px-3">
                <Skeleton shape="round" class="size-4 shrink-0" />
                <Skeleton class="h-4 flex-1" />
                <Skeleton class="h-3 w-24" />
              </div>
            {/each}
          </div>
        {/snippet}
          <!-- One grid for the header and every row, so the progress, the size
               and the buttons size to the widest row and still line up down the
               page. The cursor is one inverted block that glides under the
               rows. -->
          <div
            bind:this={rows}
            use:glide={{ key: `${cursor.at}:${filteredItems.length}:${expanded ?? ''}:${settled}` }}
            class="relative flex flex-col"
          >
            {#if filteredItems.length}
              <div
                class="label hidden border-b border-rule-strong px-3 md:grid md:gap-x-3 {DOWNLOAD_COLUMNS}"
                aria-hidden="true"
              >
                <span class="h-7"></span>
                <span class="flex h-7 items-center">Release</span>
                <span class="hidden h-7 items-center lg:flex">Source</span>
                <span class="flex h-7 items-center">Progress</span>
                <span class="flex h-7 items-center justify-end">Size</span>
                <span class="h-7"></span>
              </div>
            {/if}
            {#each filteredItems as item, index (item.id)}
              {@const failures = rowErrors[item.id] ?? []}
              <article
                class="flex flex-col {arrived.has(item.id) ? 'rise' : ''}"
                animate:slideInto
                out:strike={{ active: !replacing }}
              >
                <DownloadRow
                  {item}
                  here={index === cursor.at}
                  expanded={expanded === item.id}
                  ontoggle={() => (expanded = expanded === item.id ? null : item.id)}
                  onstart={() => $startDownload.mutate({ requestId: item.id })}
                  onretry={() => $retryDownload.mutate({ requestId: item.id })}
                  oncancel={() => $cancelDownload.mutate(item.id)}
                  starting={$startDownload.isPending}
                  retrying={$retryDownload.isPending}
                  cancelling={$cancelDownload.isPending}
                />

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
                  <div class="rise mx-3 mb-2 flex flex-col gap-1.5 bg-panel px-3 py-2.5">
                    <p class="flex items-center gap-2 text-body font-semibold text-ink">
                      <StateMark role="decide" />
                      The peer's folder changed
                    </p>
                    {#if sourceQuestion.offer.missing.length}
                      <p class="text-meta leading-relaxed text-ink-2">
                        No longer shared: {sourceQuestion.offer.missing.join(', ')}
                      </p>
                    {/if}
                    {#if sourceQuestion.offer.changed.length}
                      <p class="text-meta leading-relaxed text-ink-2">
                        Size changed: {sourceQuestion.offer.changed.join(', ')}
                      </p>
                    {/if}
                    <p class="text-meta text-ink-3">Start anyway to get what's left. The rest will fail.</p>
                    <div class="mt-1 flex gap-2">
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
                  <!-- The peers that hold a transfer until a word is typed back
                       are answered by Schall where the message is one it knows.
                       This is what is left, and not all of it is a question, so
                       the peer's own words are what it says, and the answer is a
                       person's. -->
                  <div class="rise mx-3 mb-2 flex flex-col gap-1.5 bg-panel px-3 py-2.5">
                    <p class="flex items-start gap-2 text-body text-ink" title={question.message}>
                      <StateMark role="decide" />
                      <span>Peer {question.username} wrote: &ldquo;{clipped(question.message)}&rdquo;</span>
                    </p>
                    <p class="text-meta text-ink-3">
                      Schall can't answer this. If it asks for something, type it and press Send.
                    </p>
                    <form
                      class="mt-1 flex gap-2"
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
                      <Button type="submit" size="sm" disabled={!draft.trim() || $replyToPeer.isPending}>
                        Send
                      </Button>
                    </form>
                  </div>
                {/if}

                {#if expanded === item.id}
                  <!-- The detail opens inside the row's own rule and at its own
                       inset, so the row above it is the border. -->
                  <div class="rise mx-3 mb-2 flex flex-col gap-3 bg-panel px-3 py-2.5">
                    <!-- Why it stopped, and what to do about it, live behind the
                         chevron. A row that has settled states its outcome in its
                         mark; six open paragraphs down a list state nothing at
                         all, because nobody reads the seventh. -->
                    {#if item.error}
                      <div class="flex items-start gap-2">
                        <StateMark role="fail" />
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
                      <div class="flex flex-col gap-1.5">
                        {#if item.importError}
                          <div class="flex items-start gap-2">
                            <StateMark role="decide" />
                            <ErrorNote bare error={item.importError} />
                          </div>
                        {/if}
                        {#if item.importEvidence}
                          {#if unsettled}
                            <p class="text-meta text-ink-3">
                              {unsettled === 1 ? '1 file needs' : `${unsettled} files need`} a decision.
                            </p>
                          {/if}
                        {:else if item.acquisitionTargetId}
                          <!-- One file fetched for a want. What it turned out to
                               be is recorded on the copy rather than on this
                               request, so the decision is asked under the want.
                               Checking again is worth having here all the same:
                               the file is still in the download folder, so it
                               costs a re-read rather than another transfer. -->
                          <p class="text-meta text-ink-3">Downloaded for a song on your wishlist.</p>
                          <div class="flex flex-wrap items-center gap-2">
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
                          <!-- Nothing was compared, so nobody is being asked
                               anything. The import ran out of attempts, and
                               running it again is the only thing left to try. -->
                          <p class="text-meta text-ink-3">
                            Stopped before checking any files. Nothing to decide.
                          </p>
                          <div>{@render ValidateAgain(item)}</div>
                        {/if}
                      </div>
                    {/if}

                    <p class="mono truncate text-micro text-ink-3">
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
                          <span class="h-px flex-1 bg-rule"></span>
                        </div>
                        <ol class="flex flex-col gap-1">
                          {#each item.importReviews as review, reviewIndex (reviewIndex)}
                            <li class="flex flex-wrap items-baseline gap-x-2 text-meta text-ink-3">
                              <span class="text-meta font-medium text-ink-2">
                                {reviewLabels[review.kind]}
                              </span>
                              <span class="mono text-micro text-ink-4">{requestedOn(review.recordedAt)}</span>
                              {#if review.detail}<span class="w-full text-ink-2">{review.detail}</span>{/if}
                            </li>
                          {/each}
                        </ol>
                      </div>
                    {/if}

                    <div class="flex max-h-64 flex-col gap-1 overflow-auto">
                      {#each item.files as file (file.path ?? file.name)}
                        <div class="flex items-center gap-3 text-meta text-ink-3">
                          <span class="mono min-w-0 flex-1 truncate text-micro text-ink-2">
                            {file.name}
                          </span>
                          {#if file.transfer}
                            <span class="shrink-0 {transferTones[file.transfer.status] ?? 'text-ink-3'}">
                              {transferState(file.transfer.status)}
                            </span>
                            {#if file.transfer.attempt > 1}
                              <span
                                class="mono shrink-0 text-micro text-ink-4"
                                title="Requested {file.transfer.attempt} times"
                              >
                                attempt {file.transfer.attempt}
                              </span>
                            {/if}
                          {/if}
                          <span class="mono shrink-0 whitespace-nowrap text-micro">{formatBytes(file.sizeBytes)}</span>
                          {#if file.transfer?.retryable}
                            {@const path = file.transfer.path}
                            <Button
                              variant="ghost"
                              size="xs"
                              tall
                              class="text-ink hover:text-ink"
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
                                    ? `: ${attempt.detail}`
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
          {#if moreToRead}
            <div use:readAhead class="flex justify-center pt-3">
              <Button variant="outline" disabled={$downloads.isFetchingNextPage} onclick={readMore}>
                Show more
              </Button>
            </div>
          {/if}
          {#if filteredItems.length === 0}
            <!-- The list stays mounted so its last row can strike and fold away;
                 the panel waits for it. -->
            <div class="rise" style={lastRowLeft ? `animation-delay: ${leaveMs()}ms` : ''}>
              {#if nothingMatches}
                <EmptyPanel role="idle" heading={emptyHeading}>
                  <div>
                    <Button variant="outline" onclick={() => (search = '')}>Clear search</Button>
                  </div>
                </EmptyPanel>
              {:else}
                <!-- The empty pile has a direct route to the catalogue, where a
                     reader can search for a release and start a download. -->
                <EmptyPanel role="idle" heading={emptyHeading}>
                  <div>
                    <Button href="/releases" variant="outline">Search releases</Button>
                  </div>
                </EmptyPanel>
              {/if}
            </div>
          {/if}
      </Settle>
    {/if}
  </div>
{/if}

<!-- A failure belongs to the row whose button raised it, under that row and
     inside its rule. -->
{#snippet RowProblem(failure: unknown)}
  <!-- The row's own band, so the note is drawn bare inside it. -->
  <div class="rise mx-3 mb-2 flex items-start gap-2 bg-panel px-2.5 py-2">
    <StateMark role="fail" />
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

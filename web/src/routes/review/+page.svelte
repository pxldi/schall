<script lang="ts">
  import Icon from '$lib/components/Icon.svelte';
  import {
    createInfiniteQuery,
    createMutation,
    keepPreviousData,
    useQueryClient
  } from '@tanstack/svelte-query';
  import { tick, untrack } from 'svelte';
  import { goto } from '$app/navigation';
  import { page } from '$app/state';
  import { api, type AcquisitionCandidate } from '$lib/api';
  import {
    REASONS,
    clock,
    contradicted,
    groupByAlbum,
    groupCopies,
    isCreditOnlyStopped,
    namesDiffer,
    questionsFrom,
    reasonOf,
    waitingLabel,
    type CopyColumn,
    type ReasonKey
  } from '$lib/review';
  import Button from '$lib/components/Button.svelte';
  import Hero from '$lib/components/Hero.svelte';
  import Cover from '$lib/components/Cover.svelte';
  import { coverSrc, usePagePrint } from '$lib/duoton';
  import AlbumBatch from '$lib/components/AlbumBatch.svelte';
  import CopyTable from '$lib/components/CopyTable.svelte';
  import ReviewPlayer from '$lib/components/ReviewPlayer.svelte';
  import EmptyPanel from '$lib/components/EmptyPanel.svelte';
  import Settle from '$lib/components/Settle.svelte';
  import Skeleton from '$lib/components/Skeleton.svelte';
  import SkeletonRows from '$lib/components/SkeletonRows.svelte';
  import ErrorNote from '$lib/components/ErrorNote.svelte';
  import ImportEvidence from '$lib/components/ImportEvidence.svelte';
  import VersionCandidates from '$lib/components/VersionCandidates.svelte';
  import UseAddress from '$lib/components/UseAddress.svelte';

  const client = useQueryClient();

  // A file named in the address bar used to open Review on an identity, a
  // match or a duplicate question. Those three moved to Library, so the link
  // moves with them: identity and match behind the files filter there, a
  // duplicate in the held-twice view. The `want=` and `download=` handoffs
  // from Downloads still open this page, because those two questions still
  // live here.
  $effect(() => {
    const id = page.url.searchParams.get('file');
    if (!id) return;
    let live = true;
    void api
      .libraryFiles({ id })
      .then((result) => {
        if (!live) return;
        const target =
          result.items[0]?.matchStatus === 'duplicate'
            ? '/library?view=duplicates'
            : `/library?view=files&id=${encodeURIComponent(id)}`;
        void goto(target, { replaceState: true });
      })
      .catch(() => {
        if (live) void goto(`/library?view=files&id=${encodeURIComponent(id)}`, { replaceState: true });
      });
    return () => {
      live = false;
    };
  });

  // Both reads are paged, 100 at a time, the most the server gives in one
  // read. The rail counts by reason, which only the loaded questions can say,
  // so the pages are read on by themselves up to `EAGER` questions; past that
  // the next page is read as the reader nears the end (`readAhead` below).
  const PAGE = 100;
  const EAGER = 1000;
  const nextOffset = (last: { total: number; offset: number; items: unknown[] }) =>
    last.items.length > 0 && last.offset + last.items.length < last.total
      ? last.offset + last.items.length
      : undefined;

  const wants = createInfiniteQuery({
    queryKey: ['review-queue'],
    queryFn: ({ pageParam }) => api.reviewQueue(PAGE, pageParam),
    initialPageParam: 0,
    getNextPageParam: nextOffset,
    placeholderData: keepPreviousData
  });
  // Only the folders that recorded what they found. One that gave up before it
  // could compare anything is a failure rather than a question, and the
  // server leaves those out itself (the `questions` view).
  const downloads = createInfiniteQuery({
    queryKey: ['downloads', 'questions'],
    queryFn: ({ pageParam }) => api.downloads({ view: 'questions', limit: PAGE, offset: pageParam }),
    initialPageParam: 0,
    getNextPageParam: nextOffset,
    placeholderData: keepPreviousData
  });

  const aggregateFetching = $derived([$wants.isFetching, $downloads.isFetching].some(Boolean));
  let initialLoadComplete = $state(false);
  $effect(() => {
    if (!aggregateFetching) initialLoadComplete = true;
  });
  const loading = $derived(!initialLoadComplete);
  const loadError = $derived(($wants.error ?? $downloads.error) as Error | undefined);

  function retryQueue() {
    void $wants.refetch();
    void $downloads.refetch();
  }

  const paused = $derived(
    ($downloads.data?.pages.flatMap((page) => page.items) ?? []).filter(
      (item) => item.importStatus === 'needs_review'
    )
  );

  // A question that moved between two pages while they were read would be
  // listed twice. The first sighting is kept.
  const questions = $derived.by(() => {
    const seen = new Set<string>();
    return questionsFrom({
      wants: $wants.data?.pages.flatMap((page) => page.items) ?? [],
      imports: paused
    }).filter((question) => !seen.has(question.id) && Boolean(seen.add(question.id)));
  });

  const hasQueue = $derived(questions.length > 0);
  const pageError = $derived(hasQueue ? undefined : loadError);

  const total = $derived(
    ($wants.data?.pages[0]?.total ?? 0) + ($downloads.data?.pages[0]?.total ?? 0)
  );
  const moreToRead = $derived(Boolean($wants.hasNextPage || $downloads.hasNextPage));

  /** Reads the next page of whichever list has one. */
  function readAhead() {
    if ($wants.hasNextPage && !$wants.isFetchingNextPage) void $wants.fetchNextPage();
    if ($downloads.hasNextPage && !$downloads.isFetchingNextPage) void $downloads.fetchNextPage();
  }

  // Read on until the reasons can be counted, unless a read already failed:
  // that one is offered again by the note in the rail, not retried here.
  $effect(() => {
    if (moreToRead && questions.length < EAGER && !loadError && !aggregateFetching) untrack(readAhead);
  });

  // Why each question is here, worked out once per question.
  const reasons = $derived(new Map(questions.map((question) => [question.id, reasonOf(question)])));
  const counts = $derived.by(() => {
    const tally = new Map<ReasonKey, number>();
    for (const reason of reasons.values()) tally.set(reason, (tally.get(reason) ?? 0) + 1);
    return tally;
  });
  // Only the reasons the queue holds, in the order REASONS lists them.
  const shownReasons = $derived(
    (Object.keys(REASONS) as ReasonKey[]).filter((key) => (counts.get(key) ?? 0) > 0)
  );

  let filter = $state<ReasonKey | 'all'>('all');

  // Grouped by album, the rail reads the wants of one album together and a
  // group header opens them as one batch. Remembered per browser.
  const GROUP_KEY = 'schall:review-by-album';
  let byAlbum = $state(
    (() => {
      try {
        return localStorage.getItem(GROUP_KEY) === '1';
      } catch {
        return false;
      }
    })()
  );
  function toggleByAlbum() {
    byAlbum = !byAlbum;
    batch = undefined;
    placed = false;
    try {
      localStorage.setItem(GROUP_KEY, byAlbum ? '1' : '0');
    } catch {
      // Best effort: the grouping still holds for this visit.
    }
  }

  const reasonFiltered = $derived(
    filter === 'all' ? questions : questions.filter((q) => reasons.get(q.id) === filter)
  );
  const albums = $derived(byAlbum ? groupByAlbum(reasonFiltered) : undefined);
  // The order the rail draws is the order the keys step through.
  const filtered = $derived(
    albums ? [...albums.groups.flatMap((group) => group.questions), ...albums.loose] : reasonFiltered
  );

  // The album batch open in place of a single question, by its key. The name
  // is kept apart from the group so the result still reads once every want in
  // it has been answered and the group has left the queue.
  let batch = $state<{ key: string; album: string; artist: string } | undefined>();
  const batchQuestions = $derived(
    batch ? (albums?.groups.find((group) => group.key === batch!.key)?.questions ?? []) : []
  );

  function chooseFilter(next: ReasonKey | 'all') {
    filter = next;
    batch = undefined;
    placed = false;
  }

  // Where the reader is, held by the question's id as well as by its
  // position, so a question arriving above the reader mid-decision does not
  // move them onto whatever slid into that row.
  let at = $state(0);
  const current = $derived(filtered[Math.min(at, Math.max(filtered.length - 1, 0))]);
  const currentReason = $derived(current ? reasons.get(current.id) : undefined);

  // The print is the cover of the song being decided, so each question
  // carries its own inks (ADR Duoton).
  const currentAlbum = $derived(current?.want?.target.originAlbumId ?? current?.download?.albumId);
  const currentInks = $derived(
    current?.want?.target.originAlbumId ? current.want.inks : current?.download?.inks
  );
  usePagePrint(() => {
    if (loading) return undefined;
    return {
      covers: currentAlbum ? [{ src: coverSrc(currentAlbum), seed: currentAlbum, inks: currentInks }] : [],
      mode: 'single',
      height: 16
    };
  });

  let held = $state('');
  // Whether the reader has put themselves anywhere yet. Nothing is held until
  // they do, so the position is the top of the list wherever the reads are.
  let placed = $state(false);
  $effect(() => {
    const list = filtered;
    untrack(() => {
      if (!placed) {
        at = 0;
        held = list[0]?.id ?? '';
        return;
      }
      const moved = list.findIndex((question) => question.id === held);
      if (moved >= 0) at = moved;
      else held = current?.id ?? '';
    });
  });

  // Within five rows of the end of what is loaded, the next page is read, so
  // stepping through a long queue does not wrap back to the top.
  $effect(() => {
    if (moreToRead && filtered.length - at <= 5) untrack(readAhead);
  });

  /** Reads ahead when the foot of the rail scrolls into view. */
  function nearEnd(sentinel: HTMLElement) {
    if (typeof IntersectionObserver === 'undefined') return;
    const observer = new IntersectionObserver((entries) => {
      if (entries.some((entry) => entry.isIntersecting)) readAhead();
    });
    observer.observe(sentinel);
    return { destroy: () => observer.disconnect() };
  }

  /** Moves to another question by position, wrapping at either end. */
  function step(index: number) {
    placed = true;
    batch = undefined;
    if (filtered.length === 0) {
      at = 0;
      held = '';
      return;
    }
    at = ((index % filtered.length) + filtered.length) % filtered.length;
    held = filtered[at]?.id ?? '';
  }

  /** Moves to the next question and records nothing. */
  function skip() {
    step(at + 1);
  }

  // A link naming a download or a want opens on it, once.
  let opened = $state('');
  const askedDownload = $derived(page.url.searchParams.get('download') ?? '');
  const askedWant = $derived(page.url.searchParams.get('want') ?? '');
  $effect(() => {
    const named = askedDownload || askedWant;
    if (!named || opened === named) return;
    const found = filtered.findIndex(
      (question) =>
        question.download?.id === askedDownload || question.want?.target.id === askedWant
    );
    if (found >= 0) {
      step(found);
      opened = named;
    }
  });

  // What the current want is still waiting for, when it is waiting rather than
  // asking. The reader may still answer it.
  const waiting = $derived(waitingLabel(current?.want));

  const creditOnlyStopped = $derived(isCreditOnlyStopped(current?.want));
  // A stopped want with a settled file compares that file; a credit-only
  // stopped want still has copies to choose between.
  const filed = $derived(
    Boolean(current?.want) && current!.want!.kind === 'stopped' && !creditOnlyStopped
  );
  const filedCopy = $derived(
    filed ? current!.want!.copies.find((copy) => copy.verdict === 'accepted' && copy.libraryFileId) : undefined
  );

  const groups = $derived(
    current?.want && current.kind === 'downloaded' && !filed
      ? groupCopies(current.want.copies, current.want.copyGroups)
      : []
  );
  const candidates = $derived(current?.kind === 'version' ? (current.want?.candidates ?? []) : []);

  // The selection resets with the question, alongside whether the reader has
  // said which candidate they mean.
  let chosen = $state(0);
  let saidWhich = $state(false);
  $effect(() => {
    void held;
    chosen = 0;
    saidWhich = false;
  });

  // The comparison's columns: the copies, best first, or the one file a
  // stopped want already holds. A copy whose evidence speaks against it can
  // still be accepted, but not as the main action.
  const columns = $derived.by((): CopyColumn[] => {
    const want = current?.want;
    if (!want || current?.kind !== 'downloaded') return [];
    if (filed) {
      return filedCopy
        ? [
            {
              heading: 'In your library',
              copy: filedCopy,
              rips: 1,
              filed: true,
              doubted:
                namesDiffer(want.fileArtist, want.target.artist) ||
                namesDiffer(want.fileTitle, want.target.title)
            }
          ]
        : [];
    }
    return groups.map((group, index) => ({
      heading: `Copy ${index + 1}`,
      copy: group.best,
      rips: group.rips,
      doubted: contradicted(group.best)
    }));
  });
  const column = $derived(columns[Math.min(chosen, Math.max(columns.length - 1, 0))]);
  const playerSrc = $derived(
    column
      ? column.filed && column.copy.libraryFileId
        ? api.libraryFileAudioUrl(column.copy.libraryFileId)
        : api.reviewCopyAudioUrl(column.copy.id)
      : ''
  );

  const evidenceOf = (candidate: AcquisitionCandidate) =>
    [
      candidate.trackTitle,
      candidate.artistName,
      candidate.releaseTitle ?? '',
      candidate.durationMs ?? '',
      candidate.agrees.join('|'),
      candidate.differs.join('|')
    ]
      .join(' ')
      .toLocaleLowerCase()
      .replace(/\s+/g, ' ')
      .trim();

  /** Whether two candidate recordings say the same thing, so nothing here may
   * preselect a guess between them. */
  const indistinguishable = $derived.by(() => {
    if (current?.kind !== 'version' || candidates.length < 2) return false;
    const seen = new Set<string>();
    for (const candidate of candidates) {
      const signature = evidenceOf(candidate);
      if (seen.has(signature)) return true;
      seen.add(signature);
    }
    return false;
  });
  const choiceMade = $derived(!indistinguishable || saidWhich);
  const shownChoice = $derived(choiceMade ? chosen : -1);

  // Audio may start by itself only after the reader has played something
  // here; from then on each next question's copy starts on its own.
  let heard = $state(false);
  let player = $state<ReturnType<typeof ReviewPlayer> | undefined>();

  async function playColumn(index: number) {
    heard = true;
    if (index !== chosen) {
      chosen = index;
      saidWhich = true;
      await tick();
    }
    player?.play();
  }

  // Awaited by every answer's onSuccess, so the answer stays pending, and its
  // controls disabled, until the queue no longer holds the question it
  // answered. Otherwise the same question could be answered twice.
  function invalidate() {
    return Promise.all(
      [
        ['review-queue'],
        ['downloads'],
        ['wants'],
        ['library'],
        ['library-files'],
        ['dashboard']
      ].map((key) => client.invalidateQueries({ queryKey: key }))
    );
  }

  /** Moves off the question just answered, onto whatever takes its place. The
   * pressed button loses focus, so Space cannot press it again on the next
   * question. */
  async function decided() {
    placed = true;
    at = Math.min(at, Math.max(filtered.length - 2, 0));
    if (document.activeElement instanceof HTMLElement) document.activeElement.blur();
    await invalidate();
  }

  const accept = createMutation({
    mutationFn: (copyId: string) => api.acceptReviewCopy(copyId),
    onSuccess: decided
  });
  const acceptStopped = createMutation({
    mutationFn: (targetId: string) => api.acceptStoppedWant(targetId),
    onSuccess: decided
  });
  const refuse = createMutation({
    mutationFn: (targetId: string) => api.refuseReviewCopies(targetId),
    onSuccess: decided
  });
  const notWanted = createMutation({
    mutationFn: (targetId: string) => api.stopPursuingTarget(targetId),
    onSuccess: decided
  });
  const wrongSong = createMutation({
    mutationFn: (targetId: string) => api.rejectTargetRecording(targetId),
    onSuccess: decided
  });
  const chooseRecording = createMutation({
    mutationFn: (choice: { targetId: string; recordingId: string }) =>
      api.chooseTargetRecording(choice.targetId, choice.recordingId),
    onSuccess: decided
  });
  const revalidate = createMutation({
    mutationFn: (requestId: string) => api.revalidateDownload(requestId),
    onSuccess: decided
  });

  /** Naming which catalogue track one of a folder's odd files belongs to.
   * This settles something without moving off the folder, so it invalidates
   * rather than stepping to the next question. */
  const resolveImport = createMutation({
    mutationFn: (body: { requestId: string; fileName: string; trackId: string }) =>
      api.resolveImportTrack(body.requestId, { fileName: body.fileName, trackId: body.trackId }),
    onSuccess: invalidate
  });
  const withdrawImport = createMutation({
    mutationFn: (body: { requestId: string; decisionId: string }) =>
      api.withdrawImportResolution(body.requestId, body.decisionId),
    onSuccess: invalidate
  });

  const deciding = $derived(
    $accept.isPending ||
      $acceptStopped.isPending ||
      $refuse.isPending ||
      $notWanted.isPending ||
      $wrongSong.isPending ||
      $chooseRecording.isPending ||
      $revalidate.isPending ||
      $resolveImport.isPending ||
      $withdrawImport.isPending
  );
  const failure = $derived(
    ($accept.error ??
      $acceptStopped.error ??
      $refuse.error ??
      $notWanted.error ??
      $wrongSong.error ??
      $chooseRecording.error ??
      $revalidate.error ??
      $resolveImport.error ??
      $withdrawImport.error) as Error | undefined
  );

  // A failed answer belongs to the question it was given on. Moving to
  // another question clears it.
  $effect(() => {
    void current?.id;
    untrack(() => {
      for (const answer of [
        $accept,
        $acceptStopped,
        $refuse,
        $notWanted,
        $wrongSong,
        $chooseRecording,
        $revalidate,
        $resolveImport,
        $withdrawImport
      ]) {
        if (answer.isError) answer.reset();
      }
    });
  });

  /** Accepts the copy in one column, or the filed file. */
  function acceptColumn(index: number) {
    const want = current?.want;
    const picked = columns[index];
    if (!want || !picked) return;
    if (picked.filed) $acceptStopped.mutate(want.target.id);
    else $accept.mutate(picked.copy.id);
  }

  /** What Enter and Y commit: the chosen column, or the candidate on the
   * selected row. Two candidates that say the same thing are never committed
   * this way — nothing is selected until the reader says which they mean. */
  function confirm() {
    if (!current) return;
    if (current.kind === 'downloaded') {
      acceptColumn(chosen);
      return;
    }
    if (current.kind === 'version' && current.want) {
      if (!choiceMade) return;
      const candidate = candidates[chosen];
      if (candidate) {
        $chooseRecording.mutate({ targetId: current.want.target.id, recordingId: candidate.recordingId });
      }
    }
  }

  /** What N answers: none of the copies. Only a question with copies to
   * refuse has an answer for it. */
  function refuseAll() {
    if (current?.kind === 'downloaded' && !filed && current.want) $refuse.mutate(current.want.target.id);
  }

  // Anything that already answers a keydown for itself: a control the reader
  // could be pressing on purpose, or a dialog/menu sitting over the page.
  const INTERACTIVE =
    'button, a, input, textarea, select, summary, [role="radio"], [role="button"], [role="slider"], [contenteditable], [role="dialog"], dialog[open], [role="menu"]';
  // What still takes its own letters: Y and N reach the page with focus on a
  // button, never while somebody types.
  const TYPING =
    'input, textarea, select, [contenteditable], [role="slider"], [role="dialog"], dialog[open], [role="menu"]';

  function onKey(event: KeyboardEvent) {
    if (event.defaultPrevented) return;
    if (event.metaKey || event.ctrlKey || event.altKey || event.shiftKey) return;
    const target = event.target instanceof HTMLElement ? event.target : null;
    if (target?.closest(TYPING)) return;
    if (!current || deciding) return;
    const key = event.key.toLowerCase();
    const steps = ['arrowup', 'arrowdown', 'arrowleft', 'arrowright', 'j', 'k'];
    // An open album batch answers nothing by key; the steps still leave it.
    if (batch && !steps.includes(key)) return;

    if (key === 'y' || key === 'n') {
      // Claimed whatever happens, so the letter never opens search instead.
      event.preventDefault();
      if (key === 'y') confirm();
      else refuseAll();
      return;
    }
    if (target?.closest(INTERACTIVE)) return;

    if (key === ' ') {
      event.preventDefault();
      heard = true;
      player?.toggle();
      return;
    }

    if (/^[1-9]$/.test(event.key)) {
      const index = Number(event.key) - 1;
      const max = current.kind === 'version' ? candidates.length : columns.length;
      event.preventDefault();
      if (index < max) {
        chosen = index;
        saidWhich = true;
      }
      return;
    }

    switch (key) {
      case 'enter':
        confirm();
        break;
      case 'arrowup':
      case 'arrowleft':
      case 'k':
        event.preventDefault();
        step(at - 1);
        break;
      case 'arrowdown':
      case 'arrowright':
      case 'j':
        event.preventDefault();
        step(at + 1);
        break;
    }
  }

  /** Keeps the open question's row inside the rail's own scroll as the reader
   * steps with the keys; the rail scrolls apart from the page. */
  function keepInView(rail: HTMLElement, _at: number) {
    const reveal = () =>
      rail.querySelector<HTMLElement>('[aria-current="true"]')?.scrollIntoView?.({ block: 'nearest' });
    return { update: reveal };
  }

  const countOf = (key: ReasonKey | 'all') =>
    key === 'all' ? total : (counts.get(key) ?? 0);
  // A reason counted over part of a longer queue says so.
  const partial = $derived(moreToRead);
</script>

<svelte:head><title>Review · Schall</title></svelte:head>
<svelte:window onkeydown={onKey} />

<!-- "Review" is the page's title, in the place every page keeps it; the song
     being decided heads the panel below. -->
<Hero title="Review" section />

<div class="mx-4 mb-6 flex flex-1 flex-col sm:mx-6">
  {#if pageError}
    <div class="py-10">
      <ErrorNote error={pageError} fallback="Couldn't load the review queue." retry={retryQueue} />
    </div>
  {:else}
  <Settle
    pending={loading}
    class="flex-1 {loading || hasQueue
      ? 'overflow-clip rounded-panel border border-line-thin bg-surface-thin'
      : ''}"
    layer="flex flex-col"
  >
    {#snippet placeholder()}
      <div role="status" aria-busy="true" aria-label="Loading the review queue" class="flex flex-col">
        <div class="flex items-center gap-4 border-b border-line-thin px-4 py-4 sm:px-6">
          <Skeleton shape="block" class="size-12" />
          <div class="flex flex-1 flex-col gap-2">
            <Skeleton class="h-6 w-2/5" />
            <Skeleton class="h-3.5 w-1/4" />
          </div>
        </div>
        <Skeleton shape="block" class="h-20 w-full rounded-none" />
        <div class="flex">
          <SkeletonRows count={6} class="hidden w-52 shrink-0 border-r border-line-thin lg:flex" />
          <div class="flex-1 p-5"><Skeleton shape="block" class="h-56 w-full max-w-[56rem]" /></div>
        </div>
      </div>
    {/snippet}
  {#if !hasQueue && moreToRead}
    <div class="flex flex-1 items-center justify-center py-10" aria-busy="true">
      <p class="text-body text-ink-3">Loading…</p>
    </div>
  {:else if !hasQueue}
    <div class="py-5">
      <EmptyPanel role="ok" heading="Nothing to decide" />
    </div>
  {:else}
    <!-- The song and what can be said about it as a whole, across the top. -->
    <!-- Sticky, so the answers stay in reach down a long list of candidates. -->
    <header
      class="sticky top-[var(--topbar-height)] z-10 flex flex-wrap items-center gap-x-4 gap-y-3 border-b border-line-thin bg-surface-thin px-4 py-3.5 sm:px-6"
    >
      {#if batch}
        <div class="flex min-w-0 flex-1 items-center gap-3.5">
          <Icon name="release" size={20} class="shrink-0 text-ink-3" />
          <div class="min-w-0">
            <h2 class="truncate text-quiet-lead font-bold text-ink">{batch.album}</h2>
            <p class="truncate text-meta text-ink-3">{batch.artist}</p>
          </div>
        </div>
      {:else if current}
        <div class="flex min-w-0 flex-1 basis-80 items-center gap-3.5">
          <Cover
            src={currentAlbum ? coverSrc(currentAlbum) : undefined}
            seed={currentAlbum ?? current.id}
            inks={currentInks}
            class="size-12 shrink-0 rounded-row object-cover"
          />
          <div class="min-w-0">
            <h2 class="truncate text-quiet-display font-bold text-ink">{current.title}</h2>
            <p class="hero-sub truncate text-meta text-ink-3">
              {#if current.detail}<span class="text-ink-2">{current.detail}</span>{/if}
              {#if current.want?.target.durationMs}
                <span class="numeric">{clock(current.want.target.durationMs / 1000)}</span>
              {/if}
              <span class="numeric">{(at + 1).toLocaleString()} of {(filter === 'all' ? total : filtered.length).toLocaleString()}</span>
            </p>
          </div>
          {#if currentReason}
            {@const reason = REASONS[currentReason]}
            <span
              class="hidden shrink-0 items-center gap-1.5 rounded-full border border-decide/45 bg-decide/8 px-2.5 py-1 text-meta font-medium text-decide sm:flex"
              title={currentReason === 'waiting' && waiting ? waiting : reason.tip}
            >
              <Icon name={reason.icon} size={13} />
              {currentReason === 'waiting' && waiting ? waiting : reason.label}
            </span>
          {/if}
        </div>

        <div class="flex flex-wrap items-center gap-1.5">
          {#if current.kind === 'folder' && current.download}
            <Button
              disabled={deciding || !current.download.revalidatable}
              onclick={() => $revalidate.mutate(current.download!.id)}
            >
              <Icon name="refresh" size={14} />
              Check again
            </Button>
          {:else if current.kind === 'version'}
            <Button disabled={deciding || !choiceMade || !candidates[chosen]} onclick={confirm}>
              <Icon name="check" size={14} />
              Use version
            </Button>
          {:else if !filed && current.want}
            <Button
              variant="outline"
              disabled={deciding}
              aria-label="None of these"
              title="None of these (N)"
              onclick={refuseAll}
            >
              <Icon name="close" size={14} />
              None
            </Button>
          {/if}
          <Button variant="outline" icon disabled={deciding} aria-label="Skip" title="Skip (J)" onclick={skip}>
            <Icon name="arrow-right" size={14} />
          </Button>
          {#if current.kind === 'downloaded' && current.want}
            <Button
              variant="outline"
              icon
              disabled={deciding}
              aria-label="Wrong song"
              title="Wrong song: the wish points at another recording"
              onclick={() => $wrongSong.mutate(current.want!.target.id)}
            >
              <Icon name="ban" size={14} />
            </Button>
          {/if}
          {#if current.want}
            <Button
              variant="outline"
              icon
              disabled={deciding}
              aria-label="Remove"
              title="Remove from wishlist"
              onclick={() => $notWanted.mutate(current.want!.target.id)}
            >
              <Icon name="delete" size={14} />
            </Button>
          {/if}
        </div>
      {/if}
      {#if failure}
        <p class="basis-full text-meta text-fail" role="status">{failure.message}</p>
      {/if}
    </header>

    {#if !batch && column && playerSrc}
      {#key current?.id}
        <ReviewPlayer
          bind:this={player}
          src={playerSrc}
          waveformOf={column.filed ? undefined : column.copy.id}
          label={`${column.heading} · ${column.filed ? 'in your library' : (column.copy.origin?.label ?? column.copy.username)}`}
          name={current?.title ?? ''}
          autoplay={heard}
          onstart={() => (heard = true)}
        />
      {/key}
    {/if}

    <div class="flex flex-1 flex-col lg:flex-row">
      <!-- Why each question is here, then the questions themselves. It stays
           under the sticky header and scrolls inside the window's height. -->
      <nav
        id="queue-rail"
        data-queue-rail
        aria-label="Review queue"
        class="flex shrink-0 flex-col border-b border-line-thin lg:sticky lg:top-[calc(var(--topbar-height)+5rem)] lg:max-h-[calc(100dvh-var(--topbar-height)-5rem)] lg:w-[clamp(13rem,17vw,16rem)] lg:self-start lg:border-r lg:border-b-0"
      >
        <div class="flex flex-wrap gap-0.5 p-2.5 lg:flex-col">
          {#each ['all' as const, ...shownReasons] as key (key)}
            {@const reason = key === 'all' ? undefined : REASONS[key]}
            <button
              type="button"
              onclick={() => chooseFilter(key)}
              aria-pressed={filter === key}
              title={reason?.tip}
              class="flex items-center gap-2.5 rounded-row px-2.5 py-1.5 text-left text-meta font-medium transition {filter ===
              key
                ? 'bg-surface-thick text-ink'
                : 'text-ink-2 hover:bg-surface-regular'}"
            >
              <Icon name={reason?.icon ?? 'checklist'} size={14} class="shrink-0" />
              <span class="min-w-0 flex-1 truncate">{reason?.label ?? 'All'}</span>
              <span class="numeric text-ink-4">
                {countOf(key)}{key !== 'all' && partial ? '+' : ''}
              </span>
            </button>
          {/each}
          <button
            type="button"
            onclick={toggleByAlbum}
            aria-pressed={byAlbum}
            aria-label="Group by album"
            title="Group by album: open an album and accept its copies together"
            class="flex items-center gap-2.5 rounded-row px-2.5 py-1.5 text-left text-meta font-medium transition lg:mt-1 lg:border-t lg:border-line-thin lg:rounded-none lg:pt-2.5 {byAlbum
              ? 'text-ink'
              : 'text-ink-3 hover:text-ink-2'}"
          >
            <Icon name="release" size={14} class="shrink-0" />
            <span class="hidden lg:inline">By album</span>
          </button>
        </div>

        {#if loadError}
          <div class="border-t border-line-thin px-4 py-2.5">
            <ErrorNote bare error={loadError} fallback="Couldn't load the rest of the queue." retry={retryQueue} />
          </div>
        {/if}

        <div
          use:keepInView={at}
          class="hidden min-h-0 flex-1 flex-col overflow-y-auto border-t border-line-thin lg:flex"
        >
          {#snippet row(question: (typeof filtered)[number], index: number)}
            {@const here = index === at && !batch}
            <button
              type="button"
              data-question={question.id}
              aria-current={here ? 'true' : undefined}
              onclick={() => step(index)}
              class="flex flex-col gap-0.5 border-b border-line-thin px-4 py-2 text-left {here
                ? 'queue-current'
                : 'hover:bg-surface-thick'}"
            >
              <span class="truncate text-body {here ? 'font-medium text-ink' : 'text-ink-2'}">
                {question.title}
              </span>
              <span class="truncate text-meta text-ink-4">{question.detail}</span>
            </button>
          {/snippet}
          {#if albums}
            {@const indexOf = new Map(filtered.map((question, index) => [question.id, index]))}
            {#each albums.groups as group (group.key)}
              {@const open = batch?.key === group.key}
              <button
                type="button"
                data-album={group.key}
                aria-current={open ? 'true' : undefined}
                aria-label={`${group.album} by ${group.artist}, ${group.questions.length}`}
                onclick={() => (batch = { key: group.key, album: group.album, artist: group.artist })}
                class="flex items-center gap-2 border-b border-line-thin bg-surface-regular px-4 py-2 text-left {open
                  ? 'queue-current'
                  : 'hover:bg-surface-thick'}"
              >
                <Icon name="release" size={13} class="shrink-0 text-ink-3" />
                <span class="flex min-w-0 flex-1 flex-col">
                  <span class="truncate text-body font-medium text-ink">{group.album}</span>
                  <span class="truncate text-meta text-ink-3">{group.artist}</span>
                </span>
                <span class="numeric shrink-0 text-meta text-ink-2">{group.questions.length}</span>
              </button>
              {#each group.questions as question (question.id)}
                {@render row(question, indexOf.get(question.id) ?? 0)}
              {/each}
            {/each}
            {#each albums.loose as question (question.id)}
              {@render row(question, indexOf.get(question.id) ?? 0)}
            {/each}
          {:else}
            {#each filtered as question, index (question.id)}
              {@render row(question, index)}
            {/each}
          {/if}
          {#if moreToRead && !loadError}
            <p use:nearEnd class="px-4 py-2.5 text-meta text-ink-4" aria-live="polite">Loading more…</p>
          {/if}
        </div>
      </nav>

      <section aria-label="Review" class="flex min-w-0 flex-1 flex-col gap-3 px-4 py-4 sm:px-6">
        {#if batch}
          {#key batch.key}
            <AlbumBatch album={batch.album} artist={batch.artist} questions={batchQuestions} ondone={invalidate} />
          {/key}
        {:else if !current}
          <!-- The reason chosen holds nothing. The rail stays, so the reader
               can choose another. -->
          {#if moreToRead && !loadError}
            <SkeletonRows count={4} label="Reading more of the queue" />
          {:else}
            <EmptyPanel role="idle" heading="Nothing of this kind">
              <p class="text-body text-ink-2">Choose All to see the rest.</p>
            </EmptyPanel>
          {/if}
        {:else if current.kind === 'downloaded' && current.want}
          <CopyTable
            want={current.want}
            {columns}
            {chosen}
            {deciding}
            onchoose={(index) => {
              chosen = index;
              saidWhich = true;
            }}
            onplay={(index) => void playColumn(index)}
            onaccept={acceptColumn}
          />
        {:else if current.kind === 'version' && current.want}
          <VersionCandidates
            candidates={current.want.candidates}
            chosen={shownChoice}
            onchoose={(index) => {
              chosen = index;
              saidWhich = true;
            }}
          />
          <UseAddress
            targetId={current.want.target.id}
            entryTitle={current.want.target.title}
            entryArtist={current.want.target.artist}
            entryDurationMs={current.want.target.durationMs}
          />
        {:else if current.kind === 'folder' && current.download}
          {@const request = current.download}
          {#if request.importError}
            <p class="text-body text-fail">{request.importError}</p>
          {/if}
          {#if request.importEvidence}
            <ImportEvidence
              evidence={request.importEvidence}
              decisions={request.importDecisions}
              pending={$resolveImport.isPending || $withdrawImport.isPending}
              onresolve={(fileName, trackId) =>
                $resolveImport.mutate({ requestId: request.id, fileName, trackId })}
              onwithdraw={(decisionId) => $withdrawImport.mutate({ requestId: request.id, decisionId })}
            />
          {/if}
        {/if}
      </section>
    </div>
  {/if}
  </Settle>
  {/if}
</div>

<style>
  /* The question on screen, marked in the page's light ink: the inks are chrome,
     and which row is open is a place, not a state. */
  .queue-current {
    background: color-mix(in srgb, var(--color-duo-light) 9%, transparent);
    box-shadow: inset 3px 0 0 var(--color-duo-light);
  }
</style>

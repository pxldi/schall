<script lang="ts">
  import Icon from '$lib/components/Icon.svelte';
  import type { IconName } from '$lib/icons';
  import {
    createInfiniteQuery,
    createMutation,
    keepPreviousData,
    useQueryClient
  } from '@tanstack/svelte-query';
  import { untrack } from 'svelte';
  import { goto } from '$app/navigation';
  import { page } from '$app/state';
  import { api, type AcquisitionCandidate, type AcquisitionTarget, type ReviewItem } from '$lib/api';
  import {
    albumKey,
    clock,
    groupByAlbum,
    groupCopies,
    isCreditOnlyStopped,
    listenable,
    questionsFrom,
    waitingLabel,
    type QuestionKind
  } from '$lib/review';
  import { listening, setVolume } from '$lib/preview.svelte';
  import Button from '$lib/components/Button.svelte';
  import Hero from '$lib/components/Hero.svelte';
  import { usePagePrint } from '$lib/duoton';
  import AlbumBatch from '$lib/components/AlbumBatch.svelte';
  import CopyCard from '$lib/components/CopyCard.svelte';
  import ListenPlayer from '$lib/components/ListenPlayer.svelte';
  import EmptyPanel from '$lib/components/EmptyPanel.svelte';
  import Settle from '$lib/components/Settle.svelte';
  import Skeleton from '$lib/components/Skeleton.svelte';
  import SkeletonRows from '$lib/components/SkeletonRows.svelte';
  import ErrorNote from '$lib/components/ErrorNote.svelte';
  import ImportEvidence from '$lib/components/ImportEvidence.svelte';
  import VersionCandidates from '$lib/components/VersionCandidates.svelte';
  import UseAddress from '$lib/components/UseAddress.svelte';
  import { relativeTime } from '$lib/utils';

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
  // read. The queue used to read one page of each and count what came back,
  // so a queue of thousands said 100 and ended there. The chips now take the
  // server's figures, and the next page is read as the reader nears the end
  // of what is loaded (`readAhead` below).
  const PAGE = 100;
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
  // could compare anything — the folder was gone, the database was
  // unreachable, the edition could not be fetched — is a failure rather than a
  // question: there is nothing to decide, and the only thing to do is ask the
  // machine to try what it already tried. That belongs beside the other
  // failures on the download, not in this queue.
  // The server leaves those out itself (the `questions` view), so its count
  // is the Folder chip and every page it sends is folders that ask something.
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

  // The page-wide error band stands in for the queue only when no question
  // has arrived. A later read that fails (the next page, or a refetch) leaves
  // the loaded questions where they are and says so in the rail.
  const hasQueue = $derived(questions.length > 0);
  const pageError = $derived(hasQueue ? undefined : loadError);

  const wantTotal = $derived($wants.data?.pages[0]?.total ?? 0);
  const resolutions = $derived($wants.data?.pages[0]?.resolutions ?? 0);
  const folderTotal = $derived($downloads.data?.pages[0]?.total ?? 0);
  const counts = $derived({
    all: wantTotal + folderTotal,
    downloaded: Math.max(wantTotal - resolutions, 0),
    version: resolutions,
    folder: folderTotal
  });

  const moreToRead = $derived(Boolean($wants.hasNextPage || $downloads.hasNextPage));

  /** Reads the next page of whichever list has one. Called as the reader
   * comes within a few rows of the end of what is loaded, by stepping or by
   * scrolling the rail. */
  function readAhead() {
    if ($wants.hasNextPage && !$wants.isFetchingNextPage) void $wants.fetchNextPage();
    if ($downloads.hasNextPage && !$downloads.isFetchingNextPage) void $downloads.fetchNextPage();
  }

  // Each kind carries a Misprint icon beside its word; "All" has no picture
  // that says it better than the word.
  const FILTERS: { key: QuestionKind | 'all'; label: string; icon?: IconName }[] = [
    { key: 'all', label: 'All' },
    { key: 'downloaded', label: 'Downloaded', icon: 'download' },
    { key: 'version', label: 'Version', icon: 'fingerprint' },
    { key: 'folder', label: 'Folder', icon: 'folder-add' }
  ];
  let filter = $state<QuestionKind | 'all'>('all');

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

  // Listening mode asks one question at a time and only the ones with copies
  // to hear, so the rail and the other kinds step aside.
  let listen = $state(false);
  // Audio may start by itself only after a press or a key on the page.
  let gestured = $state(
    typeof navigator !== 'undefined' &&
      Boolean((navigator as { userActivation?: { hasBeenActive?: boolean } }).userActivation?.hasBeenActive)
  );
  let player = $state<ReturnType<typeof ListenPlayer> | undefined>();
  function toggleListen(event: MouseEvent) {
    listen = !listen;
    batch = undefined;
    placed = false;
    // Space and Y/N go to the page, not to this button.
    (event.currentTarget as HTMLElement).blur();
  }

  const kindFiltered = $derived(
    filter === 'all' ? questions : questions.filter((q) => q.kind === filter)
  );
  const albums = $derived(byAlbum && !listen ? groupByAlbum(kindFiltered) : undefined);
  // The order the rail draws is the order the keys step through.
  const filtered = $derived(
    listen
      ? kindFiltered.filter(listenable)
      : albums
        ? [...albums.groups.flatMap((group) => group.questions), ...albums.loose]
        : kindFiltered
  );

  // The album batch open in place of a single question, by its key. The name
  // is kept apart from the group so the result still reads once every want in
  // it has been answered and the group has left the queue.
  let batch = $state<{ key: string; album: string; artist: string } | undefined>();
  const batchQuestions = $derived(
    batch ? (albums?.groups.find((group) => group.key === batch!.key)?.questions ?? []) : []
  );

  function chooseFilter(next: QuestionKind | 'all') {
    filter = next;
    batch = undefined;
    // The list this position was held in just changed shape; start again from
    // its top rather than at wherever the old index now lands.
    placed = false;
  }

  // Where the reader is, held by the question's id as well as by its
  // position. The two reads behind this queue answer again while it is being
  // worked through, so a question can arrive above the reader mid-decision;
  // the id is what keeps them on the one they were reading rather than on
  // whatever slid into that row.
  let at = $state(0);
  const current = $derived(filtered[Math.min(at, Math.max(filtered.length - 1, 0))]);

  // The print is the cover of the song being decided, printed large, so each
  // question carries its own inks (ADR Duoton). A want names the album it came
  // from when it did; a folder names the release it was fetched for.
  const currentAlbum = $derived(current?.want?.target.originAlbumId ?? current?.download?.albumId);
  usePagePrint(() => {
    if (loading) return undefined;
    return { covers: currentAlbum ? [currentAlbum] : [], mode: 'single', height: 16 };
  });

  let held = $state('');
  // Whether the reader has put themselves anywhere yet. Nothing is held until
  // they do, so the position is the top of the list wherever the two reads
  // have got it to.
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
  // stepping through the queue does not wrap back to the top while thousands
  // remain. A filter whose kind the loaded pages hold none of reads on until
  // it finds one or the lists end.
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

  /** Moves to the next question and records nothing. The only one of these
   * answers that is not written down. */
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
  // asking. It changes nothing about the controls: the reader may still answer
  // it, and everything here says so.
  const waiting = $derived(waitingLabel(current?.want));

  const creditOnlyStopped = $derived(isCreditOnlyStopped(current?.want));
  // A stopped want with a settled file is the library card; a credit-only
  // stopped want is an ordinary grid of copies, because there is still a copy
  // to choose between rather than one file already filed.
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
  // The copy the next question would open on, loaded ahead in listening mode.
  const nextListen = $derived.by(() => {
    if (!listen || filtered.length < 2) return '';
    const next = filtered[(at + 1) % filtered.length]?.want;
    const best = next ? groupCopies(next.copies, next.copyGroups)[0]?.best : undefined;
    return best ? api.reviewCopyAudioUrl(best.id) : '';
  });
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
   * preselect a guess between them. Copies are never checked this way: a
   * reader listens to a copy before accepting it, which is a comparison a
   * candidate's metadata cannot offer. */
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

  function invalidate() {
    for (const key of [
      ['review-queue'],
      ['downloads'],
      ['acquisition-targets'],
      ['library'],
      ['library-files'],
      ['dashboard']
    ]) {
      void client.invalidateQueries({ queryKey: key });
    }
  }

  /** Moves off the question just answered, onto whatever takes its place. */
  function decided() {
    placed = true;
    at = Math.min(at, Math.max(filtered.length - 2, 0));
    invalidate();
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
   * This settles something without moving off the folder — there is usually
   * more than one file left to resolve — so it invalidates rather than
   * stepping to the next question. */
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

  /** What Enter commits: the copy on the selected card, or the candidate on
   * the selected row. Two candidates that say the same thing are never
   * committed this way — nothing is selected until the reader says which they
   * mean. */
  function confirm() {
    if (!current) return;
    if (current.kind === 'downloaded' && current.want) {
      if (filed) {
        if (filedCopy) $acceptStopped.mutate(current.want.target.id);
        return;
      }
      const group = groups[chosen];
      if (group) $accept.mutate(group.best.id);
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

  // Anything that already answers a keydown for itself: a control the reader
  // could be pressing on purpose, or a dialog/menu sitting over the page.
  // Enter only commits the current question when focus is on none of these —
  // on the page body, or on plain text inside the copy workspace.
  const INTERACTIVE =
    'button, a, input, textarea, select, summary, [role="radio"], [role="button"], [role="slider"], [contenteditable], [role="dialog"], dialog[open], [role="menu"]';

  // What still takes its own keys in listening mode, where Y, N, Space and
  // the steps reach the page even with focus on a button.
  const TYPING =
    'input, textarea, select, [contenteditable], [role="slider"], [role="dialog"], dialog[open], [role="menu"]';

  function onKey(event: KeyboardEvent) {
    gestured = true;
    if (event.defaultPrevented) return;
    if (event.metaKey || event.ctrlKey || event.altKey || event.shiftKey) return;
    const target = event.target instanceof HTMLElement ? event.target : null;
    if (target?.closest(listen ? TYPING : INTERACTIVE)) return;
    if (!current || deciding) return;
    // An open album batch answers nothing by key; the steps still leave it.
    if (batch && !['arrowup', 'arrowdown', 'arrowleft', 'arrowright', 'j', 'k'].includes(event.key.toLowerCase())) {
      return;
    }

    if (listen) {
      const key = event.key.toLowerCase();
      if (key === 'y' || key === 'n' || key === ' ') {
        // Claimed whatever happens, so the key never opens search or presses
        // the focused button.
        event.preventDefault();
        if (key === ' ') player?.replay();
        else if (key === 'y') confirm();
        else if (current.want) $refuse.mutate(current.want.target.id);
        return;
      }
      // Enter on a focused button presses that button; the page leaves it.
      if (key === 'enter' && target?.closest(INTERACTIVE)) return;
    }

    if (/^[1-9]$/.test(event.key)) {
      const index = Number(event.key) - 1;
      const max = current.kind === 'version' ? candidates.length : !filed ? groups.length : 0;
      // Claimed even when out of range, so typing a digit here never opens
      // search instead.
      event.preventDefault();
      if (index < max) {
        chosen = index;
        saidWhich = true;
      }
      return;
    }

    switch (event.key.toLowerCase()) {
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

  /** The sample's state as a fragment. The server's reason is a paragraph,
   * so it sits behind a disclosure (`whyNoSample`) rather than in the line. */
  function sampleText(target: AcquisitionTarget) {
    return target.anchorNextAttemptAt
      ? `Sample retry ${relativeTime(target.anchorNextAttemptAt)}`
      : 'No sample';
  }

  /** Keeps the open question's row inside the rail's own scroll as the reader
   * steps with the keys; the rail scrolls apart from the page. */
  function keepInView(rail: HTMLElement, _at: number) {
    const reveal = () =>
      rail.querySelector<HTMLElement>('[aria-current="true"]')?.scrollIntoView?.({ block: 'nearest' });
    return { update: reveal };
  }

  function onVolume(event: Event) {
    setVolume(Number((event.currentTarget as HTMLInputElement).value) / 100);
  }

  /** Roves the checked copy with the arrow keys. `onKey` above already
   * ignores a keydown targeting a radio, so this is the only thing that
   * moves the queue's arrow keys away from the copy grid once a card has
   * focus. */
  function onCopyRadioKeydown(event: KeyboardEvent) {
    if (!['ArrowLeft', 'ArrowRight', 'ArrowUp', 'ArrowDown'].includes(event.key)) return;
    const target = event.target as HTMLElement;
    if (!target.matches('[role="radio"]')) return;
    event.preventDefault();
    const radios = Array.from(
      (event.currentTarget as HTMLElement).querySelectorAll<HTMLElement>('[role="radio"]')
    );
    if (radios.length === 0) return;
    const from = Math.max(radios.indexOf(target), 0);
    const delta = event.key === 'ArrowLeft' || event.key === 'ArrowUp' ? -1 : 1;
    const next = ((from + delta) % radios.length + radios.length) % radios.length;
    chosen = next;
    saidWhich = true;
    radios[next]?.focus();
  }
</script>

<svelte:head><title>Review · Schall</title></svelte:head>
<svelte:window onkeydown={onKey} onpointerdown={() => (gestured = true)} />

{#snippet whyNoSample(target: AcquisitionTarget)}
  <details class="text-meta">
    <summary
      class="flex cursor-pointer items-center gap-1.5 text-ink-3 transition-colors select-none hover:text-ink-2"
    >
      <Icon name="info" size={13} />
      Why no sample
    </summary>
    <p class="mt-1 max-w-[40rem] leading-relaxed text-ink-2">{target.anchorUnavailable}</p>
  </details>
{/snippet}

<!-- What the entry asks for, beside the copies that answer it, so the rows
     read across: Length against Length, Album against Album. "Wrong song" is
     about the entry, so it sits here rather than with the decisions. It heads
     the card: at the foot of it, a 753 px window opened with it under the
     sticky decision bar. -->
{#snippet wanted(want: ReviewItem)}
  <div class="flex w-full flex-col gap-3 rounded-card border border-dashed border-line-regular p-4">
    <div class="-my-1 -mr-1.5 flex items-center gap-1.5">
      <span class="flex items-center gap-1.5 text-meta text-ink-3">
        <Icon name="want" size={14} />
        Wishlist entry
      </span>
      <Button
        class="ml-auto"
        variant="ghost"
        size="sm"
        onclick={() => $wrongSong.mutate(want.target.id)}
        disabled={deciding}
      >
        <Icon name="ban" size={14} />
        Wrong song
      </Button>
    </div>
    <div class="grid grid-cols-[5rem_minmax(0,1fr)] gap-x-2.5 gap-y-1.5 text-quiet-meta">
      <span class="text-ink-3">Length</span>
      <span class="numeric text-body text-ink">
        {want.target.durationMs ? clock(want.target.durationMs / 1000) : 'unknown'}
      </span>
      <span class="text-ink-3">Album</span>
      <span class="truncate text-body text-ink">{want.target.album || 'none'}</span>
      <span class="text-ink-3">ISRC</span>
      <span class="truncate text-body text-ink">{want.target.isrc ?? 'none'}</span>
      {#if want.target.anchorUnavailable}
        <span class="text-ink-3">Sample</span>
        <span class="text-body text-ink">{sampleText(want.target)}</span>
      {/if}
    </div>
    {#if want.target.anchorUnavailable && !want.target.anchorNextAttemptAt}
      {@render whyNoSample(want.target)}
    {/if}
  </div>
{/snippet}

<!-- "Review" is the page's title, in the place every page keeps it; the song
     being decided heads the panel below. -->
<Hero title="Review" section>
  {#snippet actions()}
    <Button
      variant={listen ? 'primary' : 'outline'}
      icon
      aria-pressed={listen}
      aria-label="Listening mode"
      title="Listening mode: Y accept, N none, Space play, J/K step"
      onclick={toggleListen}
    >
      <Icon name="listen" size={15} />
    </Button>
  {/snippet}
</Hero>

<!-- The queue and the question share one panel, the queue down its left. -->
<div class="mx-4 mb-6 flex flex-1 flex-col sm:mx-6">
  {#if pageError}
    <div class="py-10">
      <ErrorNote error={pageError} fallback="The review queue could not be read." retry={retryQueue} />
    </div>
  {:else}
  <Settle
    pending={loading}
    class="flex-1 {loading || hasQueue
      ? 'overflow-clip rounded-panel border border-line-thin bg-surface-thin lg:min-h-[32rem]'
      : ''}"
    layer="flex flex-col lg:flex-row"
  >
    {#snippet placeholder()}
      <!-- The panel's own shape: the rail with its chips and rows, and the
           question beside it, so nothing moves when the queue arrives. -->
      <div
        class="flex shrink-0 flex-col border-b border-line-thin lg:w-[clamp(13rem,18vw,17rem)] lg:border-b-0"
        role="status"
        aria-busy="true"
        aria-label="Loading the review queue"
      >
        <div class="flex flex-wrap gap-1 p-4">
          {#each ['w-10', 'w-24', 'w-20', 'w-[4.5rem]'] as width (width)}
            <Skeleton class="h-6 rounded-full {width}" />
          {/each}
        </div>
        <SkeletonRows count={6} class="hidden border-t border-line-thin lg:flex" />
      </div>
      <div class="flex min-w-0 flex-1 flex-col gap-3 px-4 pb-6 pt-5 sm:px-6 lg:border-l lg:border-line-thin lg:px-8">
        <Skeleton class="h-3 w-16" />
        <Skeleton class="h-7 w-2/5" />
        <Skeleton class="h-3.5 w-1/4" />
        <Skeleton shape="block" class="mt-2 h-56 w-full max-w-[64rem]" />
      </div>
    {/snippet}
  {#if !hasQueue && moreToRead}
    <div class="flex flex-1 items-center justify-center py-10" aria-busy="true">
      <p class="text-body text-ink-3">Loading…</p>
    </div>
  {:else if !hasQueue}
    <div class="py-5">
      <EmptyPanel role="ok" heading={listen ? 'Nothing to listen to' : 'Nothing to decide'} />
    </div>
  {:else}
    {#if !listen}
    <nav
      id="queue-rail"
      data-queue-rail
      aria-label="Review queue"
      class="flex shrink-0 flex-col border-b border-line-thin lg:sticky lg:top-[var(--topbar-height)] lg:max-h-[calc(100dvh-var(--topbar-height))] lg:w-[clamp(13rem,18vw,17rem)] lg:self-start lg:border-b-0"
    >
      <div class="flex flex-wrap gap-1 p-4">
        {#each FILTERS as option (option.key)}
          <button
            type="button"
            onclick={() => chooseFilter(option.key)}
            aria-pressed={filter === option.key}
            class="flex items-center gap-1.5 rounded-full border px-2 py-1 text-meta {filter ===
            option.key
              ? 'border-line-regular bg-surface-thick text-ink'
              : 'border-line-thin text-ink-2'}"
          >
            {#if option.icon}<Icon name={option.icon} size={13} />{/if}
            {option.label}
            <span class="numeric text-ink-3">{counts[option.key]}</span>
          </button>
        {/each}
        <button
          type="button"
          onclick={toggleByAlbum}
          aria-pressed={byAlbum}
          aria-label="Group by album"
          title="Group by album"
          class="flex items-center rounded-full border px-2 py-1 text-meta {byAlbum
            ? 'border-line-regular bg-surface-thick text-ink'
            : 'border-line-thin text-ink-2'}"
        >
          <Icon name="release" size={13} />
        </button>
      </div>

      {#if loadError}
        <!-- A read that failed after questions arrived keeps them on screen:
             the note sits where the next page would have been announced. -->
        <div class="border-t border-line-thin px-4 py-2.5">
          <ErrorNote bare error={loadError} fallback="The rest of the queue did not load." retry={retryQueue} />
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
            class="flex flex-col gap-0.5 border-b border-line-thin px-4 py-2.5 text-left {here
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
              onclick={() =>
                (batch = { key: group.key, album: group.album, artist: group.artist })}
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

      <div class="mt-auto hidden items-center gap-2.5 border-t border-line-thin p-4 lg:flex">
        <Icon name="volume" size={14} class="shrink-0 text-ink-3" />
        <input
          type="range"
          min="0"
          max="100"
          value={listening.volume * 100}
          oninput={onVolume}
          aria-label="Volume"
          class="w-full cursor-pointer"
        />
      </div>
    </nav>
    {/if}

    {#if !current && !batch}
      <!-- The filter chosen holds nothing. The rail stays, so the reader can
           choose another kind. -->
      <section aria-label="Review" class="flex min-w-0 flex-1 flex-col px-4 pb-6 pt-5 sm:px-6 lg:border-l lg:border-line-thin lg:px-8">
        {#if moreToRead && !loadError}
          <SkeletonRows count={4} label="Reading more of the queue" />
        {:else}
          <EmptyPanel role="idle" heading="Nothing of this kind">
            <p class="text-body text-ink-2">Choose All to see the rest.</p>
          </EmptyPanel>
        {/if}
      </section>
    {:else}
    <section
      aria-label="Review"
      class="flex min-w-0 flex-1 flex-col {listen ? '' : 'lg:border-l lg:border-line-thin'}"
    >
      {#if batch}
        <div class="flex flex-col gap-3 px-4 pt-5 pb-6 sm:px-6 lg:px-8">
          {#key batch.key}
            <AlbumBatch
              album={batch.album}
              artist={batch.artist}
              questions={batchQuestions}
              ondone={invalidate}
            />
          {/key}
        </div>
      {:else if current}

      <div class="flex flex-col gap-3 px-4 sm:px-6 pb-6 pt-5 lg:px-8">
        <header class="flex flex-col gap-1">
          <p class="numeric text-meta text-ink-3">
            {(at + 1).toLocaleString()} of {(listen ? filtered.length : counts[filter]).toLocaleString()}
          </p>
          <h2 class="text-quiet-display font-bold text-ink">{current.title}</h2>
          <div class="hero-sub text-quiet-meta text-ink-2">
            {#if current.detail}<span class="font-medium text-ink">{current.detail}</span>{/if}
            {#if current.want?.target.durationMs}
              <span class="numeric">{clock(current.want.target.durationMs / 1000)}</span>
            {/if}
            {#if waiting}<span>{waiting}</span>{/if}
            <!-- A downloaded question shows the sample on the Wanted card instead. -->
            {#if current.kind === 'version' && current.want?.target.anchorUnavailable}
              <span>{sampleText(current.want.target)}</span>
            {/if}
          </div>
        </header>

        {#if failure}
          <p class="text-meta text-fail" role="status">{failure.message}</p>
        {/if}

        {#if listen && current.want && groups[chosen]}
          {@const group = groups[chosen]}
          <div class="flex max-w-[48rem] flex-col gap-3 rounded-card border border-line-thin p-4">
            <p class="truncate text-meta text-ink-3">
              Copy {chosen + 1} of {groups.length}{group.best.origin ? ` · from ${group.best.origin.label}` : ''}
            </p>
            <ListenPlayer
              bind:this={player}
              src={api.reviewCopyAudioUrl(group.best.id)}
              next={nextListen}
              name={current.title}
              autoplay={gestured}
            />
          </div>
          <p class="flex flex-wrap gap-x-4 gap-y-1 text-meta text-ink-3" aria-label="Keys">
            <span><kbd class="numeric font-medium text-ink-2">Y</kbd> accept</span>
            <span><kbd class="numeric font-medium text-ink-2">N</kbd> none</span>
            <span><kbd class="numeric font-medium text-ink-2">Space</kbd> play</span>
            <span><kbd class="numeric font-medium text-ink-2">J</kbd>/<kbd class="numeric font-medium text-ink-2">K</kbd> step</span>
            {#if groups.length > 1}<span><kbd class="numeric font-medium text-ink-2">1</kbd>–<kbd class="numeric font-medium text-ink-2">{Math.min(groups.length, 9)}</kbd> copy</span>{/if}
          </p>
        {:else if current.kind === 'downloaded' && current.want}
          {#if filed}
            {@const copy = filedCopy}
            {#if copy}
              <div class="grid max-w-[64rem] grid-cols-1 gap-3 sm:grid-cols-[repeat(auto-fit,minmax(17rem,1fr))]">
                {@render wanted(current.want)}
                <CopyCard
                  heading="In your library"
                  subheading={`as “${current.want.fileTitle ?? ''}”`}
                  {copy}
                  wantedMs={current.want.target.durationMs}
                  credit={{
                    fileArtist: current.want.fileArtist,
                    fileTitle: current.want.fileTitle,
                    wantedArtist: current.want.target.artist,
                    wantedTitle: current.want.target.title
                  }}
                  selected={true}
                  onselect={() => {}}
                />
              </div>
            {/if}
          {:else}
            <div class="grid max-w-[64rem] grid-cols-1 gap-3 sm:grid-cols-[repeat(auto-fit,minmax(17rem,1fr))]">
              {@render wanted(current.want)}
              <div
                class="contents"
                role="radiogroup"
                aria-label="Copies"
                tabindex="-1"
                onkeydown={onCopyRadioKeydown}
              >
                {#each groups as group, index (group.best.id)}
                  <CopyCard
                    heading={`Copy ${index + 1}`}
                    copy={group.best}
                    wantedMs={current.want.target.durationMs}
                    selected={index === chosen}
                    onselect={() => {
                      chosen = index;
                      saidWhich = true;
                    }}
                  />
                {/each}
              </div>
            </div>
          {/if}
        {:else if current.kind === 'version' && current.want}
          {#if current.want.target.anchorUnavailable}
            {@render whyNoSample(current.want.target)}
          {/if}
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
              onwithdraw={(decisionId) =>
                $withdrawImport.mutate({ requestId: request.id, decisionId })}
            />
          {/if}
        {/if}
      </div>

      <footer
        class="sticky bottom-0 mt-auto flex flex-wrap items-center justify-end gap-2 border-t border-line-regular bg-surface-thin px-4 sm:px-6 py-3 lg:px-8"
        style="padding-bottom: max(0.75rem, env(safe-area-inset-bottom));"
      >
        {#if current.kind === 'folder' && current.download}
          <Button
            disabled={deciding || !current.download.revalidatable}
            onclick={() => $revalidate.mutate(current.download!.id)}
          >
            Check again
          </Button>
          <Button variant="outline" disabled={deciding} onclick={skip}>Skip</Button>
        {:else}
          <Button variant="outline" disabled={deciding} onclick={skip}>Skip</Button>
          {#if current.want}
            <Button
              variant="outline"
              disabled={deciding}
              onclick={() => $notWanted.mutate(current.want!.target.id)}
            >
              Don't want
            </Button>
          {/if}
          {#if current.kind === 'downloaded' && !filed && current.want}
            <Button
              variant="outline"
              disabled={deciding}
              onclick={() => $refuse.mutate(current.want!.target.id)}
            >
              None of these
            </Button>
            <Button disabled={deciding || !groups[chosen]} onclick={confirm}>Accept copy</Button>
          {:else if current.kind === 'downloaded' && filed}
            <Button disabled={deciding || !filedCopy} onclick={confirm}>Accept copy</Button>
          {:else if current.kind === 'version'}
            <Button disabled={deciding || !choiceMade || !candidates[chosen]} onclick={confirm}>
              Use recording
            </Button>
          {/if}
        {/if}
      </footer>
      {/if}
    </section>
    {/if}
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

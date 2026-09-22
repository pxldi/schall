<script lang="ts">
  import { createMutation, createQuery, keepPreviousData, useQueryClient } from '@tanstack/svelte-query';
  import { untrack } from 'svelte';
  import { goto } from '$app/navigation';
  import { page } from '$app/state';
  import { RotateCw, Volume2 } from '@lucide/svelte';
  import { api, type AcquisitionCandidate } from '$lib/api';
  import { isAuthError } from '$lib/errors';
  import {
    clock,
    groupCopies,
    isCreditOnlyStopped,
    questionsFrom,
    waitingLabel,
    type QuestionKind
  } from '$lib/review';
  import { listening, setVolume } from '$lib/preview.svelte';
  import Button from '$lib/components/Button.svelte';
  import CopyCard from '$lib/components/CopyCard.svelte';
  import EmptyPanel from '$lib/components/EmptyPanel.svelte';
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

  const wants = createQuery({
    queryKey: ['review-queue'],
    queryFn: () => api.reviewQueue(100),
    placeholderData: keepPreviousData
  });
  // Only the folders that recorded what they found. One that gave up before it
  // could compare anything — the folder was gone, the database was
  // unreachable, the edition could not be fetched — is a failure rather than a
  // question: there is nothing to decide, and the only thing to do is ask the
  // machine to try what it already tried. That belongs beside the other
  // failures on the download, not in this queue.
  const downloads = createQuery({
    queryKey: ['downloads', 'review'],
    queryFn: () => api.downloads({ view: 'review', limit: 100 }),
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
    ($downloads.data?.items ?? []).filter((item) => item.importStatus === 'needs_review')
  );

  const questions = $derived(
    questionsFrom({ wants: $wants.data?.items ?? [], imports: paused })
  );

  const counts = $derived({
    all: questions.length,
    downloaded: questions.filter((q) => q.kind === 'downloaded').length,
    version: questions.filter((q) => q.kind === 'version').length,
    folder: questions.filter((q) => q.kind === 'folder').length
  });

  const FILTERS: { key: QuestionKind | 'all'; label: string }[] = [
    { key: 'all', label: 'All' },
    { key: 'downloaded', label: 'Downloaded' },
    { key: 'version', label: 'Version' },
    { key: 'folder', label: 'Folder' }
  ];
  let filter = $state<QuestionKind | 'all'>('all');
  const filtered = $derived(
    filter === 'all' ? questions : questions.filter((q) => q.kind === filter)
  );

  function chooseFilter(next: QuestionKind | 'all') {
    filter = next;
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

  /** Moves to another question by position, wrapping at either end — there is
   * no count on screen to say where the ends are any more. */
  function step(index: number) {
    placed = true;
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

  function onKey(event: KeyboardEvent) {
    if (event.defaultPrevented) return;
    if (event.metaKey || event.ctrlKey || event.altKey || event.shiftKey) return;
    const target = event.target;
    if (target instanceof HTMLElement && target.closest(INTERACTIVE)) return;
    if (!current || deciding) return;

    if (/^[1-9]$/.test(event.key)) {
      const index = Number(event.key) - 1;
      const max = current.kind === 'version' ? candidates.length : !filed ? groups.length : 0;
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
        step(at - 1);
        break;
      case 'arrowdown':
      case 'arrowright':
      case 'j':
        step(at + 1);
        break;
    }
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
<svelte:window onkeydown={onKey} />

<div class="flex min-h-screen flex-1 flex-col lg:flex-row">
  <!-- The current track is the visible h1 below once there is one; loading,
       failed and empty all lack it, so this hidden one stands in. -->
  {#if !current}<h1 class="sr-only">Review</h1>{/if}
  {#if loading}
    <div class="flex flex-1 items-center justify-center px-4 sm:px-6 py-10" aria-busy="true">
      <p class="text-body text-ink-3">Loading…</p>
    </div>
  {:else if loadError}
    <div class="flex flex-col items-start gap-3 px-4 sm:px-6 py-10">
      <ErrorNote
        error={loadError}
        fallback="The review queue could not be read."
        action={isAuthError(loadError) ? undefined : 'Press Retry to ask again.'}
      />
      {#if !isAuthError(loadError)}
        <Button onclick={retryQueue}>
          <RotateCw size={13} strokeWidth={2.3} />
          Retry
        </Button>
      {/if}
    </div>
  {:else if !current}
    <div class="px-4 sm:px-6 py-5">
      <EmptyPanel role="ok" heading="Nothing to decide" />
    </div>
  {:else}
    <nav
      id="queue-rail"
      data-queue-rail
      aria-label="Review queue"
      class="flex shrink-0 flex-col border-b border-line-thin lg:w-[clamp(13rem,18vw,17rem)] lg:border-b-0 lg:border-r"
    >
      <div class="flex flex-wrap gap-1.5 p-4">
        {#each FILTERS as option (option.key)}
          <button
            type="button"
            onclick={() => chooseFilter(option.key)}
            aria-pressed={filter === option.key}
            class="flex items-center gap-1.5 rounded-full border px-2.5 py-1 text-meta {filter ===
            option.key
              ? 'border-line-regular bg-surface-thick text-ink'
              : 'border-line-thin text-ink-2'}"
          >
            {option.label}
            <span class="numeric text-ink-3">{counts[option.key]}</span>
          </button>
        {/each}
      </div>

      <div class="hidden flex-1 flex-col overflow-y-auto border-t border-line-thin lg:flex">
        {#each filtered as question, index (question.id)}
          <button
            type="button"
            data-question={question.id}
            aria-current={index === at ? 'true' : undefined}
            onclick={() => step(index)}
            class="flex flex-col gap-0.5 border-b border-line-thin px-4 py-2.5 text-left {index ===
            at
              ? 'bg-surface-regular'
              : 'hover:bg-surface-thick'}"
          >
            <span class="truncate text-body {index === at ? 'font-medium text-ink' : 'text-ink-2'}">
              {question.title}
            </span>
            <span class="truncate text-meta text-ink-4">{question.detail}</span>
          </button>
        {/each}
      </div>

      <div class="mt-auto hidden items-center gap-2.5 border-t border-line-thin p-4 lg:flex">
        <Volume2 size={14} strokeWidth={1.8} class="shrink-0 text-ink-3" />
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

    <section aria-label="Review" class="flex min-w-0 flex-1 flex-col">
      <header
        class="flex flex-wrap items-baseline gap-x-3.5 gap-y-1 border-b border-line-thin px-4 sm:px-6 py-5 lg:px-8"
      >
        <h1 class="text-quiet-display font-semibold leading-none text-ink">{current.title}</h1>
        {#if current.detail}
          <span class="text-body font-medium text-ink-2">{current.detail}</span>
        {/if}
        {#if waiting}
          <span class="rounded-full border border-line-thin px-2 py-0.5 text-meta text-ink-3">
            {waiting}
          </span>
        {/if}
        {#if current.want?.target.durationMs}
          <span class="numeric text-body text-ink">{clock(current.want.target.durationMs / 1000)}</span>
        {/if}
        {#if current.want?.target.anchorUnavailable}
          <span class="text-meta text-ink-3">
            {#if current.want.target.anchorNextAttemptAt}
              Sample retry {relativeTime(current.want.target.anchorNextAttemptAt)}
            {:else}
              No sample · {current.want.target.anchorUnavailable}
            {/if}
          </span>
        {/if}
        <span class="flex-1"></span>
        {#if current.kind === 'downloaded' && current.want}
          <button
            type="button"
            class="text-meta text-ink-3 hover:text-ink"
            onclick={() => $wrongSong.mutate(current.want!.target.id)}
            disabled={deciding}
          >
            Wrong song
          </button>
        {/if}
      </header>

      <div class="flex flex-col gap-3 px-4 sm:px-6 pb-6 pt-5 lg:px-8">
        {#if failure}
          <p class="text-meta text-fail" role="status">{failure.message}</p>
        {/if}

        {#if current.kind === 'downloaded' && current.want}
          {#if filed}
            {@const copy = filedCopy}
            {#if copy}
              <div class="grid max-w-[64rem] grid-cols-1 justify-start gap-3 sm:grid-cols-[repeat(auto-fill,minmax(17rem,1fr))]">
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
            <div
              class="grid max-w-[64rem] grid-cols-1 justify-start gap-3 sm:grid-cols-[repeat(auto-fill,minmax(17rem,1fr))]"
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
          {/if}
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
              onwithdraw={(decisionId) =>
                $withdrawImport.mutate({ requestId: request.id, decisionId })}
            />
          {/if}
        {/if}
      </div>

      <footer
        class="sticky bottom-14 flex flex-wrap items-center justify-end gap-2 border-t border-line-regular bg-ground px-4 sm:px-6 py-3 lg:bottom-0 lg:px-8"
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
              Remove from Wishlist
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
    </section>
  {/if}
</div>

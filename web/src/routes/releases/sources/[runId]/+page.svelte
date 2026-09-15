<script lang="ts">
  import { createMutation, createQuery, useQueryClient } from '@tanstack/svelte-query';
  import {
    Ban,
    Check,
    ChevronDown,
    ChevronRight,
    Download,
    LoaderCircle,
    RotateCw
  } from '@lucide/svelte';
  import { page } from '$app/state';
  import {
    api,
    ApiError,
    DuplicateProtection,
    type DuplicateEvidence,
    type SourceCandidate,
    type SourceSearchResult
  } from '$lib/api';
  import BackLink from '$lib/components/BackLink.svelte';
  import Button from '$lib/components/Button.svelte';
  import Chip from '$lib/components/Chip.svelte';
  import Card from '$lib/components/Card.svelte';
  import DuplicateNotice from '$lib/components/DuplicateNotice.svelte';
  import { describeError } from '$lib/errors';
  import ErrorNote from '$lib/components/ErrorNote.svelte';
  import PageHeader from '$lib/components/PageHeader.svelte';
  import StateMark from '$lib/components/StateMark.svelte';
  import Settle from '$lib/components/Settle.svelte';

  type Role = 'ok' | 'idle' | 'decide' | 'busy' | 'fail';

  const runId = page.params.runId ?? '';
  const queryClient = useQueryClient();

  // The run is asked for again while anything is still queued. The event stream
  // pushes a notice as each release settles; this is the fallback for when the
  // stream is blocked, so it is unhurried rather than absent.
  const run = createQuery({
    queryKey: ['source-search', runId],
    queryFn: () => api.sourceSearch(runId),
    // A server being restarted and a connection dropped in the tunnel are what
    // usually stops this page, and both are over in a few seconds. So the
    // browser asks again, twice more, waiting longer each time, and says
    // nothing while it does: the placeholder rows stay up and the reader is
    // told only once the tries are spent.
    //
    // A search the server says it does not have is the one answer that will not
    // change, so it is not asked for a second time.
    retry: (spent, error) => !(error instanceof ApiError && error.status === 404) && spent < 2,
    // Every release that settles publishes a notice, and this page is one of
    // the views that notice invalidates. So the interval is the net under a
    // stream that never arrived, and it takes the rate every other net in
    // Schall takes rather than a faster one of its own.
    refetchInterval: (query) => (query.state.data?.pending ? 15_000 : false)
  });

  // Stopping the run withdraws nothing that has been answered, so the response
  // is the run as it now stands and can be shown as-is rather than refetched.
  // A run claims its releases one at a time in creation order, so this is also
  // what lets a correctly-permissioned run start without waiting this one out.
  const cancel = createMutation({
    mutationFn: () => api.cancelSourceSearch(runId),
    onSuccess: (stopped) => queryClient.setQueryData(['source-search', runId], stopped)
  });

  const items = $derived($run.data?.items ?? []);
  const pending = $derived($run.data?.pending ?? 0);
  const cancelled = $derived($run.data?.cancelledCount ?? 0);
  // Added up from the two answers that mean a search happened, rather than
  // taken off the total. A release ends in one of five states, and only "found"
  // and "none" were searched: cancelled means stopped before anybody looked,
  // and failed means looked and the provider did not answer — which is why a
  // failed release wears the chip "not searched" on its own row. Subtracting
  // the states that are not searched left failed inside the figure, so a run of
  // fourteen with three failures read "14 of 14 searched" beside "3 not
  // searched". Adding the two that are cannot drift from the chips again.
  const searched = $derived(($run.data?.foundCount ?? 0) + ($run.data?.noneCount ?? 0));

  // What stopped the page, once the tries above are spent. Three things can
  // stop it, and they lead to three different next steps: the server does not
  // have this search, nothing answered at all, or the server answered with a
  // failure. Every one of the three holds a control, because a screen with
  // nothing to press is a dead end. Asking again only fixes the middle one, so
  // it is the only one whose control asks again; the other two lead to Library,
  // where a search is started.
  type Trouble = {
    /** The word in the header chip, in the lower case the other chips use. */
    chip: string;
    heading: string;
    detail: string;
    /** What to say instead when there is still a run on screen to read. */
    stale: string;
    again: boolean;
    link?: { href: string; label: string };
    /** The server's own words, kept whole and out of the way. */
    said?: string;
  };

  const trouble = $derived.by<Trouble | null>(() => {
    if (!$run.isError) return null;
    const status = $run.error instanceof ApiError ? $run.error.status : -1;

    if (status === 404) {
      return {
        chip: 'not found',
        heading: 'This search is not on the server',
        detail:
          'The link may be an old one, or the releases it covered have been removed. Pick the releases in Library and press Find sources to search again.',
        stale: 'This search has been removed. What is below is the last answer the server gave.',
        again: false,
        link: { href: '/library?status=missing', label: 'Open Library' }
      };
    }

    if (status === 0) {
      return {
        chip: 'no answer',
        heading: 'The server did not answer',
        detail: 'Schall could not be reached. Try again once it is running.',
        stale: 'The server stopped answering. What is below is the last answer it gave.',
        again: true
      };
    }

    // A refusal is about this record of this search, so pressing the same
    // button again asks the same broken question. Picking the releases in
    // Library and searching again is what gets an answer, and the server's own
    // sentence stays under the disclosure for a bug report.
    return {
      chip: 'refused',
      heading: 'The search could not be read',
      detail:
        'The server answered with a failure instead of the search. Pick the releases in Library and press Find sources to search again.',
      stale: 'The server refused the last request. What is below is the answer before it.',
      again: false,
      link: { href: '/library?status=missing', label: 'Open Library' },
      // `describeError` is the one place a failure is turned into words, and
      // `raw` is the part of it that is the server's own sentence. Read from
      // there rather than from the error's own message, which is only the
      // problem document's title and drops the details it came with.
      said: describeError($run.error).raw
    };
  });

  // Which candidate is chosen for each release, and which rows are open. Only a
  // release with candidates has either.
  let chosen = $state<Record<string, number>>({});
  let opened = $state<string[]>([]);

  // What has been requested in this session, and what each request came to. The
  // run itself reports a download already recorded, so this only has to carry
  // what happened since the page was opened.
  let requested = $state<Record<string, string>>({});
  // What a request came to, kept as the thrown error rather than its sentence:
  // the words a reader gets are decided where every other failure is decided,
  // and the server's own sentence is still inside the error for the disclosure.
  let failures = $state<Record<string, unknown>>({});
  let working = $state<string | null>(null);
  // The release whose duplicate question is being asked, and the evidence it was
  // asked with. A refusal is answered per release; there is no bulk answer,
  // because the question is about that release's own library files.
  let duplicate = $state<{ albumId: string; evidence: DuplicateEvidence } | null>(null);

  function pick(result: SourceSearchResult): SourceCandidate | undefined {
    return result.candidates[chosen[result.albumId] ?? 0];
  }

  function alreadyRequested(result: SourceSearchResult) {
    return requested[result.albumId] ?? result.requestedDownloadId;
  }

  function toggle(albumId: string) {
    opened = opened.includes(albumId)
      ? opened.filter((id) => id !== albumId)
      : [...opened, albumId];
  }

  async function order(result: SourceSearchResult, acknowledgeDuplicates = false) {
    const candidate = pick(result);
    if (!candidate) return;
    working = result.albumId;
    failures = { ...failures, [result.albumId]: null };
    try {
      const created = await api.requestDownload(result.albumId, candidate, acknowledgeDuplicates);
      requested = { ...requested, [result.albumId]: created.id };
      duplicate = null;
    } catch (error) {
      // A duplicate refusal is a question, not a failure: it is shown with the
      // evidence behind it and answered before anything is recorded.
      if (error instanceof DuplicateProtection) {
        duplicate = { albumId: result.albumId, evidence: error.evidence };
      } else {
        failures = { ...failures, [result.albumId]: error };
      }
    } finally {
      working = null;
    }
  }

  function statusOf(result: SourceSearchResult): { role: Role; label: string } {
    if (result.status === 'queued') return { role: 'idle', label: 'waiting' };
    if (result.status === 'searching') return { role: 'busy', label: 'searching' };
    // Stopped rather than failed: nobody looked, which is not the same as
    // looking and getting nowhere, and only one of those is worth retrying.
    if (result.status === 'cancelled') return { role: 'idle', label: 'cancelled' };
    if (result.status === 'failed') return { role: 'fail', label: 'not searched' };
    if (result.status === 'none') return { role: 'idle', label: 'no sources' };
    const count = result.candidates.length;
    return { role: 'decide', label: count === 1 ? '1 source' : `${count} to choose from` };
  }

  function size(bytes: number) {
    if (bytes >= 1 << 30) return `${(bytes / (1 << 30)).toFixed(1)} GB`;
    return `${Math.round(bytes / (1 << 20))} MB`;
  }

  function files(count: number) {
    return count === 1 ? '1 file' : `${count} files`;
  }

  function quality(candidate: SourceCandidate) {
    const format = candidate.format ? candidate.format.toUpperCase() : 'unknown format';
    return candidate.averageBitRate ? `${format} · ${candidate.averageBitRate} kbps` : format;
  }

</script>

<svelte:head><title>Sources · Schall</title></svelte:head>

<PageHeader>
  <BackLink
    fallback="/library?status=missing"
    label="Back to the releases this run came from"
    size={15}
    class="inline-flex size-7 shrink-0 items-center justify-center rounded-control text-ink-2 hover:bg-surface-thick hover:text-ink"
  />
  <h1 class="font-display text-2xl font-bold text-ink">Sources</h1>

  <!-- Counts and Stop occupy a reserved row from the first paint. The run
       query fills these slots without adding a second line to the header. -->
  <div class="basis-full flex min-h-8 flex-wrap items-center gap-x-3 gap-y-2">
    {#if trouble && !$run.data}
      <Chip role="fail">{trouble.chip}</Chip>
    {:else if $run.isPending}
      <div
        class="flex min-h-7 items-center gap-3"
        role="status"
        aria-label="Loading source search summary"
      >
        <span class="h-4 w-20 animate-pulse rounded-row bg-surface-regular" aria-hidden="true"></span>
        <span class="h-4 w-24 animate-pulse rounded-row bg-surface-regular" aria-hidden="true"></span>
        <span class="h-4 w-24 animate-pulse rounded-row bg-surface-regular" aria-hidden="true"></span>
      </div>
    {:else}
      <div class="flex flex-wrap items-center gap-x-3 gap-y-1">
        {@render Stat(searched, `of ${$run.data?.total ?? 0} searched`)}
        <span class="hidden h-3 w-px bg-white/14 sm:block"></span>
        {@render Stat($run.data?.foundCount ?? 0, 'with sources')}
        {#if $run.data?.noneCount}
          <span class="hidden h-3 w-px bg-white/14 sm:block"></span>
          {@render Stat($run.data.noneCount, 'with none')}
        {/if}
        {#if $run.data?.failedCount}
          <span class="hidden h-3 w-px bg-white/14 sm:block"></span>
          {@render Stat($run.data.failedCount, 'not searched')}
        {/if}
        {#if cancelled}
          <span class="hidden h-3 w-px bg-white/14 sm:block"></span>
          {@render Stat(cancelled, 'cancelled')}
        {/if}
      </div>
    {/if}

    <div class="ml-auto flex min-h-8 min-w-[11rem] items-center justify-end gap-2">
      {#if pending}
        <Chip role="busy" dot={false}>
          <LoaderCircle size={11} class="animate-spin" />
          {pending} still to search
        </Chip>
        <!-- Stopping keeps every release already answered: the run is claimed
             one release at a time in creation order, so this is how a run
             started with the wrong permission gets out of the way of the one
             that corrects it. -->
        <Button
          variant="outline"
          size="sm"
          class="shrink-0"
          disabled={$cancel.isPending}
          aria-label={`Stop the ${pending} releases still waiting to be searched`}
          onclick={() => $cancel.mutate()}
        >
          {#if $cancel.isPending}
            <LoaderCircle size={11} class="animate-spin" />
          {:else}
            <Ban size={11} strokeWidth={2.3} />
          {/if}
          Stop the rest
        </Button>
      {:else}
        <span class="invisible h-8 w-full" aria-hidden="true"></span>
      {/if}
    </div>
  </div>
</PageHeader>

{#if $cancel.isError}
  <div class="px-4 sm:px-6 pt-4">
    <ErrorNote error={$cancel.error} />
  </div>
{/if}

{#if trouble && $run.data}
  <!-- The run is still here and still worth reading; what stopped is the asking
       after it. Said in one quiet line, with nothing to press: while any release
       is still queued the page asks again by itself. -->
  <p class="px-4 sm:px-6 pt-4 text-meta text-ink-3">{trouble.stale}</p>
{/if}

{#if trouble && !$run.data}
  <div class="max-w-lg px-4 sm:px-6 py-6">
    <Card>
      <div class="flex items-center gap-2.5">
        <StateMark role="fail">!</StateMark>
        <h2 class="font-display text-lead font-bold text-ink">{trouble.heading}</h2>
      </div>
      <p class="text-meta leading-[1.65] text-ink-2">{trouble.detail}</p>

      {#if trouble.said}
        <!-- The server's sentence, whole and behind the press. The line above
             says what happened; this is what a bug report has to quote. -->
        <details class="group border-t border-line-thin pt-2">
          <!-- `tap-tall` rather than `tap`: this press reveals the sentence the
               reader came for, and both the words and the chevron are 12px. The
               padding is taken inside the summary because an invisible box over
               it is clipped by anything that hides its overflow. -->
          <summary
            class="tap-tall flex cursor-pointer list-none items-center gap-1.5 text-meta text-ink-3 transition hover:text-ink-2 [&::-webkit-details-marker]:hidden"
          >
            <ChevronRight size={12} class="transition-transform group-open:rotate-90" />
            What went wrong
          </summary>
          <div
            class="reveal mt-1 rounded-row bg-surface-regular px-2.5 py-2 text-meta leading-[1.5] break-words whitespace-pre-wrap text-ink-3"
          >
            {trouble.said}
          </div>
        </details>
      {/if}

      <!-- One control, and never the same one twice: a server that was not
           answering may be answering now, so that screen is the only one where
           pressing again is the thing to do. A search the server does not have
           is not found by asking a second time, so that screen leads to the
           place a new one is started instead. -->
      {#if trouble.again}
        <Button class="w-fit" disabled={$run.isFetching} onclick={() => $run.refetch()}>
          {#if $run.isFetching}
            <LoaderCircle size={13} class="animate-spin" />
          {:else}
            <RotateCw size={13} strokeWidth={2.3} />
          {/if}
          Try again
        </Button>
      {:else if trouble.link}
        <Button class="w-fit" href={trouble.link.href}>{trouble.link.label}</Button>
      {/if}
    </Card>
  </div>
{:else}
  <Settle pending={$run.isPending}>
    {#snippet placeholder()}
      <div
    class="max-h-[calc(100dvh-9rem)] flex flex-col gap-2 overflow-hidden px-4 sm:px-6 py-6"
    role="status"
    aria-label="Loading source results"
  >
    {#each Array(40) as _, placeholderIndex (placeholderIndex)}
      <div class="h-48 animate-pulse rounded-card bg-surface-regular" aria-hidden="true"></div>
    {/each}
      </div>
    {/snippet}
  <div class="flex flex-col gap-2 px-4 sm:px-6 py-5">
    {#each items as result (result.albumId)}
      {@const status = statusOf(result)}
      {@const best = pick(result)}
      {@const open = opened.includes(result.albumId)}
      {@const done = alreadyRequested(result)}
      <section class="flex min-h-48 flex-col gap-3 rounded-card border border-line-thin p-3">
        <div class="flex flex-wrap items-center gap-x-3 gap-y-2">
          <span class="flex min-w-0 flex-1 flex-col gap-1">
            <span class="truncate text-body font-semibold leading-tight text-ink">
              {result.albumTitle}
            </span>
            <span class="truncate text-meta text-ink-3">
              {result.artistName}
              {#if result.trackCount}
                · {result.ownedTrackCount} of {result.trackCount} owned
              {/if}
            </span>
          </span>

          <Chip role={status.role} dot={result.status !== 'searching'} class="shrink-0">
            {#if result.status === 'searching'}
              <LoaderCircle size={9} class="animate-spin" />
            {/if}
            {status.label}
          </Chip>
        </div>

        {#if result.status === 'failed'}
          <!-- The card is already a surface; the message inside it separates by
               its tint rather than by a second border. -->
          <div class="flex flex-wrap items-center gap-x-3 gap-y-2 rounded-row bg-fail/14 px-3 py-2">
            <StateMark role="fail">!</StateMark>
            <span class="text-meta leading-relaxed text-ink">
              {result.error ?? 'The search could not be run.'}
            </span>
            <Button
              href={`/releases/${result.albumId}?sources=1`}
              variant="outline"
              size="sm"
              class="ml-auto shrink-0"
            >
              Find sources
            </Button>
          </div>
        {:else if result.status === 'cancelled'}
          <div class="flex flex-wrap items-center gap-x-3 gap-y-2 rounded-row bg-surface-thick px-3 py-2">
            <span class="text-meta text-ink-2">
              The run was stopped before this release was searched.
            </span>
            <Button
              href={`/releases/${result.albumId}?sources=1`}
              variant="outline"
              size="sm"
              class="ml-auto shrink-0"
            >
              Find sources
            </Button>
          </div>
        {:else if result.status === 'none'}
          <div class="flex flex-wrap items-center gap-x-3 gap-y-2 rounded-row bg-surface-thick px-3 py-2">
            <span class="text-meta text-ink-2">
              {#if result.refused.length > 0}
                Copies were on offer, but none matched your format settings.
              {:else}
                No peer was sharing this while the search ran.
              {/if}
            </span>
            <span class="text-meta text-ink-4">searched for <span class="numeric">"{result.query}"</span></span>
            <Button
              href={`/releases/${result.albumId}?sources=1`}
              variant="outline"
              size="sm"
              class="ml-auto shrink-0"
            >
              Search again
            </Button>
          </div>

          {@render Refused(result)}
        {:else if result.status === 'found' && best}
          <div class="flex flex-col gap-px overflow-hidden rounded-panel bg-line-thin">
            {@render Candidate(result, best, chosen[result.albumId] ?? 0, true)}

            {#if open}
              {#each result.candidates as candidate, index (candidate.username + candidate.directory)}
                {#if index !== (chosen[result.albumId] ?? 0)}
                  {@render Candidate(result, candidate, index, false)}
                {/if}
              {/each}
            {/if}
          </div>

          <div class="flex flex-wrap items-center gap-x-3 gap-y-2">
            {#if result.candidates.length > 1}
              <button
                class="inline-flex items-center gap-1 text-meta font-medium text-ink-2 transition hover:text-ink"
                onclick={() => toggle(result.albumId)}
                aria-expanded={open}
              >
                {#if open}
                  <ChevronDown size={13} strokeWidth={2} />
                  Hide the other {result.candidates.length - 1}
                {:else}
                  <ChevronRight size={13} strokeWidth={2} />
                  Compare {result.candidates.length - 1} more
                {/if}
              </button>
            {/if}

            <span class="text-meta text-ink-4">searched for <span class="numeric">"{result.query}"</span></span>

            {#if done}
              <!-- A download nobody remembers asking for has to say where it
                   came from, so a request the run made itself is labelled as
                   one rather than looking like something the user did. -->
              <Chip role="ok" dot={false} class="ml-auto">
                <Check size={11} strokeWidth={3} />
                {result.autoRequestedAt ? 'requested automatically' : 'requested'}
              </Chip>
              <Button href="/downloads" variant="outline" size="sm" class="shrink-0">
                Open downloads
              </Button>
            {:else}
              <Button
                size="sm"
                class="ml-auto"
                disabled={working === result.albumId}
                onclick={() => order(result)}
              >
                {#if working === result.albumId}
                  <LoaderCircle size={12} class="animate-spin" />
                {:else}
                  <Download size={12} strokeWidth={2.3} />
                {/if}
                Request this source
              </Button>
            {/if}
          </div>

          {#if failures[result.albumId]}
            <!-- The request is something the reader pressed for, so the note
                 never asks again by itself. -->
            <ErrorNote error={failures[result.albumId]} border={false} class="py-2" />
          {/if}

          {#if duplicate?.albumId === result.albumId}
            <DuplicateNotice
              evidence={duplicate.evidence}
              pending={working === result.albumId}
              onconfirm={() => order(result, true)}
              oncancel={() => (duplicate = null)}
            />
          {/if}

          <!-- Shown beside a result as well as instead of one: a release found
               in FLAC may still have had four MP3s turned away, and the reader
               choosing between copies is owed the ones that are not on the
               list. -->
          {@render Refused(result)}
        {/if}
      </section>
    {/each}
  </div>
  </Settle>
{/if}

<!-- What the user's own format settings kept out of this search. A release whose
     every offer was under the bitrate floor found nothing, and reading that as an
     empty network sends somebody to look in the wrong place. The count is the
     line; the copies themselves are behind the press. -->
{#snippet Refused(result: SourceSearchResult)}
  {#if result.refused.length > 0}
    <details class="group border-t border-line-thin pt-2">
      <summary
        class="tap-tall flex cursor-pointer list-none items-center gap-1.5 text-meta text-ink-3 transition hover:text-ink-2 [&::-webkit-details-marker]:hidden"
      >
        <ChevronRight size={12} class="transition-transform group-open:rotate-90" />
        {#if result.refusedBelowBitRate === result.refused.length}
          {result.refused.length}
          {result.refused.length === 1 ? 'copy' : 'copies'} below your minimum bit rate
        {:else}
          {result.refused.length}
          {result.refused.length === 1 ? 'copy' : 'copies'} outside your format settings
        {/if}
      </summary>
      <div class="reveal mt-1 flex flex-col gap-1">
        {#each result.refused as refusal (refusal.username + refusal.directory)}
          <div class="flex flex-wrap items-baseline gap-x-2 text-meta leading-[1.5] text-ink-3">
            <span class="font-medium text-ink-2">{refusal.username}</span>
            <span class="numeric break-all text-ink-4">{refusal.directory}</span>
            <span class="ml-auto shrink-0">{refusal.reason}</span>
          </div>
        {/each}
        {#if result.refusedBelowBitRate === result.refused.length}
          <a
            href="/settings#bitrate-floor"
            class="tap w-fit text-meta font-medium text-ink-2 underline-offset-2 hover:underline"
          >
            Change your minimum bit rate
          </a>
        {:else}
          <a href="/settings" class="tap w-fit text-meta font-medium text-ink-2 underline-offset-2 hover:underline">
            Change the formats you accept
          </a>
        {/if}
      </div>
    </details>
  {/if}
{/snippet}

{#snippet Candidate(
  result: SourceSearchResult,
  candidate: SourceCandidate,
  index: number,
  isChosen: boolean
)}
  <!-- The rows are clipped by the rounded box they sit in, which is what makes
       one list out of them and what would clip an invisible touch box back to
       the height it was escaping. So the room a fingertip needs is taken as
       padding on a coarse pointer, where it grows the row, and a mouse keeps
       the density it is accurate enough for. -->
  <button
    class="flex flex-wrap items-center gap-x-3 gap-y-2 bg-ground px-3 py-2 text-left transition hover:bg-surface-thick pointer-coarse:py-3"
    onclick={() => (chosen = { ...chosen, [result.albumId]: index })}
    aria-pressed={isChosen}
    aria-label={`${quality(candidate)} from ${candidate.username}, ${files(
      candidate.trackCount
    )}. ${candidate.match.summary}`}
  >
    <span class="grid size-6 shrink-0 place-items-center">
      <span
        class="grid size-[15px] place-items-center rounded-full {isChosen
          ? 'bg-accent'
          : 'border border-line-thick'}"
      >
        {#if isChosen}<span class="size-[5px] rounded-full bg-accent-ink"></span>{/if}
      </span>
    </span>

    <span class="flex min-w-0 flex-1 flex-col gap-1">
      <span class="flex flex-wrap items-baseline gap-x-2 gap-y-0.5">
        <span class="text-body font-medium text-ink">{quality(candidate)}</span>
        <span class="numeric text-meta text-ink-3">
          {files(candidate.trackCount)} · {size(candidate.totalSizeBytes)}
        </span>
      </span>
      <span class="numeric truncate text-meta text-ink-4">
        {candidate.username}{candidate.directory ? ` · ${candidate.directory}` : ''}
      </span>
    </span>

    <!-- Whether this is the right release at all is what the decision turns on,
         so it is shown at every width. It wraps onto its own line on a narrow
         screen rather than being hidden, which is where the choice is hardest.
         The quality reasons stay behind the expander: how good a copy is only
         matters once the music is known to be the right music. -->
    <span
      class="order-last w-full truncate text-meta lg:order-none lg:w-auto lg:max-w-[20rem] {candidate
        .match.complete
        ? 'text-ok'
        : 'text-ink-2'}"
    >
      {candidate.match.summary}
    </span>

    <!-- The score is not confidence: it measures how good a copy is and knows
         nothing about which release it holds. Showing it as a number invited
         reading it as certainty, so what is shown is the evidence instead. -->
    <span class="flex shrink-0 items-center gap-1.5">
      {#if candidate.match.complete}
        <Chip role="ok">confirmed</Chip>
      {:else if candidate.match.checked}
        <Chip class="numeric">{candidate.match.confirmed}/{candidate.match.expected}</Chip>
      {:else}
        <Chip>unchecked</Chip>
      {/if}
    </span>
  </button>
{/snippet}

{#snippet Stat(value: number, label: string)}
  <span class="flex items-baseline gap-1.5">
    <span class="numeric min-w-[2ch] text-body font-semibold text-ink">{value.toLocaleString()}</span>
    <span class="text-meta font-medium text-ink-3">{label}</span>
  </span>
{/snippet}

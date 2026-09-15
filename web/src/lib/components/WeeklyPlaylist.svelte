<script lang="ts">
  // The weekly playlist: the songs Schall fetched for this week, and what
  // becomes of each one.
  //
  // Once a week a refresh picks a few suggestions, obtains them, and puts them
  // on a playlist the reader's music player can see. A song the reader starred
  // in that player, or pressed Keep on here, stays. A song nobody kept is
  // announced for removal — `leaving` — and the next refresh deletes it.
  //
  // A deletion is the one thing this product does that cannot be taken back, so
  // the week of notice is what this screen is built around: the leaving rows are
  // sorted to the top, each one names the date it goes, each one carries the
  // Keep button that stops it, and the account of what was read sits behind a
  // press on the row itself.
  import {
    createMutation,
    createQuery,
    queryOptions,
    useQueryClient
  } from '@tanstack/svelte-query';
  import { toStore } from 'svelte/store';
  import { ChevronRight } from '@lucide/svelte';
  import { api, type WeeklyKeepRead, type WeeklyLease } from '$lib/api';
  import { isAuthError } from '$lib/errors';
  import { relativeTime } from '$lib/utils';
  import Button from '$lib/components/Button.svelte';
  import Chip from '$lib/components/Chip.svelte';
  import EmptyPanel from '$lib/components/EmptyPanel.svelte';
  import ErrorNote from '$lib/components/ErrorNote.svelte';
  import Settle from '$lib/components/Settle.svelte';

  const queryClient = useQueryClient();

  const weekly = createQuery({
    queryKey: ['weekly'],
    queryFn: api.weekly,
    // A refresh announces itself over the event stream. The interval is the
    // safety net and runs only while one is still going.
    refetchInterval: (query) =>
      isAuthError(query.state.error)
        ? false
        : (query.state.data?.runs ?? []).some((run) => run.status === 'running')
          ? 15_000
          : false
  });

  const settings = $derived($weekly.data?.settings);
  const runs = $derived($weekly.data?.runs ?? []);
  const removing = $derived(settings?.mode === 'remove');

  // Leaving first. A person opening this page is here for the songs that are
  // about to go, and the rest of the week can wait below them.
  const rank = { leaving: 0, held: 1 } as const;
  const songs = $derived(
    [...($weekly.data?.leases ?? [])].sort((a, b) => rank[a.state] - rank[b.state])
  );

  // Which row's account is open. One at a time, and the read is asked for only
  // once a row has been opened: the whole point of the disclosure is that the
  // reader who is not asking pays nothing for it.
  let openLease = $state<string | null>(null);

  const evidence = createQuery(
    toStore(() =>
      queryOptions({
        queryKey: ['weekly-evidence', openLease],
        queryFn: () => api.weeklyLeaseReads(String(openLease)),
        enabled: openLease !== null
      })
    )
  );

  const reads = $derived($evidence.data?.items ?? []);


  // What came of the last press, held against the row it was pressed on. A
  // failure is said at the row rather than at the top, because the reader is
  // looking at one song and the sentence is about that song.
  let refused = $state<{ id: string; error: unknown } | null>(null);

  const keep = createMutation({
    mutationFn: (lease: WeeklyLease) => api.keepWeeklySong(String(lease.libraryFileId)),
    onSuccess: async () => {
      refused = null;
      await queryClient.invalidateQueries({ queryKey: ['weekly'] });
    },
    onError: (error: Error, lease: WeeklyLease) => {
      refused = { id: lease.id, error };
    }
  });

  // The day a song goes, or the day its week is up. A date rather than a
  // countdown: nothing on this screen ticks, and a day is what a person plans
  // around.
  function day(value: string | null) {
    if (!value) return '';
    const at = new Date(value);
    if (!Number.isFinite(at.getTime())) return '';
    return at.toLocaleDateString(undefined, { day: 'numeric', month: 'long' });
  }

  function chip(lease: WeeklyLease) {
    if (lease.state === 'leaving') {
      const date = day(lease.removesAt);
      if (!date) return removing ? 'leaving' : 'would leave';
      return removing ? `leaving ${date}` : `would leave ${date}`;
    }
    const date = day(lease.expiresAt);
    return date ? `until ${date}` : 'on the list';
  }

  // What one refresh did, in the order the work happens. The last two are left
  // out when they are zero: an installation that set no mix and lost no slot
  // would otherwise read two numbers every week that never move.
  function account(run: (typeof runs)[number]) {
    const parts = [
      `chose ${run.chosen}`,
      `arrived ${run.arrived}`,
      `kept ${run.kept}`,
      `leaving ${run.leaving}`,
      `removed ${run.removed}`
    ];
    if (run.library > 0) parts.push(`library ${run.library}`);
    if (run.replaced > 0) parts.push(`replaced ${run.replaced}`);
    return parts.join(' · ');
  }

  const runRoles = {
    running: 'busy',
    complete: 'ok',
    partial: 'fail',
    failed: 'fail'
  } as const;

  // The signals, named as the reader meets them rather than as they are stored.
  const signals: Record<WeeklyKeepRead['signal'], string> = {
    navidrome_star: 'Star in your player',
    schall_keep: 'Keep in Schall'
  };

  // What one answer said. `unreadable` is not `absent`: nothing was found
  // because nothing could be asked, and the detail beside it says what stopped.
  const answers: Record<WeeklyKeepRead['outcome'], { role: 'ok' | 'idle' | 'fail'; word: string }> =
    {
      kept: { role: 'ok', word: 'keeps it' },
      absent: { role: 'idle', word: 'nothing found' },
      unreadable: { role: 'fail', word: 'could not be read' }
    };
</script>

<div class="flex flex-col gap-4 px-6 py-4">
  <!-- Only a read, so the note asks again by itself. -->
  {#if $weekly.error}
    <ErrorNote error={$weekly.error} retry={() => $weekly.refetch()} />
  {/if}

  <Settle pending={$weekly.isPending}>
    {#snippet placeholder()}
      <div
      class="flex max-h-[calc(100dvh-12rem)] flex-col gap-3 overflow-hidden rounded-panel border border-line-thin p-4"
      aria-busy="true"
      aria-label="Loading weekly playlist"
    >
      <div class="flex items-center gap-2.5">
        <span class="label">This week</span>
        <span class="h-4 w-48 animate-pulse rounded-row bg-surface-thick" aria-hidden="true"></span>
      </div>
      <div class="flex flex-wrap gap-2" role="group" aria-label="Loading weekly settings">
        <span class="h-8 w-36 animate-pulse rounded-row bg-surface-thick" aria-hidden="true"></span>
        <span class="h-8 w-36 animate-pulse rounded-row bg-surface-thick" aria-hidden="true"></span>
      </div>
      <div class="-mx-4 overflow-hidden border-y border-line-thin" aria-label="Loading weekly songs">
        <!-- Fill about 2016px of rows so a 4K viewport does not outgrow the wait. -->
        {#each Array.from({ length: 60 }) as _}
          <div aria-hidden="true" class="h-14 animate-pulse border-b border-line-thin last:border-b-0"></div>
        {/each}
      </div>
      <div class="flex flex-col gap-2" aria-label="Loading weekly history">
        <span class="label">What each refresh did</span>
        <!-- Fill about 2016px of rows so a 4K viewport does not outgrow the wait. -->
        {#each Array.from({ length: 60 }) as _}
          <div aria-hidden="true" class="h-10 animate-pulse border-b border-line-thin last:border-b-0"></div>
        {/each}
      </div>
      </div>
    {/snippet}
  {#if settings && !settings.enabled}
    <!-- Off is the whole screen. A table of songs nobody is being offered would
         be an answer to a question the reader did not ask. -->
    <EmptyPanel role="idle" heading="Weekly playlist is off">
      <span class="text-meta leading-5 text-ink-2">Turn it on under Settings.</span>
      <Button href="/settings/automation" variant="outline" size="sm" class="w-fit">
        Open settings
      </Button>
    </EmptyPanel>
  {:else if settings}
    <section class="flex flex-col gap-2.5">
      <div class="flex flex-wrap items-center gap-2.5">
        <span class="label">This week</span>
        <span class="hidden h-px flex-1 bg-line-thin sm:block"></span>
        <span class="text-meta text-ink-3">
          {removing ? 'unkept songs are deleted' : 'reporting only · nothing is deleted'}
        </span>
      </div>

      {#if removing}
        <p class="max-w-[64ch] text-meta leading-5 text-ink-2">
          Press <span class="text-ink">Keep</span> to stop a song leaving.
        </p>
      {/if}

      {#if songs.length === 0}
        <EmptyPanel role="idle" heading="No songs on the list yet">
          <span class="text-meta leading-5 text-ink-2">
            The next refresh picks some from your suggestions.
          </span>
        </EmptyPanel>
      {:else}
        <section class="rounded-panel border border-line-thin">
          {#each songs as lease (lease.id)}
            <div class="flex flex-col gap-1.5 border-b border-line-thin px-3 py-2 last:border-b-0">
              <div class="flex flex-wrap items-center gap-x-3 gap-y-1">
                <span class="flex min-w-0 flex-1 flex-col gap-0.5">
                  <span class="truncate text-body font-medium text-ink">{lease.title}</span>
                  <span class="truncate text-meta text-ink-3">{lease.artist}</span>
                  <!-- Why this song's date moved. Without it a week that grew
                       reads as a date somebody typed. -->
                  {#if lease.extensions > 0 && lease.extendedReason}
                    <span class="text-meta leading-4 text-ink-4">{lease.extendedReason}</span>
                  {/if}
                </span>

                <Chip role={lease.state === 'leaving' ? 'decide' : 'idle'} class="shrink-0">
                  {chip(lease)}
                </Chip>

                {#if lease.libraryFileId}
                  <Button
                    variant="outline"
                    size="sm"
                    class="shrink-0"
                    disabled={$keep.isPending}
                    onclick={() => $keep.mutate(lease)}
                  >
                    Keep
                  </Button>
                {:else}
                  <!-- No control, because there is no file left to keep. -->
                  <span class="shrink-0 text-meta text-ink-4">file already gone</span>
                {/if}
              </div>

              <!-- The row is what failed, so the failure is said at the row. The
                   second sentence is this screen's own, because it can name the
                   button the reader just pressed and say that the song is still
                   there. Bare: the row draws no band of its own. -->
              {#if refused?.id === lease.id}
                <ErrorNote
                  error={refused.error}
                  action="Nothing has been deleted, so press Keep again."
                  bare
                />
              {/if}

              <!--
                The account behind the announcement, behind one press. It is
                asked for when it is opened and never before: a week is twenty
                rows, and reading all of them to draw a line nobody looked at
                would be twenty requests for one glance.
              -->
              {#if lease.state === 'leaving'}
                <details
                  class="group"
                  open={openLease === lease.id}
                  ontoggle={(event) => {
                    if (event.currentTarget.open) openLease = lease.id;
                    else if (openLease === lease.id) openLease = null;
                  }}
                >
                  <summary
                    class="tap-tall flex w-fit cursor-pointer list-none items-center gap-1.5 text-meta text-ink-3 transition hover:text-ink-2 [&::-webkit-details-marker]:hidden"
                  >
                    <ChevronRight size={12} class="transition-transform group-open:rotate-90" />
                    Why it's leaving
                  </summary>

                  {#if openLease === lease.id}
                    <div class="reveal mt-1 flex flex-col gap-1.5 rounded-row bg-surface-regular px-2.5 py-2">
                      {#if $evidence.isPending}
                        <div aria-busy="true" aria-label="Loading weekly evidence" class="max-h-[calc(100dvh-14rem)] overflow-hidden">
                          <span class="text-meta text-ink-3">reading what was asked…</span>
                          {#each Array.from({ length: 40 }) as _}
                            <div
                              aria-hidden="true"
                              class="mt-1.5 h-5 animate-pulse rounded-row bg-surface-thick"
                            ></div>
                          {/each}
                        </div>
                      {:else if $evidence.error}
                        <span class="text-meta leading-4 text-ink-2">
                          What was read could not be loaded. Close this and open it again.
                        </span>
                      {:else if reads.length === 0}
                        <span class="text-meta leading-4 text-ink-2">
                          Nothing was read about this song.
                        </span>
                      {:else}
                        {#each reads as read, index (index)}
                          <div class="flex flex-wrap items-center gap-x-2.5 gap-y-1">
                            <span class="text-meta text-ink-2">
                              {signals[read.signal] ?? read.signal}
                            </span>
                            <Chip role={answers[read.outcome].role}>
                              {answers[read.outcome].word}
                            </Chip>
                            <span class="ml-auto text-meta text-ink-4">
                              read {relativeTime(read.readAt)}
                            </span>
                            {#if read.detail}
                              <span class="w-full text-meta leading-4 text-ink-3">
                                {read.detail}
                              </span>
                            {/if}
                          </div>
                        {/each}
                      {/if}
                    </div>
                  {/if}
                </details>
              {/if}
            </div>
          {/each}
        </section>
      {/if}
    </section>

    <!-- The record of what the refreshes did. It is a record and not the point
         of the page, so it is one line each and it is last. -->
    {#if runs.length > 0}
      <section class="flex flex-col gap-2.5">
        <div class="flex flex-wrap items-center gap-2.5">
          <span class="label">What each refresh did</span>
          <span class="hidden h-px flex-1 bg-line-thin sm:block"></span>
        </div>

        <section class="rounded-panel border border-line-thin">
          {#each runs as run (run.id)}
            <div
              class="flex flex-wrap items-center gap-x-3 gap-y-1 border-b border-line-thin px-3 py-2 last:border-b-0"
            >
              <span class="shrink-0 text-meta text-ink-2">{relativeTime(run.startedAt)}</span>
              <span class="numeric min-w-0 flex-1 text-meta text-ink-3">{account(run)}</span>
              {#if run.status !== 'complete'}
                <Chip role={runRoles[run.status]} class="shrink-0">{run.status}</Chip>
              {/if}
              {#if run.detail && (run.status === 'partial' || run.status === 'failed')}
                <span class="w-full text-meta leading-4 text-ink-3">{run.detail}</span>
              {/if}
            </div>
          {/each}
        </section>
      </section>
    {/if}
  {/if}
  </Settle>
</div>

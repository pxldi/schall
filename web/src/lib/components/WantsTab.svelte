<script lang="ts">
  import { toStore } from 'svelte/store';
  import {
    createMutation,
    createQuery,
    keepPreviousData,
    useQueryClient,
    type Query
  } from '@tanstack/svelte-query';
  import { Check } from '@lucide/svelte';
  import { api, lookingFor, type AcquisitionTarget, type AcquisitionTargets } from '$lib/api';
  import { relativeTime } from '$lib/utils';
  import Button from '$lib/components/Button.svelte';
  import Chip from '$lib/components/Chip.svelte';
  import EmptyPanel from '$lib/components/EmptyPanel.svelte';
  import ErrorNote from '$lib/components/ErrorNote.svelte';
  import Pager from '$lib/components/Pager.svelte';
  import Segmented from '$lib/components/Segmented.svelte';
  import Settle from '$lib/components/Settle.svelte';
  import StatusBadge from '$lib/components/StatusBadge.svelte';
  import UseAddress from '$lib/components/UseAddress.svelte';

  // A want is a recording somebody asked Schall to find. Between being asked
  // for and either arriving or raising a question it appears on no other
  // screen: Review shows the ones with a question, Downloads the ones with a
  // transfer, and a want the loop is quietly still looking for has neither. It
  // is shown here so that pressing "Want it" has somewhere to point at.
  //
  // Nothing on this tab decides anything about a file. Stopping is a decision
  // about pursuit alone — the recording is left alone, a copy already in the
  // library stays — and it is taken back where it was taken.

  type Role = 'ok' | 'idle' | 'decide' | 'busy' | 'fail';
  type Pile = 'looking' | 'stopped';

  const queryClient = useQueryClient();
  const pageSize = 25;

  let pile = $state<Pile>('looking');
  let offset = $state(0);

  const statuses: Record<Pile, readonly string[]> = {
    looking: lookingFor,
    stopped: ['not_wanted']
  };

  // The pile and the page are in the key rather than in a refetch after the
  // fact, so asking for another pile is asking a different question and not the
  // same one again. Refetching a fixed key does nothing at all while the first
  // request is still in flight — the press is swallowed and the filter then
  // names a pile the list under it is not showing.
  const wants = createQuery(
    toStore(() => ({
      queryKey: ['wants', 'list', pile, offset],
      queryFn: () => api.wants({ statuses: statuses[pile], limit: pageSize, offset }),
      placeholderData: keepPreviousData,
      // The loop moves these on its own and says so over the event stream, so
      // this is the safety net rather than the mechanism. It only runs while
      // something on the page is actually searching; the stopped pile never
      // moves on its own, so it gets no poll at all.
      refetchInterval: (query: Query<AcquisitionTargets>) =>
        query.state.data?.items.some((item) => item.status === 'searching') ? 60_000 : false
    }))
  );

  // A figure on the pile nobody is looking at has to be right, so each is asked
  // for rather than counted out of a list that was never fetched. One row each:
  // what is read here is how many there are, not which. The looking figure is
  // the tab badge's own key, so that request is made once for both.
  const looking = createQuery({
    queryKey: ['wants', 'looking', 'count'],
    queryFn: () => api.wants({ statuses: lookingFor, limit: 1 })
  });
  const stopped = createQuery({
    queryKey: ['wants', 'stopped', 'count'],
    queryFn: () => api.wants({ statuses: statuses.stopped, limit: 1 })
  });

  const items = $derived($wants.data?.items ?? []);
  const total = $derived($wants.data?.total ?? 0);
  // The one line the list itself carries. The API sends it only while wants on
  // this page are waiting and nothing on the installation could prove a copy is
  // the recording that was asked for, so its presence is the whole condition.
  const notice = $derived($wants.data?.notice ?? '');

  // A want being looked for explains itself in a sentence the loop wrote on it.
  // It is shown on the two states where a reader is waiting for something to
  // happen; on the rest the chip and the timing already say everything. A
  // want waiting on the reader already has the sentence as its detail line,
  // so it is not said twice.
  function waiting(want: AcquisitionTarget) {
    return (
      (want.status === 'pending' || want.status === 'searching') &&
      Boolean(want.summary) &&
      !waitingOnYou(want)
    );
  }

  function sampleStatus(want: AcquisitionTarget) {
    if (!want.anchorUnavailable) return '';
    if (want.anchorNextAttemptAt) return `Sample retry ${relativeTime(want.anchorNextAttemptAt)}`;
    return `No sample · ${want.anchorUnavailable}`;
  }

  const piles = $derived([
    { value: 'looking', name: 'Being looked for', count: $looking.data?.total },
    { value: 'stopped', name: 'Not wanted', count: $stopped.data?.total }
  ]);

  function show(next: Pile) {
    pile = next;
    offset = 0;
  }

  async function refresh() {
    await queryClient.invalidateQueries({ queryKey: ['wants'] });
  }

  const stop = createMutation({
    mutationFn: (targetId: string) => api.stopPursuingTarget(targetId),
    onSuccess: refresh
  });
  const resume = createMutation({
    mutationFn: (targetId: string) => api.pursueTargetAgain(targetId),
    onSuccess: refresh
  });
  const takeBest = createMutation({
    mutationFn: (targetId: string) => api.takeBestAvailable(targetId),
    onSuccess: refresh
  });
  const keepFloor = createMutation({
    mutationFn: (targetId: string) => api.keepAcquisitionFloor(targetId),
    onSuccess: refresh
  });

  // What is happening to this want, in the words the reader would use for it.
  // "Identifying" is the entry that has not been tied to a recording yet: the
  // sub-line says which, so the chip does not have to.
  function standing(want: AcquisitionTarget): { role: Role; text: string } {
    // A want that is still wanted and has no next look has stopped: its copy is
    // in the library under another recording, and only a person can say which.
    // It must not read as "Looking" — nothing is looking for it.
    if (waitingOnYou(want)) return { role: 'decide', text: 'Needs you' };
    switch (want.status) {
      case 'unresolved':
        return { role: 'busy', text: 'Identifying' };
      case 'searching':
        return { role: 'busy', text: 'Searching' };
      case 'not_wanted':
        return { role: 'idle', text: 'Not wanted' };
      case 'acquired':
        return { role: 'ok', text: 'In your library' };
      default:
        return { role: 'busy', text: 'Looking' };
    }
  }

  const origins: Record<string, string> = {
    manual: 'asked for by hand',
    playlist: 'from a playlist',
    follow_feed: 'from an artist you follow',
    label_feed: 'from a label you follow',
    recommendation: 'from a suggestion',
    upgrade: 'an automatic quality upgrade'
  };

  // The last path segment, for naming the file an upgrade want exists to
  // replace without showing its full path.
  function fileName(path: string) {
    const parts = path.split('/');
    return parts[parts.length - 1] || path;
  }

  function name(want: AcquisitionTarget) {
    return want.artist ? `${want.artist} — ${want.title}` : want.title;
  }

  // A want nothing is looking for and nothing will: it holds a copy that is
  // already a file, and the library calls that file a different recording. The
  // server says so rather than this being worked out from the times, because a
  // want with no next look might equally be one nothing has reached yet.
  function waitingOnYou(want: AcquisitionTarget) {
    return want.waitingOnYou === true;
  }

  // Where the want came from, and what has been tried for it. Three facts at
  // most: any more and the line is read as a paragraph and skipped.
  function detail(want: AcquisitionTarget) {
    // What stopped it, in its own words. On every other row this line counts
    // what has been tried, which is the wrong thing to read on the one row
    // where nothing is being tried.
    if (waitingOnYou(want)) return want.summary;
    const parts = [
      want.origin === 'upgrade' && want.upgradeOfPath
        ? `upgrade of ${fileName(want.upgradeOfPath)}`
        : (origins[want.origin] ?? 'asked for by hand')
    ];
    if (want.album) parts.push(want.album);
    if (want.externalUrl) parts.push('keyed');
    else if (want.status === 'unresolved') parts.push('waiting to be matched to a recording');
    else if (want.attempts === 0) parts.push('no copy looked at yet');
    else parts.push(want.attempts === 1 ? 'one copy looked at' : `${want.attempts} copies looked at`);
    if (want.minimumBitrate) parts.push(`${want.minimumBitrate} kbit/s`);
    return parts.join(' · ');
  }

  // When something last happened, and when the next thing will. A want with
  // neither has been recorded and not yet reached, which is its own answer.
  function timing(want: AcquisitionTarget) {
    if (want.status === 'not_wanted') return '';
    if (waitingOnYou(want)) return 'waiting for you';
    if (want.lastAttemptAt) return `tried ${relativeTime(want.lastAttemptAt)}`;
    if (want.nextAttemptAt) return `next look ${relativeTime(want.nextAttemptAt)}`;
    return 'not looked for yet';
  }

  const chips: Record<Role, string> = {
    ok: 'bg-ok/14 text-ok',
    idle: 'bg-idle/14 text-idle',
    decide: 'bg-decide/14 text-decide',
    busy: 'bg-busy/14 text-busy',
    fail: 'bg-fail/14 text-fail'
  };

  const emptyHeadings: Record<Pile, string> = {
    looking: 'Nothing is being looked for',
    stopped: 'Nothing is marked not wanted'
  };
</script>

<div class="layout-width flex flex-col gap-4 px-6 py-5">
  <Segmented
    options={piles}
    value={pile}
    onchange={(value) => show(value as Pile)}
    label="Filter by what is happening to the want"
    pending={$looking.isPending || $stopped.isPending}
  />

  <div class="h-14">
    {#if notice}
      <StatusBadge role="idle" wrap class="h-full overflow-y-auto">
        <span class="text-meta leading-relaxed text-ink">{notice}</span>
      </StatusBadge>
    {/if}
  </div>

  {#if $wants.isError}
    <!-- It is the list that failed, so the failure stands where the list would
         have been rather than above it. -->
    <!-- Bare, and with no heading of its own: the panel is the surface, and the
         note's first sentence already says what could not be read. Only a read,
         so it asks again by itself. -->
    <div class="flex flex-col gap-1.5 rounded-panel border border-line-thin p-5">
      <ErrorNote error={$wants.error} retry={() => $wants.refetch()} bare />
    </div>
  {:else}
    <Settle pending={$wants.isPending}>
      {#snippet placeholder()}
    <!-- Fill the visible viewport. The reserved summary line below is part of
         every want row, including these placeholders. -->
    <div class="flex flex-col overflow-hidden" style="max-height: calc(100dvh - 11rem)">
      {#each Array(40) as _, placeholderIndex (placeholderIndex)}
        <div class="border-b border-line-thin px-3 py-2 last:border-b-0">
          <div class="h-5 animate-pulse rounded-row bg-surface-regular"></div>
          <div class="mt-1 h-4 w-2/3 animate-pulse rounded-row bg-surface-regular"></div>
          <div class="mt-1 h-4 w-1/2 animate-pulse rounded-row bg-surface-regular"></div>
        </div>
      {/each}
    </div>
      {/snippet}
  {#if items.length}
    <div class="flex flex-col">
      {#each items as want (want.id)}
        {@const mark = standing(want)}
        {@const sample = sampleStatus(want)}
        <article class="border-b border-line-thin last:border-b-0">
          <div
            class="flex flex-wrap items-center gap-x-3 gap-y-2 rounded-row px-3 py-2 transition hover:bg-surface-thick md:grid md:grid-cols-[1rem_minmax(0,1fr)_128px_112px_auto] md:gap-y-0"
          >
            <span class="grid size-4 shrink-0 place-items-center rounded-row {chips[mark.role]}">
              {#if mark.role === 'ok'}
                <Check size={10} strokeWidth={3.2} />
              {:else}
                <span class="numeric text-micro font-bold">
                  {mark.role === 'decide'
                    ? '?'
                    : mark.role === 'fail'
                      ? '!'
                      : mark.role === 'busy'
                        ? '·'
                        : '–'}
                </span>
              {/if}
            </span>

            <!-- On a phone the name takes the whole line and everything after
                 it wraps underneath, as it does on the transfers beside it. -->
            <span class="min-w-0 flex-1 basis-full md:basis-auto">
              <span class="block truncate text-body font-medium text-ink" title={name(want)}>
                {name(want)}
              </span>
              <span class="mt-0.5 block truncate text-meta text-ink-3" title={detail(want)}>
                {#if want.externalUrl}
                  <a href={want.externalUrl} class="hover:text-ink underline-offset-2 hover:underline">
                    {detail(want)}
                  </a>
                {:else}
                  {detail(want)}
                {/if}
              </span>
              <span
                class="mt-0.5 block min-h-4 truncate text-meta text-ink-3"
                title={sample || (waiting(want) ? want.summary : undefined)}
              >
                {#if sample}{sample}{:else if waiting(want)}{want.summary}{/if}
              </span>
            </span>

            <!-- On a phone this wraps under the name rather than being dropped.
                 When a want was last looked for is half of what the row is for:
                 without it the reader is back to asking whether anything is
                 happening at all. -->
            <span class="numeric whitespace-nowrap text-meta text-ink-4">{timing(want)}</span>

            <span class="flex min-w-0 items-center">
              <Chip role={mark.role} class="shrink-0">{mark.text}</Chip>
              {#if want.floorWaivedAt}
                <Chip role="idle" class="ml-2 shrink-0">Floor waived</Chip>
              {/if}
            </span>

            <span class="ml-auto flex shrink-0 items-center md:ml-0 md:justify-end">
              {#if want.floorWaivedAt}
                <Button
                  variant="outline"
                  size="xs"
                  disabled={$keepFloor.isPending}
                  onclick={() => $keepFloor.mutate(want.id)}
                >
                  Keep the floor
                </Button>
              {:else if want.status === 'pending' && want.belowFloorSince}
                <Button
                  variant="outline"
                  size="xs"
                  disabled={$takeBest.isPending}
                  onclick={() => $takeBest.mutate(want.id)}
                >
                  Take best available
                </Button>
              {:else if want.status === 'not_wanted'}
                <Button
                  variant="outline"
                  size="xs"
                  disabled={$resume.isPending}
                  onclick={() => $resume.mutate(want.id)}
                >
                  Look again
                </Button>
              {:else}
                <Button
                  variant="ghost"
                  size="xs"
                  disabled={$stop.isPending}
                  onclick={() => $stop.mutate(want.id)}
                >
                  Stop looking
                </Button>
              {/if}
            </span>
          </div>

          {#if want.status === 'unresolved' && !want.source}
            <div class="px-3 pb-2">
              <UseAddress
                targetId={want.id}
                entryTitle={want.title}
                entryArtist={want.artist}
                entryDurationMs={want.durationMs}
              />
            </div>
          {/if}

          <!-- Both actions are pressed on a row, so their failures are reported
               against the row they were pressed on and no other. -->
          {#if $stop.isError && $stop.variables === want.id}
            <div class="px-3 pb-2"><ErrorNote error={$stop.error} /></div>
          {/if}
          {#if $resume.isError && $resume.variables === want.id}
            <div class="px-3 pb-2"><ErrorNote error={$resume.error} /></div>
          {/if}
          {#if $takeBest.isError && $takeBest.variables === want.id}
            <div class="px-3 pb-2"><ErrorNote error={$takeBest.error} /></div>
          {/if}
          {#if $keepFloor.isError && $keepFloor.variables === want.id}
            <div class="px-3 pb-2"><ErrorNote error={$keepFloor.error} /></div>
          {/if}
        </article>
      {/each}

      <Pager {total} {offset} {pageSize} onchange={(next) => (offset = next)} />
    </div>
  {:else}
    <EmptyPanel role="idle" heading={emptyHeadings[pile]}>
      {#if pile === 'looking'}
        The wishlist fills from a playlist, a followed artist, a suggestion, or the Want it button
        on a release.
      {:else}
        A want you stop looking for waits here, and can be taken back.
      {/if}
    </EmptyPanel>
  {/if}
    </Settle>
  {/if}
</div>

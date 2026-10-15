<script lang="ts">
  import Icon from './Icon.svelte';
  import { untrack } from 'svelte';
  import { toStore } from 'svelte/store';
  import {
    createMutation,
    createQuery,
    keepPreviousData,
    useQueryClient,
    type Query
  } from '@tanstack/svelte-query';
  import { api, lookingFor, type AcquisitionTarget, type AcquisitionTargets } from '$lib/api';
  import { fileFormat, relativeTime, sinceTime } from '$lib/utils';
  import type { IconName } from '$lib/icons';
  import Button from '$lib/components/Button.svelte';
  import Chip from '$lib/components/Chip.svelte';
  import EmptyPanel from '$lib/components/EmptyPanel.svelte';
  import ErrorNote from '$lib/components/ErrorNote.svelte';
  import Pager from '$lib/components/Pager.svelte';
  import Segmented from '$lib/components/Segmented.svelte';
  import Settle from '$lib/components/Settle.svelte';
  import Skeleton from '$lib/components/Skeleton.svelte';
  import { leave } from '$lib/motion.svelte';
  import StateMark from '$lib/components/StateMark.svelte';
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

  // What was typed in the filter field above the tab. The server searches every
  // page of the wishlist, so the list asks again rather than narrowing the 25
  // rows it already holds.
  let { query = '' }: { query?: string } = $props();

  const queryClient = useQueryClient();
  const pageSize = 25;

  let pile = $state<Pile>('looking');
  let offset = $state(0);

  // The search waits for a pause in typing, so "money trees" is one request and
  // not eleven. A new search starts from the first page.
  let asked = $state(untrack(() => query.trim()));
  $effect(() => {
    const next = query.trim();
    if (next === untrack(() => asked)) return;
    const timer = setTimeout(() => {
      asked = next;
      offset = 0;
    }, 250);
    return () => clearTimeout(timer);
  });

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
      queryKey: ['wants', 'list', pile, asked, offset],
      queryFn: () =>
        api.wants({ statuses: statuses[pile], query: asked, limit: pageSize, offset }),
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
  // the tab badge's own key, so that request is made once for both. Under a
  // filter both figures count the matches, so the pile and its figure agree.
  const looking = createQuery(
    toStore(() => ({
      queryKey: asked ? ['wants', 'looking', 'count', asked] : ['wants', 'looking', 'count'],
      queryFn: () => api.wants({ statuses: lookingFor, query: asked, limit: 1 }),
      placeholderData: keepPreviousData
    }))
  );
  const stopped = createQuery(
    toStore(() => ({
      queryKey: ['wants', 'stopped', 'count', asked],
      queryFn: () => api.wants({ statuses: statuses.stopped, query: asked, limit: 1 }),
      placeholderData: keepPreviousData
    }))
  );

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

  // "Not in your library yet." opens most of the loop's sentences, and every
  // row on the wishlist is not in the library yet, so the row keeps only what
  // follows it.
  const notInLibrary = 'Not in your library yet.';
  function summaryLine(want: AcquisitionTarget) {
    const summary = want.summary ?? '';
    return summary.startsWith(notInLibrary) ? summary.slice(notInLibrary.length).trim() : summary;
  }

  function sampleStatus(want: AcquisitionTarget) {
    if (!want.anchorUnavailable) return '';
    if (want.anchorNextAttemptAt) return `Sample retry ${relativeTime(want.anchorNextAttemptAt)}`;
    return `No sample · ${want.anchorUnavailable}`;
  }

  // Why a want is waiting, in the loop's own sentence. It sits in the mark's
  // tooltip rather than on a line of its own: on a list of a thousand rows that
  // line was most of the page, and it was cut off with "…" anyway.
  function reason(want: AcquisitionTarget, sample: string) {
    return sample || (waiting(want) ? summaryLine(want) : '');
  }

  const piles = $derived([
    { value: 'looking', name: 'Searching', count: $looking.data?.total },
    { value: 'stopped', name: 'Removed', count: $stopped.data?.total }
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
        return { role: 'idle', text: 'Removed' };
      case 'acquired':
        return { role: 'ok', text: 'In your library' };
      default:
        return { role: 'busy', text: 'Looking for a copy' };
    }
  }

  const origins: Record<string, string> = {
    manual: 'added by you',
    playlist: 'from a playlist',
    follow_feed: 'from an artist you follow',
    label_feed: 'from a label you follow',
    recommendation: 'from a suggestion',
    upgrade: 'quality upgrade'
  };

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

  // The file being replaced, by its format. Its name repeats the song title
  // above it, as a raw file name like 02-deftones-..._(midnight_airport_version)-dsp.mp3.
  function upgradeOf(path: string) {
    const format = fileFormat(path);
    return format ? `upgrade of your ${format}` : 'upgrade';
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
        ? upgradeOf(want.upgradeOfPath)
        : (origins[want.origin] ?? 'added by you')
    ];
    if (want.album) parts.push(want.album);
    if (want.externalUrl) parts.push('keyed');
    else if (want.status === 'unresolved') parts.push('not matched yet');
    else if (want.attempts === 0) parts.push('no copies checked yet');
    else parts.push(want.attempts === 1 ? '1 copy checked' : `${want.attempts} copies checked`);
    if (want.minimumBitrate) parts.push(`${want.minimumBitrate} kbit/s`);
    return parts.join(' · ');
  }

  // The chip repeats the mark, so it is only drawn when it says more than the
  // state almost every row on the wishlist is in.
  function chipWorthShowing(mark: { text: string }) {
    return mark.text !== 'Looking for a copy';
  }

  // When something last happened, and when the next thing will. A want with
  // neither has been recorded and not yet reached, which is its own answer.
  function timing(want: AcquisitionTarget) {
    if (want.status === 'not_wanted') return '';
    if (waitingOnYou(want)) return 'waiting for you';
    const main = want.lastAttemptAt
      ? `tried ${sinceTime(want.lastAttemptAt)}`
      : want.nextAttemptAt
        ? `next try ${relativeTime(want.nextAttemptAt)}`
        : 'not searched yet';
    const search =
      want.status === 'unresolved' && want.nextSearchAt
        ? `next search ${relativeTime(want.nextSearchAt)}`
        : '';
    return [main, search].filter(Boolean).join(' · ');
  }

  const markIcons: Record<Role, 'check' | 'minus' | 'pause' | 'busy' | 'close'> = {
    ok: 'check',
    idle: 'minus',
    decide: 'pause',
    busy: 'busy',
    fail: 'close'
  };

  const emptyHeadings: Record<Pile, string> = {
    looking: 'Wishlist is empty',
    stopped: 'Nothing removed'
  };
</script>

{#snippet RowAction(label: string, icon: IconName, pending: boolean, act: () => void)}
  <Button variant="ghost" size="xs" icon title={label} aria-label={label} disabled={pending} onclick={act}>
    <Icon name={icon} size="sm" />
  </Button>
{/snippet}

<div class="flex flex-col gap-4 px-6 py-5">
  <Segmented
    options={piles}
    value={pile}
    onchange={(value) => show(value as Pile)}
    label="Filter the wishlist"
    pending={$looking.isPending || $stopped.isPending}
  />

  <!-- Drawn only when there is something to say. A box held open for it left
       about 120px of nothing between the piles and the first row. -->
  {#if notice}
    <StatusBadge role="idle" wrap>
      <span class="text-meta leading-relaxed text-ink">{notice}</span>
    </StatusBadge>
  {/if}

  {#if $wants.isError && !$wants.data}
    <!-- It is the list that failed, so the failure stands where the list would
         have been rather than above it. Only a read, so it asks again by
         itself. -->
    <ErrorNote error={$wants.error} retry={() => $wants.refetch()} />
  {:else}
    {#if $wants.isError}
      <!-- A failed poll keeps the rows it has, with the note above them. -->
      <ErrorNote error={$wants.error} retry={() => $wants.refetch()} />
    {/if}
    <Settle pending={$wants.isPending}>
      {#snippet placeholder()}
    <!-- Fill the visible viewport. The reserved summary line below is part of
         every want row, including these placeholders. -->
    <div class="flex flex-col overflow-hidden" style="max-height: calc(100dvh - 11rem)">
      {#each Array(10) as _, placeholderIndex (placeholderIndex)}
        <div class="border-b border-line-thin px-3 py-2 last:border-b-0">
          <Skeleton class="h-4 w-full" />
          <Skeleton class="mt-1.5 h-3.5 w-2/3" />
        </div>
      {/each}
    </div>
      {/snippet}
  {#if items.length}
    <div class="flex flex-col">
      {#each items as want (want.id)}
        {@const mark = standing(want)}
        {@const sample = sampleStatus(want)}
        {@const why = reason(want, sample)}
        <!-- A row stopped or taken back leaves this pile, and folds away rather
             than vanishing; rows leaving because the page changed go on the cut. -->
        <article
          class="border-b border-line-thin last:border-b-0"
          out:leave={{ active: $stop.variables === want.id || $resume.variables === want.id }}
        >
          <div
            class="flex flex-wrap items-center gap-x-3 gap-y-2 rounded-row px-3 py-2 transition hover:bg-surface-thick md:grid md:grid-cols-[1rem_minmax(0,1fr)_128px_112px_auto] md:gap-y-0"
          >
            <!-- Icons, never typed glyphs, as on the transfers beside it. The
                 mark carries the state's name on hover and for a screen
                 reader, because the chip is left off the common case. -->
            <span class="flex shrink-0" title={[mark.text, why].filter(Boolean).join(' · ')}>
              <StateMark role={mark.role}>
                <Icon name={markIcons[mark.role]} size="sm" class="size-2.5 stroke-[3]" />
              </StateMark>
              <span class="sr-only">{[chipWorthShowing(mark) ? '' : mark.text, why].filter(Boolean).join(' · ')}</span>
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
            </span>

            <!-- On a phone this wraps under the name rather than being dropped.
                 When a want was last looked for is half of what the row is for:
                 without it the reader is back to asking whether anything is
                 happening at all. -->
            <span class="numeric whitespace-nowrap text-meta text-ink-4">{timing(want)}</span>

            <span class="flex min-w-0 items-center">
              {#if chipWorthShowing(mark)}
                <Chip role={mark.role} class="shrink-0">{mark.text}</Chip>
              {/if}
              {#if want.floorWaivedAt}
                <Chip role="idle" class="ml-2 shrink-0">Floor waived</Chip>
              {/if}
            </span>

            <!-- One icon button per row, whichever action it is, so the column
                 keeps one width. A text link beside a wide outline button made
                 the rows jump. The words are the tooltip and the name. -->
            <span class="ml-auto flex shrink-0 items-center md:ml-0 md:justify-end">
              {#if want.floorWaivedAt}
                {@render RowAction('Keep the floor', 'undo', $keepFloor.isPending, () => $keepFloor.mutate(want.id))}
              {:else if want.status === 'pending' && want.belowFloorSince}
                {@render RowAction('Take best available', 'download', $takeBest.isPending, () => $takeBest.mutate(want.id))}
              {:else if want.status === 'not_wanted'}
                {@render RowAction('Add back', 'plus', $resume.isPending, () => $resume.mutate(want.id))}
              {:else}
                {@render RowAction('Remove', 'dismiss', $stop.isPending, () => $stop.mutate(want.id))}
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
    <EmptyPanel role="idle" heading={asked ? 'Nothing matches that search' : emptyHeadings[pile]}>
      {#if asked}
        Clear the filter to see the whole list.
      {:else if pile === 'looking'}
        Songs come from playlists, followed artists, suggestions and the Want it button on a
        release.
      {:else}
        Songs you remove land here. You can add them back.
      {/if}
    </EmptyPanel>
  {/if}
    </Settle>
  {/if}
</div>

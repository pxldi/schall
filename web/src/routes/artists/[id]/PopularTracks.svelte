<script lang="ts" module>
  import { api as popularAPI } from '$lib/api';

  // Listen counts move over weeks and the server keeps them for a day, so an
  // hour on this side saves the request on every visit back. Shared with the
  // page, which starts the read beside the artist's own instead of after it.
  export function popularOptions(artistID: string) {
    return {
      queryKey: ['artists', artistID, 'popular'],
      queryFn: () => popularAPI.artistPopular(artistID),
      staleTime: 60 * 60 * 1000
    };
  }
</script>

<script lang="ts">
  import { createMutation, createQuery, useQueryClient } from '@tanstack/svelte-query';
  import { toStore } from 'svelte/store';
  import { api, type PopularTrack, type RemovedTracks } from '$lib/api';
  import { glide } from '$lib/motion.svelte';
  import Cover from '$lib/components/Cover.svelte';
  import ErrorNote from '$lib/components/ErrorNote.svelte';
  import Settle from '$lib/components/Settle.svelte';
  import SkeletonRows from '$lib/components/SkeletonRows.svelte';
  import StateIcon from '$lib/components/StateIcon.svelte';
  import WantToggle from '$lib/components/WantToggle.svelte';
  import { calendarDate, removedSummary } from '$lib/utils';
  import { holding, quiet } from '$lib/vocabulary';

  // The artist's most listened recordings across ListenBrainz, in its order.
  // The page's cursor can stand on a row (`cursorId`), so J and K walk from the
  // releases into this list. Each row is matched to the catalogue by MusicBrainz recording ID alone, so
  // a tick here means a file holds exactly that recording.

  const COLUMNS = 'grid-cols-[1.75rem_1.75rem_minmax(0,1fr)_minmax(0,14rem)_1.5rem_1.25rem_3rem]';

  let {
    artistID,
    artistName,
    cursorId = '',
    onpick
  }: {
    artistID: string;
    artistName: string;
    /** The recording the page's cursor is on, or empty. */
    cursorId?: string;
    /** Told when focus reaches a row, so the cursor follows the keyboard. */
    onpick?: (recordingId: string) => void;
  } = $props();

  const queryClient = useQueryClient();
  const popular = createQuery(toStore(() => popularOptions(artistID)));

  // Removing takes the want away at once, and a file Schall downloaded for the
  // recording is deleted 30 days later. Wanting it again before then keeps the
  // file, through the same calls the release page uses.
  type Toggle = { kind: 'want' | 'remove'; track: PopularTrack };
  const toggle = createMutation({
    mutationFn: async ({ kind, track }: Toggle): Promise<unknown> => {
      if (kind === 'remove') {
        if (track.trackId) return api.removeTrack(track.trackId);
        return api.removeTarget(track.wantId ?? '');
      }
      if (track.trackId) return api.wantTrack(track.trackId);
      if (track.wantStatus === 'not_wanted' && track.wantId) return api.pursueTargetAgain(track.wantId);
      return api.wantPopularTrack(track, artistName);
    },
    onSuccess: async () => {
      await Promise.all([
        queryClient.invalidateQueries({ queryKey: ['artists', artistID, 'popular'] }),
        queryClient.invalidateQueries({ queryKey: ['releases'] }),
        queryClient.invalidateQueries({ queryKey: ['wants'] }),
        queryClient.invalidateQueries({ queryKey: ['dashboard'] })
      ]);
    }
  });
  const removed = $derived(
    $toggle.data && $toggle.variables?.kind === 'remove'
      ? removedSummary($toggle.data as RemovedTracks)
      : ''
  );

  // A held recording counts as wanted until somebody removes it.
  function wanted(track: PopularTrack) {
    if (held(track)) return !track.removesAt;
    return Boolean(track.wantStatus && track.wantStatus !== 'not_wanted');
  }
  function held(track: PopularTrack) {
    return track.held || track.wantStatus === 'acquired';
  }
  // A held recording is removed through its track or its want. One held only
  // through a file no release here carries, with no want, has neither.
  function removable(track: PopularTrack) {
    return !held(track) || Boolean(track.trackId || track.wantId);
  }

  function length(milliseconds: number) {
    if (!milliseconds) return '';
    const seconds = Math.round(milliseconds / 1000);
    return `${Math.floor(seconds / 60)}:${String(seconds % 60).padStart(2, '0')}`;
  }
</script>

<!-- The section is drawn from the first frame, heading and all, so the column
     holds its place while the counts are read. -->
<section class="flex min-w-0 flex-col" aria-labelledby="popular-heading">
  <h2 id="popular-heading" class="label mx-5 border-b border-rule-strong pb-1.5">Popular</h2>
  {#if $popular.isError}
    <ErrorNote error={$popular.error} retry={() => $popular.refetch()} class="mx-5 mt-2" />
  {:else}
    <Settle pending={$popular.isPending}>
      {#snippet placeholder()}
        <SkeletonRows count={10} cover label="Loading popular tracks" class="px-2" />
      {/snippet}
      {#if !$popular.data?.unavailable && $popular.data?.items.length}
        <ol class="relative" use:glide={{ key: cursorId }}>
          {#each $popular.data.items as track, index (track.recordingId)}
            {@const done = held(track)}
            {@const here = track.recordingId === cursorId}
            <li
              class="row want-row scroll-mt-24 scroll-mb-12 grid {COLUMNS} items-center gap-x-3 px-5 {here
                ? 'on-ink'
                : ''}"
              data-recording={track.recordingId}
              data-cursor={here}
              onfocusin={() => onpick?.(track.recordingId)}
            >
              <span class="mono text-right text-meta {here ? '' : 'text-ink-4'}">{index + 1}</span>
              <span class="block size-7 overflow-hidden rounded-cover bg-raised {done ? '' : 'opacity-60'}">
                <Cover
                  src={track.albumId && track.hasCover
                    ? `/api/v1/albums/${track.albumId}/cover?cached=1`
                    : undefined}
                  seed={track.albumId ?? track.recordingId}
                  class="size-full object-cover"
                />
              </span>
              {#if track.albumId}
                <a
                  href={`/releases/${track.albumId}`}
                  class="truncate text-body after:absolute after:inset-0 {here
                    ? 'font-medium'
                    : done
                      ? ''
                      : 'text-ink-3'}"
                  title={track.title}>{track.title}</a
                >
              {:else}
                <span
                  class="truncate text-body {here ? 'font-medium' : done ? '' : 'text-ink-3'}"
                  title={track.title}>{track.title}</span
                >
              {/if}
              <span class="truncate text-meta {here ? '' : 'text-ink-4'}">
                {track.albumTitle ?? track.releaseTitle}
              </span>
              <!-- The toggle of a held recording waits for the pointer, as on a
                   release page, because the mark beside it says it is here. -->
              <span class="relative z-[2] flex items-center justify-center">
                {#if removable(track)}
                  <WantToggle
                    reveal={done ? 'all' : true}
                    wanted={wanted(track)}
                    title={track.title}
                    onwant={() => $toggle.mutateAsync({ kind: 'want', track })}
                    onunwant={() => $toggle.mutateAsync({ kind: 'remove', track })}
                  />
                {/if}
              </span>
              <span class="flex items-center justify-center">
                {#if track.removesAt}
                  <StateIcon
                    icon="delete"
                    label={`Deleted on ${calendarDate(track.removesAt)}`}
                    note="Add again to keep"
                  />
                {:else if done}
                  <StateIcon icon="check" label={quiet(holding.complete)} role="ok" />
                {/if}
              </span>
              <span class="mono text-right text-meta {here ? '' : 'text-ink-3'}">{length(track.lengthMs)}</span>
            </li>
          {/each}
        </ol>
        {#if $toggle.isError}
          <ErrorNote error={$toggle.error} class="mx-5 mt-2" />
        {:else if removed}
          <p class="mx-5 mt-2 text-meta text-ink-3">{removed}.</p>
        {/if}
      {:else}
        <!-- Two lines under the heading and no panel of their own: the section's
             rule already frames them. -->
        <div class="mx-5 mt-3 flex flex-col gap-1">
          <h3 class="text-body font-semibold text-ink">
            {$popular.data?.unavailable ? 'Not available' : 'No listens yet'}
          </h3>
          <p class="text-meta leading-relaxed text-ink-3">
            {$popular.data?.unavailable || 'ListenBrainz has no listens for this artist.'}
          </p>
        </div>
      {/if}
    </Settle>
  {/if}
</section>

<script lang="ts">
  import { createMutation, createQuery, useQueryClient } from '@tanstack/svelte-query';
  import { toStore } from 'svelte/store';
  import { api, type PopularTrack, type RemovedTracks } from '$lib/api';
  import Cover from '$lib/components/Cover.svelte';
  import ErrorNote from '$lib/components/ErrorNote.svelte';
  import StateIcon from '$lib/components/StateIcon.svelte';
  import WantToggle from '$lib/components/WantToggle.svelte';
  import { calendarDate, removedSummary } from '$lib/utils';
  import { holding, quiet } from '$lib/vocabulary';

  // The artist's most listened recordings across ListenBrainz, in its order.
  // The list scrolls inside its box, so the page beside it keeps its place.
  // Each row is matched to the catalogue by MusicBrainz recording ID alone, so
  // a tick here means a file holds exactly that recording.

  let { artistID, artistName }: { artistID: string; artistName: string } = $props();

  const queryClient = useQueryClient();
  // Listen counts move over weeks and the server keeps them for a day, so an
  // hour on this side saves the request on every visit back.
  const popular = createQuery(
    toStore(() => ({
      queryKey: ['artists', artistID, 'popular'],
      queryFn: () => api.artistPopular(artistID),
      staleTime: 60 * 60 * 1000
    }))
  );

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
        queryClient.invalidateQueries({ queryKey: ['acquisition-targets'] }),
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

{#if $popular.data && ($popular.data.items.length || $popular.data.unavailable)}
  <section class="flex min-w-0 flex-col" aria-labelledby="popular-heading">
    <h2
      id="popular-heading"
      class="border-b border-line-thin pb-1.5 text-[0.9375rem] font-semibold text-ink"
    >
      Popular
    </h2>
    {#if $popular.data.unavailable}
      <p class="py-2 text-meta text-ink-3">{$popular.data.unavailable}</p>
    {:else}
      <ol class="popular-list max-h-[31rem] overflow-y-auto pt-1 pr-1">
        {#each $popular.data.items as track, index (track.recordingId)}
          {@const done = held(track)}
          <li
            class="want-row grid grid-cols-[1.25rem_2.5rem_minmax(0,1fr)_1.5rem_1.25rem_2.5rem] items-center gap-2 rounded-row px-1.5 py-1"
          >
            <span class="numeric text-right text-meta text-ink-4">{index + 1}</span>
            <span class="size-10 overflow-hidden rounded-tight bg-surface-thick {done ? '' : 'opacity-60'}">
              <Cover
                src={track.albumId && track.hasCover
                  ? `/api/v1/albums/${track.albumId}/cover?cached=1`
                  : undefined}
                seed={track.albumId ?? track.recordingId}
                class="size-full object-cover"
              />
            </span>
            <span class="min-w-0">
              {#if track.albumId}
                <a
                  href={`/releases/${track.albumId}`}
                  class="block truncate text-body font-semibold hover:underline {done ? 'text-ink' : 'text-ink-2'}"
                  title={track.title}>{track.title}</a
                >
              {:else}
                <span
                  class="block truncate text-body font-semibold {done ? 'text-ink' : 'text-ink-2'}"
                  title={track.title}>{track.title}</span
                >
              {/if}
              <span class="block truncate text-meta text-ink-3">
                {track.albumTitle ?? track.releaseTitle}
              </span>
            </span>
            <!-- The toggle of a held recording waits for the pointer, as on a
                 release page, because the mark beside it says it is here. -->
            <span class="flex items-center justify-center">
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
                  note="Want it again to keep it"
                />
              {:else if done}
                <StateIcon icon="check" label={quiet(holding.complete)} role="ok" />
              {/if}
            </span>
            <span class="numeric text-right text-meta text-ink-3">{length(track.lengthMs)}</span>
          </li>
        {/each}
      </ol>
      {#if $toggle.isError}
        <ErrorNote error={$toggle.error} class="mt-2" />
      {:else if removed}
        <p class="mt-2 text-meta text-ink-3">{removed}.</p>
      {/if}
    {/if}
  </section>
{/if}

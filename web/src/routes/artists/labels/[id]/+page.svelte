<script lang="ts">
  // One label: what it published, and how much of it the library holds. The
  // monitor level decides what counts as missing; every release is listed
  // regardless, the same rule the artist discography page follows.
  import { page } from '$app/state';
  import { createQuery } from '@tanstack/svelte-query';
  import { toStore } from 'svelte/store';
  import { api } from '$lib/api';
  import { calendarDate } from '$lib/utils';
  import ErrorNote from '$lib/components/ErrorNote.svelte';
  import PageHeader from '$lib/components/PageHeader.svelte';
  import Settle from '$lib/components/Settle.svelte';

  const labelID = $derived(page.params.id ?? '');

  const detail = createQuery(
    toStore(() => ({
      queryKey: ['labels', labelID],
      queryFn: () => api.label(labelID),
      enabled: labelID.length > 0
    }))
  );

  const label = $derived($detail.data?.label);
  const releases = $derived($detail.data?.releases ?? []);
</script>

<svelte:head><title>{label ? `${label.name} · Schall` : 'Label · Schall'}</title></svelte:head>

<PageHeader>
  <a href="/artists/labels" class="text-meta font-medium text-ink-3 transition hover:text-ink">
    Labels
  </a>
  {#if label}
    <h1 class="font-display text-2xl font-bold text-ink">{label.name}</h1>
    <span class="text-meta text-ink-3">
      {label.lastRefreshedAt ? `updated ${calendarDate(label.lastRefreshedAt)}` : 'not refreshed yet'}
    </span>
  {:else}
    <span class="h-7 w-48 animate-pulse rounded-row bg-surface-regular" aria-hidden="true"></span>
    <span class="h-4 w-28 animate-pulse rounded-row bg-surface-regular" aria-hidden="true"></span>
  {/if}
</PageHeader>

<div class="flex flex-col gap-4 px-4 sm:px-6 py-5">
  {#if $detail.isError}
    <ErrorNote error={$detail.error} retry={() => $detail.refetch()} />
  {:else}
    <Settle pending={$detail.isPending}>
      {#snippet placeholder()}
    <ul
      class="flex flex-col divide-y divide-line-thin rounded-panel border border-line-regular"
      role="status"
      aria-label="Loading label releases"
    >
          <!-- Fill about 2016px of rows so a 4K viewport does not outgrow the wait. -->
          {#each Array(60) as _, placeholderIndex (placeholderIndex)}
        <li class="flex flex-wrap items-center gap-3 px-4 py-3" aria-hidden="true">
          <span class="h-4 w-40 animate-pulse rounded-row bg-surface-regular"></span>
          <span class="h-4 w-28 animate-pulse rounded-row bg-surface-regular"></span>
          <span class="ml-auto h-4 w-32 animate-pulse rounded-row bg-surface-regular"></span>
        </li>
      {/each}
    </ul>
      {/snippet}
  {#if releases.length === 0}
    <p class="text-body text-ink-3">Nothing published yet, or not fetched yet.</p>
  {:else}
    <ul class="flex flex-col divide-y divide-line-thin rounded-panel border border-line-regular">
      {#each releases as release (release.id)}
        <li class="flex flex-wrap items-center gap-x-3 gap-y-1 px-4 py-3">
          <a
            href="/releases/{release.id}"
            class="min-w-0 truncate text-body font-medium text-ink underline-offset-2 hover:underline"
          >
            {release.title}
          </a>
          <span class="text-meta text-ink-3">{release.artistName}</span>
          {#if release.releaseDate}
            <span class="text-meta text-ink-4">{release.releaseDate}</span>
          {/if}
          {#if !release.monitored}
            <span class="text-meta text-ink-4">unmonitored</span>
          {/if}
          <span class="ml-auto text-meta text-ink-3">
            {release.ownedTrackCount} of {release.trackCount} tracks
          </span>
        </li>
      {/each}
    </ul>
  {/if}
    </Settle>
  {/if}
</div>

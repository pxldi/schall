<script lang="ts">
  // One label: what it published, and how much of it the library holds. The
  // monitor level decides what counts as missing; every release is listed
  // regardless, the same rule the artist discography page follows.
  import { page } from '$app/state';
  import { createQuery } from '@tanstack/svelte-query';
  import { toStore } from 'svelte/store';
  import { api, type LabelRelease } from '$lib/api';
  import { calendarDate } from '$lib/utils';
  import { coverSrc, usePagePrint } from '$lib/duoton';
  import BackLink from '$lib/components/BackLink.svelte';
  import Cover from '$lib/components/Cover.svelte';
  import ErrorNote from '$lib/components/ErrorNote.svelte';
  import Hero from '$lib/components/Hero.svelte';
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

  // A strip of the label's pictured releases, the ones the library holds most
  // of first, inked from the first (ADR Duoton).
  usePagePrint(() => {
    if (!$detail.data) return undefined;
    return {
      covers: releases
        .filter((release) => release.hasCover)
        .toSorted((a, b) => b.ownedTrackCount - a.ownedTrackCount)
        .slice(0, 6)
        .map((release) => ({ src: coverSrc(release.id), seed: release.id, inks: release.inks }))
    };
  });

  const heroSize = $derived((label?.name.length ?? 0) > 18 ? 'm' : 'l');

  function line(release: LabelRelease): string {
    const year = release.releaseDate ? release.releaseDate.slice(0, 4) : '—';
    if (!release.monitored) return `${year} · unmonitored`;
    if (release.trackCount === 0) return `${year} · no track list`;
    return `${year} · ${release.ownedTrackCount} of ${release.trackCount}`;
  }
</script>

<svelte:head><title>{label ? `${label.name} · Schall` : 'Label · Schall'}</title></svelte:head>

{#if label}
  <Hero title={label.name} size={heroSize}>
    {#snippet back()}
      <BackLink fallback="/artists/labels" label="Back to labels" />
    {/snippet}
    {#snippet sub()}
      <span>
        <b class="numeric font-semibold text-ink">{releases.length}</b>
        {releases.length === 1 ? 'release' : 'releases'}
      </span>
      {#if label.type}<span>{label.type}</span>{/if}
      {#if label.country}<span>{label.country}</span>{/if}
      <span>
        {label.lastRefreshedAt ? `updated ${calendarDate(label.lastRefreshedAt)}` : 'not refreshed yet'}
      </span>
    {/snippet}
  </Hero>
{:else if $detail.isPending}
  <section
    class="flex flex-col justify-end gap-3 px-4 pb-4 sm:px-6"
    style="min-height: calc(var(--print-height) - var(--topbar-height));"
    aria-hidden="true"
  >
    <span class="h-16 w-96 max-w-full animate-pulse rounded-row bg-surface-regular/60"></span>
    <span class="h-4 w-56 animate-pulse rounded-row bg-surface-regular/60"></span>
  </section>
{:else}
  <h1 class="sr-only">Label</h1>
{/if}

<div class="flex flex-col gap-4 px-4 py-5 sm:px-6">
  {#if $detail.isError}
    <ErrorNote error={$detail.error} retry={() => $detail.refetch()} />
  {:else}
    <Settle pending={$detail.isPending}>
      {#snippet placeholder()}
        <div
          class="grid max-h-[calc(100dvh-24rem)] grid-cols-[repeat(auto-fill,minmax(10.5rem,1fr))] gap-x-[18px] gap-y-[22px] overflow-hidden"
          role="status"
          aria-label="Loading label releases"
        >
          {#each Array(48) as _, placeholderIndex (placeholderIndex)}
            <div class="flex flex-col gap-1.5" aria-hidden="true">
              <div class="aspect-square animate-pulse rounded-card bg-surface-regular"></div>
              <span class="h-4 w-full animate-pulse rounded-row bg-surface-regular"></span>
              <span class="h-3 w-2/3 animate-pulse rounded-row bg-surface-regular"></span>
            </div>
          {/each}
        </div>
      {/snippet}
      {#if releases.length === 0}
        <p class="text-body text-ink-3">Nothing published yet, or not fetched yet.</p>
      {:else}
        <div class="grid grid-cols-[repeat(auto-fill,minmax(10.5rem,1fr))] gap-x-[18px] gap-y-[22px]">
          {#each releases as release (release.id)}
            <a href="/releases/{release.id}" class="group flex min-w-0 flex-col gap-1.5">
              <!-- A release with nothing held is faded, the same as on an
                   artist's page, so what the library has stands out. -->
              <div
                class="aspect-square overflow-hidden rounded-card transition-transform duration-200 group-hover:-translate-y-[3px] {release.ownedTrackCount ===
                0
                  ? 'opacity-[0.38]'
                  : ''}"
                data-held={release.ownedTrackCount > 0 ? 'some' : 'none'}
              >
                <Cover
                  src={release.hasCover ? `/api/v1/albums/${release.id}/cover?cached=1` : undefined}
                  seed={release.id}
                  class="size-full object-cover"
                />
              </div>
              <span
                class="block truncate text-body font-bold tracking-[-0.01em] text-ink"
                title={release.title}>{release.title}</span
              >
              <span class="-mt-1 block truncate text-meta text-ink-2">{release.artistName}</span>
              <span class="numeric block text-meta text-ink-3">{line(release)}</span>
            </a>
          {/each}
        </div>
      {/if}
    </Settle>
  {/if}
</div>

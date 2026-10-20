<script lang="ts">
  // One label: what it published, and how much of it the library holds. The
  // monitor level decides what counts as missing; every release is listed
  // regardless, the same rule the artist discography page follows.
  import { page } from '$app/state';
  import { createQuery } from '@tanstack/svelte-query';
  import { toStore } from 'svelte/store';
  import { api, ApiError, type LabelRelease } from '$lib/api';
  import { calendarDate } from '$lib/utils';
  import { queryRetry } from '$lib/errors';
  import Button from '$lib/components/Button.svelte';
  import Skeleton from '$lib/components/Skeleton.svelte';
  import Cover from '$lib/components/Cover.svelte';
  import EmptyPanel from '$lib/components/EmptyPanel.svelte';
  import ErrorNote from '$lib/components/ErrorNote.svelte';
  import PageHeader from '$lib/components/PageHeader.svelte';
  import Settle from '$lib/components/Settle.svelte';

  const labelID = $derived(page.params.id ?? '');

  const detail = createQuery(
    toStore(() => ({
      queryKey: ['labels', labelID],
      queryFn: () => api.label(labelID),
      enabled: labelID.length > 0,
      // A label the server does not have will not appear by asking again.
      retry: (spent: number, error: Error) =>
        !(error instanceof ApiError && error.status === 404) && queryRetry(spent, error)
    }))
  );

  // An old link, or a label unfollowed and since removed. Reloading cannot
  // bring it back, so the page says so and leads to the list of labels.
  const missing = $derived($detail.error instanceof ApiError && $detail.error.status === 404);

  const label = $derived($detail.data?.label);
  const releases = $derived($detail.data?.releases ?? []);

  function line(release: LabelRelease): string {
    const year = release.releaseDate ? release.releaseDate.slice(0, 4) : '—';
    if (!release.monitored) return `${year} · unmonitored`;
    if (release.trackCount === 0) return `${year} · no track list`;
    return `${year} · ${release.ownedTrackCount} of ${release.trackCount}`;
  }
</script>

<svelte:head><title>{label ? `${label.name} · Schall` : 'Label · Schall'}</title></svelte:head>

<!-- The header settles out of its skeleton like the releases under it, so the
     title does not cut in over the boxes. -->
<Settle pending={$detail.isPending}>
  {#snippet placeholder()}
    <section class="flex flex-col gap-2 px-5 pb-4 pt-3.5" aria-hidden="true">
      <Skeleton class="h-4 w-32" />
      <Skeleton class="h-8 w-96 max-w-full" />
      <Skeleton class="h-4 w-56" />
    </section>
  {/snippet}
  {#if label}
    <PageHeader
      title={label.name}
      path={[
        { label: 'Artists', href: '/artists' },
        { label: 'Labels', href: '/artists/labels' }
      ]}
    >
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
    </PageHeader>
  {:else if missing}
    <PageHeader
      title="Label not found"
      path={[
        { label: 'Artists', href: '/artists' },
        { label: 'Labels', href: '/artists/labels' }
      ]}
    >
      {#snippet sub()}
        <span>No label here. The link may be old.</span>
      {/snippet}
    </PageHeader>
  {:else}
    <h1 class="sr-only">Label</h1>
  {/if}
</Settle>

<div class="flex flex-col gap-4 px-5 py-5">
  {#if missing}
    <div><Button href="/artists/labels">Open labels</Button></div>
  {:else if $detail.isError}
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
              <Skeleton shape="block" class="aspect-square" />
              <Skeleton class="h-4 w-full" />
              <Skeleton class="h-3 w-2/3" />
            </div>
          {/each}
        </div>
      {/snippet}
      {#if releases.length === 0}
        <EmptyPanel role="idle" heading={label?.lastRefreshedAt ? 'No releases' : 'Not fetched yet'}>
          <p class="text-body text-ink-2">
            {label?.lastRefreshedAt
              ? 'MusicBrainz lists no releases for this label.'
              : 'Not read from MusicBrainz yet.'}
          </p>
        </EmptyPanel>
      {:else}
        <div class="grid grid-cols-[repeat(auto-fill,minmax(10.5rem,1fr))] gap-x-[18px] gap-y-[22px]">
          {#each releases as release (release.id)}
            <a href="/releases/{release.id}" class="group flex min-w-0 flex-col gap-1.5">
              <!-- A release with nothing held is faded, the same as on an
                   artist's page, so what the library has stands out. -->
              <div
                class="aspect-square overflow-hidden rounded-cover {release.ownedTrackCount ===
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
                class="block truncate text-body font-semibold text-ink"
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

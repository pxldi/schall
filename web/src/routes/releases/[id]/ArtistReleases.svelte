<script lang="ts">
  import { createQuery } from '@tanstack/svelte-query';
  import { toStore } from 'svelte/store';
  import { api, type Release } from '$lib/api';
  import Cover from '$lib/components/Cover.svelte';
  import ErrorNote from '$lib/components/ErrorNote.svelte';
  import Icon from '$lib/components/Icon.svelte';
  import Settle from '$lib/components/Settle.svelte';
  import SkeletonRows from '$lib/components/SkeletonRows.svelte';

  // The artist's whole discography beside a release, newest first, in a list
  // that scrolls inside its box, where the artist page keeps Popular. The open
  // release is marked so the reader can see where they are and step to the
  // next one. It asks with the artist page's own key, so moving between the
  // two pages reads the discography once.

  let {
    artistID,
    artistName,
    releaseID
  }: { artistID: string; artistName: string; releaseID: string } = $props();

  const releases = createQuery(
    toStore(() => ({
      queryKey: ['releases', 'artist', artistID, ''],
      queryFn: () => api.releases({ artistId: artistID, sort: 'year' as const }),
      enabled: Boolean(artistID)
    }))
  );

  const items = $derived(
    [...($releases.data?.items ?? [])].sort(
      (a, b) =>
        (b.firstReleaseDate ?? '').localeCompare(a.firstReleaseDate ?? '') ||
        a.title.localeCompare(b.title)
    )
  );

  // The same kinds and the same counting rule as the artist page's rows.
  function kind(release: Release) {
    if (release.albumType === 'album' && !release.secondaryTypes?.length) return 'Album';
    if (release.albumType === 'ep') return 'EP';
    if (release.albumType === 'single') return 'Single';
    return 'Other';
  }
  function countable(release: Release) {
    if (release.artistMonitorLevel === 'owned') return release.ownedTrackCount;
    return release.trackCount - release.dismissedTrackCount;
  }
</script>

<section class="flex min-w-0 flex-col" aria-labelledby="discography-heading">
  <div class="flex items-baseline gap-2.5 border-b border-rule pb-1.5">
    <h2 id="discography-heading" class="min-w-0 truncate text-body font-semibold text-ink">
      {artistName}
      {#if $releases.data}
        <span class="numeric text-meta font-normal text-ink-4">{items.length}</span>
      {/if}
    </h2>
    <a
      href={`/artists/${artistID}`}
      class="tap ml-auto inline-flex shrink-0 items-center gap-1 text-meta font-medium text-ink-3 transition hover:text-ink"
    >
      Artist <Icon name="chevron-right" size="sm" />
    </a>
  </div>
  {#if $releases.isError}
    <ErrorNote error={$releases.error} retry={() => $releases.refetch()} class="mt-2" />
  {:else}
    <Settle pending={$releases.isPending}>
      {#snippet placeholder()}
        <SkeletonRows count={8} cover label="Loading discography" />
      {/snippet}
      <ol class="max-h-[27rem] overflow-y-auto pt-1 pr-1">
        {#each items as release (release.id)}
          {@const here = release.id === releaseID}
          {@const total = countable(release)}
          {@const none = release.ownedTrackCount === 0}
          <li>
            <a
              href={`/releases/${release.id}`}
              aria-current={here ? 'page' : undefined}
              class="grid grid-cols-[2.5rem_minmax(0,1fr)_auto] items-center gap-2.5 px-1.5 py-1 transition {here
                ? 'bg-panel'
                : 'hover:bg-panel'}"
            >
              <span class="size-10 overflow-hidden bg-raised {none ? 'opacity-[0.38]' : ''}">
                <Cover
                  src={release.hasCover ? `/api/v1/albums/${release.id}/cover?cached=1` : undefined}
                  seed={release.id}
                  class="size-full object-cover"
                />
              </span>
              <span class="min-w-0">
                <span
                  class="block truncate text-body {none ? 'font-medium text-ink-2' : 'font-semibold text-ink'}"
                  title={release.title}>{release.title}</span
                >
                <span class="numeric block truncate text-meta text-ink-3">
                  {release.firstReleaseDate?.slice(0, 4) ?? '—'} · {kind(release)}
                </span>
              </span>
              {#if total > 0 && release.ownedTrackCount >= total}
                <span class="inline-flex text-ok" title="Complete">
                  <Icon name="check" size="sm" aria-label="Complete" />
                </span>
              {:else if release.ownedTrackCount > 0}
                <span class="numeric text-meta text-ink-3">{release.ownedTrackCount}/{total}</span>
              {/if}
            </a>
          </li>
        {/each}
      </ol>
    </Settle>
  {/if}
</section>

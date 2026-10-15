<script lang="ts">
  import { createQuery, keepPreviousData } from '@tanstack/svelte-query';
  import { toStore } from 'svelte/store';
  import { api } from '$lib/api';
  import Cover from '$lib/components/Cover.svelte';
  import SplitList from '$lib/components/SplitList.svelte';
  import { coverSrc } from '$lib/duoton';
  import { artistsList } from '$lib/list-memory.svelte';

  // The artists list beside an artist page (SplitList). It asks with the
  // artists page's own key and choices, so it is the list the reader came from
  // and is usually already in the cache.

  let { openID }: { openID: string } = $props();

  const artists = createQuery(
    toStore(() => {
      const { scope, search, tab, genre, sort } = artistsList.choice;
      return {
        queryKey: ['artists', scope, search, tab, genre, sort],
        queryFn: () =>
          api.artists({
            scope: scope || undefined,
            query: search || undefined,
            completeness: tab || undefined,
            genre: genre || undefined,
            sort
          }),
        placeholderData: keepPreviousData
      };
    })
  );

  let filter = $state('');
  const shown = $derived.by(() => {
    const items = $artists.data?.items ?? [];
    const needle = filter.trim().toLocaleLowerCase();
    return needle ? items.filter((artist) => artist.name.toLocaleLowerCase().includes(needle)) : items;
  });
</script>

<SplitList label="Artists" filterLabel="Filter artists" bind:filter>
  {#each shown as artist (artist.id)}
    <li>
      <a
        href={`/artists/${artist.id}`}
        aria-current={artist.id === openID ? 'page' : undefined}
        class="split-row"
      >
        <Cover
          src={artist.hasImage
            ? `/api/v1/artists/${artist.id}/image`
            : artist.coverAlbumIds[0]
              ? coverSrc(artist.coverAlbumIds[0])
              : undefined}
          seed={artist.id}
          inks={artist.coverInks}
          class="size-7 shrink-0 rounded-tight object-cover"
        />
        <span class="min-w-0 flex-1 truncate">{artist.name}</span>
        {#if artist.releaseCount > 0}
          <span class="shrink-0 font-mono text-meta tabular-nums text-ink-3">
            {artist.ownedReleaseCount}/{artist.releaseCount}
          </span>
        {/if}
      </a>
    </li>
  {:else}
    {#if $artists.isSuccess}
      <li class="px-4 py-2 text-meta text-ink-3">No artists</li>
    {/if}
  {/each}
</SplitList>

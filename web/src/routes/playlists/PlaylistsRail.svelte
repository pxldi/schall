<script lang="ts">
  import { createQuery } from '@tanstack/svelte-query';
  import { api } from '$lib/api';
  import Cover from '$lib/components/Cover.svelte';
  import SplitList from '$lib/components/SplitList.svelte';
  import { coverSrc } from '$lib/duoton';

  // The playlists beside a playlist page (SplitList), with the playlists
  // page's own key.

  let { openID }: { openID: string } = $props();

  const playlists = createQuery({ queryKey: ['playlists'], queryFn: api.playlists });
</script>

<SplitList label="Playlists">
  {#each $playlists.data?.items ?? [] as playlist (playlist.id)}
    <li>
      <a
        href={`/playlists/${playlist.id}`}
        aria-current={playlist.id === openID ? 'page' : undefined}
        class="split-row"
      >
        <Cover
          src={playlist.coverReleaseId ? coverSrc(playlist.coverReleaseId) : undefined}
          seed={playlist.id}
          inks={playlist.inks}
          class="size-7 shrink-0 rounded-tight object-cover"
        />
        <span class="min-w-0 flex-1 truncate">{playlist.name}</span>
        {#if playlist.entryCount > 0}
          <span class="shrink-0 font-mono text-meta tabular-nums text-ink-3">
            {playlist.ownedCount}/{playlist.entryCount}
          </span>
        {/if}
      </a>
    </li>
  {/each}
</SplitList>

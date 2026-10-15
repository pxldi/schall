<script lang="ts">
  import { page } from '$app/state';
  import PlaylistsRail from './PlaylistsRail.svelte';

  let { children } = $props();

  // A playlist opens beside the playlists on a wide window (SplitList).
  const openID = $derived(page.route.id === '/playlists/[id]' ? (page.params.id ?? '') : '');
</script>

{#if openID}
  <div class="split">
    <PlaylistsRail {openID} />
    <div class="min-w-0">
      <!-- The detail page reads its id once, so it is remounted per id. -->
      {#key openID}
        {@render children()}
      {/key}
    </div>
  </div>
{:else}
  {@render children()}
{/if}

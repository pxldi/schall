<script lang="ts">
  import { page } from '$app/state';
  import ArtistsRail from './ArtistsRail.svelte';

  let { children } = $props();

  // An artist opens beside the artists list on a wide window (SplitList). The
  // list stays mounted from one artist to the next, so only the detail is
  // swapped.
  const openID = $derived(page.route.id === '/artists/[id]' ? (page.params.id ?? '') : '');
</script>

<!-- SvelteKit keeps a page when only its parameters change, and the detail
     pages read their id once. Going from one artist to another would leave the
     page, its data and its actions on the first, so the detail is remounted
     per id. A change of query string keeps the page. -->
{#if openID}
  <div class="split">
    <ArtistsRail {openID} />
    <div class="min-w-0">
      {#key openID}
        {@render children()}
      {/key}
    </div>
  </div>
{:else}
  {#key JSON.stringify(page.params)}
    {@render children()}
  {/key}
{/if}

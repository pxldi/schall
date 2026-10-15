<script lang="ts">
  import { page } from '$app/state';

  let { children } = $props();
</script>

<!-- SvelteKit keeps a page when only its parameters change, and the detail
     pages read their id once. Going from one release to another would leave
     the page, its data and its actions on the first. A change of query string
     keeps the page. -->
{#key JSON.stringify(page.params)}
  {@render children()}
{/key}

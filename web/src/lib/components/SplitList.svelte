<script lang="ts">
  import type { Snippet } from 'svelte';
  import { tick } from 'svelte';

  // The list beside a detail page. On a wide window an artist or a playlist
  // opens next to the list it was picked from, and the list stays mounted while
  // the reader steps from one to the next, so a click swaps only the detail.
  // Below 1280px there is no room for both and the detail page stands alone.
  //
  // The rows are the caller's. A row for the open thing carries
  // `aria-current="page"`, and the list scrolls it into view when it is first
  // drawn, so the reader sees where they are.

  let {
    label,
    filter = $bindable(''),
    filterLabel,
    children
  }: {
    label: string;
    filter?: string;
    /** Placeholder of the filter field. Left out, the list has no field. */
    filterLabel?: string;
    children: Snippet;
  } = $props();

  let list = $state<HTMLElement>();
  let placed = false;

  $effect(() => {
    if (!list || placed) return;
    void tick().then(() => {
      const open = list?.querySelector<HTMLElement>('[aria-current="page"]');
      if (!open) return;
      placed = true;
      open.scrollIntoView({ block: 'center' });
    });
  });
</script>

<nav class="split-rail max-xl:hidden" aria-label={label}>
  {#if filterLabel !== undefined}
    <div class="px-3 pb-2 pt-3">
      <input
        type="search"
        bind:value={filter}
        placeholder={filterLabel}
        aria-label={filterLabel}
        class="h-8 w-full rounded-pill border border-line-regular bg-surface-regular px-3 text-meta text-ink placeholder:text-ink-3 focus:border-line-live focus:outline-none"
      />
    </div>
  {/if}
  <ul bind:this={list} class="min-h-0 flex-1 overflow-y-auto overscroll-contain pb-4">
    {@render children()}
  </ul>
</nav>

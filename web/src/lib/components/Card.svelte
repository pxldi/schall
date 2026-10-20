<script lang="ts">
  import type { Snippet } from 'svelte';

  // A section of a page you act on as a unit, drawn as a flat panel with no
  // border (ADR 0042). The settings page is built from sixteen of them. Panels
  // are never nested, so anything this holds draws no panel of its own.

  let { children }: { children?: Snippet } = $props();
</script>

<section
  class="rise flex flex-col gap-3.5 bg-panel p-4"
>
  {@render children?.()}
</section>

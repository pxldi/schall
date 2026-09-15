<script lang="ts">
  import type { Snippet } from 'svelte';

  // The first band of every page, and the only place a page says its own name.
  // design/page-header.html settles the shape: one wrapping line on the 24px
  // page gutter, closed by a hairline that runs the full width.
  //
  // What belongs on it is identity and action, and nothing else: the way back,
  // the picture of the thing, its name, one line of context about it, and then
  // the actions pushed right with `ml-auto`. Eleven pages had written the band
  // out by hand, in one string, identical at every one.
  //
  // **A filter, a search box, a scope selector and a sort order do not go
  // here.** They go in ControlRail, the band directly underneath. This was not
  // a preference: a name set in the display face at 24px aligns on its
  // baseline, a field is a 34px box that aligns on its edges, and a line
  // holding both has nothing for either of them to line up with. Artists put a
  // heading, a search field and a select on one line and none of the three
  // agreed with the others. Splitting them is what fixes it; nudging cannot.

  let { children }: { children?: Snippet } = $props();
</script>

<header class="flex flex-wrap items-center gap-x-3 gap-y-2 border-b border-line-thin px-4 sm:px-6 py-4">
  {@render children?.()}
</header>

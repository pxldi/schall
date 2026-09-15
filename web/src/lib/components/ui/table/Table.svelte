<script lang="ts">
  import type { Snippet } from 'svelte';
  import type { HTMLTableAttributes } from 'svelte/elements';
  import { cn } from '$lib/utils';

  // The table itself. It is wrapped in a box that scrolls sideways, because a
  // library table has more columns than a phone has room for and a table that
  // widens the page takes every screen with it.
  //
  // The sticky header needs somewhere to scroll under, so the scroll happens
  // here and not on the document.
  //
  // The box is also a size container, which is what lets something inside the
  // table ask how wide the *window onto* the table is rather than how wide the
  // table is. Prose in a full-width cell needs that: measured against the table
  // it comes out wider than the phone it is being read on.
  let {
    class: className,
    wrapperClass,
    children,
    ...rest
  }: HTMLTableAttributes & { wrapperClass?: string; children?: Snippet } = $props();
</script>

<div class={cn('relative w-full overflow-x-auto @container', wrapperClass)}>
  <table
    class={cn('w-full border-collapse text-left text-dense-body text-ink', className)}
    {...rest}
  >
    {@render children?.()}
  </table>
</div>

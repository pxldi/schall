<script lang="ts">
  import type { Snippet } from 'svelte';
  import type { HTMLAttributes } from 'svelte/elements';
  import { cn } from '$lib/utils';

  // The row of column names, held at the top of the scrolling box while the
  // rows run under it.
  //
  // The fill is the shell's ground (the ground with the page's dark-ink wash)
  // and not a surface. A see-through header shows the rows sliding underneath
  // the column names, and the shell's ground is opaque and matches the page
  // behind the table.
  let {
    class: className,
    children,
    ...rest
  }: HTMLAttributes<HTMLTableSectionElement> & { children?: Snippet } = $props();
</script>

<thead
  data-sticky="true"
  data-surface="ground"
  class={cn(
    'shell-ground sticky top-0 z-1 [&_tr]:border-b [&_tr]:border-line-regular',
    className
  )}
  {...rest}
>
  {@render children?.()}
</thead>

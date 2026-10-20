<script lang="ts">
  import type { Snippet } from 'svelte';
  import type { HTMLAttributes } from 'svelte/elements';
  import { cn } from '$lib/utils';

  // The row of column names, held at the top of the scrolling box while the
  // rows run under it.
  //
  // The fill is the page's ground, opaque. A see-through header shows the rows
  // sliding underneath the column names.
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
    'bg-ground sticky top-0 z-1 [&_tr]:border-b [&_tr]:border-rule',
    className
  )}
  {...rest}
>
  {@render children?.()}
</thead>

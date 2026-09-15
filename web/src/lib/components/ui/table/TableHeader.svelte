<script lang="ts">
  import type { Snippet } from 'svelte';
  import type { HTMLAttributes } from 'svelte/elements';
  import { cn } from '$lib/utils';

  // The row of column names, held at the top of the scrolling box while the
  // rows run under it.
  //
  // The fill is the ground and not a surface. Every surface in this product is
  // white laid over the ground at a low opacity, which is see-through by
  // definition, and a see-through header shows the rows sliding underneath the
  // column names. The ground is the one opaque colour there is, so it is the
  // one a sticky header can take.
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
    'sticky top-0 z-1 bg-ground [&_tr]:border-b [&_tr]:border-line-regular',
    className
  )}
  {...rest}
>
  {@render children?.()}
</thead>

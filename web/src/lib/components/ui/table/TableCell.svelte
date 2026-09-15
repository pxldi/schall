<script lang="ts">
  import type { Snippet } from 'svelte';
  import type { HTMLTdAttributes } from 'svelte/elements';
  import { cn } from '$lib/utils';

  // One value. No vertical rule: the header carries the column boundaries and
  // the body carries the data.
  //
  // `numeric` sets the figures in the mono face with tabular digits and pushes
  // them right, so a column of durations or counts lines up on the units and a
  // number that changes does not reflow the row.
  let {
    class: className,
    numeric = false,
    children,
    ...rest
  }: HTMLTdAttributes & { numeric?: boolean; children?: Snippet } = $props();
</script>

<td
  data-numeric={numeric ? 'true' : undefined}
  class={cn('px-3 py-2 align-middle', numeric && 'numeric text-right', className)}
  {...rest}
>
  {@render children?.()}
</td>

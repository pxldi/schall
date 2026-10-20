<script lang="ts">
  import type { Snippet } from 'svelte';
  import type { HTMLThAttributes } from 'svelte/elements';
  import { cn } from '$lib/utils';

  // A column name. The header keeps its own bottom rule, while the columns stay
  // open to the rows below it. Vertical rules made the table read as a grid
  // before the reader had reached its data.
  //
  // The name itself takes the smallest step in the product, which is the one
  // role allowed under 12px: a column heading repeats in full, in the ordinary
  // size, whatever it introduces.
  //
  // `numeric` is a column of figures. It is set right, so the digits line up on
  // the units, and the heading follows the column it names.
  let {
    class: className,
    numeric = false,
    children,
    ...rest
  }: HTMLThAttributes & { numeric?: boolean; children?: Snippet } = $props();
</script>

<th
  scope="col"
  class={cn(
    'label whitespace-nowrap px-3 py-2 align-middle',
    numeric && 'text-right',
    className
  )}
  {...rest}
>
  {@render children?.()}
</th>

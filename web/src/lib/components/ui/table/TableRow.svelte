<script lang="ts">
  import type { Snippet } from 'svelte';
  import type { HTMLAttributes } from 'svelte/elements';
  import { cn } from '$lib/utils';

  // One row. `selected` is the row somebody is acting on, and it is a surface
  // rather than a colour: nothing about a table row is a state the API reports,
  // so nothing about it may borrow a status hue.
  //
  // A row is 36px with a 1px rule under it, and a hovered row takes the raised
  // fill.
  let {
    class: className,
    selected = false,
    children,
    ...rest
  }: HTMLAttributes<HTMLTableRowElement> & { selected?: boolean; children?: Snippet } = $props();
</script>

<tr
  data-selected={selected ? '' : undefined}
  aria-selected={selected ? true : undefined}
  class={cn(
    'h-9 border-b border-rule transition hover:bg-raised data-selected:bg-panel',
    className
  )}
  {...rest}
>
  {@render children?.()}
</tr>

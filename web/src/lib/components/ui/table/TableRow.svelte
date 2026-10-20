<script lang="ts">
  import type { Snippet } from 'svelte';
  import type { HTMLAttributes } from 'svelte/elements';
  import { cn } from '$lib/utils';

  // One row. `selected` is the row somebody is acting on, and it is a surface
  // rather than a colour: nothing about a table row is a state the API reports,
  // so nothing about it may borrow a status hue.
  //
  // The separator between rows is Line Inset, the quietest line token: sixty
  // rows of it in one glance read as a texture, not sixty individual borders.
  // Line Thin stays on the table's own outer frame, which only has to read
  // once.
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
    'border-b border-line-inset transition hover:bg-surface-thick data-selected:bg-surface-regular',
    className
  )}
  {...rest}
>
  {@render children?.()}
</tr>

<script lang="ts">
  import { DropdownMenu as MenuPrimitive } from 'bits-ui';
  import { cn } from '$lib/utils';

  // One decision in the list.
  //
  // `data-highlighted` is where the keyboard is, which is not the same as where
  // the pointer is: a menu is walked with the arrow keys and the item under the
  // arrow has to look no different from the item under the mouse, or a reader
  // using one cannot follow a screenshot taken with the other. Bits UI sets the
  // attribute for both, so one rule draws both.
  //
  // A refused item stays in the list rather than vanishing, so the set of
  // choices is the same length on every row, and the words under it say why.
  let { class: className, ...rest }: MenuPrimitive.ItemProps = $props();
</script>

<MenuPrimitive.Item
  class={cn(
    'flex w-full cursor-default select-none items-center gap-2 rounded-control px-2.5 py-[7px]',
    'text-left text-dense-body font-medium text-ink transition-[background]',
    'data-highlighted:bg-surface-thick',
    'data-disabled:pointer-events-none data-disabled:opacity-50',
    className
  )}
  {...rest}
/>

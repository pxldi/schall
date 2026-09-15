<script lang="ts">
  import { DropdownMenu as MenuPrimitive } from 'bits-ui';
  import { cn } from '$lib/utils';

  // The line above a group of items, saying what the group is choosing between.
  // It takes `.label`, which is the smallest step in the product and the one
  // role allowed under 12px: a heading that repeats in full, in the ordinary
  // size, whatever it introduces.
  let { class: className, ...rest }: MenuPrimitive.GroupHeadingProps = $props();
</script>

<MenuPrimitive.GroupHeading class={cn('label block px-2.5 pb-1 pt-1.5', className)} {...rest} />

<script lang="ts">
  import { DropdownMenu as MenuPrimitive } from 'bits-ui';
  import { cn } from '$lib/utils';

  // A rule between two groups of items. It says the list has more than one kind
  // of thing in it, and it is the thinnest line the system draws, because a
  // menu is small and a strong rule inside one reads as two menus.
  let { class: className, ...rest }: MenuPrimitive.SeparatorProps = $props();
</script>

<MenuPrimitive.Separator class={cn('-mx-1 my-1 h-px bg-line-thin', className)} {...rest} />

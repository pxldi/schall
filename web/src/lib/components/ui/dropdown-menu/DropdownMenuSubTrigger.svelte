<script lang="ts">
  import type { Snippet } from 'svelte';
  import { DropdownMenu as MenuPrimitive } from 'bits-ui';
  import { ChevronRight } from '@lucide/svelte';
  import { cn } from '$lib/utils';

  // An item that opens a list of its own instead of doing something. The
  // chevron is the only mark that says a list follows, so it is never left off:
  // without it the item reads as an action that does nothing on press.
  let {
    class: className,
    children,
    ...rest
  }: MenuPrimitive.SubTriggerProps & { children?: Snippet } = $props();
</script>

<MenuPrimitive.SubTrigger
  class={cn(
    'flex w-full cursor-default select-none items-center gap-2 rounded-control px-2.5 py-[7px]',
    'text-left text-dense-body font-medium text-ink transition-[background]',
    'data-highlighted:bg-surface-thick data-[state=open]:bg-surface-thick',
    'data-disabled:pointer-events-none data-disabled:opacity-50',
    className
  )}
  {...rest}
>
  <span class="min-w-0 flex-1">{@render children?.()}</span>
  <ChevronRight size={13} class="shrink-0 text-ink-3" aria-hidden="true" />
</MenuPrimitive.SubTrigger>

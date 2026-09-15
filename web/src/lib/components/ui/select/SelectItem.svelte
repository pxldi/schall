<script lang="ts">
  import type { Snippet } from 'svelte';
  import { Select as SelectPrimitive } from 'bits-ui';
  import { Check } from '@lucide/svelte';
  import { cn } from '$lib/utils';

  // One choice.
  //
  // The chosen one carries a tick, and the tick sits in a column of its own so
  // that the words of every choice start at the same place whether they are
  // chosen or not. Colour is not what says a row is chosen: this is the same
  // rule the checkbox follows, and for the same reason.
  //
  // `data-highlighted` is where the keyboard is. The list is walked with the
  // arrow keys and typed at — a reader who types "fl" while it is open lands on
  // the first choice beginning with those letters — so the row under the arrow
  // must look the same as the row under the mouse.
  //
  // On a touch screen the row is given the 44px a fingertip needs. A native
  // drop-down handed the choosing to the operating system's own picker, which
  // is finger-sized whatever the page says; this list is drawn by the page, so
  // the height is the page's to say. `pointer: coarse` is the same condition
  // the stylesheet already uses for `.field` and the navigation rail.
  let {
    class: className,
    children: label,
    ...rest
    // The primitive's own `children` is handed the selected state as an
    // argument. What a caller writes here is the choice's words and nothing
    // else, so the plain snippet type replaces it.
  }: Omit<SelectPrimitive.ItemProps, 'children' | 'child'> & { children?: Snippet } = $props();
</script>

<SelectPrimitive.Item
  class={cn(
    'flex w-full cursor-default select-none items-center gap-2 rounded-control py-[7px] pl-2 pr-2.5',
    'pointer-coarse:min-h-11',
    'text-left text-dense-body text-ink transition-[background]',
    'data-highlighted:bg-surface-thick',
    'data-disabled:pointer-events-none data-disabled:opacity-50',
    className
  )}
  {...rest}
>
  {#snippet children({ selected })}
    <span class="grid size-4 shrink-0 place-items-center text-accent">
      {#if selected}
        <Check size={13} aria-hidden="true" />
      {/if}
    </span>
    <span class="min-w-0">{@render label?.()}</span>
  {/snippet}
</SelectPrimitive.Item>

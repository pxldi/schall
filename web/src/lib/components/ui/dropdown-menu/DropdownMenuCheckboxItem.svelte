<script lang="ts">
  import type { Snippet } from 'svelte';
  import { DropdownMenu as MenuPrimitive } from 'bits-ui';
  import { Check } from '@lucide/svelte';
  import { cn } from '$lib/utils';

  // An item that is on or off rather than an action. The tick sits in a column
  // of its own so the words of every item in the list start at the same place,
  // whether that item carries a mark or not.
  //
  // `closeOnSelect` is false by default here. A list of things to show or hide
  // is nearly always set more than one at a time, and a menu that shuts on the
  // first press makes the reader open it again for the second.
  let {
    class: className,
    checked = $bindable(false),
    indeterminate = $bindable(false),
    closeOnSelect = false,
    children: label,
    ...rest
    // The primitive's own `children` is handed the checked state as an
    // argument. What a caller writes here is the item's words and nothing else,
    // so the plain snippet type replaces it and the state is read from the
    // snippet declared below instead.
  }: Omit<MenuPrimitive.CheckboxItemProps, 'children' | 'child'> & { children?: Snippet } =
    $props();
</script>

<MenuPrimitive.CheckboxItem
  bind:checked
  bind:indeterminate
  {closeOnSelect}
  class={cn(
    'flex w-full cursor-default select-none items-center gap-2 rounded-control py-[7px] pl-2 pr-2.5',
    'text-left text-dense-body font-medium text-ink transition-[background]',
    'data-highlighted:bg-surface-thick',
    'data-disabled:pointer-events-none data-disabled:opacity-50',
    className
  )}
  {...rest}
>
  {#snippet children({ checked: on, indeterminate: partly })}
    <span class="grid size-4 shrink-0 place-items-center text-accent">
      {#if partly}
        <span aria-hidden="true" class="h-[2px] w-[9px] rounded-full bg-accent"></span>
      {:else if on}
        <Check size={13} aria-hidden="true" />
      {/if}
    </span>
    <span class="min-w-0">{@render label?.()}</span>
  {/snippet}
</MenuPrimitive.CheckboxItem>

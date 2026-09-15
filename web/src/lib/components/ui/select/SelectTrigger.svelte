<script lang="ts">
  import type { Snippet } from 'svelte';
  import { Select as SelectPrimitive } from 'bits-ui';
  import { ChevronDown } from '@lucide/svelte';
  import { cn } from '$lib/utils';

  // The control that shows the chosen value and opens the list.
  //
  // It takes the `.field` class, so it is the same height, the same fill and
  // the same corner as every text box on the same form. A picker that is two
  // pixels shorter than the field beside it is the cheapest kind of wrong.
  //
  // The chevron is the only mark that says a list follows. It turns over while
  // the list is open, at the state step, which is the shortest thing the motion
  // scale draws: nothing travels and nothing arrives.
  let {
    class: className,
    children,
    ...rest
  }: SelectPrimitive.TriggerProps & { children?: Snippet } = $props();
</script>

<SelectPrimitive.Trigger
  class={cn(
    // The reading face rather than the field's mono. A value that was typed is
    // data and is set in mono; a value that was chosen from a list is words.
    // The stylesheet used to say this as `select.field { font-sans }`, keyed
    // off the element. There is no `<select>` in the product any more, so the
    // decision is said here, on the button Bits UI draws in its place.
    'field flex w-full items-center justify-between gap-2 text-left font-sans',
    'data-placeholder:text-ink-4',
    'transition hover:border-line-thick',
    'disabled:pointer-events-none disabled:opacity-50',
    className
  )}
  {...rest}
>
  <span class="min-w-0 truncate">{@render children?.()}</span>
  <ChevronDown
    size={14}
    aria-hidden="true"
    class="shrink-0 text-ink-3 transition-transform in-data-[state=open]:rotate-180"
  />
</SelectPrimitive.Trigger>

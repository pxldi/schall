<script lang="ts">
  import type { Snippet } from 'svelte';
  import { Select as SelectPrimitive } from 'bits-ui';
  import { ChevronDown, ChevronUp } from '@lucide/svelte';
  import { cn } from '$lib/utils';

  // The list of choices.
  //
  // Portalled and collision-aware for the reasons written out in
  // `PopoverContent.svelte`. A picker in a settings card near the bottom of the
  // page has to open upwards, and one inside a scrolling panel must not be cut
  // off at that panel's edge.
  //
  // The list is capped in height and scrolls inside itself. The two chevrons
  // are the scroll buttons: they appear only when there is more list above or
  // below, and they are what makes a long list usable with a pointer that has
  // no wheel.
  //
  // `--bits-select-anchor-width` is the width of the trigger, which Bits UI
  // measures and hands to the panel. The list is at least as wide as the
  // control it belongs to, so the chosen value does not move sideways when the
  // list opens.
  let {
    class: className,
    sideOffset = 6,
    collisionPadding = 12,
    children,
    portalTo,
    ...rest
  }: SelectPrimitive.ContentProps & {
    /** Where the list is drawn. The document body unless a caller says otherwise. */
    portalTo?: string | Element;
    children?: Snippet;
  } = $props();
</script>

<SelectPrimitive.Portal to={portalTo}>
  <SelectPrimitive.Content
    {sideOffset}
    {collisionPadding}
    avoidCollisions
    escapeKeydownBehavior="close"
    class={cn(
      'overlay-panel z-50 max-h-[18rem] min-w-[var(--bits-select-anchor-width)]',
      'max-w-[calc(100vw-1.5rem)] overflow-hidden rounded-panel p-1',
      'text-dense-body text-ink outline-none',
      className
    )}
    {...rest}
  >
    <SelectPrimitive.ScrollUpButton class="flex h-5 items-center justify-center text-ink-3">
      <ChevronUp size={13} aria-hidden="true" />
    </SelectPrimitive.ScrollUpButton>
    <SelectPrimitive.Viewport class="max-h-[16rem] overflow-y-auto">
      {@render children?.()}
    </SelectPrimitive.Viewport>
    <SelectPrimitive.ScrollDownButton class="flex h-5 items-center justify-center text-ink-3">
      <ChevronDown size={13} aria-hidden="true" />
    </SelectPrimitive.ScrollDownButton>
  </SelectPrimitive.Content>
</SelectPrimitive.Portal>

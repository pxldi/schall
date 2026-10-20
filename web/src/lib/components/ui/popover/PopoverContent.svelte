<script lang="ts">
  import { Popover as PopoverPrimitive } from 'bits-ui';
  import { cn } from '$lib/utils';

  // A panel anchored to the control that opened it.
  //
  // Two things are set here that every floating surface in this directory sets,
  // and they are the whole reason a positioning library was taken as a
  // dependency.
  //
  // **Collision handling.** `avoidCollisions` measures the room between the
  // trigger and the edge of the window on each side, and flips or shifts the
  // panel to whichever side it fits on. `collisionPadding` is how close to that
  // edge the panel is allowed to come. What this replaces is a fixed number
  // written by hand — `const flipRoom = 200;` in `Menu.svelte` — which guesses
  // at the panel's height before the panel exists and is wrong for every panel
  // that is not that height.
  //
  // **A `Portal`.** The panel is drawn at the end of the document instead of
  // beside its trigger. A popover opened from inside a table cell, a scrolling
  // list, or any box that hides its overflow is otherwise cut off at that box's
  // edge.
  let {
    class: className,
    side = 'bottom',
    align = 'start',
    sideOffset = 6,
    collisionPadding = 12,
    portalTo,
    ...rest
  }: PopoverPrimitive.ContentProps & {
    /** Where the panel is drawn. The document body unless a caller says otherwise. */
    portalTo?: string | Element;
  } = $props();
</script>

<PopoverPrimitive.Portal to={portalTo}>
  <PopoverPrimitive.Content
    {side}
    {align}
    {sideOffset}
    {collisionPadding}
    avoidCollisions
    escapeKeydownBehavior="close"
    class={cn(
      'overlay-panel z-50 max-w-[calc(100vw-1.5rem)] rounded-panel p-3',
      'text-dense-body text-ink outline-none',
      className
    )}
    {...rest}
  />
</PopoverPrimitive.Portal>

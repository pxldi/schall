<script lang="ts">
  import { DropdownMenu as MenuPrimitive } from 'bits-ui';
  import { cn } from '$lib/utils';

  // The panel a menu trigger opens. It is portalled and collision-aware for the
  // reasons written out in `PopoverContent.svelte`: a menu opened from the end
  // of a table row is inside a box that hides its overflow, and a menu opened
  // near the bottom of the window has to be able to open upwards.
  //
  // `loop` is what makes the arrow keys wrap from the last item back to the
  // first. Without it a keyboard reader who holds the down arrow stops at the
  // bottom of the list with no sign that the list has ended.
  let {
    class: className,
    side = 'bottom',
    align = 'end',
    sideOffset = 6,
    collisionPadding = 12,
    loop = true,
    portalTo,
    ...rest
  }: MenuPrimitive.ContentProps & {
    /** Where the panel is drawn. The document body unless a caller says otherwise. */
    portalTo?: string | Element;
  } = $props();
</script>

<MenuPrimitive.Portal to={portalTo}>
  <MenuPrimitive.Content
    {side}
    {align}
    {sideOffset}
    {collisionPadding}
    {loop}
    avoidCollisions
    escapeKeydownBehavior="close"
    class={cn(
      'overlay-panel z-50 min-w-[10rem] max-w-[calc(100vw-1.5rem)] overflow-hidden rounded-panel p-1',
      'text-dense-body text-ink outline-none',
      className
    )}
    {...rest}
  />
</MenuPrimitive.Portal>

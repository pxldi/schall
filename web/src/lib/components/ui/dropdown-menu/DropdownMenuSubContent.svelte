<script lang="ts">
  import { DropdownMenu as MenuPrimitive } from 'bits-ui';
  import { cn } from '$lib/utils';

  // The list a sub-trigger opens. It sits beside its parent and flips to the
  // other side when there is no room, which is the case a hand-written menu
  // gets wrong most often: a sub-menu near the right edge of the window opens
  // off the screen and cannot be reached at all.
  let {
    class: className,
    sideOffset = 2,
    collisionPadding = 12,
    loop = true,
    portalTo,
    ...rest
  }: MenuPrimitive.SubContentProps & {
    /** Where the panel is drawn. The document body unless a caller says otherwise. */
    portalTo?: string | Element;
  } = $props();
</script>

<MenuPrimitive.Portal to={portalTo}>
  <MenuPrimitive.SubContent
    {sideOffset}
    {collisionPadding}
    {loop}
    avoidCollisions
    escapeKeydownBehavior="close"
    class={cn(
      'overlay-panel z-50 min-w-[9rem] max-w-[calc(100vw-1.5rem)] overflow-hidden rounded-panel p-1',
      'text-dense-body text-ink outline-none',
      className
    )}
    {...rest}
  />
</MenuPrimitive.Portal>

<script lang="ts">
  import { Tooltip as TooltipPrimitive } from 'bits-ui';
  import { cn } from '$lib/utils';

  // The word a glyph stands for, shown beside it.
  //
  // A tooltip is not a popover with a shorter delay. It holds no controls,
  // nothing in it can be reached or pressed, and it is drawn while a pointer
  // rests on the control or while the keyboard focus is on it — which is the
  // part that matters here, because the narrow rail is seven icons and a person
  // who cannot hover has to be able to learn what they are.
  //
  // It sets the two things every floating surface in this directory sets:
  // collision handling, so the panel flips to the side it fits on rather than
  // guessing, and a `Portal`, so it is drawn at the end of the document instead
  // of inside the rail, whose own box would otherwise cut it off.
  let {
    class: className,
    side = 'right',
    sideOffset = 8,
    collisionPadding = 12,
    ...rest
  }: TooltipPrimitive.ContentProps = $props();
</script>

<TooltipPrimitive.Portal>
  <!-- `role="tooltip"` is what says this panel is the description of the control
       that opened it. The library wires the control to it by id and leaves the
       role off; without it the panel is an anonymous box, and a reader following
       the description arrives at text with nothing saying what it belongs to. -->
  <TooltipPrimitive.Content
    role="tooltip"
    {side}
    {sideOffset}
    {collisionPadding}
    avoidCollisions
    class={cn(
      'overlay-panel z-50 rounded-panel px-2 py-1',
      'text-meta font-medium text-ink outline-none',
      className
    )}
    {...rest}
  />
</TooltipPrimitive.Portal>

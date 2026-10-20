<script lang="ts">
  import { Dialog as DialogPrimitive } from 'bits-ui';
  import { cn } from '$lib/utils';

  // The cover between the page and the panel. `.scrim` is where its colour, its
  // blur and its arrival are written; nothing about them is restated here.
  //
  // It sits above the application's own layers — the mobile bar is 30 and the
  // navigation drawer is 40 — because a modal is the one surface nothing may be
  // drawn on top of.
  let { class: className, ...rest }: DialogPrimitive.OverlayProps = $props();
</script>

<DialogPrimitive.Overlay class={cn('scrim z-50', className)} {...rest} />

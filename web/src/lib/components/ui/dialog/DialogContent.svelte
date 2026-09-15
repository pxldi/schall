<script lang="ts">
  import type { Snippet } from 'svelte';
  import { Dialog as DialogPrimitive } from 'bits-ui';
  import { X } from '@lucide/svelte';
  import { cn } from '$lib/utils';
  import DialogOverlay from './DialogOverlay.svelte';

  // The panel itself, and everything that has to be true while it is open.
  //
  // A `Portal` puts the panel at the end of the document instead of where the
  // trigger sits. A dialog opened from inside a table cell, a scrolling list or
  // a panel with rounded corners is otherwise clipped by whichever ancestor
  // hides its overflow — the panel is drawn, and half of it is not on screen.
  //
  // Three behaviours are named rather than left to the default, because they
  // are the reason a headless primitive was taken as a dependency at all:
  //
  //   trapFocus              Tab does not leave the panel. Without it the next
  //                          Tab lands on a control behind the scrim, which a
  //                          keyboard reader cannot see is behind anything.
  //   preventScroll          The page underneath does not scroll while the
  //                          panel is open.
  //   escapeKeydownBehavior  Escape closes this panel. `close` is the plain
  //                          answer and is stated so that a reader of this file
  //                          does not have to know what the default was.
  //
  // The close control is a real button in the corner rather than the scrim
  // alone, because a pointer that never leaves a keyboard has no scrim to press.
  let {
    class: className,
    children,
    portalTo,
    showClose = true,
    ...rest
  }: DialogPrimitive.ContentProps & {
    /** Where the panel is drawn. The document body unless a caller says otherwise. */
    portalTo?: string | Element;
    /** The corner control. Off only where the panel draws a close of its own. */
    showClose?: boolean;
    children?: Snippet;
  } = $props();
</script>

<DialogPrimitive.Portal to={portalTo}>
  <DialogOverlay />
  <DialogPrimitive.Content
    trapFocus
    preventScroll
    escapeKeydownBehavior="close"
    class={cn(
      // Centred while the panel and its air fit, and capped at the height that
      // is left the moment they do not, so a long panel scrolls inside itself
      // rather than growing past the window after the page's own scrollbar has
      // been locked away.
      'overlay-panel fixed left-1/2 top-1/2 z-50 flex max-h-[calc(100vh-3rem)] w-[calc(100vw-3rem)] max-w-md',
      '-translate-x-1/2 -translate-y-1/2 flex-col overflow-hidden rounded-panel',
      'text-dense-body text-ink outline-none',
      className
    )}
    {...rest}
  >
    {@render children?.()}
    {#if showClose}
      <DialogPrimitive.Close
        class="press tap absolute right-3 top-3 grid size-7 place-items-center rounded-control text-ink-3 transition hover:bg-surface-thick hover:text-ink"
        aria-label="Close"
      >
        <X size={15} aria-hidden="true" />
      </DialogPrimitive.Close>
    {/if}
  </DialogPrimitive.Content>
</DialogPrimitive.Portal>

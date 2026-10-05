<script lang="ts">
  import type { Component } from 'svelte';
  import { cn } from '$lib/utils';

  // Every icon in Schall is a Lucide icon drawn through this, so they share one
  // stroke and three sizes (ADR Duoton). Text glyphs ("->", "↑↓", "×") are not
  // icons and are not drawn anywhere a control or a direction is meant.
  //
  //   import { ArrowRight } from '@lucide/svelte';
  //   <Icon icon={ArrowRight} />
  //
  // An icon beside a word is decoration and is hidden from assistive
  // technology. An icon that stands alone in a control gets its name from the
  // control (`aria-label` on the button), not from here.

  let {
    icon: Glyph,
    size = 'md',
    class: className
  }: {
    /** A Lucide component. Typed loosely so it is not tied to one release of
     *  the library's prop types. */
    // eslint-disable-next-line @typescript-eslint/no-explicit-any
    icon: Component<any>;
    /** `sm` inside a chip or a row (14px at the 16px design root), `md` beside
     *  body text and in buttons (16px), `lg` at the head of an empty panel (20px). */
    size?: 'sm' | 'md' | 'lg';
    class?: string;
  } = $props();

  // In rem, so an icon follows the root font-size like the text beside it.
  const sizes = { sm: '0.875rem', md: '1rem', lg: '1.25rem' };
</script>

<Glyph
  size={sizes[size]}
  strokeWidth={1.75}
  absoluteStrokeWidth={false}
  aria-hidden="true"
  class={cn('shrink-0', className)}
/>

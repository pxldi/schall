<script lang="ts">
  import type { Component } from 'svelte';
  import { icons, type IconName } from '$lib/icons';
  import { cn } from '$lib/utils';

  // Every icon in new code is drawn through this, by name, so they share one
  // stroke and three sizes and the whole set can be swapped in `$lib/icons`
  // (ADR Duoton). Text glyphs ("->", "↑↓", "×") are not icons and are not drawn
  // anywhere a control or a direction is meant.
  //
  //   <Icon name="arrow-right" />
  //
  // `icon` takes a library component directly. It exists for the code written
  // before the names did; prefer `name`.
  //
  // An icon beside a word is decoration and is hidden from assistive
  // technology. An icon that stands alone in a control gets its name from the
  // control (`aria-label` on the button), not from here.

  let {
    name,
    icon,
    size = 'md',
    class: className
  }: {
    /** A Lucide component. Typed loosely so it is not tied to one release of
     *  the library's prop types. */
    // eslint-disable-next-line @typescript-eslint/no-explicit-any
    icon?: Component<any>;
    name?: IconName;
    /** `sm` inside a chip or a row (14px at the 16px design root), `md` beside
     *  body text and in buttons (16px), `lg` at the head of an empty panel (20px). */
    size?: 'sm' | 'md' | 'lg';
    class?: string;
  } = $props();

  // In rem, so an icon follows the root font-size like the text beside it.
  const sizes = { sm: '0.875rem', md: '1rem', lg: '1.25rem' };

  const Glyph = $derived(name ? icons[name] : icon);
</script>

{#if Glyph}
<Glyph
  size={sizes[size]}
  strokeWidth={1.75}
  absoluteStrokeWidth={false}
  aria-hidden="true"
  class={cn('shrink-0', className)}
/>
{/if}

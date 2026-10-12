<script lang="ts">
  import type { Snippet } from 'svelte';

  // The top of a page in Duoton: the page's name set very large over the print,
  // with a line of context under it and the page's actions to the right. It
  // fills the print below the top bar, so the title always sits on the print
  // whatever height the page asked for in `usePagePrint`.
  //
  // It is the page's <h1>. A page that draws a Hero draws no PageHeader.
  //
  // The seven destinations of the top bar pass `section`. Their prints differ
  // in height, and a title sitting on the foot of each print landed between
  // 145 and 289px down depending on the page. A section title instead hangs
  // from a fixed distance under the top bar at one size, so it does not move
  // when the reader goes from one destination to the next.

  let {
    title,
    size = 'l',
    kicker,
    sub,
    actions,
    back,
    section = false
  }: {
    title: string;
    /** A top-bar destination: `xl`, at the same place on every one of them. */
    section?: boolean;
    /** `xl` for one-word list pages (Artists, Library), `l` for a release or an
     *  artist, `m` for a long name. */
    size?: 'm' | 'l' | 'xl';
    /** A short line above the title: what kind of thing this is. */
    kicker?: Snippet;
    /** The line under the title. Each direct child is separated by a dot. */
    sub?: Snippet;
    actions?: Snippet;
    /** A way back, drawn at the top left of the print. */
    back?: Snippet;
  } = $props();

  // Written out per size, because Tailwind reads class names as literal text.
  const sizes = {
    m: 'text-poster-m',
    l: 'text-poster-l',
    xl: 'text-poster-xl'
  };
</script>

<section
  class="relative flex flex-col px-4 pb-4 sm:px-6 {section ? 'justify-start pt-16' : 'justify-end'}"
 
  style="min-height: calc(var(--print-height) - var(--topbar-height));"
>
  <!-- The way back sits at the top of the print, where it is in the same place
       on every page whatever the title's size. The auto margin takes the free
       height that would otherwise go above the title. -->
  {#if back}
    <div class="mb-auto pt-4 pb-4">{@render back()}</div>
  {/if}
  {#if kicker}
    <p class="mb-1 text-dense-meta text-ink-2">{@render kicker()}</p>
  {/if}
  <div class="flex items-end justify-between gap-6">
    <div class="min-w-0">
      <h1 class="-ml-[0.04em] mb-3 font-extrabold text-balance text-ink {sizes[section ? 'xl' : size]}">{title}</h1>
      {#if sub}
        <div class="hero-sub text-quiet-meta text-ink-2">{@render sub()}</div>
      {/if}
    </div>
    {#if actions}
      <div class="flex shrink-0 flex-wrap items-center gap-2">{@render actions()}</div>
    {/if}
  </div>
</section>

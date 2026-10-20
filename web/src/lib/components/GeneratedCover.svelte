<script lang="ts">
  import { canPaint, paintGenerated } from '$lib/duoton/paint';
  import type { Inks } from '$lib/duoton/inks';
  import { duoton } from '$lib/duoton/page.svelte';
  import { cn } from '$lib/utils';

  // The cover a release, artist or playlist gets when it has no art (ADR
  // Duoton). It replaces the letter circles and grey squares: a pattern drawn
  // from the thing's palette when one is known, otherwise in the two inks. The
  // seed picks the pattern, so the same thing looks the same everywhere.
  //
  // It is a picture of nothing in particular, so it carries no alt text; the
  // name beside it is what says what it is.

  let {
    seed,
    inks,
    class: className
  }: {
    /** Usually the album id, or "artist title" where there is no id. */
    seed: string;
    /** The thing's own inks or palette. Defaults to the open page's two inks. */
    inks?: Partial<Inks> | null;
    class?: string;
  } = $props();

  let canvas = $state<HTMLCanvasElement>();
  let size = $state(0);

  // The page's palette belongs to the page's cover, not to this thing, so only
  // the page's two inks are borrowed.
  const pair = $derived(inks ?? { dark: duoton.inks.dark, light: duoton.inks.light });

  $effect(() => {
    if (!canvas || !canPaint()) return;
    const observer = new ResizeObserver(([entry]) => {
      // Twice the box, so a 42px thumbnail stays crisp on a dense screen.
      size = Math.max(32, Math.round(Math.max(entry.contentRect.width, entry.contentRect.height) * 2));
    });
    observer.observe(canvas);
    return () => observer.disconnect();
  });

  $effect(() => {
    if (!canvas || !size) return;
    canvas.width = canvas.height = size;
    paintGenerated(canvas, seed, pair);
  });
</script>

<canvas
  bind:this={canvas}
  class={cn('block bg-duo-dark', className)}
  data-generated={seed}
  aria-hidden="true"
></canvas>

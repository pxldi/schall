<script lang="ts">
  import { canPaint, loadPicture, paintPrint, type PrintSource } from '$lib/duoton/paint';
  import { duoton, resolveCover, type CoverRef, type ResolvedCover } from '$lib/duoton/page.svelte';
  import type { Inks } from '$lib/duoton/inks';
  import { clampInks } from '$lib/duoton/inks';
  import { cn } from '$lib/utils';

  // The duotone print (ADR Duoton): covers softened and mapped between two
  // inks. The shell draws the page's own print from `duoton`; a page can also
  // place one of its own, for a card or a playlist row, with this component.
  //
  // It is drawing and nothing else, so it is hidden from assistive technology.
  // The canvas is sized to its box in CSS pixels, not device pixels: the print
  // is blurred on purpose and a retina copy would cost four times the work for
  // nothing anyone could see.

  let {
    covers,
    mode = 'strip',
    inks,
    height,
    class: className
  }: {
    covers: CoverRef[];
    mode?: 'strip' | 'single';
    /** The two inks. Defaults to the open page's. */
    inks?: Inks | null;
    /** Height in rem. Left out, the print fills its box. */
    height?: number;
    class?: string;
  } = $props();

  let canvas = $state<HTMLCanvasElement>();
  let size = $state({ width: 0, height: 0 });
  let sources = $state<PrintSource[]>([]);

  const pair = $derived(inks ? clampInks(inks) : duoton.inks);
  // With no covers at all the print is generated art seeded from the page, so
  // an empty page is still printed rather than blank.
  const refs = $derived<ResolvedCover[]>(
    covers.length ? covers.map(resolveCover) : [0, 1, 2, 3, 4, 5].map((n) => ({ seed: `schall ${n}` }))
  );

  $effect(() => {
    const wanted = refs;
    let live = true;
    // Generated stand-ins draw at once; pictures replace them as they land.
    sources = wanted.map((ref) => ({ seed: ref.seed, inks: ref.inks }));
    wanted.forEach((ref, i) => {
      if (!ref.src) return;
      void loadPicture(ref.src).then((image) => {
        if (!live || !image) return;
        sources = sources.map((source, j) => (j === i ? { ...source, image } : source));
      });
    });
    return () => {
      live = false;
    };
  });

  $effect(() => {
    if (!canvas || !canPaint()) return;
    const observer = new ResizeObserver(([entry]) => {
      size = {
        width: Math.round(entry.contentRect.width),
        height: Math.round(entry.contentRect.height)
      };
    });
    observer.observe(canvas);
    return () => observer.disconnect();
  });

  $effect(() => {
    if (!canvas || !size.width || !size.height || !canPaint()) return;
    canvas.width = size.width;
    canvas.height = size.height;
    paintPrint(canvas, sources, mode, pair);
  });
</script>

<div
  class={cn('print relative overflow-hidden bg-duo-dark', className)}
  style:height={height === undefined ? undefined : `${height}rem`}
  data-mode={mode}
  aria-hidden="true"
>
  <canvas bind:this={canvas} class="absolute inset-0 block size-full"></canvas>
</div>

<script lang="ts">
  import { untrack } from 'svelte';
  import { canPaint, loadPicture, paintPrint, type PrintSource } from '$lib/duoton/paint';
  import { duoton, resolveCover, type CoverRef, type ResolvedCover } from '$lib/duoton/page.svelte';
  import type { Inks } from '$lib/duoton/inks';
  import { clampInks } from '$lib/duoton/inks';
  import { cn } from '$lib/utils';
  import { motionMs } from '$lib/motion.svelte';

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

  // Two canvases, so a new print fades in over the one before it instead of
  // cutting. Each paint goes to the one underneath, which is then raised.
  let canvases = $state<HTMLCanvasElement[]>([]);
  let front = 0;
  let size = $state({ width: 0, height: 0 });
  let sources = $state<PrintSource[]>([]);

  const pair = $derived(inks ? clampInks(inks) : duoton.inks);
  // With no covers at all the print is generated art seeded from the page, so
  // an empty page is still printed rather than blank.
  const refs = $derived<ResolvedCover[]>(
    covers.length ? covers.map(resolveCover) : [0, 1, 2, 3, 4, 5].map((n) => ({ seed: `schall ${n}` }))
  );

  // The page hands over a fresh array whenever its data is fetched again, so
  // the print follows what the covers are rather than which array holds them.
  const refsKey = $derived(JSON.stringify(refs));
  const inksKey = $derived(`${pair.dark} ${pair.light}`);

  // How long a print waits for its pictures before it is drawn with what has
  // arrived. Until then the previous print, or the bare ink, stays up.
  const WAIT_MS = 600;

  $effect(() => {
    void refsKey;
    const wanted = untrack(() => refs);
    let live = true;
    const loaded: (HTMLImageElement | undefined)[] = wanted.map(() => undefined);
    const commit = () => {
      if (!live) return;
      sources = wanted.map((ref, i) => ({ seed: ref.seed, inks: ref.inks, image: loaded[i] }));
    };
    const pending = wanted.map((ref, i) =>
      ref.src
        ? loadPicture(ref.src).then((image) => {
            loaded[i] = image;
          })
        : undefined
    );
    if (!pending.some(Boolean)) {
      commit();
      return () => (live = false);
    }
    // The pictures are drawn together, once, rather than one repaint per
    // picture as each lands. Pictures that are slower than the wait are
    // drawn in a second pass when the last of them arrives.
    const timer = setTimeout(commit, WAIT_MS);
    void Promise.all(pending).then(() => {
      clearTimeout(timer);
      commit();
    });
    return () => {
      live = false;
      clearTimeout(timer);
    };
  });

  $effect(() => {
    const first = canvases[0];
    if (!first || !canPaint()) return;
    const observer = new ResizeObserver(([entry]) => {
      size = {
        width: Math.round(entry.contentRect.width),
        height: Math.round(entry.contentRect.height)
      };
    });
    observer.observe(first);
    return () => observer.disconnect();
  });

  // Painting is held to one per frame, so a size, the covers and the inks
  // changing together cost one paint.
  $effect(() => {
    const { width, height } = size;
    const drawing = sources;
    void inksKey;
    const colours = untrack(() => pair);
    const layout = mode;
    if (canvases.length < 2 || !width || !height || !drawing.length || !canPaint()) return;
    const frame = requestAnimationFrame(() => {
      const next = canvases[1 - front];
      const previous = canvases[front];
      next.width = width;
      next.height = height;
      paintPrint(next, drawing, layout, colours);
      front = 1 - front;
      next.style.zIndex = '1';
      previous.style.zIndex = '0';
      next.style.opacity = '1';
      const ms = motionMs('enter');
      if (ms) next.animate([{ opacity: 0 }, { opacity: 1 }], { duration: ms, easing: 'ease-out' });
    });
    return () => cancelAnimationFrame(frame);
  });
</script>

<div
  class={cn('print relative isolate overflow-hidden bg-duo-dark', className)}
  style:height={height === undefined ? undefined : `${height}rem`}
  data-mode={mode}
  aria-hidden="true"
>
  <canvas bind:this={canvases[0]} class="absolute inset-0 block size-full opacity-0"></canvas>
  <canvas bind:this={canvases[1]} class="absolute inset-0 block size-full opacity-0"></canvas>
</div>

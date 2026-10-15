<script lang="ts" module>
  // Paints from every print on a page share one budget per frame. A page of
  // artists opens with a hundred card prints at once, and painting them all in
  // the same frame blocked the main thread for 90 to 130 ms at a time.
  const FRAME_BUDGET_MS = 8;
  const queue: { run: () => void; cancelled: boolean }[] = [];
  let draining = false;

  function drain() {
    const start = performance.now();
    while (queue.length && performance.now() - start < FRAME_BUDGET_MS) {
      const job = queue.shift()!;
      if (!job.cancelled) job.run();
    }
    if (queue.length) requestAnimationFrame(drain);
    else draining = false;
  }

  function schedulePaint(run: () => void): () => void {
    const job = { run, cancelled: false };
    queue.push(job);
    if (!draining) {
      draining = true;
      requestAnimationFrame(drain);
    }
    return () => (job.cancelled = true);
  }

  // Finished prints, by what they show and the size they were painted at. Going
  // back to a page, or a card scrolling back into view, draws its print from
  // here in the same frame instead of queueing a repaint. A hero is about 2 MB,
  // a card a tenth of that.
  const PAINTED_KEPT = 80;
  const painted = new Map<string, HTMLCanvasElement>();
  function keep(key: string, canvas: HTMLCanvasElement) {
    const copy = document.createElement('canvas');
    copy.width = canvas.width;
    copy.height = canvas.height;
    copy.getContext('2d')?.drawImage(canvas, 0, 0);
    painted.delete(key);
    painted.set(key, copy);
    if (painted.size > PAINTED_KEPT) painted.delete(painted.keys().next().value!);
  }
</script>

<script lang="ts">
  import { untrack } from 'svelte';
  import { canPaint, loadPicture, paintPrint, type PrintSource } from '$lib/duoton/paint';
  import { duoton, resolveCover, type CoverRef, type ResolvedCover } from '$lib/duoton/page.svelte';
  import type { Inks } from '$lib/duoton/inks';
  import { clampInks } from '$lib/duoton/inks';
  import { cn } from '$lib/utils';
  import { coverAt, printPictureWidth } from '$lib/cover-size';
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
  // Whether every picture in `sources` has answered, so the paint is final and
  // worth keeping.
  let complete = $state(false);
  // The covers `sources` was loaded for, which can trail `refsKey` while new
  // pictures load.
  let sourcesFor = '';
  // What the front canvas shows, so a print already drawn from the kept copy
  // is not painted again.
  let shown = '';

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
  const WAIT_MS = 250;

  // A box that is resized repaints once its size has held this long. A window
  // being dragged repainted on every step, and a screenshot that briefly
  // measured the page 320 pixels wide caught the print painted at that width
  // and stretched back across 1440.
  const RESIZE_SETTLE_MS = 150;

  // The pictures are asked for at the width this box draws them at, once its
  // size is known. Every print used to load 500 pixel covers, and a page of
  // them moved megabytes for prints a fifth of that size.
  const pictureWidth = $derived(
    size.width && size.height ? (printPictureWidth(mode, refs.length, size.width, size.height) ?? 500) : 0
  );

  $effect(() => {
    const forKey = refsKey;
    const width = pictureWidth;
    if (!width) return;
    const wanted = untrack(() => refs);
    let live = true;
    const loaded: (HTMLImageElement | undefined)[] = wanted.map(() => undefined);
    const commit = (final: boolean) => {
      if (!live) return;
      sourcesFor = forKey;
      sources = wanted.map((ref, i) => ({ seed: ref.seed, inks: ref.inks, image: loaded[i] }));
      complete = final;
    };
    const pending = wanted.map((ref, i) =>
      ref.src
        ? loadPicture(width === 500 ? ref.src : coverAt(ref.src, width)).then((image) => {
            loaded[i] = image;
          })
        : undefined
    );
    if (!pending.some(Boolean)) {
      commit(true);
      return () => (live = false);
    }
    // The pictures are drawn together, once, rather than one repaint per
    // picture as each lands. Pictures that are slower than the wait are
    // drawn in a second pass when the last of them arrives.
    const timer = setTimeout(() => commit(false), WAIT_MS);
    void Promise.all(pending).then(() => {
      clearTimeout(timer);
      commit(true);
    });
    return () => {
      live = false;
      clearTimeout(timer);
    };
  });

  $effect(() => {
    const first = canvases[0];
    if (!first || !canPaint()) return;
    let settle: ReturnType<typeof setTimeout> | undefined;
    const observer = new ResizeObserver(([entry]) => {
      const next = {
        width: Math.round(entry.contentRect.width),
        height: Math.round(entry.contentRect.height)
      };
      clearTimeout(settle);
      if (!size.width || !size.height) {
        size = next;
        return;
      }
      settle = setTimeout(() => {
        if (next.width !== size.width || next.height !== size.height) size = next;
      }, RESIZE_SETTLE_MS);
    });
    observer.observe(first);
    return () => {
      observer.disconnect();
      clearTimeout(settle);
    };
  });

  // Raises the canvas underneath once something is drawn on it. The first print
  // on a box cuts in over the bare ink, and a print replacing another fades
  // over the state step.
  let drawn = false;
  function raise(next: HTMLCanvasElement, previous: HTMLCanvasElement, key: string) {
    front = 1 - front;
    shown = key;
    next.style.zIndex = '1';
    previous.style.zIndex = '0';
    next.style.opacity = '1';
    const ms = drawn ? motionMs('state') : 0;
    drawn = true;
    if (ms) next.animate([{ opacity: 0 }, { opacity: 1 }], { duration: ms, easing: 'ease-out' });
  }

  // A print painted before at this size is drawn from the kept copy as soon as
  // the box is measured, before its pictures have been asked for.
  $effect(() => {
    const { width, height } = size;
    const key = `${refsKey}|${inksKey}|${mode}|${width}x${height}`;
    if (canvases.length < 2 || !width || !height || shown === key) return;
    const copy = painted.get(key);
    if (!copy) return;
    const next = canvases[1 - front];
    next.width = width;
    next.height = height;
    next.getContext('2d')?.drawImage(copy, 0, 0);
    raise(next, canvases[front], key);
  });

  // Painting waits for the next frame with room in it, so a size, the covers
  // and the inks changing together cost one paint.
  $effect(() => {
    const { width, height } = size;
    const drawing = sources;
    const final = complete;
    void inksKey;
    const colours = untrack(() => pair);
    const layout = mode;
    if (canvases.length < 2 || !width || !height || !drawing.length || !canPaint()) return;
    const key = untrack(() => `${sourcesFor}|${inksKey}|${layout}|${width}x${height}`);
    if (final && shown === key) return;
    const cancel = schedulePaint(() => {
      const next = canvases[1 - front];
      next.width = width;
      next.height = height;
      paintPrint(next, drawing, layout, colours);
      if (final) keep(key, next);
      raise(next, canvases[front], final ? key : '');
    });
    return cancel;
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

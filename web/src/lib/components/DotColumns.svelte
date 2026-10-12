<script lang="ts">
  import type { OverviewDay } from '$lib/api-types';
  import { measure } from '$lib/measure';

  // A count per day printed as stacked dots in the page's light ink, one dot
  // for a fixed number of whatever is counted (ADR Duoton, halftone charts).
  // The unit is the smallest round number that lets the busiest day fit the
  // height the panel gives, so a dot always means the same amount within one
  // chart, and the caller prints that unit as the key. Each dot sits on an
  // offset copy of itself, the Misprint plate.

  let {
    days,
    label,
    unit = $bindable(1),
    onhover,
    onleave,
    class: className = ''
  }: {
    days: OverviewDay[];
    /** The tooltip for one day's column. */
    label: (day: OverviewDay) => string;
    /** Read back by the caller for the key: how many one dot stands for. */
    unit?: number;
    onhover: (event: MouseEvent, text: string) => void;
    onleave: () => void;
    class?: string;
  } = $props();

  let width = $state(0);
  let height = $state(0);

  // The test window measures nothing; draw at a plain size there.
  const w = $derived(width || 600);
  const h = $derived(height || 120);

  const NICE = [1, 2, 4, 5, 10, 20, 25, 50, 100, 200, 250, 500, 1000];

  const layout = $derived.by(() => {
    const column = w / Math.max(1, days.length);
    const r = Math.min(4, Math.max(1.5, column * 0.24));
    const step = 2 * r + Math.max(2, r * 0.8);
    const fits = Math.max(1, Math.floor((h - 2 * r) / step) + 1);
    const max = Math.max(1, ...days.map((day) => day.count));
    const need = Math.ceil(max / fits);
    const per = NICE.find((n) => n >= need) ?? need;
    return { column, r, step, per };
  });

  $effect(() => {
    unit = layout.per;
  });

  // A day with any count shows at least one dot, so a quiet day is not drawn
  // as an empty one; the tooltip carries the exact number.
  function dots(count: number): number {
    if (count === 0) return 0;
    return Math.max(1, Math.round(count / layout.per));
  }
</script>

<div class="relative {className}" use:measure={(w, h) => ((width = w), (height = h))}>
  <svg width={w} height={h} class="absolute inset-0" aria-hidden="true">
    {#each days as day, index (day.date)}
      {@const x = index * layout.column + layout.column / 2}
      <g class={index === days.length - 1 ? 'today' : 'day'}>
        {#each Array.from({ length: dots(day.count) }) as _, k (k)}
          {@const y = h - layout.r - 1 - k * layout.step}
          <circle cx={x + layout.r * 0.4} cy={y + layout.r * 0.4} r={layout.r} class="plate" />
          <circle cx={x} cy={y} r={layout.r} class="dot" />
        {/each}
        {#if day.count === 0}
          <circle cx={x} cy={h - layout.r - 1} r="1" class="none" />
        {/if}
      </g>
      <rect
        x={index * layout.column}
        y="0"
        width={layout.column}
        height={h}
        fill="transparent"
        aria-hidden="true"
        onmouseenter={(event) => onhover(event, label(day))}
        onmouseleave={onleave}
      />
    {/each}
  </svg>
</div>

<style>
  /* Fills as properties: an SVG presentation attribute does not read a custom
     property everywhere. */
  .dot {
    fill: var(--color-duo-light);
  }
  .plate {
    fill: var(--color-duo-light);
    fill-opacity: 0.3;
  }
  .none {
    fill: var(--color-line-regular);
  }
  .day {
    opacity: 0.62;
  }
</style>

<script lang="ts">
  import Skeleton from '$lib/components/Skeleton.svelte';
  import { cn } from '$lib/utils';

  // The placeholder for a list of rows: an optional cover, a title and a line
  // under it, and a short figure at the end. Most lists in Schall are that
  // row. The widths step through a fixed set, so the column reads as text of
  // different lengths and two loads of the same page draw the same shape.

  let {
    count = 8,
    cover = false,
    label,
    class: className
  }: {
    count?: number;
    /** Draw a square cover at the head of every row. */
    cover?: boolean;
    /** What is loading, for assistive technology. Leave out when the panel
     *  around the rows already says so. */
    label?: string;
    class?: string;
  } = $props();

  const titles = ['w-2/5', 'w-1/2', 'w-1/3', 'w-3/5', 'w-2/5', 'w-1/4'];
  const subtitles = ['w-1/4', 'w-1/5', 'w-1/3', 'w-1/5', 'w-1/4', 'w-1/6'];
</script>

<div
  class={cn('flex flex-col', className)}
  aria-busy={label ? 'true' : undefined}
  aria-label={label}
  role={label ? 'status' : undefined}
>
  {#each Array.from({ length: count }, (_, index) => index) as index (index)}
    <div data-placeholder-row class="flex items-center gap-3 border-b border-line-thin px-3 py-2 last:border-b-0">
      {#if cover}<Skeleton shape="cover" class="size-10 shrink-0" />{/if}
      <div class="flex min-w-0 flex-1 flex-col gap-1.5">
        <Skeleton class={cn('h-3.5', titles[index % titles.length])} />
        <Skeleton class={cn('h-3', subtitles[index % subtitles.length])} />
      </div>
      <Skeleton class="h-3 w-10 shrink-0" />
    </div>
  {/each}
</div>

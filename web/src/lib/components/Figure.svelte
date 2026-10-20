<script lang="ts">
  import { changed } from '$lib/motion.svelte';

  // One large mono figure with a small label under it, for the numbers a screen
  // is read for. The figure ticks when its value changes after the first draw.

  let {
    value,
    label,
    size = 'lg',
    title,
    class: className = ''
  }: {
    value: string;
    label?: string;
    size?: 'lg' | 'md';
    title?: string;
    class?: string;
  } = $props();

  const ticked = changed(() => value);
</script>

<div class="flex min-w-0 flex-col gap-1.5 {className}" {title}>
  <span
    class="mono truncate font-semibold leading-none text-ink {size === 'lg' ? 'text-title' : 'text-lead'}"
    class:tick={ticked.now}
    onanimationend={ticked.settle}>{value}</span
  >
  {#if label}<span class="label">{label}</span>{/if}
</div>

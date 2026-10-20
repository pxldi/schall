<script lang="ts">
  import { changed } from '$lib/motion.svelte';

  // How much of something the library holds, beside the mono figure it stands
  // for: four blocks, filled in the `ok` colour for the share held (ADR 0042).
  // Fewer than four things get one block each. A part held never shows as
  // none or all.

  let {
    owned,
    total,
    // What is being counted, for the sentence a screen reader gets in place of
    // blocks and a fraction that mean nothing read aloud.
    noun = 'tracks',
    // "9/12" where a card footer has no room for "9 of 12". The aria-label
    // keeps the full sentence either way.
    compact = false
  }: { owned: number; total: number; width?: number; noun?: string; compact?: boolean } = $props();

  const BLOCKS = 4;
  const blocks = $derived(Math.min(total, BLOCKS));
  const filled = $derived.by(() => {
    if (total <= 0 || owned <= 0) return 0;
    if (owned >= total) return blocks;
    if (total <= BLOCKS) return owned;
    return Math.min(blocks - 1, Math.max(1, Math.round((owned / total) * blocks)));
  });

  // A block that has just filled draws itself left to right; the figure ticks.
  // Neither happens on the first draw.
  const grew = changed(() => filled);
  const counted = changed(() => owned);
  let before = $state(0);
  $effect(() => {
    if (!grew.now) before = filled;
  });
</script>

<span class="inline-flex items-center gap-2" aria-label={`${owned} of ${total} ${noun} in your library`}>
  <span class="flex shrink-0 gap-0.5" aria-hidden="true">
    {#each Array.from({ length: blocks }) as _, index (index)}
      <span
        class="h-2 w-3.5 {index < filled ? 'bg-ok' : 'bg-rule-strong'}"
        class:fill-in={grew.now && index < filled && index >= before}
        data-held={index < filled}
        onanimationend={grew.settle}
      ></span>
    {/each}
  </span>
  <span
    class="mono whitespace-nowrap text-meta text-ink-3"
    class:tick={counted.now}
    onanimationend={counted.settle}>{owned}{compact ? '/' : ' of '}{total}</span
  >
</span>

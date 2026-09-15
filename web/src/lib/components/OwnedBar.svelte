<script lang="ts">
  // How much of something the library holds, drawn the way every redesigned
  // board draws it: a 3px bar, never a percentage alone, beside the mono
  // figure it stands for.
  //
  // This is not `CompletenessBadge.svelte`, which is a pill with the same two
  // figures inside it. The boards draw the bar and the figure loose in a row —
  // a table cell, a card footer — so this component draws only the bar and the
  // figure, with none of a badge's fill or corners.

  let {
    owned,
    total,
    width = 60,
    // What is being counted, for the sentence a screen reader gets in place of
    // a bar and a fraction that mean nothing read aloud.
    noun = 'tracks',
    // A tighter card footer has no room for "9 of 12" beside a check mark, so
    // this reads "9/12" instead. The aria-label keeps the full sentence either
    // way — it is read aloud, not squeezed for space.
    compact = false
  }: { owned: number; total: number; width?: number; noun?: string; compact?: boolean } = $props();

  const fraction = $derived(total > 0 ? owned / total : 0);
</script>

<span class="inline-flex items-center gap-2" aria-label={`${owned} of ${total} ${noun} in your library`}>
  <span class="h-[3px] shrink-0 overflow-hidden rounded-[2px] bg-surface-thin" style:width={`${width}px`}>
    <span class="block h-full rounded-[2px] bg-accent" style:width={`${fraction * 100}%`}></span>
  </span>
  <span class="numeric text-meta text-ink-3">{owned}{compact ? '/' : ' of '}{total}</span>
</span>

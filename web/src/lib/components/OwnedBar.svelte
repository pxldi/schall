<script lang="ts">
  // How much of something the library holds, beside the mono figure it stands
  // for. Printed as dots (ADR Duoton, halftone tables): one per track while
  // they fit the width, so "5 of 5" and "0 of 12" read at a glance. A longer
  // list gets as many dots as fit, each standing for an equal share, filled
  // for the share held; a part held never shows as none or all.
  //
  // This is not `CompletenessBadge.svelte`, which is a pill with the same two
  // figures inside it. The boards draw the dots and the figure loose in a row —
  // a table cell, a card footer — so this component draws only those two, with
  // none of a badge's fill or corners.

  let {
    owned,
    total,
    width = 60,
    // What is being counted, for the sentence a screen reader gets in place of
    // dots and a fraction that mean nothing read aloud.
    noun = 'tracks',
    // A tighter card footer has no room for "9 of 12" beside a check mark, so
    // this reads "9/12" instead. The aria-label keeps the full sentence either
    // way — it is read aloud, not squeezed for space.
    compact = false
  }: { owned: number; total: number; width?: number; noun?: string; compact?: boolean } = $props();

  // A dot and its gap are 8px.
  const fits = $derived(Math.max(4, Math.floor((width + 2) / 8)));
  const dots = $derived(Math.min(total, fits));
  const filled = $derived.by(() => {
    if (total <= 0 || owned <= 0) return 0;
    if (owned >= total) return dots;
    if (total <= fits) return owned;
    return Math.min(dots - 1, Math.max(1, Math.round((owned / total) * dots)));
  });
</script>

<span class="inline-flex items-center gap-2" aria-label={`${owned} of ${total} ${noun} in your library`}>
  <span class="flex shrink-0 flex-wrap gap-[2px]" style:max-width={`${width}px`} aria-hidden="true">
    {#each Array.from({ length: dots }) as _, index (index)}
      <span class="owned-dot" data-held={index < filled}></span>
    {/each}
  </span>
  <span class="numeric text-meta text-ink-3">{owned}{compact ? '/' : ' of '}{total}</span>
</span>

<style>
  /* Held is a state, so it is drawn in the state colour, not the page's ink. */
  .owned-dot {
    width: 6px;
    height: 6px;
    border-radius: 9999px;
    box-shadow: inset 0 0 0 1.5px var(--color-ink-4);
  }
  .owned-dot[data-held='true'] {
    background: var(--color-ok);
    box-shadow: none;
  }
</style>

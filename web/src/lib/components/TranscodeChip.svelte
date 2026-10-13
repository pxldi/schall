<script lang="ts">
  import Chip from './Chip.svelte';

  // The chip that says a copy's audio was squeezed before it reached the
  // container it is in. It appears in the two places two copies are compared:
  // the duplicates page and the review queue's copy rows.
  //
  // It is a suspicion about quality and never about identity, so it names no
  // verdict and blocks nothing. The measurement behind it sits behind the
  // disclosure, where somebody who wants to weigh it can read it and everybody
  // else reads two words.
  let {
    cutoffHz,
    encoder
  }: {
    cutoffHz?: number;
    encoder?: string;
  } = $props();

  const cutoff = $derived(
    cutoffHz ? `${(cutoffHz / 1000).toFixed(1).replace(/\.0$/, '')} kHz` : ''
  );
</script>

<details class="group/transcode inline-block align-middle">
  <summary class="tap-tall inline-flex cursor-pointer list-none items-center">
    <Chip role="decide">Likely transcode</Chip>
  </summary>
  <p class="pt-1 text-meta text-ink-3">
    {#if cutoff}
      Sound cuts off at {cutoff}, like a lossy encode.
    {:else}
      Sound cuts off like a lossy encode.
    {/if}
    {#if encoder}
      Encoder: {encoder}.
    {/if}
  </p>
</details>

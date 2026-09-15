<script lang="ts">
  // A skeleton settling into the answer it stood for.
  //
  // Every loading state used to end between two frames: the pulsing boxes were
  // taken down and the content was drawn from opacity nought, so the whole
  // area blanked for a frame before it rose. Here the two states share one
  // grid cell. The content mounts on top with whatever arrival its page gives
  // it, and the skeleton fades out underneath over the arriving step, so the
  // boxes are still there behind the first frames of the real thing.
  //
  // `motionMs` returns 0 under reduced motion, which is Svelte's own way of
  // saying "no transition": the reader gets the cut the fade replaced.

  import { fade } from 'svelte/transition';
  import { motionMs } from '$lib/motion.svelte';
  import type { Snippet } from 'svelte';

  let {
    pending,
    placeholder,
    children
  }: {
    /** Whether the page is still waiting for its data. */
    pending: boolean;
    /** The skeleton, drawn while waiting and faded out when the answer lands. */
    placeholder: Snippet;
    children: Snippet;
  } = $props();
</script>

<div class="grid">
  {#if pending}
    <!-- pointer-events-none so the fading boxes never sit between a reader and
         the content already drawn over them. -->
    <!-- min-w-0 on both layers: a grid item's min-width is auto, so a row of
         content that refuses to shrink widens the whole page instead of
         truncating. Playlists at 390px was 488px wide through this. -->
    <div class="pointer-events-none min-w-0 [grid-area:1/1]" out:fade={{ duration: motionMs('enter') }}>
      {@render placeholder()}
    </div>
  {:else}
    <div class="min-w-0 [grid-area:1/1]">
      {@render children()}
    </div>
  {/if}
</div>

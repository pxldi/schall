<script module lang="ts">
  import { coverMissing, markCoverMissing } from '$lib/cover-size';
</script>

<script lang="ts">
  import GeneratedCover from './GeneratedCover.svelte';
  import { COVER_WIDTHS, coverAt } from '$lib/cover-size';
  import type { Inks } from '$lib/duoton/inks';

  // A picture that arrives instead of appearing.
  //
  // Every cover and every artist photograph in Schall was a bare image tag, so
  // each one was drawn the instant its last byte landed — full opacity, no
  // transition, in whatever order the network happened to answer. A grid of
  // forty of them reads as flashing, because forty hard cuts at forty
  // unpredictable moments is what flashing is.
  //
  // So each picture fades up over the state step once its bytes are ready. It
  // used to wait 120 ms for its neighbours and fade over 280 ms, and covers
  // read as arriving late. A picture the browser already holds is drawn at
  // once. The box under it is already the right size and colour, so nothing
  // moves.
  //
  // A missing picture is drawn as generated art when the caller gives a seed
  // (ADR Duoton), or as the text fallback a caller supplies. The absence is
  // remembered for the picture at every size, so a missing cover is not asked
  // for again by another component or after a remount.

  let {
    src,
    alt = '',
    class: className = '',
    eager = false,
    onmissing,
    fallback,
    seed,
    inks
  }: {
    src?: string;
    alt?: string;
    class?: string;
    eager?: boolean;
    fallback?: string;
    /** Draws generated art in place of a missing picture, seeded with this
     * (usually the album id). Wins over `fallback`. */
    seed?: string;
    /** The thing's own inks or palette, for the generated art. */
    inks?: Partial<Inks> | null;
    /** Told once, when there turns out to be no picture. The release page uses
     * it to take down the caption that qualifies the picture: a note saying the
     * cover was matched by name, over no cover, is a note about nothing. */
    onmissing?: () => void;
  } = $props();

  let stage = $state<'loading' | 'shown' | 'missing'>('loading');
  // A picture the browser already holds is drawn at once, with no fade: going
  // back to a page shows its covers as they were.
  let instant = $state(false);
  let live = true;
  $effect(() => () => (live = false));

  function held(image: HTMLImageElement) {
    if (image.complete && image.naturalWidth > 0) {
      instant = true;
      stage = 'shown';
    }
  }
  const missing = $derived(!src || stage === 'missing' || coverMissing(src));

  // Covers are stored 500 pixels wide and most are drawn at 40 to 100. The API
  // makes 128 and 256 on request, and the browser picks by the size the image
  // is laid out at (`sizes="auto"`, lazy images only). A browser that cannot
  // read "auto" falls through to 500px and loads the stored picture as before.
  const srcset = $derived(
    !eager && src?.startsWith('/api/')
      ? COVER_WIDTHS.map((width) => `${coverAt(src, width)} ${width}w`)
          .concat(`${src} 500w`)
          .join(', ')
      : undefined
  );
</script>

{#if !missing}
  <!-- loading, sizes and srcset come before src. Svelte sets them in this
       order, and an image whose src is set first starts fetching that src at
       once, so the lazy 128 and 256 pixel covers were never chosen. -->
  <img
    loading={eager ? 'eager' : 'lazy'}
    decoding="async"
    sizes={srcset ? 'auto, 500px' : undefined}
    {srcset}
    {src}
    {alt}
    data-state={stage}
    use:held
    onload={() => {
      if (live) stage = 'shown';
    }}
    onerror={() => {
      stage = 'missing';
      if (src) markCoverMissing(src);
      onmissing?.();
    }}
    class="cover-arrive {stage === 'shown' ? 'is-here' : ''} {instant ? 'is-instant' : ''} {className}"
  />
{:else if seed}
  <GeneratedCover {seed} {inks} class={className} />
{:else if fallback}
  <span class="cover-fallback {className}" aria-hidden="true">{fallback}</span>
{/if}

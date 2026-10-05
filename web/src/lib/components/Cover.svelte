<script module lang="ts">
  const missingSources = new Set<string>();

  // Pictures that land close together are shown together. A grid of covers
  // answered over a few dozen milliseconds then fades up as one, instead of
  // one picture after another in whatever order the network answered.
  const REVEAL_WINDOW_MS = 120;
  let waiting: (() => void)[] = [];
  let revealTimer: ReturnType<typeof setTimeout> | undefined;

  function revealSoon(show: () => void) {
    waiting.push(show);
    revealTimer ??= setTimeout(() => {
      const due = waiting;
      waiting = [];
      revealTimer = undefined;
      for (const reveal of due) reveal();
    }, REVEAL_WINDOW_MS);
  }
</script>

<script lang="ts">
  import GeneratedCover from './GeneratedCover.svelte';
  import type { Inks } from '$lib/duoton/inks';

  // A picture that arrives instead of appearing.
  //
  // Every cover and every artist photograph in Schall was a bare image tag, so
  // each one was drawn the instant its last byte landed — full opacity, no
  // transition, in whatever order the network happened to answer. A grid of
  // forty of them reads as flashing, because forty hard cuts at forty
  // unpredictable moments is what flashing is.
  //
  // So each picture fades up over the arriving step once its bytes are ready,
  // together with any other picture that landed in the same short window, so a
  // grid resolves in one or two steps rather than forty. A picture the browser
  // already holds is drawn at once. The box under it is already the right size
  // and colour, so nothing moves — only the picture resolves into a frame that
  // was always there.
  //
  // A missing picture is drawn as generated art when the caller gives a seed
  // (ADR Duoton), or as the text fallback a caller supplies. The source is remembered so a known absence is not asked
  // for again after a component remounts.

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
  const missing = $derived(!src || stage === 'missing' || missingSources.has(src));
</script>

{#if !missing}
  <img
    {src}
    {alt}
    loading={eager ? 'eager' : 'lazy'}
    decoding="async"
    data-state={stage}
    use:held
    onload={() => {
      if (stage === 'shown') return;
      revealSoon(() => live && (stage = 'shown'));
    }}
    onerror={() => {
      stage = 'missing';
      if (src) missingSources.add(src);
      onmissing?.();
    }}
    class="cover-arrive {stage === 'shown' ? 'is-here' : ''} {instant ? 'is-instant' : ''} {className}"
  />
{:else if seed}
  <GeneratedCover {seed} {inks} class={className} />
{:else if fallback}
  <span class="cover-fallback {className}" aria-hidden="true">{fallback}</span>
{/if}

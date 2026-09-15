<script module lang="ts">
  const missingSources = new Set<string>();
</script>

<script lang="ts">
  // A picture that arrives instead of appearing.
  //
  // Every cover and every artist photograph in Schall was a bare image tag, so
  // each one was drawn the instant its last byte landed — full opacity, no
  // transition, in whatever order the network happened to answer. A grid of
  // forty of them reads as flashing, because forty hard cuts at forty
  // unpredictable moments is what flashing is.
  //
  // So each picture fades up over the arriving step when its own bytes are
  // ready. Nothing is staggered and nothing is delayed: the offsets are the
  // network's, which is the honest source of them and also the reason a list is
  // never given a stagger of its own. The box under it is already the right
  // size and colour, so nothing moves — only the picture resolves into a frame
  // that was always there.
  //
  // A missing picture keeps the frame's existing treatment or the fallback a
  // caller supplies. The source is remembered so a known absence is not asked
  // for again after a component remounts.

  let {
    src,
    alt = '',
    class: className = '',
    eager = false,
    onmissing,
    fallback
  }: {
    src?: string;
    alt?: string;
    class?: string;
    eager?: boolean;
    fallback?: string;
    /** Told once, when there turns out to be no picture. The release page uses
     * it to take down the caption that qualifies the picture: a note saying the
     * cover was matched by name, over no cover, is a note about nothing. */
    onmissing?: () => void;
  } = $props();

  let state = $state<'loading' | 'shown' | 'missing'>('loading');
  const missing = $derived(!src || state === 'missing' || missingSources.has(src));
</script>

{#if !missing}
  <img
    {src}
    {alt}
    loading={eager ? 'eager' : 'lazy'}
    decoding="async"
    data-state={state}
    onload={() => (state = 'shown')}
    onerror={() => {
      state = 'missing';
      if (src) missingSources.add(src);
      onmissing?.();
    }}
    class="cover-arrive {state === 'shown' ? 'is-here' : ''} {className}"
  />
{:else if fallback}
  <span class="cover-fallback {className}" aria-hidden="true">{fallback}</span>
{/if}

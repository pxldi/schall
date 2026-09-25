<script lang="ts">
  import { Check, Pause, Play, X } from '@lucide/svelte';
  import { onDestroy } from 'svelte';
  import { api, type AcquiredCopy } from '$lib/api';
  import { clock, verdict } from '$lib/review';
  import { listening } from '$lib/preview.svelte';

  // One copy, played and compared. Fills its grid cell — the grid, not the
  // card, decides how many sit side by side — so the card never reflows while
  // its own content changes underneath a reader.
  //
  // The waveform is drawn from `GET .../waveform`, read once per copy and kept
  // for the life of the card. A copy nothing has generated one for yet (404)
  // draws a flat line instead of an error — there is still a play button and a
  // scrubber, only nothing to look at while it runs.
  //
  // Only one card plays at a time, coordinated through `listening.sounding`:
  // starting this one pauses whichever other card had it.

  let {
    heading,
    subheading,
    copy,
    wantedMs,
    credit,
    selected,
    onselect
  }: {
    /** "Copy 1", or "In your library" for a stopped want's filed copy. */
    heading: string;
    /** `as "<fileTitle>"`, shown only on the filed card. */
    subheading?: string;
    copy: AcquiredCopy;
    /** The want's wanted duration, for the length comparison. */
    wantedMs?: number | null;
    /** Present only on the filed card: the file's own credit and title,
     * compared against what was wanted. A row is drawn only where the two
     * differ. */
    credit?: { fileArtist?: string; fileTitle?: string; wantedArtist?: string; wantedTitle?: string };
    selected: boolean;
    onselect: () => void;
  } = $props();

  const src = $derived(api.reviewCopyAudioUrl(copy.id));

  let audioEl = $state<HTMLAudioElement | null>(null);
  let loadedSrc = $state('');
  let paused = $state(true);
  let elapsed = $state(0);
  let duration = $state(0);
  let peaks = $state<number[] | null>(null);
  let waveformFailed = $state(false);

  const playing = $derived(loadedSrc === src && listening.sounding === src && !paused);

  // Somebody else took the speakers. The icon reverts and the position holds,
  // the same as an ordinary pause.
  $effect(() => {
    if (loadedSrc === src && listening.sounding !== src) audioEl?.pause();
  });

  $effect(() => {
    if (audioEl) audioEl.volume = listening.volume;
  });

  onDestroy(() => {
    audioEl?.pause();
    if (listening.sounding === src) listening.sounding = '';
  });

  // Read once per copy. A copy the waveform endpoint has not heard of yet
  // (404) draws a flat line rather than failing the card.
  $effect(() => {
    const id = copy.id;
    peaks = null;
    waveformFailed = false;
    let live = true;
    void api
      .reviewCopyWaveform(id)
      .then((result) => {
        if (live) peaks = result.peaks;
      })
      .catch(() => {
        if (live) waveformFailed = true;
      });
    return () => {
      live = false;
    };
  });

  function togglePlay() {
    if (!audioEl) return;
    if (loadedSrc === src) {
      listening.sounding = src;
      if (audioEl.paused) void audioEl.play();
      else audioEl.pause();
      return;
    }
    // Play starts at the beginning. Intros are part of what a person is here
    // to hear, unlike the preview elsewhere in Schall that opens in the
    // middle of a file nobody is comparing note for note.
    loadedSrc = src;
    elapsed = 0;
    duration = 0;
    listening.sounding = src;
    audioEl.src = src;
    audioEl.volume = listening.volume;
    audioEl.load();
  }

  function opened() {
    if (!audioEl) return;
    duration = Number.isFinite(audioEl.duration) ? audioEl.duration : 0;
    void audioEl.play().catch(() => undefined);
  }

  function seek(event: MouseEvent) {
    if (!audioEl || loadedSrc !== src || duration <= 0) return;
    const box = (event.currentTarget as HTMLElement).getBoundingClientRect();
    const ratio = Math.max(0, Math.min(1, (event.clientX - box.left) / box.width));
    audioEl.currentTime = ratio * duration;
    elapsed = audioEl.currentTime;
  }

  const playedRatio = $derived(loadedSrc === src && duration > 0 ? elapsed / duration : 0);

  // The bars, downsampled to a size that reads at 200px wide. `peaks` runs
  // about 200 values already, so this mostly passes them straight through.
  const BARS = 48;
  const bars = $derived.by(() => {
    if (!peaks || peaks.length === 0) return [];
    const perBar = peaks.length / BARS;
    return Array.from({ length: BARS }, (_, index) => {
      const from = Math.floor(index * perBar);
      const to = Math.max(from + 1, Math.floor((index + 1) * perBar));
      const slice = peaks!.slice(from, to);
      const average = slice.reduce((sum, value) => sum + value, 0) / slice.length;
      return average / 255;
    });
  });

  function barHeight(value: number) {
    return 4 + value * 28;
  }

  const observedMs = $derived(copy.evidence?.observed?.durationMs);
  const lengthVerdict = $derived(verdict(copy, 'duration'));
  const lengthText = $derived(
    observedMs == null
      ? '—'
      : lengthVerdict === 'bad' && wantedMs != null
        ? `${clock(observedMs / 1000)}, wanted ${clock(wantedMs / 1000)}`
        : clock(observedMs / 1000)
  );

  const LOSSLESS = new Set(['FLAC', 'WAV', 'ALAC', 'AIFF', 'APE']);
  const formatText = $derived.by(() => {
    const dot = copy.name.lastIndexOf('.');
    const ext = dot > 0 ? copy.name.slice(dot + 1).toUpperCase() : '';
    const rate = copy.evidence?.bitRate ?? 0;
    if (ext && !LOSSLESS.has(ext) && rate > 0) return `${ext} ${rate}`;
    return ext || '—';
  });

  const sizeText = $derived.by(() => {
    const bytes = copy.sizeBytes ?? copy.evidence?.sizeBytes ?? 0;
    if (!bytes) return '—';
    return bytes >= 1e9 ? `${(bytes / 1e9).toFixed(1)} GB` : `${(bytes / 1e6).toFixed(1)} MB`;
  });

  const album = $derived(copy.evidence?.observed?.album?.trim() ?? '');
  const albumVerdict = $derived(verdict(copy, 'album'));

  function differs(a?: string, b?: string) {
    const left = a?.trim().toLocaleLowerCase() ?? '';
    const right = b?.trim().toLocaleLowerCase() ?? '';
    return left !== '' && right !== '' && left !== right;
  }
  const creditRow = $derived(
    credit && differs(credit.fileArtist, credit.wantedArtist) ? credit.fileArtist : ''
  );
  const titleRow = $derived(
    credit && differs(credit.fileTitle, credit.wantedTitle) ? credit.fileTitle : ''
  );
  const displayHeading = $derived(
    heading === 'In your library' || !copy.origin
      ? heading
      : `${heading} · from ${copy.origin.label}`
  );
</script>

<audio
  bind:this={audioEl}
  onloadedmetadata={opened}
  ontimeupdate={() => (elapsed = audioEl?.currentTime ?? 0)}
  onplay={() => (paused = false)}
  onpause={() => (paused = true)}
  onended={() => (paused = true)}
  preload="none"
></audio>

<div
  class="flex w-full min-h-[14rem] flex-col gap-3 rounded-card border p-4 {selected
    ? 'border-accent bg-surface-regular'
    : 'border-line-thin'}"
>
  <!-- The selectable element. Kept to the label row on purpose: the play
       button and the seek bar below are its siblings, not its children, so a
       radio never carries another focusable control and pressing either of
       them never selects the card. -->
  <div
    role="radio"
    aria-checked={selected}
    tabindex={selected ? 0 : -1}
    onclick={onselect}
    onkeydown={(event) => {
      if (event.key === ' ' || event.key === 'Enter') {
        event.preventDefault();
        onselect();
      }
    }}
    aria-label={heading}
    class="flex min-w-0 cursor-pointer items-center gap-2.5"
  >
    <span
      aria-hidden="true"
      class="flex size-5 shrink-0 items-center justify-center rounded-full border-2 {selected
        ? 'border-accent'
        : 'border-line-thick'}"
    >
      {#if selected}<span class="size-2.5 rounded-full bg-accent"></span>{/if}
    </span>
    <span class="truncate text-meta text-ink-3">
      {displayHeading}
    </span>
    {#if subheading}
      <span class="truncate text-meta text-ink-3">{subheading}</span>
    {/if}
  </div>

  <div class="flex items-center gap-2.5">
    <button
      type="button"
      class="flex size-11 shrink-0 items-center justify-center rounded-full border border-line-regular bg-surface-thick text-ink"
      onclick={togglePlay}
      aria-label={playing ? `Pause ${heading}` : `Play ${heading}`}
    >
      {#if playing}
        <Pause size={16} fill="currentColor" />
      {:else}
        <Play size={16} fill="currentColor" />
      {/if}
    </button>
    <div
      role="slider"
      tabindex="0"
      aria-label="Seek"
      aria-valuenow={Math.round(elapsed)}
      aria-valuemin={0}
      aria-valuemax={Math.round(duration)}
      class="min-w-0 flex-1 cursor-pointer"
      onclick={seek}
      onkeydown={(event) => {
        if (loadedSrc !== src || duration <= 0) return;
        const step = event.key === 'ArrowRight' ? 5 : event.key === 'ArrowLeft' ? -5 : 0;
        if (!step || !audioEl) return;
        event.preventDefault();
        audioEl.currentTime = Math.max(0, Math.min(duration, audioEl.currentTime + step));
        elapsed = audioEl.currentTime;
      }}
    >
      {#if waveformFailed || bars.length === 0}
        <svg width="100%" height="36" viewBox="0 0 200 36" preserveAspectRatio="none">
          <rect x="0" y="17" width={200 * playedRatio} height="2" class="fill-accent" />
          <rect
            x={200 * playedRatio}
            y="17"
            width={200 - 200 * playedRatio}
            height="2"
            class="fill-line-thick"
          />
        </svg>
      {:else}
        <svg width="100%" height="36" viewBox="0 0 200 36" preserveAspectRatio="none">
          {#each bars as value, index (index)}
            {@const height = barHeight(value)}
            {@const x = (index * 200) / bars.length + 1}
            {@const width = 200 / bars.length - 2}
            <rect
              {x}
              y={(36 - height) / 2}
              {width}
              {height}
              class={index / bars.length < playedRatio ? 'fill-accent' : 'fill-line-thick'}
            />
          {/each}
        </svg>
      {/if}
    </div>
  </div>

  <div class="grid grid-cols-[5rem_minmax(0,1fr)] gap-x-2.5 gap-y-1.5 text-quiet-meta">
    <span class="text-ink-3">Length</span>
    <span class="flex items-center gap-1.5">
      {#if lengthVerdict === 'bad'}
        <X aria-label="differs" size={11} strokeWidth={2.5} class="shrink-0 text-fail" />
        <span class="numeric text-body text-fail">{lengthText}</span>
      {:else}
        {#if lengthVerdict === 'ok'}<Check aria-label="agrees" size={11} strokeWidth={2.5} class="shrink-0 text-ok" />{/if}
        <span class="numeric text-body text-ink">{lengthText}</span>
      {/if}
    </span>

    <span class="text-ink-3">Format</span>
    <span class="truncate text-body text-ink">{formatText}</span>

    <span class="text-ink-3">Size</span>
    <span class="numeric">{sizeText}</span>

    {#if creditRow}
      <span class="text-ink-3">Credit</span>
      <span aria-invalid="true" class="truncate text-fail">{creditRow}</span>
    {/if}
    {#if titleRow}
      <span class="text-ink-3">Title</span>
      <span aria-invalid="true" class="truncate text-fail">{titleRow}</span>
    {/if}

    <span class="text-ink-3">Album</span>
    {#if !album}
      <span class="flex items-center gap-1.5 text-ink-2">
        <span aria-hidden="true" class="h-px w-2.5 bg-ink-4"></span>
        none
      </span>
    {:else if albumVerdict === 'ok'}
      <span class="flex items-center gap-1.5">
        <Check aria-label="agrees" size={11} strokeWidth={2.5} class="shrink-0 text-ok" />
        <span class="truncate">{album}</span>
      </span>
    {:else if albumVerdict === 'bad'}
      <span class="flex items-center gap-1.5">
        <X aria-label="differs" size={11} strokeWidth={2.5} class="shrink-0 text-fail" />
        <span class="truncate text-fail">{album}</span>
      </span>
    {:else}
      <span class="truncate">{album}</span>
    {/if}
  </div>
</div>

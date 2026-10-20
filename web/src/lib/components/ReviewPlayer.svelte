<script lang="ts">
  import Icon from './Icon.svelte';
  import { onDestroy, untrack } from 'svelte';
  import { api } from '$lib/api';
  import { clock, listenFrom } from '$lib/review';
  import { listening, setVolume } from '$lib/preview.svelte';

  // The one player of Review, across the top of the comparison. Choosing
  // another copy while one plays carries on at the same second, so two copies
  // are compared on the same bar of music rather than from their openings.
  //
  // The meter reads the file itself, before the volume, so a quiet rip reads
  // quiet however loud the room is set. A browser without Web Audio still
  // plays, with the meter at rest.

  let {
    src,
    waveformOf,
    label,
    name,
    autoplay = false,
    onstart
  }: {
    src: string;
    /** The review copy whose waveform to draw. A library file has none. */
    waveformOf?: string;
    /** Which copy is loaded, as the column heads name it. */
    label: string;
    /** What is playing, for the reader who cannot see the button. */
    name: string;
    /** Set once the reader has played something on this page: the next
     * question's copy then starts by itself, a third of the way in. */
    autoplay?: boolean;
    /** Called each time playing starts, from a press or by itself. */
    onstart?: () => void;
  } = $props();

  let audioEl = $state<HTMLAudioElement | null>(null);
  let paused = $state(true);
  let elapsed = $state(0);
  let duration = $state(0);
  let peaks = $state<number[] | null>(null);
  // Where to start the source that is loading, and whether to play it.
  let resume = { at: -1, play: false };
  let loaded = '';

  let context: AudioContext | undefined;
  let analyser: AnalyserNode | undefined;
  let gain: GainNode | undefined;

  /** Built on the first press, because a browser only lets audio start after
   * one. Routing the element through Web Audio is what lets the meter read it. */
  function wire() {
    if (context || !audioEl) return;
    const Context =
      typeof window === 'undefined'
        ? undefined
        : (window.AudioContext ??
          (window as unknown as { webkitAudioContext?: typeof AudioContext }).webkitAudioContext);
    if (!Context) return;
    try {
      context = new Context();
      const source = context.createMediaElementSource(audioEl);
      analyser = context.createAnalyser();
      analyser.fftSize = 1024;
      gain = context.createGain();
      gain.gain.value = listening.volume;
      source.connect(analyser);
      analyser.connect(gain);
      gain.connect(context.destination);
      audioEl.volume = 1;
    } catch {
      context = undefined;
      analyser = undefined;
      gain = undefined;
    }
  }

  $effect(() => {
    const volume = listening.volume;
    if (gain) gain.gain.value = volume;
    else if (audioEl) audioEl.volume = volume;
  });

  // A new source keeps the second and the play state of the old one. The
  // player is drawn again for each question, so its first source starts
  // only on a press, or by itself a third of the way in under `autoplay`.
  $effect(() => {
    const next = src;
    untrack(() => {
      if (!audioEl || loaded === next) return;
      if (loaded) {
        const wasPlaying = !audioEl.paused;
        resume = { at: elapsed > 0 ? elapsed : -1, play: wasPlaying };
        if (wasPlaying) listening.sounding = next;
      } else {
        resume = autoplay ? { at: -2, play: true } : { at: -1, play: false };
      }
      loaded = next;
      elapsed = Math.max(resume.at, 0);
      duration = 0;
      audioEl.src = next;
      audioEl.load();
    });
  });

  $effect(() => {
    const id = waveformOf;
    peaks = null;
    if (!id) return;
    let live = true;
    void api
      .reviewCopyWaveform(id)
      .then((result) => {
        if (live) peaks = result.peaks;
      })
      .catch(() => undefined);
    return () => {
      live = false;
    };
  });

  // Somebody else took the speakers: a preview elsewhere on the page.
  $effect(() => {
    if (listening.sounding && listening.sounding !== src) audioEl?.pause();
  });

  function opened() {
    if (!audioEl) return;
    duration = Number.isFinite(audioEl.duration) ? audioEl.duration : 0;
    if (resume.at === -2) audioEl.currentTime = listenFrom(duration);
    else if (resume.at > 0) audioEl.currentTime = Math.min(resume.at, Math.max(duration - 1, 0));
    if (resume.play) begin();
    resume = { at: -1, play: false };
  }

  function begin() {
    if (!audioEl) return;
    wire();
    void context?.resume().catch(() => undefined);
    listening.sounding = src;
    onstart?.();
    void audioEl.play().catch(() => undefined);
  }

  /** Space and the play buttons. */
  export function toggle() {
    if (!audioEl) return;
    if (audioEl.paused) begin();
    else audioEl.pause();
  }

  export function play() {
    if (audioEl?.paused) begin();
  }

  function seek(ratio: number) {
    if (!audioEl || duration <= 0) return;
    audioEl.currentTime = Math.max(0, Math.min(1, ratio)) * duration;
    elapsed = audioEl.currentTime;
  }

  function onSeekClick(event: MouseEvent) {
    const box = (event.currentTarget as HTMLElement).getBoundingClientRect();
    if (box.width > 0) seek((event.clientX - box.left) / box.width);
  }

  function onSeekKey(event: KeyboardEvent) {
    if (!audioEl || duration <= 0) return;
    const step = event.key === 'ArrowRight' ? 5 : event.key === 'ArrowLeft' ? -5 : 0;
    if (!step) return;
    event.preventDefault();
    event.stopPropagation();
    seek((audioEl.currentTime + step) / duration);
  }

  const BARS = 96;
  const bars = $derived.by(() => {
    if (!peaks || peaks.length === 0) return [];
    const per = peaks.length / BARS;
    return Array.from({ length: BARS }, (_, index) => {
      const from = Math.floor(index * per);
      const to = Math.max(from + 1, Math.floor((index + 1) * per));
      const slice = peaks!.slice(from, to);
      return slice.reduce((sum, value) => sum + value, 0) / slice.length / 255;
    });
  });
  const played = $derived(duration > 0 ? elapsed / duration : 0);

  // The meter: sixteen steps from -48 dB to 0 dB of the file's own peak, with
  // the top step held briefly so a short peak can be read.
  const STEPS = 16;
  let lit = $state(0);
  let held = $state(0);
  let heldAt = 0;
  let frame = 0;
  const buffer = new Float32Array(1024);
  function measure(now: number) {
    frame = 0;
    if (!analyser || paused) {
      lit = 0;
      held = 0;
      return;
    }
    analyser.getFloatTimeDomainData(buffer);
    let peak = 0;
    for (const sample of buffer) peak = Math.max(peak, Math.abs(sample));
    const db = peak > 0 ? 20 * Math.log10(peak) : -Infinity;
    lit = Math.max(0, Math.min(STEPS, Math.round(((db + 48) / 48) * STEPS)));
    if (lit >= held || now - heldAt > 900) {
      held = lit;
      heldAt = now;
    }
    frame = requestAnimationFrame(measure);
  }
  $effect(() => {
    if (!paused && analyser && typeof requestAnimationFrame !== 'undefined' && !frame) {
      frame = requestAnimationFrame(measure);
    }
  });

  onDestroy(() => {
    if (frame) cancelAnimationFrame(frame);
    audioEl?.pause();
    if (listening.sounding === src) listening.sounding = '';
    void context?.close().catch(() => undefined);
  });
</script>

<audio
  bind:this={audioEl}
  onloadedmetadata={opened}
  ontimeupdate={() => (elapsed = audioEl?.currentTime ?? 0)}
  onplay={() => (paused = false)}
  onpause={() => (paused = true)}
  onended={() => (paused = true)}
  preload="metadata"
></audio>

<div
  class="grid grid-cols-[auto_minmax(0,1fr)] items-center gap-x-4 gap-y-2 border border-rule bg-inset px-3 py-2.5 lg:grid-cols-[auto_minmax(0,1fr)_auto]"
>
  <button
    type="button"
    onclick={(event) => {
      toggle();
      (event.currentTarget as HTMLElement).blur();
    }}
    aria-label={paused ? `Play ${name}` : `Pause ${name}`}
    title={paused ? 'Play (Space)' : 'Pause (Space)'}
    class="inverted press flex size-[2.125rem] shrink-0 items-center justify-center transition"
  >
    <Icon name={paused ? 'play' : 'pause'} size="md" />
  </button>

  <div class="flex min-w-0 flex-col gap-1.5">
    <p class="flex min-w-0 items-center gap-2 text-meta text-ink-3">
      <span class="truncate text-ink-2">{label}</span>
      <span class="mono ml-auto shrink-0">{clock(elapsed)} / {clock(duration)}</span>
    </p>
    <div
      role="slider"
      tabindex="0"
      aria-label="Seek"
      aria-valuemin={0}
      aria-valuemax={Math.round(duration)}
      aria-valuenow={Math.round(elapsed)}
      aria-valuetext={clock(elapsed)}
      class="cursor-pointer"
      onclick={onSeekClick}
      onkeydown={onSeekKey}
    >
      <svg width="100%" height="28" viewBox="0 0 400 44" preserveAspectRatio="none" aria-hidden="true">
        {#if bars.length === 0}
          <rect x="0" y="21" width={400 * played} height="2" class="fill-ink" />
          <rect x={400 * played} y="21" width={400 - 400 * played} height="2" class="fill-rule-strong" />
        {:else}
          {#each bars as value, index (index)}
            {@const height = 3 + value * 40}
            <rect
              x={(index * 400) / BARS + 0.6}
              y={(44 - height) / 2}
              width={400 / BARS - 1.4}
              {height}
              class={index / BARS < played ? 'fill-ink' : 'fill-rule-strong'}
            />
          {/each}
        {/if}
      </svg>
    </div>
  </div>

  <div
    class="col-span-2 flex items-center gap-4 lg:col-span-1 lg:border-l lg:border-rule lg:pl-4"
  >
    <div
      class="flex h-8 items-end gap-0.5"
      role="meter"
      aria-label="Level"
      aria-valuemin={0}
      aria-valuemax={STEPS}
      aria-valuenow={lit}
      title="Level of the file itself, before the volume"
    >
      {#each Array.from({ length: STEPS }, (_, step) => step) as index (index)}
        <span
          class="w-1 {index < lit || index === held - 1
            ? index >= STEPS - 2
              ? 'bg-fail'
              : index >= STEPS - 5
                ? 'bg-decide'
                : 'bg-ok'
            : 'bg-rule-strong'}"
          style="height: {30 + (70 * index) / (STEPS - 1)}%"
        ></span>
      {/each}
    </div>
    <label class="flex flex-1 items-center gap-2 text-ink-3 lg:flex-none" title="Volume">
      <Icon name="volume" size={16} />
      <input
        type="range"
        min="0"
        max="100"
        value={Math.round(listening.volume * 100)}
        oninput={(event) => setVolume(Number(event.currentTarget.value) / 100)}
        aria-label="Volume"
        class="w-full cursor-pointer lg:w-28"
      />
    </label>
  </div>
</div>

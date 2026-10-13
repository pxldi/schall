<script lang="ts">
  import Icon from './Icon.svelte';
  import { onDestroy, untrack } from 'svelte';
  import { clock, listenFrom } from '$lib/review';
  import { listening } from '$lib/preview.svelte';

  // The one player of listening mode. A new source starts a third of the way
  // in, on its own once the reader has answered or stepped, and the next
  // question's copy loads behind it so stepping on does not wait for the
  // network.
  //
  // The want's sample is not played first. The API keeps the sample's
  // fingerprint and alignment, never its audio, so there is nothing to play.

  let {
    src,
    next = '',
    name,
    autoplay
  }: {
    src: string;
    /** The copy the next question would play, loaded and not played. */
    next?: string;
    /** What is playing, for the reader who cannot see the button. */
    name: string;
    /** Off for the copy the mode opens on: nobody asked for sound yet. On once
     * the reader has answered or stepped, so the next copy plays by itself. */
    autoplay: boolean;
  } = $props();

  let audioEl = $state<HTMLAudioElement | null>(null);
  let paused = $state(true);
  let elapsed = $state(0);
  let duration = $state(0);
  let start = $state(0);

  $effect(() => {
    const source = src;
    if (!audioEl) return;
    elapsed = 0;
    duration = 0;
    audioEl.src = source;
    // Untracked: the effect below follows the volume. Tracking it here would
    // load the copy again, from the start, every time the slider moved.
    audioEl.volume = untrack(() => listening.volume);
    audioEl.load();
  });

  $effect(() => {
    if (audioEl) audioEl.volume = listening.volume;
  });

  // Somebody else took the speakers.
  $effect(() => {
    if (listening.sounding !== src) audioEl?.pause();
  });

  onDestroy(() => {
    audioEl?.pause();
    if (listening.sounding === src) listening.sounding = '';
  });

  function opened() {
    if (!audioEl) return;
    duration = Number.isFinite(audioEl.duration) ? audioEl.duration : 0;
    start = listenFrom(duration);
    audioEl.currentTime = start;
    if (autoplay) begin();
  }

  function begin() {
    if (!audioEl) return;
    listening.sounding = src;
    void audioEl.play().catch(() => undefined);
  }

  /** Space: plays a paused copy and pauses a playing one, like the button. */
  export function toggle() {
    if (!audioEl) return;
    if (audioEl.paused) begin();
    else audioEl.pause();
  }
</script>

<audio
  bind:this={audioEl}
  onloadedmetadata={opened}
  ontimeupdate={() => (elapsed = audioEl?.currentTime ?? 0)}
  onplay={() => (paused = false)}
  onpause={() => (paused = true)}
  onended={() => (paused = true)}
  preload="auto"
></audio>
{#if next && next !== src}
  <audio src={next} preload="auto" muted></audio>
{/if}

<div class="flex items-center gap-3">
  <button
    type="button"
    onclick={(event) => {
      toggle();
      (event.currentTarget as HTMLElement).blur();
    }}
    aria-label={paused ? `Play ${name}` : `Pause ${name}`}
    title={paused ? 'Play (Space)' : 'Pause (Space)'}
    class="flex size-12 shrink-0 items-center justify-center rounded-full border border-line-regular bg-surface-thick text-ink"
  >
    <Icon name={paused ? 'play' : 'pause'} size={18} />
  </button>
  <div
    role="progressbar"
    aria-label="Position"
    aria-valuemin={0}
    aria-valuemax={Math.round(duration)}
    aria-valuenow={Math.round(elapsed)}
    aria-valuetext={clock(elapsed)}
    class="flex h-6 min-w-0 flex-1 items-center"
  >
    <div class="relative h-1 w-full overflow-hidden rounded-full bg-line-thick">
      <div
        class="absolute inset-y-0 left-0 rounded-full bg-accent"
        style="width: {duration > 0 ? (elapsed / duration) * 100 : 0}%"
      ></div>
    </div>
  </div>
  <span class="numeric shrink-0 text-meta text-ink-3">
    <span class="text-ink-2">{clock(elapsed)}</span> / {clock(duration)}
  </span>
</div>

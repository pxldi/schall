<script lang="ts">
  import { Pause, Play, Volume2 } from '@lucide/svelte';
  import { clock } from '$lib/review';
  import { listening, setVolume as persistVolume } from '$lib/preview.svelte';

  let {
    src,
    name = 'this file',
    volume: showVolume = true
  }: {
    /** Where the audio comes from. Set as an <audio> src rather than fetched,
     * so the browser issues range requests and the scrubber works. */
    src: string;
    /** What is being played, for the reader who cannot see the button. */
    name?: string;
    /** The duplicate question stands two players side by side and one volume
     * serves both, so the second one draws no slider. */
    volume?: boolean;
  } = $props();

  let audio = $state<HTMLAudioElement | null>(null);
  let started = $state(false);
  let paused = $state(true);
  let elapsed = $state(0);
  let duration = $state(0);
  let failed = $state('');

  // Somebody else took the speakers. Stopping here rather than in the other
  // player keeps the rule in one place: whoever starts sets the name, and every
  // player that is not it falls silent.
  $effect(() => {
    if (started && listening.sounding !== src) stop();
  });

  function stop() {
    audio?.pause();
    started = false;
    paused = true;
    elapsed = 0;
    duration = 0;
  }

  /** Playback opens in the middle. Intros mislead — a quiet count-in sounds the
   * same on a good copy and a bad one — and the middle is where a file says what
   * it is. The scrubber stays live, so it is a starting point and not a window. */
  function toggle() {
    if (!audio) return;
    if (started) {
      if (audio.paused) {
        listening.sounding = src;
        void audio.play();
      } else {
        audio.pause();
      }
      return;
    }
    failed = '';
    started = true;
    elapsed = 0;
    duration = 0;
    listening.sounding = src;
    audio.src = src;
    audio.volume = listening.volume;
    audio.load();
  }

  function opened() {
    if (!audio) return;
    duration = Number.isFinite(audio.duration) ? audio.duration : 0;
    if (duration > 0) audio.currentTime = duration / 2;
    void audio.play().catch(() => undefined);
  }

  function seek(event: MouseEvent) {
    if (!audio || !started || duration <= 0) return;
    const box = (event.currentTarget as HTMLElement).getBoundingClientRect();
    const ratio = Math.max(0, Math.min(1, (event.clientX - box.left) / box.width));
    audio.currentTime = ratio * duration;
    elapsed = audio.currentTime;
  }

  function seekByKey(event: KeyboardEvent) {
    if (!audio || !started || duration <= 0) return;

    let next = audio.currentTime;
    switch (event.key) {
      case 'ArrowLeft':
        next -= 5;
        break;
      case 'ArrowRight':
        next += 5;
        break;
      case 'Home':
        next = 0;
        break;
      case 'End':
        next = duration;
        break;
      default:
        return;
    }

    event.preventDefault();
    event.stopPropagation();
    audio.currentTime = Math.max(0, Math.min(duration, next));
    elapsed = audio.currentTime;
  }

  function setVolume(event: Event) {
    persistVolume(Number((event.currentTarget as HTMLInputElement).value) / 100);
    if (audio) audio.volume = listening.volume;
  }

  // What went wrong, in the two words a person can act on. The route answers
  // with a sentence when it knows one, but an <audio> element only ever reports
  // that it could not play, so this says the same thing every time and the
  // reason lives in the server's log.
  function broke() {
    if (!started) return;
    stop();
    failed = 'Schall could not play this file.';
  }
</script>

<audio
  bind:this={audio}
  onloadedmetadata={opened}
  ontimeupdate={() => (elapsed = audio?.currentTime ?? 0)}
  onplay={() => (paused = false)}
  onpause={() => (paused = true)}
  onended={stop}
  onerror={broke}
  preload="none"
></audio>

<div class="flex flex-col gap-1.5">
  <div class="flex items-center gap-2.5 rounded-control bg-surface-thick px-2 py-1.5">
    <button
      type="button"
      class="flex size-7 shrink-0 items-center justify-center rounded-control bg-surface-thick text-ink transition hover:bg-accent hover:text-accent-ink"
      onclick={toggle}
      aria-label={started && !paused ? `Pause ${name}` : `Play ${name} from the middle`}
    >
      {#if started && !paused}
        <Pause size={12} fill="currentColor" />
      {:else}
        <Play size={12} fill="currentColor" />
      {/if}
    </button>
    <div class="flex min-w-0 flex-1 items-center gap-2.5">
      <div
        role="slider"
        tabindex="0"
        aria-label="Seek"
        aria-valuenow={started ? Math.round(elapsed) : 0}
        aria-valuemin={0}
        aria-valuemax={Math.round(duration)}
        aria-valuetext={started ? clock(elapsed) : 'Not playing'}
        class="relative flex h-3.5 flex-1 cursor-pointer items-center"
        onclick={seek}
        onkeydown={seekByKey}
      >
        <div class="h-1 w-full overflow-hidden rounded-full bg-surface-thick">
          <div
            class="h-full rounded-full bg-[#d6d3d1]"
            style="width: {started && duration > 0 ? (elapsed / duration) * 100 : 0}%"
          ></div>
        </div>
      </div>
      <span class="numeric shrink-0 text-micro font-medium text-ink-3">
        <span class="text-ink-2">{clock(started ? elapsed : 0)}</span>
        / {clock(started ? duration : 0)}
      </span>
    </div>
    {#if showVolume}
      <Volume2 size={13} strokeWidth={1.8} class="shrink-0 text-ink-3" />
      <input
        type="range"
        min="0"
        max="100"
        value={listening.volume * 100}
        class="w-16 shrink-0 cursor-pointer"
        aria-label="Volume"
        oninput={setVolume}
      />
    {/if}
  </div>
  {#if failed}
    <p class="text-meta text-fail">{failed}</p>
  {/if}
</div>

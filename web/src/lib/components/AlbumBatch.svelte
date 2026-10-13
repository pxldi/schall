<script lang="ts">
  import Icon from './Icon.svelte';
  import Button from './Button.svelte';
  import { onDestroy } from 'svelte';
  import { api } from '$lib/api';
  import { batchChoice, batchProof, clock, listenFrom, tierOf, type Question } from '$lib/review';
  import { listening } from '$lib/preview.svelte';

  // One album's wants, decided together. Each row is the copy the single view
  // would have preselected, so accepting here sends what pressing Accept on
  // each question in turn would have sent. A want that asks anything else is
  // left for the single view and only counted. A row starts checked only when
  // its evidence names a proof (batchProof); the rest wait for the reader.

  let {
    album,
    artist,
    questions,
    ondone
  }: {
    album: string;
    artist: string;
    questions: Question[];
    /** Called once the accepts have run, so the page can read the queue again. */
    ondone: () => void;
  } = $props();

  const rows = $derived(
    questions.flatMap((question) => {
      const copy = batchChoice(question);
      return copy ? [{ question, copy, proof: batchProof(copy) }] : [];
    })
  );
  const oneByOne = $derived(questions.length - rows.length);

  // What the reader changed from the starting marks, so a row that arrives
  // later starts from its own evidence.
  let flipped = $state(new Set<string>());
  const isChecked = (row: (typeof rows)[number]) =>
    (row.proof !== '') !== flipped.has(row.question.id);
  const checked = $derived(rows.filter(isChecked));

  function toggle(id: string) {
    const next = new Set(flipped);
    if (next.has(id)) next.delete(id);
    else next.add(id);
    flipped = next;
  }

  let running = $state(false);
  let result = $state('');

  /** One accept after another, so a failure stops nothing else and the server
   * sees the same requests the single view sends. */
  async function acceptChecked() {
    const chosen = checked.map((row) => row.copy.id);
    if (chosen.length === 0) return;
    running = true;
    result = '';
    let accepted = 0;
    let failure = '';
    for (const id of chosen) {
      try {
        await api.acceptReviewCopy(id);
        accepted++;
      } catch (error) {
        failure ||= error instanceof Error ? error.message : String(error);
      }
    }
    const failed = chosen.length - accepted;
    result = failed ? `Accepted ${accepted}. ${failed} failed: ${failure}` : `Accepted ${accepted}.`;
    running = false;
    ondone();
  }

  let audioEl = $state<HTMLAudioElement | null>(null);
  let playingId = $state('');
  let paused = $state(true);

  const srcOf = (copyId: string) => api.reviewCopyAudioUrl(copyId);

  $effect(() => {
    if (playingId && listening.sounding !== srcOf(playingId)) audioEl?.pause();
  });

  onDestroy(() => {
    audioEl?.pause();
    if (playingId && listening.sounding === srcOf(playingId)) listening.sounding = '';
  });

  function play(copyId: string) {
    if (!audioEl) return;
    const src = srcOf(copyId);
    listening.sounding = src;
    if (playingId === copyId) {
      if (audioEl.paused) void audioEl.play().catch(() => undefined);
      else audioEl.pause();
      return;
    }
    playingId = copyId;
    audioEl.src = src;
    audioEl.volume = listening.volume;
    audioEl.load();
  }

  function opened() {
    if (!audioEl) return;
    audioEl.currentTime = listenFrom(audioEl.duration);
    void audioEl.play().catch(() => undefined);
  }

  const sounding = (copyId: string) =>
    playingId === copyId && !paused && listening.sounding === srcOf(copyId);
</script>

<audio
  bind:this={audioEl}
  onloadedmetadata={opened}
  onplay={() => (paused = false)}
  onpause={() => (paused = true)}
  onended={() => (paused = true)}
  preload="none"
></audio>

<div class="flex flex-col gap-3">
  <header class="flex flex-col gap-1">
    <p class="numeric text-meta text-ink-3">{questions.length.toLocaleString()} in album</p>
    <h2 class="text-quiet-display font-bold text-ink">{album}</h2>
    <p class="text-quiet-meta font-medium text-ink">{artist}</p>
  </header>

  {#if rows.length > 0}
    <ul aria-label="Album copies" class="flex max-w-[64rem] flex-col rounded-card border border-line-thin">
      {#each rows as row (row.question.id)}
        {@const ms = row.copy.evidence?.observed?.durationMs}
        <li class="flex items-center gap-3 border-b border-line-thin px-3 py-2 last:border-b-0">
          <input
            type="checkbox"
            checked={isChecked(row)}
            onchange={() => toggle(row.question.id)}
            aria-label={`Include ${row.question.title}`}
            disabled={running}
            class="size-4 shrink-0 cursor-pointer"
          />
          <button
            type="button"
            onclick={() => play(row.copy.id)}
            aria-label={sounding(row.copy.id) ? `Pause ${row.question.title}` : `Play ${row.question.title}`}
            title={sounding(row.copy.id) ? 'Pause' : 'Play'}
            class="flex size-8 shrink-0 items-center justify-center rounded-full border border-line-regular bg-surface-thick text-ink"
          >
            <Icon name={sounding(row.copy.id) ? 'pause' : 'play'} size={13} />
          </button>
          <span class="min-w-0 flex-1 truncate text-body text-ink">{row.question.title}</span>
          {#if row.proof}
            <span class="shrink-0 text-meta text-ink-2">{row.proof} agrees</span>
          {:else if (row.copy.evidence?.differs ?? []).length > 0}
            <span
              class="min-w-0 max-w-[14rem] shrink truncate text-meta text-fail"
              title={row.copy.evidence?.differs.join(', ')}
            >
              {row.copy.evidence?.differs.join(', ')} differs
            </span>
          {:else}
            <span class="shrink-0 text-meta text-ink-3">{tierOf(row.copy)}</span>
          {/if}
          <span class="hidden min-w-0 max-w-[16rem] truncate text-meta text-ink-3 sm:block" title={row.copy.name}>
            {row.copy.name}
          </span>
          <span class="numeric shrink-0 text-meta text-ink-2">{ms ? clock(ms / 1000) : '—'}</span>
        </li>
      {/each}
    </ul>
  {/if}

  {#if oneByOne > 0}
    <p class="text-meta text-ink-3">{oneByOne} to decide one by one</p>
  {/if}

  <div class="flex flex-wrap items-center gap-3">
    <Button disabled={running || checked.length === 0} onclick={acceptChecked}>
      <Icon name="check" size={14} />
      Accept checked
      <span class="numeric">{checked.length}</span>
    </Button>
    {#if result}
      <p class="text-meta text-ink-2" role="status">{result}</p>
    {/if}
  </div>
</div>

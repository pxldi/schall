<script lang="ts">
  import Icon from './Icon.svelte';
  import EmptyPanel from '$lib/components/EmptyPanel.svelte';
  import Keycap from './Keycap.svelte';
  import type { AcquisitionCandidate } from '$lib/api';
  import { glide } from '$lib/motion.svelte';
  import { clock } from '$lib/review';
  import { STATE_GLYPH } from '$lib/states';

  // Which MusicBrainz recording a playlist entry means. One row per
  // candidate, read down the same columns: the key that picks it, the
  // recording, its release, how long it runs against the entry, and which
  // fields fit the entry, which is the grader's own agrees and differs read
  // back, never scored. The identifiers nobody compares by eye sit behind a
  // disclosure under each row.

  let {
    candidates,
    chosen,
    onchoose,
    entryDurationMs = null,
    identifiers = false
  }: {
    candidates: AcquisitionCandidate[];
    /** -1 when nothing is selected, which is what two candidates saying the
     * same thing leaves behind. Nothing here may guess between them. */
    chosen: number;
    onchoose: (index: number) => void;
    /** How long the entry runs, so each length can print its difference. */
    entryDurationMs?: number | null;
    /** Opens every row's identifiers (the I key). */
    identifiers?: boolean;
  } = $props();

  const COLUMNS = 'grid-cols-[2rem_minmax(0,2fr)_minmax(0,1.5fr)_6.5rem_minmax(0,2fr)]';

  /** A verdict entry ("duration (3 seconds out)") as a short word in
   * sentence case ("Length"). The measurement is printed in the length
   * column. */
  function word(entry: string) {
    const opened = entry.indexOf(' (');
    const field = (opened < 0 ? entry : entry.slice(0, opened)).trim();
    if (field === 'duration' || field === 'length') return 'Length';
    if (field.toLowerCase() === 'isrc') return 'ISRC';
    return field.charAt(0).toUpperCase() + field.slice(1);
  }

  /** The difference from the entry's length, signed, in whole seconds. */
  function difference(durationMs: number | null | undefined) {
    if (!durationMs || !entryDurationMs) return '';
    const seconds = Math.round((durationMs - entryDurationMs) / 1000);
    if (seconds === 0) return '±0s';
    return `${seconds > 0 ? '+' : '−'}${Math.abs(seconds)}s`;
  }

  const lengthDiffers = (candidate: AcquisitionCandidate) =>
    candidate.differs.some((entry) => /^(duration|length)/.test(entry));
</script>

{#if candidates.length === 0}
  <!-- A header row of columns with nothing under it read as a list that failed
       to draw. -->
  <EmptyPanel role="idle" level={3} heading="No versions to choose">
    <p class="text-body text-ink-2">MusicBrainz has no match for this song.</p>
  </EmptyPanel>
{:else}
  <div class="overflow-x-auto">
    <div class="min-w-[40rem] sm:min-w-0" role="radiogroup" aria-label="Versions this could be">
      <div class={`grid ${COLUMNS} h-7 items-center gap-x-4 border-b border-rule-strong px-2.5`}>
        <span class="label">Key</span>
        <span class="label">Recording</span>
        <span class="label">Release</span>
        <span class="label text-right">Length</span>
        <span class="label">Agrees</span>
      </div>

      <div class="relative" use:glide={{ key: chosen }}>
        {#each candidates as candidate, index (candidate.recordingId)}
          {@const selected = index === chosen}
          <div class="rise border-b border-rule" style="animation-delay: calc({Math.min(index, 2)} * var(--motion-stagger))">
            <div
              role="radio"
              aria-checked={selected}
              tabindex="0"
              data-candidate={index}
              data-cursor={selected}
              onclick={() => onchoose(index)}
              onkeydown={(event) => {
                if (event.key === ' ') {
                  event.preventDefault();
                  onchoose(index);
                }
              }}
              class={`row grid ${COLUMNS} min-h-9 cursor-pointer items-center gap-x-4 border-b-0 px-2.5 ${selected ? 'on-ink' : ''}`}
            >
              <Keycap key={String(index + 1)} class="w-fit" />
              <span class="min-w-0 truncate text-body">
                <span class="text-ink">{candidate.trackTitle}</span>
                <span class="ml-1.5 text-meta text-ink-3">{candidate.artistName}</span>
              </span>
              <span class="min-w-0 truncate text-body text-ink-2">{candidate.releaseTitle || '—'}</span>
              <span class="mono flex items-center justify-end gap-1.5 text-meta text-ink-2">
                {candidate.durationMs ? clock(candidate.durationMs / 1000) : '—'}
                {#if difference(candidate.durationMs)}
                  <span class={lengthDiffers(candidate) ? 'text-fail' : 'text-ink-4'}>
                    {difference(candidate.durationMs)}
                  </span>
                {/if}
              </span>
              <span class="flex min-w-0 gap-x-3 overflow-hidden whitespace-nowrap text-meta">
                {#each candidate.agrees as entry (entry)}
                  <span class="flex items-center gap-1 whitespace-nowrap text-ink-2">
                    <Icon name={STATE_GLYPH.ok} size={12} class="text-ok" aria-label="agrees" />
                    {word(entry)}
                  </span>
                {/each}
                {#each candidate.differs as entry (entry)}
                  <span class="flex items-center gap-1 whitespace-nowrap text-fail" title={entry}>
                    <Icon name={STATE_GLYPH.fail} size={12} aria-label="differs" />
                    {word(entry)}
                  </span>
                {/each}
              </span>
            </div>

            <details class="relative px-2.5 pb-2 pl-[3.25rem]" open={identifiers || undefined}>
              <summary class="w-fit cursor-pointer text-meta text-ink-4 transition hover:text-ink">Identifiers</summary>
              <dl class="reveal mt-1.5 grid grid-cols-[minmax(0,8rem)_minmax(0,1fr)] gap-x-4 gap-y-1 text-meta text-ink-2">
                <dt>Recording ID</dt>
                <dd class="mono truncate">{candidate.recordingId}</dd>
                {#if candidate.releaseGroupId}
                  <dt>Release group ID</dt>
                  <dd class="mono truncate">{candidate.releaseGroupId}</dd>
                {/if}
                <dt>ISRC</dt>
                <dd class="mono truncate">{candidate.isrc || '—'}</dd>
              </dl>
            </details>
          </div>
        {/each}
      </div>
    </div>
  </div>
{/if}

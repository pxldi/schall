<script lang="ts">
  import { Check, X } from '@lucide/svelte';
  import type { AcquisitionCandidate } from '$lib/api';
  import { clock } from '$lib/review';

  // Which MusicBrainz recording a playlist entry means. One row-card per
  // candidate, read down the same columns every row answers: the recording,
  // its release, how long it runs, and which fields fit the entry — the
  // grader's own agrees/differs, read back rather than scored. The
  // identifiers nobody compares by eye (ISRC, the MusicBrainz IDs) sit behind
  // a disclosure under the row instead of taking a column of their own.

  let {
    candidates,
    chosen,
    onchoose
  }: {
    candidates: AcquisitionCandidate[];
    /** -1 when nothing is selected, which is what two candidates saying the
     * same thing leaves behind — nothing here may guess between them. */
    chosen: number;
    onchoose: (index: number) => void;
  } = $props();

  const COLUMNS =
    'grid-cols-[1.75rem_1.5rem_minmax(0,2fr)_minmax(0,1.6fr)_4.5rem_minmax(0,1.6fr)]';

  /** A verdict entry ("duration (3 seconds out)") as a short chip label
   * ("duration"). The measurement in brackets is the grader's, not a fact for
   * this chip to restate. */
  function chipLabel(entry: string) {
    const opened = entry.indexOf(' (');
    return opened < 0 ? entry : entry.slice(0, opened);
  }
</script>

<div class="overflow-x-auto">
  <div class="flex min-w-[40rem] flex-col gap-2" role="radiogroup" aria-label="Recordings this could be">
    <div class={`grid ${COLUMNS} gap-x-4 px-[18px] text-dense-micro uppercase text-ink-3`}>
      <span></span>
      <span></span>
      <span>Recording</span>
      <span>Release</span>
      <span>Length</span>
      <span>Fits the entry</span>
    </div>

    {#each candidates as candidate, index (candidate.recordingId)}
      {@const selected = index === chosen}
      <div
        class={`rounded-card border ${selected ? 'border-accent bg-surface-regular' : 'border-line-thin'}`}
      >
        <div
          role="radio"
          aria-checked={selected}
          tabindex="0"
          data-candidate={index}
          onclick={() => onchoose(index)}
          onkeydown={(event) => {
            if (event.key === ' ') {
              event.preventDefault();
              onchoose(index);
            }
          }}
          class={`grid ${COLUMNS} cursor-pointer items-center gap-x-4 px-[18px] py-3.5`}
        >
          <span
            aria-hidden="true"
            class={`flex size-5 items-center justify-center rounded-full border-2 ${
              selected ? 'border-accent' : 'border-line-thick'
            }`}
          >
            {#if selected}<span class="size-2.5 rounded-full bg-accent"></span>{/if}
          </span>
          <span class="numeric text-meta">{index + 1}</span>
          <span class="min-w-0">
            <span class="block truncate text-quiet-meta font-medium text-ink">{candidate.trackTitle}</span>
            <span class="block truncate text-meta text-ink-3">{candidate.artistName}</span>
          </span>
          <span class="min-w-0">
            <span class="block truncate text-quiet-meta text-ink-2">{candidate.releaseTitle || '—'}</span>
          </span>
          <span class="numeric text-quiet-meta">{candidate.durationMs ? clock(candidate.durationMs / 1000) : '—'}</span>
          <span class="flex flex-wrap gap-1.5">
            {#each candidate.agrees as entry (entry)}
              <span class="flex items-center gap-1 rounded-full border border-line-thin px-2 py-0.5 text-meta text-ink-2">
                <Check size={10} strokeWidth={2.5} class="text-ok" />
                {chipLabel(entry)}
              </span>
            {/each}
            {#each candidate.differs as entry (entry)}
              <span class="flex items-center gap-1 rounded-full border border-fail-border px-2 py-0.5 text-meta text-ink-2">
                <X size={10} strokeWidth={2.5} class="text-fail" />
                {chipLabel(entry)}
              </span>
            {/each}
          </span>
        </div>

        <details class="border-t border-line-thin px-4 py-2">
          <summary class="cursor-pointer text-meta text-ink-3">Identifiers</summary>
          <dl class="mt-1.5 grid grid-cols-[minmax(0,8rem)_minmax(0,1fr)] gap-x-4 gap-y-1 text-meta text-ink-2">
            <dt>Recording ID</dt>
            <dd class="numeric truncate">{candidate.recordingId}</dd>
            {#if candidate.releaseGroupId}
              <dt>Release group ID</dt>
              <dd class="numeric truncate">{candidate.releaseGroupId}</dd>
            {/if}
            <dt>ISRC</dt>
            <dd class="numeric truncate">{candidate.isrc || '—'}</dd>
          </dl>
        </details>
      </div>
    {/each}
  </div>
</div>

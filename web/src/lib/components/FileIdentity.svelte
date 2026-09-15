<script lang="ts">
  import { LoaderCircle } from '@lucide/svelte';
  import { api, type FileResolution } from '$lib/api';
  import { CANDIDATE_COLUMNS, candidateRow, clock } from '$lib/review';
  import AudioPreview from '$lib/components/AudioPreview.svelte';
  import ErrorNote from '$lib/components/ErrorNote.svelte';
  import ReviewEvidence from '$lib/components/ReviewEvidence.svelte';

  let {
    resolution,
    fileId = '',
    loading = false,
    error,
    chosen,
    onchoose,
    expectedMs
  }: {
    resolution: FileResolution | null;
    /** The file being asked about, so it can be played. Empty where the panel
     * is drawn without one, and then there is simply no player. */
    fileId?: string;
    loading?: boolean;
    /** Whatever failed: the read, the answer, or asking again. The panel hands
     * it on rather than flattening it to a sentence, because the words a reader
     * gets are chosen from the status and the server's own title. */
    error?: unknown;
    /** The answer lives in the page's footer with every other kind's, so the
     * position is the page's to hold. This panel only draws the candidates. */
    chosen: number;
    onchoose: (index: number) => void;
    /** How long the file being identified runs, where the scanner read one. A
     * candidate whose length the grader called a disagreement then reads as both
     * times at once rather than as one time and a subtraction. */
    expectedMs?: number | null;
  } = $props();

  const identity = $derived(resolution?.identity ?? null);
  const candidates = $derived(resolution?.candidates ?? []);

  // Not `candidates.map(candidateRow)`. `map` hands the callback the index as
  // its second argument, and the second argument here is a duration in
  // milliseconds.
  const rows = $derived(candidates.map((candidate) => candidateRow(candidate, expectedMs)));
</script>

<div class="flex flex-col gap-3">
  {#if loading}
    <div class="flex min-h-[520px] items-start gap-2 text-body text-ink-3">
      <LoaderCircle size={14} class="animate-spin" /> Loading…
    </div>
  {:else if error}
    <!-- The read, the answer and asking again all fail the same way here: the
         file is still unidentified. -->
    <ErrorNote {error} />
  {:else if resolution}
    <!-- The audio, above the candidates it decides between. Two recordings can
         agree on title, artist and length and differ only in an identifier no
         reader recognises; the one thing that tells them apart is the sound. -->
    {#if fileId}
      <AudioPreview src={api.libraryFileAudioUrl(fileId)} name="this file" />
    {/if}
    {#if resolution.summary}
      <p class="text-body leading-relaxed text-ink-2">{resolution.summary}</p>
    {/if}
    {#if resolution.error}
      <p class="text-body text-decide">{resolution.error}</p>
    {/if}

    {#if identity}
      <div class="rounded-panel bg-ok/14 px-4 py-3">
        {#if identity.kind === 'external'}
          <p class="text-body font-semibold text-ink">{identity.trackTitle}</p>
          <p class="mt-1 text-meta text-ink-3">
            {[identity.artistName, identity.releaseTitle, clock((identity.durationMs ?? 0) / 1000)]
              .filter(Boolean)
              .join(' · ')}
          </p>
        {:else}
          <p class="text-body font-semibold text-ink">Owned music with no external identity</p>
        {/if}
        <p class="mt-2 text-meta text-ink-3">
          <!-- The server writes the sentence. The method beside it is a name
               Schall uses to itself, and printing it told the reader nothing. -->
          {identity.manual ? 'Decided by hand' : identity.methodText}{identity.evidence
            .length
            ? ` · ${identity.evidence.join(', ')} agree`
            : ''}
        </p>
      </div>
    {/if}

    {#if candidates.length > 0}
      <ReviewEvidence
        columns={CANDIDATE_COLUMNS}
        {rows}
        {chosen}
        {onchoose}
        label="Recordings this file could be"
      >
        {#snippet detail(_row, index)}
          {#if candidates[index]?.summary}
            <p class="text-dense-meta text-ink-3">{candidates[index].summary}</p>
          {/if}
        {/snippet}
      </ReviewEvidence>
    {/if}
  {/if}
</div>

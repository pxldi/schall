<script lang="ts">
  import { createMutation, useQueryClient } from '@tanstack/svelte-query';
  import { LoaderCircle, RefreshCw, Search, Unlink } from '@lucide/svelte';
  import { api, type LibraryFile, type MatchCandidate } from '$lib/api';
  import Button from '$lib/components/Button.svelte';
  import ErrorNote from '$lib/components/ErrorNote.svelte';

  let { file, ondecided }: { file: LibraryFile; ondecided: () => void } = $props();

  const client = useQueryClient();

  let candidates = $state<MatchCandidate[]>([]);
  let loading = $state(false);
  // The thrown error itself, not its sentence: `ErrorNote` is what turns a
  // failure into words, and it needs the whole error to do it. `null` is the
  // panel with nothing wrong.
  let failure = $state<unknown>(null);
  let search = $state('');
  // Reassigning takes a track away from the file that holds it, so it is asked
  // about in the page rather than carried out on the click that suggested it.
  let confirming = $state<MatchCandidate | null>(null);

  // The candidates are read per file rather than with the queue: a page of
  // questions would otherwise fetch every file's answers to show one of them.
  //
  // What the file has become is a dependency as well as which file it is. A
  // match decided here changes the file rather than replacing it, and reading
  // only the id would leave the answered question on screen listing the
  // candidates it had before it was answered.
  $effect(() => {
    void file.matchStatus;
    void file.mappingManual;
    void load(file.id);
  });

  async function load(fileId: string) {
    candidates = [];
    confirming = null;
    search = '';
    failure = null;
    loading = true;
    try {
      candidates = (await api.matchCandidates(fileId)).items;
    } catch (error) {
      failure = error;
    } finally {
      loading = false;
    }
  }

  async function searchCatalogue(event: SubmitEvent) {
    event.preventDefault();
    const query = search.trim();
    if (query.length < 2) return;
    failure = null;
    loading = true;
    try {
      candidates = (await api.searchTracks(file.id, query)).items;
    } catch (error) {
      failure = error;
    } finally {
      loading = false;
    }
  }

  function settled() {
    confirming = null;
    void client.invalidateQueries({ queryKey: ['library'] });
    void client.invalidateQueries({ queryKey: ['library-files'] });
    void client.invalidateQueries({ queryKey: ['releases'] });
    ondecided();
  }

  const match = createMutation({
    mutationFn: ({ trackId, reassign }: { trackId: string; reassign: boolean }) =>
      api.setManualMatch(file.id, trackId, reassign),
    onSuccess: settled
  });
  const clear = createMutation({
    mutationFn: () => api.clearManualMatch(file.id),
    onSuccess: settled
  });
  // Re-running the automatic pass is not an answer, it is asking again. A file
  // whose catalogue has grown since it was last judged can be proven now, and
  // then this question is gone rather than answered by hand.
  const reconcile = createMutation({
    mutationFn: () => api.reconcileLibraryFile(file.id),
    onSuccess: settled
  });

  const pending = $derived($match.isPending || $clear.isPending || $reconcile.isPending);
  // The one failure this panel is showing, whichever of the four produced it.
  // `null` rather than the empty string, because an empty string is still a
  // failure as far as `ErrorNote` is concerned and would draw a note with
  // nothing wrong behind it.
  const problem = $derived(
    failure ?? $match.error ?? $clear.error ?? $reconcile.error ?? null
  );

  function choose(candidate: MatchCandidate) {
    if (candidate.manuallyMapped) {
      confirming = candidate;
      return;
    }
    $match.mutate({ trackId: candidate.trackId, reassign: false });
  }

  // Every track this file could sit in already has another file matched to it.
  // That is not really the question the heading asks — there is nothing to pick
  // between, and the one candidate cannot be taken without taking it from
  // somewhere — so the panel says what the situation is instead of letting the
  // reader work it out from a label reading "manual match exists".
  const contested = $derived(
    candidates.length > 0 && candidates.every((candidate) => candidate.alreadyMapped)
  );
  const holder = $derived(candidates.find((candidate) => candidate.heldByPath)?.heldByPath ?? '');

  // What put the track in the list, in words rather than in the column name the
  // query used. "tags only" is the one that has to read differently from "tags
  // and length": both are in front of the reader because Schall would not decide
  // them alone, and the first is here because there was no length to check the
  // tags against.
  const EVIDENCE: Record<string, string> = {
    verified_download: 'Schall fetched it',
    musicbrainz_recording_id: 'recording ID',
    resolved_identity: "the file's identity",
    isrc: 'ISRC',
    album_disc_track: 'tags and length',
    album_disc_track_untimed: 'tags only',
    catalogue_search: 'from search'
  };
</script>

<div class="flex flex-col gap-3">
  <p class="numeric text-meta leading-relaxed text-ink-3">{file.path}</p>

  {#if problem}
    <ErrorNote error={problem} />
  {/if}

  {#if contested && !loading}
    <div class="flex flex-col gap-1.5 rounded-panel bg-surface-thick p-4">
      <p class="text-body leading-relaxed text-ink">
        {candidates.length === 1
          ? 'Another file is already matched to the one track this file fits.'
          : 'Another file is already matched to every track this file fits.'}
        This is usually the same recording twice.
      </p>
      {#if holder}
        <p class="text-meta leading-relaxed text-ink-3">matched to {holder}</p>
      {/if}
      <p class="text-meta leading-relaxed text-ink-3">
        Taking the track moves it to this file and leaves the other one unmatched. Leave it
        alone if this file is the copy you do not want.
      </p>
    </div>
  {/if}

  {#if confirming}
    {@const asked = confirming}
    <div class="flex flex-col gap-3 rounded-panel bg-surface-thick p-4">
      <h3 class="font-display text-lead font-bold text-ink">
        Move {asked.title} to this file?
      </h3>
      <p class="text-meta leading-relaxed text-ink-3">
        {asked.heldByPath || 'Another file'} is matched to it now and will be left unmatched.
      </p>
      <div class="flex flex-wrap items-center gap-2">
        <Button
          disabled={pending}
          onclick={() => $match.mutate({ trackId: asked.trackId, reassign: true })}
        >
          Reassign
        </Button>
        <Button variant="ghost" disabled={pending} onclick={() => (confirming = null)}>
          Cancel
        </Button>
      </div>
    </div>
  {/if}

  <form class="flex gap-2" onsubmit={searchCatalogue}>
    <input
      bind:value={search}
      aria-label="Search catalogue tracks"
      placeholder="Search all catalogue tracks"
      class="field min-w-0 flex-1"
    />
    <Button type="submit" variant="outline" disabled={search.trim().length < 2}>
      <Search size={12} strokeWidth={2.2} /> Search
    </Button>
  </form>

  {#if loading}
    <div class="flex min-h-[17.5rem] items-start gap-2 text-meta text-ink-3">
      <LoaderCircle size={12} class="animate-spin" /> Loading matches…
    </div>
  {:else}
    <div class="flex flex-col">
      {#each candidates as candidate (candidate.trackId)}
        <button
          class="flex items-center gap-3 border-b border-line-thin px-3 py-2 text-left transition last:border-b-0 hover:bg-surface-thick disabled:opacity-40"
          disabled={pending}
          onclick={() => choose(candidate)}
        >
          <span class="min-w-0 flex-1">
            <span class="block truncate text-body font-medium text-ink">{candidate.title}</span>
            <span class="mt-0.5 block truncate text-meta text-ink-3">
              {candidate.artistName} · {candidate.albumTitle} · {candidate.discNumber}.{candidate.trackNumber ??
                '—'}
            </span>
          </span>
          <span class="shrink-0 text-meta text-ink-4">
            {candidate.manuallyMapped
              ? 'manual match exists'
              : candidate.alreadyMapped
                ? 'replace auto match'
                : (EVIDENCE[candidate.method] ?? candidate.method.replaceAll('_', ' '))}
          </span>
        </button>
      {:else}
        <div class="px-3 py-2">
          <span class="text-meta text-ink-3">
            no candidates · search the catalogue
          </span>
        </div>
      {/each}
    </div>
  {/if}

  <div class="flex flex-wrap gap-2">
    <Button variant="ghost" disabled={pending} onclick={() => $reconcile.mutate()}>
      <RefreshCw size={14} /> Ask again
    </Button>
    {#if file.mappingManual}
      <Button variant="ghost" class="text-fail" disabled={pending} onclick={() => $clear.mutate()}>
        <Unlink size={14} /> Withdraw match
      </Button>
    {/if}
  </div>
</div>

<script lang="ts">
  import { createMutation, createQuery, useQueryClient } from '@tanstack/svelte-query';
  import { LoaderCircle } from '@lucide/svelte';
  import { api, type InboxCleanup, type InboxCleanupClass } from '$lib/api';
  import Button from '$lib/components/Button.svelte';
  import Card from '$lib/components/Card.svelte';
  import ErrorNote from '$lib/components/ErrorNote.svelte';
  import { formatBytes, relativeTime } from '$lib/utils';

  // Clean the inbox: the pass that deletes the downloaded files nothing needs
  // any more. The rule is ADR 0035 and it lives on the server; this
  // block asks for a count, shows what the count came to, and asks for the
  // deletion.
  //
  // Counting first is not a nicety. The inbox holds 922 GB across 36,082 files
  // and the numbers are the only thing standing between a person and an
  // irreversible delete, so the second button does not exist until a dry run has
  // answered.

  const queryClient = useQueryClient();

  const cleanups = createQuery({
    queryKey: ['inbox-cleanups'],
    queryFn: api.inboxCleanups,
    // A running pass announces itself over the event stream; this is the
    // fallback for a stream that never arrived.
    refetchInterval: (query) =>
      ['queued', 'running'].includes(query.state.data?.items?.[0]?.status ?? '') ? 15_000 : false
  });

  const latest = $derived<InboxCleanup | undefined>($cleanups.data?.items?.[0]);
  const running = $derived(['queued', 'running'].includes(latest?.status ?? ''));
  // Only a dry run that answered says what a real one would delete. A finished
  // deletion has already happened, so it offers nothing to press.
  const counted = $derived(
    latest && latest.dryRun && latest.status === 'finished' ? latest : undefined
  );

  let confirming = $state(false);

  const clean = createMutation({
    mutationFn: (dryRun: boolean) => api.cleanInbox(dryRun),
    onSuccess: async () => {
      confirming = false;
      await queryClient.invalidateQueries({ queryKey: ['inbox-cleanups'] });
    }
  });

  // What each class is called on screen. The class names are the record's
  // words; these are the reader's.
  const classNames: Record<InboxCleanupClass['class'], string> = {
    imported: 'Already imported',
    refused: 'Refused copies',
    settled: 'Wants that are finished',
    unknown: 'Nothing recorded them',
    kept_question: 'Waiting for your answer',
    kept_open: 'Downloads still open',
    kept_recent: 'Changed in the last day',
    kept_unconfirmed: 'No library copy found',
    kept_undecided: 'No rule covers them',
    kept_unsafe_path: 'Path changed while running'
  };

  const deleteClasses = $derived(counted?.classes.filter((row) => row.deletes) ?? []);
  const keptClasses = $derived(
    counted?.classes.filter((row) => !row.deletes && row.files > 0) ?? []
  );
</script>

<Card>
  <div class="flex flex-wrap items-center gap-2.5">
    <h2 class="font-display text-lead font-bold text-ink">Clean the inbox</h2>
    <Button
      variant="outline"
      size="sm"
      class="ml-auto"
      disabled={$cleanups.isPending || running || $clean.isPending}
      onclick={() => $clean.mutate(true)}
    >
      {#if running || $clean.isPending}<LoaderCircle size={12} class="animate-spin" />{/if}
      Count what can go
    </Button>
  </div>

  <span class="text-meta text-ink-3">
    Deletes the downloaded files nothing needs any more. Review questions, open downloads and
    anything changed in the last day stay.
  </span>

  <details class="text-meta text-ink-3">
    <summary class="cursor-pointer text-ink-2">What is deleted</summary>
    <ul class="mt-1.5 flex list-disc flex-col gap-1 pl-4">
      <li>the source of a download already in your library, checked on disk first</li>
      <li>copies refused because the audio or the tags said this is another recording</li>
      <li>copies of wants that are acquired, not wanted, or superseded</li>
      <li>files no download and no copy in Schall recorded, including folders fetched by hand</li>
    </ul>
  </details>

  {#if $cleanups.isError}
    <ErrorNote
      error={$cleanups.error}
      retry={() => $cleanups.refetch()}
      fallback="The passes over the inbox could not be read."
    />
  {/if}

  {#if $clean.isError}
    <ErrorNote error={$clean.error} />
  {/if}

  <!-- The server's own sentence. It covers both a pass that stopped and one
       that finished with files it could not remove, and it already names what
       happened, so nothing is added in front of it. -->
  {#if latest?.error}
    <span class="text-meta text-fail">{latest.error}</span>
  {/if}

  {#if running}
    <span class="text-meta text-ink-3">
      {latest?.dryRun ? 'Counting' : 'Deleting'} — this reads every file in the inbox.
    </span>
  {/if}

  {#if counted}
    <table class="w-full text-meta">
      <thead>
        <tr class="border-b border-line-thin text-ink-3">
          <th scope="col" class="py-1 text-left font-medium">To delete</th>
          <th scope="col" class="py-1 text-right font-medium">Files</th>
          <th scope="col" class="py-1 text-right font-medium">Size</th>
        </tr>
      </thead>
      <tbody>
        {#each deleteClasses as row (row.class)}
          <tr class="border-b border-line-thin">
            <td class="py-1 text-ink-2">{classNames[row.class]}</td>
            <td class="numeric py-1 text-right text-ink">{row.files.toLocaleString()}</td>
            <td class="numeric py-1 text-right text-ink">{formatBytes(row.bytes)}</td>
          </tr>
        {/each}
        {#each keptClasses as row (row.class)}
          <tr class="border-b border-line-thin">
            <td class="py-1 text-ink-3">{classNames[row.class]} · kept</td>
            <td class="numeric py-1 text-right text-ink-3">{row.files.toLocaleString()}</td>
            <td class="numeric py-1 text-right text-ink-3">{formatBytes(row.bytes)}</td>
          </tr>
        {/each}
      </tbody>
    </table>

    <span class="numeric text-meta text-ink-4">
      counted {relativeTime(counted.finishedAt) || 'just now'} · {counted.keptFiles.toLocaleString()}
      files kept
    </span>

    {#if counted.deleteFiles === 0}
      <span class="text-meta text-ink-3">Nothing in the inbox can go.</span>
    {:else if confirming}
      <div class="flex flex-wrap items-center gap-2.5">
        <span class="text-meta text-ink">
          Delete {counted.deleteFiles.toLocaleString()} files? This cannot be undone.
        </span>
        <Button
          variant="danger"
          size="sm"
          disabled={$clean.isPending}
          onclick={() => $clean.mutate(false)}
        >
          Delete
        </Button>
        <Button variant="ghost" size="sm" onclick={() => (confirming = false)}>Cancel</Button>
      </div>
    {:else}
      <div>
        <Button
          variant="outline"
          size="sm"
          disabled={$clean.isPending}
          onclick={() => (confirming = true)}
        >
          Delete {counted.deleteFiles.toLocaleString()} files ({formatBytes(counted.deleteBytes)})
        </Button>
      </div>
    {/if}
  {/if}

  <!-- Shown for a finished deletion whether or not something went wrong, so a
       pass that could not remove three files still says what it did remove. -->
  {#if latest && !latest.dryRun && latest.finishedAt}
    <span class="numeric text-meta text-ink-4">
      {(latest.deleteFiles - latest.failedFiles).toLocaleString()} files ({formatBytes(
        latest.deleteBytes
      )}) deleted {relativeTime(latest.finishedAt) || 'just now'}
    </span>
  {/if}
</Card>

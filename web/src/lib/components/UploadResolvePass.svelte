<script lang="ts">
  import { createMutation } from '@tanstack/svelte-query';
  import { ListChecks, Music2 } from '@lucide/svelte';
  import { api, type UploadFile } from '$lib/api';
  import Button from '$lib/components/Button.svelte';
  import ReviewMatch from '$lib/components/ReviewMatch.svelte';
  import SoundCloudTrackModal from '$lib/components/SoundCloudTrackModal.svelte';

  let { files }: { files: UploadFile[] } = $props();

  let activeId = $state('');
  let skipped = $state<string[]>([]);
  let mode = $state<'soundcloud' | 'manual' | ''>('');
  let soundCloudOpen = $state(false);

  const waiting = $derived(
    files.filter((file) => file.state === 'waiting' && file.file?.id)
  );
  const active = $derived(
    waiting.find((file) => file.file?.id === activeId) ??
      waiting.find((file) => !skipped.includes(file.file!.id)) ??
      null
  );
  const currentID = $derived(active?.file?.id ?? '');
  const remaining = $derived(waiting.filter((file) => !skipped.includes(file.file!.id)));

  $effect(() => {
    if (active && active.file!.id !== activeId) {
      activeId = active.file!.id;
      mode = '';
    }
    if (!active) {
      activeId = '';
      mode = '';
    }
  });

  function advance(fileID: string) {
    skipped = [...skipped, fileID];
    activeId = '';
    mode = '';
  }

  const setAsideMutation = createMutation({
    mutationFn: (fileID: string) => api.setAsideLibraryFile(fileID),
    onSuccess: (_data, fileID) => advance(fileID)
  });

  function answer() {
    if (!currentID) return;
    advance(currentID);
  }

  function chooseMode(next: 'soundcloud' | 'manual') {
    mode = next;
    if (next === 'soundcloud') soundCloudOpen = true;
  }
</script>

{#if files.length > 1}
  <section class="flex flex-col gap-3 rounded-panel border border-line-regular p-4">
    <div class="flex flex-wrap items-center gap-2">
      <ListChecks size={15} class="text-ink-3" />
      <span class="label">Files in this upload</span>
      <span class="numeric text-meta text-ink-4">{files.length}</span>
      <span class="h-px flex-1 bg-line-thin"></span>
    </div>

    <div class="flex flex-col">
      {#each files as item (item.name)}
        <div class="flex items-center gap-3 border-b border-line-thin py-2 last:border-b-0">
          <span class="numeric min-w-0 flex-1 truncate text-body text-ink" title={item.name}>
            {item.name}
          </span>
          <span class="text-meta text-ink-3">
            {item.state === 'importing'
              ? 'Importing automatically'
              : item.state === 'waiting'
                ? 'Waiting for an answer'
                : item.state === 'set_aside'
                  ? 'Set aside'
                : item.state === 'failed'
                  ? 'Import failed'
                  : 'Imported'}
          </span>
        </div>
      {/each}
    </div>

    {#if active}
      <div class="flex flex-col gap-3 border-t border-line-thin pt-3">
        <div class="flex items-start gap-2.5">
          <Music2 size={15} class="mt-0.5 shrink-0 text-ink-3" />
          <div class="min-w-0">
            <p class="text-body font-semibold text-ink">{active.name} needs an answer</p>
            <p class="mt-1 text-meta text-ink-3">Choose how to name this file.</p>
          </div>
        </div>

        <div class="flex flex-wrap gap-2">
          <Button
            variant="outline"
            aria-pressed={mode === 'soundcloud'}
            onclick={() => chooseMode('soundcloud')}
          >
            SoundCloud track
          </Button>
          <Button
            variant="outline"
            aria-pressed={mode === 'manual'}
            onclick={() => chooseMode('manual')}
          >
            Match by hand
          </Button>
          <Button
            variant="ghost"
            disabled={$setAsideMutation.isPending}
            onclick={() => currentID && $setAsideMutation.mutate(currentID)}
          >Set aside</Button>
        </div>

        <p class="text-meta text-ink-4">
          Set aside keeps {active.name} unmatched until you choose Return to review in Library.
        </p>

        {#if mode === 'manual'}
          <div class="border-t border-line-thin pt-3">
            <ReviewMatch file={active.file!} ondecided={answer} />
          </div>
        {/if}

        <p class="text-meta text-ink-4">
          {remaining.length} {remaining.length === 1 ? 'file needs' : 'files need'} an answer in this pass.
        </p>
      </div>
    {:else if waiting.length > 0}
      <p class="border-t border-line-thin pt-3 text-meta text-ink-3">
        No more files need an answer in this pass.
      </p>
    {/if}
  </section>
{/if}

{#if active?.file && soundCloudOpen}
  <SoundCloudTrackModal
    bind:open={soundCloudOpen}
    fileId={active.file.id}
    path={active.file.path}
    ondecided={answer}
  />
{/if}

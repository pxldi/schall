<script lang="ts">
  import { createMutation, useQueryClient } from '@tanstack/svelte-query';
  import { untrack } from 'svelte';
  import { fade } from 'svelte/transition';
  import { FileUp, LoaderCircle, Upload, X } from '@lucide/svelte';
  import { api, type PlaylistFilePreview } from '$lib/api';
  import { motionMs } from '$lib/motion.svelte';
  import Button from './Button.svelte';
  import ErrorNote from './ErrorNote.svelte';

  // A file playlist arrives in two requests: the first reads it and shows
  // what it found, the second imports it. Both are given the file itself —
  // the second never trusts what the first told the browser, because an entry
  // is evidence and evidence comes from the file, not from a preview shown on
  // screen a moment before.
  let { open = $bindable(false) }: { open: boolean } = $props();

  const queryClient = useQueryClient();

  let chosen = $state<File | null>(null);
  let listName = $state('');
  let dragging = $state(false);

  let panel = $state<HTMLElement | null>(null);
  let picker = $state<HTMLInputElement | null>(null);
  let nameField = $state<HTMLInputElement | null>(null);

  const preview = createMutation({
    mutationFn: (file: File) => api.previewPlaylistFile(file),
    onSuccess: (result: PlaylistFilePreview) => {
      listName = result.name;
    }
  });

  const importFile = createMutation({
    mutationFn: () => {
      if (!chosen) throw new Error('choose a file first');
      return api.importPlaylistFile(chosen, listName);
    },
    onSuccess: async () => {
      close();
      await queryClient.invalidateQueries({ queryKey: ['playlists'] });
    }
  });

  const inFlight = $derived($preview.isPending || $importFile.isPending);
  const result = $derived($preview.data ?? null);
  const shownRows = $derived(result?.rows.slice(0, 8) ?? []);

  function take(file: File | null | undefined) {
    if (!file || inFlight) return;
    chosen = file;
    $preview.reset();
    $importFile.reset();
    $preview.mutate(file);
  }

  function picked(event: Event) {
    const input = event.currentTarget as HTMLInputElement;
    take(input.files?.[0]);
    input.value = '';
  }

  function dropped(event: DragEvent) {
    event.preventDefault();
    dragging = false;
    take(event.dataTransfer?.files?.[0]);
  }

  function submit(event: SubmitEvent) {
    event.preventDefault();
    if (result && !inFlight) $importFile.mutate();
  }

  $effect(() => {
    if (!open) return;

    const opener = document.activeElement;
    const body = document.body;
    const overflow = body.style.overflow;
    const gutterRoom = body.style.paddingRight;
    const gutter = window.innerWidth - document.documentElement.clientWidth;

    body.style.overflow = 'hidden';
    if (gutter > 0) body.style.paddingRight = `${gutter}px`;
    untrack(() => picker)?.focus();

    return () => {
      body.style.overflow = overflow;
      body.style.paddingRight = gutterRoom;
      returnFocus(opener);
    };
  });

  function returnFocus(opener: Element | null) {
    if (opener instanceof HTMLElement && opener.isConnected) {
      opener.focus();
      return;
    }
    const heading = document.querySelector('h1');
    if (!heading) return;
    if (!heading.hasAttribute('tabindex')) heading.setAttribute('tabindex', '-1');
    heading.focus();
  }

  function keydown(event: KeyboardEvent) {
    if (!open) return;

    if (event.key === 'Escape') {
      if (!inFlight) close();
      return;
    }
    if (event.key !== 'Tab' || !panel) return;

    const stops = [
      ...panel.querySelectorAll<HTMLElement>(
        'a[href], button:not([disabled]), input:not([disabled]), [tabindex]:not([tabindex="-1"])'
      )
    ].filter((stop) => stop.offsetParent !== null || stop === document.activeElement);
    if (stops.length === 0) {
      event.preventDefault();
      panel.focus();
      return;
    }

    const first = stops[0];
    const last = stops[stops.length - 1];
    const here = document.activeElement;
    const outside = !(here instanceof Node) || !panel.contains(here);

    if (event.shiftKey && (outside || here === first)) {
      event.preventDefault();
      last.focus();
    } else if (!event.shiftKey && (outside || here === last)) {
      event.preventDefault();
      first.focus();
    }
  }

  function scrimPress(event: MouseEvent) {
    if (event.target !== event.currentTarget) return;
    if (!inFlight) close();
  }

  function close() {
    open = false;
    chosen = null;
    listName = '';
    dragging = false;
    $preview.reset();
    $importFile.reset();
  }
</script>

<svelte:window onkeydown={keydown} />

{#if open}
  <div
    class="fixed inset-0 z-50 flex items-center justify-center bg-black/55 p-6 backdrop-blur-[8px]"
    role="presentation"
    onmousedown={scrimPress}
    transition:fade={{ duration: motionMs('surface') }}
  >
    <div
      class="flex max-h-full w-full max-w-md flex-col overflow-hidden rounded-panel border border-[rgba(232,233,231,0.09)] bg-[rgba(32,34,39,0.55)] backdrop-blur-[14px]"
      role="dialog"
      aria-modal="true"
      aria-labelledby="import-file-title"
      tabindex="-1"
      bind:this={panel}
    >
      <form class="flex min-h-0 flex-1 flex-col" onsubmit={submit}>
        <div class="flex flex-none items-center gap-3 border-b border-line-thin px-4 py-3.5">
          <h2 id="import-file-title" class="font-display text-lead font-bold">
            Import from file
          </h2>
          <button
            class="ml-auto grid size-6 flex-none place-items-center rounded-control text-ink-3 transition hover:bg-surface-thick hover:text-ink disabled:opacity-50"
            type="button"
            onclick={close}
            disabled={inFlight}
            aria-label="Close"
          >
            <X size={18} />
          </button>
        </div>

        <div class="min-h-0 flex-1 overflow-y-auto px-4 pb-3.5 pt-3">
          {#if !result}
            <button
              type="button"
              class="flex w-full flex-col items-center gap-1.5 rounded-control border border-dashed px-4 py-6 text-center transition disabled:opacity-50 {dragging
                ? 'border-accent-soft bg-surface-thick'
                : 'border-line-regular hover:bg-surface-thick'}"
              onclick={() => picker?.click()}
              ondragover={(event) => {
                event.preventDefault();
                dragging = true;
              }}
              ondragleave={() => (dragging = false)}
              ondrop={dropped}
              disabled={inFlight}
            >
              {#if $preview.isPending}
                <LoaderCircle size={18} class="animate-spin text-ink-3" />
                <span class="text-body font-medium text-ink">Reading {chosen?.name}…</span>
              {:else}
                <FileUp size={18} class="text-ink-3" />
                <span class="text-body font-medium text-ink">Choose a CSV or M3U file</span>
                <span class="text-meta text-ink-3">or drop one here</span>
              {/if}
            </button>
            <input
              bind:this={picker}
              id="playlist-file"
              type="file"
              accept=".csv,.tsv,.m3u,.m3u8,text/csv,audio/x-mpegurl"
              class="sr-only"
              onchange={picked}
              tabindex="-1"
              aria-hidden="true"
            />
            {#if $preview.isError}
              <ErrorNote error={$preview.error} class="mt-3" />
            {/if}
          {:else}
            <label for="playlist-file-name" class="label">Name</label>
            <input
              id="playlist-file-name"
              bind:this={nameField}
              type="text"
              bind:value={listName}
              maxlength="200"
              autocomplete="off"
              class="field mt-2 w-full"
              disabled={inFlight}
            />

            <p class="mt-3 text-meta text-ink-3">
              {result.rowCount}
              {result.rowCount === 1 ? 'song' : 'songs'} read from {chosen?.name}
              {#if result.skipped.length > 0}
                · {result.skipped.length} row{result.skipped.length === 1 ? '' : 's'} skipped
              {/if}
            </p>

            {#if shownRows.length > 0}
              <ul class="mt-2 rounded-panel border border-line-thin">
                {#each shownRows as row (row.position)}
                  <li
                    class="truncate border-b border-line-thin px-2.5 py-1.5 text-meta text-ink-2 last:border-b-0"
                  >
                    {row.artist ? `${row.artist} — ${row.title}` : row.title}
                  </li>
                {/each}
                {#if result.rows.length > shownRows.length}
                  <li class="px-2.5 py-1.5 text-meta text-ink-3">
                    and {result.rows.length - shownRows.length} more
                  </li>
                {/if}
              </ul>
            {/if}

            {#if result.skipped.length > 0}
              <details class="mt-2 text-meta text-ink-3">
                <summary class="cursor-pointer select-none">Why rows were skipped</summary>
                <ul class="mt-1 flex flex-col gap-0.5">
                  {#each result.skipped as skipped (skipped.line)}
                    <li>Line {skipped.line}: {skipped.reason}</li>
                  {/each}
                </ul>
              </details>
            {/if}

            {#if $importFile.isError}
              <ErrorNote error={$importFile.error} class="mt-3" />
            {/if}
          {/if}
        </div>

        <div
          class="flex flex-none items-center justify-end gap-2 border-t border-line-thin px-4 py-3"
        >
          <Button type="button" variant="ghost" onclick={close} disabled={inFlight}>Cancel</Button>
          {#if result}
            <Button type="submit" disabled={!listName.trim() || inFlight}>
              {#if $importFile.isPending}
                <LoaderCircle size={13} class="animate-spin" /> Importing
              {:else}
                <Upload size={13} strokeWidth={2.2} />
                Import
              {/if}
            </Button>
          {/if}
        </div>
      </form>
    </div>
  </div>
{/if}

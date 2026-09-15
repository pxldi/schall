<script lang="ts">
  // The one confirm before Schall deletes music on purpose.
  //
  // The person has picked the copy to keep on the duplicates list. This names
  // every file that goes and the one that stays, with its path and its size, so
  // nothing is deleted that was not read first. It reads the copies it was
  // handed and asks for nothing of its own.
  import { untrack } from 'svelte';
  import { fade } from 'svelte/transition';
  import { LoaderCircle, X } from '@lucide/svelte';
  import type { DuplicateCopy, DuplicateRecording } from '$lib/api';
  import { formatBytes } from '$lib/utils';
  import { motionMs } from '$lib/motion.svelte';
  import Button from './Button.svelte';
  import ErrorNote from './ErrorNote.svelte';

  let {
    recording,
    keeper,
    busy = false,
    failure = null,
    onconfirm,
    oncancel
  }: {
    recording: DuplicateRecording;
    keeper: DuplicateCopy;
    busy?: boolean;
    failure?: unknown;
    onconfirm: () => void;
    oncancel: () => void;
  } = $props();

  // A copy that is also a copy of different music is never deleted here, so it
  // is not on the list. It is named underneath instead: a file the person can
  // see on the screen and will still be able to see afterwards.
  const going = $derived(
    recording.copies.filter((copy) => copy.id !== keeper.id && !copy.answersOther)
  );
  const spared = $derived(
    recording.copies.filter((copy) => copy.id !== keeper.id && copy.answersOther)
  );
  const name = $derived(
    [recording.title, recording.artist].filter(Boolean).join(' · ') || 'this recording'
  );

  // The handles the panel needs to move focus and to hold it. Nothing on screen
  // reads them.
  let panel = $state<HTMLElement | null>(null);

  // Opening takes the page away and gives it back, the way the other overlay
  // does: the scroll is locked with its gutter kept so nothing shifts sideways,
  // and on the way out focus returns where it came from. Focus lands on the
  // panel itself and on no control, so the first key a person presses here
  // cannot be the irreversible one; the reading starts at the top, which is
  // where the files are named.
  $effect(() => {
    const opener = document.activeElement;
    const body = document.body;
    const overflow = body.style.overflow;
    const gutterRoom = body.style.paddingRight;
    const gutter = window.innerWidth - document.documentElement.clientWidth;

    body.style.overflow = 'hidden';
    if (gutter > 0) body.style.paddingRight = `${gutter}px`;
    untrack(() => panel)?.focus();

    return () => {
      body.style.overflow = overflow;
      body.style.paddingRight = gutterRoom;
      if (opener instanceof HTMLElement && opener.isConnected) opener.focus();
    };
  });

  // Tab and Shift-Tab cycle inside the panel and never reach the page behind.
  // Escape leaves on the same terms as every other way out.
  function keydown(event: KeyboardEvent) {
    if (event.key === 'Escape') {
      if (!busy) oncancel();
      return;
    }
    if (event.key !== 'Tab' || !panel) return;

    const stops = [
      ...panel.querySelectorAll<HTMLElement>(
        'a[href], button:not([disabled]), input:not([disabled]), [tabindex]:not([tabindex="-1"])'
      )
    ];
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
    if (!busy) oncancel();
  }
</script>

<svelte:window onkeydown={keydown} />

<div
  class="fixed inset-0 z-50 flex items-center justify-center bg-black/55 p-6 backdrop-blur-[8px]"
  role="presentation"
  onmousedown={scrimPress}
  transition:fade={{ duration: motionMs('surface') }}
>
  <div
    class="flex max-h-full w-full max-w-lg flex-col overflow-hidden rounded-panel border border-[rgba(232,233,231,0.09)] bg-[rgba(32,34,39,0.55)] backdrop-blur-[14px]"
    role="dialog"
    aria-modal="true"
    aria-labelledby="keep-one-title"
    tabindex="-1"
    bind:this={panel}
  >
    <div class="flex flex-none items-center gap-3 border-b border-line-thin px-4 py-3.5">
      <h2 id="keep-one-title" class="font-display text-lead font-bold">Keep this copy</h2>
      <button
        class="ml-auto grid size-6 flex-none place-items-center rounded-control text-ink-3 transition hover:bg-surface-thick hover:text-ink disabled:opacity-50"
        type="button"
        onclick={oncancel}
        disabled={busy}
        aria-label="Close"
      >
        <X size={18} />
      </button>
    </div>

    <div class="min-h-0 flex-1 overflow-y-auto px-4 pb-3.5 pt-3">
      <p class="text-body text-ink">
        {going.length === 1 ? '1 copy' : `${going.length} copies`} of {name} will be deleted from disc.
        This cannot be undone.
      </p>

      <p class="mt-3 label">Stays</p>
      <div class="mt-1 flex items-baseline gap-3">
        <span class="numeric min-w-0 flex-1 break-all text-meta text-ink" title={keeper.path}>
          {keeper.path}
        </span>
        <span class="numeric shrink-0 text-meta text-ink-2">{formatBytes(keeper.sizeBytes)}</span>
      </div>

      <p class="mt-3 label">Deleted</p>
      <div class="mt-1 flex flex-col gap-1">
        {#each going as copy (copy.id)}
          <div class="flex items-baseline gap-3">
            <span class="numeric min-w-0 flex-1 break-all text-meta text-ink-2" title={copy.path}>
              {copy.path}
            </span>
            <span class="numeric shrink-0 text-meta text-ink-3">{formatBytes(copy.sizeBytes)}</span>
          </div>
        {/each}
      </div>

      {#if spared.length > 0}
        <p class="mt-3 text-meta leading-4 text-ink-3">
          {spared.length === 1 ? '1 copy stays' : `${spared.length} copies stay`} as well: each one
          is also a copy of other music.
        </p>
      {/if}

      {#if failure}
        <ErrorNote error={failure} class="mt-3" />
      {/if}
    </div>

    <div class="flex flex-none items-center justify-end gap-2 border-t border-line-thin px-4 py-3">
      <Button type="button" variant="ghost" onclick={oncancel} disabled={busy}>Cancel</Button>
      <Button type="button" onclick={onconfirm} disabled={busy || going.length === 0}>
        {#if busy}
          <LoaderCircle size={13} class="animate-spin" /> Deleting
        {:else}
          {#if going.length === 0}
            Nothing to delete
          {:else if going.length === 1}
            Delete the other copy
          {:else}
            Delete {going.length} copies
          {/if}
        {/if}
      </Button>
    </div>
  </div>
</div>

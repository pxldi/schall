<script lang="ts">
  import { createMutation, useQueryClient } from '@tanstack/svelte-query';
  import { fade } from 'svelte/transition';
  import { X } from '@lucide/svelte';
  import { api, type SoundCloudNamingFields, type SoundCloudTrack } from '$lib/api';
  import { motionMs } from '$lib/motion.svelte';
  import Button from './Button.svelte';
  import ErrorNote from './ErrorNote.svelte';
  import SoundCloudNamingFieldsInput from './SoundCloudNamingFields.svelte';

  // The file being named, and the path shown so the reader can see which file
  // they are about to decide about for good.
  let {
    open = $bindable(false),
    fileId,
    path = '',
    ondecided
  }: { open: boolean; fileId: string; path?: string; ondecided?: () => void } = $props();

  const queryClient = useQueryClient();

  let url = $state('');
  let found = $state<SoundCloudTrack | null>(null);
  // What the track will be called. Schall fills these in from the title and the
  // person corrects whatever it got wrong: the file is theirs and the address is
  // their word about it, so the split is a suggestion, never a claim.
  let named = $state<SoundCloudNamingFields>({ artist: '', title: '', remixer: '' });
  let panel = $state<HTMLElement | null>(null);
  let field = $state<HTMLInputElement | null>(null);

  // Two steps, two requests. The first only reads: it is what puts the title,
  // the uploader and the artwork on screen before anything is written. The
  // second is the decision, and it is permanent.
  const look = createMutation({
    mutationFn: (address: string) => api.soundCloudTrack(address),
    onSuccess: (track) => {
      found = track;
      named = {
        artist: track.suggested?.artist || track.uploader,
        title: track.suggested?.title || track.trackTitle,
        remixer: track.suggested?.remixer ?? ''
      };
    }
  });

  const name = createMutation({
    mutationFn: (track: SoundCloudTrack) =>
      api.nameFromSoundCloud(fileId, track.permalink, {
        artist: named.artist.trim(),
        title: named.title.trim(),
        remixer: named.remixer.trim()
      }),
    onSuccess: async () => {
      close();
      await Promise.all([
        queryClient.invalidateQueries({ queryKey: ['library-files'] }),
        queryClient.invalidateQueries({ queryKey: ['library'] }),
        queryClient.invalidateQueries({ queryKey: ['artists'] })
      ]);
      ondecided?.();
    }
  });

  const inFlight = $derived($look.isPending || $name.isPending);

  $effect(() => {
    if (!open) return;
    const opener = document.activeElement;
    field?.focus();
    return () => {
      if (opener instanceof HTMLElement && opener.isConnected) opener.focus();
    };
  });

  // Tab and Shift-Tab cycle inside the panel and never reach the page behind.
  // Escape leaves on the same terms as every other way out.
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
    if (!inFlight) close();
  }

  // Editing the address takes the answer off the screen. What is shown has to
  // be what the confirm button will act on.
  function updateUrl(event: Event) {
    url = (event.currentTarget as HTMLInputElement).value;
    found = null;
    named = { artist: '', title: '', remixer: '' };
    $look.reset();
    $name.reset();
  }

  // A track with no artist and no title would be filed under nothing, so the
  // confirm waits until both say something.
  const namedEnough = $derived(named.artist.trim() !== '' && named.title.trim() !== '');

  function submit(event: SubmitEvent) {
    event.preventDefault();
    if (found) {
      if (namedEnough) $name.mutate(found);
    } else if (url.trim()) {
      $look.mutate(url.trim());
    }
  }

  function close() {
    open = false;
    url = '';
    found = null;
    named = { artist: '', title: '', remixer: '' };
    $look.reset();
    $name.reset();
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
      aria-labelledby="soundcloud-title"
      tabindex="-1"
      bind:this={panel}
    >
      <form class="flex min-h-0 flex-1 flex-col" onsubmit={submit}>
        <div class="flex flex-none items-center gap-3 border-b border-line-thin px-4 py-3.5">
          <h2 id="soundcloud-title" class="font-display text-lead font-bold">
            This is a SoundCloud track
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
          {#if path}
            <p class="mb-3 truncate text-meta text-ink-3" title={path}>{path}</p>
          {/if}

          <label for="soundcloud-url" class="label">Track link</label>
          <input
            id="soundcloud-url"
            class="field mt-2 w-full"
            value={url}
            oninput={updateUrl}
            placeholder="https://soundcloud.com/artist/track"
            autocomplete="off"
            maxlength="500"
            bind:this={field}
          />

          {#if $look.isError}
            <div class="mt-3">
              <ErrorNote error={$look.error} bare />
            </div>
          {/if}
          {#if $name.isError}
            <div class="mt-3">
              <ErrorNote error={$name.error} bare />
            </div>
          {/if}

          {#if found}
            <div class="mt-4 flex flex-col gap-4 border-t border-line-thin pt-4">
              <SoundCloudNamingFieldsInput track={found} bind:named />
            </div>

            <details class="mt-3">
              <summary class="cursor-pointer text-meta text-ink-3">What this changes</summary>
              <p class="mt-2 text-meta leading-relaxed text-ink-3">
                The file takes these names and this artwork, and moves to the
                folder they spell. Schall stops looking for a matching recording,
                and the file cannot fill a want. You can change it later.
              </p>
            </details>
          {/if}
        </div>

        <div class="flex flex-none items-center justify-end gap-2 border-t border-line-thin px-4 py-3">
          <Button type="button" variant="ghost" onclick={close} disabled={inFlight}>Cancel</Button>
          {#if found}
            <Button type="submit" disabled={inFlight || !namedEnough}>Confirm</Button>
          {:else}
            <Button type="submit" disabled={inFlight || url.trim() === ''}>Look up</Button>
          {/if}
        </div>
      </form>
    </div>
  </div>
{/if}

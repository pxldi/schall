<script lang="ts">
  import { createMutation, useQueryClient } from '@tanstack/svelte-query';
  import { untrack } from 'svelte';
  import { fade } from 'svelte/transition';
  import { Image, LoaderCircle, X } from '@lucide/svelte';
  import { api } from '$lib/api';
  import { motionMs } from '$lib/motion.svelte';
  import Button from './Button.svelte';
  import ErrorNote from './ErrorNote.svelte';

  // `settable` is a picture the person supplied, which is the only kind this
  // offers to take back: a cover an archive gave is a cached answer, and
  // removing it here would empty the cache for a question nobody asked.
  let {
    open = $bindable(false),
    releaseID,
    settable = false,
    onsaved
  }: {
    open: boolean;
    releaseID: string;
    settable?: boolean;
    onsaved?: () => void;
  } = $props();

  const queryClient = useQueryClient();

  let chosen = $state<File | null>(null);
  let address = $state('');
  let dragging = $state(false);

  // Handles the panel needs to move focus and to trap it. Nothing on screen
  // reads them.
  let panel = $state<HTMLElement | null>(null);
  let addressField = $state<HTMLInputElement | null>(null);
  let picker = $state<HTMLInputElement | null>(null);

  // The same cover is drawn on the library grid and on the artist's page, so
  // what is asked again is every release query rather than this one.
  async function settled() {
    await queryClient.invalidateQueries({ queryKey: ['releases'] });
    onsaved?.();
    close();
  }

  const setCover = createMutation({
    mutationFn: () =>
      chosen
        ? api.setReleaseCoverFile(releaseID, chosen)
        : api.setReleaseCoverUrl(releaseID, address.trim()),
    onSuccess: settled
  });

  const removeCover = createMutation({
    mutationFn: () => api.removeReleaseCover(releaseID),
    onSuccess: settled
  });

  const inFlight = $derived($setCover.isPending || $removeCover.isPending);
  const ready = $derived(Boolean(chosen) || address.trim().length > 0);
  const failure = $derived($setCover.error ?? $removeCover.error);

  // Opening takes the page away and gives it back: the scroll is locked with
  // its gutter kept so nothing shifts sideways, the address field takes focus
  // because typing is one of the two ways in, and on the way out focus goes
  // back where it came from.
  $effect(() => {
    if (!open) return;

    const opener = document.activeElement;
    const body = document.body;
    const overflow = body.style.overflow;
    const gutterRoom = body.style.paddingRight;
    const gutter = window.innerWidth - document.documentElement.clientWidth;

    body.style.overflow = 'hidden';
    if (gutter > 0) body.style.paddingRight = `${gutter}px`;
    untrack(() => addressField)?.focus();

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

  // Dismissal follows mousedown, not mouseup: a click that starts inside the
  // panel and ends on the scrim was a selection being dragged.
  function scrimPress(event: MouseEvent) {
    if (event.target !== event.currentTarget) return;
    if (!inFlight) close();
  }

  function submit(event: SubmitEvent) {
    event.preventDefault();
    if (ready && !inFlight) $setCover.mutate();
  }

  // A file and an address are two answers to one question, so choosing either
  // one puts the other down. The picker's own value is cleared as well, or
  // choosing the same file twice after an error would not count as a change.
  function take(file: File | null | undefined) {
    if (!file) return;
    chosen = file;
    address = '';
    $setCover.reset();
  }

  function picked(event: Event) {
    const input = event.currentTarget as HTMLInputElement;
    take(input.files?.[0]);
    input.value = '';
  }

  function dropped(event: DragEvent) {
    event.preventDefault();
    dragging = false;
    if (inFlight) return;
    take(event.dataTransfer?.files?.[0]);
  }

  function typedAddress(event: Event) {
    address = (event.currentTarget as HTMLInputElement).value;
    chosen = null;
    $setCover.reset();
  }

  function close() {
    open = false;
    chosen = null;
    address = '';
    dragging = false;
    $setCover.reset();
    $removeCover.reset();
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
      aria-labelledby="set-cover-title"
      tabindex="-1"
      bind:this={panel}
    >
      <form class="flex min-h-0 flex-1 flex-col" onsubmit={submit}>
        <div class="flex flex-none items-center gap-3 border-b border-line-thin px-4 py-3.5">
          <h2 id="set-cover-title" class="font-display text-lead font-bold">Set cover</h2>
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
          <!-- The drop target is a button as well as a target, because dragging
               is not available to everybody and a target that only accepts a
               drag is a control half the readers cannot press. -->
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
            <Image size={18} class="text-ink-3" />
            <span class="text-body font-medium text-ink">
              {chosen ? chosen.name : 'Choose image'}
            </span>
            <span class="text-meta text-ink-3">or drop one here</span>
          </button>
          <!-- The picker itself is never reached: the button above is the
               control, and a second stop on the way to the same file dialog is
               a stop that answers nothing. -->
          <input
            bind:this={picker}
            id="cover-file"
            type="file"
            accept="image/*"
            class="sr-only"
            onchange={picked}
            tabindex="-1"
            aria-hidden="true"
          />

          <label for="cover-address" class="label mt-4 block">Image address</label>
          <input
            id="cover-address"
            bind:this={addressField}
            type="url"
            value={address}
            oninput={typedAddress}
            placeholder="https://"
            autocomplete="off"
            maxlength="2000"
            class="field mt-2 w-full"
            disabled={inFlight}
          />

          {#if failure}
            <ErrorNote error={failure} class="mt-3" />
          {/if}
        </div>

        <div
          class="flex flex-none items-center justify-end gap-2 border-t border-line-thin px-4 py-3"
        >
          {#if settable}
            <Button
              type="button"
              variant="ghost"
              class="mr-auto"
              onclick={() => $removeCover.mutate()}
              disabled={inFlight}
            >
              Remove cover
            </Button>
          {/if}
          <Button type="button" variant="ghost" onclick={close} disabled={inFlight}>Cancel</Button>
          <Button type="submit" disabled={!ready || inFlight}>
            {#if $setCover.isPending}
              <LoaderCircle size={13} class="animate-spin" /> Saving
            {:else}
              Save
            {/if}
          </Button>
        </div>
      </form>
    </div>
  </div>
{/if}

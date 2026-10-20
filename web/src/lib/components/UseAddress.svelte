<script lang="ts">
  import { useQueryClient } from '@tanstack/svelte-query';
  import { api, type SourceTrack } from '$lib/api';
  import Button from '$lib/components/Button.svelte';
  import ErrorNote from '$lib/components/ErrorNote.svelte';
  import { clock } from '$lib/review';

  let {
    targetId,
    entryTitle,
    entryArtist,
    entryDurationMs,
    open = $bindable(false),
    trigger = true
  }: {
    targetId: string;
    entryTitle: string;
    entryArtist: string;
    entryDurationMs: number | null | undefined;
    open?: boolean;
    /** Off where the caller draws its own control for `open`, as Review
     *  does in its answer bar. */
    trigger?: boolean;
  } = $props();

  const queryClient = useQueryClient();
  let field = $state<HTMLInputElement | undefined>();

  // The click leaves focus on the button, and autofocus on an element drawn
  // after the page loaded did not move it in Chrome.
  $effect(() => {
    if (open && field) field.focus();
  });
  let address = $state('');
  let lookup = $state<SourceTrack | undefined>();
  let lookupError = $state<Error | undefined>();
  let lookingUp = $state(false);
  let using = $state(false);
  let useError = $state<Error | undefined>();

  function lengthDiffers() {
    return entryDurationMs != null && lookup?.durationMs != null
      ? Math.abs(entryDurationMs - lookup.durationMs) > 5_000
      : false;
  }

  async function lookUp() {
    const url = address.trim();
    if (!url) return;
    lookingUp = true;
    lookupError = undefined;
    lookup = undefined;
    try {
      lookup = await api.lookupSourceTrack(url);
    } catch (error) {
      lookupError = error as Error;
    } finally {
      lookingUp = false;
    }
  }

  async function useAddress() {
    if (!lookup) return;
    using = true;
    useError = undefined;
    try {
      await api.keyTargetToSource(targetId, { url: lookup.url, externalId: lookup.externalId });
      await Promise.all([
        queryClient.invalidateQueries({ queryKey: ['wants'] }),
        queryClient.invalidateQueries({ queryKey: ['playlists'] }),
        queryClient.invalidateQueries({ queryKey: ['review-queue'] })
      ]);
    } catch (error) {
      useError = error as Error;
    } finally {
      using = false;
    }
  }
</script>

<!-- A link in the line under a row. Opened, the link stays where it is and
     the form takes the next line of the same row, at a width an address
     needs. -->
{#if trigger}
  <button
    type="button"
    aria-expanded={open}
    aria-controls="source-address-form-{targetId}"
    class="cursor-pointer text-meta font-medium text-ink-2 underline decoration-rule-strong underline-offset-[3px] hover:text-ink hover:decoration-[var(--color-ink)]"
    onclick={() => (open = !open)}
  >
    Use link
  </button>
{/if}
{#if open}
  <div
    id="source-address-form-{targetId}"
    class="mt-1 flex w-full max-w-2xl basis-full flex-col gap-2 border border-rule px-3 py-2"
  >
    <div class="flex flex-wrap items-center gap-2">
      <label class="sr-only" for="source-address-{targetId}">Track link on SoundCloud, YouTube or Bandcamp</label>
      <input
        id="source-address-{targetId}"
        bind:value={address}
        type="url"
        bind:this={field}
        placeholder="SoundCloud, YouTube or Bandcamp link"
        class="min-w-0 flex-1 border border-rule bg-inset px-2.5 py-1.5 font-mono text-meta text-ink outline-none focus:border-rule-strong"
        onkeydown={(event) => {
          if (event.key === 'Enter') void lookUp();
          if (event.key === 'Escape') open = false;
        }}
      />
      <Button variant="outline" size="xs" disabled={!address.trim() || lookingUp} onclick={lookUp}>
        {lookingUp ? 'Looking up…' : 'Look up'}
      </Button>
    </div>

    {#if lookup}
      <div class="grid gap-2 bg-panel px-2.5 py-2 text-meta sm:grid-cols-2">
        <div>
          <span class="block text-ink-3">Song</span>
          <span class="block truncate text-ink">{entryTitle} · {entryArtist}</span>
          <span class="numeric text-ink-3">{entryDurationMs != null ? clock(entryDurationMs / 1000) : '—'}</span>
        </div>
        <div>
          <span class="block text-ink-3">Link</span>
          <span class="block truncate text-ink">{lookup.title} · {lookup.artist}</span>
          <span class="numeric text-ink-3">
            {lookup.durationMs != null ? clock(lookup.durationMs / 1000) : '—'}
            {#if lengthDiffers()} · over 5s off{/if}
          </span>
        </div>
      </div>
      <Button disabled={using} onclick={useAddress}>{using ? 'Using…' : 'Use link'}</Button>
    {/if}

    {#if lookupError}
      <ErrorNote error={lookupError} bare />
    {/if}
    {#if useError}
      <ErrorNote error={useError} bare />
    {/if}
  </div>
{/if}

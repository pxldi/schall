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
    entryDurationMs
  }: {
    targetId: string;
    entryTitle: string;
    entryArtist: string;
    entryDurationMs: number | null | undefined;
  } = $props();

  const queryClient = useQueryClient();
  let open = $state(false);
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

<details bind:open={open} class="rounded-row border border-line-thin px-3 py-2">
  <summary class="cursor-pointer list-none text-meta font-medium text-ink-2 [&::-webkit-details-marker]:hidden">
    Use address
  </summary>
  {#if open}
    <div class="mt-2 flex flex-col gap-2">
      <div class="flex flex-wrap items-center gap-2">
        <label class="sr-only" for="source-address-{targetId}">Track address</label>
        <input
          id="source-address-{targetId}"
          bind:value={address}
          type="url"
          placeholder="Track address"
          class="min-w-0 flex-1 rounded-control border border-line-regular bg-inset px-2.5 py-1.5 font-mono text-meta text-ink outline-none focus:border-line-thick"
          onkeydown={(event) => event.key === 'Enter' && void lookUp()}
        />
        <Button variant="outline" size="xs" disabled={!address.trim() || lookingUp} onclick={lookUp}>
          {lookingUp ? 'Looking up…' : 'Look up'}
        </Button>
      </div>

      {#if lookup}
        <div class="grid gap-2 rounded-row bg-surface-regular px-2.5 py-2 text-meta sm:grid-cols-2">
          <div>
            <span class="block text-ink-3">Entry</span>
            <span class="block truncate text-ink">{entryTitle} · {entryArtist}</span>
            <span class="numeric text-ink-3">{entryDurationMs != null ? clock(entryDurationMs / 1000) : '—'}</span>
          </div>
          <div>
            <span class="block text-ink-3">Address</span>
            <span class="block truncate text-ink">{lookup.title} · {lookup.artist}</span>
            <span class="numeric text-ink-3">
              {lookup.durationMs != null ? clock(lookup.durationMs / 1000) : '—'}
              {#if lengthDiffers()} · length differs by more than 5 seconds{/if}
            </span>
          </div>
        </div>
        <Button disabled={using} onclick={useAddress}>{using ? 'Using…' : 'Use address'}</Button>
      {/if}

      {#if lookupError}
        <ErrorNote error={lookupError} bare />
      {/if}
      {#if useError}
        <ErrorNote error={useError} bare />
      {/if}
    </div>
  {/if}
</details>

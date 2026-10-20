<script lang="ts">
  import Icon from './Icon.svelte';
  import { createMutation, createQuery, keepPreviousData, useQueryClient } from '@tanstack/svelte-query';
  import { tick, untrack } from 'svelte';
  import { toStore } from 'svelte/store';
  import { fade } from 'svelte/transition';
  import { api, type LabelSearchResult } from '$lib/api';
  import { glide, motionMs } from '$lib/motion.svelte';
  import Button from './Button.svelte';
  import CyclingPlaceholder from './CyclingPlaceholder.svelte';
  import ErrorNote from './ErrorNote.svelte';
  import SkeletonRows from './SkeletonRows.svelte';

  let { open = $bindable(false) }: { open: boolean } = $props();

  const queryClient = useQueryClient();
  let labelName = $state('');
  // Lives out here rather than inside the field, because the box is destroyed
  // when the panel closes and the cycle must not start again on reopening.
  let hasTyped = $state(false);
  let debouncedLabelName = $state('');
  let selectedLabel = $state<LabelSearchResult | null>(null);
  let debounceTimer: ReturnType<typeof setTimeout>;

  // Nothing on screen reads these; they are the handles the panel needs to move
  // focus and to trap it.
  let panel = $state<HTMLElement | null>(null);
  let searchField = $state<HTMLInputElement | null>(null);

  // The term is in the key, so each one is its own question. See
  // FollowArtistModal for why: a refetch under a single key joins whatever
  // request is already running instead of starting a new one.
  const labelSearch = createQuery(
    toStore(() => ({
      queryKey: ['label-search', debouncedLabelName],
      queryFn: () => api.searchLabels(debouncedLabelName),
      enabled: debouncedLabelName.length >= 2,
      staleTime: 5 * 60 * 1000,
      // The last answer stays while the next term is asked, so the list does
      // not drop to one line and grow back on every word typed.
      placeholderData: keepPreviousData
    }))
  );

  $effect(() => {
    clearTimeout(debounceTimer);
    const query = labelName.trim();
    if (query.length < 2 || selectedLabel) {
      debouncedLabelName = '';
      return;
    }
    debounceTimer = setTimeout(() => {
      debouncedLabelName = query;
    }, 300);
    return () => clearTimeout(debounceTimer);
  });

  const followLabel = createMutation({
    mutationFn: (label: LabelSearchResult) => api.followLabel(label),
    onSuccess: async () => {
      close();
      await Promise.all([
        queryClient.invalidateQueries({ queryKey: ['labels'] }),
        queryClient.invalidateQueries({ queryKey: ['dashboard'] })
      ]);
    }
  });

  // Only a follow already sent refuses anything. A search still returning locks
  // nothing: it is cancelled by closing.
  const inFlight = $derived($followLabel.isPending);
  const candidates = $derived($labelSearch.data?.items ?? []);
  const showingCandidates = $derived(debouncedLabelName.length >= 2 && !selectedLabel);

  // The cursor over the candidates (ADR 0042): Up and Down in the field move one
  // inverted row, and Enter takes it. Without a cursor Enter submits, as before.
  let candidateAt = $state(-1);
  $effect(() => {
    void debouncedLabelName;
    candidateAt = -1;
  });

  function fieldKeys(event: KeyboardEvent) {
    if (!showingCandidates || candidates.length === 0) return;
    if (event.key === 'ArrowDown' || event.key === 'ArrowUp') {
      event.preventDefault();
      const by = event.key === 'ArrowDown' ? 1 : -1;
      candidateAt = candidateAt < 0 ? (by > 0 ? 0 : candidates.length - 1) : Math.min(Math.max(candidateAt + by, 0), candidates.length - 1);
      void tick().then(() =>
        panel?.querySelector('[data-cursor="true"]')?.scrollIntoView?.({ block: 'nearest' })
      );
    } else if (event.key === 'Enter' && candidateAt >= 0) {
      event.preventDefault();
      select(candidates[candidateAt]);
    }
  }

  // Opening takes the page away and gives it back, the same arrangement
  // FollowArtistModal uses: scroll locked with its gutter kept, the field
  // takes focus, and on the way out focus goes back where it came from.
  $effect(() => {
    if (!open) return;

    const opener = document.activeElement;
    const body = document.body;
    const overflow = body.style.overflow;
    const gutterRoom = body.style.paddingRight;
    const gutter = window.innerWidth - document.documentElement.clientWidth;

    body.style.overflow = 'hidden';
    if (gutter > 0) body.style.paddingRight = `${gutter}px`;
    untrack(() => searchField)?.focus();

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

  function submit(event: SubmitEvent) {
    event.preventDefault();
    if (selectedLabel) $followLabel.mutate(selectedLabel);
  }

  function updateName(event: Event) {
    labelName = (event.currentTarget as HTMLInputElement).value;
    selectedLabel = null;
    $followLabel.reset();
  }

  function select(label: LabelSearchResult) {
    selectedLabel = label;
    labelName = label.name;
    debouncedLabelName = '';
    $followLabel.reset();
  }

  function close() {
    open = false;
    labelName = '';
    debouncedLabelName = '';
    selectedLabel = null;
    $followLabel.reset();
  }
</script>

<svelte:window onkeydown={keydown} />

{#if open}
  <div
    class="scrim z-50 flex items-center justify-center p-6"
    role="presentation"
    onmousedown={scrimPress}
    transition:fade={{ duration: motionMs('surface') }}
  >
    <div
      class="flex max-h-full w-full max-w-md flex-col overflow-hidden overlay-panel"
      role="dialog"
      aria-modal="true"
      aria-labelledby="follow-label-title"
      tabindex="-1"
      bind:this={panel}
    >
      <form class="flex min-h-0 flex-1 flex-col" onsubmit={submit}>
        <div class="flex flex-none items-center gap-3 border-b border-rule px-4 py-3.5">
          <h2 id="follow-label-title" class="text-lead font-semibold">Follow label</h2>
          {#if showingCandidates && candidates.length > 0}
            <span class="text-meta text-ink-3">
              {candidates.length}
              {candidates.length === 1 ? 'candidate' : 'candidates'}
            </span>
          {/if}
          <button
            class="ml-auto grid size-6 flex-none place-items-center text-ink-3 transition hover:bg-raised hover:text-ink disabled:opacity-50"
            type="button"
            onclick={close}
            disabled={inFlight}
            aria-label="Close"
          >
            <Icon name="close" size={18} />
          </button>
        </div>

        <div class="min-h-0 flex-1 overflow-y-auto px-4 pb-3.5 pt-3">
          <label for="label-name" class="label">Search MusicBrainz</label>
          <CyclingPlaceholder class="mt-2 w-full" value={labelName} trailing bind:typed={hasTyped}>
            <input
              id="label-name"
              value={labelName}
              oninput={updateName}
              onkeydown={fieldKeys}
              maxlength="200"
              autocomplete="off"
              class="field trailing w-full"
              bind:this={searchField}
            />
            {#if $labelSearch.isFetching}
              <Icon name="busy"
                class="pointer-events-none absolute right-2.5 top-1/2 z-2 -translate-y-1/2 animate-spin text-busy"
                size={14} />
            {:else if selectedLabel}
              <Icon name="check"
                class="pointer-events-none absolute right-2.5 top-1/2 z-2 -translate-y-1/2 text-ok"
                size={14} />
            {/if}
          </CyclingPlaceholder>

          {#if showingCandidates}
            <div class="mt-2">
              {#if $labelSearch.isError}
                <div class="px-3 py-4">
                  <ErrorNote
                    error={$labelSearch.error}
                    retry={() => $labelSearch.refetch()}
                    action="Type the name again."
                    bare
                  />
                </div>
              {:else if $labelSearch.isPending || ($labelSearch.isPlaceholderData && !candidates.length)}
                <!-- Only the first search, or one after an empty answer, has no
                     rows to keep. -->
                <SkeletonRows count={4} label="Searching labels" />
              {:else if candidates.length}
                <div class="relative" use:glide={{ key: candidateAt }}>
                {#each candidates as result, index (result.musicbrainzId)}
                  {@const here = index === candidateAt}
                  <button
                    type="button"
                    class="row flex w-full items-center gap-3 px-3 py-1.5 text-left {here ? 'on-ink' : ''}"
                    data-cursor={here}
                    onclick={() => select(result)}
                  >
                    <span
                      class="grid size-4 shrink-0 place-items-center bg-raised text-micro font-semibold text-ink-2"
                    >
                      {result.name.slice(0, 1).toUpperCase()}
                    </span>
                    <span class="min-w-0">
                      <span class="block truncate text-body font-medium text-ink">
                        {result.name}
                      </span>
                      <span class="mt-0.5 block truncate text-meta text-ink-3">
                        {[result.type, result.area || result.country, result.disambiguation]
                          .filter(Boolean)
                          .join(' · ') || 'MusicBrainz label'}
                      </span>
                    </span>
                  </button>
                {/each}
              </div>
              {:else}
                <p class="px-3 py-4 text-center text-body text-ink-3">No matching labels</p>
              {/if}
            </div>
          {:else if selectedLabel}
            <p class="mt-2 text-meta text-ink-2">
              Selected {selectedLabel.name}{selectedLabel.disambiguation
                ? ` · ${selectedLabel.disambiguation}`
                : ''}
            </p>
          {/if}

          {#if $followLabel.isError}
            <ErrorNote error={$followLabel.error} class="mt-3" />
          {/if}
        </div>

        <div
          class="flex flex-none items-center justify-end gap-2 border-t border-rule px-4 py-3"
        >
          <Button type="button" variant="ghost" onclick={close} disabled={inFlight}>Cancel</Button>
          <Button type="submit" variant="primary" disabled={!selectedLabel || inFlight}>
            {#if inFlight}
              <Icon name="busy" size={13} class="animate-spin" /> Following
            {:else}
              Follow
            {/if}
          </Button>
        </div>
      </form>
    </div>
  </div>
{/if}

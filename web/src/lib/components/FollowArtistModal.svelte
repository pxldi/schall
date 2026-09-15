<script lang="ts">
  import { createMutation, createQuery, useQueryClient } from '@tanstack/svelte-query';
  import { untrack } from 'svelte';
  import { toStore } from 'svelte/store';
  import { fade } from 'svelte/transition';
  import { Check, LoaderCircle, X } from '@lucide/svelte';
  import { api, type ArtistSearchResult } from '$lib/api';
  import { motionMs } from '$lib/motion.svelte';
  import Button from './Button.svelte';
  import CyclingPlaceholder from './CyclingPlaceholder.svelte';
  import ErrorNote from './ErrorNote.svelte';

  let { open = $bindable(false) }: { open: boolean } = $props();

  const queryClient = useQueryClient();
  let artistName = $state('');
  // Lives out here rather than inside the field, because the box is destroyed
  // when the panel closes and the cycle must not start again on reopening.
  let hasTyped = $state(false);
  let debouncedArtistName = $state('');
  let selectedArtist = $state<ArtistSearchResult | null>(null);
  let debounceTimer: ReturnType<typeof setTimeout>;

  // Nothing on screen reads these; they are the handles the panel needs to move
  // focus and to trap it.
  let panel = $state<HTMLElement | null>(null);
  let searchField = $state<HTMLInputElement | null>(null);

  // The term is in the key, so each one is its own question. Under a single key
  // the settled term had to be searched for by refetching that key, and a
  // refetch while the previous term's request is still running joins that
  // request instead of starting a new one — so a second search typed a moment
  // later answered with the first term's artists. Two searches 300ms apart is
  // ordinary typing, not a corner.
  //
  // Asking is now the term itself arriving, which is why there is no refetch
  // below: the debounce sets the term and the query follows it.
  const artistSearch = createQuery(
    toStore(() => ({
      queryKey: ['artist-search', debouncedArtistName],
      queryFn: () => api.searchArtists(debouncedArtistName),
      enabled: debouncedArtistName.length >= 2,
      staleTime: 5 * 60 * 1000
    }))
  );

  $effect(() => {
    clearTimeout(debounceTimer);
    const query = artistName.trim();
    if (query.length < 2 || selectedArtist) {
      debouncedArtistName = '';
      return;
    }
    debounceTimer = setTimeout(() => {
      debouncedArtistName = query;
    }, 300);
    return () => clearTimeout(debounceTimer);
  });

  // Want what's missing, asked here because following somebody and asking for
  // their back catalogue are two decisions and this is the one moment both are
  // in front of the reader. Off by default: following says keep them complete
  // from here, and ordering a discography nobody asked for is a decision
  // nobody made.
  let wantMissing = $state(false);
  // Set when the follow landed and the standing want did not. The artist is
  // followed, so the note has to say that rather than reading as a failed
  // follow.
  let wantMissingFailed = $state(false);

  const followArtist = createMutation({
    mutationFn: async (artist: ArtistSearchResult) => {
      const followed = await api.followArtist(artist);
      if (!wantMissing) return followed;
      try {
        await api.setArtistWantMissing(followed.id, true);
      } catch (error) {
        // The follow happened, so the lists are told even though this failed.
        wantMissingFailed = true;
        await invalidate();
        throw error;
      }
      return followed;
    },
    onSuccess: async () => {
      close();
      await invalidate();
    }
  });

  function invalidate() {
    return Promise.all([
      queryClient.invalidateQueries({ queryKey: ['artists'] }),
      queryClient.invalidateQueries({ queryKey: ['dashboard'] }),
      queryClient.invalidateQueries({ queryKey: ['releases'] })
    ]);
  }

  // Only a follow already sent refuses anything. A search still returning locks
  // nothing: it is cancelled by closing.
  const inFlight = $derived($followArtist.isPending);
  const candidates = $derived($artistSearch.data?.items ?? []);
  const showingCandidates = $derived(debouncedArtistName.length >= 2 && !selectedArtist);

  // Opening takes the page away and gives it back: the scroll is locked with its
  // gutter kept so nothing shifts sideways, the field takes focus because the
  // task starts with typing, and on the way out focus goes back where it came
  // from. Nothing here is read by the template, so the whole arrangement lives
  // and dies with `open`.
  $effect(() => {
    if (!open) return;

    const opener = document.activeElement;
    const body = document.body;
    const overflow = body.style.overflow;
    const gutterRoom = body.style.paddingRight;
    const gutter = window.innerWidth - document.documentElement.clientWidth;

    body.style.overflow = 'hidden';
    if (gutter > 0) body.style.paddingRight = `${gutter}px`;
    // Untracked, or the field arriving would count as a reason to run all of
    // this again — and the run before it would have handed focus back to the
    // page on its way out.
    untrack(() => searchField)?.focus();

    return () => {
      body.style.overflow = overflow;
      body.style.paddingRight = gutterRoom;
      returnFocus(opener);
    };
  });

  // The control that opened the panel, unless following the artist re-rendered
  // the row it stood on. Then the page header takes it, which is where a reader
  // who has just changed the page would look anyway.
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

  // Dismissal follows mousedown, not mouseup: a click that starts inside the
  // panel and ends on the scrim was a selection being dragged, not a dismissal.
  function scrimPress(event: MouseEvent) {
    if (event.target !== event.currentTarget) return;
    if (!inFlight) close();
  }

  function submit(event: SubmitEvent) {
    event.preventDefault();
    if (selectedArtist) $followArtist.mutate(selectedArtist);
  }

  function updateName(event: Event) {
    artistName = (event.currentTarget as HTMLInputElement).value;
    selectedArtist = null;
    wantMissingFailed = false;
    $followArtist.reset();
  }

  function select(artist: ArtistSearchResult) {
    selectedArtist = artist;
    artistName = artist.name;
    debouncedArtistName = '';
    $followArtist.reset();
  }

  function close() {
    open = false;
    artistName = '';
    debouncedArtistName = '';
    selectedArtist = null;
    wantMissing = false;
    wantMissingFailed = false;
    $followArtist.reset();
  }
</script>

<svelte:window onkeydown={keydown} />

{#if open}
  <!-- The scrim is the elevation and the border is the edge: black at 55% with
       an 8px blur leaves the page behind present as shape and unreadable as
       text, and the panel is the frosted material the palette is made of, so
       nothing on this layer needs a shadow. Both fade in together over the
       surface step, the one every surface in the application opens at — no
       slide, no scale, and nothing at all under prefers-reduced-motion, which
       is what `motionMs` returns zero for. -->
  <div
    class="fixed inset-0 z-50 flex items-center justify-center bg-black/55 p-6 backdrop-blur-[8px]"
    role="presentation"
    data-scrim="dark-blurred"
    onmousedown={scrimPress}
    transition:fade={{ duration: motionMs('surface') }}
  >
    <!-- Centred while the panel and its air fit, and capped at the height that
         is left the moment they do not, so a tall list scrolls inside the panel
         rather than growing past the viewport after the page scrollbar it would
         have needed has been locked away. -->
    <div
      class="flex max-h-full w-full max-w-md flex-col overflow-hidden rounded-panel border border-[rgba(232,233,231,0.09)] bg-[rgba(32,34,39,0.55)] backdrop-blur-[14px]"
      role="dialog"
      aria-modal="true"
      aria-labelledby="follow-title"
      data-height="constrained"
      data-shadow="none"
      tabindex="-1"
      bind:this={panel}
    >
      <form class="flex min-h-0 flex-1 flex-col" onsubmit={submit}>
        <!-- Pinned, so the way out stays reachable at any height. -->
        <div class="flex flex-none items-center gap-3 border-b border-line-thin px-4 py-3.5">
          <h2 id="follow-title" class="font-display text-lead font-bold">
            Follow artist
          </h2>
          {#if showingCandidates && candidates.length > 0}
            <span class="text-meta text-ink-3">
              {candidates.length}
              {candidates.length === 1 ? 'candidate' : 'candidates'}
            </span>
          {/if}
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

        <!-- The only part that scrolls. -->
        <div class="min-h-0 flex-1 overflow-y-auto px-4 pb-3.5 pt-3">
          <label for="artist-name" class="label">Search MusicBrainz</label>
          <!-- No leading glyph: the label above already says what this searches,
               and a magnifier inside the box says it a second time in the space
               the value needs. The one glyph a field carries is the trailing
               one, and it is state. -->
          <CyclingPlaceholder class="mt-2 w-full" value={artistName} trailing bind:typed={hasTyped}>
            <input
              id="artist-name"
              value={artistName}
              oninput={updateName}
              maxlength="200"
              autocomplete="off"
              data-inset="component"
              data-trailing="true"
              class="field trailing w-full"
              bind:this={searchField}
            />
            {#if $artistSearch.isFetching}
              <LoaderCircle
                class="pointer-events-none absolute right-2.5 top-1/2 z-2 -translate-y-1/2 animate-spin text-busy"
                size={14}
              />
            {:else if selectedArtist}
              <Check
                class="pointer-events-none absolute right-2.5 top-1/2 z-2 -translate-y-1/2 text-ok"
                size={14}
              />
            {/if}
          </CyclingPlaceholder>

          <!-- The results are rows on hairlines, not cards: this is already
               inside a bordered dialog and a bordered list would be the second
               border. They scroll with the body rather than in a box of their
               own — one scrolling region to a panel. -->
          {#if showingCandidates}
            <div class="mt-2">
              {#if $artistSearch.isError}
                <!-- The search is a read, so asking again is the whole answer
                     and the note asks by itself. Bare: this is already inside a
                     bordered dialog, and a band here would be the second
                     surface. -->
                <div class="px-3 py-4">
                  <!-- Reloading is the wrong thing to name in a dialog: it
                       closes the dialog. The box the reader is already typing
                       in is the control that asks again. -->
                  <ErrorNote
                    error={$artistSearch.error}
                    retry={() => $artistSearch.refetch()}
                    action="Type the name again to search once more."
                    bare
                  />
                </div>
              {:else if $artistSearch.isPending}
                <p class="px-3 py-4 text-center text-body text-ink-3">Searching…</p>
              {:else if candidates.length}
                {#each candidates as result (result.musicbrainzId)}
                  <button
                    type="button"
                    class="flex w-full items-center gap-3 border-b border-line-thin px-3 py-2 text-left transition last:border-b-0 hover:bg-surface-thick focus:bg-surface-thick focus:outline-none"
                    onclick={() => select(result)}
                  >
                    <span
                      class="grid size-4 shrink-0 place-items-center rounded-row bg-ok/14 text-micro font-bold text-ok"
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
                          .join(' · ') || 'MusicBrainz artist'}
                      </span>
                    </span>
                  </button>
                {/each}
              {:else}
                <p class="px-3 py-4 text-center text-body text-ink-3">No matching artists</p>
              {/if}
            </div>
          {:else if selectedArtist}
            <p class="mt-2 text-meta text-ok">
              Selected {selectedArtist.name}{selectedArtist.disambiguation
                ? ` · ${selectedArtist.disambiguation}`
                : ''}
            </p>
          {/if}

          <!-- Following keeps an artist complete from here. This is the other
               decision: their back catalogue too, and every release whose
               tracklist arrives over the hours after. -->
          <label
            class="mt-3 flex w-fit cursor-pointer items-center gap-2 text-meta font-medium text-ink-2"
          >
            <input
              type="checkbox"
              checked={wantMissing}
              disabled={inFlight}
              onchange={(event) => (wantMissing = event.currentTarget.checked)}
              class="check cursor-pointer"
            />
            Want what's missing
          </label>

          <!-- The follow failed and there is no row and no form elsewhere to
               attach it to, so it stays here, in the panel, and the panel stays
               open. -->
          {#if $followArtist.isError}
            <ErrorNote
              error={$followArtist.error}
              action={wantMissingFailed
                ? 'The artist is followed. Press Want missing on their page.'
                : undefined}
              class="mt-3"
            />
          {/if}
        </div>

        <!-- Pinned, actions right, no prose. While the follow is in flight every
             way out is dimmed at once — close, Cancel, Escape and the scrim —
             which is what makes a dead Escape key read as a refusal rather than
             a bug. Reading is never refused: the body still scrolls, text still
             selects, focus still moves. -->
        <div
          class="flex flex-none items-center justify-end gap-2 border-t border-line-thin px-4 py-3"
        >
          <Button type="button" variant="ghost" onclick={close} disabled={inFlight}>Cancel</Button>
          <Button type="submit" disabled={!selectedArtist || inFlight}>
            {#if inFlight}
              <LoaderCircle size={13} class="animate-spin" /> Following
            {:else}
              Follow
            {/if}
          </Button>
        </div>
      </form>
    </div>
  </div>
{/if}

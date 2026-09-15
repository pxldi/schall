<script lang="ts">
  import { Check } from '@lucide/svelte';
  import type { ReleaseEdition } from '$lib/api';
  import ErrorNote from '$lib/components/ErrorNote.svelte';
  import Settle from '$lib/components/Settle.svelte';

  // An edition is one pressing of a record: the 1994 British CD, the 2011
  // reissue, the Japanese one with the extra track. MusicBrainz keeps them all
  // under one release group, and Schall works from exactly one of them — the
  // track list on screen, and every want counted against it, come from the
  // pressing selected here.
  //
  // They used to be a picker: a disclosure, a button inside it, and a scrolling
  // list that appeared after two presses. Choosing between pressings is a
  // comparison, and a comparison behind a control is not one, so every edition
  // is a block and every block is on screen. The facts sit in the same three
  // places in every block, which is what lets a reader run down the column and
  // see which pressing is the one they hold.
  //
  // Three facts, because three is what the editions endpoint carries: the date,
  // the country and the MusicBrainz status. It does not carry the label, the
  // catalogue number, the track count or the disc count, so this component
  // cannot show them and does not pretend to.
  let {
    editions,
    // The pressing Schall is working from, by its MusicBrainz release id.
    selectedId,
    // That same pressing as the release endpoint already describes it, which is
    // a different source from the list above and always answers. MusicBrainz is
    // a third party and the list can fail to arrive; the pressing in use is
    // drawn from this whatever happens to the list, because a page that cannot
    // say which pressing it is working from is worse than one that cannot offer
    // the alternatives.
    selected = null,
    loading = false,
    retryEditions,
    // Why the list could not be fetched, in the words the API used. Empty when
    // nothing went wrong.
    error,
    // False when this release is not linked to a MusicBrainz release group at
    // all — music somebody ripped themselves, which has no pressings to compare
    // and is not a failure.
    linked = true,
    // The extra facts the release endpoint carries about the selected pressing
    // only, so they can be shown on its block and nowhere else.
    barcode = '',
    selectionReason = '',
    // A choice is being written. Every block stops taking presses until it
    // settles, because two choices in flight would race for the same track list.
    pending = false,
    selectError,
    onselect
  }: {
    editions: ReleaseEdition[];
    selectedId: string | null;
    selected?: ReleaseEdition | null;
    loading?: boolean;
    /** Whatever the edition list failed with. Handed on rather than flattened
     * to a sentence: the words a reader gets are chosen from the status and the
     * server's own title. */
    error?: unknown;
    /** How to ask MusicBrainz for the list again. The list is read, never
     * written, so asking again is the whole of the answer and nobody is handed
     * a sentence whose only action is to reload the page themselves. */
    retryEditions?: () => void;
    linked?: boolean;
    barcode?: string;
    selectionReason?: string;
    pending?: boolean;
    selectError?: unknown;
    onselect: (musicbrainzReleaseId: string) => void;
  } = $props();

  // The blocks on screen, which are the fetched list unless the pressing in use
  // is not in it. That happens two ways: the list did not arrive, and the list
  // arrived without the pressing Schall is working from. Both put it first,
  // from the release's own facts, so the question "which one is this" is always
  // answered on the page.
  const blocks = $derived.by(() => {
    if (!selected) return editions;
    if (editions.some((item) => item.musicbrainzReleaseId === selected.musicbrainzReleaseId)) {
      return editions;
    }
    return [selected, ...editions];
  });

  // A fact MusicBrainz has no answer for is said to be missing rather than left
  // blank. A blank cell in a column of dates reads as a page that failed to
  // draw; the words read as what they are.
  const facts = (edition: ReleaseEdition) => [
    { name: 'Released', value: edition.releaseDate ?? '' },
    { name: 'Country', value: edition.country ?? '' },
    { name: 'Status', value: edition.status ?? '' }
  ];

</script>

<section class="mt-8">
  <div class="flex flex-wrap items-baseline justify-between gap-3">
    <span class="label">Editions</span>
    {#if blocks.length > 1}
      <span class="text-meta text-ink-4">Choose the pressing you hold</span>
    {/if}
  </div>

  <div class="mt-3 max-h-[calc(100dvh-27rem)] overflow-hidden rounded-panel border border-line-thin">
    <Settle pending={loading}>
      {#snippet placeholder()}
        <div
        class="flex flex-col"
        role="status"
        aria-label="Loading editions"
      >
        {#each Array(40) as _, placeholderIndex (placeholderIndex)}
          <div
            class="h-28 border-b border-line-thin px-4 py-3 last:border-b-0"
            aria-hidden="true"
          >
            <div class="h-full animate-pulse rounded-row bg-surface-regular"></div>
          </div>
        {/each}
        </div>
      {/snippet}
    {#if !linked}
      <p class="px-4 py-5 text-body text-ink-2">
        This release is not linked to MusicBrainz, so there are no other pressings to compare.
      </p>
    {:else if !blocks.length}
      {#if error}
        <!-- The list is what failed, so the note names the list. Without a
             subject the catch-all can only say that something did not finish,
             which on a page of six panels does not say which one. -->
        <div class="px-4 py-5">
          <ErrorNote
            {error}
            fallback="The other pressings of this release could not be read."
            retry={retryEditions}
          />
        </div>
      {:else}
        <p class="px-4 py-5 text-body text-ink-2">MusicBrainz lists no pressings of this release.</p>
      {/if}
    {:else}
      {#each blocks as edition, index (edition.musicbrainzReleaseId)}
        {@const selected = edition.musicbrainzReleaseId === selectedId}
        <!-- The selected block keeps the surface every other block sits on. A
             raised fill would put its quiet lines on `thick`, where ink-4 falls
             under the contrast floor, and the tick and the word beside the
             title already say which one is chosen. -->
        <div
          class="min-h-28 px-4 py-3 {index ? 'border-t border-line-thin' : ''}"
          aria-current={selected ? 'true' : undefined}
          data-edition="true"
        >
          <div class="flex flex-wrap items-start justify-between gap-3">
            <!-- The title is what identifies a pressing, so it wraps. -->
            <p class="min-w-0 flex-1 text-body font-medium break-words text-ink">
              {edition.title}
            </p>
            {#if selected}
              <span class="flex shrink-0 items-center gap-1.5 text-meta text-ok">
                <Check size={13} strokeWidth={3} /> Selected
              </span>
            {:else}
              <button
                class="tap shrink-0 rounded-control border border-line-thin px-2.5 py-1 text-meta text-ink-2 transition hover:bg-surface-thick hover:text-ink disabled:opacity-50"
                disabled={pending}
                onclick={() => onselect(edition.musicbrainzReleaseId)}
              >
                Use this
              </button>
            {/if}
          </div>

          <dl class="mt-2 flex flex-wrap gap-x-8 gap-y-2">
            {#each facts(edition) as fact (fact.name)}
              <div class="flex min-w-20 flex-col gap-0.5">
                <dt class="label">{fact.name}</dt>
                {#if fact.value}
                  <dd class="numeric text-meta text-ink-2">{fact.value}</dd>
                {:else}
                  <!-- ink-3 rather than ink-4: this line has to stay legible
                       when the block is hovered onto the thick surface. -->
                  <dd class="text-meta text-ink-3">Not recorded</dd>
                {/if}
              </div>
            {/each}
            {#if selected && barcode}
              <div class="flex min-w-20 flex-col gap-0.5">
                <dt class="label">Barcode</dt>
                <dd class="numeric text-meta text-ink-2">{barcode}</dd>
              </div>
            {/if}
          </dl>

          {#if selected && selectionReason}
            <p class="mt-2 text-meta text-ink-3">Picked automatically: {selectionReason}.</p>
          {/if}
        </div>
      {/each}
      <!-- The pressing in use is on screen and the rest are not. Said once, at
           the foot of the list, rather than in place of it. -->
      {#if error}
        <div class="border-t border-line-thin px-4 py-3">
          <ErrorNote
            {error}
            bare
            fallback="The other pressings of this release could not be read."
            retry={retryEditions}
          />
        </div>
      {/if}
    {/if}
    </Settle>
  </div>

  <!-- The answer under the control that asked for it. -->
  {#if selectError}
    <ErrorNote error={selectError} class="mt-3" />
  {/if}
</section>

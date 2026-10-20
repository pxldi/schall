<script lang="ts">
  import type { Release } from '$lib/api';
  import { calendarDate } from '$lib/utils';
  import Cover from '$lib/components/Cover.svelte';
  import Icon from '$lib/components/Icon.svelte';
  import OwnedBar from '$lib/components/OwnedBar.svelte';
  import WantToggle from '$lib/components/WantToggle.svelte';

  // One release in a section of an artist's page: a small cover, the title,
  // the year, and how much of it the library holds as the bar and the
  // fraction. A state that has no fraction to draw (no track list, a failed
  // read, everything dismissed) is its glyph, with the words as the tooltip.
  // A bin means somebody removed it and its files are waiting to be deleted.
  // The + shows on the hovered or cursor row, as on a row of tracks.
  //
  // The link covers the row through its ::after, and the + sits above it, so
  // the row is one target and the button is not inside the anchor.

  const COLUMNS = 'grid-cols-[1.75rem_minmax(0,1fr)_9rem_11rem_1.5rem]';

  let {
    release,
    meta,
    state,
    tile,
    owned,
    countable,
    wanted,
    wantable,
    here = false,
    onwant,
    onunwant,
    onfocus
  }: {
    release: Release;
    /** The year, and for Other the kind. */
    meta: string;
    /** The state in words, for the tooltip. */
    state: string;
    tile: 'owned' | 'partial' | 'missing' | 'dismissed' | 'unknown' | 'busy' | 'failed';
    owned: number;
    countable: number;
    wanted: boolean;
    /** Whether the release has anything left to want. */
    wantable: boolean;
    /** Whether the page's cursor is on this row. */
    here?: boolean;
    onwant: () => unknown;
    onunwant: () => unknown;
    onfocus?: () => void;
  } = $props();

  const leaving = $derived(
    release.leavingTrackCount > 0 && release.removesAt
      ? `Deleted on ${calendarDate(release.removesAt)}`
      : ''
  );
</script>

<div
  class="row want-row scroll-mt-24 scroll-mb-12 grid {COLUMNS} items-center gap-x-3 px-5 {here
    ? 'on-ink'
    : ''}"
  data-release={release.id}
  data-cursor={here}
  data-held={owned > 0 ? 'some' : 'none'}
  onfocusin={onfocus}
>
  <span class="block size-7 overflow-hidden rounded-cover bg-raised">
    <!-- A release with nothing held is faded so the ones in the library stand
         out. Only cached covers are asked for, as everywhere a page draws many. -->
    <span class="block size-full {owned === 0 ? 'opacity-[0.38]' : ''}">
      <Cover
        src={release.hasCover ? `/api/v1/albums/${release.id}/cover?cached=1` : undefined}
        seed={release.id}
        class="size-full object-cover"
      />
    </span>
  </span>

  <a
    href={`/releases/${release.id}`}
    class="truncate text-body after:absolute after:inset-0 {here ? 'font-medium' : owned > 0 ? '' : 'text-ink-3'}"
    title={`${release.title} · ${leaving || state}`}>{release.title}</a
  >

  <span class="mono truncate text-meta {here ? '' : 'text-ink-3'}">{meta}</span>

  <span class="flex min-w-0 items-center gap-2">
    {#if leaving}
      <span class="flex" title={leaving}>
        <Icon name="delete" size="sm" aria-label={leaving} />
      </span>
    {/if}
    {#if tile === 'failed'}
      <span class="flex text-fail" title={state}>
        <Icon name="close" size="sm" /><span class="sr-only">{state}</span>
      </span>
    {:else if tile === 'busy'}
      <span class="flex text-busy" title={state}>
        <Icon name="busy" size="sm" class="animate-spin" /><span class="sr-only">{state}</span>
      </span>
    {:else if tile === 'dismissed'}
      <span class="flex" title={state}>
        <Icon name="dismiss" size="sm" /><span class="sr-only">{state}</span>
      </span>
    {:else if tile === 'unknown' || countable <= 0}
      <span class="flex text-idle" title={state}>
        <Icon name="minus" size="sm" /><span class="sr-only">{state}</span>
      </span>
    {:else}
      <OwnedBar {owned} total={countable} noun="tracks" compact />
    {/if}
  </span>

  <span class="relative z-[2] flex items-center justify-center">
    {#if wantable}
      <WantToggle
        reveal={owned > 0 ? 'all' : true}
        {wanted}
        title={release.title}
        {onwant}
        {onunwant}
      />
    {/if}
  </span>
</div>

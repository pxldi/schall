<script lang="ts">
  import type { Release } from '$lib/api';
  import { calendarDate } from '$lib/utils';
  import Cover from '$lib/components/Cover.svelte';
  import Icon from '$lib/components/Icon.svelte';
  import WantToggle from '$lib/components/WantToggle.svelte';

  // One release in a row of an artist's page: a small cover with the title
  // under it. What the library holds is printed on the cover, a tick for all of
  // it and "2/14" for some, and a bin when somebody removed it and its files
  // are waiting to be deleted. The + sits on the cover's corner and shows on
  // hover, as on a row of tracks. A wanted release keeps its tick, unless the
  // library holds some of it: the cover says that already.

  let {
    release,
    meta,
    state,
    owned,
    countable,
    wanted,
    wantable,
    onwant,
    onunwant
  }: {
    release: Release;
    /** The line under the title: the year, and for Other the kind. */
    meta: string;
    /** The state in words, for the tooltip. */
    state: string;
    owned: number;
    countable: number;
    wanted: boolean;
    /** Whether the release has anything left to want. */
    wantable: boolean;
    onwant: () => unknown;
    onunwant: () => unknown;
  } = $props();

  const complete = $derived(countable > 0 && owned >= countable);
  const partial = $derived(owned > 0 && owned < countable);
  const leaving = $derived(
    release.leavingTrackCount > 0 && release.removesAt
      ? `Deleted on ${calendarDate(release.removesAt)}`
      : ''
  );
</script>

<div class="want-tile relative flex w-19 shrink-0 flex-col" data-held={owned > 0 ? 'some' : 'none'}>
  <a
    href={`/releases/${release.id}`}
    class="group flex flex-col gap-1 rounded-tight"
    title={`${release.title} · ${leaving || state}`}
  >
    <span
      class="relative block size-19 overflow-hidden rounded-row bg-surface-thick transition-transform duration-200 group-hover:-translate-y-0.5"
    >
      <!-- A release with nothing held is faded so the ones in the library
           stand out. Only cached covers are asked for, as everywhere a page
           draws many (ADR Duoton). -->
      <span class="block size-full {owned === 0 ? 'opacity-[0.38]' : ''}">
        <Cover
          src={release.hasCover ? `/api/v1/albums/${release.id}/cover?cached=1` : undefined}
          seed={release.id}
          inks={release.inks}
          class="size-full object-cover"
        />
      </span>
      {#if leaving}
        <span
          class="absolute right-1 bottom-1 grid size-4.5 place-items-center rounded-pill bg-ground/80 text-ink-2"
          aria-label={leaving}
        >
          <Icon name="delete" size="sm" class="size-3" />
        </span>
      {:else if complete}
        <span
          class="absolute right-1 bottom-1 grid size-4.5 place-items-center rounded-pill bg-ground/80 text-ok [--plate:currentColor]"
          aria-label="Complete"
        >
          <Icon name="check" size="sm" class="size-3" />
        </span>
      {:else if partial}
        <span
          class="numeric absolute right-1 bottom-1 rounded-pill bg-ground/80 px-1.5 py-px text-[0.625rem] font-semibold text-ink"
          aria-label={`${owned} of ${countable}`}>{owned}/{countable}</span
        >
      {/if}
    </span>
    <span class="line-clamp-2 text-[0.71875rem] leading-tight font-bold tracking-[-0.01em] {owned > 0 ? 'text-ink' : 'text-ink-2'}">
      {release.title}
    </span>
    <span class="numeric -mt-0.5 truncate text-[0.65625rem] text-ink-3">{meta}</span>
  </a>
  {#if wantable}
    <WantToggle
      reveal={owned > 0 ? 'all' : true}
      {wanted}
      title={release.title}
      {onwant}
      {onunwant}
      class="absolute top-1 right-1 bg-ground/70 backdrop-blur-sm"
    />
  {/if}
</div>

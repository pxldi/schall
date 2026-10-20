<script lang="ts">
  import Skeleton from '$lib/components/Skeleton.svelte';
  import Settle from '$lib/components/Settle.svelte';
  import { createQuery, useQueryClient } from '@tanstack/svelte-query';
  import { api, type Overview, type OverviewDay } from '$lib/api';
  import { formatBytes, relativeTime } from '$lib/utils';
  import type { OverviewSession } from '$lib/api-types';
  import Cover from '$lib/components/Cover.svelte';
  import Icon from '$lib/components/Icon.svelte';
  import type { IconName } from '$lib/icons';
  import WantToggle from '$lib/components/WantToggle.svelte';
  import ErrorNote from '$lib/components/ErrorNote.svelte';
  import PageHeader from '$lib/components/PageHeader.svelte';

  // The first screen: mostly what was listened to, and a little on how much
  // music arrived. Every figure comes from one read, counted on the server in
  // this browser's zone, because "21:00" means nothing in UTC to somebody in
  // Vienna.
  const zone = Intl.DateTimeFormat().resolvedOptions().timeZone || 'UTC';
  const queryClient = useQueryClient();
  const overview = createQuery({
    queryKey: ['overview', zone],
    queryFn: () => api.overview(zone),
    // Read once when the page opens, newest arrival included. Re-reading every
    // 15 s while something downloaded moved the lists under the reader.
    refetchInterval: false
  });

  // A played song the library does not hold is wanted and let go with the
  // same + as a track row. Wanting one somebody let go of hands back the
  // standing decision, so the press takes it back explicitly.
  async function wantSong(trackId: string) {
    const target = await api.wantTrack(trackId);
    if (target.status === 'not_wanted') await api.pursueTargetAgain(target.id);
    await queryClient.invalidateQueries({ queryKey: ['overview'] });
  }
  async function letGo(trackId: string) {
    await api.removeTrack(trackId);
    await queryClient.invalidateQueries({ queryKey: ['overview'] });
  }

  const data = $derived($overview.data);
  const listening = $derived(data?.listening);

  // ---- figures --------------------------------------------------------------

  const change = $derived.by(() => {
    if (!listening) return null;
    const { total, previous } = listening.listens;
    if (previous === 0) return null;
    return Math.round(((total - previous) / previous) * 100);
  });

  const fileCount = $derived(data?.library.fileCount ?? 0);
  const roomLeft = $derived.by(() => {
    const storage = data?.library.storage;
    if (!storage || storage.totalBytes === 0) return null;
    return {
      free: storage.totalBytes - storage.usedBytes,
      usedShare: Math.min(100, Math.round((storage.usedBytes / storage.totalBytes) * 100))
    };
  });

  const WEEKDAYS = ['Mon', 'Tue', 'Wed', 'Thu', 'Fri', 'Sat', 'Sun'];
  // The ramp runs from the inset colour to the ink. How often somebody listens
  // at an hour is not a state, so it has no state colour.
  const HEAT = [0, 18, 34, 52, 74, 100].map((share) =>
    share === 0
      ? 'var(--color-inset)'
      : `color-mix(in srgb, var(--color-ink) ${share}%, var(--color-inset))`
  );
  const heatMax = $derived(Math.max(1, ...(listening?.whenYouListen.cells.flat() ?? [0])));
  // The first hour with the most listens carries a ring, so the eye finds the
  // peak without reading the line above the grid.
  const heatPeak = $derived(listening?.whenYouListen.cells.flat().indexOf(heatMax) ?? -1);
  function heatColour(count: number): string {
    if (count === 0) return HEAT[0];
    const step = Math.ceil((count / heatMax) * (HEAT.length - 1));
    return HEAT[Math.min(HEAT.length - 1, Math.max(1, step))];
  }

  // ---- dates ----------------------------------------------------------------

  const MONTHS = ['Jan', 'Feb', 'Mar', 'Apr', 'May', 'Jun', 'Jul', 'Aug', 'Sep', 'Oct', 'Nov', 'Dec'];
  function dayLabel(date: string): string {
    const [, month, day] = date.split('-').map(Number);
    return `${day} ${MONTHS[month - 1]}`;
  }

  // ---- sessions ---------------------------------------------------------

  // "11:03–11:18" today, "Yesterday 21:47–22:27", else "Sat 6 Sep 21:47–22:27",
  // in the browser's zone. A session still going ends at "now".
  function sessionRange(session: OverviewSession): string {
    const start = new Date(session.startedAt);
    const end = new Date(session.endedAt);
    const clock = (at: Date) => `${String(at.getHours()).padStart(2, '0')}:${String(at.getMinutes()).padStart(2, '0')}`;
    const day = sessionDay(start);
    // One song, or several inside a minute, is a moment rather than a span.
    const until = session.ongoing ? 'now' : clock(end);
    const range = until === clock(start) ? clock(start) : `${clock(start)}–${until}`;
    return `${day ? day + ' ' : ''}${range}`;
  }
  function sessionDay(at: Date): string {
    const midnight = (of: Date) => new Date(of.getFullYear(), of.getMonth(), of.getDate()).getTime();
    const daysAgo = Math.round((midnight(new Date()) - midnight(at)) / 86_400_000);
    if (daysAgo === 0) return '';
    if (daysAgo === 1) return 'Yesterday';
    return `${WEEKDAYS[(at.getDay() + 6) % 7]} ${at.getDate()} ${MONTHS[at.getMonth()]}`;
  }
  // The first three names, then how many more.
  function artistsLine(artists: string[]): string {
    const shown = artists.slice(0, 3).join(', ');
    const more = artists.length - 3;
    return more > 0 ? `${shown} + ${more} more` : shown;
  }
  function weekdayOf(date: string): string {
    const [year, month, day] = date.split('-').map(Number);
    return WEEKDAYS[(new Date(Date.UTC(year, month - 1, day)).getUTCDay() + 6) % 7];
  }
  function monthLabel(month: string): string {
    const [year, m] = month.split('-').map(Number);
    return `${['Jan', 'Feb', 'Mar', 'Apr', 'May', 'Jun', 'Jul', 'Aug', 'Sep', 'Oct', 'Nov', 'Dec'][m - 1]} ${year}`;
  }
  function hourLabel(hour: number): string {
    return `${String(hour).padStart(2, '0')}:00`;
  }
  function plural(n: number, word: string): string {
    return `${n.toLocaleString()} ${word}${n === 1 ? '' : 's'}`;
  }

  // ---- tooltip --------------------------------------------------------------

  // One tooltip for every chart mark, drawn above the mark that is hovered.
  // Position is measured from the panel the mark sits in, so the panel is the
  // positioning parent.
  let tip = $state<{ text: string; x: number; y: number; panel: string } | null>(null);
  function show(event: MouseEvent | FocusEvent, panel: string, text: string) {
    const target = event.currentTarget as Element;
    const host = target.closest('[data-panel]') as HTMLElement | null;
    if (!host) return;
    const mark = target.getBoundingClientRect();
    const box = host.getBoundingClientRect();
    tip = { text, panel, x: mark.left + mark.width / 2 - box.left, y: mark.top - box.top };
  }
  function hide() {
    tip = null;
  }
  // A re-read can take away the mark under the pointer; the tooltip must not
  // outlive it.
  $effect(() => {
    void $overview.dataUpdatedAt;
    tip = null;
  });

  // ---- heat exploration -------------------------------------------------

  // 168 cells is too many tab stops. One focusable group instead: arrow keys
  // move a highlighted cell, and the same text the mouse tooltip shows reaches
  // a live region so a screen reader hears what changed. A tap does the same,
  // for touch.
  let heatFocus = $state({ day: 0, hour: 0 });
  let heatFocused = $state(false);
  let heatAnnounce = $state('');

  function heatCellText(day: number, hour: number, count: number): string {
    return `${WEEKDAYS[day]} ${hourLabel(hour)} · ${plural(count, 'listen')}`;
  }

  function moveHeatFocus(event: KeyboardEvent) {
    const cells = listening?.whenYouListen.cells;
    if (!cells) return;
    let { day, hour } = heatFocus;
    switch (event.key) {
      case 'ArrowRight':
        hour = (hour + 1) % 24;
        break;
      case 'ArrowLeft':
        hour = (hour - 1 + 24) % 24;
        break;
      case 'ArrowDown':
        day = (day + 1) % 7;
        break;
      case 'ArrowUp':
        day = (day - 1 + 7) % 7;
        break;
      default:
        return;
    }
    event.preventDefault();
    heatFocus = { day, hour };
    heatAnnounce = heatCellText(day, hour, cells[day][hour]);
  }

  function touchHeatCell(day: number, hour: number, count: number) {
    heatFocus = { day, hour };
    heatAnnounce = heatCellText(day, hour, count);
  }

  // ---- lists --------------------------------------------------------------

  // A list that can scroll further says so with a fade at its foot, drawn by
  // the wrapper around it. The flag follows the scroll position, the list's
  // size and its rows, and goes when the last row is in view.
  function more(list: HTMLElement) {
    const update = () =>
      list.parentElement?.toggleAttribute('data-more', list.scrollTop + list.clientHeight < list.scrollHeight - 1);
    list.addEventListener('scroll', update, { passive: true });
    // The test window has no ResizeObserver; the flag is cosmetic there.
    const resized = typeof ResizeObserver === 'undefined' ? null : new ResizeObserver(update);
    resized?.observe(list);
    const changed = new MutationObserver(update);
    changed.observe(list, { childList: true });
    update();
    return {
      destroy() {
        list.removeEventListener('scroll', update);
        resized?.disconnect();
        changed.disconnect();
      }
    };
  }

  // A ranked row carries a wash behind it as long as its share of the first
  // row's listens, so the gap between first and fifth is seen, not read.
  function share(value: number, top: number): number {
    return top > 0 ? Math.max(4, Math.round((value / top) * 100)) : 0;
  }

  // Room on the disc as forty tiles, each a fortieth of it.
  const ROOM_TILES = 40;

  // ---- bar charts -------------------------------------------------------------

  // Bars are drawn in a viewBox of fixed width so the gaps stay the same at any panel width; the SVG stretches to fill the row.
  const BAR_W = 600;
  function bars(days: OverviewDay[], height: number) {
    const max = Math.max(1, ...days.map((day) => day.count));
    const gap = 5;
    const width = (BAR_W - gap * (days.length - 1)) / days.length;
    return days.map((day, index) => {
      const h = day.count === 0 ? 2 : Math.max(2, (day.count / max) * height);
      return { ...day, x: index * (width + gap), width, h, y: height - h, today: index === days.length - 1 };
    });
  }

  function growthPath(months: { month: string; files: number }[], width: number, height: number) {
    const max = Math.max(1, ...months.map((m) => m.files));
    const step = months.length > 1 ? width / (months.length - 1) : 0;
    const points = months.map((m, i) => ({ x: i * step, y: height - (m.files / max) * (height - 4) - 2, ...m }));
    const line = points.map((p, i) => `${i === 0 ? 'M' : 'L'}${p.x.toFixed(1)},${p.y.toFixed(1)}`).join(' ');
    const area = `${line} L${width},${height} L0,${height} Z`;
    return { points, line, area };
  }
</script>

<svelte:head>
  <title>Overview · Schall</title>
</svelte:head>

<!-- A panel's head: its icon and name on one centre line, the period at the
     far right. The name is set at line-height 1 so the icon and the capitals
     share a centre. -->
{#snippet head(icon: IconName, title: string, period: string)}
  <div class="flex items-center gap-2 text-ink-3">
    <Icon name={icon} size="sm" />
    <h2 class="label head-title">{title}</h2>
    {#if period}<span class="ml-auto text-meta leading-none text-ink-4">{period}</span>{/if}
  </div>
{/snippet}

<!-- The line a panel shows when the read has nothing for it. A panel left
     blank reads as one that failed to draw. -->
{#snippet quiet(text: string = 'No listens yet')}
  <p class="text-meta text-ink-4">{text}</p>
{/snippet}

<!-- What a panel shows while the overview is still being read: a breathing
     box the shape of what is coming, so the wait is nine panels loading
     rather than nine empty boxes. Each names itself for a screen reader and
     hides its boxes; the grid says busy. -->
{#snippet loadingChart(title: string, height: string)}
  <div class="flex grow flex-col gap-3" aria-busy="true" aria-label="Loading {title}">
    <Skeleton class="h-9 w-28" />
    <Skeleton class="h-0 {height} w-full grow" />
  </div>
{/snippet}

{#snippet loadingRows(title: string, height: string)}
  <div class="flex flex-col gap-2 {height}" aria-busy="true" aria-label="Loading {title}">
    {#each Array.from({ length: 5 }) as _, index (index)}
      <Skeleton class="h-8" />
    {/each}
  </div>
{/snippet}

{#snippet loadingFigures(title: string)}
  <div class="flex flex-col gap-3" aria-busy="true" aria-label="Loading {title}">
    <div class="flex gap-3">
      {#each Array.from({ length: 3 }) as _, index (index)}
        <Skeleton class="h-11 w-20" />
      {/each}
    </div>
    <Skeleton class="h-1" />
  </div>
{/snippet}

{#snippet tooltip(panel: string)}
  {#if tip && tip.panel === panel}
    <div
      role="tooltip"
      class="overlay-panel pointer-events-none absolute z-10 -translate-x-1/2 -translate-y-full whitespace-nowrap px-2 py-1 text-meta text-ink"
      style="left: {tip.x}px; top: {tip.y - 6}px"
    >
      {tip.text}
    </div>
  {/if}
{/snippet}

{#snippet row(cover: string | null, title: string, artist: string)}
  <span role="img" aria-label="Cover for {title}" class="h-8 w-8 shrink-0 overflow-hidden rounded-cover bg-inset">
    <Cover src={cover ?? undefined} seed={`${artist} – ${title}`} alt="" class="h-8 w-8 object-cover" />
  </span>
  <span class="flex min-w-0 flex-1 flex-col">
    <span class="truncate text-body font-medium text-ink">{title}</span>
    <span class="truncate text-meta text-ink-4">{artist}</span>
  </span>
{/snippet}

<!-- No line under the title: every figure it could carry is already in a
     panel below, and saying it twice is noise. -->
<PageHeader title="Overview" />
<div class="flex flex-col gap-4 px-5 pb-6 pt-4">
  {#if $overview.isError}
    <ErrorNote error={$overview.error} retry={() => $overview.refetch()} />
  {:else}
  <div class="grid grid-cols-1 gap-3.5 lg:grid-cols-12" aria-busy={$overview.isPending}>
    <!-- Listens -->
    <section data-panel class="panel relative lg:col-span-7">
      {@render head('listen', 'Listens', 'last 30 days')}
      <Settle pending={$overview.isPending} class="grow" layer="flex flex-col gap-[14px]">
        {#snippet placeholder()}
          {@render loadingChart('Listens', 'min-h-[132px]')}
        {/snippet}
        {#if listening?.available}
          <div class="flex items-center gap-3">
            <span class="numeric text-title font-semibold text-ink">{listening.listens.total.toLocaleString()}</span>
            {#if change !== null}
              <!-- Against the thirty days before; the earlier count itself is
                   not shown. -->
              <span class="delta inline-flex items-center gap-1 font-mono text-meta font-semibold {change >= 0 ? 'text-ok' : 'text-fail'}"
                title="{listening.listens.previous.toLocaleString()} in the 30 days before"
                ><Icon name={change >= 0 ? 'arrow-up' : 'arrow-down'} size="sm" />{change >= 0 ? '+' : ''}{change}%</span
              >
            {/if}
          </div>
          {@const columns = bars(listening.listens.days, 130)}
          <svg viewBox="0 0 {BAR_W} 132" preserveAspectRatio="none" class="h-0 min-h-[132px] w-full grow" aria-hidden="true">
            {#each columns as bar (bar.date)}
              <rect x={bar.x} y={bar.y} width={bar.width} height={bar.h} class={bar.today ? 'bar-today' : 'bar'} />
              <rect
                x={bar.x - 1.5}
                y="0"
                width={bar.width + 3}
                height="132"
                fill="transparent"
                aria-hidden="true"
                onmouseenter={(event) => show(event, 'listens', `${weekdayOf(bar.date)} ${dayLabel(bar.date)} · ${plural(bar.count, 'listen')}`)}
                onmouseleave={hide}
              />
            {/each}
          </svg>
          <div class="flex justify-between font-mono text-micro text-ink-4">
            <span>{dayLabel(listening.listens.days[0].date)}</span>
            <span>{dayLabel(listening.listens.days[14].date)}</span>
            <span>today</span>
          </div>
        {:else if listening}
          {@render quiet()}
        {/if}
      </Settle>
      {@render tooltip('listens')}
    </section>

    <!-- Most played -->
    <section data-panel class="panel lg:col-span-5">
      {@render head('track', 'Most played', 'this month')}
      <Settle pending={$overview.isPending} class="grow" layer="flex flex-col gap-[14px]">
        {#snippet placeholder()}
          {@render loadingRows('Most played', 'min-h-[12.5rem]')}
        {/snippet}
        {#if listening?.available && listening.mostPlayed.length > 0}
          <!-- Every list here is keyed by position. Two files added in the same
               second with one title, or one album under two editions, made a
               content key collide and the throw blanked every panel after it
               (2026-09-08). The lists are replaced whole on each read, so a
               positional key loses nothing. -->
          <div class="panel-scroll min-h-[12.5rem]">
          <ul class="panel-list" use:more>
            {#each listening.mostPlayed as song, index (index)}
              <li class="ranked want-row flex h-9 shrink-0 items-center gap-2.5 border-b border-rule">
                <span class="wash" style="width: {share(song.listens, listening.mostPlayed[0].listens)}%" aria-hidden="true"></span>
                <svelte:element
                  this={song.releaseId ? 'a' : 'div'}
                  href={song.releaseId ? `/releases/${song.releaseId}` : undefined}
                  class="row-link"
                >
                  <span class="rank" aria-hidden="true">{index + 1}</span>
                  {@render row(song.coverUrl, song.title, song.artist)}
                </svelte:element>
                <!-- A held song is the ordinary case and carries no mark; the
                     exception is the one that can be wanted. -->
                <span class="flex w-6 shrink-0 items-center justify-center">
                  {#if !song.inLibrary && song.trackId}
                    <WantToggle
                      reveal
                      wanted={song.wanted}
                      title={song.title}
                      onwant={() => wantSong(song.trackId!)}
                      onunwant={() => letGo(song.trackId!)}
                    />
                  {/if}
                </span>
                <span class="w-7 text-right font-mono text-body font-semibold text-ink">{song.listens}</span>
              </li>
            {/each}
          </ul>
          </div>
        {:else if listening}
          {@render quiet(listening.available ? 'No listens this month' : undefined)}
        {/if}
      </Settle>
    </section>

    <!-- When you listen -->
    <section data-panel class="panel relative lg:col-span-7">
      {@render head('waiting', 'When you listen', '30 days')}
      <Settle pending={$overview.isPending} class="grow" layer="flex flex-col gap-[14px]">
        {#snippet placeholder()}
          {@render loadingChart('When you listen', 'min-h-[7rem]')}
        {/snippet}
        {#if listening?.available}
          <div class="flex items-baseline gap-5">
            <span class="flex items-baseline gap-2"><span class="fig">{hourLabel(listening.whenYouListen.peakHour)}</span><span class="text-body text-ink-4">peak</span></span>
            <span class="flex items-baseline gap-1.5"><span class="font-mono text-body text-ink">{listening.whenYouListen.busiestWeekday}</span><span class="text-meta text-ink-4">busiest</span></span>
          </div>
          <!-- One tab stop for 168 cells; arrow keys move the highlighted one,
               like a grid, so the group itself takes the keydown. -->
          <!-- svelte-ignore a11y_no_noninteractive_tabindex -->
          <!-- svelte-ignore a11y_no_noninteractive_element_interactions -->
          <div
            class="flex flex-col gap-1"
            role="group"
            tabindex="0"
            aria-label="Listens by weekday and hour, arrow keys to explore"
            onkeydown={moveHeatFocus}
            onfocus={() => (heatFocused = true)}
            onblur={() => (heatFocused = false)}
          >
            {#each listening.whenYouListen.cells as hours, day (day)}
              <div class="grid items-center gap-1" style="grid-template-columns: 2.25rem repeat(24, minmax(0, 1fr))">
                <span class="pr-1.5 text-right font-mono text-micro text-ink-4">{WEEKDAYS[day]}</span>
                {#each hours as count, hour (hour)}
                  <span
                    data-cell
                    class="aspect-square w-full"
                    style="background: {heatColour(count)}; {heatFocused && heatFocus.day === day && heatFocus.hour === hour
                      ? 'box-shadow: inset 0 0 0 2px var(--color-ink);'
                      : day * 24 + hour === heatPeak
                        ? 'box-shadow: inset 0 0 0 2px var(--color-ink);'
                        : ''}"
                    aria-hidden="true"
                    onmouseenter={(event) => show(event, 'heat', heatCellText(day, hour, count))}
                    onmouseleave={hide}
                    onclick={() => touchHeatCell(day, hour, count)}
                  ></span>
                {/each}
              </div>
            {/each}
            <div class="grid gap-1 pt-0.5 font-mono text-micro text-ink-4" style="grid-template-columns: 2.25rem repeat(24, minmax(0, 1fr))">
              <span></span>
              {#each Array.from({ length: 24 }, (_, hour) => hour) as hour (hour)}
                <span class="whitespace-nowrap">{hour % 3 === 0 ? hourLabel(hour).slice(0, 2) : ''}</span>
              {/each}
            </div>
          </div>
          <p aria-live="polite" class="sr-only">{heatAnnounce}</p>
        {:else if listening}
          {@render quiet()}
        {/if}
      </Settle>
      {@render tooltip('heat')}
    </section>

    <!-- Top artists -->
    <section data-panel class="panel lg:col-span-5">
      {@render head('artists', 'Top artists', 'this month')}
      <Settle pending={$overview.isPending} class="grow" layer="flex flex-col gap-[14px]">
        {#snippet placeholder()}
          {@render loadingRows('Top artists', 'min-h-[12.5rem]')}
        {/snippet}
        {#if listening?.available && listening.topArtists.length > 0}
          <div class="panel-scroll min-h-[12.5rem]">
          <ul class="panel-list" use:more>
            {#each listening.topArtists as artist, index (index)}
              <li class="ranked flex h-9 shrink-0 items-center gap-2.5 border-b border-rule">
                <span class="wash" style="width: {share(artist.listens, listening.topArtists[0].listens)}%" aria-hidden="true"></span>
                <svelte:element
                  this={artist.artistId ? 'a' : 'div'}
                  href={artist.artistId ? `/artists/${artist.artistId}` : undefined}
                  class="row-link"
                >
                  <span class="rank" aria-hidden="true">{index + 1}</span>
                  <span class="flex h-8 w-8 shrink-0 items-center justify-center overflow-hidden bg-inset text-body font-semibold text-ink-2" aria-hidden="true">
                    <Cover src={artist.pictureUrl ?? undefined} alt="" class="h-8 w-8 object-cover" fallback={artist.name.slice(0, 1)} />
                  </span>
                  <span class="min-w-0 flex-1 truncate text-body font-medium text-ink">{artist.name}</span>
                </svelte:element>
                <span class="w-7 text-right font-mono text-body font-semibold text-ink">{artist.listens}</span>
              </li>
            {/each}
          </ul>
          </div>
        {:else if listening}
          {@render quiet(listening.available ? 'No listens this month' : undefined)}
        {/if}
      </Settle>
    </section>

    <!-- Top albums -->
    <section data-panel class="panel lg:col-span-7">
      {@render head('release', 'Top albums', 'this month')}
      <Settle pending={$overview.isPending} class="grow" layer="flex flex-col gap-[14px]">
        {#snippet placeholder()}
          {@render loadingRows('Top albums', 'min-h-[12.5rem]')}
        {/snippet}
        {#if listening?.available && listening.topAlbums.length > 0}
          <!-- The month's five most played albums as a wall of covers, the first
               twice the size of the rest. -->
          <ul class="wall">
            {#each listening.topAlbums.slice(0, 5) as album, index (index)}
              <li>
                <svelte:element
                  this={album.releaseId ? 'a' : 'div'}
                  href={album.releaseId ? `/releases/${album.releaseId}` : undefined}
                  class="wall-tile"
                >
                  <span role="img" aria-label="Cover for {album.title}" class="absolute inset-0 bg-inset">
                    <Cover src={album.coverUrl ?? undefined} seed={`${album.artist} – ${album.title}`} alt="" class="h-full w-full object-cover" />
                  </span>
                  <span class="wall-caption">
                    <span class="truncate font-medium text-ink">{album.title}</span>
                    <span class="truncate text-ink-3">{album.artist} · {album.listens}</span>
                  </span>
                </svelte:element>
              </li>
            {/each}
          </ul>
        {:else if listening}
          {@render quiet(listening.available ? 'No listens this month' : undefined)}
        {/if}
      </Settle>
    </section>

    <!-- Recently played -->
    <section data-panel class="panel lg:col-span-5">
      {@render head('play', 'Recently played', '')}
      <Settle pending={$overview.isPending} class="grow" layer="flex flex-col gap-[14px]">
        {#snippet placeholder()}
          {@render loadingRows('Recently played', 'min-h-[15rem]')}
        {/snippet}
        {#if listening?.available && listening.sessions.length > 0}
          <!-- One row per sitting: when it ran and how many songs, then who
               was on. A gap over thirty minutes starts the next one. -->
          <div class="panel-scroll min-h-[15rem]">
          <ul class="panel-list" use:more>
            {#each listening.sessions as session, index (index)}
              <li class="flex min-h-9 shrink-0 items-center gap-2.5 border-b border-rule py-1.5">
                <!-- Up to three of the session's covers, overlapping, the first
                     on top. The slot is always three covers wide so the text
                     starts at the same x in every row. -->
                <span class="flex w-[3.75rem] shrink-0" aria-hidden="true">
                  {#each session.coverUrls.slice(0, 3) as url, index (index)}
                    <span
                      class="relative block h-7 w-7 overflow-hidden rounded-cover bg-inset ring-2 ring-[var(--color-panel)]"
                      class:-ml-3={index > 0}
                      style="z-index: {3 - index}"
                    >
                      <Cover src={url} alt="" class="h-7 w-7 object-cover" />
                    </span>
                  {/each}
                </span>
                <span class="flex min-w-0 flex-1 flex-col gap-0.5">
                  <!-- The range alone on the first line: with a day in front it
                       fills the panel's width, so the count sits on the second. -->
                  <span class="truncate text-meta tabular-nums text-ink">{sessionRange(session)}</span>
                  <span class="truncate text-meta text-ink-3"
                    ><span class="tabular-nums text-ink-4">{plural(session.songs, 'song')}</span> · {artistsLine(session.artists)}</span
                  >
                </span>
              </li>
            {/each}
          </ul>
          </div>
        {:else if listening}
          {@render quiet(listening.available ? 'No listens yet' : undefined)}
        {/if}
      </Settle>
    </section>

    <!-- Recently added -->
    <section data-panel class="panel lg:col-span-5">
      {@render head('plus', 'Recently added', '')}
      <Settle pending={$overview.isPending} class="grow" layer="flex flex-col gap-[14px]">
        {#snippet placeholder()}
          {@render loadingRows('Recently added', 'min-h-[12.5rem]')}
        {/snippet}
        {#if data && data.recentlyAdded.length > 0}
          <div class="panel-scroll min-h-[12.5rem]">
          <ul class="panel-list" use:more>
            {#each data.recentlyAdded as file, index (index)}
              <li class="flex h-9 shrink-0 items-center gap-2.5 border-b border-rule">
                <svelte:element
                  this={file.releaseId ? 'a' : 'div'}
                  href={file.releaseId ? `/releases/${file.releaseId}` : undefined}
                  class="row-link"
                >
                  {@render row(file.coverUrl, file.title, file.artist)}
                </svelte:element>
                <span class="font-mono text-meta text-ink-4">{relativeTime(file.addedAt).replace(' ago', '')}</span>
              </li>
            {/each}
          </ul>
          </div>
        {:else if data}
          {@render quiet('Nothing added yet')}
        {/if}
      </Settle>
    </section>

    <!-- Downloaded -->
    <section data-panel class="panel relative lg:col-span-4">
      {@render head('download', 'Downloaded', '14 days')}
      <Settle pending={$overview.isPending} class="grow" layer="flex flex-col gap-[14px]">
        {#snippet placeholder()}
          {@render loadingChart('Downloaded', 'min-h-[7rem]')}
        {/snippet}
        {#if data}
          <div class="flex items-baseline gap-3">
            <span class="fig">{data.arrived.today.toLocaleString()}</span>
            <span class="text-body text-ink-4">today</span>
            <span class="font-mono text-body text-ink-2">{data.arrived.downloading.toLocaleString()}</span>
            <span class="text-body text-ink-4">downloading</span>
          </div>
          {@const columns = bars(data.arrived.days, 96)}
          <svg viewBox="0 0 {BAR_W} 98" preserveAspectRatio="none" class="h-0 min-h-[98px] w-full grow" aria-hidden="true">
            {#each columns as bar (bar.date)}
              <rect x={bar.x} y={bar.y} width={bar.width} height={bar.h} class={bar.today ? 'bar-today' : 'bar'} />
              <rect
                x={bar.x - 1.5}
                y="0"
                width={bar.width + 3}
                height="98"
                fill="transparent"
                aria-hidden="true"
                onmouseenter={(event) => show(event, 'arrived', `${dayLabel(bar.date)} · ${bar.count.toLocaleString()} downloaded`)}
                onmouseleave={hide}
              />
            {/each}
          </svg>
          <div class="flex justify-between font-mono text-micro text-ink-4">
            <span>{dayLabel(data.arrived.days[0].date)}</span>
            <span>today</span>
          </div>
        {/if}
      </Settle>
      {@render tooltip('arrived')}
    </section>

    <!-- Library -->
    <section data-panel class="panel relative lg:col-span-3">
      {@render head('library', 'Library', '')}
      <Settle pending={$overview.isPending} class="grow" layer="flex flex-col gap-[14px]">
        {#snippet placeholder()}
          {@render loadingFigures('Library')}
        {/snippet}
        {#if data}
          <div class="flex gap-3">
            <div class="flex flex-col gap-0.5">
              <span class="font-mono text-lead text-ink">{fileCount.toLocaleString()}</span>
              <span class="text-meta text-ink-3">{fileCount === 1 ? 'file' : 'files'}</span>
            </div>
            <div class="flex flex-col gap-0.5">
              <span class="font-mono text-lead text-ink">{formatBytes(data.library.totalBytes)}</span>
              <span class="text-meta text-ink-3">total size</span>
            </div>
          </div>
          {@const growth = growthPath(data.library.growth, 240, 64)}
          <svg viewBox="0 0 240 64" preserveAspectRatio="none" class="h-0 min-h-16 w-full grow" aria-label="Library files over the last year">
            <path d={growth.area} class="growth-area" />
            <path d={growth.line} class="growth-line" stroke-width="2" vector-effect="non-scaling-stroke" />
            {#if growth.points.length > 0}
              {@const end = growth.points[growth.points.length - 1]}
              <circle cx={end.x} cy={end.y} r="3" class="growth-end" />
            {/if}
            {#each growth.points as point, index (point.month)}
              <rect
                x={index === 0 ? 0 : point.x - 10}
                y="0"
                width="20"
                height="64"
                fill="transparent"
                role="img"
                aria-label="{monthLabel(point.month)} · {plural(point.files, 'file')}"
                onmouseenter={(event) => show(event, 'library', `${monthLabel(point.month)} · ${plural(point.files, 'file')}`)}
                onmouseleave={hide}
              />
            {/each}
          </svg>
          {#if roomLeft}
            <div class="mt-auto flex flex-col gap-1.5">
              <p class="text-meta text-ink-4">Room left <span class="font-mono text-ink-2">{formatBytes(roomLeft.free)}</span></p>
              <div
                class="room"
                role="img"
                aria-label="{formatBytes(data.library.storage!.usedBytes)} used of {formatBytes(data.library.storage!.totalBytes)}"
                onmouseenter={(event) => show(event, 'library', `${formatBytes(data.library.storage!.usedBytes)} used of ${formatBytes(data.library.storage!.totalBytes)}`)}
                onmouseleave={hide}
              >
                {#each Array.from({ length: ROOM_TILES }, (_, tile) => tile) as tile (tile)}
                  <span class:used={tile < Math.round((roomLeft.usedShare / 100) * ROOM_TILES)}></span>
                {/each}
              </div>
            </div>
          {/if}
        {/if}
      </Settle>
      {@render tooltip('library')}
    </section>
  </div>
  {/if}
</div>

<style>
  /* Chart marks in the ink. Set as properties, because an SVG presentation
     attribute does not read a custom property everywhere. */
  .bar {
    fill: color-mix(in srgb, var(--color-ink) 62%, var(--color-inset));
  }
  .bar-today {
    fill: var(--color-ink);
  }
  .growth-area {
    fill: var(--color-ink);
    fill-opacity: 0.14;
  }
  .growth-line {
    fill: none;
    stroke: var(--color-ink);
  }
  /* A row that names a release or artist opens it; the whole row is the
     target, so the hover fill runs to the panel's edges. */
  .row-link {
    display: flex;
    flex: 1 1 0;
    min-width: 0;
    align-items: center;
    gap: 0.625rem;
    align-self: stretch;
    margin-inline: -0.5rem;
    padding-inline: 0.5rem;
  }
  a.row-link:hover {
    background: var(--color-raised);
  }
  .growth-end {
    fill: var(--color-ink);
  }
  /* A flat panel on the ground, no border. */
  .panel {
    display: flex;
    flex-direction: column;
    gap: 14px;
    min-width: 0;
    min-height: 0;
    padding: 16px 20px 20px;
    background: var(--color-panel);
  }
  /* The label class sets the size and the capitals. Line-height 1 puts the
     text's middle on the box's middle, so the icon beside it shares a line. */
  .head-title {
    line-height: 1;
  }
  /* A ranked row: its place and a bar behind it as long as its share of the
     first row. */
  .ranked {
    position: relative;
    isolation: isolate;
    padding-inline: 0.625rem;
  }
  .ranked:has(a.row-link:hover),
  .ranked:has(a.row-link:focus-visible) {
    background: var(--color-raised);
  }
  /* The list scrolls, so it clips sideways: the row keeps its bar and hover
     inside its own box instead of reaching past the list's edge. */
  .ranked .row-link {
    margin-inline: 0;
    padding-inline: 0;
  }
  .ranked a.row-link:hover {
    background: none;
  }
  /* The list's clip would cut the link's own ring, so the row draws it inside. */
  .ranked a.row-link:focus-visible {
    outline: none;
  }
  .ranked:has(a.row-link:focus-visible) {
    outline: 2px solid var(--color-ink);
    outline-offset: -2px;
  }
  .wash {
    position: absolute;
    inset: 0 auto 0 0;
    z-index: -1;
    background: var(--color-rule);
  }
  .rank {
    width: 1.1rem;
    flex-shrink: 0;
    font-family: var(--font-mono);
    font-size: var(--text-meta);
    color: var(--color-ink-3);
  }
  .wall {
    display: grid;
    grid-template-columns: repeat(4, minmax(0, 1fr));
    gap: 10px;
  }
  .wall > li:first-child {
    grid-column: span 2;
    grid-row: span 2;
  }
  .wall-tile {
    position: relative;
    display: block;
    aspect-ratio: 1;
    overflow: hidden;
    border-radius: var(--radius-cover);
  }
  /* The caption sits on a flat strip of the panel colour, so the words never
     lie on the picture. */
  .wall-caption {
    position: absolute;
    z-index: 1;
    left: 0;
    right: 0;
    bottom: 0;
    display: flex;
    flex-direction: column;
    padding: 6px 10px;
    background: var(--color-panel);
    font-size: var(--text-meta);
    line-height: 1.25;
  }
  .wall > li:first-child .wall-caption {
    padding: 8px 14px;
    font-size: var(--text-body);
  }
  .room {
    display: grid;
    grid-template-columns: repeat(20, minmax(0, 1fr));
    gap: 3px;
  }
  .room > span {
    height: 14px;
    background: var(--color-inset);
  }
  .room > span.used {
    background: var(--color-ink);
  }
  /* Charts grow with `grow`, not `flex-1`: a percentage basis in a panel of
     indefinite height resolves to the SVG's aspect-ratio height, and a
     year of growth 970px wide wanted 259px. A zero basis lets min-height set
     the floor. */
  /* A list fills whatever height the row of panels gives it and scrolls
     inside; `contain: size` keeps its 25 rows out of the row's height
     calculation, so the charts beside it set the height and the list follows.
     A rule at the foot says there is more; a scrollbar in a panel this
     small did not fit. */
  .panel-scroll {
    position: relative;
    display: flex;
    flex-direction: column;
    flex: 1 1 0;
    contain: size;
  }
  .panel-list {
    display: flex;
    flex-direction: column;
    flex: 1 1 0;
    min-height: 0;
    overflow-y: auto;
    scrollbar-width: none;
    scroll-snap-type: y proximity;
  }
  .panel-list > li {
    scroll-snap-align: start;
  }
  .panel-list::-webkit-scrollbar {
    display: none;
  }
  /* The attribute is set by the action, never by the template, so the
     selector is global or the compiler drops it as unused. */
  :global(.panel-scroll[data-more])::after {
    content: '';
    position: absolute;
    left: 0;
    right: 0;
    bottom: 0;
    height: 1px;
    pointer-events: none;
    background: var(--color-rule-strong);
  }
  .fig {
    font-family: var(--font-mono);
    font-variant-numeric: tabular-nums;
    font-size: var(--text-lead);
    font-weight: 600;
    line-height: 1;
    color: var(--color-ink);
  }
</style>

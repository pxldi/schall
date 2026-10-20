<script lang="ts">
  import { tick } from 'svelte';
  import { goto } from '$app/navigation';
  import Skeleton from '$lib/components/Skeleton.svelte';
  import Settle from '$lib/components/Settle.svelte';
  import Figure from '$lib/components/Figure.svelte';
  import { useKeys } from '$lib/keys.svelte';
  import { glide } from '$lib/motion.svelte';
  import { createQuery, useQueryClient } from '@tanstack/svelte-query';
  import { api, type OverviewDay } from '$lib/api';
  import { formatBytes, relativeTime } from '$lib/utils';
  import type { OverviewSession } from '$lib/api-types';
  import Cover from '$lib/components/Cover.svelte';
  import Icon from '$lib/components/Icon.svelte';
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

  // ---- recently added ---------------------------------------------------------

  // The one list with a cursor. J and K move it, Enter opens the release. The
  // arrow keys are left to the page, which scrolls.
  const added = $derived(data?.recentlyAdded ?? []);
  let at = $state(0);
  let addedList = $state<HTMLElement>();
  $effect(() => {
    if (at > added.length - 1) at = Math.max(0, added.length - 1);
  });
  async function step(to: number) {
    at = Math.min(Math.max(0, to), added.length - 1);
    await tick();
    addedList?.querySelector('[data-cursor="true"]')?.scrollIntoView?.({ block: 'nearest' });
  }
  useKeys(() => [
    {
      keys: ['j', 'k'],
      verb: 'move',
      order: 0,
      enabled: () => added.length > 0,
      run: (event) => void step(event.key.toLowerCase() === 'k' ? at - 1 : at + 1)
    },
    {
      keys: ['enter'],
      verb: 'open',
      order: 1,
      enabled: () => Boolean(added[at]?.releaseId),
      run: () => void goto(`/releases/${added[at].releaseId}`)
    }
  ]);

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
  // The ramp runs from the raised fill to the ink. How often somebody listens
  // at an hour is not a state, so it has no state colour.
  const HEAT = [0, 18, 34, 52, 74, 100].map((share) =>
    share === 0
      ? 'var(--color-raised)'
      : `color-mix(in srgb, var(--color-ink) ${share}%, var(--color-raised))`
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

<!-- A section's head: its name in `.label`, the period at the far right. The
     rule above it is the section's only edge. -->
{#snippet head(title: string, period: string)}
  <div class="flex items-baseline gap-2">
    <h2 class="label">{title}</h2>
    {#if period}<span class="ml-auto text-meta text-ink-4">{period}</span>{/if}
  </div>
{/snippet}

<!-- The line a section shows when the read has nothing for it. A section left
     blank reads as one that failed to draw. -->
{#snippet quiet(text: string = 'No listens yet')}
  <p class="text-meta text-ink-4">{text}</p>
{/snippet}

<!-- What a section shows while the overview is still being read: a breathing
     box the shape of what is coming. Each names itself for a screen reader
     and hides its boxes; the grid says busy. -->
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
    <div class="flex gap-6">
      {#each Array.from({ length: 3 }) as _, index (index)}
        <Skeleton class="h-11 w-24" />
      {/each}
    </div>
    <Skeleton class="h-16" />
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

<!-- No line under the title: every figure it could carry is in a section
     below, and saying it twice is noise. -->
<PageHeader title="Overview" />
<div class="px-5 pb-6 pt-4">
  {#if $overview.isError}
    <ErrorNote error={$overview.error} retry={() => $overview.refetch()} />
  {:else}
    <div class="flex flex-col gap-6" aria-busy={$overview.isPending}>
      <div class="grid grid-cols-1 gap-x-10 gap-y-6 lg:grid-cols-12">
        <!-- Recently added: the working list, with a cursor. J and K move it,
             Enter opens the release. -->
        <section class="flex min-w-0 flex-col gap-3 border-t border-rule pt-3 lg:col-span-5">
          {@render head('Recently added', '')}
          <Settle pending={$overview.isPending} class="grow" layer="flex flex-col gap-[14px]">
            {#snippet placeholder()}
              {@render loadingRows('Recently added', 'min-h-[12.5rem]')}
            {/snippet}
            {#if data && added.length > 0}
              <div
                bind:this={addedList}
                class="no-scrollbar relative flex max-h-[calc(100dvh-16rem)] flex-col overflow-y-auto"
                use:glide={{ key: `${at}:${added.length}` }}
              >
                <ul class="flex flex-col">
                  {#each added as file, index (index)}
                    {@const here = index === at}
                    <li data-cursor={here} class="row flex shrink-0 items-center gap-2.5 px-2.5 {here ? 'on-ink' : ''}">
                      {#if file.releaseId}
                        <a
                          href={`/releases/${file.releaseId}`}
                          class="row-link"
                          onclick={() => (at = index)}
                          onfocus={() => (at = index)}
                        >
                          {@render row(file.coverUrl, file.title, file.artist)}
                        </a>
                      {:else}
                        <div class="row-link">
                          {@render row(file.coverUrl, file.title, file.artist)}
                        </div>
                      {/if}
                      <span class="mono text-meta text-ink-4">{relativeTime(file.addedAt).replace(' ago', '')}</span>
                    </li>
                  {/each}
                </ul>
              </div>
            {:else if data}
              {@render quiet('Nothing added yet')}
            {/if}
          </Settle>
        </section>

        <div class="flex min-w-0 flex-col gap-6 lg:col-span-7">
          <!-- Library -->
          <section data-panel class="relative flex flex-col gap-3 border-t border-rule pt-3">
            {@render head('Library', '')}
            <Settle pending={$overview.isPending} class="grow" layer="flex flex-col gap-[14px]">
              {#snippet placeholder()}
                {@render loadingFigures('Library')}
              {/snippet}
              {#if data}
                <div class="flex flex-wrap gap-x-10 gap-y-3">
                  <Figure value={fileCount.toLocaleString()} label={fileCount === 1 ? 'file' : 'files'} />
                  <Figure value={formatBytes(data.library.totalBytes)} label="total size" />
                  {#if roomLeft}
                    <Figure value={formatBytes(roomLeft.free)} label="Room left" />
                  {/if}
                </div>
                {@const growth = growthPath(data.library.growth, 240, 64)}
                <svg viewBox="0 0 240 64" preserveAspectRatio="none" class="h-0 min-h-16 w-full grow" aria-label="Library files over the last year">
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
                {/if}
              {/if}
            </Settle>
            {@render tooltip('library')}
          </section>

          <!-- Downloaded -->
          <section data-panel class="relative flex flex-col gap-3 border-t border-rule pt-3">
            {@render head('Downloaded', '14 days')}
            <Settle pending={$overview.isPending} class="grow" layer="flex flex-col gap-[14px]">
              {#snippet placeholder()}
                {@render loadingChart('Downloaded', 'min-h-[7rem]')}
              {/snippet}
              {#if data}
                <div class="flex flex-wrap gap-x-10 gap-y-3">
                  <Figure value={data.arrived.today.toLocaleString()} label="today" />
                  <Figure value={data.arrived.downloading.toLocaleString()} label="downloading" />
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
        </div>
      </div>

      {#if $overview.isPending || listening?.available}
        <div class="grid grid-cols-1 gap-x-10 gap-y-6 lg:grid-cols-12">
          <div class="flex min-w-0 flex-col gap-6 lg:col-span-7">
            <!-- Listens -->
            <section data-panel class="relative flex flex-col gap-3 border-t border-rule pt-3">
              {@render head('Listens', 'last 30 days')}
              <Settle pending={$overview.isPending} class="grow" layer="flex flex-col gap-[14px]">
                {#snippet placeholder()}
                  {@render loadingChart('Listens', 'min-h-[132px]')}
                {/snippet}
                {#if listening?.available}
                  <div class="flex items-baseline gap-4">
                    <Figure value={listening.listens.total.toLocaleString()} />
                    {#if change !== null}
                      <!-- Against the thirty days before; the earlier count itself is
                           not shown. -->
                      <span
                        class="inline-flex items-center gap-1 font-mono text-meta font-semibold {change >= 0 ? 'text-ok' : 'text-fail'}"
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
                {/if}
              </Settle>
              {@render tooltip('listens')}
            </section>

            <!-- When you listen -->
            <section data-panel class="relative flex flex-col gap-3 border-t border-rule pt-3">
              {@render head('When you listen', '30 days')}
              <Settle pending={$overview.isPending} class="grow" layer="flex flex-col gap-[14px]">
                {#snippet placeholder()}
                  {@render loadingChart('When you listen', 'min-h-[7rem]')}
                {/snippet}
                {#if listening?.available}
                  <div class="flex items-end gap-10">
                    <Figure size="md" value={hourLabel(listening.whenYouListen.peakHour)} label="peak" />
                    <div class="flex flex-col gap-1.5">
                      <span class="text-lead font-semibold leading-none text-ink">{listening.whenYouListen.busiestWeekday}</span>
                      <span class="label">busiest</span>
                    </div>
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
                            class:ringed={(heatFocused && heatFocus.day === day && heatFocus.hour === hour) || day * 24 + hour === heatPeak}
                            style="background: {heatColour(count)}"
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
                {/if}
              </Settle>
              {@render tooltip('heat')}
            </section>

            <!-- Top albums -->
            <section data-panel class="flex flex-col gap-3 border-t border-rule pt-3">
              {@render head('Top albums', 'this month')}
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
                  {@render quiet('No listens this month')}
                {/if}
              </Settle>
            </section>
          </div>

          <div class="flex min-w-0 flex-col gap-6 lg:col-span-5">
            <!-- Most played -->
            <section data-panel class="flex flex-col gap-3 border-t border-rule pt-3">
              {@render head('Most played', 'this month')}
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
                  <div class="list-wrap">
                    <ul class="list" use:more>
                      {#each listening.mostPlayed as song, index (index)}
                        <li class="ranked row want-row flex shrink-0 items-center gap-2.5 px-2.5">
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
                          <span class="mono w-7 text-right text-body font-semibold text-ink">{song.listens}</span>
                        </li>
                      {/each}
                    </ul>
                  </div>
                {:else if listening}
                  {@render quiet('No listens this month')}
                {/if}
              </Settle>
            </section>

            <!-- Top artists -->
            <section data-panel class="flex flex-col gap-3 border-t border-rule pt-3">
              {@render head('Top artists', 'this month')}
              <Settle pending={$overview.isPending} class="grow" layer="flex flex-col gap-[14px]">
                {#snippet placeholder()}
                  {@render loadingRows('Top artists', 'min-h-[12.5rem]')}
                {/snippet}
                {#if listening?.available && listening.topArtists.length > 0}
                  <div class="list-wrap">
                    <ul class="list" use:more>
                      {#each listening.topArtists as artist, index (index)}
                        <li class="ranked row flex shrink-0 items-center gap-2.5 px-2.5">
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
                          <span class="mono w-7 text-right text-body font-semibold text-ink">{artist.listens}</span>
                        </li>
                      {/each}
                    </ul>
                  </div>
                {:else if listening}
                  {@render quiet('No listens this month')}
                {/if}
              </Settle>
            </section>

            <!-- Recently played -->
            <section data-panel class="flex flex-col gap-3 border-t border-rule pt-3">
              {@render head('Recently played', '')}
              <Settle pending={$overview.isPending} class="grow" layer="flex flex-col gap-[14px]">
                {#snippet placeholder()}
                  {@render loadingRows('Recently played', 'min-h-[15rem]')}
                {/snippet}
                {#if listening?.available && listening.sessions.length > 0}
                  <!-- One row per sitting: when it ran and how many songs, then who
                       was on. A gap over thirty minutes starts the next one. -->
                  <div class="list-wrap">
                    <ul class="list" use:more>
                      {#each listening.sessions as session, index (index)}
                        <li class="row flex shrink-0 items-center gap-2.5 px-2.5 py-1.5">
                          <!-- Up to three of the session's covers, overlapping, the first
                               on top. The slot is always three covers wide so the text
                               starts at the same x in every row. -->
                          <span class="flex w-[3.75rem] shrink-0" aria-hidden="true">
                            {#each session.coverUrls.slice(0, 3) as url, index (index)}
                              <span
                                class="stacked relative block h-7 w-7 overflow-hidden rounded-cover bg-inset"
                                class:-ml-3={index > 0}
                                style="z-index: {3 - index}"
                              >
                                <Cover src={url} alt="" class="h-7 w-7 object-cover" />
                              </span>
                            {/each}
                          </span>
                          <span class="flex min-w-0 flex-1 flex-col gap-0.5">
                            <!-- The range alone on the first line: with a day in front it
                                 fills the section's width, so the count sits on the second. -->
                            <span class="mono truncate text-meta text-ink">{sessionRange(session)}</span>
                            <span class="truncate text-meta text-ink-3"
                              ><span class="mono text-ink-4">{plural(session.songs, 'song')}</span> · {artistsLine(session.artists)}</span
                            >
                          </span>
                        </li>
                      {/each}
                    </ul>
                  </div>
                {:else if listening}
                  {@render quiet('No listens yet')}
                {/if}
              </Settle>
            </section>
          </div>
        </div>
      {:else}
        <!-- No listens are stored, so six empty sections collapse to one line. -->
        <section class="flex flex-wrap items-baseline gap-x-6 gap-y-1 border-t border-rule pt-3">
          <h2 class="label">Listening</h2>
          <p class="text-body text-ink-2">No listens yet</p>
          <p class="text-body text-ink-4">Listens arrive from Navidrome.</p>
        </section>
      {/if}
    </div>
  {/if}
</div>

<style>
  /* Chart marks in ink tokens. Set as properties, because an SVG presentation
     attribute does not read a custom property everywhere. */
  .bar {
    fill: var(--color-ink-3);
  }
  .bar-today {
    fill: var(--color-ink);
  }
  .growth-line {
    fill: none;
    stroke: var(--color-ink);
  }
  .growth-end {
    fill: var(--color-ink);
  }
  /* The peak hour and the hour the arrow keys are on carry a 2px ring drawn
     as an outline, so nothing is a shadow. */
  [data-cell].ringed {
    outline: 2px solid var(--color-ink);
    outline-offset: -2px;
  }
  /* A row that names a release or artist opens it; the whole row is the
     target. */
  .row-link {
    display: flex;
    flex: 1 1 0;
    min-width: 0;
    align-items: center;
    gap: 0.625rem;
    align-self: stretch;
  }
  /* A ranked row: its place and a bar behind it as long as its share of the
     first row. */
  .ranked {
    isolation: isolate;
  }
  .ranked:has(a.row-link:focus-visible) {
    outline: 2px solid var(--color-ink);
    outline-offset: -2px;
  }
  .ranked a.row-link:focus-visible {
    outline: none;
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
    font-stretch: 87.5%;
    font-size: var(--text-meta);
    color: var(--color-ink-3);
  }
  /* Overlapping covers are cut apart by the ground, drawn as an outline. */
  .stacked {
    outline: 2px solid var(--color-ground);
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
  /* The caption sits on a flat strip of the ground colour, so the words never
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
    background: var(--color-ground);
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
    background: var(--color-raised);
  }
  .room > span.used {
    background: var(--color-ink);
  }
  /* A ranked list shows ten rows and scrolls inside. A rule at the foot says
     there is more; a scrollbar this small did not fit. */
  .list-wrap {
    position: relative;
  }
  .list {
    display: flex;
    flex-direction: column;
    max-height: 22.5rem;
    overflow-y: auto;
    scrollbar-width: none;
  }
  .list::-webkit-scrollbar {
    display: none;
  }
  /* The attribute is set by the action, never by the template, so the
     selector is global or the compiler drops it as unused. */
  :global(.list-wrap[data-more])::after {
    content: '';
    position: absolute;
    left: 0;
    right: 0;
    bottom: 0;
    height: 1px;
    pointer-events: none;
    background: var(--color-rule-strong);
  }
</style>

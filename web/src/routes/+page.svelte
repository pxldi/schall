<script lang="ts">
  import { createMutation, createQuery, useQueryClient } from '@tanstack/svelte-query';
  import { api, type Overview, type OverviewDay } from '$lib/api';
  import { formatBytes, relativeTime } from '$lib/utils';
  import type { OverviewSession } from '$lib/api-types';
  import Cover from '$lib/components/Cover.svelte';
  import ErrorNote from '$lib/components/ErrorNote.svelte';

  // The first screen: mostly what was listened to, and a little on how much
  // music arrived. Every figure comes from one read, counted on the server in
  // this browser's zone, because "21:00" means nothing in UTC to somebody in
  // Vienna.
  const zone = Intl.DateTimeFormat().resolvedOptions().timeZone || 'UTC';
  const queryClient = useQueryClient();
  const overview = createQuery({
    queryKey: ['overview', zone],
    queryFn: () => api.overview(zone),
    // Listens arrive without an event, so the page re-reads on its own: often
    // while something is downloading, otherwise every five minutes.
    refetchInterval: (query) => (query.state.data?.arrived.downloading ? 15_000 : 5 * 60_000)
  });

  const want = createMutation({
    mutationFn: (trackId: string) => api.wantTrack(trackId),
    onSuccess: () => queryClient.invalidateQueries({ queryKey: ['overview'] })
  });

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
  const HEAT = ['#1a1420', '#2e2236', '#4a3556', '#6b4c80', '#9478b0', '#bfa3e8'];
  const heatMax = $derived(Math.max(1, ...(listening?.whenYouListen.cells.flat() ?? [0])));
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
    return `${day ? day + ' ' : ''}${clock(start)}–${session.ongoing ? 'now' : clock(end)}`;
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

  // ---- bar charts -------------------------------------------------------------

  // Bars are drawn in a viewBox of fixed width so the gaps and rounded ends
  // stay the same at any panel width; the SVG stretches to fill the row.
  const BAR_W = 600;
  function bars(days: OverviewDay[], height: number) {
    const max = Math.max(1, ...days.map((day) => day.count));
    const gap = 3;
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

{#snippet head(title: string, period: string, lead = false)}
  <div class="flex items-baseline gap-2.5">
    <h2 class="text-quiet-body font-semibold tracking-tight {lead ? 'text-ink' : 'text-ink-3'}">{title}</h2>
    {#if period}<span class="text-quiet-meta text-ink-4">{period}</span>{/if}
  </div>
{/snippet}

{#snippet quiet()}
  <p class="text-quiet-meta text-ink-4">No listens yet</p>
{/snippet}

<!-- What a panel shows while the overview is still being read: a breathing
     box the shape of what is coming, so the wait is nine panels loading
     rather than nine empty boxes. Each names itself for a screen reader and
     hides its boxes; the grid says busy. -->
{#snippet loadingChart(title: string, height: string)}
  <div class="flex grow flex-col gap-3" aria-busy="true" aria-label="Loading {title}">
    <span class="h-9 w-28 animate-pulse rounded-row bg-surface-thick" aria-hidden="true"></span>
    <span class="h-0 {height} w-full grow animate-pulse rounded-row bg-surface-thick" aria-hidden="true"></span>
  </div>
{/snippet}

{#snippet loadingRows(title: string, height: string)}
  <div class="flex flex-col gap-2 {height}" aria-busy="true" aria-label="Loading {title}">
    {#each Array.from({ length: 5 }) as _, index (index)}
      <span class="h-8 animate-pulse rounded-row bg-surface-thick" aria-hidden="true"></span>
    {/each}
  </div>
{/snippet}

{#snippet loadingFigures(title: string)}
  <div class="flex flex-col gap-3" aria-busy="true" aria-label="Loading {title}">
    <div class="flex gap-3">
      {#each Array.from({ length: 3 }) as _, index (index)}
        <span class="h-11 w-20 animate-pulse rounded-row bg-surface-thick" aria-hidden="true"></span>
      {/each}
    </div>
    <span class="h-1 animate-pulse rounded-full bg-surface-thick" aria-hidden="true"></span>
  </div>
{/snippet}

{#snippet tooltip(panel: string)}
  {#if tip && tip.panel === panel}
    <div
      role="tooltip"
      class="pointer-events-none absolute z-10 -translate-x-1/2 -translate-y-full whitespace-nowrap rounded-panel border border-line-regular bg-inset px-2 py-1 text-meta text-ink"
      style="left: {tip.x}px; top: {tip.y - 6}px"
    >
      {tip.text}
    </div>
  {/if}
{/snippet}

{#snippet row(cover: string | null, title: string, artist: string)}
  <span role="img" aria-label="Cover for {title}" class="h-8 w-8 shrink-0 overflow-hidden rounded-row border border-line-thin bg-surface-thin">
    <Cover src={cover ?? undefined} alt="" class="h-8 w-8 rounded-row object-cover" />
  </span>
  <span class="flex min-w-0 flex-1 flex-col">
    <span class="truncate text-body font-medium text-ink">{title}</span>
    <span class="truncate text-meta text-ink-4">{artist}</span>
  </span>
{/snippet}

<div class="flex flex-col gap-4 px-4 sm:px-6 py-6 lg:px-8">
  <!-- Hidden: the mast's highlight already says this is Overview, but that
       highlight is not a document heading. -->
  <h1 class="sr-only">Overview</h1>
  {#if $overview.isError}
    <ErrorNote error={$overview.error} retry={() => $overview.refetch()} />
  {:else}
  <div class="grid grid-cols-1 gap-4 lg:grid-cols-4" aria-busy={$overview.isPending}>
    <!-- Listens -->
    <section data-panel class="panel relative lg:col-span-2">
      {@render head('Listens', 'last 30 days', true)}
      {#if $overview.isPending}
        {@render loadingChart('Listens', 'min-h-[112px]')}
      {:else if listening?.available}
        <div class="flex items-baseline gap-3">
          <span class="font-mono text-quiet-display font-medium tabular-nums text-ink"
            >{listening.listens.total.toLocaleString()}</span
          >
          {#if change !== null}
            <!-- Against the thirty days before; the earlier count itself is
                 not shown. -->
            <span class="font-mono text-body {change >= 0 ? 'text-ok' : 'text-fail'}"
              title="{listening.listens.previous.toLocaleString()} in the 30 days before"
              >{change >= 0 ? '+' : ''}{change}%</span
            >
          {/if}
        </div>
        {@const columns = bars(listening.listens.days, 110)}
        <svg viewBox="0 0 {BAR_W} 112" preserveAspectRatio="none" class="h-0 min-h-[112px] w-full grow" aria-hidden="true">
          {#each columns as bar (bar.date)}
            <rect x={bar.x} y={bar.y} width={bar.width} height={bar.h} rx="2" fill={bar.today ? '#bfa3e8' : '#8f79b3'} />
            <rect
              x={bar.x - 1.5}
              y="0"
              width={bar.width + 3}
              height="112"
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
      {@render tooltip('listens')}
    </section>

    <!-- Most played -->
    <section data-panel class="panel lg:col-span-2">
      {@render head('Most played', 'this month', true)}
      {#if $overview.isPending}
        {@render loadingRows('Most played', 'min-h-[12.5rem]')}
      {:else if listening?.available}
        <!-- Every list here is keyed by position. Two files added in the same
             second with one title, or one album under two editions, made a
             content key collide and the throw blanked every panel after it
             (2026-09-08). The lists are replaced whole on each read, so a
             positional key loses nothing. -->
        <div class="panel-scroll min-h-[12.5rem]">
        <ul class="panel-list" use:more>
          {#each listening.mostPlayed as song, index (index)}
            <li class="flex h-10 shrink-0 items-center gap-2.5">
              {@render row(song.coverUrl, song.title, song.artist)}
              <!-- A held song is the ordinary case and carries no mark; the
                   exception is the one that can be wanted. -->
              {#if !song.inLibrary && song.wanted}
                <span class="text-meta text-ink-4">Wanted</span>
              {:else if !song.inLibrary && song.trackId}
                <button
                  type="button"
                  class="text-meta text-ink-4 transition hover:text-accent"
                  disabled={$want.isPending}
                  onclick={() => $want.mutate(song.trackId!)}>Want</button
                >
              {/if}
              <span class="w-6 text-right font-mono text-meta text-ink-2">{song.listens}</span>
            </li>
          {/each}
        </ul>
        </div>
      {:else if listening}
        {@render quiet()}
      {/if}
    </section>

    <!-- When you listen -->
    <section data-panel class="panel relative lg:col-span-2">
      {@render head('When you listen', '30 days')}
      {#if $overview.isPending}
        {@render loadingChart('When you listen', 'min-h-[7rem]')}
      {:else if listening?.available}
        <div class="flex items-baseline gap-5">
          <span class="flex items-baseline gap-2"><span class="fig">{hourLabel(listening.whenYouListen.peakHour)}</span><span class="text-body text-ink-4">peak</span></span>
          <span class="flex items-baseline gap-1.5"><span class="font-mono text-quiet-body text-ink">{listening.whenYouListen.busiestWeekday}</span><span class="text-quiet-meta text-ink-4">busiest</span></span>
        </div>
        <!-- One tab stop for 168 cells; arrow keys move the highlighted one,
             like a grid, so the group itself takes the keydown. -->
        <!-- svelte-ignore a11y_no_noninteractive_tabindex -->
        <!-- svelte-ignore a11y_no_noninteractive_element_interactions -->
        <div
          class="flex flex-col gap-[3px]"
          role="group"
          tabindex="0"
          aria-label="Listens by weekday and hour, arrow keys to explore"
          onkeydown={moveHeatFocus}
          onfocus={() => (heatFocused = true)}
          onblur={() => (heatFocused = false)}
        >
          {#each listening.whenYouListen.cells as hours, day (day)}
            <div class="grid items-center gap-[3px]" style="grid-template-columns: 2.25rem repeat(24, minmax(0, 1fr))">
              <span class="pr-1.5 text-right font-mono text-micro text-ink-4">{WEEKDAYS[day]}</span>
              {#each hours as count, hour (hour)}
                <span
                  class="aspect-square w-full rounded-[3px]"
                  style="background: {heatColour(count)}; {heatFocused && heatFocus.day === day && heatFocus.hour === hour
                    ? 'box-shadow: inset 0 0 0 2px var(--color-accent);'
                    : ''}"
                  aria-hidden="true"
                  onmouseenter={(event) => show(event, 'heat', heatCellText(day, hour, count))}
                  onmouseleave={hide}
                  onclick={() => touchHeatCell(day, hour, count)}
                ></span>
              {/each}
            </div>
          {/each}
          <div class="grid gap-[3px] pt-0.5 font-mono text-micro text-ink-4" style="grid-template-columns: 2.25rem repeat(24, minmax(0, 1fr))">
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
      {@render tooltip('heat')}
    </section>

    <!-- Top artists -->
    <section data-panel class="panel">
      {@render head('Top artists', 'this month')}
      {#if $overview.isPending}
        {@render loadingRows('Top artists', 'min-h-[12.5rem]')}
      {:else if listening?.available}
        <div class="panel-scroll min-h-[12.5rem]">
        <ul class="panel-list" use:more>
          {#each listening.topArtists as artist, index (index)}
            <li class="flex h-10 shrink-0 items-center gap-2.5">
              <span class="flex h-8 w-8 shrink-0 items-center justify-center overflow-hidden rounded-full bg-surface-thin text-body font-semibold text-ink-2" aria-hidden="true">
                <Cover src={artist.pictureUrl ?? undefined} alt="" class="h-8 w-8 rounded-full object-cover" fallback={artist.name.slice(0, 1)} />
              </span>
              <span class="min-w-0 flex-1 truncate text-body font-medium text-ink">{artist.name}</span>
              <span class="font-mono text-meta text-ink-2">{artist.listens}</span>
            </li>
          {/each}
        </ul>
        </div>
      {:else if listening}
        {@render quiet()}
      {/if}
    </section>

    <!-- Top albums -->
    <section data-panel class="panel">
      {@render head('Top albums', 'this month')}
      {#if $overview.isPending}
        {@render loadingRows('Top albums', 'min-h-[12.5rem]')}
      {:else if listening?.available}
        <div class="panel-scroll min-h-[12.5rem]">
        <ul class="panel-list" use:more>
          {#each listening.topAlbums as album, index (index)}
            <li class="flex h-12 shrink-0 items-center gap-2.5">
              <span role="img" aria-label="Cover for {album.title}" class="h-10 w-10 shrink-0 overflow-hidden rounded-row border border-line-thin bg-surface-thin">
                <Cover src={album.coverUrl ?? undefined} alt="" class="h-10 w-10 rounded-row object-cover" />
              </span>
              <span class="flex min-w-0 flex-1 flex-col">
                <span class="truncate text-body font-medium text-ink">{album.title}</span>
                <span class="truncate text-meta text-ink-4">{album.artist}</span>
              </span>
              <span class="font-mono text-meta text-ink-2">{album.listens}</span>
            </li>
          {/each}
        </ul>
        </div>
      {:else if listening}
        {@render quiet()}
      {/if}
    </section>

    <!-- Recently played -->
    <section data-panel class="panel lg:col-span-2">
      {@render head('Recently played', '')}
      {#if $overview.isPending}
        {@render loadingRows('Recently played', 'min-h-[15rem]')}
      {:else if listening?.available}
        <!-- One row per sitting: when it ran and how many songs, then who
             was on. A gap over thirty minutes starts the next one. -->
        <div class="panel-scroll min-h-[15rem]">
        <ul class="panel-list" use:more>
          {#each listening.sessions as session, index (index)}
            <li class="flex h-12 shrink-0 items-center gap-2.5">
              <!-- Up to three of the session's covers, overlapping, the first
                   on top. The slot is always three covers wide so the text
                   starts at the same x in every row. -->
              <span class="flex w-[3.75rem] shrink-0" aria-hidden="true">
                {#each session.coverUrls.slice(0, 3) as url, index (index)}
                  <span
                    class="relative block h-7 w-7 overflow-hidden rounded-row border border-line-thin bg-surface-thin ring-2 ring-[var(--color-inset)]"
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
        {@render quiet()}
      {/if}
    </section>

    <!-- Recently added -->
    <section data-panel class="panel lg:col-span-2">
      {@render head('Recently added', '')}
      {#if $overview.isPending}
        {@render loadingRows('Recently added', 'min-h-[12.5rem]')}
      {:else if data}
        <div class="panel-scroll min-h-[12.5rem]">
        <ul class="panel-list" use:more>
          {#each data.recentlyAdded as file, index (index)}
            <li class="flex h-10 shrink-0 items-center gap-2.5">
              {@render row(file.coverUrl, file.title, file.artist)}
              <span class="font-mono text-meta text-ink-4">{relativeTime(file.addedAt).replace(' ago', '')}</span>
            </li>
          {/each}
        </ul>
        </div>
      {/if}
    </section>

    <!-- Downloaded -->
    <section data-panel class="panel relative lg:col-span-2">
      {@render head('Downloaded', '14 days')}
      {#if $overview.isPending}
        {@render loadingChart('Downloaded', 'min-h-[7rem]')}
      {:else if data}
        <div class="flex items-baseline gap-3">
          <span class="fig">{data.arrived.today.toLocaleString()}</span>
          <span class="text-body text-ink-4">today</span>
          <span class="font-mono text-quiet-body text-ink-2">{data.arrived.downloading.toLocaleString()}</span>
          <span class="text-body text-ink-4">downloading</span>
        </div>
        {@const columns = bars(data.arrived.days, 96)}
        <svg viewBox="0 0 {BAR_W} 98" preserveAspectRatio="none" class="h-0 min-h-[98px] w-full grow" aria-hidden="true">
          {#each columns as bar (bar.date)}
            <rect x={bar.x} y={bar.y} width={bar.width} height={bar.h} rx="2" fill={bar.today ? '#bfa3e8' : '#8f79b3'} />
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
      {@render tooltip('arrived')}
    </section>

    <!-- Library -->
    <section data-panel class="panel relative lg:col-span-2">
      {@render head('Library', '')}
      {#if $overview.isPending}
        {@render loadingFigures('Library')}
      {:else if data}
        <div class="flex gap-3">
          <div class="flex flex-col gap-0.5">
            <span class="font-mono text-quiet-lead text-ink">{fileCount.toLocaleString()}</span>
            <span class="text-quiet-meta text-ink-3">{fileCount === 1 ? 'file' : 'files'}</span>
          </div>
          <div class="flex flex-col gap-0.5">
            <span class="font-mono text-quiet-lead text-ink">{formatBytes(data.library.totalBytes)}</span>
            <span class="text-quiet-meta text-ink-3">total size</span>
          </div>
        </div>
        {@const growth = growthPath(data.library.growth, 240, 64)}
        <svg viewBox="0 0 240 64" preserveAspectRatio="none" class="h-0 min-h-16 w-full grow" aria-label="Library files over the last year">
          <path d={growth.area} fill="#bfa3e8" fill-opacity="0.12" />
          <path d={growth.line} fill="none" stroke="#bfa3e8" stroke-width="2" vector-effect="non-scaling-stroke" />
          {#if growth.points.length > 0}
            {@const end = growth.points[growth.points.length - 1]}
            <circle cx={end.x} cy={end.y} r="3" fill="#bfa3e8" />
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
              class="h-1 overflow-hidden rounded-full bg-surface-thin"
              role="img"
              aria-label="{formatBytes(data.library.storage!.usedBytes)} used of {formatBytes(data.library.storage!.totalBytes)}"
              onmouseenter={(event) => show(event, 'library', `${formatBytes(data.library.storage!.usedBytes)} used of ${formatBytes(data.library.storage!.totalBytes)}`)}
              onmouseleave={hide}
            >
              <div class="h-full rounded-full bg-accent" style="width: {roomLeft.usedShare}%"></div>
            </div>
          </div>
        {/if}
      {/if}
      {@render tooltip('library')}
    </section>
  </div>
  {/if}
</div>

<style>
  .panel {
    display: flex;
    flex-direction: column;
    gap: 12px;
    min-width: 0;
    min-height: 0;
    padding: 14px 18px 16px;
    border-radius: var(--radius-panel);
    --panel-bg: var(--color-inset);
    background: var(--panel-bg);
    border: 1px solid var(--color-line-thin);
  }
  /* Every panel sits on Inset. Listens and Most played used to be raised on
     Surface Regular, and two plum panels above seven dark ones read as two
     different things. The lead panels keep their brighter heading instead. */
  /* Charts grow with `grow`, not `flex-1`: a percentage basis in a panel of
     indefinite height resolves to the SVG's aspect-ratio height, and a
     year of growth 970px wide wanted 259px. A zero basis lets min-height set
     the floor. */
  /* A list fills whatever height the row of panels gives it and scrolls
     inside; `contain: size` keeps its 25 rows out of the row's height
     calculation, so the charts beside it set the height and the list follows.
     The cut-off last row says there is more; a scrollbar in a panel this
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
  }
  .panel-list::-webkit-scrollbar {
    display: none;
  }
  /* The fade at the foot while there is more below: the panel's own colour
     coming up over the last visible row, blurred a little so the row reads
     as continuing under it. The attribute is set by the action, never by the
     template, so the selector is global or the compiler drops it as unused. */
  :global(.panel-scroll[data-more])::after {
    content: '';
    position: absolute;
    left: 0;
    right: 0;
    bottom: 0;
    height: 2.5rem;
    pointer-events: none;
    background: linear-gradient(to bottom, transparent, var(--panel-bg));
    backdrop-filter: blur(2px);
    -webkit-backdrop-filter: blur(2px);
    mask-image: linear-gradient(to bottom, transparent, black);
    -webkit-mask-image: linear-gradient(to bottom, transparent, black);
  }
  .fig {
    font-family: var(--font-mono);
    font-variant-numeric: tabular-nums;
    font-size: var(--text-quiet-lead);
    font-weight: 500;
    line-height: 1;
    letter-spacing: -0.02em;
    color: var(--color-ink);
  }
</style>

<script lang="ts">
  import { untrack } from 'svelte';
  import { page } from '$app/state';
  import {
    Download,
    Ellipsis,
    House,
    Inbox,
    Library,
    ListMusic,
    Search,
    Settings,
    Users
  } from '@lucide/svelte';

  let { children } = $props();

  // The chrome is one 208px mast down the left edge: the destinations from the
  // top, the seven destinations as icon-and-name rows, and search at the
  // foot. The mast is always open and every row prints its own name, so
  // there are no tooltips. There is no readout and no per-room mark — the
  // owner scrapped counts and dots alike to keep the mast clean, so what is
  // waiting is read on the Overview and on the pages themselves. Below the
  // breakpoint five tabs move to a fixed bar along the bottom of the screen.
  // The four other destinations move into More because the mast that carried
  // them is gone; a destination reached that way gets a small title bar
  // above the content, because the tab bar for it only ever says "More".
  //
  // The mast's own highlight is not a document heading — a screen reader
  // does not read a `class="active"` row — so every page still carries its
  // own `<h1>`, visible or not. `<main>` is the landing spot for the skip
  // link, the first focusable element in the document.

  const destinations = [
    { href: '/', label: 'Overview', icon: House },
    { href: '/artists', label: 'Artists', icon: Users },
    { href: '/playlists', label: 'Playlists', icon: ListMusic },
    { href: '/downloads?view=open', label: 'Downloads', icon: Download },
    { href: '/review', label: 'Review', icon: Inbox },
    { href: '/library', label: 'Library', icon: Library },
    { href: '/settings/jobs', label: 'Settings', icon: Settings }
  ];

  const phoneTabs = [destinations[0], destinations[1], destinations[4]];
  const moreDestinations = [destinations[2], destinations[3], destinations[5], destinations[6]];
  let moreOpen = $state(false);
  let moreSheet = $state<HTMLElement | null>(null);

  // Below `lg` the five tabs and the mast's active highlight are the same
  // thing, so a tab route needs no other title. A destination reached through
  // More has nothing on screen to say which one it is — the bar under it just
  // says "More" — so this bar names it. It reuses the same four rows the
  // sheet draws; the label just repeats.
  const phoneTitle = $derived(moreDestinations.find((destination) => isActive(destination.href)));

  // A release and a source run are reached from the library and belong to it,
  // so the nav says so. They keep their own paths because they are things with
  // addresses, not a view of a list.
  function isActive(href: string) {
    const path = page.url.pathname;
    // The overview is the one destination whose address is a single slash, and
    // every other address starts with one. It is the open page only when it is
    // the whole address.
    if (href === '/') return path === '/';
    // A destination is its first segment and nothing more. Two of the seven
    // point deeper than that — Downloads names a filter, Settings names a
    // category — because neither stops being that destination once the
    // reader moves within it.
    const root = `/${href.split('?')[0].split('/')[1]}`;
    if (root === '/library' && path.startsWith('/releases')) return true;
    return path.startsWith(root);
  }

  const moreActive = $derived(moreDestinations.some((destination) => isActive(destination.href)));

  function closeMore() {
    moreOpen = false;
  }

  // The sheet is a dialog on a page that has no other one open at the same
  // time, so it takes the same contract FollowArtistModal does: opening moves
  // focus in and remembers what it left, closing gives that back. Untracked,
  // or the sheet mounting a frame after `moreOpen` flips would count as a
  // reason to run this again and hand focus back to the page early.
  $effect(() => {
    if (!moreOpen) return;
    const opener = document.activeElement;
    untrack(() => moreSheet)?.querySelector<HTMLElement>('a[href]')?.focus();
    return () => {
      if (opener instanceof HTMLElement && opener.isConnected) opener.focus();
    };
  });

  // Tab and Shift-Tab cycle inside the sheet and never reach the page behind,
  // the same trap FollowArtistModal runs on its own panel.
  function trapTab(event: KeyboardEvent) {
    if (!moreSheet) return;
    const stops = [...moreSheet.querySelectorAll<HTMLElement>('a[href], button:not([disabled])')];
    if (stops.length === 0) return;

    const first = stops[0];
    const last = stops[stops.length - 1];
    const here = document.activeElement;
    const outside = !(here instanceof Node) || !moreSheet.contains(here);

    if (event.shiftKey && (outside || here === first)) {
      event.preventDefault();
      last.focus();
    } else if (!event.shiftKey && (outside || here === last)) {
      event.preventDefault();
      first.focus();
    }
  }

  function onWindowKeydown(event: KeyboardEvent) {
    if (!moreOpen) return;
    if (event.key === 'Escape') {
      closeMore();
      return;
    }
    if (event.key === 'Tab') trapTab(event);
  }
</script>

<svelte:window onkeydown={onWindowKeydown} />

<div class="min-h-screen bg-ground text-ink">
  <a
    href="#main"
    class="sr-only rounded-control border border-line-thin bg-surface-regular px-3 py-2 text-ink focus:not-sr-only focus:fixed focus:left-2 focus:top-2 focus:z-40"
  >
    Skip to content
  </a>

  <!-- The mast, carried by the `.fascia` class: the one gradient in the
       application running black at the top into plum at the bottom.
       `env(safe-area-inset-left)` clears a notch turned sideways. -->
  <header
    class="fascia fixed inset-y-0 left-0 z-30 hidden w-52 flex-col pb-3 pt-4 lg:flex"
    style="padding-left: env(safe-area-inset-left);"
  >
    <nav class="flex w-full flex-col gap-0.5" aria-label="Sections">
      {#each destinations as destination (destination.href)}
        {@const active = isActive(destination.href)}
        <a
          href={destination.href}
          class:active
          aria-current={active ? 'page' : undefined}
          class="rail-item"
        >
          <destination.icon size={18} strokeWidth={2} aria-hidden="true" />
          <span>{destination.label}</span>
        </a>
      {/each}
    </nav>

    <button
      class="rail-item mt-auto"
      onclick={() => window.dispatchEvent(new CustomEvent('schall:search'))}
    >
      <Search size={18} strokeWidth={2} aria-hidden="true" />
      <span>Search</span>
    </button>

    <!-- The wordmark at the foot, with the build it is. The version is what
         CI tagged the image with, or "dev" outside CI. -->
    <div class="mt-3 flex flex-col gap-1 px-5">
      <a href="/" class="wordmark self-start whitespace-nowrap text-lg text-ink-2">schall</a>
      <span class="font-mono text-micro text-ink-4" title="Build">{__SCHALL_VERSION__}</span>
    </div>
  </header>

  <!-- Inert while More is open: a background control cannot take a click, a
       tap or a Tab stop, and a screen reader does not read it either. Without
       it the sheet's own focus trap is the only thing standing between a
       keyboard reader and the artist filters behind the scrim. -->
  <main id="main" tabindex="-1" class="pb-16 lg:pb-0 lg:pl-52" inert={moreOpen}>
    {#if phoneTitle}
      <div class="border-b border-line-thin px-4 py-2 text-body font-medium text-ink lg:hidden">
        {phoneTitle.label}
      </div>
    {/if}
    {@render children()}
  </main>

  {#if moreOpen}
    <div class="fixed inset-0 z-40 lg:hidden" role="presentation">
      <button
        type="button"
        class="absolute inset-0 bg-ground/80"
        aria-label="Close More"
        onclick={closeMore}
      ></button>
      <div
        bind:this={moreSheet}
        id="phone-more-sheet"
        class="absolute inset-x-0 bottom-[calc(3.5rem+env(safe-area-inset-bottom))] rounded-t-2xl border border-line-thin bg-surface-regular px-4 pb-3 pt-4"
        role="dialog"
        aria-modal="true"
        aria-labelledby="phone-more-title"
      >
        <h2 id="phone-more-title" class="mb-2 text-body font-semibold">More</h2>
        <nav aria-label="More destinations" class="-mx-4">
          {#each moreDestinations as destination (destination.href)}
            {@const active = isActive(destination.href)}
            <a
              href={destination.href}
              aria-current={active ? 'page' : undefined}
              class="flex min-h-11 w-full items-center gap-3 border-t border-line-thin px-4 text-body text-ink-2 transition hover:bg-surface-thick hover:text-ink aria-[current=page]:text-accent"
              onclick={closeMore}
            >
              <destination.icon size={18} strokeWidth={2} aria-hidden="true" />
              <span>{destination.label}</span>
            </a>
          {/each}
        </nav>
      </div>
    </div>
  {/if}

  <!-- The phone version of the mast: five tabs fixed to the bottom edge and
       clear of the home indicator. The remaining destinations live in More.
       Inert along with main while the sheet sits over it, the More button
       included — a second press on it while it is already open is not a
       fresh way in. -->
  <nav
    class="fixed inset-x-0 bottom-0 z-30 flex items-stretch justify-between border-t border-shell-line bg-ground px-1 lg:hidden"
    style="padding-bottom: env(safe-area-inset-bottom);"
    aria-label="Sections, compact"
    inert={moreOpen}
  >
    {#each phoneTabs as destination (destination.href)}
      {@const active = isActive(destination.href)}
      <a
        href={destination.href}
        class:active
        aria-current={active ? 'page' : undefined}
        aria-label={destination.label}
        class="tap nav-item relative h-14 flex-1 flex-col items-center justify-center gap-1 border-b-0 border-t-2 px-0 {active
          ? '!border-t-accent'
          : '!border-t-transparent'}"
      >
        <destination.icon size={18} strokeWidth={2} aria-hidden="true" />
        <span class="min-w-0 whitespace-nowrap text-center text-micro leading-tight">{destination.label}</span>
      </a>
    {/each}
    <button
      class="tap nav-item h-14 flex-1 flex-col items-center justify-center gap-1 border-b-0 border-t-2 !border-t-transparent px-0"
      onclick={() => window.dispatchEvent(new CustomEvent('schall:search'))}
      aria-label="Search"
    >
      <Search size={18} strokeWidth={2} aria-hidden="true" />
      <span class="min-w-0 whitespace-nowrap text-center text-micro leading-tight">Search</span>
    </button>
    <button
      type="button"
      class:active={moreActive}
      class="tap nav-item relative h-14 flex-1 flex-col items-center justify-center gap-1 border-b-0 border-t-2 px-0 {moreActive
        ? '!border-t-accent'
        : '!border-t-transparent'}"
      onclick={() => (moreOpen = !moreOpen)}
      aria-label="More"
      aria-expanded={moreOpen}
      aria-controls="phone-more-sheet"
      aria-current={moreActive ? 'page' : undefined}
    >
      <Ellipsis size={18} strokeWidth={2} aria-hidden="true" />
      <span class="min-w-0 whitespace-nowrap text-center text-micro leading-tight">More</span>
    </button>
  </nav>
</div>

<script lang="ts">
  import { afterNavigate, beforeNavigate } from '$app/navigation';
  import { page, updated } from '$app/state';
  import { loadNewerBuild } from '$lib/reload';
  import { duoton } from '$lib/duoton/page.svelte';
  import { openSearch } from '$lib/search';
  import type { IconName } from '$lib/icons';
  import Icon from './Icon.svelte';
  import Print from './Print.svelte';

  let { children } = $props();

  // The chrome is a top bar sitting on the page's print (ADR Duoton): the
  // wordmark, the seven destinations as pills, and search pushed right. There
  // are no counts or state marks on it: what is waiting is read on the Overview and
  // on the pages themselves. There is no phone form; the phone is served by the
  // native app, decided 2026-09-20.
  //
  // The bar's highlight is not a document heading, so every page still carries
  // its own <h1>, visible or not. `<main>` is the landing spot for the skip
  // link, the first focusable element in the document.

  // Each destination has a Misprint icon. Below 1280px the bar shows the
  // icons alone, named by a tooltip and by the link itself, so the seven still
  // fit on one line.
  const destinations: { href: string; label: string; icon: IconName }[] = [
    { href: '/', label: 'Overview', icon: 'listen' },
    { href: '/artists', label: 'Artists', icon: 'artists' },
    { href: '/playlists', label: 'Playlists', icon: 'playlist' },
    { href: '/downloads?view=open', label: 'Downloads', icon: 'download' },
    { href: '/review', label: 'Review', icon: 'checklist' },
    { href: '/library', label: 'Library', icon: 'library' },
    { href: '/settings/jobs', label: 'Settings', icon: 'settings' }
  ];

  // A destination is its first segment and nothing more. Two of the seven
  // point deeper than that (Downloads names a filter, Settings names a
  // category) because neither stops being that destination once the reader
  // moves within it.
  const rootOf = (href: string) => `/${href.split('?')[0].split('/')[1]}`;
  const roots = destinations.map((destination) => rootOf(destination.href));

  // A release and a source run have their own addresses and are reached from
  // Artists, Playlists, Review and Library alike. The bar keeps the section the
  // reader opened it from rather than jumping to Library; a release opened
  // straight from a link belongs to Library.
  let section = $state<string | null>(null);
  afterNavigate(({ to }) => {
    if (!to) return;
    const root = rootOf(to.url.pathname);
    if (root !== '/releases') section = roots.includes(root) ? root : null;
  });

  // The section a release belongs to, read before the address so the bar
  // follows the remembered section whichever of the two changes.
  const releaseRoot = $derived(section ?? '/library');

  function isActive(href: string) {
    const fromSection = releaseRoot;
    const path = page.url.pathname;
    // The overview is the one destination whose address is a single slash, and
    // every other address starts with one. It is the open page only when it is
    // the whole address.
    if (href === '/') return path === '/';
    const root = rootOf(href);
    if (path.startsWith('/releases')) return root === fromSection;
    return path.startsWith(root);
  }

  // SvelteKit asks every minute whether a newer build is out (svelte.config.js).
  // Once one is, the next page change loads it in full.
  beforeNavigate((navigation) => {
    loadNewerBuild(navigation, updated.current, (href) => location.assign(href));
  });

  // The page's two inks live on <html>, so the tokens that name them (the
  // accent, the focus ring, the primary button) resolve to the page's colours
  // everywhere, overlays included.
  //
  // They change in the same frame as the page, with every transition held for
  // that frame. A button easing its background from the old accent started
  // only after the new page's first frame was drawn, and arrived late with it.
  $effect(() => {
    const html = document.documentElement;
    html.dataset.inksChanging = '';
    html.style.setProperty('--color-duo-dark', duoton.inks.dark);
    html.style.setProperty('--color-duo-light', duoton.inks.light);
    // Reading a layout back applies the new inks to the whole page while
    // transitions are off.
    void document.body.offsetHeight;
    delete html.dataset.inksChanging;
  });

  // The bar is a wash over the print and turns solid once the print has
  // scrolled out from under it.
  let scrollY = $state(0);
  let remPx = $state(16);
  $effect(() => {
    remPx = parseFloat(getComputedStyle(document.documentElement).fontSize) || 16;
  });
  const solid = $derived(scrollY > (duoton.height - 2.75) * remPx);
</script>

<svelte:window bind:scrollY />

<div
  class="duoton-app relative min-h-screen text-ink"
  style="--print-height: {duoton.height}rem; --topbar-height: 2.75rem;"
>
  <a
    href="#main"
    class="sr-only rounded-control border border-line-thin bg-surface-regular px-3 py-2 text-ink focus:not-sr-only focus:fixed focus:left-2 focus:top-2 focus:z-40"
  >
    Skip to content
  </a>

  <div class="absolute inset-x-0 top-0 overflow-hidden" style="height: var(--print-height);">
    <Print covers={duoton.covers} mode={duoton.mode} class="absolute inset-0" />
    <div class="print-fade absolute inset-0" aria-hidden="true"></div>
  </div>

  <header
    class="topbar sticky top-0 z-30 flex h-[var(--topbar-height)] items-center gap-0.5 px-4 lg:px-6"
    class:solid
  >
    <a href="/" class="wordmark mr-2 shrink-0 lg:mr-4 whitespace-nowrap text-[1.0625rem] text-ink" title="Build {__SCHALL_VERSION__}">schall</a>
    <nav class="flex min-w-0 items-center gap-0.5" aria-label="Sections">
      {#each destinations as destination (destination.href)}
        {@const active = isActive(destination.href)}
        <a
          href={destination.href}
          class:active
          aria-current={active ? 'page' : undefined}
          class="topbar-item whitespace-nowrap"
          title={destination.label}
        >
          <Icon name={destination.icon} size="sm" />
          <span class="max-xl:sr-only">{destination.label}</span>
        </a>
      {/each}
    </nav>

    <!-- Below 1024px the field folds to its icon, so the seven destinations
         still fit on one line in half a screen. -->
    <button class="topbar-search ml-auto shrink-0" onclick={openSearch} aria-label="Search">
      <Icon name="search" size="sm" />
      <span class="max-lg:hidden">Search</span>
    </button>
  </header>

  <main id="main" tabindex="-1" class="page-view relative z-[2]">
    {@render children()}
  </main>
</div>

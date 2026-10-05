<script lang="ts">
  import { page } from '$app/state';
  import { Search } from '@lucide/svelte';
  import { duoton } from '$lib/duoton/page.svelte';
  import { openSearch } from '$lib/search';
  import Print from './Print.svelte';

  let { children } = $props();

  // The chrome is a top bar sitting on the page's print (ADR Duoton): the
  // wordmark, the seven destinations as pills, and search pushed right. Every
  // destination prints its own name, so there are no tooltips, and there are
  // no counts or state marks on it: what is waiting is read on the Overview and
  // on the pages themselves. There is no phone form; the phone is served by the
  // native app, decided 2026-09-20.
  //
  // The bar's highlight is not a document heading, so every page still carries
  // its own <h1>, visible or not. `<main>` is the landing spot for the skip
  // link, the first focusable element in the document.

  const destinations = [
    { href: '/', label: 'Overview' },
    { href: '/artists', label: 'Artists' },
    { href: '/playlists', label: 'Playlists' },
    { href: '/downloads?view=open', label: 'Downloads' },
    { href: '/review', label: 'Review' },
    { href: '/library', label: 'Library' },
    { href: '/settings/jobs', label: 'Settings' }
  ];

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

  // The page's two inks live on <html>, so the tokens that name them (the
  // accent, the focus ring, the primary button) resolve to the page's colours
  // everywhere, overlays included.
  $effect(() => {
    const root = document.documentElement.style;
    root.setProperty('--color-duo-dark', duoton.inks.dark);
    root.setProperty('--color-duo-light', duoton.inks.light);
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
    class="topbar sticky top-0 z-30 flex h-[var(--topbar-height)] items-center gap-0.5 px-4 sm:px-6"
    class:solid
  >
    <a href="/" class="wordmark mr-4 text-[1.0625rem] text-ink" title="Build {__SCHALL_VERSION__}">schall</a>
    <nav class="flex min-w-0 items-center gap-0.5" aria-label="Sections">
      {#each destinations as destination (destination.href)}
        {@const active = isActive(destination.href)}
        <a
          href={destination.href}
          class:active
          aria-current={active ? 'page' : undefined}
          class="topbar-item whitespace-nowrap"
        >
          {destination.label}
        </a>
      {/each}
    </nav>

    <button class="topbar-search ml-auto" onclick={openSearch}>
      <Search size={14} strokeWidth={2} aria-hidden="true" />
      <span>Search</span>
      <kbd aria-hidden="true">Ctrl K</kbd>
    </button>
  </header>

  <main id="main" tabindex="-1" class="relative z-[2]">
    {@render children()}
  </main>
</div>

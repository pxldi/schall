<script lang="ts">
  import { page } from '$app/state';
  import { Download, House, Inbox, Library, ListMusic, Search, Settings, Users } from '@lucide/svelte';

  let { children } = $props();

  // The chrome is one 208px mast down the left edge: the seven destinations
  // as icon-and-name rows from the top, and search at the foot. The mast is
  // always open and every row prints its own name, so there are no tooltips.
  // There is no readout and no per-room mark — the owner scrapped counts and
  // dots alike to keep the mast clean, so what is waiting is read on the
  // Overview and on the pages themselves. There is no phone form of the
  // mast: the phone is served by the native app, decided 2026-09-20.
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
</script>

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
    class="fascia fixed inset-y-0 left-0 z-30 flex w-52 flex-col pb-3 pt-4"
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

  <main id="main" tabindex="-1" class="pl-52">
    {@render children()}
  </main>
</div>

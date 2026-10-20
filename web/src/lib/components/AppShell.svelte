<script lang="ts">
  import { afterNavigate, beforeNavigate, goto } from '$app/navigation';
  import { page, updated } from '$app/state';
  import { createQuery } from '@tanstack/svelte-query';
  import { api } from '$lib/api';
  import { trackFocus, useKeys } from '$lib/keys.svelte';
  import { changed, glide } from '$lib/motion.svelte';
  import { loadNewerBuild } from '$lib/reload';
  import { openSearch } from '$lib/search';
  import { STATE_GLYPH } from '$lib/states';
  import { followSystemTheme } from '$lib/theme.svelte';
  import Icon from './Icon.svelte';
  import JobStatus from './JobStatus.svelte';
  import Keycap from './Keycap.svelte';
  import KeySheet from './KeySheet.svelte';
  import KeyStrip from './KeyStrip.svelte';

  let { children } = $props();

  // The chrome every screen is drawn inside (ADR 0042): a 40px strip across
  // the top with the wordmark, the seven destinations each with its number
  // key, and what is running; a 32px strip along the foot naming the keys of
  // the screen. The current destination is drawn inverted. There is no phone
  // form; the phone is served by the native app, decided 2026-09-20.
  //
  // The strip is not a document heading, so every page still carries its own
  // <h1>. `<main>` is the landing spot for the skip link, the first focusable
  // element in the document.
  const destinations: { href: string; label: string }[] = [
    { href: '/', label: 'Overview' },
    { href: '/artists', label: 'Artists' },
    { href: '/playlists', label: 'Playlists' },
    { href: '/downloads?view=open', label: 'Downloads' },
    { href: '/review', label: 'Review' },
    { href: '/library', label: 'Library' },
    { href: '/settings/jobs', label: 'Settings' }
  ];

  // A destination is its first segment and nothing more. Two of the seven
  // point deeper than that because neither stops being that destination once
  // the reader moves within it.
  const rootOf = (href: string) => `/${href.split('?')[0].split('/')[1]}`;
  const roots = destinations.map((destination) => rootOf(destination.href));

  // A release and a source run are reached from Artists, Playlists, Review
  // and Library alike. The strip keeps the section the reader opened it from;
  // a release opened straight from a link belongs to Library.
  let section = $state<string | null>(null);
  afterNavigate(({ to }) => {
    if (!to) return;
    const root = rootOf(to.url.pathname);
    if (root !== '/releases') section = roots.includes(root) ? root : null;
  });
  const releaseRoot = $derived(section ?? '/library');

  function isActive(href: string) {
    // Read first, so the strip follows the remembered section whichever of
    // the two changes.
    const fromSection = releaseRoot;
    const path = page.url.pathname;
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

  // How many questions Review holds: the wants and the paused imports, the
  // same two totals the Review page counts All by.
  const wantQuestions = createQuery({
    queryKey: ['review-queue', 'count'],
    queryFn: () => api.reviewQueue(1, 0)
  });
  const folderQuestions = createQuery({
    queryKey: ['downloads', 'questions', 'count'],
    queryFn: () => api.downloads({ view: 'questions', limit: 1 })
  });
  const reviewCount = $derived(($wantQuestions.data?.total ?? 0) + ($folderQuestions.data?.total ?? 0));
  const reviewTicked = changed(() => reviewCount);

  let sheetOpen = $state(false);

  useKeys(
    () => [
      ...destinations.map((destination, index) => ({
        keys: [String(index + 1)],
        verb: `open ${destination.label}`,
        run: () => void goto(destination.href)
      })),
      { keys: ['/'], verb: 'search', run: openSearch },
      { keys: ['?'], verb: 'all keys', run: () => (sheetOpen = true) },
      // J and K move a list's cursor. A screen without a list still claims
      // them, so the letters never open search.
      { keys: ['j', 'k'], verb: 'move', hidden: true, enabled: () => false, run: () => {} }
    ],
    { global: true }
  );

  $effect(() => followSystemTheme());
  $effect(() => trackFocus());
</script>

<div class="relative min-h-screen bg-ground pb-8 text-ink">
  <a
    href="#main"
    class="sr-only border border-rule bg-panel px-3 py-2 text-ink focus:not-sr-only focus:fixed focus:left-2 focus:top-2 focus:z-40"
  >
    Skip to content
  </a>

  <header
    class="sticky top-0 z-30 flex h-10 items-stretch border-b border-rule bg-ground"
    style="view-transition-name: top-strip"
  >
    <a
      href="/"
      class="wordmark flex shrink-0 items-center border-r border-rule px-4 text-lead text-ink"
      title="Build {__SCHALL_VERSION__}"
    >
      schall
    </a>
    <nav class="no-scrollbar relative flex min-w-0 items-stretch overflow-x-auto" aria-label="Sections" use:glide={{ axis: 'x', key: page.url.pathname }}>
      {#each destinations as destination, index (destination.href)}
        {@const active = isActive(destination.href)}
        <a
          href={destination.href}
          aria-current={active ? 'page' : undefined}
          data-cursor={active}
          title={destination.label}
          aria-describedby={destination.label === 'Review' && reviewCount > 0 ? 'review-count' : undefined}
          class="relative flex items-center gap-1.5 whitespace-nowrap border-r border-rule px-3 text-meta transition {active
            ? 'on-ink font-medium'
            : 'text-ink-3 hover:bg-raised hover:text-ink'}"
        >
          <Keycap key={String(index + 1)} class="border-0 px-0 text-ink-4" />
          <span>{destination.label}</span>
          {#if destination.label === 'Review' && reviewCount > 0}
            <span class="flex items-center gap-1 text-ink" aria-hidden="true">
              <span class="mono text-meta" class:tick={reviewTicked.now} onanimationend={reviewTicked.settle}>
                {reviewCount}
              </span>
              <Icon name={STATE_GLYPH.decide} size="sm" class={active ? '' : 'text-decide'} />
            </span>
          {/if}
        </a>
      {/each}
    </nav>
    {#if reviewCount > 0}
      <span id="review-count" class="sr-only">{reviewCount} to decide</span>
    {/if}

    <div class="ml-auto flex shrink-0 items-stretch">
      <JobStatus />
      <button
        type="button"
        class="flex items-center gap-2 border-l border-rule px-3 text-meta text-ink-3 transition hover:bg-raised hover:text-ink"
        onclick={openSearch}
        aria-label="Search"
      >
        <Icon name="search" size="sm" />
        <span class="max-xl:hidden">Search</span>
        <Keycap key="/" />
      </button>
    </div>
  </header>

  <main id="main" tabindex="-1" class="relative">
    {@render children()}
  </main>

  <KeyStrip onsheet={() => (sheetOpen = true)} />
  <KeySheet bind:open={sheetOpen} />
</div>

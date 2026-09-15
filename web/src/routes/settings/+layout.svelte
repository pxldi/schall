<script lang="ts">
  import type { Snippet } from 'svelte';
  import { page } from '$app/state';

  // Settings was one page: eleven cards stacked in a single column, 3,400
  // pixels of scroll, and no way to reach the one you wanted except to
  // recognise it going past. A row of chips fixed that once already; this
  // narrows it further into a section rail beside the content, so a category
  // stays in view while the one before it scrolls past rather than sitting in
  // a strip that has to be found again after a jump.
  //
  // Below `lg` there is no room for a 150px rail beside a form, so the same
  // five links move above the content as a horizontally scrollable row
  // instead: the rail and the row are the same list, drawn twice and shown
  // one at a time by breakpoint.
  //
  // Five categories, each an address of its own, grouped by what a card
  // configures rather than by service name: Sources holds the four things
  // with a connect-and-test cycle, Library holds everything that changes
  // what is kept and how files are named — including the audio check that
  // used to be its own category, Automation holds the background features
  // nobody checks daily, Phone is where a phone is given a token, and Jobs is
  // the queue that runs all of it.
  const categories = [
    { href: '/settings/sources', name: 'Sources' },
    { href: '/settings/library', name: 'Library' },
    { href: '/settings/automation', name: 'Automation' },
    { href: '/settings/phone', name: 'Phone' },
    { href: '/settings/jobs', name: 'Jobs' }
  ];

  let { children }: { children: Snippet } = $props();

  const current = $derived(
    categories.find((category) => page.url.pathname.startsWith(category.href))
  );
</script>

<svelte:head><title>{current?.name ?? 'Settings'} · Schall</title></svelte:head>

<!-- No visible page title: the mast's own highlight already says this is
     Settings, and the rail (or, below `lg`, the row above the content) says
     which part of it. Each category page carries its own hidden h1, since
     neither highlight is a document heading. -->
<div class="flex flex-col gap-4 px-4 sm:px-6 py-5 lg:flex-row lg:gap-8">
  <!-- Below `lg` the same links become a scrollable row above the content,
       because there is no width left to give a fixed-width rail beside a
       320px form. -->
  <nav aria-label="Settings sections" class="no-scrollbar flex gap-5 overflow-x-auto lg:hidden">
    {#each categories as category (category.href)}
      {@const active = category.href === current?.href}
      <a
        href={category.href}
        aria-current={active ? 'page' : undefined}
        class="flex h-8 shrink-0 items-center border-b-2 px-0 text-body transition {active
          ? 'border-accent text-ink'
          : 'border-transparent text-ink-2 hover:text-ink'}"
      >
        {category.name}
      </a>
    {/each}
  </nav>

  <nav
    aria-label="Settings sections"
    class="hidden w-[9.375rem] shrink-0 flex-col gap-0.5 lg:flex"
  >
    {#each categories as category (category.href)}
      {@const active = category.href === current?.href}
      <a
        href={category.href}
        aria-current={active ? 'page' : undefined}
        class="flex h-8 items-center rounded-control px-2.5 text-body transition {active
          ? 'bg-surface-regular font-medium text-ink'
          : 'text-ink-2 hover:text-ink'}"
      >
        {category.name}
      </a>
    {/each}
  </nav>

  <div class="min-w-0 flex-1">
    {@render children()}
  </div>
</div>

<!-- The credit for the two open projects Schall is built on. It used to sit
     at the foot of every release page; a person reads a release many times
     and Settings once, so it belongs here instead. -->
<footer class="border-t border-line-thin px-4 sm:px-6 py-6 text-meta text-ink-4">
  <p>
    Release data from
    <a
      class="underline underline-offset-2 transition hover:text-ink-2"
      href="https://musicbrainz.org"
      target="_blank"
      rel="noreferrer">MusicBrainz</a
    >. Audio identified by
    <a
      class="underline underline-offset-2 transition hover:text-ink-2"
      href="https://acoustid.org"
      target="_blank"
      rel="noreferrer">AcoustID</a
    >.
  </p>
</footer>

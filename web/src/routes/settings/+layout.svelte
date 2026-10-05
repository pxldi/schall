<script lang="ts">
  import type { Snippet } from 'svelte';
  import { page } from '$app/state';
  import Hero from '$lib/components/Hero.svelte';
  import { useNewestPrint } from '$lib/duoton';

  // Settings was one page: eleven cards stacked in a single column, 3,400
  // pixels of scroll, and no way to reach the one you wanted except to
  // recognise it going past. A row of chips fixed that once already; this
  // narrows it further into a section rail beside the content, so a category
  // stays in view while the one before it scrolls past rather than sitting in
  // a strip that has to be found again after a jump.
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

  // Settings has no music of its own, so it is printed from the newest
  // arrivals: a short strip, since the page is a form and not a poster.
  useNewestPrint({ height: 12, count: 4 });

  const current = $derived(
    categories.find((category) => page.url.pathname.startsWith(category.href))
  );
</script>

<svelte:head><title>{current?.name ?? 'Settings'} · Schall</title></svelte:head>

<Hero title="Settings" size="m" />

<!-- Each category page carries a hidden h2 naming itself, under the Hero's h1. -->
<div class="flex flex-row gap-10 px-4 pb-10 pt-6 sm:px-6">
  <nav
    aria-label="Settings sections"
    class="sticky top-[calc(var(--topbar-height)+1.5rem)] flex w-[10.5rem] shrink-0 flex-col gap-0.5 self-start"
  >
    {#each categories as category (category.href)}
      {@const active = category.href === current?.href}
      <a
        href={category.href}
        aria-current={active ? 'page' : undefined}
        class="flex h-9 items-center rounded-[0.625rem] px-3 text-body font-semibold transition {active
          ? 'bg-duo-light/12 text-ink'
          : 'text-ink-2 hover:text-ink'}"
      >
        {category.name}
      </a>
    {/each}
  </nav>

  <div class="settings-body min-w-0 flex-1">
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

<style>
  /* Settings in Duoton (ADR Duoton): sections are blocks on the ground with a
     bold heading, as the prototype draws them. Card and FormGrid keep their
     look on the other pages that use them, so the change is scoped here. */
  .settings-body :global(section.rounded-card) {
    border: 0;
    border-radius: 0;
    padding: 0 0 2.125rem;
  }

  .settings-body :global(section.rounded-card h2),
  .settings-body :global(section > h2) {
    font-size: 1.375rem;
    font-weight: 800;
    letter-spacing: -0.02em;
    line-height: 1.15;
  }

  .settings-body :global(form label) {
    font-weight: 600;
  }
</style>

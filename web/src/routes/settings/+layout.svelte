<script lang="ts">
  import type { Snippet } from 'svelte';
  import { page } from '$app/state';
  import PageHeader from '$lib/components/PageHeader.svelte';
  import AppearanceSetting from '$lib/components/AppearanceSetting.svelte';
  import { glide } from '$lib/motion.svelte';

  // Settings was one page: eleven sections stacked in a single column, 3,400
  // pixels of scroll, and no way to reach the one you wanted except to
  // recognise it going past. It is five addresses now, shown as a strip of
  // tabs above one column of sections.
  //
  // Five categories, grouped by what a section
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

<!-- The appearance choice is a page action: it is the one setting that takes
     effect at once and is kept in this browser alone. -->
<PageHeader title="Settings">
  {#snippet actions()}
    <AppearanceSetting />
  {/snippet}
</PageHeader>

<!-- Each category is an address of its own, shown as a strip of tabs. The
     current one is the inverted block, which slides to the next on navigation. -->
<div class="px-5 pt-3">
  <nav
    aria-label="Settings sections"
    class="relative inline-flex h-7 items-stretch border border-rule"
    use:glide={{ axis: 'x', key: current?.href }}
  >
    {#each categories as category (category.href)}
      {@const active = category.href === current?.href}
      <a
        href={category.href}
        aria-current={active ? 'page' : undefined}
        data-cursor={active}
        class="relative flex items-center border-r border-rule px-3 text-body transition last:border-r-0 {active
          ? 'on-ink font-medium'
          : 'text-ink-3 hover:bg-raised hover:text-ink'}"
      >
        {category.name}
      </a>
    {/each}
  </nav>
</div>

<!-- Each category page carries a hidden h2 naming itself, under the PageHeader's h1. -->
<div class="px-5 pb-6 pt-6">
  <div class="settings-body @container max-w-[56rem]">
    {@render children()}
  </div>
</div>

<!-- The credit for the two open projects Schall is built on. It used to sit
     at the foot of every release page; a person reads a release many times
     and Settings once, so it belongs here instead. -->
<footer class="border-t border-rule px-5 py-6 text-meta text-ink-4">
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

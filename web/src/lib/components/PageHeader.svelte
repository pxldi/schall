<script lang="ts">
  import type { Snippet } from 'svelte';
  import { goto } from '$app/navigation';
  import { previousScreen } from '$lib/navigation.svelte';
  import { useKeys } from '$lib/keys.svelte';

  // The title spot, at one place on every page (ADR 0042): a one-line path of
  // links in mono to the places above this one, then the page's name. A line of context and the page's
  // actions sit under it. Nothing goes above the path; there are no kicker
  // labels.
  //
  // Esc goes back: to the screen the reader came from when there is one,
  // otherwise to the last link of the path.

  let {
    title,
    path = [],
    level = 1,
    sub,
    actions,
    class: className = ''
  }: {
    title: string;
    /** The way here, outermost first. The page itself is the title, not a
     *  link on the path. */
    path?: { label: string; href: string }[];
    /** 2 where the page's own <h1> is the path's first entry (Review). */
    level?: 1 | 2;
    sub?: Snippet;
    actions?: Snippet;
    class?: string;
  } = $props();

  const back = $derived(previousScreen.href ?? path.at(-1)?.href);

  useKeys(() =>
    back
      ? [
          {
            keys: ['escape'],
            verb: 'back',
            order: 90,
            run: () => {
              if (previousScreen.href && previousScreen.oneStepBack) history.back();
              else if (back) void goto(back);
            }
          }
        ]
      : []
  );
</script>

<header class="flex flex-col gap-1 px-5 pt-3.5 {className}">
  <!-- The line is drawn empty on a top-level page, so the title sits at the
       same height on every page. -->
  {#if path.length}
    <nav aria-label="Path" class="mono flex h-4 min-w-0 items-center gap-1.5 text-micro text-ink-4">
      {#each path as step, index (step.href)}
        {#if index > 0}<span aria-hidden="true">/</span>{/if}
        <a href={step.href} class="truncate transition hover:text-ink hover:underline">{step.label}</a>
      {/each}
    </nav>
  {:else}
    <div class="h-4" aria-hidden="true"></div>
  {/if}
  <div class="flex flex-wrap items-end justify-between gap-x-6 gap-y-2">
    <div class="min-w-0 flex-[1_1_20rem]">
      {#if level === 1}
        <h1 class="truncate text-title font-semibold text-ink">{title}</h1>
      {:else}
        <h2 class="truncate text-title font-semibold text-ink">{title}</h2>
      {/if}
      {#if sub}
        <div class="mt-1 flex flex-wrap items-baseline gap-x-3 gap-y-1 text-body text-ink-2">{@render sub()}</div>
      {/if}
    </div>
    {#if actions}
      <div class="flex shrink-0 flex-wrap items-center gap-1.5">{@render actions()}</div>
    {/if}
  </div>
</header>

<script lang="ts">
  import { page } from '$app/state';
  import { ChevronRight } from '@lucide/svelte';
  import PageHeader from '$lib/components/PageHeader.svelte';
  import EmptyPanel from '$lib/components/EmptyPanel.svelte';
  import Button from '$lib/components/Button.svelte';

  // What SvelteKit draws when a page cannot be reached: an address that names
  // nothing, or a load that threw. Until now this file did not exist, so the
  // reader got SvelteKit's own default — the number and the word `Not Found`,
  // in the browser's font, outside the application's frame, with nowhere to go.
  //
  // design-plan 3.6 is the rule this keeps: nothing is ever a dead end. The
  // page says what happened to the address the reader typed and offers the way
  // back, because a person who mistyped an address has exactly one useful next
  // action and the interface can name it.

  // 404 is the only status worth a sentence of its own, because it is the only
  // one with an ordinary cause a reader can act on. Everything else is the
  // application failing, and the reader can do the same one thing about it.
  const missing = $derived(page.status === 404);
  const heading = $derived(missing ? 'That address is not part of Schall' : 'This page did not load');
</script>

<svelte:head>
  <title>{missing ? 'Not found' : 'Error'} · Schall</title>
</svelte:head>

<PageHeader>
  <h1 class="font-display text-2xl font-bold text-ink">
    {missing ? 'Not found' : 'Error'}
  </h1>
</PageHeader>

<div class="px-4 sm:px-6 py-5">
  <EmptyPanel role={missing ? 'idle' : 'fail'} {heading}>
    {#if missing}
      <p class="text-body leading-[1.65] text-ink-2">
        Nothing answers to <span class="numeric text-ink">{page.url.pathname}</span>.
      </p>
    {/if}

    <!-- `data-sveltekit-reload` is what makes the link work from an error the
         router itself raised: it leaves the client-side router out of it and
         asks the server for the page, so a broken route cannot swallow the way
         out of itself. -->
    <div class="flex flex-wrap items-center gap-2">
      <Button href="/" data-sveltekit-reload>Go to Overview</Button>
      {#if !missing}
        <Button variant="outline" onclick={() => location.reload()}>Reload</Button>
      {/if}
    </div>

    {#if !missing && page.error?.message}
      <!-- The exact words the failure left behind. Two sentences are on the
           screen; this is the third thing, and it is behind a disclosure so it
           stays in the product without being the first thing read. -->
      <details class="group border-t border-line-thin pt-2">
        <summary
          class="tap-tall flex cursor-pointer list-none items-center gap-1.5 text-meta text-ink-3 transition hover:text-ink-2 [&::-webkit-details-marker]:hidden"
        >
          <ChevronRight size={12} class="transition-transform group-open:rotate-90" />
          What went wrong
        </summary>
        <p class="reveal mt-1 text-meta leading-[1.65] text-ink-2">{page.error.message}</p>
      </details>
    {/if}
  </EmptyPanel>
</div>

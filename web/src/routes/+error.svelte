<script lang="ts">
  import { page } from '$app/state';
  import ErrorNote from '$lib/components/ErrorNote.svelte';
  import Hero from '$lib/components/Hero.svelte';
  import Button from '$lib/components/Button.svelte';
  import { useNewestPrint } from '$lib/duoton';
  import { openSearch } from '$lib/search';

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

  // Printed from the three newest arrivals, tall, so a wrong address still
  // lands on the collection rather than on a blank page.
  useNewestPrint({ height: 26, count: 3 });
</script>

<svelte:head>
  <title>{missing ? 'Not found' : 'Error'} · Schall</title>
</svelte:head>

<Hero title={missing ? '404' : 'Error'} size="xl">
  {#snippet sub()}
    {#if missing}
      <!-- The address is named, because the ordinary cause is a typed or
           stale link and the reader cannot check one they are not shown. -->
      <b class="font-semibold text-ink">Nothing at {page.url.pathname}</b>
      <span>It may have moved, or the link is wrong.</span>
    {:else}
      <b class="font-semibold text-ink">This page didn't load.</b>
      <span>Reload it, or go back to Overview.</span>
    {/if}
  {/snippet}
</Hero>

<div class="flex flex-col gap-4 px-4 pb-10 pt-5 sm:px-6">
  <!-- `data-sveltekit-reload` is what makes the link work from an error the
       router itself raised: it leaves the client-side router out of it and
       asks the server for the page, so a broken route cannot swallow the way
       out of itself. -->
  <div class="flex flex-wrap items-center gap-2">
    <Button href="/" data-sveltekit-reload>Go to Overview</Button>
    {#if missing}
      <Button variant="outline" onclick={openSearch}>Search</Button>
    {:else}
      <Button variant="outline" onclick={() => location.reload()}>Reload</Button>
    {/if}
  </div>

  {#if !missing && page.error?.message}
    <!-- Given the message, not the object: SvelteKit's error is a plain object
         the note would print as [object Object]. No action line, because the
         line under the title already names Reload. -->
    <ErrorNote
      error={page.error.message}
      fallback="The page threw an error."
      action=""
      class="max-w-[40rem]"
    />
  {/if}
</div>

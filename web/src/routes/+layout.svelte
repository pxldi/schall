<script lang="ts">
  import { QueryClientProvider } from '@tanstack/svelte-query';
  import { onMount } from 'svelte';
  import { onNavigate } from '$app/navigation';
  import { page } from '$app/state';
  import { connectEvents } from '$lib/events';
  import { pageTransitions } from '$lib/motion.svelte';
  import { trackNavigation } from '$lib/navigation.svelte';
  import { queriesRestored, queryClient } from '$lib/query-client';
  import AppShell from '$lib/components/AppShell.svelte';
  import CommandPalette from '$lib/components/CommandPalette.svelte';
  import '../styles.css';

  let { children } = $props();

  // The pages wait for the kept answers before they first draw
  // ($lib/query-client).
  let restored = $state(false);
  void queriesRestored.then(() => (restored = true));

  // One stream for the whole application: the views that care are invalidated
  // by topic, and there is no reason to open a connection per page.
  onMount(() => connectEvents(queryClient));

  // The one place the application watches where the reader goes. It belongs
  // here because this layout is mounted for as long as the application is
  // running, and a page that only draws its back arrow once its data has
  // arrived would otherwise start listening after the navigation it needed to
  // hear about.
  trackNavigation();

  // Each page change fades the page and holds the two strips still (ADR 0042).
  pageTransitions(onNavigate);
</script>

<QueryClientProvider client={queryClient}>
  <AppShell>
    {#if restored}
      <!-- SvelteKit keeps a page when only its parameters change, and the
           detail pages read their id once. Going from one artist to another
           would leave the page, its data and its actions on the first. A
           change of query string keeps the page. -->
      {#key JSON.stringify(page.params)}
        {@render children()}
      {/key}
    {/if}
  </AppShell>
  <!-- Mounted here rather than in the shell: it is summoned from every page and
       has to outlive all of them, and it is not part of the chrome — it rests on
       top of whatever is showing and blocks none of it. -->
  <CommandPalette />
</QueryClientProvider>

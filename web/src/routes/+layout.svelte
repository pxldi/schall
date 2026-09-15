<script lang="ts">
  import { QueryClient, QueryClientProvider } from '@tanstack/svelte-query';
  import { onMount } from 'svelte';
  import { connectEvents } from '$lib/events';
  import { queryRetry } from '$lib/errors';
  import { trackNavigation } from '$lib/navigation.svelte';
  import AppShell from '$lib/components/AppShell.svelte';
  import CommandPalette from '$lib/components/CommandPalette.svelte';
  import '../styles.css';

  let { children } = $props();

  const queryClient = new QueryClient({
    defaultOptions: {
      queries: {
        staleTime: 30_000,
        refetchOnWindowFocus: false,
        retry: queryRetry
      }
    }
  });

  // One stream for the whole application: the views that care are invalidated
  // by topic, and there is no reason to open a connection per page.
  onMount(() => connectEvents(queryClient));

  // The one place the application watches where the reader goes. It belongs
  // here because this layout is mounted for as long as the application is
  // running, and a page that only draws its back arrow once its data has
  // arrived would otherwise start listening after the navigation it needed to
  // hear about.
  trackNavigation();
</script>

<QueryClientProvider client={queryClient}>
  <AppShell>
    {@render children()}
  </AppShell>
  <!-- Mounted here rather than in the shell: it is summoned from every page and
       has to outlive all of them, and it is not part of the chrome — it rests on
       top of whatever is showing and blocks none of it. -->
  <CommandPalette />
</QueryClientProvider>

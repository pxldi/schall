<script lang="ts">
  import { createQuery } from '@tanstack/svelte-query';
  import { api } from '$lib/api';
  import { changed } from '$lib/motion.svelte';
  import { STATE_GLYPH, STATE_TEXT, type StateRole } from '$lib/states';
  import Icon from './Icon.svelte';

  // What is running, at the right of the top strip: transfers in flight and
  // whether the download source can search. Both come from the dashboard
  // summary, which the event stream invalidates as transfers move.

  const dashboard = createQuery({
    queryKey: ['dashboard'],
    queryFn: () => api.dashboard(),
    refetchInterval: (query) => ((query.state.data?.activeDownloadCount ?? 0) > 0 ? 15_000 : false)
  });

  const inFlight = $derived($dashboard.data?.activeDownloadCount ?? 0);
  const ticked = changed(() => inFlight);

  const source = $derived.by((): { role: StateRole; label: string } | null => {
    const data = $dashboard.data;
    if (!data) return null;
    if (data.sourceLoggedOutSince) return { role: 'fail', label: 'slskd is not logged in to Soulseek' };
    if (data.sourceSilencedUntil) return { role: 'decide', label: 'slskd searches paused' };
    return { role: 'ok', label: 'slskd connected' };
  });
</script>

<div class="flex h-full items-center gap-3 border-l border-rule px-3 text-meta text-ink-3">
  {#if inFlight > 0}
    <a
      href="/downloads?view=open"
      class="flex items-center gap-1.5 text-busy transition hover:text-ink"
      title={inFlight === 1 ? '1 download in flight' : `${inFlight} downloads in flight`}
    >
      <Icon name={STATE_GLYPH.busy} size="sm" class="animate-spin" />
      <span class="mono" class:tick={ticked.now} onanimationend={ticked.settle}>{inFlight}</span>
      <span class="sr-only">{inFlight === 1 ? 'download in flight' : 'downloads in flight'}</span>
    </a>
  {/if}
  {#if source}
    <a
      href="/settings/sources"
      class="flex items-center gap-1.5 transition hover:text-ink"
      title={source.label}
      aria-label={source.label}
    >
      <span class="mono">slskd</span>
      <Icon name={STATE_GLYPH[source.role]} size="sm" class={STATE_TEXT[source.role]} />
    </a>
  {/if}
</div>

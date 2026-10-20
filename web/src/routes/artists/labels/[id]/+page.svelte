<script lang="ts">
  // One label: what it published, and how much of it the library holds. The
  // monitor level decides what counts as missing; every release is listed
  // regardless, the same rule the artist discography page follows.
  import { page } from '$app/state';
  import { goto } from '$app/navigation';
  import { createQuery } from '@tanstack/svelte-query';
  import { tick } from 'svelte';
  import { toStore } from 'svelte/store';
  import { api, ApiError, type LabelRelease, type MonitorLevel } from '$lib/api';
  import { calendarDate } from '$lib/utils';
  import { queryRetry } from '$lib/errors';
  import { useKeys } from '$lib/keys.svelte';
  import { glide } from '$lib/motion.svelte';
  import Button from '$lib/components/Button.svelte';
  import Icon from '$lib/components/Icon.svelte';
  import Skeleton from '$lib/components/Skeleton.svelte';
  import Cover from '$lib/components/Cover.svelte';
  import EmptyPanel from '$lib/components/EmptyPanel.svelte';
  import ErrorNote from '$lib/components/ErrorNote.svelte';
  import PageHeader from '$lib/components/PageHeader.svelte';
  import Settle from '$lib/components/Settle.svelte';

  const labelID = $derived(page.params.id ?? '');

  const detail = createQuery(
    toStore(() => ({
      queryKey: ['labels', labelID],
      queryFn: () => api.label(labelID),
      enabled: labelID.length > 0,
      // A label the server does not have will not appear by asking again.
      retry: (spent: number, error: Error) =>
        !(error instanceof ApiError && error.status === 404) && queryRetry(spent, error)
    }))
  );

  // An old link, or a label unfollowed and since removed. Reloading cannot
  // bring it back, so the page says so and leads to the list of labels.
  const missing = $derived($detail.error instanceof ApiError && $detail.error.status === 404);

  const label = $derived($detail.data?.label);
  const releases = $derived($detail.data?.releases ?? []);

  function line(release: LabelRelease): string {
    const year = release.releaseDate ? release.releaseDate.slice(0, 4) : '—';
    if (!release.monitored) return `${year} · unmonitored`;
    if (release.trackCount === 0) return `${year} · no track list`;
    return `${year} · ${release.ownedTrackCount} of ${release.trackCount}`;
  }

  const monitorNames: Record<MonitorLevel, string> = {
    everything: 'Everything',
    main: 'Main releases',
    albums_eps: 'Albums & EPs',
    owned: 'Owned only'
  };

  // A release is held when every track of it maps onto a file.
  const heldCount = $derived(
    releases.filter((release) => release.trackCount > 0 && release.ownedTrackCount >= release.trackCount)
      .length
  );

  // The cursor (ADR 0042): none until J or K or a tab onto a row, then one
  // inverted block that glides between releases.
  let cursorId = $state('');
  let listElement = $state<HTMLElement>();

  function moveCursor(by: number) {
    if (!releases.length) return;
    const at = releases.findIndex((release) => release.id === cursorId);
    const next = at < 0 ? 0 : Math.min(Math.max(at + by, 0), releases.length - 1);
    cursorId = releases[next].id;
    void tick().then(() =>
      listElement?.querySelector('[data-cursor="true"]')?.scrollIntoView?.({ block: 'nearest' })
    );
  }

  useKeys(() => [
    {
      keys: ['j', 'k', 'arrowdown', 'arrowup'],
      caps: ['j', 'k'],
      verb: 'move',
      order: 0,
      enabled: () => releases.length > 0,
      run: (event) => moveCursor(event.key === 'j' || event.key === 'ArrowDown' ? 1 : -1)
    },
    ...(cursorId
      ? [
          {
            keys: ['enter'],
            verb: 'open',
            order: 1,
            run: () => void goto(`/releases/${cursorId}`)
          }
        ]
      : [])
  ]);
</script>


<svelte:head><title>{label ? `${label.name} · Schall` : 'Label · Schall'}</title></svelte:head>

{#if missing}
  <PageHeader
    title="Label not found"
    path={[
      { label: 'Artists', href: '/artists' },
      { label: 'Labels', href: '/artists/labels' }
    ]}
  >
    {#snippet sub()}
      <span>No label here. The link may be old.</span>
    {/snippet}
  </PageHeader>
  <div class="px-5 py-5"><Button href="/artists/labels">Open labels</Button></div>
{:else}
  <div class="grid items-start lg:grid-cols-[minmax(0,1fr)_21.25rem]">
    <div class="flex min-w-0 flex-col pb-8">
      <!-- The header settles out of its skeleton like the releases under it, so
           the title does not cut in over the boxes. -->
      <Settle pending={$detail.isPending}>
        {#snippet placeholder()}
          <section class="flex flex-col gap-2 px-5 pb-4 pt-3.5" aria-hidden="true">
            <Skeleton class="h-4 w-32" />
            <Skeleton class="h-8 w-96 max-w-full" />
            <Skeleton class="h-4 w-56" />
          </section>
        {/snippet}
        {#if label}
          <PageHeader
            title={label.name}
            path={[
              { label: 'Artists', href: '/artists' },
              { label: 'Labels', href: '/artists/labels' }
            ]}
          >
            {#snippet sub()}
              <span>
                <b class="numeric font-semibold text-ink">{releases.length}</b>
                {releases.length === 1 ? 'release' : 'releases'}
              </span>
              {#if label.type}<span>{label.type}</span>{/if}
              {#if label.country}<span>{label.country}</span>{/if}
              <span>
                {label.lastRefreshedAt
                  ? `updated ${calendarDate(label.lastRefreshedAt)}`
                  : 'not refreshed yet'}
              </span>
            {/snippet}
          </PageHeader>
        {:else}
          <h1 class="sr-only">Label</h1>
        {/if}
      </Settle>

      <div class="flex flex-col gap-4 pt-5">
        {#if $detail.isError}
          <div class="px-5"><ErrorNote error={$detail.error} retry={() => $detail.refetch()} /></div>
        {:else}
          <Settle pending={$detail.isPending}>
            {#snippet placeholder()}
              <div
                class="max-h-[calc(100dvh-24rem)] overflow-hidden"
                role="status"
                aria-label="Loading label releases"
              >
                {#each Array(24) as _, placeholderIndex (placeholderIndex)}
                  <div class="h-9 border-b border-rule px-5 py-2" aria-hidden="true">
                    <Skeleton class="h-full" />
                  </div>
                {/each}
              </div>
            {/snippet}
            {#if releases.length === 0}
              <div class="px-5">
                <EmptyPanel role="idle" heading={label?.lastRefreshedAt ? 'No releases' : 'Not fetched yet'}>
                  <p class="text-body text-ink-2">
                    {label?.lastRefreshedAt
                      ? 'MusicBrainz lists no releases for this label.'
                      : 'Not read from MusicBrainz yet.'}
                  </p>
                </EmptyPanel>
              </div>
            {:else}
              <div
                class="grid grid-cols-[1.75rem_minmax(0,1fr)_minmax(0,14rem)_12rem] label h-7 items-center gap-x-3 border-b border-rule-strong px-5"
                aria-hidden="true"
              >
                <span></span>
                <span>Release</span>
                <span>Artist</span>
                <span>Released</span>
              </div>
              <div class="relative" bind:this={listElement} use:glide={{ key: cursorId }}>
                {#each releases as release (release.id)}
                  {@const here = release.id === cursorId}
                  <div
                    class="row scroll-mt-24 scroll-mb-12 grid grid-cols-[1.75rem_minmax(0,1fr)_minmax(0,14rem)_12rem] items-center gap-x-3 px-5 {here
                      ? 'on-ink'
                      : ''}"
                    data-release={release.id}
                    data-cursor={here}
                    data-held={release.ownedTrackCount > 0 ? 'some' : 'none'}
                    onfocusin={() => (cursorId = release.id)}
                  >
                    <!-- A release with nothing held is faded, the same as on an
                         artist's page, so what the library has stands out. -->
                    <span
                      class="block size-7 overflow-hidden rounded-cover {release.ownedTrackCount === 0
                        ? 'opacity-[0.38]'
                        : ''}"
                    >
                      <Cover
                        src={release.hasCover ? `/api/v1/albums/${release.id}/cover?cached=1` : undefined}
                        seed={release.id}
                        class="size-full object-cover"
                      />
                    </span>
                    <a
                      href="/releases/{release.id}"
                      class="truncate text-body after:absolute after:inset-0 {here ? 'font-medium' : ''}"
                      title="{release.title} · {line(release)}">{release.title}</a
                    >
                    <span class="truncate text-meta {here ? '' : 'text-ink-2'}">{release.artistName}</span>
                    <span class="mono truncate text-meta {here ? '' : 'text-ink-3'}">{line(release)}</span>
                  </div>
                {/each}
              </div>
            {/if}
          </Settle>
        {/if}
      </div>
    </div>

    <!-- The inspector: the facts as keys and values. -->
    <aside
      class="flex min-w-0 flex-col gap-5 border-rule bg-panel p-5 max-lg:border-t lg:sticky lg:top-10 lg:max-h-[calc(100dvh-4.5rem)] lg:min-h-[calc(100dvh-4.5rem)] lg:overflow-y-auto lg:border-l"
      aria-label="About this label"
    >
      {#if label}
        <dl class="grid grid-cols-[5.5rem_minmax(0,1fr)] gap-x-3 gap-y-2.5 text-body">
          <dt class="label pt-0.5">Following</dt>
          <dd class="text-ink-2">
            {#if label.followed}
              <span class="flex items-center gap-1.5">
                <Icon name="follow" size="sm" class="text-ink-3" /> Following
              </span>
            {:else}
              Not following
            {/if}
          </dd>

          {#if label.followed}
            <dt class="label pt-0.5">Monitor</dt>
            <dd class="text-ink-2">{monitorNames[label.monitorLevel]}</dd>
          {/if}

          {#if label.type}
            <dt class="label pt-0.5">Type</dt>
            <dd class="text-ink-2">{label.type}</dd>
          {/if}
          {#if label.country}
            <dt class="label pt-0.5">Country</dt>
            <dd class="mono text-meta text-ink-2">{label.country}</dd>
          {/if}

          <dt class="label pt-0.5">Owned</dt>
          <dd class="text-ink-2">
            {#if releases.length}
              <span class="text-meta"
                ><span class="mono">{heldCount} of {releases.length}</span> releases held</span
              >
            {:else}
              <span class="text-ink-4">No releases</span>
            {/if}
          </dd>

          <dt class="label pt-0.5">Updated</dt>
          <dd class="text-meta text-ink-2">
            {#if label.lastRefreshedAt}
              <span class="mono">{calendarDate(label.lastRefreshedAt)}</span>
            {:else}
              not refreshed yet
            {/if}
          </dd>
        </dl>
      {:else if $detail.isPending}
        <div class="flex flex-col gap-3" aria-hidden="true">
          <Skeleton class="h-4 w-40" />
          <Skeleton class="h-4 w-32" />
          <Skeleton class="h-4 w-36" />
        </div>
      {/if}
    </aside>
  </div>
{/if}

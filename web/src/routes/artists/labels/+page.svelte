<script lang="ts">
  import Skeleton from '$lib/components/Skeleton.svelte';
  // Labels a person follows the way they follow an artist: a standing request
  // to keep what the label publishes complete. The follow feed brings in a
  // followed label's new releases as ordinary wants, the same loop and the
  // same proof an artist's releases go through — this page only records the
  // standing request and shows what the label has published so far.
  //
  // Unlike the artists list, this one is not paged: both halves of it are
  // labels somebody chose (queries/labels.sql explains why), so it stays
  // small and reads as one list rather than a browse.
  import { createMutation, createQuery, queryOptions, useQueryClient } from '@tanstack/svelte-query';
  import { tick } from 'svelte';
  import { toStore } from 'svelte/store';
  import Icon from '$lib/components/Icon.svelte';
  import { goto } from '$app/navigation';
  import { api, type LabelListItem, type MonitorLevel } from '$lib/api';
  import { useKeys } from '$lib/keys.svelte';
  import { changed, glide, slideInto } from '$lib/motion.svelte';
  import { calendarDate } from '$lib/utils';
  import Button from '$lib/components/Button.svelte';
  import Cover from '$lib/components/Cover.svelte';
  import PageHeader from '$lib/components/PageHeader.svelte';
  import { coverSrc } from '$lib/cover-size';
  import ControlRail from '$lib/components/ControlRail.svelte';
  import EmptyPanel from '$lib/components/EmptyPanel.svelte';
  import ErrorNote from '$lib/components/ErrorNote.svelte';
  import FollowLabelModal from '$lib/components/FollowLabelModal.svelte';
  import OwnedBar from '$lib/components/OwnedBar.svelte';
  import PillSelect from '$lib/components/PillSelect.svelte';
  import Settle from '$lib/components/Settle.svelte';

  // Cover, label, owned, monitor, updated, actions. The header row and every
  // label row use the same columns.
  const COLUMNS = 'grid-cols-[1.75rem_minmax(0,1fr)_11rem_10rem_9rem_8rem]';

  const queryClient = useQueryClient();
  let showAddLabel = $state(false);
  // Narrows the already-loaded list in the browser rather than asking the
  // server again: the list is never paged (see the comment above), so every
  // label a search could find is already on hand.
  let search = $state('');
  // The label whose unfollow is being confirmed, one at a time — confirming
  // replaces the row's own actions, so there is nowhere to put a second
  // confirmation.
  let confirming = $state('');

  const monitorLevels: { value: MonitorLevel; name: string }[] = [
    { value: 'everything', name: 'Everything' },
    { value: 'main', name: 'Main releases' },
    { value: 'albums_eps', name: 'Albums & EPs' },
    { value: 'owned', name: 'Owned only' }
  ];

  const labelsOptions = queryOptions({
    queryKey: ['labels'],
    queryFn: () => api.labels(),
    // The safety net for a refresh the event stream missed: only while one is
    // actually in flight, the same rule every other list on this installation
    // follows. 'pending' means no refresh job exists, so it is not in flight.
    refetchInterval: (query) =>
      (query.state.data?.items ?? []).some((item) =>
        ['queued', 'running'].includes(item.refreshStatus)
      )
        ? 15_000
        : false
  });
  const labels = createQuery(toStore(() => labelsOptions));
  // Enough rows to fill a tall 4K window.
  const labelSkeletonCount = 60;

  const dashboard = createQuery({ queryKey: ['dashboard'], queryFn: api.dashboard });

  const items = $derived($labels.data?.items ?? []);
  const filtered = $derived(
    search.trim()
      ? items.filter((label) => label.name.toLowerCase().includes(search.trim().toLowerCase()))
      : items
  );
  const labelsTotal = $derived($labels.data?.total);
  const followedTotal = $derived($labels.data?.followedCount);
  const artistsTotal = $derived($dashboard.data?.artistCount);

  function invalidate() {
    return Promise.all([
      queryClient.invalidateQueries({ queryKey: ['labels'] }),
      queryClient.invalidateQueries({ queryKey: ['dashboard'] })
    ]);
  }

  const refresh = createMutation({
    mutationFn: (id: string) => api.refreshLabel(id),
    onSuccess: () => invalidate()
  });

  const setMonitor = createMutation({
    mutationFn: (input: { id: string; level: MonitorLevel }) =>
      api.setLabelMonitorLevel(input.id, input.level),
    onSuccess: () => invalidate()
  });

  // Following a label Schall already holds a row for is the same promotion
  // following a held artist is: the row survives an earlier unfollow, so this
  // is that decision made again rather than a fresh fetch.
  const follow = createMutation({
    mutationFn: (label: LabelListItem) =>
      api.followLabel({
        musicbrainzId: label.musicbrainzId,
        name: label.name,
        type: label.type,
        country: label.country,
        disambiguation: label.disambiguation,
        score: 0
      }),
    onSuccess: () => invalidate()
  });

  const unfollow = createMutation({
    mutationFn: (id: string) => api.unfollowLabel(id),
    onSuccess: () => {
      confirming = '';
      return invalidate();
    }
  });

  // The cursor (ADR 0042): none until J or K or a tab onto a row, then one
  // inverted block that glides between labels. Kept by id, so a refresh that
  // reorders the list does not move it to another label.
  let cursorId = $state('');
  let listElement = $state<HTMLElement>();
  $effect(() => {
    void search;
    cursorId = '';
  });

  function moveCursor(by: number) {
    if (!filtered.length) return;
    const at = filtered.findIndex((label) => label.id === cursorId);
    const next = at < 0 ? 0 : Math.min(Math.max(at + by, 0), filtered.length - 1);
    cursorId = filtered[next].id;
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
      enabled: () => filtered.length > 0,
      run: (event) => moveCursor(event.key === 'j' || event.key === 'ArrowDown' ? 1 : -1)
    },
    ...(cursorId
      ? [
          {
            keys: ['enter'],
            verb: 'open',
            order: 1,
            run: () => void goto(`/artists/labels/${cursorId}`)
          }
        ]
      : [])
  ]);

  // The label figure ticks when a follow lands, never on the first draw.
  const labelsTick = changed(() => labelsTotal);
</script>


<svelte:head><title>Labels · Schall</title></svelte:head>

<PageHeader title="Labels" path={[{ label: 'Artists', href: '/artists' }]}>
  {#snippet sub()}
    <!-- The counts hold their place while the list is read, as /artists does,
         so the line does not grow when they land. A failed read shows a dash. -->
    {#if labelsTotal !== undefined}
      <span>
        <b
          class="numeric font-semibold text-ink"
          class:tick={labelsTick.now}
          onanimationend={labelsTick.settle}>{labelsTotal.toLocaleString()}</b
        >
        {labelsTotal === 1 ? 'label' : 'labels'}
      </span>
    {:else if $labels.isError}
      <span><b class="numeric font-semibold text-ink">–</b> labels</span>
    {:else}
      <span>
        <Skeleton class="numeric inline-block h-2.5 w-[2ch]" />
        labels
      </span>
    {/if}
    {#if followedTotal !== undefined}
      <span><span class="numeric">{followedTotal.toLocaleString()}</span> followed</span>
    {:else if $labels.isPending}
      <span>
        <Skeleton class="numeric inline-block h-2.5 w-[2ch]" />
        followed
      </span>
    {/if}
    <a href="/artists" class="transition hover:text-ink">
      <span class="underline underline-offset-3">
        {#if artistsTotal !== undefined}
          <span class="numeric">{artistsTotal.toLocaleString()}</span>
          {artistsTotal === 1 ? 'artist' : 'artists'}
        {:else}
          Artists
        {/if}
      </span>
    </a>
  {/snippet}
  {#snippet actions()}
    <Button variant="primary" key="f" verb="follow label" onclick={() => (showAddLabel = true)}>
      <Icon name="plus" size="sm" /> Follow label
    </Button>
  {/snippet}
</PageHeader>

<ControlRail>
  <form class="w-full sm:w-56" onsubmit={(event) => event.preventDefault()}>
    <label class="field flex w-full items-center gap-2">
      <Icon name="search" size="sm" class="shrink-0 text-ink-4" />
      <input
        bind:value={search}
        placeholder="Filter labels"
        aria-label="Search labels"
        class="min-w-0 flex-1 bg-transparent font-sans text-meta text-ink outline-none placeholder:text-ink-4"
      />
    </label>
  </form>
</ControlRail>

<div class="flex flex-col gap-4 pb-8">
  {#if $labels.isError}
    <div class="px-5 pt-5">
      <ErrorNote error={$labels.error} retry={() => $labels.refetch()} />
    </div>
  {:else}
    <Settle pending={$labels.isPending}>
      {#snippet placeholder()}
        <ul
          class="max-h-[calc(100dvh-9rem)] overflow-hidden"
          role="status"
          aria-label="Loading labels"
        >
          {#each Array(labelSkeletonCount) as _, placeholderIndex (placeholderIndex)}
            <li class="h-9 border-b border-rule px-5 py-2" aria-hidden="true">
              <Skeleton class="h-full" />
            </li>
          {/each}
        </ul>
      {/snippet}
      {#if items.length === 0}
        <div class="px-5 pt-5">
          <EmptyPanel heading="No labels followed" role="idle">
            <p class="text-body text-ink-3">Follow a label to track its releases.</p>
          </EmptyPanel>
        </div>
      {:else if filtered.length === 0}
        <div class="px-5 pt-5">
          <EmptyPanel heading="No labels match that search" role="idle" />
        </div>
      {:else}
        {#each [$follow.error, $unfollow.error, $refresh.error, $setMonitor.error] as error, errorIndex (errorIndex)}
          {#if error}
            <div class="px-5 pt-5"><ErrorNote {error} /></div>
          {/if}
        {/each}

        <div class="flex flex-col">
          <div
            class="grid {COLUMNS} label h-7 items-center gap-x-3 border-b border-rule-strong px-5"
            aria-hidden="true"
          >
            <span></span>
            <span>Label</span>
            <span>Owned</span>
            <span>Monitor</span>
            <span>Updated</span>
            <span></span>
          </div>

          <div class="relative" bind:this={listElement} use:glide={{ key: `${cursorId}:${search}` }}>
            <ul class="flex flex-col">
              {#each filtered as label (label.id)}
                {@const here = label.id === cursorId}
                <li
                  class="row scroll-mt-24 scroll-mb-12 grid {COLUMNS} items-center gap-x-3 px-5 {here
                    ? 'on-ink'
                    : ''}"
                  data-label={label.id}
                  data-cursor={here}
                  onfocusin={() => (cursorId = label.id)}
                  animate:slideInto
                >
                  {#if confirming === label.id}
                    <div class="col-span-6 flex flex-wrap items-center gap-3 py-1">
                      <p class="text-meta">
                        Unfollow {label.name}? Its releases stay; nothing you own is touched.
                      </p>
                      <div class="ml-auto flex items-center gap-2">
                        <Button
                          size="sm"
                          disabled={$unfollow.isPending}
                          onclick={() => $unfollow.mutate(label.id)}
                        >
                          {$unfollow.isPending ? 'Unfollowing…' : 'Unfollow'}
                        </Button>
                        <Button
                          size="sm"
                          variant="ghost"
                          disabled={$unfollow.isPending}
                          onclick={() => (confirming = '')}
                        >
                          Keep following
                        </Button>
                      </div>
                    </div>
                  {:else}
                    <!-- The label's most-held cover. Only covers already cached
                         are named, and a label with none gets the flat placeholder. -->
                    <span class="block size-7 overflow-hidden rounded-cover" aria-hidden="true">
                      <Cover
                        src={label.coverAlbumIds?.[0] ? coverSrc(label.coverAlbumIds[0]) : undefined}
                        seed={label.id}
                        class="size-full object-cover"
                      />
                    </span>
                    <!-- The link covers the row through its ::after; the controls
                         beside it sit above it. -->
                    <span class="flex min-w-0 items-baseline gap-2">
                      <a
                        href="/artists/labels/{label.id}"
                        class="truncate text-body after:absolute after:inset-0 {here ? 'font-medium' : ''}"
                      >
                        {label.name}
                      </a>
                      {#if label.type || label.country}
                        <span class="truncate text-meta {here ? '' : 'text-ink-3'}">
                          {[label.type, label.country].filter(Boolean).join(' · ')}
                        </span>
                      {/if}
                    </span>

                    <span class="flex items-center">
                      {#if label.releaseCount > 0}
                        <OwnedBar
                          owned={label.ownedReleaseCount}
                          total={label.releaseCount}
                          noun="releases"
                          compact
                        />
                      {:else}
                        <span class="truncate text-meta {here ? '' : 'text-ink-3'}">catalogue not fetched</span>
                      {/if}
                    </span>

                    <span class="relative z-[2] flex items-center">
                      {#if label.followed}
                        <PillSelect
                          options={monitorLevels}
                          value={label.monitorLevel}
                          onchange={(next) =>
                            $setMonitor.mutate({ id: label.id, level: next as MonitorLevel })}
                          label="Monitor {label.name}"
                        />
                      {/if}
                    </span>

                    <span class="mono truncate text-meta {here ? '' : 'text-ink-3'}">
                      {#if label.followed}
                        {label.lastRefreshedAt ? calendarDate(label.lastRefreshedAt) : 'not refreshed yet'}
                      {/if}
                    </span>

                    <span class="relative z-[2] flex items-center justify-end gap-1">
                      {#if label.followed}
                        <Button
                          size="xs"
                          variant="ghost"
                          icon
                          title="Refresh {label.name}"
                          aria-label="Refresh {label.name}"
                          disabled={$refresh.isPending || label.refreshStatus === 'running'}
                          onclick={() => $refresh.mutate(label.id)}
                        >
                          <Icon name="refresh" size="sm" class={label.refreshStatus === 'running' ? 'animate-spin' : ''} />
                        </Button>
                        <Button size="xs" variant="ghost" onclick={() => (confirming = label.id)}>
                          <Icon name="unfollow" size="sm" /> Unfollow
                        </Button>
                      {:else}
                        <!-- Held but not followed: an earlier follow was
                             withdrawn and its releases were kept. Following
                             again is that decision made a second time, not a
                             fresh fetch. -->
                        <Button
                          size="xs"
                          variant="outline"
                          disabled={$follow.isPending}
                          onclick={() => $follow.mutate(label)}
                        >
                          {$follow.isPending ? 'Following…' : 'Follow'}
                        </Button>
                      {/if}
                    </span>
                  {/if}
                </li>
              {/each}
            </ul>
          </div>
        </div>
      {/if}
    </Settle>
  {/if}
</div>

<FollowLabelModal bind:open={showAddLabel} />

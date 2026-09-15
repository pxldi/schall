<script lang="ts">
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
  import { toStore } from 'svelte/store';
  import { Plus, RefreshCw, Search, UserRoundMinus } from '@lucide/svelte';
  import { api, type LabelListItem, type MonitorLevel } from '$lib/api';
  import { calendarDate } from '$lib/utils';
  import Button from '$lib/components/Button.svelte';
  import ControlRail from '$lib/components/ControlRail.svelte';
  import EmptyPanel from '$lib/components/EmptyPanel.svelte';
  import ErrorNote from '$lib/components/ErrorNote.svelte';
  import FollowLabelModal from '$lib/components/FollowLabelModal.svelte';
  import OwnedBar from '$lib/components/OwnedBar.svelte';
  import PillSelect from '$lib/components/PillSelect.svelte';
  import Settle from '$lib/components/Settle.svelte';

  const COLUMNS = 'grid-cols-[minmax(0,1fr)_150px_170px_110px_100px]';

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
    // follows.
    refetchInterval: (query) =>
      (query.state.data?.items ?? []).some((item) =>
        ['pending', 'queued', 'running'].includes(item.refreshStatus)
      )
        ? 15_000
        : false
  });
  const labels = createQuery(toStore(() => labelsOptions));
  // Fill about 2016px of rows so a 4K viewport does not outgrow the wait.
  const labelSkeletonCount = 60;

  const dashboard = createQuery({ queryKey: ['dashboard'], queryFn: api.dashboard });

  const items = $derived($labels.data?.items ?? []);
  const filtered = $derived(
    search.trim()
      ? items.filter((label) => label.name.toLowerCase().includes(search.trim().toLowerCase()))
      : items
  );
  const labelsTotal = $derived($labels.data?.total);
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
</script>

<svelte:head><title>Labels · Schall</title></svelte:head>

<!-- Hidden: the mast highlights Artists here too, so this names the
     narrower page a screen reader would otherwise not be told. -->
<h1 class="sr-only">Labels</h1>

<ControlRail>
  <div class="flex items-center gap-5">
    <a href="/artists" class="flex h-8 shrink-0 items-center gap-1.5 text-body text-ink-2 hover:text-ink">
      Artists
      {#if artistsTotal !== undefined}
        <span class="numeric text-meta text-ink-3">{artistsTotal.toLocaleString()}</span>
      {:else}
        <span
          class="numeric inline-block h-2.5 w-[2ch] animate-pulse rounded-row bg-white/10"
          aria-hidden="true"
        ></span>
      {/if}
    </a>
    <span aria-current="page" class="flex h-8 shrink-0 items-center gap-1.5 text-body text-ink">
      Labels
      {#if labelsTotal !== undefined}
        <span class="numeric text-meta text-ink-3">{labelsTotal.toLocaleString()}</span>
      {:else}
        <span
          class="numeric inline-block h-2.5 w-[2ch] animate-pulse rounded-row bg-white/10"
          aria-hidden="true"
        ></span>
      {/if}
    </span>
  </div>

  <form class="ml-auto w-full sm:w-56" onsubmit={(event) => event.preventDefault()}>
    <label class="field flex w-full items-center gap-2">
      <Search size={13} strokeWidth={2} class="shrink-0 text-ink-4" />
      <input
        bind:value={search}
        placeholder="Filter labels"
        aria-label="Search labels"
        class="min-w-0 flex-1 bg-transparent font-sans text-meta text-ink outline-none placeholder:text-ink-4"
      />
    </label>
  </form>

  <Button onclick={() => (showAddLabel = true)}>
    <Plus size={13} strokeWidth={2.3} /> Follow label
  </Button>
</ControlRail>

<div class="flex flex-col gap-4 px-4 sm:px-6 py-5">
  {#if $labels.isError}
    <ErrorNote error={$labels.error} retry={() => $labels.refetch()} />
  {:else}
    <Settle pending={$labels.isPending}>
      {#snippet placeholder()}
        <ul
          class="max-h-[calc(100dvh-9rem)] flex flex-col divide-y divide-line-thin overflow-hidden"
          role="status"
          aria-label="Loading labels"
        >
          {#each Array(labelSkeletonCount) as _, placeholderIndex (placeholderIndex)}
            <li class="flex items-center gap-3 px-1 py-3.5" aria-hidden="true">
              <span class="h-4 w-40 animate-pulse rounded-row bg-surface-regular"></span>
              <span class="h-4 w-28 animate-pulse rounded-row bg-surface-regular"></span>
              <span class="ml-auto h-4 w-36 animate-pulse rounded-row bg-surface-regular"></span>
            </li>
          {/each}
        </ul>
      {/snippet}
      {#if items.length === 0}
        <EmptyPanel
          heading="No labels followed"
          role="idle"
          class="w-full max-w-none items-center text-center"
        >
          <p class="text-body text-ink-3">Follow a label to track everything it publishes.</p>
        </EmptyPanel>
      {:else if filtered.length === 0}
        <EmptyPanel
          heading="No labels match that search"
          role="idle"
          class="w-full max-w-none items-center text-center"
        />
      {:else}
        {#each [$unfollow.error, $refresh.error, $setMonitor.error] as error, errorIndex (errorIndex)}
          {#if error}
            <ErrorNote {error} />
          {/if}
        {/each}

        <div class="flex flex-col">
          <div class={`grid ${COLUMNS} gap-x-4 border-b border-line-thin px-1 text-micro font-mono uppercase tracking-[0.08em] text-ink-3`}>
            <span class="flex h-7 items-center">Label</span>
            <span class="flex h-7 items-center">Owned</span>
            <span class="flex h-7 items-center">Monitor</span>
            <span class="flex h-7 items-center">Updated</span>
            <span class="flex h-7 items-center"></span>
          </div>

          <ul class="flex flex-col divide-y divide-line-thin">
            {#each filtered as label (label.id)}
              <li class={`grid ${COLUMNS} min-h-[52px] items-center gap-x-4 px-1 py-2`}>
                {#if confirming === label.id}
                  <div class="col-span-5 flex flex-wrap items-center gap-3 py-1">
                    <p class="text-meta text-ink-2">
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
                  <span class="flex min-w-0 flex-col gap-0.5">
                    <a
                      href="/artists/labels/{label.id}"
                      class="truncate text-body font-medium text-ink underline-offset-2 hover:underline"
                    >
                      {label.name}
                    </a>
                    {#if label.type || label.country}
                      <span class="truncate text-meta text-ink-3">
                        {[label.type, label.country].filter(Boolean).join(' · ')}
                      </span>
                    {/if}
                  </span>

                  <span>
                    {#if label.releaseCount > 0}
                      <OwnedBar owned={label.ownedReleaseCount} total={label.releaseCount} width={56} noun="releases" />
                    {:else}
                      <span class="text-meta text-ink-3">catalogue not fetched</span>
                    {/if}
                  </span>

                  <span>
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

                  <span class="text-meta text-ink-3">
                    {#if label.followed}
                      {label.lastRefreshedAt ? calendarDate(label.lastRefreshedAt) : 'not refreshed yet'}
                    {/if}
                  </span>

                  <span class="flex items-center justify-end gap-1">
                    {#if label.followed}
                      <Button
                        size="sm"
                        variant="ghost"
                        icon
                        title="Refresh {label.name}"
                        aria-label="Refresh {label.name}"
                        disabled={$refresh.isPending || label.refreshStatus === 'running'}
                        onclick={() => $refresh.mutate(label.id)}
                      >
                        <RefreshCw
                          size={13}
                          strokeWidth={2.2}
                          class={label.refreshStatus === 'running' ? 'animate-spin' : ''}
                        />
                      </Button>
                      <Button
                        size="sm"
                        variant="ghost"
                        onclick={() => (confirming = label.id)}
                      >
                        <UserRoundMinus size={13} strokeWidth={2.2} /> Unfollow
                      </Button>
                    {:else}
                      <!-- Held but not followed: an earlier follow was
                           withdrawn and its releases were kept. Following
                           again is that decision made a second time, not a
                           fresh fetch. -->
                      <Button
                        size="sm"
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
      {/if}
    </Settle>
  {/if}
</div>

<FollowLabelModal bind:open={showAddLabel} />

<script lang="ts">
  import { createMutation, createQuery, useQueryClient } from '@tanstack/svelte-query';
  import { FileUp, LoaderCircle, Plus, RotateCw, Send, UserRoundMinus } from '@lucide/svelte';
  import { page } from '$app/state';
  import { api, type Playlist } from '$lib/api';
  import { isAuthError } from '$lib/errors';
  import { calendarDate, keepInUrl, urlChoice } from '$lib/utils';
  import Button from '$lib/components/Button.svelte';
  import ControlRail from '$lib/components/ControlRail.svelte';
  import EmptyPanel from '$lib/components/EmptyPanel.svelte';
  import ErrorNote from '$lib/components/ErrorNote.svelte';
  import ImportPlaylistFileModal from '$lib/components/ImportPlaylistFileModal.svelte';
  import OwnedBar from '$lib/components/OwnedBar.svelte';
  import Recommendations from '$lib/components/Recommendations.svelte';
  import Settle from '$lib/components/Settle.svelte';
  import Segmented from '$lib/components/Segmented.svelte';
  import WeeklyPlaylist from '$lib/components/WeeklyPlaylist.svelte';

  const COLUMNS = 'grid-cols-[minmax(0,1fr)_180px_300px]';

  const queryClient = useQueryClient();

  // Three lists of music from outside the library, on one page. A followed
  // playlist is a list somebody else made and Schall tracks; a recommendation
  // is a list ListenBrainz made from what this account listens to; the weekly
  // list is what Schall fetched from those recommendations and is about to take
  // back. None of the three is music the library chose to hold, and all three
  // are read here for the same reason.
  const views = ['playlists', 'recommended', 'weekly'] as const;
  // Read once, before anything is tracked, so that following the address bar
  // back into it later cannot become a dependency of the effect that writes it.
  const opened = page.url;
  let view = $state<(typeof views)[number]>(urlChoice(opened, 'view', views, 'playlists'));

  // The address bar keeps which of the two is open, so a reload and a copied
  // link both come back to the view the reader was on.
  $effect(() => {
    keepInUrl(opened.pathname, { view }, { view: 'playlists' });
  });

  // Mounted the first time it is asked for and kept from then on, so somebody
  // who never opens it never pays for its read.
  let recommendedOpened = $state(urlChoice(opened, 'view', views, 'playlists') === 'recommended');
  $effect(() => {
    if (view === 'recommended') recommendedOpened = true;
  });

  let weeklyOpened = $state(urlChoice(opened, 'view', views, 'playlists') === 'weekly');
  $effect(() => {
    if (view === 'weekly') weeklyOpened = true;
  });

  const playlists = createQuery({
    queryKey: ['playlists'],
    queryFn: api.playlists,
    // SSE announces imports finishing; the interval is the safety net, and it
    // only runs while a list is still waiting on its first import.
    refetchInterval: (query) =>
      isAuthError(query.state.error)
        ? false
        : (query.state.data?.items ?? []).some((item) => !item.importedAt)
          ? 15_000
          : false
  });

  const items = $derived($playlists.data?.items ?? []);
  const playlistSkeletonCount = $derived(Math.max($playlists.data?.items.length ?? 0, 4));

  let url = $state('');
  let fileModalOpen = $state(false);

  function chooseView(value: string) {
    if (value === 'recommended') recommendedOpened = true;
    if (value === 'weekly') weeklyOpened = true;
    view = value as (typeof views)[number];
  }

  const follow = createMutation({
    mutationFn: () => api.followPlaylist(url.trim()),
    onSuccess: async () => {
      url = '';
      await queryClient.invalidateQueries({ queryKey: ['playlists'] });
    }
  });

  const reimport = createMutation({
    mutationFn: (id: string) => api.importPlaylist(id),
    onSuccess: () => queryClient.invalidateQueries({ queryKey: ['playlists'] })
  });

  const sendToPlayer = createMutation({
    mutationFn: (id: string) => api.syncPlaylistToNavidrome(id),
    onSuccess: () => queryClient.invalidateQueries({ queryKey: ['playlists'] })
  });

  const remove = createMutation({
    mutationFn: (id: string) => api.deletePlaylist(id),
    onSuccess: () => queryClient.invalidateQueries({ queryKey: ['playlists'] })
  });

  function importedAgo(playlist: Playlist) {
    if (!playlist.importedAt) return 'importing…';
    return `imported ${calendarDate(playlist.importedAt)}`;
  }

  function playlistSubline(playlist: Playlist) {
    if (!playlist.ownerName && !playlist.importedAt) return 'Importing playlist…';
    return `${playlist.ownerName ? `by ${playlist.ownerName}` : 'Owner not recorded'} · ${importedAgo(playlist)}`;
  }

  // Schall makes these two for itself, one row each for the life of the
  // installation. Stopping to follow them is not an action the reader has —
  // there is nothing to unfollow, only music to keep or not.
  function schallMade(playlist: Playlist) {
    return playlist.source === 'weekly' || playlist.source === 'new_releases';
  }

  function confirmRemove(playlist: Playlist) {
    if (
      confirm(
        `Stop following “${playlist.name}”? Schall stops tracking its songs. Anything already wanted stays wanted.`
      )
    ) {
      $remove.mutate(playlist.id);
    }
  }
</script>

<svelte:head><title>Playlists · Schall</title></svelte:head>

<!-- Hidden: the mast's highlight already says this is Playlists, but that
     highlight is not a document heading. -->
<h1 class="sr-only">Playlists</h1>

<ControlRail>
  <Segmented
    options={[
      { value: 'playlists', name: 'Followed', count: $playlists.data ? items.length : undefined },
      { value: 'recommended', name: 'Recommended' },
      { value: 'weekly', name: 'Weekly' }
    ]}
    value={view}
    onchange={chooseView}
    label="What to look at"
    pending={$playlists.isPending}
  />

  <div class="ml-auto flex flex-wrap items-center gap-2">
    <Button type="button" variant="ghost" onclick={() => (fileModalOpen = true)}>
      <FileUp size={13} strokeWidth={2.2} />
      Import file
    </Button>
    <form
      class="flex items-center gap-2"
      onsubmit={(event) => {
        event.preventDefault();
        if (url.trim()) $follow.mutate();
      }}
    >
      <input
        bind:value={url}
        type="text"
        aria-label="Spotify playlist link"
        placeholder="Spotify playlist link"
        class="field min-w-0 w-full flex-1 sm:w-[18.75rem] sm:flex-none"
      />
      <Button type="submit" disabled={!url.trim() || $follow.isPending}>
        {#if $follow.isPending}
          <LoaderCircle size={13} class="animate-spin" />
        {:else}
          <Plus size={13} strokeWidth={2.2} />
        {/if}
        Follow
      </Button>
    </form>
  </div>
</ControlRail>

<ImportPlaylistFileModal bind:open={fileModalOpen} />

{#if recommendedOpened}
  <div class:hidden={view !== 'recommended'}>
    <Recommendations />
  </div>
{/if}

{#if weeklyOpened}
  <div class:hidden={view !== 'weekly'}>
    <WeeklyPlaylist />
  </div>
{/if}

<div class="flex flex-col gap-4 px-4 sm:px-6 py-5" class:hidden={view !== 'playlists'}>
  {#if $follow.isError}
    <ErrorNote error={$follow.error} />
  {/if}

  {#if $playlists.isError}
    <ErrorNote error={$playlists.error} retry={() => $playlists.refetch()} />
  {:else}
    <Settle pending={$playlists.isPending}>
      {#snippet placeholder()}
        <ul
          class="max-h-[calc(100dvh-11rem)] flex flex-col divide-y divide-line-thin overflow-hidden"
          aria-busy="true"
          aria-label="Loading playlists"
        >
          {#each Array.from({ length: playlistSkeletonCount }) as _, placeholderIndex (placeholderIndex)}
            <li class="flex items-center gap-3 px-1 py-4" aria-hidden="true">
              <span class="h-4 w-48 animate-pulse rounded-row bg-surface-regular"></span>
              <span class="ml-auto h-4 w-36 animate-pulse rounded-row bg-surface-regular"></span>
            </li>
          {/each}
        </ul>
      {/snippet}
      {#if items.length === 0}
        <EmptyPanel
          heading="No playlists followed"
          role="idle"
          class="w-full max-w-none items-center text-center"
        >
          <p class="text-body text-ink-3">Paste a Spotify link or import a file to follow one.</p>
        </EmptyPanel>
      {:else}
        <div class="flex flex-col">
          <div class={`grid ${COLUMNS} gap-x-4 border-b border-line-thin px-1 text-micro font-mono uppercase tracking-[0.08em] text-ink-3`}>
            <span class="flex h-7 items-center">Playlist</span>
            <span class="flex h-7 items-center">Owned</span>
            <span class="flex h-7 items-center"></span>
          </div>

          <ul class="flex flex-col divide-y divide-line-thin">
            {#each items as playlist (playlist.id)}
              <li class={`grid ${COLUMNS} min-h-[52px] items-center gap-x-4 px-1 py-2`}>
                <a
                  href={`/playlists/${playlist.id}`}
                  class="flex min-w-0 flex-col gap-0.5"
                >
                  <span class="truncate text-body font-medium text-ink">{playlist.name}</span>
                  <span class="truncate text-meta text-ink-3">{playlistSubline(playlist)}</span>
                </a>

                <span>
                  <OwnedBar owned={playlist.ownedCount} total={playlist.entryCount} width={64} noun="songs" />
                </span>

                <span class="flex items-center justify-end gap-1">
                  {#if playlist.source === 'spotify'}
                    <Button
                      size="sm"
                      variant="ghost"
                      disabled={$reimport.isPending}
                      onclick={() => $reimport.mutate(playlist.id)}
                    >
                      <RotateCw size={13} strokeWidth={2.2} /> Re-import
                    </Button>
                  {/if}
                  <Button
                    size="sm"
                    variant="ghost"
                    disabled={$sendToPlayer.isPending}
                    onclick={() => $sendToPlayer.mutate(playlist.id)}
                  >
                    <Send size={13} strokeWidth={2.2} /> Send to player
                  </Button>
                  {#if !schallMade(playlist)}
                    <Button
                      size="sm"
                      variant="ghost"
                      disabled={$remove.isPending}
                      onclick={() => confirmRemove(playlist)}
                    >
                      <UserRoundMinus size={13} strokeWidth={2.2} /> Stop following
                    </Button>
                  {/if}
                </span>
              </li>
            {/each}
          </ul>
        </div>
        {#if $reimport.isError}
          <ErrorNote error={$reimport.error} />
        {/if}
        {#if $sendToPlayer.isError}
          <ErrorNote error={$sendToPlayer.error} />
        {/if}
        {#if $remove.isError}
          <ErrorNote error={$remove.error} />
        {/if}
      {/if}
    </Settle>
  {/if}
</div>

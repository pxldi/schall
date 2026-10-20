<script lang="ts">
  import ConfirmDialog from '$lib/components/ConfirmDialog.svelte';
  import SkeletonRows from '$lib/components/SkeletonRows.svelte';
  import { createMutation, createQuery, useQueryClient } from '@tanstack/svelte-query';
  import { page } from '$app/state';
  import { api, type Playlist } from '$lib/api';
  import { isAuthError } from '$lib/errors';
  import { coverSrc } from '$lib/cover-size';
  import { keepInUrl, urlChoice } from '$lib/utils';
  import { playlistOrigin } from '$lib/playlists';
  import Button from '$lib/components/Button.svelte';
  import Icon from '$lib/components/Icon.svelte';
  import ControlRail from '$lib/components/ControlRail.svelte';
  import EmptyPanel from '$lib/components/EmptyPanel.svelte';
  import Cover from '$lib/components/Cover.svelte';
  import ErrorNote from '$lib/components/ErrorNote.svelte';
  import PageHeader from '$lib/components/PageHeader.svelte';
  import ImportPlaylistFileModal from '$lib/components/ImportPlaylistFileModal.svelte';
  import OwnedBar from '$lib/components/OwnedBar.svelte';
  import Recommendations from '$lib/components/Recommendations.svelte';
  import Settle from '$lib/components/Settle.svelte';
  import Segmented from '$lib/components/Segmented.svelte';
  import WeeklyPlaylist from '$lib/components/WeeklyPlaylist.svelte';

  const COLUMNS = 'grid-cols-[2.5rem_minmax(0,1fr)_max-content_max-content]';

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

  // Only Spotify and file lists are ever imported. The others never get an
  // import date, so counting them as waiting would poll for good.
  function awaitingImport(item: Playlist) {
    return (item.source === 'spotify' || item.source === 'file') && !item.importedAt;
  }

  const playlists = createQuery({
    queryKey: ['playlists'],
    queryFn: api.playlists,
    // SSE announces imports finishing; the interval is the safety net, and it
    // only runs while a list is still waiting on its first import.
    refetchInterval: (query) =>
      isAuthError(query.state.error)
        ? false
        : (query.state.data?.items ?? []).some(awaitingImport)
          ? 15_000
          : false
  });

  const items = $derived($playlists.data?.items ?? []);
  // Each playlist's cover is its first entry's release. One without a
  // release yet draws the flat placeholder seeded with its own id.
  function coverOf(playlist: Playlist) {
    return {
      src: playlist.coverReleaseId ? coverSrc(playlist.coverReleaseId) : null,
      seed: playlist.id
    };
  }

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

  // Schall makes these two for itself, one row each for the life of the
  // installation. Stopping to follow them is not an action the reader has —
  // there is nothing to unfollow, only music to keep or not.
  function schallMade(playlist: Playlist) {
    return playlist.source === 'weekly' || playlist.source === 'new_releases';
  }

  // The list Unfollow was pressed on, while the question is open.
  let removing = $state<Playlist | null>(null);
  let removeOpen = $state(false);
  function confirmRemove(playlist: Playlist) {
    removing = playlist;
    removeOpen = true;
  }
</script>

<svelte:head><title>Playlists · Schall</title></svelte:head>

<ConfirmDialog
  bind:open={removeOpen}
  title={`Unfollow “${removing?.name ?? ''}”?`}
  description="Songs already on your wishlist stay there."
  action="Unfollow"
  keep="Keep following"
  onconfirm={() => removing && $remove.mutate(removing.id)}
/>

<!-- The page's name, with the ways to follow a new playlist beside it. -->
<PageHeader title="Playlists">
  {#snippet actions()}
    <Button type="button" variant="outline" onclick={() => (fileModalOpen = true)}>
      <Icon name="upload" size="sm" />
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
          <Icon name="busy" size="sm" class="animate-spin" />
        {:else}
          <Icon name="plus" size="sm" />
        {/if}
        Follow
      </Button>
    </form>
  {/snippet}
</PageHeader>

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

<div class="flex flex-col gap-4 px-5 py-5" class:hidden={view !== 'playlists'}>
  {#if $follow.isError}
    <ErrorNote error={$follow.error} />
  {/if}

  {#if $playlists.isError}
    <ErrorNote error={$playlists.error} retry={() => $playlists.refetch()} />
  {:else}
    <Settle pending={$playlists.isPending}>
      {#snippet placeholder()}
        <!-- The header strip and a covered row of the same height as the
             list's own, so the rows do not move down or grow when it lands. -->
        <div class="max-h-[calc(100dvh-11rem)] overflow-hidden">
          <div class="h-7 border-b border-rule"></div>
          <SkeletonRows count={playlistSkeletonCount} cover label="Loading playlists" />
        </div>
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
        <!-- One grid for the header and every row, so the owned and action
             columns size to the widest row and line up down the list. A fixed
             action column let a Spotify row's three buttons spill left over
             its owned count. -->
        <div class={`grid ${COLUMNS} gap-x-4`}>
          <div class="col-span-full grid grid-cols-subgrid border-b border-rule px-1 text-micro font-mono uppercase tracking-[0.08em] text-ink-3">
            <span class="flex h-7 items-center"></span>
            <span class="flex h-7 items-center">Playlist</span>
            <span class="flex h-7 items-center">Owned</span>
            <span class="flex h-7 items-center"></span>
          </div>

          <ul class="col-span-full grid grid-cols-subgrid divide-y divide-rule">
            {#each items as playlist (playlist.id)}
              {@const cover = coverOf(playlist)}
              <li class="col-span-full grid min-h-[52px] grid-cols-subgrid items-center px-1 py-2">
                <Cover
                  src={cover.src ?? undefined}
                  seed={cover.seed}
                  class="size-10 object-cover"
                />
                <a
                  href={`/playlists/${playlist.id}`}
                  class="flex min-w-0 flex-col gap-0.5"
                >
                  <span class="truncate text-body font-medium text-ink">{playlist.name}</span>
                  <span class="truncate text-meta text-ink-3">{playlistOrigin(playlist).join(' · ')}</span>
                </a>

                <span>
                  <OwnedBar owned={playlist.ownedCount} total={playlist.entryCount} width={64} noun="songs" />
                </span>

                <!-- Below 1024px the three buttons are icons with their name in
                     the tooltip; with words they ran past the right edge of a
                     half-screen window. -->
                <span class="flex items-center justify-end gap-1">
                  {#if playlist.source === 'spotify'}
                    <Button
                      size="sm"
                      variant="ghost"
                      disabled={$reimport.isPending}
                      onclick={() => $reimport.mutate(playlist.id)}
                      title="Re-import"
                    >
                      <Icon name="refresh" size="sm" /><span class="max-lg:sr-only">Re-import</span>
                    </Button>
                  {/if}
                  <Button
                    size="sm"
                    variant="ghost"
                    disabled={$sendToPlayer.isPending}
                    onclick={() => $sendToPlayer.mutate(playlist.id)}
                    title="Send to player"
                  >
                    <Icon name="send" size="sm" /><span class="max-lg:sr-only">Send to player</span>
                  </Button>
                  {#if !schallMade(playlist)}
                    <Button
                      size="sm"
                      variant="ghost"
                      disabled={$remove.isPending}
                      onclick={() => confirmRemove(playlist)}
                      title="Unfollow"
                    >
                      <Icon name="unfollow" size="sm" /><span class="max-lg:sr-only">Unfollow</span>
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

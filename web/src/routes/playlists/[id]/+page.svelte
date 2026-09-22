<script lang="ts">
  import { createMutation, createQuery, useQueryClient } from '@tanstack/svelte-query';
  import { Check, ChevronRight, LoaderCircle, MonitorSpeaker, RotateCw, SearchCheck } from '@lucide/svelte';
  import { page } from '$app/state';
  import { api, type PlayerPairing, type PlaylistEntry, type UnpairedFile } from '$lib/api';
  import AddToMusicBrainz from '$lib/components/AddToMusicBrainz.svelte';
  import BackLink from '$lib/components/BackLink.svelte';
  import Button from '$lib/components/Button.svelte';
  import ErrorNote from '$lib/components/ErrorNote.svelte';
  import OwnedBar from '$lib/components/OwnedBar.svelte';
  import Settle from '$lib/components/Settle.svelte';
  import StateTag from '$lib/components/StateTag.svelte';
  import UseAddress from '$lib/components/UseAddress.svelte';
  import { entryState, ownedOf, type EntryRole } from '$lib/playlists';
  import { calendarDate } from '$lib/utils';

  const queryClient = useQueryClient();
  const playlistId = page.params.id ?? '';

  const detail = createQuery({
    queryKey: ['playlists', playlistId],
    queryFn: () => api.playlist(playlistId),
    // The entries settle as the sweep resolves them, minutes after the import.
    // SSE covers that; the interval is the safety net while wants are open.
    refetchInterval: (query) =>
      (query.state.data?.entries ?? []).some(
        (entry) => entry.targetStatus === 'unresolved' || entry.targetStatus === 'pending'
      )
        ? 15_000
        : false
  });

  const playlist = $derived($detail.data?.playlist);
  const entries = $derived($detail.data?.entries ?? []);
  const playlistEntrySkeletonCount = $derived(Math.max($detail.data?.entries.length ?? 0, 8));

  const reimport = createMutation({
    mutationFn: () => api.importPlaylist(playlistId),
    onSuccess: () => queryClient.invalidateQueries({ queryKey: ['playlists'] })
  });

  // An import already queues one of these. The button is for the other reason a
  // list changes: a copy arrived, and the player has not been told.
  const sendToPlayer = createMutation({
    mutationFn: () => api.syncPlaylistToNavidrome(playlistId),
    onSuccess: () => queryClient.invalidateQueries({ queryKey: ['playlists'] })
  });

  // The door for an entry that never made a want on its own — every list
  // adopted as a file, today. A second press on a row that already has one
  // changes nothing.
  const wantEntry = createMutation({
    mutationFn: (entryId: string) => api.wantPlaylistEntry(playlistId, entryId),
    onSuccess: () => queryClient.invalidateQueries({ queryKey: ['playlists'] })
  });
  const wantingEntryId = $derived($wantEntry.isPending ? ($wantEntry.variables ?? null) : null);

  // This asks the player about every file Schall holds, one question each, so it
  // is asked for and never on arrival: disabled, and fetched by the button.
  const pairing = createQuery({
    queryKey: ['playlist-pairing', playlistId],
    queryFn: () => api.playerPairing(playlistId),
    enabled: false,
    refetchInterval: false,
    retry: false
  });

  let pairingAsked = $state(false);
  // The header states the pairing result in one line; the file-by-file detail
  // is behind it, open the moment a check is asked for and collapsible after.
  let pairingPanelOpen = $state(false);

  function checkPlayer() {
    pairingAsked = true;
    pairingPanelOpen = true;
    void $pairing.refetch();
  }

  const pairingData = $derived($pairing.data);
  const titles = $derived(new Map(entries.map((entry) => [entry.id, entry.title])));

  // Position order, because these rows are read against the list above them.
  const unpaired = $derived(
    [...(pairingData?.unpaired ?? [])].sort((a, b) => a.position - b.position)
  );

  // One label per reason the player had nothing for a file, in the reader's
  // words rather than the wire's.
  function reason(value: string): { label: string; role: EntryRole } {
    switch (value) {
      case 'no_candidates':
        return { label: 'search found nothing', role: 'fail' };
      case 'no_path_match':
        return { label: 'found other files', role: 'busy' };
      case 'path_repeated':
        return { label: 'shared path', role: 'fail' };
      case 'no_search_term':
        return { label: 'nothing to search for', role: 'idle' };
    }
    return { label: value, role: 'idle' };
  }

  // The entry names the row; a file the list no longer carries still has to be
  // nameable, so its own name stands in.
  function nameFor(file: UnpairedFile) {
    const title = titles.get(file.entryId);
    if (title) return title;
    const segments = file.path.split('/');
    return segments[segments.length - 1] || file.path;
  }

  // What was last sent is a different fact from what the player answers to now,
  // so it only joins the line when there was a push.
  function pushNote(data: PlayerPairing) {
    if (data.pushed <= 0) return '';
    const at = data.lastPushedAt ? ` on ${calendarDate(data.lastPushedAt)}` : '';
    return ` · ${data.pushed} sent in the last push${at}`;
  }

  // The evidence under a row: what the player was asked, and how much it said
  // back before none of it was this file.
  function askedLine(file: UnpairedFile) {
    const asked = file.searched.length ? `asked ${file.searched.join(' · ')} · ` : '';
    return `${asked}${file.candidates} candidates`;
  }

  function length(entry: PlaylistEntry) {
    if (!entry.durationMs) return '';
    const total = Math.round(entry.durationMs / 1000);
    return `${Math.floor(total / 60)}:${String(total % 60).padStart(2, '0')}`;
  }

  // The list's total running time, from its own entries: nothing on the wire
  // states it, and every entry already carries its own duration.
  function totalDuration(entryList: PlaylistEntry[]): string {
    const totalMs = entryList.reduce((sum, entry) => sum + (entry.durationMs ?? 0), 0);
    if (totalMs <= 0) return '';
    const totalMinutes = Math.round(totalMs / 60000);
    const hours = Math.floor(totalMinutes / 60);
    const minutes = totalMinutes % 60;
    return hours > 0 ? `${hours} h ${minutes} min` : `${minutes} min`;
  }

  function factLine(value: NonNullable<typeof playlist>, entryList: PlaylistEntry[]) {
    if (!value.ownerName && !value.importedAt) return 'Importing playlist…';
    const parts = [value.ownerName ? `by ${value.ownerName}` : 'Owner not recorded'];
    parts.push(`${entryList.length} song${entryList.length === 1 ? '' : 's'}`);
    const duration = totalDuration(entryList);
    if (duration) parts.push(duration);
    if (value.importedAt) parts.push(`imported ${calendarDate(value.importedAt)}`);
    return parts.join(' · ');
  }
</script>

<svelte:head><title>{playlist?.name ?? 'Playlist'} · Schall</title></svelte:head>

<div class="px-4 sm:px-6 pt-6 pb-3">
  <BackLink fallback="/playlists" label="Back to playlists" />
</div>

<section class="flex items-start justify-between gap-5 px-4 sm:px-6 pb-4">
  <div class="flex min-w-0 flex-1 flex-col gap-2">
    {#if playlist}
      <h1 class="min-w-0 truncate text-quiet-display font-semibold text-ink">{playlist.name}</h1>
      <p class="text-body text-ink-2">{factLine(playlist, entries)}</p>
      <div class="mt-1 flex flex-wrap items-center gap-2">
        <OwnedBar owned={ownedOf(entries)} total={entries.length} width={120} noun="songs" />
        <span class="text-meta text-ink-3">owned</span>
        {#if pairingAsked}
          <span class="text-meta text-ink-4">·</span>
          {#if $pairing.isFetching}
            <span class="text-meta text-ink-3">checking player…</span>
          {:else if $pairing.isError}
            <span class="text-meta text-fail">player check failed</span>
          {:else if pairingData && !pairingData.configured}
            <span class="text-meta text-ink-3">no player configured</span>
          {:else if pairingData}
            <button
              type="button"
              class="text-meta text-accent underline-offset-2 hover:underline"
              onclick={() => (pairingPanelOpen = !pairingPanelOpen)}
            >
              player: {pairingData.pairedCount} paired, {unpaired.length} not
            </button>
          {/if}
        {/if}
      </div>
    {:else}
      <h1 aria-label="Loading playlist" class="inline-block min-w-48 animate-pulse rounded-row bg-surface-regular text-quiet-display font-semibold text-transparent">
        Loading playlist
      </h1>
      <span class="numeric min-w-48 text-body text-ink-3" aria-busy="true">Loading playlist details…</span>
    {/if}
  </div>
  <div class="flex flex-none items-center gap-1.5">
    <Button
      variant="outline"
      size="sm"
      disabled={!playlist || $sendToPlayer.isPending}
      onclick={() => $sendToPlayer.mutate()}
      title="Push what is owned to Navidrome"
    >
      {#if $sendToPlayer.isPending}
        <LoaderCircle size={12} class="animate-spin" />
      {:else}
        <MonitorSpeaker size={12} />
      {/if}
      Send to player
    </Button>
    <Button
      variant="outline"
      size="sm"
      disabled={!playlist || $pairing.isFetching}
      onclick={checkPlayer}
      title="Ask the player which of these files it holds"
    >
      {#if $pairing.isFetching}
        <LoaderCircle size={12} class="animate-spin" />
      {:else}
        <SearchCheck size={12} />
      {/if}
      Check player
    </Button>
    <Button
      variant="outline"
      size="sm"
      disabled={!playlist || playlist.source !== 'spotify' || $reimport.isPending}
      onclick={() => $reimport.mutate()}
      title={playlist?.source === 'spotify' ? 'Re-import from Spotify' : 'Only Spotify playlists can be re-imported'}
    >
      {#if $reimport.isPending}
        <LoaderCircle size={12} class="animate-spin" />
      {:else}
        <RotateCw size={12} />
      {/if}
      Re-import
    </Button>
  </div>
</section>

<div class="flex flex-col gap-3.5 px-4 sm:px-6 pb-5">
  {#if $detail.isError}
    <!-- The playlist is read, not written, so the note asks again by itself.
         It draws its own tinted band, which is the one this section used to
         draw by hand. -->
    <ErrorNote error={$detail.error} retry={() => $detail.refetch()} />
  {:else}
    <Settle pending={$detail.isPending}>
      {#snippet placeholder()}
        <section
      class="max-h-[calc(100dvh-10rem)] overflow-hidden rounded-panel border border-line-thin"
      aria-busy="true"
      aria-label="Loading playlist entries"
    >
      <div aria-hidden="true" class="h-10 animate-pulse border-b border-line-thin"></div>
      {#each Array.from({ length: playlistEntrySkeletonCount }) as _}
        <div aria-hidden="true" class="h-14 animate-pulse border-b border-line-thin last:border-b-0"></div>
      {/each}
        </section>
      {/snippet}
    {#if pairingAsked && pairingPanelOpen}
      <section class="rounded-panel border border-line-thin">
        <div class="flex flex-wrap items-center gap-x-2.5 gap-y-1 px-4 py-3">
          <span class="text-body font-medium text-ink">Player pairing</span>
          {#if $pairing.isFetching}
            <LoaderCircle size={12} class="animate-spin text-ink-3" />
            <span class="text-meta text-ink-3">checking player…</span>
          {/if}
          {#if $pairing.isError}
            <!-- Bare: this is the header row of the pairing card, which is
                 already a surface. -->
            <ErrorNote error={$pairing.error} action="Press Check player to ask again." bare />
          {:else if pairingData && !pairingData.configured}
            <span class="text-meta text-ink-3">no player configured</span>
          {:else if pairingData}
            <span class="numeric text-meta text-ink-3">
              {pairingData.entryCount} songs · {pairingData.acquiredCount} owned ·
              {pairingData.pairedCount} paired · {unpaired.length} unpaired{pushNote(pairingData)}
            </span>
          {/if}
        </div>
        {#if $pairing.isFetching}
          <div
            class="flex max-h-[calc(100dvh-14rem)] flex-col overflow-hidden border-t border-line-thin"
            aria-busy="true"
            aria-label="Loading player pairing"
          >
            {#each Array.from({ length: 40 }) as _}
              <div aria-hidden="true" class="h-14 animate-pulse border-b border-line-thin last:border-b-0"></div>
            {/each}
          </div>
        {:else if pairingData?.configured}
          {#if pairingData.checked < pairingData.acquiredCount || unpaired.length === 0}
            <div class="flex flex-col gap-1 px-4 pb-3">
              {#if pairingData.checked < pairingData.acquiredCount}
                <span class="numeric text-meta text-ink-3">
                  {pairingData.checked} of {pairingData.acquiredCount} asked before the check ran out
                  of time
                </span>
              {/if}
              {#if unpaired.length === 0}
                <span class="text-meta text-ink-3">every file paired</span>
              {/if}
            </div>
          {/if}
          {#if unpaired.length}
            <div class="flex flex-col border-t border-line-thin">
              {#each unpaired as file (`${file.entryId}-${file.position}`)}
                {@const state = reason(file.reason)}
                <div
                  class="flex flex-col gap-1 border-b border-line-thin px-4 py-2 last:border-b-0"
                >
                  <div class="flex items-center gap-3">
                    <span class="numeric text-meta text-ink-4">{file.position}</span>
                    <span class="truncate text-body font-medium text-ink">{nameFor(file)}</span>
                    <span class="ml-auto shrink-0">
                      <StateTag tone={state.role === 'fail' ? 'broken' : 'neutral'}>{state.label}</StateTag>
                    </span>
                  </div>
                  <span class="numeric text-meta break-all text-ink-3">{askedLine(file)}</span>
                  <span class="numeric text-meta break-all text-ink-4">{file.pathEscaped}</span>
                  {#if file.offered.length}
                    <span class="text-meta text-ink-3">offered</span>
                    {#each file.offered as offer, index (index)}
                      <span class="numeric text-meta break-all text-ink-4">{offer}</span>
                    {/each}
                  {/if}
                </div>
              {/each}
            </div>
          {/if}
        {/if}
      </section>
    {/if}
    <section class="rounded-panel border border-line-thin">
      <!-- The table is what scrolls sideways on a narrow screen, not the panel:
           the pager under it has to stay where it was put. -->
      <!-- Artist and album stand down on a narrow screen; the state does not.
           At 390px the table was 640px wide inside a sideways scroll with no
           scrollbar and no edge to show there was more, so the one column the
           page is opened for — whether each song is held, wanted or still being
           resolved — sat off the right edge unseen. The artist moves under the
           title in the same cell, which is where a phone reads it anyway. -->
      <div class="overflow-x-auto">
        <table class="w-full border-collapse text-left md:min-w-[640px]">
          <thead>
            <tr class="border-b border-line-thin text-micro font-mono uppercase tracking-[0.08em] text-ink-3">
              <th class="numeric px-4 py-2 font-medium">#</th>
              <th class="px-3 py-2 font-medium">Title</th>
              <th class="hidden px-3 py-2 font-medium md:table-cell">Artist</th>
              <th class="hidden px-3 py-2 font-medium md:table-cell">Album</th>
              <th class="hidden px-3 py-2 text-right font-medium md:table-cell">Length</th>
              <th class="px-4 py-2 font-medium">State</th>
            </tr>
          </thead>
          <tbody>
            {#each entries as entry (entry.id)}
              {@const state = entryState(entry)}
              <!-- A note and the entry it belongs to are one row, so the border
                   waits for whichever of the two is last. -->
              <tr class={state.note ? '' : 'border-b border-line-thin last:border-b-0'}>
                <td class="numeric px-4 py-1.5 text-meta text-ink-4">{entry.position}</td>
                <td class="max-w-64 px-3 py-1.5 text-body font-medium text-ink">
                  <span class="block truncate">{entry.title}</span>
                  <span class="block truncate text-meta font-normal text-ink-3 md:hidden">
                    {[entry.artist, length(entry)].filter(Boolean).join(' · ')}
                  </span>
                </td>
                <td class="hidden max-w-48 truncate px-3 py-1.5 text-meta text-ink-2 md:table-cell">
                  {entry.artist}
                </td>
                <td class="hidden max-w-48 truncate px-3 py-1.5 text-meta text-ink-3 md:table-cell">
                  {entry.album}
                </td>
                <td class="numeric hidden px-3 py-1.5 text-right text-meta text-ink-3 md:table-cell">
                  {length(entry)}
                </td>
                <td class="px-4 py-1.5">
                  {#if entry.externalUrl}
                    <a
                      href={entry.externalUrl}
                      class="text-meta text-ink-2 underline-offset-2 hover:text-ink hover:underline"
                    >
                      Keyed{#if entry.minimumBitrate} · {entry.minimumBitrate} kbit/s{/if}
                    </a>
                  {:else if state.role === 'ok'}
                    <span
                      class="inline-flex text-ok"
                      title={state.label}
                      aria-label={state.label}
                    >
                      <Check size={14} strokeWidth={2.6} />
                    </span>
                  {:else if state.role === 'fail'}
                    <a
                      href="/review"
                      class="inline-flex items-center gap-1 text-accent hover:text-accent-soft"
                    >
                      <StateTag tone="attention">{state.label}</StateTag>
                      <ChevronRight size={11} strokeWidth={2.4} />
                    </a>
                  {:else if state.label === 'no want'}
                    <Button
                      size="xs"
                      variant="ghost"
                      disabled={wantingEntryId === entry.id}
                      onclick={() => $wantEntry.mutate(entry.id)}
                    >
                      {wantingEntryId === entry.id ? 'Wanting…' : 'Want'}
                    </Button>
                  {:else}
                    <StateTag>{state.label}</StateTag>
                  {/if}
                </td>
              </tr>
              {#if state.note}
                <tr class="border-b border-line-thin last:border-b-0">
                  <td></td>
                  <!-- The note says what happened; the form beside it is the only
                       row on this page whose dead end somebody can do something
                       about, and it is offered nowhere else. -->
                  <td class="px-3 pb-2" colspan="5">
                    <span class="inline-flex flex-wrap items-center gap-x-2 gap-y-1">
                      <span class="text-meta text-ink-3">{state.note}</span>
                      {#if entry.musicbrainzSeed}
                        <AddToMusicBrainz seed={entry.musicbrainzSeed} />
                      {/if}
                    </span>
                  </td>
                </tr>
              {/if}
              {#if entry.targetId && entry.targetStatus === 'unresolved' && !entry.source}
                <tr class="border-b border-line-thin last:border-b-0">
                  <td></td>
                  <td class="px-3 pb-2" colspan="5">
                    <UseAddress
                      targetId={entry.targetId}
                      entryTitle={entry.title}
                      entryArtist={entry.artist}
                      entryDurationMs={entry.durationMs}
                    />
                  </td>
                </tr>
              {/if}
            {/each}
          </tbody>
        </table>
      </div>
    </section>
    </Settle>
  {/if}
  {#if $reimport.isError}
    <ErrorNote error={$reimport.error} />
  {/if}
  {#if $sendToPlayer.isError}
    <ErrorNote error={$sendToPlayer.error} />
  {/if}
  {#if $wantEntry.isError}
    <ErrorNote error={$wantEntry.error} />
  {/if}
</div>

<script lang="ts" module>
  import { SvelteMap } from 'svelte/reactivity';

  // Outlives the page, so going back to a list finds as many rows as it left
  // and the browser can put the reader back where they had scrolled to.
  const grown = new SvelteMap<string, number>();
</script>

<script lang="ts">
  import Skeleton from '$lib/components/Skeleton.svelte';
  import { createMutation, createQuery, useQueryClient } from '@tanstack/svelte-query';
  import { page } from '$app/state';
  import { api, type PlayerPairing, type PlaylistEntry, type UnpairedFile } from '$lib/api';
  import AddToMusicBrainz from '$lib/components/AddToMusicBrainz.svelte';
  import Button from '$lib/components/Button.svelte';
  import Icon from '$lib/components/Icon.svelte';
  import ErrorNote from '$lib/components/ErrorNote.svelte';
  import EmptyPanel from '$lib/components/EmptyPanel.svelte';
  import PageHeader from '$lib/components/PageHeader.svelte';
  import WantToggle from '$lib/components/WantToggle.svelte';
  import OwnedBar from '$lib/components/OwnedBar.svelte';
  import Settle from '$lib/components/Settle.svelte';
  import StateStrip from '$lib/components/StateStrip.svelte';
  import StateTag from '$lib/components/StateTag.svelte';
  import StateIcon from '$lib/components/StateIcon.svelte';
  import UseAddress from '$lib/components/UseAddress.svelte';
  import { entryState, ownedOf, playlistOrigin, reviewHref, type EntryRole } from '$lib/playlists';
  import { calendarDate } from '$lib/utils';
  import { lineSeparators } from '$lib/separators';
  import { nearEnd } from '$lib/grow';
  import { tick } from 'svelte';

  const queryClient = useQueryClient();
  const playlistId = page.params.id ?? '';

  const detail = createQuery({
    queryKey: ['playlists', playlistId],
    queryFn: () => api.playlist(playlistId),
    // Read when the page opens, and again when an import or a sync ends. An
    // entry's want settling is not re-read: while downloads ran that fetched
    // every loaded page of the list every 8 to 11 s.
    refetchInterval: false
  });

  const playlist = $derived($detail.data?.playlist);
  const entries = $derived($detail.data?.entries ?? []);
  // Read once per row and shared by the strip and the table.
  const states = $derived(new Map(entries.map((entry) => [entry.id, entryState(entry)])));

  // A 323-row list held the page for 670 ms before its title showed. The
  // first rows are drawn at once and the rest as the reader scrolls
  // (lib/grow.ts). Rows are taller than artist cards are wide, so a step is
  // smaller.
  const ROW_STEP = 50;
  const drawn = $derived(entries.slice(0, grown.get(playlistId) ?? ROW_STEP));
  function drawMore() {
    if (drawn.length < entries.length) grown.set(playlistId, drawn.length + ROW_STEP);
  }

  // A dot in the strip can name a row not drawn yet: draw up to it first.
  async function reveal(id: string) {
    const index = entries.findIndex((entry) => entry.id === id);
    if (index >= drawn.length) {
      grown.set(playlistId, index + 1 + ROW_STEP);
      await tick();
    }
    document.getElementById(`entry-${id}`)?.scrollIntoView({ block: 'center', behavior: 'smooth' });
  }
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

  // The line under the title, one part per fact; the hero puts the dots
  // between them.
  function factParts(value: NonNullable<typeof playlist>, entryList: PlaylistEntry[]) {
    const origin = playlistOrigin(value);
    if (origin[0] === 'Importing playlist…') return origin;
    const parts = [origin[0], `${entryList.length} song${entryList.length === 1 ? '' : 's'}`];
    const duration = totalDuration(entryList);
    if (duration) parts.push(duration);
    return [...parts, ...origin.slice(1)];
  }

  // Rows are numbered by where they sit, not by the stored position: the
  // weekly list drops entries and keeps the others' positions, which numbered
  // it 2, 3, 4, 8. The unpaired list uses the same numbers so the two agree.
  const rowNumber = $derived(new Map(entries.map((entry, index) => [entry.id, index + 1])));
</script>

<svelte:head><title>{playlist?.name ?? 'Playlist'} · Schall</title></svelte:head>

<!-- One set of actions for the header and for the frame drawn while the list
     loads, where they wait disabled. Send to player is the one primary. -->
{#snippet playlistActions()}
  <Button
    variant="outline"
    disabled={!playlist || $pairing.isFetching}
    onclick={checkPlayer}
    title="Check which files the player has"
  >
    {#if $pairing.isFetching}
      <Icon name="busy" size="sm" class="animate-spin" />
    {:else}
      <Icon name="player-check" size="sm" />
    {/if}
    Check player
  </Button>
  <!-- Only a Spotify list has somewhere to be imported from again, so the
       others leave the button out rather than show it disabled for good. -->
  {#if !playlist || playlist.source === 'spotify'}
    <Button
      variant="outline"
      disabled={!playlist || $reimport.isPending}
      onclick={() => $reimport.mutate()}
      title="Re-import from Spotify"
    >
      {#if $reimport.isPending}
        <Icon name="busy" size="sm" class="animate-spin" />
      {:else}
        <Icon name="refresh" size="sm" />
      {/if}
      Re-import
    </Button>
  {/if}
  <Button
    disabled={!playlist || $sendToPlayer.isPending}
    onclick={() => $sendToPlayer.mutate()}
    title="Send owned songs to Navidrome"
  >
    {#if $sendToPlayer.isPending}
      <Icon name="busy" size="sm" class="animate-spin" />
    {:else}
      <Icon name="player" size="sm" />
    {/if}
    Send to player
  </Button>
{/snippet}

{#if playlist}
  <PageHeader
    title={playlist.name}
    path={[{ label: 'Playlists', href: '/playlists' }]}
    actions={playlistActions}
  >
    {#snippet sub()}
      <!-- One child, so the header puts no gap of its own between the parts.
           Each part carries its dot in front, in a box as wide as the gap.
           A part that starts a line hides its dot (lib/separators.ts), and
           the row is pulled left by the dot's width and clipped, so the
           hidden dot takes no room at the text edge. -->
      <span class="block overflow-x-clip">
      <span class="-ml-4 flex flex-wrap items-center gap-y-1" use:lineSeparators>
        {#each factParts(playlist, entries) as part (part)}
          <span class="numeric whitespace-nowrap"><span data-sep class="inline-block w-4 text-center text-ink-4" aria-hidden="true">·</span>{part}</span>
        {/each}
        <span class="flex flex-wrap items-center gap-2">
        <span data-sep class="-mr-2 inline-block w-4 text-center text-ink-4" aria-hidden="true">·</span>
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
              class="text-meta text-ink underline-offset-2 hover:underline"
              onclick={() => (pairingPanelOpen = !pairingPanelOpen)}
            >
              player: {pairingData.pairedCount} paired, {unpaired.length} not
            </button>
          {/if}
        {/if}
        </span>
      </span>
      </span>
    {/snippet}
  </PageHeader>
{:else}
  <!-- The frame the header will fill, so the list under it does not move. -->
  {#if $detail.isError}
    <!-- A read that failed is not still loading: the title stops pulsing and
         the note under it says what happened. -->
    <PageHeader title="Playlist" path={[{ label: 'Playlists', href: '/playlists' }]} />
  {:else}
    <section class="flex flex-col gap-1 px-5 pt-3.5">
      <nav aria-label="Path" class="mono flex h-4 items-center gap-1.5 text-micro text-ink-4">
        <a href="/playlists" class="transition hover:text-ink hover:underline">Playlists</a>
      </nav>
      <h1
        aria-label="Loading playlist"
        class="skeleton inline-block w-fit min-w-48 text-title font-semibold text-transparent"
      >
        Loading playlist
      </h1>
      <div class="flex items-end justify-between gap-6">
        <Skeleton class="w-48" />
        <div class="flex shrink-0 items-center gap-2">{@render playlistActions()}</div>
      </div>
    </section>
  {/if}
{/if}

<div class="flex flex-col gap-3.5 px-5 pb-5">
  <!-- A failed refetch keeps the rows it has: the next one usually answers,
       and a table that vanished for 20 s after one slow answer read as data
       loss. Only a list never read shows the error. -->
  {#if $detail.isError && !$detail.data}
    <!-- The playlist is read, not written, so the note asks again by itself.
         It draws its own tinted band, which is the one this section used to
         draw by hand. -->
    <ErrorNote error={$detail.error} retry={() => $detail.refetch()} />
  {:else}
    <Settle pending={$detail.isPending}>
      {#snippet placeholder()}
        <section
      class="max-h-[calc(100dvh-10rem)] overflow-hidden border border-rule"
      aria-busy="true"
      aria-label="Loading playlist entries"
    >
      <div aria-hidden="true" class="flex h-10 flex-col justify-center gap-1.5 border-b border-rule px-3"><Skeleton class="h-3.5 w-2/5" /><Skeleton class="h-3 w-1/4" /></div>
      {#each Array.from({ length: playlistEntrySkeletonCount }) as _}
        <div aria-hidden="true" class="flex h-14 flex-col justify-center gap-1.5 border-b border-rule px-3 last:border-b-0"><Skeleton class="h-3.5 w-2/5" /><Skeleton class="h-3 w-1/4" /></div>
      {/each}
        </section>
      {/snippet}
    {#if pairingAsked && pairingPanelOpen}
      <section class="bg-panel">
        <div class="flex flex-wrap items-center gap-x-2.5 gap-y-1 px-4 py-3">
          <span class="text-body font-medium text-ink">Player pairing</span>
          {#if $pairing.isFetching}
            <Icon name="busy" size="sm" class="animate-spin text-ink-3" />
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
            class="flex max-h-[calc(100dvh-14rem)] flex-col overflow-hidden border-t border-rule"
            aria-busy="true"
            aria-label="Loading player pairing"
          >
            {#each Array.from({ length: 40 }) as _}
              <div aria-hidden="true" class="flex h-14 flex-col justify-center gap-1.5 border-b border-rule px-3 last:border-b-0"><Skeleton class="h-3.5 w-2/5" /><Skeleton class="h-3 w-1/4" /></div>
            {/each}
          </div>
        {:else if pairingData?.configured}
          {#if pairingData.checked < pairingData.acquiredCount || unpaired.length === 0}
            <div class="flex flex-col gap-1 px-4 pb-3">
              {#if pairingData.checked < pairingData.acquiredCount}
                <span class="numeric text-meta text-ink-3">
                  {pairingData.checked} of {pairingData.acquiredCount} checked before time ran out
                </span>
              {/if}
              {#if unpaired.length === 0}
                <span class="text-meta text-ink-3">every file paired</span>
              {/if}
            </div>
          {/if}
          {#if unpaired.length}
            <div class="flex flex-col border-t border-rule">
              {#each unpaired as file (`${file.entryId}-${file.position}`)}
                {@const state = reason(file.reason)}
                <div
                  class="flex flex-col gap-1 border-b border-rule px-4 py-2 last:border-b-0"
                >
                  <div class="flex items-center gap-3">
                    <span class="numeric text-meta text-ink-4">{rowNumber.get(file.entryId) ?? file.position}</span>
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
    {#if entries.length === 0}
      <!-- A Spotify list or a file is read by a job after it is followed; until
           that lands an empty list means the import has not finished, not that
           the list holds nothing. -->
      {#if playlist && !playlist.importedAt && (playlist.source === 'spotify' || playlist.source === 'file')}
        <EmptyPanel role="busy" heading="Importing playlist" level={3}>
          <p class="text-body text-ink-3">Songs appear when the import finishes.</p>
        </EmptyPanel>
      {:else}
        <EmptyPanel role="idle" heading="No songs in this playlist" level={3} />
      {/if}
    {:else}
    <!-- Rows under a hairline rather than inside a panel, the way the release
         page sets its track list. -->
    <section>
      <!-- The table is what scrolls sideways on a narrow screen, not the panel:
           the pager under it has to stay where it was put. -->
      <!-- Artist and album stand down on a narrow screen; the state does not.
           At 390px the table was 640px wide inside a sideways scroll with no
           scrollbar and no edge to show there was more, so the one column the
           page is opened for — whether each song is held, wanted or still being
           resolved — sat off the right edge unseen. The artist moves under the
           title in the same cell, which is where a phone reads it anyway. -->
      {#if entries.length > 1}
        <div class="px-4 pb-4">
          <StateStrip
            items={entries.map((entry) => ({ ...entry, state: states.get(entry.id)! }))}
            onopen={reveal}
          />
        </div>
      {/if}
      <div class="overflow-x-auto">
        <table class="w-full border-collapse text-left md:min-w-[640px]">
          <thead>
            <tr class="border-b border-rule text-micro font-mono uppercase tracking-[0.08em] text-ink-3">
              <th class="numeric px-4 py-2 font-medium">#</th>
              <th class="px-3 py-2 font-medium">Title</th>
              <th class="hidden px-3 py-2 font-medium md:table-cell">Artist</th>
              <th class="hidden px-3 py-2 font-medium xl:table-cell">Album</th>
              <th class="hidden px-3 py-2 text-right font-medium md:table-cell">Length</th>
              <th class="px-4 py-2 font-medium">State</th>
            </tr>
          </thead>
          <tbody>
            {#each drawn as entry, index (entry.id)}
              {@const state = states.get(entry.id)!}
              <!-- Only a row still waiting has anything to offer: a row a file
                   answers is finished, whatever its want still reads. -->
              {@const open = !entry.ownedFileId}
              {@const addressable = open && !!entry.targetId && entry.targetStatus === 'unresolved' && !entry.source}
              {@const seed = open ? entry.musicbrainzSeed : undefined}
              {@const decide = state.role === 'fail'}
              <!-- The reason is in the state's tooltip only; the line under the
                   row is drawn only when it carries something to do. -->
              {@const line = addressable || !!seed || decide}
              <!-- A note and the entry it belongs to are one row, so the border
                   waits for whichever of the two is last. -->
              <tr
                id="entry-{entry.id}"
                class="want-row scroll-mt-24 {line ? '' : 'border-b border-rule last:border-b-0'}"
              >
                <td class="numeric px-4 py-1.5 text-meta text-ink-4">{index + 1}</td>
                <td class="max-w-64 px-3 py-1.5 text-body font-medium text-ink">
                  <span class="block truncate">{entry.title}</span>
                  <span class="block truncate text-meta font-normal text-ink-3 md:hidden">
                    {[entry.artist, length(entry)].filter(Boolean).join(' · ')}
                  </span>
                </td>
                <td class="hidden max-w-48 truncate px-3 py-1.5 text-meta text-ink-2 md:table-cell">
                  {entry.artist}
                </td>
                <td class="hidden max-w-48 truncate px-3 py-1.5 text-meta text-ink-3 xl:table-cell">
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
                  {:else if decide}
                    <a
                      href={reviewHref(entry)}
                      class="inline-flex text-fail hover:text-ink"
                      title={[state.label, state.note].filter(Boolean).join(' · ')}
                      aria-label="{state.label}, open in Review"
                    >
                      <Icon name={state.icon} size="md" />
                    </a>
                  {:else if state.label === 'not on wishlist'}
                    <!-- The + on the hovered or focused row only, the way a
                         player offers it. Once the want exists the row shows
                         its state instead, so there is nothing to take back
                         here. -->
                    <WantToggle
                      reveal
                      wanted={false}
                      title={entry.title}
                      onwant={() => $wantEntry.mutateAsync(entry.id)}
                      onunwant={() => {}}
                    />
                  {:else}
                    <!-- The reason rides in the tooltip with the word. -->
                    <StateIcon icon={state.icon} label={state.label} role={state.role} note={state.note} />
                  {/if}
                </td>
              </tr>
              {#if line}
                <!-- One line under the row: what happened, then what can be
                     done about it. "Use address" opens its form under it. -->
                <tr class="border-b border-rule last:border-b-0">
                  <td></td>
                  <td class="px-3 pb-2" colspan="5">
                    <span class="flex flex-wrap items-baseline gap-x-2 gap-y-1">
                      {#if decide}
                        <a
                          href={reviewHref(entry)}
                          class="text-meta font-medium text-ink-2 underline decoration-rule-strong underline-offset-[3px] hover:text-ink"
                        >
                          Decide in Review
                        </a>
                      {/if}
                      {#if seed}
                        <AddToMusicBrainz {seed} />
                      {/if}
                      {#if addressable}
                        <UseAddress
                          targetId={entry.targetId!}
                          entryTitle={entry.title}
                          entryArtist={entry.artist}
                          entryDurationMs={entry.durationMs}
                        />
                      {/if}
                    </span>
                  </td>
                </tr>
              {/if}
            {/each}
          </tbody>
        </table>
      </div>
      {#if drawn.length < entries.length}
        <div use:nearEnd={{ drawn: drawn.length, more: drawMore }} aria-hidden="true"></div>
      {/if}
    </section>
    {/if}
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

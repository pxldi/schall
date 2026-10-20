<script lang="ts">
  import Icon from './Icon.svelte';
  // Where the library holds the same recording twice, and which of the copies
  // it can prove is the lesser one.
  //
  // The matcher works at track level and the catalogue holds one recording as
  // one track row per release carrying it, so an album copy and the single
  // lifted from it are matched to two different rows and nothing collides. This
  // asks the other question — which recording is on disc more than once — and
  // now answers a second one beside it: of these copies, is one of them simply
  // a worse version of another.
  //
  // Deleting is still a press. Schall works out which copy to keep, because
  // that is a comparison of stored numbers nobody should have to do by hand,
  // but removing music is the one thing here that cannot be undone and it never
  // happens on its own.
  import { tick } from 'svelte';
  import { toStore } from 'svelte/store';
  import { createMutation, createQuery, useQueryClient } from '@tanstack/svelte-query';
  import { api, type DuplicateCopy, type DuplicateRecording } from '$lib/api';
  import { formatBytes, formatDuration } from '$lib/utils';
  import { listening } from '$lib/preview.svelte';
  import { useKeys } from '$lib/keys.svelte';
  import { glide } from '$lib/motion.svelte';
  import Button from '$lib/components/Button.svelte';
  import EmptyPanel from '$lib/components/EmptyPanel.svelte';
  import Settle from '$lib/components/Settle.svelte';
  import SkeletonRows from '$lib/components/SkeletonRows.svelte';
  import Pager from '$lib/components/Pager.svelte';
  import Segmented from '$lib/components/Segmented.svelte';
  import ErrorNote from '$lib/components/ErrorNote.svelte';
  import TranscodeChip from '$lib/components/TranscodeChip.svelte';
  import KeepOneCopyDialog from '$lib/components/KeepOneCopyDialog.svelte';

  // All three library views stay mounted, so `active` says whether this one is
  // showing. Only the one showing answers keys.
  let { active = true }: { active?: boolean } = $props();

  const views = ['to-decide', 'kept'] as const;
  type View = (typeof views)[number];

  const queryClient = useQueryClient();
  let view = $state<View>('to-decide');
  let offset = $state(0);
  const pageSize = 25;
  // The failure itself rather than the sentence it was flattened into: the
  // panel writes the words, and needs the status and the server's own title to
  // choose them.
  let failure = $state<unknown>(null);
  // What a press is about to destroy, held until it is pressed again. A single
  // id is enough: asking about a second thing forgets the first, which is what
  // somebody who changed their mind would want anyway.
  let confirming = $state('');

  // Hearing a copy. One <audio> element for the whole page rather than one per
  // row — a page is twenty-five groups of two or three copies, and that many
  // media elements is that many things the browser holds open to play one
  // file. Which copy it is pointed at is the whole of the state, and the
  // `listening` agreement silences this one if another player on the page
  // starts.
  let audio = $state<HTMLAudioElement | null>(null);
  let sounding = $state('');

  $effect(() => {
    if (sounding && listening.sounding !== api.libraryFileAudioUrl(sounding)) silence();
  });

  function silence() {
    if (sounding) audio?.pause();
    sounding = '';
  }

  function hear(fileId: string) {
    if (!audio) return;
    if (sounding === fileId) {
      silence();
      return;
    }
    const src = api.libraryFileAudioUrl(fileId);
    sounding = fileId;
    listening.sounding = src;
    audio.src = src;
    audio.volume = listening.volume;
    void audio.play().catch(() => (sounding = ''));
  }

  // Both halves are read at once, because the strip above them says how many
  // are in each and a count nobody asked for is a count that is missing.
  const toDecide = createQuery(
    toStore(() => ({
      queryKey: ['library-duplicates', 'to-decide', offset],
      queryFn: () => api.duplicateRecordings(false, pageSize, offset)
    }))
  );
  const kept = createQuery(
    toStore(() => ({
      queryKey: ['library-duplicates', 'kept', offset],
      queryFn: () => api.duplicateRecordings(true, pageSize, offset)
    }))
  );

  // A press can take the last group off the page it was on -- decided, kept,
  // or deleted away -- and land the reader on a page with nothing on it and
  // no way back: the pager itself draws nothing once total <= pageSize, so a
  // stranded reader on a page beyond that has no Previous button either.
  // Retreat to the last page that still holds something instead.
  function clampPage() {
    const total = shown.data?.total ?? 0;
    const lastOffset = Math.max(0, (Math.ceil(total / pageSize) - 1) * pageSize);
    if (offset > lastOffset) offset = lastOffset;
  }

  async function refresh() {
    failure = null;
    confirming = '';
    // The answer is written on the files, so the file list and the library
    // counts are stale too.
    await Promise.all([
      queryClient.invalidateQueries({ queryKey: ['library-duplicates'] }),
      queryClient.invalidateQueries({ queryKey: ['library-files'] }),
      queryClient.invalidateQueries({ queryKey: ['library'] })
    ]);
    clampPage();
  }

  function setView(next: View) {
    view = next;
    offset = 0;
    silence();
  }

  function changePage(next: number) {
    offset = next;
    silence();
  }

  const answer = createMutation({
    mutationFn: (asked: { recordingId: string; keep: boolean }) =>
      asked.keep
        ? api.keepRecordingCopies(asked.recordingId)
        : api.askAboutRecordingAgain(asked.recordingId),
    onSuccess: refresh,
    onError: (error: Error) => {
      failure = error;
    }
  });

  // Deleting runs one file at a time and stops at the first refusal, so a
  // failure halfway through a group leaves what is left listed rather than
  // silently half-done.
  const remove = createMutation({
    mutationFn: async (fileIds: string[]) => {
      for (const fileId of fileIds) {
        await api.deleteLibraryFile(fileId);
      }
    },
    onSuccess: refresh,
    onError: async (error: Error) => {
      failure = error;
      confirming = '';
      // A refusal partway through still deletes the files before the one
      // that failed, so the page can empty out here too.
      await queryClient.invalidateQueries({ queryKey: ['library-duplicates'] });
      clampPage();
    }
  });

  // Keeping one copy is the other answer, and the one that deletes music. The
  // person names the copy that stays and Schall takes the rest away, so it is
  // asked once, in a dialog that names every file by path and size.
  const keepOne = createMutation({
    mutationFn: (asked: { recordingId: string; fileId: string; deleting: string[] }) =>
      api.keepOneCopy(asked.recordingId, asked.fileId, asked.deleting),
    onSuccess: async (result) => {
      chosen = null;
      // Some of the copies really are gone even when the press did not finish
      // every one of them, so that is shown rather than swallowed by a dialog
      // that just closes as if everything asked for happened.
      failure = result.stopped ? new Error(result.stopped) : null;
      await refresh();
    }
    // A refusal stays in the dialog, which stays open. The person is still
    // looking at the choice it is about.
  });

  // The copy the dialog is open about, or null when it is closed.
  let chosen = $state<{ recording: DuplicateRecording; copy: DuplicateCopy } | null>(null);

  // Trying again is the only action a stuck file has, and it is one press.
  const retry = createMutation({
    mutationFn: () => api.retryRemovals(),
    onSuccess: refresh,
    onError: (error: Error) => {
      failure = error;
    }
  });

  const shown = $derived(view === 'kept' ? $kept : $toDecide);
  const recordings = $derived(shown.data?.recordings ?? []);
  const busy = $derived(
    $answer.isPending || $remove.isPending || $keepOne.isPending || $retry.isPending
  );

  // Read off whichever half is on screen; both carry the same list, because a
  // file the disc kept belongs to neither question.
  const stranded = $derived(shown.data?.stillOnDisc ?? []);

  // Whether keeping this copy would take anything away. A copy that is also a
  // copy of other music is never deleted, so a group whose every other copy is
  // one of those has nothing to offer.
  function goingWith(recording: DuplicateRecording, keeper: DuplicateCopy) {
    return recording.copies
      .filter((copy) => copy.id !== keeper.id && !copy.answersOther)
      .map((copy) => copy.id);
  }

  function ask(recording: DuplicateRecording, copy: DuplicateCopy) {
    failure = null;
    confirming = '';
    // A refusal from a press before this one is about a choice that is over.
    // Without this the dialog opens with the old note already in it.
    $keepOne.reset();
    chosen = { recording, copy };
  }

  // Every copy the library can prove it does not need, across the whole list.
  // Only ever taken from the half still to decide: a group somebody said they
  // meant to have is not waiting on anything.
  const spare = $derived(
    view === 'to-decide'
      ? recordings.filter((recording) => recording.decided).flatMap(redundant)
      : []
  );

  function redundant(recording: DuplicateRecording) {
    return recording.copies.filter((copy) => copy.redundant).map((copy) => copy.id);
  }

  function name(recording: DuplicateRecording) {
    return [recording.title, recording.artist].filter(Boolean).join(' · ') || 'Untitled song';
  }

  // What a copy is, in the order somebody compares them by.
  //
  // A group is still to decide while any copy of it is unanswered, so one that
  // was answered on its own can appear in that list beside the ones that were
  // not, and it says so. In the kept list every copy was kept and saying it on
  // each row would be noise.
  function details(copy: DuplicateCopy) {
    const answered = view === 'to-decide' && copy.keptAt ? 'kept on purpose' : '';
    // A copy Schall can prove is a lesser version of another one is drawn
    // dimmed, and it is also the copy the Delete button would remove. Dimming
    // said which rows were different from the others and never said how, so the
    // word says it: this is the row that goes.
    const lesser = copy.redundant ? 'lesser copy' : '';
    return [lesser, copy.release, quality(copy), copy.reason, answered].filter(Boolean).join(' · ');
  }

  // The audio itself, empty for a file no scan has listened to yet. Channel
  // counts are named rather than numbered, because "2 ch" is a unit nobody
  // says out loud.
  function quality(copy: DuplicateCopy) {
    const parts = [];
    if (copy.bitRateKbps) parts.push(`${copy.bitRateKbps} kbps`);
    if (copy.sampleRateHz) parts.push(`${(copy.sampleRateHz / 1000).toFixed(1).replace(/\.0$/, '')} kHz`);
    if (copy.channels === 1) parts.push('mono');
    else if (copy.channels === 2) parts.push('stereo');
    else if (copy.channels) parts.push(`${copy.channels} channels`);
    return parts.join(' · ');
  }

  function keepLabel(recording: DuplicateRecording) {
    return recording.copies.length === 2 ? 'Keep both' : 'Keep all';
  }

  function deleteLabel(count: number) {
    return count === 1 ? 'Delete the other copy' : `Delete the other ${count} copies`;
  }

  function press(key: string, fileIds: string[]) {
    if (confirming !== key) {
      failure = null;
      confirming = key;
      return;
    }
    $remove.mutate(fileIds);
  }

  // The cursor (ADR 0042): the group J and K move between, and Space plays the
  // copy Schall says to keep, or the first one where it says none. Deleting is
  // never a key: it is a button, asked about again before it happens.
  let at = $state(0);
  let list = $state<HTMLElement>();

  $effect(() => {
    void [view, offset];
    at = 0;
  });

  $effect(() => {
    if (at > 0 && at >= recordings.length) at = Math.max(0, recordings.length - 1);
  });

  function move(by: number) {
    if (recordings.length === 0) return;
    at = Math.min(Math.max(at + by, 0), recordings.length - 1);
    void tick().then(() =>
      list?.querySelector('[data-cursor="true"]')?.scrollIntoView?.({ block: 'nearest' })
    );
  }

  function listen() {
    const copies = recordings[at]?.copies ?? [];
    const copy = copies.find((candidate) => candidate.keeper) ?? copies[0];
    if (copy) hear(copy.id);
  }

  useKeys(() =>
    active
      ? [
          {
            keys: ['j', 'k', 'arrowdown', 'arrowup'],
            caps: ['j', 'k'],
            verb: 'move',
            order: 0,
            enabled: () => recordings.length > 0,
            run: (event) => move(['k', 'arrowup'].includes(event.key.toLowerCase()) ? -1 : 1)
          },
          {
            keys: [' '],
            verb: 'play',
            order: 1,
            enabled: () => recordings.length > 0,
            run: listen
          }
        ]
      : []
  );
</script>

<div
  role="group"
  aria-label="Which duplicates to look at"
  class="flex flex-wrap items-center gap-x-3 gap-y-2 border-y border-rule px-5 py-2"
>
  <Segmented
    options={[
      { value: 'to-decide', name: 'To decide', count: $toDecide.data?.total },
      { value: 'kept', name: 'Kept on purpose', count: $kept.data?.total }
    ]}
    value={view}
    onchange={(value) => setView(value as View)}
    label="Which duplicates to look at"
  />

  <!-- Deleting is permanent, so the one thing worth saying is what Schall will
       and will not offer to delete. Why an album copy and a single land here,
       and what happens when nothing separates two copies, are the mechanism:
       the rows themselves say which case each group is. -->
  <p class="min-w-0 text-meta text-ink-3">
    These songs are on disk more than once. Schall only offers to delete a copy it can prove is
    worse.
  </p>
</div>

<div class="flex flex-col gap-2 px-5 pt-3">
  {#if shown.error}
    <ErrorNote error={shown.error} retry={() => void shown.refetch()} />
  {/if}
  {#if failure}
    <ErrorNote error={failure} />
  {/if}

  <!-- One <audio> element for the whole page. Where this one points is the copy
       that is sounding. -->
  <audio bind:this={audio} class="hidden" preload="none" onended={() => (sounding = '')}></audio>

  {#if stranded.length > 0}
    <!-- The library has let these go and the disc still has them. Nothing else
         on this screen would ever mention them again: they have no library
         row, and their recording is no longer held twice. -->
    <div class="flex flex-col gap-2 border-b border-rule pb-2.5">
      <div class="flex flex-wrap items-center gap-2.5">
        <span class="min-w-0 flex-1 text-meta text-ink-2">
          {stranded.length}
          {stranded.length === 1 ? 'file' : 'files'} left the library but couldn't be deleted.
          Still on disk.
        </span>
        <Button
          variant="outline"
          size="sm"
          class="shrink-0"
          disabled={busy}
          onclick={() => $retry.mutate()}
        >
          Try again
        </Button>
      </div>
      {#each stranded as file (file.path)}
        <div class="flex flex-wrap items-baseline gap-x-3">
          <span class="mono min-w-0 flex-1 truncate text-meta text-ink-3" title={file.path}>
            {file.path}
          </span>
          {#if file.reason}
            <span class="truncate text-meta text-ink-3">{file.reason}</span>
          {/if}
        </div>
      {/each}
    </div>
  {/if}

  {#if spare.length > 0}
    <div class="flex flex-wrap items-center gap-2.5 border-b border-rule pb-2.5">
      <span class="min-w-0 flex-1 text-meta text-ink-2">
        {spare.length}
        {spare.length === 1 ? 'copy on this page is' : 'copies on this page are'} worse than one
        you have. Deleting can't be undone.
      </span>
      <Button
        variant={confirming === 'all' ? 'danger' : 'outline'}
        size="sm"
        class="shrink-0"
        disabled={busy}
        onclick={() => press('all', spare)}
      >
        {confirming === 'all' ? 'Confirm delete' : `Delete ${spare.length}`}
      </Button>
    </div>
  {/if}
</div>

<Settle pending={shown.isPending}>
  {#snippet placeholder()}
    <div class="px-5 pt-3">
      <SkeletonRows count={6} label="Loading duplicates" />
    </div>
  {/snippet}
  <div class="px-5">
    <div class="relative" bind:this={list} use:glide={{ key: at }}>
      {#each recordings as recording, index (recording.recordingId)}
        {@const here = index === at}
        <!-- The hairline sits on the whole group, so the copies inside it read as
             one entry. Comparing the copies is the rows themselves: each names
             its own format, its path, its length and size, and offers Play and
             Keep this. The cursor is the group, drawn as one inverted block. -->
        <div
          data-cursor={here}
          data-recording={recording.recordingId}
          aria-current={here ? 'true' : undefined}
          class="row flex flex-col px-3 py-2.5 {here ? 'on-ink' : ''}"
          onclick={() => (at = index)}
          role="presentation"
        >
          <div class="flex items-baseline gap-2 pb-1.5">
            <span class="min-w-0 flex-1 truncate text-lead font-semibold">{name(recording)}</span>
            <span class="mono text-meta text-ink-3">{recording.copies.length} copies</span>
          </div>

          {#if recording.verdict}
            <p class="pb-1.5 text-meta leading-4 text-ink-3">{recording.verdict}</p>
          {/if}

          {#each recording.copies as copy (copy.id)}
            <div
              class="flex flex-wrap items-center gap-x-3 gap-y-1 py-1 md:grid md:grid-cols-[16px_70px_minmax(0,1fr)_60px_84px_auto] md:items-center md:gap-y-0"
              class:opacity-55={copy.redundant}
            >
              <!-- A filled square on the copy Schall can prove is the better one;
                   an empty one everywhere else, including a group nothing
                   separates. -->
              <span class="hidden size-4 shrink-0 place-items-center md:grid" aria-hidden="true">
                <span
                  class="size-2.5 border {copy.keeper
                    ? 'border-ink bg-ink'
                    : 'border-rule-strong bg-transparent'}"
                ></span>
              </span>
              <span class="mono shrink-0 border border-rule-strong px-1.5 py-[2px] text-center text-micro font-medium text-ink-2">
                {copy.format || 'file'}
              </span>
              <span class="min-w-0 flex-1">
                <span class="mono block truncate text-meta text-ink-2" title={copy.path}>
                  {copy.path}
                </span>
                <span class="mt-0.5 block truncate text-meta text-ink-3">
                  {details(copy)}
                </span>
                <!-- A copy whose sound stops where a lossy encoder would have
                     stopped it. It is quality and not identity: both copies are
                     the same recording, and this one was squeezed before it was
                     put in the container it is in. Nothing above reads it, so it
                     is here for the person deciding. -->
                {#if copy.transcodeSuspected}
                  <span class="mt-1 block">
                    <TranscodeChip cutoffHz={copy.spectralCutoffHz} />
                  </span>
                {/if}
              </span>
              <span class="mono text-right text-meta text-ink-2">
                {formatDuration(copy.durationMs)}
              </span>
              <span class="mono text-right text-meta text-ink-2">
                {formatBytes(copy.sizeBytes)}
              </span>
              <span class="flex shrink-0 items-center justify-end gap-1">
                <Button variant="ghost" size="sm" onclick={() => hear(copy.id)}>
                  {#if sounding === copy.id}
                    <Icon name="pause" size={12} /> Stop
                  {:else}
                    <Icon name="play" size={12} /> Play
                  {/if}
                </Button>
                {#if view === 'to-decide'}
                  <Button
                    variant={copy.keeper ? 'primary' : 'outline'}
                    size="sm"
                    disabled={busy || goingWith(recording, copy).length === 0}
                    onclick={() => ask(recording, copy)}
                  >
                    Keep this
                  </Button>
                {/if}
              </span>
            </div>
          {/each}

          <!-- The recording's own decision, under its copies rather than beside
               its title: keeping one deletes the rest, and the note and the
               button that does it stand together. -->
          <div class="flex flex-wrap items-center gap-2.5 pt-1.5">
            {#if view === 'kept'}
              <Button variant="ghost" size="sm" disabled={busy} onclick={() => $answer.mutate({ recordingId: recording.recordingId, keep: false })}>
                Ask again
              </Button>
            {:else}
              <span class="min-w-0 flex-1 text-meta text-ink-3">Keeping one copy deletes the other.</span>
              {#if recording.decided}
                <Button
                  variant={confirming === recording.recordingId ? 'danger' : 'outline'}
                  size="sm"
                  class="shrink-0"
                  disabled={busy}
                  onclick={() => press(recording.recordingId, redundant(recording))}
                >
                  {confirming === recording.recordingId
                    ? 'Confirm delete'
                    : deleteLabel(redundant(recording).length)}
                </Button>
              {/if}
              <Button variant="ghost" size="sm" class="shrink-0" disabled={busy} onclick={() => $answer.mutate({ recordingId: recording.recordingId, keep: true })}>
                {keepLabel(recording)}
              </Button>
            {/if}
          </div>
        </div>
      {:else}
        <!-- An empty list and a failed request are different claims. The ErrorNote
             above reports the failure; this renders nothing so "No recording is
             on disc twice" does not also appear. Nothing held twice is nothing
             left to do; nothing kept on purpose is nothing yet. -->
        {#if !shown.isError}
          <EmptyPanel
            role={view === 'kept' ? 'idle' : 'ok'}
            heading={view === 'kept' ? 'Nothing kept on purpose yet' : 'No song on disk twice'}
          />
        {/if}
      {/each}
    </div>
  </div>

  <Pager total={shown.data?.total ?? 0} {offset} {pageSize} keys={active} onchange={changePage} />
</Settle>

{#if chosen}
  <KeepOneCopyDialog
    recording={chosen.recording}
    keeper={chosen.copy}
    busy={$keepOne.isPending}
    failure={$keepOne.error}
    onconfirm={() =>
      $keepOne.mutate({
        recordingId: chosen!.recording.recordingId,
        fileId: chosen!.copy.id,
        // Exactly the files the dialog named, so the server can refuse if the
        // library no longer agrees with what was read.
        deleting: goingWith(chosen!.recording, chosen!.copy)
      })}
    oncancel={() => (chosen = null)}
  />
{/if}


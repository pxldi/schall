<script lang="ts">
  import Icon from './Icon.svelte';
  import { tick } from 'svelte';
  import { goto } from '$app/navigation';
  import { createMutation, createQuery, keepPreviousData, useQueryClient } from '@tanstack/svelte-query';
  import {
    api,
    type SoundCloudNamingFields,
    type SoundCloudTrack,
    type Upload,
    type UploadStatus
  } from '$lib/api';
  import { describeError } from '$lib/errors';
  import { formatBytes } from '$lib/utils';
  import Button from '$lib/components/Button.svelte';
  import Skeleton from '$lib/components/Skeleton.svelte';
  import Chip from '$lib/components/Chip.svelte';
  import EmptyPanel from '$lib/components/EmptyPanel.svelte';
  import ErrorNote from '$lib/components/ErrorNote.svelte';
  import Settle from '$lib/components/Settle.svelte';
  import SoundCloudNamingFieldsInput from '$lib/components/SoundCloudNamingFields.svelte';
  import UploadResolvePass from '$lib/components/UploadResolvePass.svelte';
  import StateMark from '$lib/components/StateMark.svelte';
  import UploadRow from '$lib/components/UploadRow.svelte';
  import { glide, slideInto } from '$lib/motion.svelte';
  import { useKeys } from '$lib/keys.svelte';
  import { arrivals, leaveMs, listCursor, settleDelays, strike } from '$lib/live-list.svelte';

  type Role = 'ok' | 'idle' | 'decide' | 'busy' | 'fail';

  // The types a scan indexes, which is the whole of what the library can hold.
  // The server refuses anything else; this only spares somebody the discovery.
  const acceptedTypes =
    '.mp3,.flac,.ogg,.oga,.opus,.m4a,.m4b,.m4p,.mp4,.dsf,.aiff,.aif,.wav';

  const queryClient = useQueryClient();
  let chosen = $state<File[]>([]);
  let picker = $state<HTMLInputElement | null>(null);
  let progress = $state(0);

  // The address is offered only for a single file: one address names one
  // track, so asking for it over a selection of five would be a claim about
  // files nobody looked at. Changing the selection away from exactly one
  // file drops whatever was found, below.
  let soundCloudUrl = $state('');
  let soundCloudTrack = $state<SoundCloudTrack | null>(null);
  let soundCloudNamed = $state<SoundCloudNamingFields>({ artist: '', title: '', remixer: '' });

  $effect(() => {
    if (chosen.length !== 1) resetSoundCloud();
  });

  function resetSoundCloud() {
    soundCloudUrl = '';
    soundCloudTrack = null;
    soundCloudNamed = { artist: '', title: '', remixer: '' };
    $lookSoundCloud.reset();
  }

  // The same key the page uses for its tab badge, so the two are one request
  // and one poll rather than two.
  const uploads = createQuery({
    queryKey: ['uploads'],
    queryFn: api.uploads,
    placeholderData: keepPreviousData,
    // The import announces itself over the event stream, so this is the safety
    // net rather than the mechanism: it covers a stream blocked by a proxy or
    // dropped without reconnecting yet, and stops once nothing is moving.
    refetchInterval: (query) =>
      query.state.data?.items.some((item) =>
        ['staged', 'queued', 'validating'].includes(item.status) ||
        item.fileStates?.some((file) => file.state === 'importing')
      )
        ? 15_000
        : false
  });

  const items = $derived($uploads.data?.items ?? []);
  const chosenBytes = $derived(chosen.reduce((total, file) => total + file.size, 0));
  const maxBytes = $derived($uploads.data?.maxBytes);
  const tooLarge = $derived(maxBytes !== undefined && maxBytes > 0 && chosenBytes > maxBytes);
  // A pasted address nobody has looked up yet, or looked up but not confirmed,
  // would otherwise be silently dropped: Upload waits for it to resolve one
  // way or the other rather than sending the file address-less by accident.
  const unresolvedSoundCloudAddress = $derived(
    chosen.length === 1 && soundCloudUrl.trim() !== '' && !soundCloudTrack
  );
  // A 503 is an installation with no staging folder rather than a failure, and
  // it is the one error this page can tell somebody how to fix. The words read
  // are the server's own, which is what `describeError` keeps as `raw`.
  const unconfigured = $derived(
    $uploads.isError && /not configured/i.test(describeError($uploads.error).raw)
  );

  // Looking up an address is a read, the same request the naming dialog on a
  // file's own row makes. Confirming it happens as part of the upload itself
  // rather than as a second write, because the file has no row yet to write
  // it against.
  const lookSoundCloud = createMutation({
    mutationFn: (url: string) => api.soundCloudTrack(url),
    onSuccess: (track) => {
      soundCloudTrack = track;
      soundCloudNamed = {
        artist: track.suggested?.artist || track.uploader,
        title: track.suggested?.title || track.trackTitle,
        remixer: track.suggested?.remixer ?? ''
      };
    }
  });

  const send = createMutation({
    mutationFn: (files: File[]) =>
      api.uploadFiles(
        files,
        (fraction) => {
          progress = fraction;
        },
        chosen.length === 1 && soundCloudTrack
          ? {
              sourceUrl: soundCloudTrack.permalink,
              artist: soundCloudNamed.artist.trim(),
              title: soundCloudNamed.title.trim(),
              remixer: soundCloudNamed.remixer.trim()
            }
          : undefined
      ),
    onSuccess: async () => {
      chosen = [];
      progress = 0;
      resetSoundCloud();
      if (picker) picker.value = '';
      await queryClient.invalidateQueries({ queryKey: ['uploads'] });
    }
  });

  const discard = createMutation({
    mutationFn: (uploadId: string) => api.discardUpload(uploadId),
    onSuccess: async () => {
      await queryClient.invalidateQueries({ queryKey: ['uploads'] });
    }
  });

  function choose(event: Event) {
    const input = event.currentTarget as HTMLInputElement;
    chosen = Array.from(input.files ?? []);
  }

  // Editing the address takes the answer off the screen, the same as it does
  // in the naming dialog: what is shown has to be what Upload will act on.
  function updateSoundCloudUrl(event: Event) {
    soundCloudUrl = (event.currentTarget as HTMLInputElement).value;
    soundCloudTrack = null;
    soundCloudNamed = { artist: '', title: '', remixer: '' };
    $lookSoundCloud.reset();
  }

  function label(upload: Upload): { role: Role; text: string } {
    switch (upload.status) {
      case 'imported':
        return { role: 'ok', text: 'Imported' };
      case 'validating':
        return { role: 'busy', text: 'Validating' };
      case 'queued':
        return { role: 'busy', text: 'Queued' };
      case 'failed':
        return { role: 'fail', text: 'Refused' };
      default:
        return { role: 'idle', text: 'Staged' };
    }
  }

  function detail(upload: Upload) {
    if (upload.detail) return upload.detail;
    if (upload.importPath) return upload.importPath;
    if (upload.status === 'staged') return 'waiting to import';
    return upload.files.join(' · ');
  }

  // Where an imported upload landed in Library. The row is the only place its
  // file name is known, and without this the person is told to go and find it
  // among twenty thousand others. The search is the file name without its
  // extension, which is what the library search matches on.
  //
  // view=files is the half of this that matters. Library opens on Releases, and
  // an upload that has not been identified yet belongs to no release — so the
  // link has to name the files view or it lands on a search that cannot find
  // the file whatever it is given.
  function libraryLink(upload: Upload) {
    const name = upload.files[0];
    if (!name) return null;
    const search = name.replace(/\.[^./]+$/, '');
    return `/library?view=files&q=${encodeURIComponent(search)}`;
  }

  const busyStatuses: UploadStatus[] = ['queued', 'validating'];
  const busy = $derived(
    items.some(
      (item) =>
        busyStatuses.includes(item.status) ||
        item.fileStates?.some((file) => file.state === 'importing')
    )
  );
  const header = $derived.by((): { role: Role; text: string } => {
    if (unconfigured) return { role: 'idle', text: 'Not configured' };
    if ($send.isPending) return { role: 'busy', text: 'Uploading' };
    if (busy) return { role: 'busy', text: 'Importing' };
    return { role: 'ok', text: 'Ready' };
  });

  // A discard, an import settling and a refetch each change the list the
  // reader is looking at. Only a discard strikes a row; the rest are the
  // list being replaced.
  // Whether the list had rows a moment ago, so the empty panel waits for the
  // last one to leave instead of arriving over it.
  let hadRows = $state(false);
  $effect(() => {
    if (items.length > 0) hadRows = true;
  });

  const ids = $derived(items.map((item) => item.id));
  const cursor = listCursor(() => ids);
  const arrived = arrivals(() => ids);

  let rows = $state<HTMLElement>();
  // Rows slide to their new places over the arriving step, and the cursor block
  // measures them where they are, so it is placed again once they have landed.
  let settled = $state(0);
  $effect(() => {
    void ids.join('|');
    // Once the slide has landed, and once a leaving row has folded away.
    const timers = settleDelays().map((ms) => setTimeout(() => settled++, ms));
    return () => timers.forEach(clearTimeout);
  });

  function moveCursor(by: number) {
    cursor.move(by);
    void tick().then(() =>
      rows?.querySelector('[data-cursor="true"]')?.scrollIntoView?.({ block: 'nearest' })
    );
  }

  useKeys(() => [
    {
      keys: ['j', 'k'],
      caps: ['j', 'k'],
      verb: 'move',
      order: 0,
      enabled: () => items.length > 0,
      run: (event) => moveCursor(event.key.toLowerCase() === 'j' ? 1 : -1)
    },
    {
      keys: ['enter'],
      verb: 'open',
      order: 2,
      enabled: () => {
        const upload = items[cursor.at];
        return Boolean(upload && upload.status === 'imported' && libraryLink(upload));
      },
      run: () => {
        const upload = items[cursor.at];
        const link = upload && upload.status === 'imported' ? libraryLink(upload) : null;
        if (link) void goto(link);
      }
    }
  ]);
</script>

<div class="flex flex-col gap-4 px-5 pt-3 pb-6">
  <div class="flex flex-wrap items-center gap-x-3 gap-y-2">
    <span
      class="mono truncate bg-panel px-2 py-[3px] text-micro text-ink-2"
      title={$uploads.data?.stagingPath || 'Staging path'}
    >
      {$uploads.data?.stagingPath || '—'}
    </span>

    <Chip role={header.role}>{header.text}</Chip>

    <!-- The limit is what somebody needs to know before choosing files; what an
         upload is for is not. -->
    <span class="mono hidden text-micro text-ink-4 lg:block">
      up to {maxBytes && maxBytes > 0 ? formatBytes(maxBytes) : '—'} at a time
    </span>

    <div class="ml-auto flex items-center gap-2">
      <Button
        variant="outline"
        key="f"
        verb="choose files"
        disabled={unconfigured || $send.isPending}
        onclick={() => picker?.click()}
      >
        Choose files
      </Button>
      <Button
        variant="primary"
        key="u"
        verb="upload"
        disabled={chosen.length === 0 ||
          tooLarge ||
          unresolvedSoundCloudAddress ||
          $send.isPending}
        onclick={() => $send.mutate(chosen)}
      >
        {#if $send.isPending}
          <Icon name="busy" size={13} class="animate-spin" /> Uploading
        {:else}
          <Icon name="upload" size={13} /> Upload
        {/if}
      </Button>
    </div>
  </div>

  <input
    bind:this={picker}
    type="file"
    multiple
    accept={acceptedTypes}
    class="hidden"
    aria-label="Choose files to upload"
    onchange={choose}
  />

  <!-- One address names one track, so the link below is only offered over a
       single chosen file. Any other selection (none chosen, or several) falls
       back to naming from the row in Library, the way every upload has always
       been named. -->
  {#if !unconfigured && chosen.length !== 1}
    <p class="text-meta text-ink-3">
      SoundCloud edits land unmatched. Name one in Library with "This is a SoundCloud track".
    </p>
  {/if}

  {#if unconfigured}
    <EmptyPanel role="decide" heading="Uploads not set up">
      <p class="text-meta leading-relaxed text-ink-2">
        Set <span class="mono">SCHALL_UPLOAD_STAGING_PATH</span> to a folder outside every music
        folder, and <span class="mono">SCHALL_IMPORT_LIBRARY_PATH</span> to the managed library
        uploads are filed into.
      </p>
    </EmptyPanel>
  {/if}

  {#if chosen.length > 0}
    <section class="rise flex flex-col gap-3">
      <div class="flex flex-wrap items-center gap-2.5">
        <span class="label">Ready to send</span>
        <span class="mono whitespace-nowrap text-micro text-ink-4">
          {chosen.length}
          {chosen.length === 1 ? 'file' : 'files'} · {formatBytes(chosenBytes)}
        </span>
        <span class="h-px flex-1 bg-rule"></span>
      </div>

      <div class="flex flex-col">
        {#each chosen as file (file.name)}
          <div class="flex min-h-9 items-center gap-3 border-b border-rule py-1.5 last:border-b-0">
            <span class="mono min-w-0 flex-1 truncate text-micro text-ink" title={file.name}>
              {file.name}
            </span>
            <span class="mono whitespace-nowrap text-micro text-ink-4">{formatBytes(file.size)}</span>
          </div>
        {/each}
      </div>

      {#if chosen.length === 1}
        <div class="flex flex-col gap-3">
          <div>
            <label for="upload-soundcloud-url" class="label">SoundCloud link</label>
            <input
              id="upload-soundcloud-url"
              class="field mt-2 w-full"
              value={soundCloudUrl}
              oninput={updateSoundCloudUrl}
              placeholder="https://soundcloud.com/artist/track"
              autocomplete="off"
              maxlength="500"
            />
          </div>

          {#if $lookSoundCloud.isError}<ErrorNote error={$lookSoundCloud.error} bare />{/if}

          {#if soundCloudTrack}
            <SoundCloudNamingFieldsInput track={soundCloudTrack} bind:named={soundCloudNamed} />
          {:else}
            <div class="flex items-center gap-2.5">
              <Button
                type="button"
                variant="outline"
                disabled={soundCloudUrl.trim() === '' || $lookSoundCloud.isPending}
                onclick={() => $lookSoundCloud.mutate(soundCloudUrl.trim())}
              >
                Look up
              </Button>
              {#if unresolvedSoundCloudAddress}
                <span class="text-meta text-ink-3">Look up the link first</span>
              {/if}
            </div>
          {/if}
        </div>
      {/if}

      {#if tooLarge}
        {@render Problem(
          `Max ${formatBytes(maxBytes ?? 0)} per upload. Send the release in parts.`
        )}
      {/if}

      <!-- The send failed at this form, so it is reported here rather than at
           the top of the page. -->
      {#if $send.isError}<ErrorNote error={$send.error} />{/if}

      {#if $send.isPending}
        <div class="flex items-center gap-3">
          <div
            role="progressbar"
            aria-label="Upload progress"
            aria-valuenow={Math.round(progress * 100)}
            aria-valuemin="0"
            aria-valuemax="100"
            class="h-0.5 flex-1 bg-rule-strong"
          >
            <div
              class="h-full origin-left bg-ink transition-transform"
              style="transform: scaleX({progress})"
            ></div>
          </div>
          <!-- The bar is the glance; the figure is the answer. -->
          <p class="mono text-micro text-ink-3">
            {Math.round(progress * 100)}% of {formatBytes(chosenBytes)}
          </p>
        </div>
      {/if}

      <!-- A limit nothing here enforces: two releases in one send are only
           refused later, at the import. -->
      <span class="text-meta leading-relaxed text-ink-3">one release at a time</span>
    </section>
  {/if}

  <section class="flex flex-col gap-2.5">
    <div class="flex items-center gap-2.5">
      <span class="label">Recent uploads</span>
      <span class="h-px flex-1 bg-rule"></span>
    </div>

    <Settle pending={$uploads.isPending}>
      {#snippet placeholder()}
        <div class="flex flex-col overflow-hidden" style="max-height: calc(100dvh - 18rem)">
          {#each Array(6) as _, placeholderIndex (placeholderIndex)}
            <div data-placeholder-row class="flex h-9 items-center gap-3 border-b border-rule px-3">
              <Skeleton shape="round" class="size-4 shrink-0" />
              <Skeleton class="h-4 flex-1" />
              <Skeleton class="h-3 w-20" />
            </div>
          {/each}
        </div>
      {/snippet}
      {#if $uploads.isError}
        <!-- The list is what failed, so the note stands where it would be. A
             failed poll keeps the rows already read under it. Only a read, so
             the note asks again by itself. -->
        <ErrorNote error={$uploads.error} retry={() => $uploads.refetch()} class="mb-2" />
      {/if}
        <div
          bind:this={rows}
          use:glide={{ key: `${cursor.at}:${items.length}:${settled}` }}
          class="relative flex flex-col"
        >
          {#each items as upload, index (upload.id)}
            {@const state = label(upload)}
            {@const link = upload.status === 'imported' ? libraryLink(upload) : null}
            <article
              class="flex flex-col {arrived.has(upload.id) ? 'rise' : ''}"
              animate:slideInto
              out:strike={{ active: $discard.variables === upload.id }}
            >
              <UploadRow
                {upload}
                role={state.role}
                state={state.text}
                detail={detail(upload)}
                libraryHref={link}
                here={index === cursor.at}
                discarding={$discard.isPending}
                ondiscard={() => $discard.mutate(upload.id)}
              />

              <!-- A discard is an action on this row, so its failure is reported
                   against the row it was pressed on and no other. -->
              {#if $discard.isError && $discard.variables === upload.id}
                <div class="px-3 pb-2"><ErrorNote error={$discard.error} /></div>
              {/if}

              {#if upload.fileStates?.length}
                <div class="px-3 pb-3">
                  <UploadResolvePass files={upload.fileStates} />
                </div>
              {/if}
            </article>
          {/each}
        </div>
      {#if items.length === 0 && !$uploads.isError}
        <!-- The list stays mounted so its last row can strike and fold away;
             the panel waits for it. -->
        <div class="rise" style={hadRows ? `animation-delay: ${leaveMs()}ms` : ''}>
          <EmptyPanel role="idle" heading="Nothing uploaded yet" />
        </div>
      {/if}
    </Settle>
  </section>
</div>

{#snippet Problem(message: string)}
  <div class="flex items-start gap-2 bg-panel px-2.5 py-2">
    <StateMark role="fail" />
    <span class="min-w-0 text-meta leading-relaxed text-fail">{message}</span>
  </div>
{/snippet}

<script lang="ts">
  import { createMutation, createQuery, keepPreviousData, useQueryClient } from '@tanstack/svelte-query';
  import { Check, LoaderCircle, Trash2, Upload as UploadIcon } from '@lucide/svelte';
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
  import Chip from '$lib/components/Chip.svelte';
  import EmptyPanel from '$lib/components/EmptyPanel.svelte';
  import ErrorNote from '$lib/components/ErrorNote.svelte';
  import Settle from '$lib/components/Settle.svelte';
  import SoundCloudNamingFieldsInput from '$lib/components/SoundCloudNamingFields.svelte';
  import UploadResolvePass from '$lib/components/UploadResolvePass.svelte';
  import StateMark from '$lib/components/StateMark.svelte';

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
        return { role: 'busy', text: 'Waiting to be read' };
      case 'failed':
        return { role: 'fail', text: 'Refused' };
      default:
        return { role: 'idle', text: 'Staged' };
    }
  }

  function detail(upload: Upload) {
    if (upload.detail) return upload.detail;
    if (upload.importPath) return upload.importPath;
    if (upload.status === 'staged') return 'waiting for the import to be picked up';
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
  const chips: Record<Role, string> = {
    ok: 'bg-ok/14 text-ok',
    idle: 'bg-idle/14 text-idle',
    decide: 'bg-decide/14 text-decide',
    busy: 'bg-busy/14 text-busy',
    fail: 'bg-fail/14 text-fail'
  };
  const header = $derived.by((): { role: Role; text: string } => {
    if (unconfigured) return { role: 'idle', text: 'Not configured' };
    if ($send.isPending) return { role: 'busy', text: 'Uploading' };
    if (busy) return { role: 'busy', text: 'Importing' };
    return { role: 'ok', text: 'Ready' };
  });
</script>

<div class="layout-width flex flex-col gap-4 px-6 py-4">
  <div class="flex flex-wrap items-center gap-x-3 gap-y-2">
    <span
      class="numeric truncate rounded-row border border-line-thin bg-surface-regular px-2 py-[3px] text-meta font-medium text-ink-2"
      title={$uploads.data?.stagingPath || 'Staging path'}
    >
      {$uploads.data?.stagingPath || '—'}
    </span>

    <Chip role={header.role}>{header.text}</Chip>

    <!-- The limit is what somebody needs to know before choosing files; what an
         upload is for is not. -->
    <span class="hidden text-meta font-medium text-ink-4 lg:block">
      up to {maxBytes && maxBytes > 0 ? formatBytes(maxBytes) : '—'} at a time
    </span>

    <div class="ml-auto flex items-center gap-2.5">
      <Button
        variant="outline"
        disabled={unconfigured || $send.isPending}
        onclick={() => picker?.click()}
      >
        Choose files
      </Button>
      <Button
        disabled={chosen.length === 0 ||
          tooLarge ||
          unresolvedSoundCloudAddress ||
          $send.isPending}
        onclick={() => $send.mutate(chosen)}
      >
        {#if $send.isPending}
          <LoaderCircle size={13} class="animate-spin" /> Uploading
        {:else}
          <UploadIcon size={13} strokeWidth={2.3} /> Upload
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
       single chosen file. Any other selection — none chosen, or several —
       falls back to naming from the row in Library, the way every upload has
       always been named. -->
  {#if !unconfigured && chosen.length !== 1}
    <p class="text-meta text-ink-3">
      A SoundCloud edit lands unmatched. Open it in Library from its row and choose "This is a
      SoundCloud track".
    </p>
  {/if}

  {#if unconfigured}
    <div class="flex flex-col gap-2 rounded-panel border border-line-thin p-4">
      <span class="text-body font-semibold text-ink">This installation cannot take uploads</span>
      <span class="text-meta leading-relaxed text-ink-2">
        Set <span class="numeric">SCHALL_UPLOAD_STAGING_PATH</span> to a folder outside every music
        folder, and <span class="numeric">SCHALL_IMPORT_LIBRARY_PATH</span> to the managed library
        uploads are filed into.
      </span>
    </div>
  {:else if $uploads.isError}
    <!-- Only a read, so the note asks again by itself. -->
    <ErrorNote error={$uploads.error} retry={() => $uploads.refetch()} />
  {/if}

  {#if chosen.length > 0}
    <section class="flex flex-col gap-3 rounded-panel border border-line-regular p-4">
      <div class="flex flex-wrap items-center gap-2">
        <span class="label">Ready to send</span>
        <span class="numeric whitespace-nowrap text-meta text-ink-4">
          {chosen.length}
          {chosen.length === 1 ? 'file' : 'files'} · {formatBytes(chosenBytes)}
        </span>
        <span class="h-px flex-1 bg-line-thin"></span>
      </div>

      <!-- No border of its own: the panel around it is already a surface, and a
           box inside a box is one border too many. A hairline separates them. -->
      <div class="flex flex-col">
        {#each chosen as file (file.name)}
          <div class="flex items-center gap-3 border-b border-line-thin py-2 last:border-b-0">
            <span class="numeric min-w-0 flex-1 truncate text-body text-ink" title={file.name}>
              {file.name}
            </span>
            <span class="numeric whitespace-nowrap text-meta text-ink-4">{formatBytes(file.size)}</span>
          </div>
        {/each}
      </div>

      {#if chosen.length === 1}
        <div class="flex flex-col gap-3 border-t border-line-thin pt-3">
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
                <span class="text-meta text-ink-3">Look up the address before uploading</span>
              {/if}
            </div>
          {/if}
        </div>
      {/if}

      {#if tooLarge}
        {@render Problem(
          `One upload may carry up to ${formatBytes(maxBytes ?? 0)}. Send the release in more than one go.`
        )}
      {/if}

      <!-- The send failed at this form, so it is reported here rather than at
           the top of the page. -->
      {#if $send.isError}<ErrorNote error={$send.error} />{/if}

      {#if $send.isPending}
        <div>
          <div
            role="progressbar"
            aria-label="Upload progress"
            aria-valuenow={Math.round(progress * 100)}
            aria-valuemin="0"
            aria-valuemax="100"
            class="h-1.5 overflow-hidden rounded-full bg-surface-thick"
          >
            <div
              class="h-full rounded-full bg-busy transition-[width]"
              style="width: {Math.round(progress * 100)}%"
            ></div>
          </div>
          <!-- The bar is the glance; the figure is the answer. -->
          <p class="numeric mt-1 text-meta text-ink-4">
            {Math.round(progress * 100)}% of {formatBytes(chosenBytes)}
          </p>
        </div>
      {/if}

      <!-- A limit nothing here enforces: two releases in one send are only
           refused later, at the import. "No folders" went — the picker cannot
           take one, so saying so restated a control the user already has. -->
      <span class="text-meta leading-relaxed text-ink-3">one release at a time</span>
    </section>
  {/if}

  <section class="flex flex-col gap-2.5">
    <div class="flex items-center gap-2.5">
      <span class="label">Recent uploads</span>
      <span class="h-px flex-1 bg-line-thin"></span>
    </div>

    <Settle pending={$uploads.isPending}>
      {#snippet placeholder()}
        <div class="flex flex-col overflow-hidden" style="max-height: calc(100dvh - 18rem)">
        {#each Array(40) as _, placeholderIndex (placeholderIndex)}
          <div class="border-b border-line-thin px-3 py-2 last:border-b-0">
            <div class="h-5 animate-pulse rounded-row bg-surface-regular"></div>
            <div class="mt-1 h-4 w-2/3 animate-pulse rounded-row bg-surface-regular"></div>
            <div class="mt-1 h-4 w-1/3 animate-pulse rounded-row bg-surface-regular"></div>
            <div class="mt-2 h-4 w-1/4 animate-pulse rounded-row bg-surface-regular"></div>
          </div>
        {/each}
        </div>
      {/snippet}
      {#if items.length}
        {#each items as upload (upload.id)}
        {@const state = label(upload)}
        {@const link = upload.status === 'imported' ? libraryLink(upload) : null}
        <div class="border-b border-line-thin last:border-b-0">
          <div
              class="flex flex-wrap items-center gap-x-3 gap-y-2 rounded-row px-3 py-2 transition hover:bg-surface-thick md:grid md:grid-cols-[1rem_minmax(0,1fr)_84px_auto_auto] md:gap-y-0"
          >
            <!-- Four states and four glyphs. A decide graded as a dash would be
                 a violet mark saying nothing, which is worse than no mark. -->
            <span class="grid size-4 shrink-0 place-items-center rounded-row {chips[state.role]}">
              {#if state.role === 'ok'}
                <Check size={10} strokeWidth={3.2} />
              {:else}
                <span class="numeric text-micro font-bold">
                  {state.role === 'decide'
                    ? '?'
                    : state.role === 'fail'
                      ? '!'
                      : state.role === 'busy'
                        ? '·'
                        : '–'}
                </span>
              {/if}
            </span>

            <span class="min-w-0 flex-1">
              <span class="numeric block truncate text-body text-ink">
                {upload.files.length > 0
                  ? upload.files.join(' · ')
                  : `upload ${upload.id.slice(0, 8)}`}
              </span>
              <span class="mt-0.5 block truncate text-meta text-ink-3" title={detail(upload)}>
                {detail(upload)}
              </span>
              <span class="mt-0.5 block min-h-4">
                {#if link}
                  <a
                    href={link}
                    class="inline-block text-meta font-medium text-ink-2 underline decoration-white/24 underline-offset-4 transition hover:text-ink"
                  >
                    Open in Library
                  </a>
                {/if}
              </span>
            </span>

            <span class="numeric hidden whitespace-nowrap text-right text-meta text-ink-4 sm:block">
              {formatBytes(upload.sizeBytes)}
            </span>

            <Chip role={state.role} aria-live="polite" aria-atomic="true">{state.text}</Chip>

            <span class="ml-auto flex shrink-0 items-center md:ml-0 md:justify-end">
              {#if upload.staged}
                <Button
                  variant="ghost"
                  icon
                  size="xs"
                  title="Discard the staged files"
                  aria-label="Discard the staged files"
                  disabled={$discard.isPending || busyStatuses.includes(upload.status)}
                  onclick={() => $discard.mutate(upload.id)}
                >
                  <Trash2 size={13} strokeWidth={2} />
                </Button>
              {/if}
            </span>
          </div>

          <!-- A discard is an action on this row, so its failure is reported
               against the row it was pressed on and no other. -->
          {#if $discard.isError && $discard.variables === upload.id}
            <div class="px-3 pb-2"><ErrorNote error={$discard.error} /></div>
          {/if}

          <div class="min-h-4 px-3 pb-3 {upload.fileStates?.length ? '' : 'invisible'}">
            {#if upload.fileStates?.length}
              <UploadResolvePass files={upload.fileStates} />
            {/if}
          </div>
        </div>
        {/each}
      {:else if !$uploads.isError}
        <EmptyPanel role="idle" heading="Nothing uploaded yet" />
      {/if}
    </Settle>
  </section>
</div>

{#snippet Problem(message: string)}
  <div class="flex items-start gap-2 rounded-row border border-line-thin bg-fail/14 px-2.5 py-2">
    <StateMark role="fail">!</StateMark>
    <span class="min-w-0 text-meta leading-relaxed text-fail">{message}</span>
  </div>
{/snippet}

<script lang="ts">
  import type { Upload } from '$lib/api';
  import { changed } from '$lib/motion.svelte';
  import { UPLOAD_COLUMNS } from '$lib/live-list.svelte';
  import { formatBytes } from '$lib/utils';
  import type { StateRole } from '$lib/states';
  import Button from '$lib/components/Button.svelte';
  import StateMark from '$lib/components/StateMark.svelte';

  // The line an upload is read on (ADR 0042): the state's glyph, the file
  // names, what became of them, and what the cursor row can do. It is its own
  // component so a row can notice its own state turning over and hold the
  // inverted flash on the state cell.

  let {
    upload,
    role,
    state,
    detail,
    libraryHref,
    here,
    discarding,
    ondiscard
  }: {
    upload: Upload;
    role: StateRole;
    state: string;
    detail: string;
    /** Where an imported upload landed in Library, when it did. */
    libraryHref: string | null;
    /** Whether the list's cursor is on this row. */
    here: boolean;
    discarding: boolean;
    ondiscard: () => void;
  } = $props();

  const flashed = changed(() => state);
  const busy = $derived(upload.status === 'queued' || upload.status === 'validating');
</script>

<div
  data-cursor={here}
  class="row flex flex-wrap items-center gap-x-3 gap-y-1 px-3 py-1.5 md:grid md:gap-y-0 {UPLOAD_COLUMNS} {here
    ? 'on-ink'
    : ''}"
>
  <span
    class="flex size-7 items-center justify-center self-center {flashed.now ? 'flash' : ''}"
    title={state}
    onanimationend={(event) => event.animationName === 'schall-echo' && flashed.settle()}
  >
    <StateMark {role} />
    <span class="sr-only" aria-live="polite" aria-atomic="true">{state}</span>
  </span>

  <span class="mono block min-w-0 flex-1 basis-0 truncate text-micro text-ink md:basis-auto">
    {upload.files.length > 0 ? upload.files.join(' · ') : `upload ${upload.id.slice(0, 8)}`}
  </span>

  <span class="block min-w-0 basis-full truncate text-meta text-ink-3 md:hidden lg:block lg:basis-auto" title={detail}>{detail}</span>

  <span class="mono hidden whitespace-nowrap text-right text-micro text-ink-2 sm:block">
    {formatBytes(upload.sizeBytes)}
  </span>

  <span class="ml-auto flex shrink-0 items-center gap-1 md:ml-0 md:justify-end">
    {#if libraryHref}
      <Button
        href={libraryHref}
        variant="outline"
        size="xs"
        key={here ? 'o' : undefined}
        verb="open in library"
      >
        Open in Library
      </Button>
    {/if}
    {#if upload.staged}
      <Button
        variant="ghost"
        size="xs"
        key={here ? 'x' : undefined}
        verb="discard"
        title="Discard files"
        aria-label="Discard files"
        disabled={discarding || busy}
        onclick={ondiscard}
      >
        Discard
      </Button>
    {/if}
  </span>
</div>

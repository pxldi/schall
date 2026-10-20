<script lang="ts">
  import type { DownloadRequest } from '$lib/api';
  import { downloadName, formatBytes } from '$lib/utils';
  import { changed } from '$lib/motion.svelte';
  import Button from '$lib/components/Button.svelte';
  import Icon from '$lib/components/Icon.svelte';
  import StateMark from '$lib/components/StateMark.svelte';
  import {
    DOWNLOAD_COLUMNS,
    decideHref,
    markRole,
    rowHeading,
    rowSubject,
    statusLabel,
    stateRole,
    subLine,
    transferred,
    waiting
  } from './download-rows';

  // The line a transfer is read on (ADR 0042). It is its own component so each
  // row can notice its own change: the state cell holds an inverted flash when
  // the state turns over, and the percentage ticks when it moves. The grid
  // columns come from the list, so the cells line up down the page.
  //
  // The row's keys are registered only while it holds the cursor, so one key
  // never means two rows.

  let {
    item,
    here,
    expanded,
    ontoggle,
    onstart,
    onretry,
    oncancel,
    starting = false,
    retrying = false,
    cancelling = false
  }: {
    item: DownloadRequest;
    /** Whether the list's cursor is on this row. */
    here: boolean;
    expanded: boolean;
    ontoggle: () => void;
    onstart: () => void;
    onretry: () => void;
    oncancel: () => void;
    starting?: boolean;
    retrying?: boolean;
    cancelling?: boolean;
  } = $props();

  const role = $derived(stateRole(item));
  const mark = $derived(markRole(item));
  const decide = $derived(decideHref(item));
  const label = $derived(statusLabel(item));
  const percent = $derived(transferred(item));
  const showProgress = $derived(item.status === 'started' && !waiting(item));
  // The files counter speaks once some of the files have arrived, or once
  // there is more than one to count. One file queued at the peer reads
  // "Queued at peer" and nothing more.
  const counted = $derived(item.progress.completedCount > 0 || item.progress.transferCount > 1);

  const flashed = changed(() => label);
  const ticked = changed(() => percent);
  const stepped = changed(() => item.progress.completedCount);
</script>

<div
  data-cursor={here}
  class="row flex flex-wrap items-center gap-x-3 gap-y-1 px-3 py-1.5 md:grid md:gap-y-0 {DOWNLOAD_COLUMNS} {here
    ? 'on-ink'
    : ''} {item.status === 'cancelled' ? 'opacity-60' : ''}"
>
  <!-- The word rides in the tooltip and in a live region, so a screen reader
       hears the state change that the glyph shows. -->
  <span
    class="flex size-7 items-center justify-center self-center {flashed.now ? 'flash' : ''}"
    title={label}
    onanimationend={(event) => event.animationName === 'schall-echo' && flashed.settle()}
  >
    <StateMark role={mark} />
    <span class="sr-only" aria-live="polite" aria-atomic="true">{label}</span>
  </span>

  {#if item.albumId}
    <a
      href={`/releases/${item.albumId}`}
      class="tap-tall block min-w-0 flex-1 basis-0 truncate text-body font-medium text-ink transition hover:underline md:basis-auto"
    >
      {rowHeading(item)}{#if rowSubject(item)}{' '}<span class="font-normal text-ink-3">· {rowSubject(item)}</span>{/if}
    </a>
  {:else}
    <span class="block min-w-0 flex-1 basis-0 truncate text-body font-medium text-ink md:basis-auto">
      {rowHeading(item)}{#if rowSubject(item)}{' '}<span class="font-normal text-ink-3">· {rowSubject(item)}</span>{/if}
    </span>
  {/if}

  <span class="mono block min-w-0 basis-full truncate text-micro text-ink-3 md:hidden lg:block lg:basis-auto" title={subLine(item)}>
    {subLine(item)}
  </span>

  <span class="flex min-w-0 items-center">
    {#if showProgress}
      <span class="flex w-44 items-center gap-2">
        {#if counted}
          <span
            class="mono text-micro text-ink-3"
            class:tick={stepped.now}
            onanimationend={stepped.settle}
            title="Files transferred"
          >
            {item.progress.completedCount}/{item.progress.transferCount}
          </span>
        {/if}
        <span
          role="progressbar"
          aria-label={`Download progress for ${downloadName(item)}`}
          aria-valuenow={percent}
          aria-valuemin="0"
          aria-valuemax="100"
          title={`${formatBytes(item.progress.transferredBytes)} of ${formatBytes(item.totalSizeBytes)}${item.progress.failedCount ? ` · ${item.progress.failedCount} failed` : ''}`}
          class="block h-0.5 min-w-8 flex-1 bg-rule-strong"
        >
          <span
            class="block h-full origin-left bg-ink transition-transform"
            style={`transform: scaleX(${percent / 100})`}
          ></span>
        </span>
        <span
          class="mono w-9 text-right text-micro text-ink-2"
          class:tick={ticked.now}
          onanimationend={ticked.settle}
        >
          {percent}%
        </span>
      </span>
    {/if}
  </span>

  <span class="mono whitespace-nowrap text-right text-micro text-ink-2">
    {formatBytes(item.totalSizeBytes)}
  </span>

  <span class="ml-auto flex shrink-0 items-center gap-1 md:ml-0 md:justify-end">
    {#if item.startable}
      <Button
        variant="outline"
        size="xs"
        key={here ? 's' : undefined}
        verb="start"
        disabled={starting}
        onclick={onstart}
      >
        <Icon name="play" size={12} /> Start
      </Button>
    {:else if item.retryableCount && item.status !== 'started'}
      <Button
        variant="outline"
        size="xs"
        key={here ? 'r' : undefined}
        verb="retry"
        aria-label={`Retry ${item.retryableCount === 1 ? '1 file' : `${item.retryableCount} files`}`}
        title={`Retry ${item.retryableCount === 1 ? '1 file' : `${item.retryableCount} files`}`}
        disabled={retrying}
        onclick={onretry}
      >
        <Icon name="undo" size={12} /> Retry
      </Button>
    {/if}
    {#if role === 'decide' && decide}
      <Button href={decide} variant="outline" size="xs" key={here ? 'd' : undefined} verb="decide">Decide</Button>
    {/if}
    {#if item.status === 'requested' || item.status === 'started'}
      <Button
        variant="ghost"
        size="xs"
        key={here ? 'c' : undefined}
        verb="cancel"
        title="Cancel download"
        aria-label="Cancel download"
        disabled={cancelling}
        onclick={oncancel}
      >
        Cancel
      </Button>
    {/if}
    <Button
      variant="ghost"
      icon
      size="xs"
      title="Show requested files"
      aria-label="Show requested files"
      aria-expanded={expanded}
      onclick={ontoggle}
    >
      <Icon name="chevron-down" size={14} class={expanded ? 'rotate-180' : ''} />
    </Button>
  </span>
</div>

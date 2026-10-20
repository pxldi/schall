<script lang="ts">
  import { changed } from '$lib/motion.svelte';
  import { WANT_COLUMNS } from '$lib/live-list.svelte';
  import type { StateRole } from '$lib/states';
  import Button from '$lib/components/Button.svelte';
  import Chip from '$lib/components/Chip.svelte';
  import StateMark from '$lib/components/StateMark.svelte';

  // The line a want is read on (ADR 0042): the state's glyph, the song, what
  // has been tried, and the one thing the cursor row can do with it. It is its
  // own component so a row can notice its own state turning over and hold the
  // inverted flash on the state cell.
  //
  // The state's word rides in the tooltip and in a live region. The sentence
  // the loop wrote for a waiting want is in the tooltip only.

  let {
    name,
    role,
    state,
    why,
    detail,
    detailHref,
    timing,
    floorWaived,
    here,
    action
  }: {
    name: string;
    role: StateRole;
    /** The state in words, as the loop says it. */
    state: string;
    /** The reason the want is waiting, when there is one. */
    why: string;
    detail: string;
    /** Set for a want keyed to an address, where the detail line links to it. */
    detailHref?: string;
    timing: string;
    floorWaived: boolean;
    /** Whether the list's cursor is on this row. */
    here: boolean;
    action: {
      /** Shown on the button. */
      label: string;
      /** The button's name, when the label is shorter than what it does. */
      name: string;
      key: string;
      pending: boolean;
      run: () => void;
    };
  } = $props();

  const flashed = changed(() => state);
</script>

<div
  data-cursor={here}
  class="row flex flex-wrap items-center gap-x-3 gap-y-1 px-3 py-1.5 md:grid md:gap-y-0 {WANT_COLUMNS} {here
    ? 'on-ink'
    : ''}"
>
  <span
    class="flex size-7 items-center justify-center self-center {flashed.now ? 'flash' : ''}"
    title={[state, why].filter(Boolean).join(' · ')}
    onanimationend={(event) => event.animationName === 'schall-echo' && flashed.settle()}
  >
    <StateMark {role} />
    <span class="sr-only" aria-live="polite" aria-atomic="true">{state}</span>
  </span>

  <span class="block min-w-0 flex-1 basis-0 truncate text-body font-medium text-ink md:basis-auto" title={name}>
    {name}
  </span>

  <span class="block min-w-0 basis-full truncate text-meta text-ink-3 md:hidden lg:block lg:basis-auto" title={detail}>
    {#if detailHref}
      <a href={detailHref} class="underline-offset-2 hover:text-ink hover:underline">{detail}</a>
    {:else}
      {detail}
    {/if}
  </span>

  <!-- When a want was last looked for is half of what the row is for: without
       it the reader is back to asking whether anything is happening at all. -->
  <span class="mono whitespace-nowrap text-micro text-ink-3">{timing}</span>

  <span class="ml-auto flex shrink-0 items-center gap-1.5 md:ml-0 md:justify-end">
    {#if floorWaived}
      <Chip role="idle" class="shrink-0">Floor waived</Chip>
    {/if}
    <Button
      variant="ghost"
      size="xs"
      key={here ? action.key : undefined}
      verb={action.label.toLocaleLowerCase()}
      title={action.name}
      aria-label={action.name}
      disabled={action.pending}
      onclick={action.run}
    >
      {action.label}
    </Button>
  </span>
</div>

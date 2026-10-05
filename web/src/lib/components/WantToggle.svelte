<script lang="ts">
  import { cn } from '$lib/utils';

  // The + on a track. Pressed, the track is wanted; pressed again, it is not.
  //
  // It answers the press at once and does not wait for the server: the plus
  // turns into a tick the moment it is pressed, and `onwant` or `onunwant` runs
  // underneath. If that rejects, the toggle goes back to what the server last
  // said and shakes once; the caller shows the error, because the caller knows
  // which words go with it. The server's answer arriving later moves nothing,
  // since it says what the toggle already shows.
  //
  // Unwanted is a ring with a plus. Wanted is a disc in the want colour with a
  // tick. A pointer resting on a wanted toggle turns the plus back in as a
  // cross, which is what pressing it will do, except straight after the press
  // that wanted it: a cross under the pointer that just pressed + reads as the
  // press having failed.

  type Props = {
    /** What the server last said. */
    wanted: boolean;
    /** Called on a press that wants the track. Return its promise. */
    onwant: () => unknown;
    /** Called on a press that stops wanting it. Return its promise. */
    onunwant: () => unknown;
    /** The track's name, for the accessible name: "Want <title>". */
    title?: string;
    /** The words beside the glyph, unwanted and wanted. Leave out for the
     *  round glyph alone, which is what a row of tracks uses. */
    label?: [string, string];
    size?: 'xs' | 'sm';
    /** Draw the + only while the row is hovered or holds focus. The row says
     *  where it starts with the `want-row` class. A wanted toggle always shows. */
    reveal?: boolean;
    disabled?: boolean;
    class?: string;
  };

  let {
    wanted,
    onwant,
    onunwant,
    title,
    label,
    size = 'xs',
    reveal = false,
    disabled = false,
    class: className
  }: Props = $props();

  // The state the press asked for, until the server agrees with it.
  let asked = $state<boolean | null>(null);
  let inFlight = $state(false);
  let fresh = $state(false);
  let played = $state<'want-pop' | 'want-drop' | 'refuse' | null>(null);
  let bursts = $state(0);

  const shown = $derived(asked ?? wanted);

  $effect(() => {
    if (asked !== null && asked === wanted) asked = null;
  });

  async function press() {
    if (inFlight || disabled) return;
    const next = !shown;
    asked = next;
    fresh = next;
    played = next ? 'want-pop' : 'want-drop';
    if (next) bursts += 1;
    inFlight = true;
    try {
      await (next ? onwant() : onunwant());
    } catch {
      asked = null;
      fresh = false;
      played = 'refuse';
    } finally {
      inFlight = false;
    }
  }

  const sizes = {
    xs: { round: 'size-6', pill: 'h-6 gap-0.5 pl-1.5 pr-2.5 text-meta', glyph: 'size-4' },
    sm: { round: 'size-7', pill: 'h-7 gap-0.5 pl-2 pr-3 text-meta', glyph: 'size-5' }
  };
  const name = $derived(title ? `Want ${title}` : 'Want');
</script>

<button
  type="button"
  class={cn(
    'want-toggle tap relative cursor-pointer disabled:cursor-not-allowed disabled:opacity-50',
    label ? sizes[size].pill : sizes[size].round,
    label && 'inline-flex font-medium',
    played,
    className
  )}
  data-wanted={shown ? 'true' : 'false'}
  data-fresh={fresh ? 'true' : 'false'}
  data-reveal={reveal ? 'true' : 'false'}
  aria-pressed={shown}
  aria-label={label ? undefined : name}
  aria-busy={inFlight}
  title={shown ? 'Not wanted' : name}
  {disabled}
  onclick={press}
  onpointerleave={() => (fresh = false)}
  onanimationend={(event) => {
    if (event.target === event.currentTarget) played = null;
  }}
>
  <span class={cn('grid place-items-center', label ? sizes[size].glyph : 'size-full')}>
    <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2.5" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true">
      <g class="want-plus"><path d="M12 6v12M6 12h12" /></g>
      <path class="want-tick" pathLength="1" d="M6.5 12.5l3.75 3.75L17.5 8.5" />
    </svg>
  </span>
  {#if label}
    <!-- Both words share one cell, so the button is as wide as the longer one
         and nothing beside it moves when the word changes. -->
    <span class="grid">
      <span class="[grid-area:1/1]" class:invisible={shown} aria-hidden={shown}>{label[0]}</span>
      <span class="[grid-area:1/1]" class:invisible={!shown} aria-hidden={!shown}>{label[1]}</span>
    </span>
  {/if}
  {#key bursts}
    {#if bursts > 0}
      <span class="want-burst" aria-hidden="true"></span>
    {/if}
  {/key}
</button>

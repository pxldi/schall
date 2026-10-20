<script lang="ts">
  import type { Snippet } from 'svelte';
  import { cn } from '$lib/utils';
  import StateMark from '$lib/components/StateMark.svelte';

  // A status badge is the tinted band that reports a state in words at the
  // thing the state belongs to: the failure under a toolbar button that
  // refused, the message where a list would have been, the reason a provider
  // cannot be reached. It is the state mark of design/chip.html with the label
  // written out in full beside it, on the role's tint at 14%.
  //
  // Nineteen call sites had written the band out by hand, and all of them
  // report a failure. The role is a prop because the tint and the mark are one
  // decision, and a band that said something other than "this failed" would
  // otherwise be written from scratch again.
  //
  // `ok` is missing from the roles on purpose: its mark carries a drawn tick
  // rather than a character, no call site asks for it, and Schall confirms
  // success by the interface changing rather than by a message about it
  // (design/error.html).
  type Role = 'idle' | 'decide' | 'busy' | 'fail';

  let {
    role = 'fail',
    // A border is dropped where the band sits inside a card that already has
    // one. design/section.html's rule is never two borders deep: the tint is
    // what makes this a distinct surface, and a second hairline inside the
    // first says nothing the tint has not already said.
    border = true,
    // Let a long message fall below the mark instead of squeezing beside it.
    // The settings page wraps because its cards are narrow; a band that spans a
    // page does not need it.
    wrap = false,
    class: className,
    children
  }: {
    role?: Role;
    border?: boolean;
    wrap?: boolean;
    class?: string;
    children?: Snippet;
  } = $props();

  // Written out per role because Tailwind reads class names out of the source
  // as literal text and never sees a name assembled at runtime.
  const tints: Record<Role, string> = {
    idle: 'bg-idle/14',
    decide: 'bg-decide/14',
    busy: 'bg-busy/14',
    fail: 'bg-fail/14'
  };

  // The four bold mono characters design/chip.html enumerates.
  const glyphs: Record<Role, string> = {
    idle: '–',
    decide: '?',
    busy: '·',
    fail: '!'
  };
</script>

<!-- `role="status"` makes the band a live region: a screen reader reads it out
     when it arrives, without taking the focus away from the control the reader
     is on. Every call site draws the band only once there is something to
     report — a save the server refused, a list that could not be loaded — so
     the arrival is the news. Without it a person pressing Test connection heard
     the spinner stop and nothing else: the reason was on screen and was never
     said.

     A band that is already there when the page is drawn is not announced. That
     is what a live region does, and it is what makes this safe on the settings
     page, where a connection that failed earlier is on screen from the first
     frame and is not news. -->
<div
  role="status"
  class={cn(
    // A band arrives because something has just gone wrong, or because a list
    // that was expected is not there. It is drawn only when there is something
    // to say, so it is always an arrival and never a redraw, and it takes the
    // arriving step.
    'rise flex items-center gap-2.5 rounded-panel px-3 py-2.5',
    tints[role],
    border && 'border border-line-thin',
    wrap && 'flex-wrap',
    className
  )}
>
  <StateMark {role}>{glyphs[role]}</StateMark>
  {@render children?.()}
</div>

<script lang="ts">
  import { ArrowLeft } from '@lucide/svelte';
  import { previousScreen } from '$lib/navigation.svelte';
  import { cn } from '$lib/utils';

  let {
    // Where to go when there is nothing to go back to — a link somebody was
    // sent, a reload, a tab opened straight onto this page. The section this
    // page belongs to, which is what the arrow used to point at unconditionally.
    fallback,
    label,
    size = 17,
    class: className
  }: { fallback: string; label: string; size?: number; class?: string } = $props();

  // Where the reader actually came from, which the application has been keeping
  // since it started. An artist reached from a release should go back to that
  // release, not to the artist list — the arrow was pointing at a section
  // rather than at a screen, so it was wrong every time somebody arrived from
  // anywhere but the obvious place. Reading it rather than listening for it is
  // what lets this work on a page that draws its header only once its query has
  // answered, which is long after the navigation it would have had to hear.
  const href = $derived(previousScreen.href ?? fallback);

  // Left-click pops the history entry rather than pushing the same address on
  // top of it, so the browser's own back button and this arrow stay in step —
  // but only while the entry behind is the screen the arrow names, because
  // popping to anywhere else would make the two disagree. Otherwise the address
  // is followed like any other link. Everything else — middle click, a
  // modifier, a right-click menu — is left to the browser, which is the whole
  // reason this is an anchor and not a button.
  function back(event: MouseEvent) {
    if (!previousScreen.oneStepBack) return;
    if (event.button !== 0 || event.metaKey || event.ctrlKey || event.shiftKey || event.altKey) {
      return;
    }
    event.preventDefault();
    history.back();
  }
</script>

<a
  {href}
  onclick={back}
  aria-label={previousScreen.href ? 'Back' : label}
  class={cn('tap text-ink-4 transition hover:text-ink', className)}
>
  <ArrowLeft {size} />
</a>

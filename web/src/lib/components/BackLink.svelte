<script lang="ts">
  import Button from './Button.svelte';
  import Icon from './Icon.svelte';
  import { previousScreen } from '$lib/navigation.svelte';
  import { screenLabel } from '$lib/screen-name';

  let {
    // Where to go when there is nothing to go back to — a link somebody was
    // sent, a reload, a tab opened straight onto this page. The section this
    // page belongs to, which is what the arrow used to point at unconditionally.
    fallback,
    // The name of that section, shown on the button when it is where the
    // button leads.
    label,
    class: className
  }: { fallback: string; label: string; class?: string } = $props();

  // Where the reader actually came from, which the application has been keeping
  // since it started. An artist reached from a release should go back to that
  // release, not to the artist list — the arrow was pointing at a section
  // rather than at a screen, so it was wrong every time somebody arrived from
  // anywhere but the obvious place. Reading it rather than listening for it is
  // what lets this work on a page that draws its header only once its query has
  // answered, which is long after the navigation it would have had to hear.
  const href = $derived(previousScreen.href ?? fallback);

  // The button says where it goes, so a reader can tell before pressing it
  // whether it leads to the release they came from or to the whole library.
  // An artist or a release is named by its own name, not by its kind.
  const name = $derived(
    previousScreen.href ? screenLabel(previousScreen.href, previousScreen.title) : label
  );

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

<!-- A plain ghost link: a word and an arrow, with no ground of its own. -->
<Button
  {href}
  onclick={back}
  variant="ghost"
  size="sm"
  aria-label={`Back to ${name}`}
  class={className}
>
  <Icon name="arrow-left" size="sm" />
  <span class="max-w-[16rem] truncate">{name}</span>
</Button>

<script lang="ts">
  import { untrack } from 'svelte';
  import { capLabel, echo } from '$lib/keys.svelte';
  import { cn } from '$lib/utils';
  import Icon from './Icon.svelte';

  // A key's name in a box, on a button or in the key strip. It is drawing:
  // the button it sits on is named by its words, so the cap is hidden from
  // assistive technology and the button carries `aria-keyshortcuts` instead.
  //
  // When its key fires it flashes inverted for one state beat, so a press
  // made from the keyboard is seen to land.

  let { key, class: className }: { key: string; class?: string } = $props();

  let echoing = $state(false);
  let seen = untrack(() => echo.at);
  $effect(() => {
    const at = echo.at;
    if (at === seen) return;
    seen = at;
    if (echo.key === key) {
      // Restart the animation when the same key fires twice in a row.
      echoing = false;
      requestAnimationFrame(() => (echoing = true));
    }
  });
</script>

<kbd
  aria-hidden="true"
  class={cn('keycap', className)}
  data-echo={echoing ? '' : undefined}
  onanimationend={() => (echoing = false)}
>
  {#if key === 'enter'}
    <Icon name="enter" size={13} />
  {:else}
    {capLabel(key)}
  {/if}
</kbd>

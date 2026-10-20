<script lang="ts">
  import Button from './Button.svelte';
  import KeyStrip from './KeyStrip.svelte';
  import { useKeys } from '$lib/keys.svelte';

  // A screen with one keyed button, one key of its own and a field, for
  // KeyStrip.svelte.test.ts.
  let {
    onwant,
    onmove,
    disabled = false
  }: { onwant: () => void; onmove: () => void; disabled?: boolean } = $props();

  useKeys(() => [{ keys: ['j', 'k'], caps: ['j', 'k'], verb: 'move', order: 0, run: onmove }]);
</script>

<Button key="w" verb="want" {disabled} onclick={onwant}>Want 2</Button>
<button type="button">Elsewhere</button>
<input aria-label="Search terms" />
<KeyStrip onsheet={() => {}} />

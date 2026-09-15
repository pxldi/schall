<script lang="ts">
  import * as Tooltip from './index';

  // A control with no word on it and a tooltip that supplies the word, which is
  // the only way this component is used: the narrow navigation rail. It exists
  // for `Tooltip.svelte.test.ts`.
</script>

<Tooltip.Provider delayDuration={0}>
  <Tooltip.Root>
    <Tooltip.Trigger>
      {#snippet child({ props })}
        <a {...props} href="/review" aria-label="Review">▣</a>
      {/snippet}
    </Tooltip.Trigger>
    <Tooltip.Content>Review</Tooltip.Content>
  </Tooltip.Root>
</Tooltip.Provider>

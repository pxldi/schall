<script lang="ts">
  import * as Popover from './index';

  // A popover with a trigger and a panel holding one control, so the test has a
  // real one to open, to press Escape at, and to look for outside its trigger's
  // own box. It exists only for `Popover.svelte.test.ts`.
</script>

<Popover.Root>
  <Popover.Trigger>Filter</Popover.Trigger>
  <Popover.Content>
    <input aria-label="Search sources" />
  </Popover.Content>
</Popover.Root>

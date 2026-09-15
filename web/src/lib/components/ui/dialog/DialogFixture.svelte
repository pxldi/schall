<script lang="ts">
  import * as Dialog from './index';

  // A dialog with a trigger, two controls inside it and something focusable
  // outside it, so the test has a real one to open and to try to Tab out of. It
  // exists only for `Dialog.svelte.test.ts` and nothing on any screen imports
  // it.
</script>

<button type="button">Outside</button>

<Dialog.Root>
  <Dialog.Trigger>Follow an artist</Dialog.Trigger>
  <Dialog.Content>
    <Dialog.Header>
      <Dialog.Title>Follow an artist</Dialog.Title>
      <Dialog.Description>New releases are added to the queue.</Dialog.Description>
    </Dialog.Header>
    <Dialog.Body>
      <input aria-label="Artist name" />
    </Dialog.Body>
    <Dialog.Footer>
      <Dialog.Close>Cancel</Dialog.Close>
    </Dialog.Footer>
  </Dialog.Content>
</Dialog.Root>

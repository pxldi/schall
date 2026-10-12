<script lang="ts">
  import * as Dialog from '$lib/components/ui/dialog';
  import Button from './Button.svelte';

  // The question before a step that is hard to take back, asked in the page's
  // own panel. The browser's confirm() blocked the whole tab and looked like
  // nothing else in Schall.
  let {
    open = $bindable(false),
    title,
    description,
    action,
    danger = false,
    onconfirm
  }: {
    open?: boolean;
    title: string;
    description: string;
    /** The label of the button that does it: one to three words, verb first. */
    action: string;
    danger?: boolean;
    onconfirm: () => void;
  } = $props();

  function confirm() {
    open = false;
    onconfirm();
  }
</script>

<Dialog.Root bind:open>
  <Dialog.Content>
    <Dialog.Header>
      <Dialog.Title>{title}</Dialog.Title>
    </Dialog.Header>
    <Dialog.Body>
      <Dialog.Description>{description}</Dialog.Description>
    </Dialog.Body>
    <Dialog.Footer>
      <Dialog.Close>
        {#snippet child({ props })}
          <Button type="button" variant="ghost" size="sm" {...props}>Cancel</Button>
        {/snippet}
      </Dialog.Close>
      <Button type="button" size="sm" variant={danger ? 'danger' : 'primary'} onclick={confirm}>
        {action}
      </Button>
    </Dialog.Footer>
  </Dialog.Content>
</Dialog.Root>

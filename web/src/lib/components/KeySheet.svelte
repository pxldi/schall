<script lang="ts">
  import * as Dialog from '$lib/components/ui/dialog';
  import { sheetBindings, type KeyBinding } from '$lib/keys.svelte';
  import Keycap from './Keycap.svelte';

  // Every key the open screen answers, then the ones that work everywhere,
  // from the same registry the keys run from. `?` opens it.

  let { open = $bindable(false) }: { open?: boolean } = $props();

  const lists = $derived(open ? sheetBindings() : { screen: [], everywhere: [] });
</script>

{#snippet list(bindings: KeyBinding[])}
  <dl class="grid grid-cols-[minmax(6rem,auto)_1fr] gap-x-4">
    {#each bindings as binding, index (index)}
      <dt class="flex h-8 items-center gap-0.5 border-b border-rule">
        {#each binding.caps ?? [binding.keys[0]] as cap (cap)}
          <Keycap key={cap} />
        {/each}
      </dt>
      <dd class="flex h-8 items-center border-b border-rule text-body text-ink-2">{binding.verb}</dd>
    {/each}
  </dl>
{/snippet}

<Dialog.Root bind:open>
  <Dialog.Content>
    <Dialog.Header>
      <Dialog.Title>Keys</Dialog.Title>
    </Dialog.Header>
    <Dialog.Body class="flex flex-col gap-5">
      {#if lists.screen.length}
        <section>
          <h3 class="label mb-1">This screen</h3>
          {@render list(lists.screen)}
        </section>
      {/if}
      <section>
        <h3 class="label mb-1">Everywhere</h3>
        {@render list(lists.everywhere)}
      </section>
    </Dialog.Body>
  </Dialog.Content>
</Dialog.Root>

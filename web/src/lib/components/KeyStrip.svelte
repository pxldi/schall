<script lang="ts">
  import { focus, stripBindings } from '$lib/keys.svelte';
  import Keycap from './Keycap.svelte';

  // The strip along the foot of every screen, naming the keys that screen
  // answers (ADR 0042). It reads the registry the keys themselves run from,
  // so it names exactly what a press would do. While a field has focus the
  // letters type into it, and the strip says so instead.

  let { onsheet }: { onsheet: () => void } = $props();

  const bindings = $derived(stripBindings());
</script>

<footer
  class="fixed inset-x-0 bottom-0 z-30 flex h-8 items-center gap-4 overflow-hidden border-t border-rule bg-ground px-4 text-meta text-ink-3"
  style="view-transition-name: key-strip"
  aria-label="Keys on this screen"
>
  {#if focus.typing}
    <span class="flex items-center gap-1.5 whitespace-nowrap">
      <Keycap key="escape" />
      <span>leave the field</span>
    </span>
  {:else}
    <ul class="flex min-w-0 items-center gap-4 overflow-hidden">
      {#each bindings as binding, index (index)}
        <li
          class="flex items-center gap-1.5 whitespace-nowrap transition {binding.enabled && !binding.enabled()
            ? 'opacity-50'
            : ''}"
        >
          <span class="flex items-center gap-0.5">
            {#each binding.caps ?? [binding.keys[0]] as cap (cap)}
              <Keycap key={cap} />
            {/each}
          </span>
          <span>{binding.verb}</span>
        </li>
      {/each}
    </ul>
  {/if}
  <button
    type="button"
    class="ml-auto flex shrink-0 items-center gap-1.5 whitespace-nowrap text-ink-3 transition hover:text-ink"
    onclick={onsheet}
  >
    <Keycap key="?" />
    <span>all keys</span>
  </button>
</footer>

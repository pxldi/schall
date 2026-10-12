<script lang="ts">
  import Icon from './Icon.svelte';
  import type { IconName } from '$lib/icons';
  import type { EntryRole } from '$lib/playlists';

  // A row's state as one Misprint icon, where a word in a box used to be. A
  // column of "wanted" tags sixty rows tall was more text than the rows it
  // labelled. The word stays as the tooltip and as the accessible name, so
  // nothing is lost to a pointer or a screen reader.

  let { icon, label, role = 'idle' }: { icon: IconName; label: string; role?: EntryRole } =
    $props();

  // Written out per role, because Tailwind reads class names from the source.
  const tones: Record<EntryRole, string> = {
    ok: 'text-ok',
    idle: 'text-ink-3',
    decide: 'text-decide',
    busy: 'text-busy',
    fail: 'text-fail'
  };
</script>

<span class="inline-flex [--plate:currentColor] {tones[role]}" title={label} data-state={label}>
  <Icon name={icon} size="md" aria-label={label} />
</span>

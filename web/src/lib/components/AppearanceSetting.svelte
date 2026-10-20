<script lang="ts">
  import { appearance, chooseAppearance, type Appearance } from '$lib/theme.svelte';
  import Segmented from './Segmented.svelte';

  // Light or dark, or whatever the system says. Kept in this browser only:
  // two people on one Schall can each read it their own way.
  const options: { value: Appearance; name: string }[] = [
    { value: 'system', name: 'System' },
    { value: 'light', name: 'Light' },
    { value: 'dark', name: 'Dark' }
  ];
</script>

<div class="flex flex-wrap items-center gap-x-4 gap-y-2">
  <span class="text-body text-ink-2">Appearance</span>
  <Segmented
    {options}
    value={appearance.choice}
    onchange={(value) => chooseAppearance(value as Appearance)}
    label="Appearance"
  />
</div>

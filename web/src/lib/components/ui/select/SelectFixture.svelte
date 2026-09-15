<script lang="ts">
  import * as Select from './index';

  // A picker with two groups of choices, so the test has a real list to open
  // with a key and walk with the arrow keys. It exists only for
  // `Select.svelte.test.ts`.

  const formats = [
    { value: 'flac', label: 'FLAC', group: 'Lossless' },
    { value: 'alac', label: 'ALAC', group: 'Lossless' },
    { value: 'mp3', label: 'MP3', group: 'Lossy' },
    { value: 'opus', label: 'Opus', group: 'Lossy' }
  ];

  const groups = ['Lossless', 'Lossy'];

  let value = $state('');

  const chosen = $derived(formats.find((entry) => entry.value === value)?.label ?? 'Choose one');
</script>

<Select.Root type="single" bind:value items={formats}>
  <Select.Trigger aria-label="Format">{chosen}</Select.Trigger>
  <Select.Content>
    {#each groups as group (group)}
      <Select.Group>
        <Select.GroupHeading>{group}</Select.GroupHeading>
        {#each formats.filter((entry) => entry.group === group) as entry (entry.value)}
          <Select.Item value={entry.value} label={entry.label}>{entry.label}</Select.Item>
        {/each}
      </Select.Group>
    {/each}
  </Select.Content>
</Select.Root>

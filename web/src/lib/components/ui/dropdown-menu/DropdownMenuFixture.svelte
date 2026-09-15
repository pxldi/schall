<script lang="ts">
  import * as DropdownMenu from './index';

  // A menu with three ordinary items, a rule, a checkbox item and a nested
  // list, so the test has every part of one to walk with the arrow keys. It
  // exists only for `DropdownMenu.svelte.test.ts`.

  let onlyMatched = $state(false);
</script>

<DropdownMenu.Root>
  <DropdownMenu.Trigger>More</DropdownMenu.Trigger>
  <DropdownMenu.Content>
    <DropdownMenu.Group>
      <DropdownMenu.GroupHeading>Not interested in</DropdownMenu.GroupHeading>
      <DropdownMenu.Item>This recording</DropdownMenu.Item>
      <DropdownMenu.Item>Anything on this release</DropdownMenu.Item>
      <DropdownMenu.Item>Anything by this artist</DropdownMenu.Item>
    </DropdownMenu.Group>
    <DropdownMenu.Separator />
    <DropdownMenu.CheckboxItem bind:checked={onlyMatched}>
      Only matched files
    </DropdownMenu.CheckboxItem>
    <DropdownMenu.Sub>
      <DropdownMenu.SubTrigger>Copy</DropdownMenu.SubTrigger>
      <DropdownMenu.SubContent>
        <DropdownMenu.Item>Recording id</DropdownMenu.Item>
      </DropdownMenu.SubContent>
    </DropdownMenu.Sub>
  </DropdownMenu.Content>
</DropdownMenu.Root>

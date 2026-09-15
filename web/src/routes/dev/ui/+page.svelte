<script lang="ts">
  import { Ellipsis, Filter, Plus } from '@lucide/svelte';
  import { Badge } from '$lib/components/ui/badge';
  import { Button } from '$lib/components/ui/button';
  import { Checkbox } from '$lib/components/ui/checkbox';
  import * as Dialog from '$lib/components/ui/dialog';
  import * as DropdownMenu from '$lib/components/ui/dropdown-menu';
  import { Input } from '$lib/components/ui/input';
  import * as Popover from '$lib/components/ui/popover';
  import * as Tooltip from '$lib/components/ui/tooltip';
  import * as Select from '$lib/components/ui/select';
  import * as Table from '$lib/components/ui/table';
  import Segmented from '$lib/components/Segmented.svelte';
  import PillSelect from '$lib/components/PillSelect.svelte';
  import OwnedBar from '$lib/components/OwnedBar.svelte';
  import StateTag from '$lib/components/StateTag.svelte';

  // Every component in `lib/components/ui/`, in every variant and state, on one
  // page. Nothing on a real screen uses these yet, so this is the only place
  // they can be looked at.
  //
  // The page is behind a development-only load, which is why it may say things
  // in the file names and headings that would be wrong on a screen a person
  // uses. It is read by whoever is building the components.

  const variants = ['primary', 'quiet', 'danger', 'ghost'] as const;
  const sizes = ['md', 'sm', 'xs'] as const;
  const roles = ['ok', 'idle', 'decide', 'busy', 'fail', 'done'] as const;

  let checked = $state(true);
  let unchecked = $state(false);
  let partly = $state(true);
  let format = $state('flac');
  let dialogOpen = $state(false);
  let onlyMatched = $state(true);
  let segmentedValue = $state('owned');
  let scope = $state('library');

  const formats = [
    { value: 'flac', label: 'FLAC', group: 'Lossless' },
    { value: 'alac', label: 'ALAC', group: 'Lossless' },
    { value: 'wav', label: 'WAV', group: 'Lossless' },
    { value: 'mp3', label: 'MP3', group: 'Lossy' },
    { value: 'aac', label: 'AAC', group: 'Lossy' },
    { value: 'ogg', label: 'Ogg Vorbis', group: 'Lossy' },
    { value: 'opus', label: 'Opus', group: 'Lossy' }
  ];

  const groups = ['Lossless', 'Lossy'];

  const rows = [
    { track: 1, title: 'Weightless', artist: 'Marconi Union', length: '8:10', role: 'ok' as const },
    { track: 2, title: 'Sleepless', artist: 'Marconi Union', length: '6:42', role: 'decide' as const },
    { track: 3, title: 'Alpha Wave', artist: 'Marconi Union', length: '5:19', role: 'fail' as const }
  ];

  const chosen = $derived(formats.find((entry) => entry.value === format)?.label ?? 'Choose one');
</script>

<div class="mx-auto flex max-w-4xl flex-col gap-10 p-6">
  <header class="flex flex-col gap-1">
    <h1 class="font-display text-dense-display font-bold text-ink">Component gallery</h1>
    <p class="text-dense-body text-ink-3">
      Everything in <code class="numeric">lib/components/ui/</code>. Development only.
    </p>
  </header>

  <section class="flex flex-col gap-3">
    <h2 class="label">Button</h2>
    {#each sizes as size (size)}
      <div class="flex flex-wrap items-center gap-2">
        {#each variants as variant (variant)}
          <Button {variant} {size}>{variant}</Button>
        {/each}
        <Button {size} variant="primary" disabled>disabled</Button>
        <Button {size} variant="quiet" icon title="More" aria-label="More">
          <Ellipsis size={14} aria-hidden="true" />
        </Button>
        <Button {size} variant="ghost" href="/library">a link</Button>
      </div>
    {/each}
  </section>

  <section class="flex flex-col gap-3">
    <h2 class="label">Input</h2>
    <div class="grid max-w-md gap-2">
      <Input placeholder="Search the library" />
      <Input value="Marconi Union — Weightless" />
      <Input placeholder="Waiting on a lookup" trailing />
      <Input placeholder="Refused" disabled />
    </div>
  </section>

  <section class="flex flex-col gap-3">
    <h2 class="label">Checkbox</h2>
    <div class="flex flex-col gap-2 text-dense-body text-ink">
      <label class="flex items-center gap-2">
        <Checkbox bind:checked />
        On
      </label>
      <label class="flex items-center gap-2">
        <Checkbox bind:checked={unchecked} />
        Off
      </label>
      <label class="flex items-center gap-2">
        <Checkbox bind:indeterminate={partly} />
        Some of what this stands for
      </label>
      <label class="flex items-center gap-2">
        <Checkbox checked disabled />
        Refused
      </label>
    </div>
  </section>

  <section class="flex flex-col gap-3">
    <h2 class="label">Badge</h2>
    <div class="flex flex-wrap items-center gap-2">
      {#each roles as role (role)}
        <Badge {role}>{role}</Badge>
      {/each}
      <Badge>no role</Badge>
      <Badge role="busy" dot={false}>no dot</Badge>
    </div>
  </section>

  <section class="flex flex-col gap-3">
    <h2 class="label">Segmented (view chips)</h2>
    <Segmented
      options={[
        { value: 'all', name: 'All', count: 412 },
        { value: 'owned', name: 'Owned', count: 291 },
        { value: 'missing', name: 'Missing', count: 51 }
      ]}
      value={segmentedValue}
      onchange={(v) => (segmentedValue = v)}
      label="Filter releases"
    />
  </section>

  <section class="flex flex-col gap-3">
    <h2 class="label">Pill select</h2>
    <PillSelect
      options={[
        { value: 'library', name: 'Library' },
        { value: 'everything', name: 'Everything' }
      ]}
      value={scope}
      onchange={(v) => (scope = v)}
      label="Scope"
    />
  </section>

  <section class="flex flex-col gap-3">
    <h2 class="label">Owned bar</h2>
    <OwnedBar owned={9} total={12} />
  </section>

  <section class="flex flex-col gap-3">
    <h2 class="label">State tag</h2>
    <div class="flex flex-wrap items-center gap-2">
      <StateTag>held twice</StateTag>
      <StateTag tone="attention">needs review</StateTag>
      <StateTag tone="broken">unreadable</StateTag>
      <StateTag tone="warn">no track list</StateTag>
    </div>
  </section>

  <section class="flex flex-col gap-3">
    <h2 class="label">Select</h2>
    <div class="max-w-xs">
      <Select.Root type="single" bind:value={format} items={formats}>
        <Select.Trigger>{chosen}</Select.Trigger>
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
    </div>
  </section>

  <section class="flex flex-col gap-3">
    <h2 class="label">Popover</h2>
    <Popover.Root>
      <Popover.Trigger>
        {#snippet child({ props })}
          <Button variant="quiet" size="sm" {...props}>
            <Filter size={13} aria-hidden="true" />
            Filter
          </Button>
        {/snippet}
      </Popover.Trigger>
      <Popover.Content class="w-64">
        <p class="text-dense-body text-ink">
          A panel anchored to the control that opened it.
        </p>
      </Popover.Content>
    </Popover.Root>
  </section>

  <section class="flex flex-col gap-3">
    <h2 class="label">Tooltip</h2>
    <!-- The word a control has no room to print. Rest a pointer on the button,
         or move the keyboard onto it, and the word appears beside it. -->
    <Tooltip.Provider delayDuration={200}>
      <Tooltip.Root>
        <Tooltip.Trigger>
          {#snippet child({ props })}
            <Button variant="quiet" size="sm" {...props} aria-label="Filter">
              <Filter size={13} aria-hidden="true" />
            </Button>
          {/snippet}
        </Tooltip.Trigger>
        <Tooltip.Content>Filter</Tooltip.Content>
      </Tooltip.Root>
    </Tooltip.Provider>
  </section>

  <section class="flex flex-col gap-3">
    <h2 class="label">Dropdown menu</h2>
    <DropdownMenu.Root>
      <DropdownMenu.Trigger>
        {#snippet child({ props })}
          <Button variant="quiet" size="sm" icon title="More" aria-label="More" {...props}>
            <Ellipsis size={14} aria-hidden="true" />
          </Button>
        {/snippet}
      </DropdownMenu.Trigger>
      <DropdownMenu.Content>
        <DropdownMenu.Group>
          <DropdownMenu.GroupHeading>Not interested in</DropdownMenu.GroupHeading>
          <DropdownMenu.Item>This recording</DropdownMenu.Item>
          <DropdownMenu.Item>Anything on this release</DropdownMenu.Item>
          <DropdownMenu.Item disabled>Anything by this artist</DropdownMenu.Item>
        </DropdownMenu.Group>
        <DropdownMenu.Separator />
        <DropdownMenu.CheckboxItem bind:checked={onlyMatched}>
          Only matched files
        </DropdownMenu.CheckboxItem>
        <DropdownMenu.Separator />
        <DropdownMenu.Sub>
          <DropdownMenu.SubTrigger>Copy</DropdownMenu.SubTrigger>
          <DropdownMenu.SubContent>
            <DropdownMenu.Item>Recording id</DropdownMenu.Item>
            <DropdownMenu.Item>File path</DropdownMenu.Item>
          </DropdownMenu.SubContent>
        </DropdownMenu.Sub>
      </DropdownMenu.Content>
    </DropdownMenu.Root>
  </section>

  <section class="flex flex-col gap-3">
    <h2 class="label">Dialog</h2>
    <Dialog.Root bind:open={dialogOpen}>
      <Dialog.Trigger>
        {#snippet child({ props })}
          <Button size="sm" {...props}>
            <Plus size={13} aria-hidden="true" />
            Follow an artist
          </Button>
        {/snippet}
      </Dialog.Trigger>
      <Dialog.Content>
        <Dialog.Header>
          <Dialog.Title>Follow an artist</Dialog.Title>
          <Dialog.Description>New releases are added to the queue.</Dialog.Description>
        </Dialog.Header>
        <Dialog.Body>
          <Input placeholder="Artist name" />
        </Dialog.Body>
        <Dialog.Footer>
          <Dialog.Close>
            {#snippet child({ props })}
              <Button variant="quiet" size="sm" {...props}>Cancel</Button>
            {/snippet}
          </Dialog.Close>
          <Button size="sm" onclick={() => (dialogOpen = false)}>Follow</Button>
        </Dialog.Footer>
      </Dialog.Content>
    </Dialog.Root>
  </section>

  <section class="flex flex-col gap-3">
    <h2 class="label">Table</h2>
    <div class="max-h-40 overflow-y-auto rounded-panel border border-line-thin">
      <Table.Root>
        <Table.Header>
          <Table.Row>
            <Table.Head numeric>#</Table.Head>
            <Table.Head>Title</Table.Head>
            <Table.Head>Artist</Table.Head>
            <Table.Head numeric>Length</Table.Head>
            <Table.Head>Standing</Table.Head>
          </Table.Row>
        </Table.Header>
        <Table.Body>
          {#each rows as row (row.track)}
            <Table.Row selected={row.track === 2}>
              <Table.Cell numeric>{row.track}</Table.Cell>
              <Table.Cell>{row.title}</Table.Cell>
              <Table.Cell>{row.artist}</Table.Cell>
              <Table.Cell numeric>{row.length}</Table.Cell>
              <Table.Cell><Badge role={row.role}>{row.role}</Badge></Table.Cell>
            </Table.Row>
          {/each}
        </Table.Body>
      </Table.Root>
    </div>
  </section>
</div>

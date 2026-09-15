<script lang="ts">
  import * as Table from './index';

  // A table drawn the way a screen would draw one, so the test has something to
  // read. It exists only for `Table.svelte.test.ts` and nothing on any screen
  // imports it.
</script>

<Table.Root>
  <Table.Header>
    <Table.Row>
      <Table.Head numeric>#</Table.Head>
      <Table.Head>Title</Table.Head>
      <Table.Head>Artist</Table.Head>
    </Table.Row>
  </Table.Header>
  <Table.Body>
    <Table.Row>
      <Table.Cell numeric>1</Table.Cell>
      <Table.Cell>Weightless</Table.Cell>
      <Table.Cell>Marconi Union</Table.Cell>
    </Table.Row>
    <Table.Row selected>
      <Table.Cell numeric>2</Table.Cell>
      <Table.Cell>Sleepless</Table.Cell>
      <Table.Cell>Marconi Union</Table.Cell>
    </Table.Row>
  </Table.Body>
</Table.Root>

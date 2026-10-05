<script lang="ts">
  import { usePagePrint, type PagePrint } from '../page.svelte';

  // A page that asks for a print, for the tests of usePagePrint.
  let { print }: { print: () => PagePrint | null | undefined } = $props();
  usePagePrint(() => print());
</script>

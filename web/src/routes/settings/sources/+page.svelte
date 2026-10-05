<script lang="ts">
  import SettingsSections from '$lib/components/SettingsSections.svelte';
</script>

<!-- Hidden: the mast highlights Settings and the section nav highlights
     Sources, but neither is a heading. The Hero above is the h1. -->
<h2 class="sr-only">Settings · Sources</h2>

<SettingsSections show="sources" />

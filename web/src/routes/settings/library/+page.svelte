<script lang="ts">
  import SettingsSections from '$lib/components/SettingsSections.svelte';
</script>

<!-- Hidden: the mast highlights Settings and the section nav highlights
     Library, but neither is a document heading. -->
<h1 class="sr-only">Settings · Library</h1>

<SettingsSections show="library" />

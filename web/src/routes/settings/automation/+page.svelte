<script lang="ts">
  import SettingsSections from '$lib/components/SettingsSections.svelte';
</script>

<!-- Hidden: the mast highlights Settings and the section nav highlights
     Automation, but neither is a heading. The Hero above is the h1. -->
<h2 class="sr-only">Settings · Automation</h2>

<SettingsSections show="automation" />

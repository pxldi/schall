<script lang="ts">
  import JobsView from '$lib/components/JobsView.svelte';
</script>

<!-- A category of Settings, and deliberately not a nav item of its own. The nav
     is six and says not to re-add a dashboard; a jobs table earning a Jobs
     destination is exactly the pattern the ten-to-six cut corrected. What
     Settings carries instead is a fail badge counting jobs that have spent their
     attempts, because a queue nobody looks at cannot tell you Schall is stuck.

     It is also not a dashboard, not metrics and not the activity feed: no stat
     cards, no totals across the top, no graph. Counters and rates are
     Prometheus's on their own endpoint; the user-facing account of what Schall
     did lives with the things it happened to. This is the operator's view of the
     machinery, and it says `job 48044` out loud. -->

<!-- Hidden: the mast highlights Settings and the section nav highlights
     Jobs, but neither is a document heading. -->
<h1 class="sr-only">Settings · Jobs</h1>

<JobsView />

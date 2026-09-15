<script lang="ts">
  import { createQuery } from '@tanstack/svelte-query';
  import { page } from '$app/state';
  import { api } from '$lib/api';
  import { urlChoice } from '$lib/utils';
  import DuplicateRecordings from '$lib/components/DuplicateRecordings.svelte';
  import LibraryFiles from '$lib/components/LibraryFiles.svelte';
  import ControlRail from '$lib/components/ControlRail.svelte';
  import ReleasesBrowser from '$lib/components/ReleasesBrowser.svelte';
  import Segmented from '$lib/components/Segmented.svelte';

  const views = ['releases', 'files', 'duplicates'] as const;

  const opened = page.url;
  let view = $state<(typeof views)[number]>(urlChoice(opened, 'view', views, 'releases'));

  // The catalogue is never torn down, only hidden. It writes the whole query
  // string — its scope, search, filter, sort and page — and `keepInUrl` replaces
  // rather than merges, so unmounting it would have meant the address bar losing
  // that record the moment the files view wrote its own, and a reader who
  // glanced at their files coming back to an unfiltered catalogue. It is also
  // the view that opens by default, so it is mounted either way.
  //
  // The files view is mounted the first time it is asked for and kept from then
  // on. Its three reads are worth not paying for somebody who never opens it,
  // and worth not paying twice for somebody who switches back and forth.
  let filesOpened = $state(urlChoice(opened, 'view', views, 'releases') === 'files');
  $effect(() => {
    if (view === 'files') filesOpened = true;
  });

  // Bumped only by the Releases empty state's link to Files. Switching tabs
  // through the segmented control never remounts the files view, so a search
  // typed there earlier in the visit stays put — but the files view reads its
  // search off the address bar once, at mount, and the link promises the
  // search it named. Remounting is how that search gets there even when the
  // files view was already open.
  let filesKey = $state(0);

  // The duplicates view is mounted the same way and for the same reason: its
  // two reads belong to somebody who opened it.
  let duplicatesOpened = $state(urlChoice(opened, 'view', views, 'releases') === 'duplicates');
  $effect(() => {
    if (view === 'duplicates') duplicatesOpened = true;
  });

  // The counts beside the three view chips. Each is a cheap read of its own —
  // one row of releases, the library summary, the held-twice total — and each
  // is kept under the query key its own view already reads by, so opening that
  // view never asks twice for what this row already knows.
  const releaseTotal = createQuery({
    queryKey: ['releases', 'library-count'],
    queryFn: () => api.releases({ scope: 'library', limit: 1 })
  });
  const librarySummary = createQuery({ queryKey: ['library'], queryFn: api.library });
  const heldTwiceTotal = createQuery({
    queryKey: ['library-duplicates', 'to-decide-total'],
    queryFn: () => api.duplicateRecordings(false, 1, 0)
  });

  // The library summary counts present files only; a file the last scan lost
  // still belongs on this chip's figure, because it is still a file the reader
  // asked the Files view to show them.
  const filesTotal = $derived(
    $librarySummary.data
      ? $librarySummary.data.fileCount + $librarySummary.data.missingCount
      : undefined
  );
</script>

<svelte:head><title>Library · Schall</title></svelte:head>

<!-- Hidden: the mast's highlight already says this is Library, but that
     highlight is not a document heading. -->
<h1 class="sr-only">Library</h1>

<!-- One page for the music, in the two shapes it comes in. They were two pages
     saying the same thing from either end: the catalogue is what the library is
     meant to hold, and the files are what is actually on the disk. Which one
     answers a question depends on the question, not on which page you opened.

     No visible heading here: the mast highlight already says this is Library,
     and the three chips below say which of it is showing. -->
<ControlRail label="Which part of the library to show" width="layout">
  <Segmented
    options={[
      { value: 'releases', name: 'Releases', count: $releaseTotal.data?.scopeTotal },
      { value: 'files', name: 'Files', count: filesTotal },
      { value: 'duplicates', name: 'Held twice', count: $heldTwiceTotal.data?.total }
    ]}
    value={view}
    onchange={(value) => (view = value as (typeof views)[number])}
    label="Which part of the library to show"
    pending={$releaseTotal.isPending || $librarySummary.isPending || $heldTwiceTotal.isPending}
    variant="tabs"
  />
</ControlRail>

<div class:hidden={view !== 'releases'}>
  <ReleasesBrowser
    {view}
    onviewfiles={() => {
      view = 'files';
      filesKey++;
    }}
  />
</div>

{#if filesOpened}
  <div class:hidden={view !== 'files'}>
    {#key filesKey}
      <LibraryFiles onheldtwice={() => (view = 'duplicates')} />
    {/key}
  </div>
{/if}

{#if duplicatesOpened}
  <div class:hidden={view !== 'duplicates'}>
    <DuplicateRecordings />
  </div>
{/if}

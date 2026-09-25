<script lang="ts">
  // Music the library does not hold, suggested from what the connected
  // ListenBrainz account has listened to.
  //
  // A background sweep asks ListenBrainz for the list and stores it. This view
  // reads that stored list, and the hard rules decide what may be shown: a
  // recording is held back when the library already holds it, when the reader
  // has already answered about acquiring it — asked for it, or stopped looking
  // for it — when a followed artist or a followed label made it, since the
  // follow feed brings those in instead, when the reader dismissed it, and when
  // it has been shown three times without an answer. The rules are applied as
  // the list is read, so following an artist or a label, or wanting a track,
  // takes effect here at once.
  //
  // Two decisions are taken here. "Want it" records a want: the same row a
  // release or a playlist records, which the acquisition loop resolves,
  // searches for, and proves by the audio before anything enters the library.
  // "Not interested" is permanent, is said about the recording, its release or
  // its artist, and is kept in the recommendation store alone: what somebody
  // thinks of a piece of music is not the same fact as whether a downloaded
  // file is the recording it claims to be (ADR 0005).
  //
  // Two sources are read here, one at a time: ListenBrainz, and Schall's own
  // engine, built from the listens already copied (ADR 0039). Their ranks are
  // not comparable, so the switch shows one list or the other. Feedback belongs
  // to the list it was pressed on.
  import {
    createMutation,
    createQuery,
    keepPreviousData,
    queryOptions,
    useQueryClient
  } from '@tanstack/svelte-query';
  import { toStore } from 'svelte/store';
  import { ChevronRight, ListMusic } from '@lucide/svelte';
  import {
    api,
    type AcquisitionTarget,
    type Recommendation,
    type RecommendationFeedbackSignal,
    type RecommendationPageRequest,
    type RecommendationSource,
    type RecommendationSubject
  } from '$lib/api';
  import { relativeTime } from '$lib/utils';
  import Button from '$lib/components/Button.svelte';
  import EmptyPanel from '$lib/components/EmptyPanel.svelte';
  import ErrorNote from '$lib/components/ErrorNote.svelte';
  import Settle from '$lib/components/Settle.svelte';
  import Menu, { type MenuItem } from '$lib/components/Menu.svelte';
  import Pager from '$lib/components/Pager.svelte';
  import Segmented from '$lib/components/Segmented.svelte';

  const pageSize = 25;
  const queryClient = useQueryClient();

  const sources = [
    { value: 'listenbrainz', name: 'ListenBrainz' },
    { value: 'schall', name: 'Schall' }
  ];
  let source = $state<RecommendationSource>('listenbrainz');
  const own = $derived(source === 'schall');
  const sourceName = $derived(own ? 'Schall' : 'ListenBrainz');

  // Which page to ask for. Reading a page can take rows out of the list — three
  // showings with no answer is the fifth rule — so a position in it moves under
  // the reader, and Previous and Next name the rank of a row they were just
  // shown instead. The two ends of the list stay exact and stay positions.
  let asked = $state<RecommendationPageRequest>({ offset: 0 });
  // What the last press was refused with, kept as the thrown error rather than
  // as its text: `ErrorNote` is what turns a failure into words.
  let failure = $state<unknown>(null);
  // What became of the last recording somebody pressed "Want it" on. The row
  // itself leaves the list on the next read, so without this the press would
  // answer by making the music disappear.
  let wanted = $state('');
  let feedbackMessage = $state('');
  let clearFeedbackOpen = $state(false);

  // What was asked for is in the key because each page is its own answer.
  // Keeping the previous one while the next arrives stops the list emptying out
  // under a press.
  const options = $derived(
    queryOptions({
      queryKey: ['recommendations', source, asked],
      queryFn: () => api.recommendations(pageSize, asked, source),
      // Only a page of the same list stands in: the other source's rows under
      // this source's header would send a press to the wrong list.
      placeholderData: (previous, previousQuery) =>
        previousQuery?.queryKey[1] === source ? keepPreviousData(previous) : undefined
    })
  );
  const recommendations = createQuery(toStore(() => options));

  const items = $derived($recommendations.data?.items ?? []);
  const total = $derived($recommendations.data?.total ?? 0);
  // Where the page the reader is looking at starts. The server says so rather
  // than the browser assuming: a window that has run off the end of a list that
  // shrank is put back onto it there, which is what dismissing the last
  // suggestion on the last page does.
  const offset = $derived($recommendations.data?.offset ?? 0);

  // A switch starts the other list at its first page. Its ranks are not this
  // list's, so a rank step would land nowhere.
  function chooseSource(value: string) {
    source = value as RecommendationSource;
    asked = { offset: 0 };
    clearFeedbackOpen = false;
    feedbackMessage = '';
  }

  // The Pager speaks in positions, because first page, last page and the one
  // before this one are what a reader thinks in. Two of its four controls can
  // be answered with a position; the other two are steps from a row that was
  // just on screen, and are sent as that row's rank (issue #329).
  function step(next: number) {
    const first = items[0]?.rank;
    const last = items[items.length - 1]?.rank;
    if (next === 0 || first === undefined || last === undefined) {
      asked = { offset: 0 };
    } else if (next === offset + pageSize) {
      asked = { after: last };
    } else if (next === offset - pageSize) {
      asked = { before: first };
    } else {
      asked = { offset: next };
    }
  }

  const hidden = $derived($recommendations.data?.hidden);
  const snapshot = $derived($recommendations.data?.snapshot);
  const refresh = $derived($recommendations.data?.refresh);

  // What one press came to, in the reader's own terms. Asking for a recording
  // that is already wanted answers with the want that exists, so the answer can
  // be a want in any state — including one somebody stopped pursuing. This
  // press does not take that decision back: a decision is taken back where it
  // was made, never as a side effect of another screen.
  function wantedMessage(target: AcquisitionTarget) {
    const title = target.title || 'That recording';
    if (target.status === 'not_wanted') {
      return `${title} was marked as not wanted earlier, so Schall did not ask for it.
        Open its release under Library and press Want it.`;
    }
    if (target.status === 'acquired') {
      return `${title} is already in your library.`;
    }
    if (target.status === 'awaiting_review') {
      return `${title} is wanted, and it is waiting for a decision from you on Review.`;
    }
    return `${title} is wanted.`;
  }

  // Wanting a suggestion. It records the same want a release or a playlist
  // records, and the second suppression rule then holds the recording back, so
  // the row leaves this list on the next read.
  const want = createMutation({
    mutationFn: (recommendation: Recommendation) => api.wantRecommendation(recommendation),
    onSuccess: async (target) => {
      failure = null;
      wanted = wantedMessage(target);
      await Promise.all([
        queryClient.invalidateQueries({ queryKey: ['recommendations'] }),
        queryClient.invalidateQueries({ queryKey: ['acquisition-targets'] }),
        queryClient.invalidateQueries({ queryKey: ['dashboard'] })
      ]);
    },
    onError: (error: Error) => {
      wanted = '';
      failure = error;
    }
  });

  // Saying no, at the level the reader meant it. The store has held all three
  // since it was designed and the suppression rules read all three, so a
  // dismissal of an artist takes every suggestion crediting them off the list on
  // the next read (ADR 0016).
  //
  // A credit can name several artists. The words say so, and every one of them
  // is dismissed, because a reader who says no to the row in front of them has
  // not been shown which of the names on it is the one Schall would act on.
  const dismiss = createMutation({
    mutationFn: async (choice: { subject: RecommendationSubject; ids: string[] }) => {
      for (const musicBrainzId of choice.ids) {
        await api.dismissRecommendation(choice.subject, musicBrainzId);
      }
    },
    onSuccess: async () => {
      failure = null;
      await queryClient.invalidateQueries({ queryKey: ['recommendations'] });
    },
    onError: (error: Error) => {
      failure = error;
    }
  });

  // The source travels with the press, so a switch while it is in flight
  // cannot file it under the other list.
  const feedback = createMutation({
    mutationFn: (choice: {
      recordingId: string;
      signal: RecommendationFeedbackSignal;
      source: RecommendationSource;
    }) => api.recordRecommendationFeedback(choice.recordingId, choice.signal, choice.source),
    onSuccess: (_result, choice) => {
      failure = null;
      feedbackMessage =
        choice.signal === 'more_like_this'
          ? 'More like this recorded. It changes the next sweep.'
          : 'Less like this recorded. It changes the next sweep.';
    },
    onError: (error: Error) => {
      feedbackMessage = '';
      failure = error;
    }
  });

  const clearFeedback = createMutation({
    mutationFn: (from: RecommendationSource) => api.clearRecommendationFeedback(from),
    onSuccess: async () => {
      clearFeedbackOpen = false;
      failure = null;
      feedbackMessage = 'Feedback cleared. The next sweep uses source ranking alone.';
      await queryClient.invalidateQueries({ queryKey: ['recommendations'] });
    },
    onError: (error: Error) => {
      failure = error;
    }
  });

  // The three scopes, in the order they widen. Each one names the thing it rules
  // out under the label, because "anything by this artist" is a decision nobody
  // should have to take from memory of which row they opened.
  function scopes(recommendation: Recommendation): MenuItem[] {
    const artists = recommendation.artistIds ?? [];
    return [
      {
        label: 'This recording',
        detail: name(recommendation),
        onchoose: () =>
          $dismiss.mutate({ subject: 'recording', ids: [recommendation.recordingId] })
      },
      {
        label: 'Anything from this release',
        detail: recommendation.releaseTitle || 'the release it was suggested from',
        onchoose: () =>
          $dismiss.mutate({ subject: 'release_group', ids: [recommendation.releaseGroupId] })
      },
      {
        label: artists.length > 1 ? 'Anything by these artists' : 'Anything by this artist',
        // A refused scope keeps its place and says why, rather than leaving the
        // menu a different length on one row.
        detail: artists.length === 0 ? 'Schall does not know who this is' : artistOf(recommendation),
        disabled: artists.length === 0,
        onchoose: () => $dismiss.mutate({ subject: 'artist', ids: artists })
      }
    ];
  }

  // What the reader was shown. The fifth rule counts showings, so it has to be
  // written by the view that did the showing. A recording is reported once per
  // visit: the server counts one showing a day at most, and reporting the same
  // row again on every refetch would say nothing new.
  //
  // Shown means it was on screen. A page is twenty-five rows and a window fits
  // about thirteen, so reporting the page as it arrived counted twelve rows
  // nobody had scrolled to. Three visits like that held them back for ninety
  // days, and the screen then told the reader they had ignored music it had
  // never put in front of them (issue #327).
  //
  // A row counts once it has stayed on screen for a moment, in a tab somebody
  // is looking at. Passing under the pointer during a fast scroll is not a
  // showing, and neither is a page opened in a background tab.
  const dwell = 1000;
  const reported = new Set<string>();
  const impressions = createMutation({
    mutationFn: (recordingIds: string[]) => api.recordRecommendationImpressions(recordingIds)
  });

  // Rows arrive on screen together, so their reports are collected and sent as
  // one call rather than one call each.
  let waiting: string[] = [];
  let sending: ReturnType<typeof setTimeout> | undefined;

  function shown(recordingId: string) {
    if (reported.has(recordingId)) return;
    reported.add(recordingId);
    waiting.push(recordingId);
    clearTimeout(sending);
    sending = setTimeout(() => {
      const batch = waiting;
      waiting = [];
      if (batch.length > 0) $impressions.mutate(batch);
    }, 250);
  }

  // One observer for the whole list, and one timer per row currently on screen.
  // The timer is what makes this "was there", rather than "went past".
  const named = new Map<Element, string>();
  const dwelling = new Map<Element, ReturnType<typeof setTimeout>>();
  let watcher: IntersectionObserver | undefined;

  function arrived(entries: IntersectionObserverEntry[]) {
    for (const entry of entries) {
      const row = entry.target;
      if (!entry.isIntersecting) {
        clearTimeout(dwelling.get(row));
        dwelling.delete(row);
        continue;
      }
      if (dwelling.has(row)) continue;
      const count = () => {
        // A hidden tab shows nothing, whatever the geometry says. Wait rather
        // than drop it: the reader may come back to this exact screen.
        if (document.visibilityState !== 'visible') {
          dwelling.set(row, setTimeout(count, dwell));
          return;
        }
        dwelling.delete(row);
        const recordingId = named.get(row);
        if (!recordingId) return;
        shown(recordingId);
        watcher?.unobserve(row);
      };
      dwelling.set(row, setTimeout(count, dwell));
    }
  }

  /** Counts this row as shown once it has been on screen for a moment. */
  function onscreen(row: HTMLElement, recordingId: string) {
    named.set(row, recordingId);
    if (typeof IntersectionObserver === 'undefined') return;
    watcher ??= new IntersectionObserver(arrived, { threshold: 0.6 });
    watcher.observe(row);
    return {
      update: (next: string) => named.set(row, next),
      destroy: () => {
        watcher?.unobserve(row);
        clearTimeout(dwelling.get(row));
        dwelling.delete(row);
        named.delete(row);
      }
    };
  }

  $effect(() => () => {
    watcher?.disconnect();
    clearTimeout(sending);
    for (const timer of dwelling.values()) clearTimeout(timer);
  });

  function name(recommendation: Recommendation) {
    return recommendation.recordingTitle || 'Unnamed recording';
  }

  function artistOf(recommendation: Recommendation) {
    return recommendation.artistName || 'the artist it was suggested from';
  }

  function details(recommendation: Recommendation) {
    return [recommendation.artistName, recommendation.releaseTitle].filter(Boolean).join(' · ');
  }

  // The source's own words for why it offered a recording, written out. An
  // unknown code is shown as it came, because inventing a sentence for it would
  // be Schall speaking for the source.
  const reasons: Record<string, string> = {
    cf_raw: 'people with your listening history play it',
    cf_top: 'near the top of what it suggests for you',
    similar_artist: 'close to an artist you listen to',
    similar_recording: 'close to a recording you listen to',
    top_recording: 'one of your own most played',
    listened: "you played it, you don't have it",
    co_listened: 'played beside music you have',
    feedback_more_like_this: 'you asked for more like this',
    feedback_less_like_this: 'you asked for less like this'
  };

  function why(recommendation: Recommendation) {
    return recommendation.reasonCodes.map((code) => reasons[code] ?? code).join(' · ');
  }

  // What the six product entries held back, as a sentence. Without it a short list looks
  // like a source with little to say.
  const heldBack = $derived.by(() => {
    if (!hidden || hidden.total === 0) return '';
    const parts = [
      [hidden.owned, 'already in your library'],
      [hidden.requested, 'already requested'],
      [hidden.notWanted, 'you stopped looking for'],
      [hidden.followedArtist, 'by artists you follow'],
      [hidden.followedLabel, 'from labels you follow'],
      [hidden.dismissed, 'you said no to'],
      [hidden.unkept, 'you had for a week and did not keep'],
      [hidden.impressionFatigue, 'shown three times without an answer']
    ] as const;
    const said = parts
      .filter(([count]) => count > 0)
      .map(([count, text]) => `${count} ${text}`)
      .join(', ');
    return `${hidden.total} more ${hidden.total === 1 ? 'suggestion is' : 'suggestions are'} not shown: ${said}.`;
  });

  const readAt = $derived(snapshot?.fetchedAt ? relativeTime(snapshot.fetchedAt) : '');

  // The sweep refreshes this list by itself, once a day. When it stops being
  // able to, the last complete list stays here and gets older with nothing said,
  // which reads as a quiet week rather than as a fault. `stalled` is when to say
  // it. A stored list with no sweep behind it at all — a seeded or restored
  // database — has nothing to report and says nothing.
  const stalled = $derived(!!refresh?.attempted && !refresh.succeeded);
  const nextRead = $derived(refresh?.nextAttemptAt ? relativeTime(refresh.nextAttemptAt) : '');
</script>

<div class="flex flex-col gap-4 px-6 py-4">
  <!-- The list is only a read, so the note asks again by itself. The press
       below it is not: somebody asked for that, and the button they pressed is
       the control to name. -->
  {#if $recommendations.error}
    <ErrorNote error={$recommendations.error} retry={() => $recommendations.refetch()} />
  {/if}
  {#if failure}
    <ErrorNote error={failure} />
  {/if}

  <section class="flex flex-col gap-2.5">
    <div class="flex flex-wrap items-center gap-2.5">
      <span class="label">Suggested by your listening</span>
      <Segmented
        variant="filter"
        options={sources}
        value={source}
        onchange={chooseSource}
        label="Recommendation source"
      />
      <span class="hidden h-px flex-1 bg-line-thin sm:block"></span>
      <span class="min-w-56 text-meta text-ink-3">
        {#if readAt}
          {own ? 'built from your listens' : 'read from ListenBrainz'} {readAt}
        {:else}
          <span aria-hidden="true">{own ? 'built from your listens' : 'read from ListenBrainz'} …</span>
        {/if}
      </span>
    </div>

    <div class="flex flex-wrap items-start gap-3">
      <details class="group min-w-[12rem] flex-1">
        <summary
          class="tap-tall flex w-fit cursor-pointer list-none items-center gap-1.5 text-meta text-ink-3 transition hover:text-ink-2 [&::-webkit-details-marker]:hidden"
        >
          <ChevronRight size={12} class="transition-transform group-open:rotate-90" />
          What is this
        </summary>
        <p class="reveal mt-1 max-w-[64ch] text-meta leading-5 text-ink-2">
          {#if own}
            Music you played at least twice that your library does not hold, ranked by how
            often you played it beside music you have. Schall reads the listens it copied
            from ListenBrainz.
          {:else}
            Music your library does not hold, suggested from what your ListenBrainz
            account has listened to.
          {/if}
          Press <span class="text-ink">Want it</span> and Schall looks
          for a copy. Press
          <span class="text-ink">Not interested</span> and choose what is never suggested again: this
          recording, its release, or its artist. Use <span class="text-ink">More like this</span> or
          <span class="text-ink">Less like this</span> to adjust the next sweep without hiding a row.
        </p>
      </details>
      {#if clearFeedbackOpen}
        <div class="w-full rounded-row border border-fail/40 bg-fail/14 p-3" role="group" aria-label="Clear feedback confirmation">
          <p class="text-body font-medium text-ink">Clear all {sourceName} feedback?</p>
          <p class="mt-1 text-meta leading-5 text-ink-2">
            This removes every More like this and Less like this pressed on the {sourceName} list.
            The next sweep uses source ranking alone.
          </p>
          <div class="mt-3 flex flex-wrap justify-end gap-2">
            <Button variant="ghost" size="sm" tall onclick={() => (clearFeedbackOpen = false)} disabled={$clearFeedback.isPending}>
              Cancel
            </Button>
            <Button variant="danger" size="sm" tall onclick={() => $clearFeedback.mutate(source)} disabled={$clearFeedback.isPending}>
              {$clearFeedback.isPending ? 'Clearing' : 'Clear feedback'}
            </Button>
          </div>
        </div>
      {:else}
        <Button variant="outline" size="sm" tall onclick={() => (clearFeedbackOpen = true)}>
          Clear feedback
        </Button>
      {/if}
    </div>

    <!--
      Said only when there is a stored list to be stale. With no list at all the
      empty state below says it instead, and says it as the whole answer.
    -->
    {#if stalled && snapshot?.fetched}
      <p class="max-w-[64ch] text-meta leading-4 text-ink-3">
        This list stopped refreshing,
        {#if nextRead}
          and Schall tries again {nextRead}.
        {:else}
          and no next read is waiting.
        {/if}
        <!-- The own engine needs no account, so Settings has nothing to fix
             for it. -->
        {#if !own}
          Check your account under
          <a href="/settings" class="text-ink underline underline-offset-2">Settings</a>.
        {/if}
      </p>
    {/if}

    <!--
      A short list and an old list are two different faults, and this is the
      short one: the read that built this list arrived on time and got only part
      of the answer, because one of the ListenBrainz requests behind it was
      refused. The header has said so since the sweep was written and nothing
      read it (issue #330).

      The header also carries the sweep's own sentence about what was missing,
      and that is not shown. A published header can carry three of them: how
      many of the account's top recordings the seed budget spent, a similarity
      request that stopped at a named MusicBrainz seed, and seeds a provider
      refused. Two repeat an identifier or a provider's error and the third is
      an internal budget, so the fact is shown here and the sentence is not.

      The line above owns when the next read is, so this one promises one only
      when that line is absent. Saying "the next read tries for all of it"
      under "no next read is waiting" would contradict it.
    -->
    <!-- A partial Schall list is one still being looked up in MusicBrainz,
         25 recordings a pass, and it grows until the pool is done. -->
    {#if snapshot?.status === 'partial' && own}
      <p class="max-w-[64ch] text-meta leading-4 text-ink-3">
        This list is still growing. Schall looks up a few recordings in MusicBrainz at a time.
      </p>
    {:else if snapshot?.status === 'partial'}
      <p class="max-w-[64ch] text-meta leading-4 text-ink-3">
        This list is shorter than usual. The read that built it did not get the whole
        answer from ListenBrainz{stalled ? '' : ', and the next read tries for all of it'}.
      </p>
    {/if}

    {#if heldBack}
      <p class="max-w-[64ch] text-meta leading-4 text-ink-3">{heldBack}</p>
    {/if}

    {#if wanted}
      <p class="max-w-[64ch] text-meta leading-4 text-ink-2">{wanted}</p>
    {/if}
    {#if feedbackMessage}
      <p class="max-w-[64ch] text-meta leading-4 text-ink-2">{feedbackMessage}</p>
    {/if}

    {#if items.length > 0}
      <section class="rounded-panel border border-line-thin">
        {#each items as recommendation (recommendation.recordingId)}
          <div
            use:onscreen={recommendation.recordingId}
            class="flex flex-wrap items-center gap-x-3 gap-y-1 border-b border-line-thin px-3 py-2 last:border-b-0"
          >
            <span class="flex min-w-0 flex-1 basis-full flex-col gap-0.5 sm:basis-auto">
              <span class="truncate text-body font-medium text-ink">{name(recommendation)}</span>
              <span class="truncate text-meta text-ink-3">
                {details(recommendation)}
              </span>
            </span>
            <div class="order-3 flex basis-full flex-wrap gap-2 sm:order-none sm:basis-auto">
              <Button
                variant="outline"
                size="xs"
                tall
                aria-label="More like this"
                disabled={$feedback.isPending}
                onclick={() =>
                  $feedback.mutate({ recordingId: recommendation.recordingId, signal: 'more_like_this', source })}
              >
                More like this
              </Button>
              <Button
                variant="outline"
                size="xs"
                tall
                aria-label="Less like this"
                disabled={$feedback.isPending}
                onclick={() =>
                  $feedback.mutate({ recordingId: recommendation.recordingId, signal: 'less_like_this', source })}
              >
                Less like this
              </Button>
            </div>
            <Button
              variant="outline"
              size="sm"
              tall
              class="shrink-0"
              disabled={$want.isPending || $feedback.isPending}
              onclick={() => $want.mutate(recommendation)}
            >
              Want it
            </Button>
            <!--
              One decision at three widths. Three buttons on a row would read as
              three separate actions and crowd a row meant to stay readable, so
              the scopes go behind the one trigger that carries the decision.
            -->
            <Menu
              class="shrink-0"
              trigger="labelled"
              label="Not interested"
              heading="Never suggest again"
              items={scopes(recommendation)}
            />
            {#if why(recommendation)}
              <span class="order-4 basis-full break-words text-meta leading-4 text-ink-3">
                {why(recommendation)}
              </span>
            {/if}
          </div>
        {/each}
        <Pager {total} {offset} {pageSize} onchange={step} />
      </section>
    {:else}
      <Settle pending={$recommendations.isPending}>
        {#snippet placeholder()}
          <div
          class="flex max-h-[calc(100dvh-16rem)] flex-col gap-3 overflow-hidden rounded-panel border border-line-thin p-5"
          aria-busy="true"
          aria-label="Loading recommendations"
        >
          <span class="text-meta text-ink-3">reading your suggestions…</span>
          <div class="-mx-5 -mb-5 overflow-hidden">
            {#each Array.from({ length: 40 }) as _}
              <div aria-hidden="true" class="h-14 animate-pulse border-t border-line-thin"></div>
            {/each}
          </div>
          </div>
        {/snippet}
        <EmptyPanel class="items-start gap-2">
          <ListMusic size={18} class="text-ink-4" />
          {#if !snapshot?.fetched && stalled}
          <!--
            A list is stored only when a sweep walks the whole answer. A sweep
            that stopped part way keeps nothing, so a first sweep that stopped
            leaves no list at all. Saying "nothing to suggest" here would be
            wrong, and so would asking for an account that is already set up.
          -->
          <span class="text-body font-semibold text-ink">The first read did not finish</span>
          <span class="text-meta leading-5 text-ink-2">
            There is no list yet.
            {#if nextRead}
              Schall tries again {nextRead}.
            {:else}
              No next read is waiting.
            {/if}
          </span>
          {#if !own}
            <Button href="/settings" variant="outline" size="sm">Open settings</Button>
          {/if}
          {:else if !snapshot?.fetched && own}
          <!-- The own engine runs once a copied listen names a recording. It
               needs no account of its own, but the listens come from a
               ListenBrainz sync, so Settings is where they start. -->
          <span class="text-body font-semibold text-ink">No sweep has run yet</span>
          <span class="text-meta leading-5 text-ink-2">
            Schall builds this list from the listens it copies from ListenBrainz.
          </span>
          <Button href="/settings" variant="outline" size="sm">Open settings</Button>
          {:else if !snapshot?.fetched}
          <span class="text-body font-semibold text-ink">No listening history read yet</span>
          <!-- Where suggestions come from is how the feature works. The reader
               needs the one thing they can do, and Open settings is under it. -->
          <span class="text-meta leading-5 text-ink-2">
            Add a ListenBrainz account under Settings. The first list arrives within a few
            minutes.
          </span>
          <Button href="/settings" variant="outline" size="sm">Open settings</Button>
          {:else if hidden && hidden.total > 0}
          <span class="text-body font-semibold text-ink">Every suggestion is held back</span>
          <span class="text-meta leading-5 text-ink-2">
            {sourceName} had {hidden.total}
            {hidden.total === 1 ? 'suggestion' : 'suggestions'}, all held back for now. The next
            sweep {own ? 'builds the list again' : 'asks again'}.
          </span>
          {:else}
          <span class="text-body font-semibold text-ink">Nothing suggested yet</span>
          <span class="text-meta leading-5 text-ink-2">
            {#if own}
              Schall found nothing to suggest from your listens yet.
            {:else}
              ListenBrainz had nothing to suggest for this account yet.
            {/if}
          </span>
          {/if}
        </EmptyPanel>
      </Settle>
    {/if}
  </section>
</div>

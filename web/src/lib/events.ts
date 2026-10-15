import type { QueryClient } from '@tanstack/svelte-query';
import { api } from '$lib/api';

// The queue's own three keys, on every topic below. Every notice the server
// sends is published at a job boundary — claimed, or settled one way or the
// other — so whichever topic carries it, what it also says is that the queue
// has moved. The jobs view is the one place that is the subject rather than a
// side effect, and it is the reason its refetch intervals can stay off while
// nothing is running.
const queue = [['jobs'], ['jobs-failed'], ['jobs-schedule']];

// The topics the server publishes. A notice says a view is stale, never what it
// now holds, so each one maps to the query keys that have to be asked again. A
// key here invalidates everything under it, so ['releases'] covers one release
// and its tracks as well as the list.
const invalidations: Record<string, string[][]> = {
  // A pass over the download inbox is announced here too: it is the same folder
  // the transfers land in, and the settings block that asks for one watches it.
  // The Overview is read once when it opens and never on a notice: with twenty
  // downloads running it re-read the whole page every few seconds.
  downloads: [['downloads'], ['dashboard'], ['inbox-cleanups'], ...queue],
  // Bytes moving on a transfer whose request kept its status. A poll sends one
  // every few seconds while anything downloads, and only the downloads list
  // draws progress, so nothing that counts requests is asked again for it.
  // The review page keeps its download questions under ['downloads'] too, as
  // pages of 100, and every notice re-read each loaded page in turn.
  'download-progress': [['downloads', 'list']],
  'source-searches': [['source-search'], ...queue],
  // An upload is announced on the library topic: what it changes is what the
  // library holds, and the page that shows uploads is watching the same work.
  // A running layout migration is announced here too: what it changes is where
  // the library holds its files, and a pass over the tags changes what those
  // files say.
  // The review page reads the same files under its own keys — what nothing
  // could identify, what nothing could match, what is here twice — and a scan
  // is where most of those questions come from, so the topic that says a scan
  // moved has to reach them as well.
  // The palette asks one question of everything at once, so anything that
  // changes what Schall holds changes what it would answer. Nothing is refetched
  // by it unless a palette is open on that query, which is the whole reason it
  // can afford to be on three topics.
  library: [
    ['library'],
    ['library-files'],
    ['global-search'],
    ['library-duplicates'],
    ['review-identities'],
    ['review-matches'],
    ['review-duplicates'],
    ['uploads'],
    ['dashboard'],
    ['library-layout-run'],
    ['library-tags'],
    ...queue
  ],
  // A label refresh is announced here too, so the labels are on it.
  catalogue: [['artists'], ['releases'], ['labels'], ['dashboard'], ['global-search'], ...queue],
  // An import, a sync with the player or a refresh of a generated list ending.
  // These are rare, and an import is one somebody asked for. Settings reads the
  // two generated lists under keys of their own.
  playlists: [
    ['playlists'],
    ['weekly'],
    ['new-releases'],
    ['spotify-settings'],
    ['global-search'],
    ...queue
  ],
  // A want settling changes what is waiting in the review queue and the list of
  // wants still being looked for, which is why one topic reaches both. The
  // playlist pages are left alone: with 22 downloads running they re-read every
  // loaded page of a list every 8 to 11 s, and an entry's new state can wait
  // until the page is opened again. It is also every pass that defers a want because
  // the download source is not logged in (#495), which is what keeps the
  // dashboard's banner about it from waiting on the next unrelated refetch.
  // Search marks a wanted track, so it hears about wants too.
  acquisitions: [['review-queue'], ['wants'], ['dashboard'], ['global-search'], ...queue]
};

/** The shortest time between two rounds of invalidation. */
export const NOTICE_GAP_MS = 1000;

/** The shortest time between two reads of a count. */
export const COUNT_GAP_MS = 10_000;

// Counts: the navigation's badges and the Wishlist tab's figure. Four topics
// reach them, and with downloads running the Artists page read the dashboard
// four times in ten seconds for figures nobody watches change.
//
// The review queue waits on the same gap. Every want a sweep settles is a
// notice, and Review read `/review-queue?limit=100` three times as it opened.
// A question the reader answers is re-read at once by the page itself.
const counts = [['dashboard'], ['wants', 'looking', 'count'], ['review-queue']];

function startsWith(key: readonly unknown[], prefix: readonly unknown[]) {
  return prefix.every((part, index) => key[index] === part);
}

function isCount(key: readonly unknown[]) {
  return counts.some((count) => startsWith(key, count));
}

// connectEvents holds a server-sent event stream open and invalidates the views
// a notice concerns. It returns the function that closes it.
//
// It is a supplement to the refetch intervals rather than a replacement for
// them: a stream can be blocked by a proxy, dropped, or unsupported, and a view
// that then never updated would be worse than one that updates slowly. The
// intervals are unhurried because this exists, not absent because of it.
export function connectEvents(queryClient: QueryClient): () => void {
  if (typeof EventSource === 'undefined') return () => {};

  let source: EventSource;
  try {
    source = api.events();
  } catch {
    return () => {};
  }

  // Notices are gathered and sent on at most once a second. A refresh of a few
  // hundred releases announces each one twice, and every notice re-read the
  // whole artist list and cancelled the read already under way, so in a burst
  // the list could be asked for many times and never arrive. The first notice
  // after a quiet second goes through at once; the rest of a burst is one more
  // round at its end.
  const pending = new Map<string, string[]>();
  let last = 0;
  let timer: ReturnType<typeof setTimeout> | undefined;
  // The counts a round touched wait on a gap of their own.
  const countsPending = new Map<string, string[]>();
  let countsLast = 0;
  let countsTimer: ReturnType<typeof setTimeout> | undefined;

  const flushCounts = () => {
    countsTimer = undefined;
    countsLast = Date.now();
    const queryKeys = [...countsPending.values()];
    countsPending.clear();
    for (const queryKey of queryKeys) {
      void queryClient.invalidateQueries({ queryKey }, { cancelRefetch: false });
    }
  };

  const scheduleCounts = () => {
    if (countsTimer !== undefined) return;
    const wait = countsLast + COUNT_GAP_MS - Date.now();
    if (wait <= 0) flushCounts();
    else countsTimer = setTimeout(flushCounts, wait);
  };

  const flush = () => {
    timer = undefined;
    last = Date.now();
    const queryKeys = [...pending.values()];
    pending.clear();
    for (const queryKey of queryKeys) {
      for (const count of counts) {
        if (startsWith(count, queryKey)) countsPending.set(JSON.stringify(count), count);
      }
      if (isCount(queryKey)) continue;
      // A read already under way is answering the same question; starting it
      // again only throws away what it has done.
      void queryClient.invalidateQueries(
        { queryKey, predicate: (query) => !isCount(query.queryKey) },
        { cancelRefetch: false }
      );
    }
    if (countsPending.size) scheduleCounts();
  };

  for (const [topic, keys] of Object.entries(invalidations)) {
    source.addEventListener(topic, () => {
      for (const queryKey of keys) pending.set(JSON.stringify(queryKey), queryKey);
      if (timer !== undefined) return;
      const wait = last + NOTICE_GAP_MS - Date.now();
      if (wait <= 0) flush();
      else timer = setTimeout(flush, wait);
    });
  }

  // EventSource reconnects on its own, so an error is not something to act on.
  // What it must not do is invalidate on a connection that is down, which is
  // what the intervals are still there for.
  source.addEventListener('error', () => {});

  return () => {
    clearTimeout(timer);
    clearTimeout(countsTimer);
    source.close();
  };
}

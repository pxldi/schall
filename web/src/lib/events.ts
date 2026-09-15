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
  downloads: [['downloads'], ['dashboard'], ['overview'], ['inbox-cleanups'], ...queue],
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
    ['overview'],
    ...queue
  ],
  catalogue: [['artists'], ['releases'], ['dashboard'], ['global-search'], ...queue],
  // A target settling changes what a playlist entry shows, so both topics
  // reach the playlist views.
  playlists: [['playlists'], ['spotify-settings'], ['global-search'], ...queue],
  // A want settling changes what a playlist entry shows, what is waiting in the
  // review queue, and the list of wants still being looked for, which is why one
  // topic reaches all three. It is also every pass that defers a want because
  // the download source is not logged in (#495), which is what keeps the
  // dashboard's banner about it from waiting on the next unrelated refetch.
  acquisitions: [['playlists'], ['review-queue'], ['wants'], ['dashboard'], ...queue]
};

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

  for (const [topic, keys] of Object.entries(invalidations)) {
    source.addEventListener(topic, () => {
      for (const queryKey of keys) {
        void queryClient.invalidateQueries({ queryKey });
      }
    });
  }

  // EventSource reconnects on its own, so an error is not something to act on.
  // What it must not do is invalidate on a connection that is down, which is
  // what the intervals are still there for.
  source.addEventListener('error', () => {});

  return () => source.close();
}

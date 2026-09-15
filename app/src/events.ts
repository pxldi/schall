import type { QueryClient } from '@tanstack/react-query';
import EventSource from 'react-native-sse';
import { authHeaders, url, type Session } from '@/api/client';

/** The server's topics that reach a view the app draws, and the query keys
 * each one makes stale. The map is a subset of web/src/lib/events.ts: a notice
 * says a view is stale, never what it now holds. */
type Topic = 'downloads' | 'acquisitions';

const invalidations: Record<Topic, string[][]> = {
  downloads: [['downloads']],
  acquisitions: [['review-queue'], ['wants']]
};

/** Holds the event stream open and invalidates what a notice concerns. The
 * browser's EventSource cannot send a header, so this one is react-native-sse
 * with the Bearer on it. It reconnects by itself after an error; the 15 s
 * poll stays as the safety net, the same as on the web. Returns the function
 * that closes the stream. */
export function connectEvents(client: QueryClient, session: Session): () => void {
  const source = new EventSource<Topic>(url(session, '/api/v1/events'), {
    headers: authHeaders(session),
    pollingInterval: 5_000
  });
  for (const topic of Object.keys(invalidations) as Topic[]) {
    source.addEventListener(topic, () => {
      for (const queryKey of invalidations[topic]) void client.invalidateQueries({ queryKey });
    });
  }
  // A dropped stream is not something to act on: the poll covers it, and
  // invalidating here would refetch on a connection that is down.
  source.addEventListener('error', () => {});
  return () => {
    source.removeAllEventListeners();
    source.close();
  };
}

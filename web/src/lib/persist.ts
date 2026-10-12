import type { QueryClient } from '@tanstack/svelte-query';
import {
  persistQueryClientRestore,
  persistQueryClientSave,
  type PersistedClient,
  type Persister
} from '@tanstack/query-persist-client-core';

// The answers the pages have read, kept in the browser between visits. A page
// opened again, after a reload or the next day, draws what it showed last time
// at once and asks the API again behind it, so the placeholders are seen on the
// first visit only. Server-sent events and the query's own staleness still
// decide when anything is read again; this only changes what is on screen while
// that happens.
//
// IndexedDB rather than localStorage: the artist list alone is about a
// megabyte on a large library, and localStorage stops at five.

const DATABASE = 'schall';
const STORE = 'queries';
const KEY = 'client';

/** How long a kept answer may be shown before it is thrown away unread. */
export const PERSIST_MAX_AGE = 24 * 60 * 60 * 1000;

function open(): Promise<IDBDatabase> {
  return new Promise((resolve, reject) => {
    const request = indexedDB.open(DATABASE, 1);
    request.onupgradeneeded = () => request.result.createObjectStore(STORE);
    request.onsuccess = () => resolve(request.result);
    request.onerror = () => reject(request.error);
  });
}

function run<T>(mode: IDBTransactionMode, act: (store: IDBObjectStore) => IDBRequest<T>): Promise<T> {
  return open().then(
    (db) =>
      new Promise<T>((resolve, reject) => {
        const request = act(db.transaction(STORE, mode).objectStore(STORE));
        request.onsuccess = () => resolve(request.result);
        request.onerror = () => reject(request.error);
      }).finally(() => db.close())
  );
}

/** A persister that keeps the whole cache under one IndexedDB key. */
export function indexedDBPersister(): Persister {
  return {
    persistClient(client) {
      // A full disc or a private window refuses the write; the app runs as it
      // did before, without the kept copy.
      void run('readwrite', (store) => store.put(client, KEY)).catch(() => undefined);
    },
    restoreClient() {
      return run<PersistedClient | undefined>('readonly', (store) => store.get(KEY)).catch(() => undefined);
    },
    removeClient() {
      return run('readwrite', (store) => store.delete(KEY)).then(
        () => undefined,
        () => undefined
      );
    }
  };
}

/** Restores the kept answers into the client, then keeps them up to date.
 *  Resolves once the restore is done or has failed; it never rejects. A new
 *  build (`buster`) starts empty, because the shape of an answer may have
 *  changed. */
export async function persistQueries(queryClient: QueryClient, buster: string): Promise<void> {
  if (typeof indexedDB === 'undefined') return;
  const options = { queryClient, persister: indexedDBPersister(), maxAge: PERSIST_MAX_AGE, buster };
  try {
    await persistQueryClientRestore(options);
  } catch {
    // An unreadable copy is dropped by the restore itself; start empty.
  }
  saveAfterAnswers(queryClient, () => void persistQueryClientSave(options));
}

/** How long after an answer arrives the cache is written. Answers that land
 *  together, a page reading ten queries or a burst of notices, write once. */
export const PERSIST_DELAY_MS = 2000;

/** Calls save a while after a query receives an answer or leaves the cache.
 *
 *  The library's own subscription writes on every cache event, and most events
 *  are a query starting or stopping a fetch: nothing a reload would draw
 *  differently. Each write copies the whole cache, a megabyte or more on a
 *  large library, on the main thread. */
export function saveAfterAnswers(queryClient: QueryClient, save: () => void): () => void {
  let timer: ReturnType<typeof setTimeout> | undefined;
  const unsubscribe = queryClient.getQueryCache().subscribe((event) => {
    const answered = event.type === 'updated' && event.action.type === 'success';
    if (!answered && event.type !== 'removed') return;
    timer ??= setTimeout(() => {
      timer = undefined;
      save();
    }, PERSIST_DELAY_MS);
  });
  return () => {
    clearTimeout(timer);
    unsubscribe();
  };
}

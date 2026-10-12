import type { QueryClient } from '@tanstack/svelte-query';
import {
  persistQueryClientRestore,
  persistQueryClientSubscribe,
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

/** A persister that keeps the whole cache under one IndexedDB key. Writes are
 *  held for a second, so a page reading ten queries writes once. */
export function indexedDBPersister(): Persister {
  let timer: ReturnType<typeof setTimeout> | undefined;
  let latest: PersistedClient | undefined;
  return {
    persistClient(client) {
      latest = client;
      timer ??= setTimeout(() => {
        timer = undefined;
        const value = latest;
        latest = undefined;
        // A full disc or a private window refuses the write; the app runs as
        // it did before, without the kept copy.
        void run('readwrite', (store) => store.put(value, KEY)).catch(() => undefined);
      }, 1000);
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
  persistQueryClientSubscribe(options);
}

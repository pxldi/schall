import { afterEach, describe, expect, it, vi } from 'vitest';
import type { QueryClient } from '@tanstack/svelte-query';
import { connectEvents } from '$lib/events';

/** A notice carries no state, so what there is to check is which views a topic
 * reaches. That is a table nobody reads while adding a page, which is how the
 * review page came to sit behind a topic that never invalidated it — so the
 * keys are named here rather than read back out of the module. */

class FakeStream {
  static opened: FakeStream[] = [];
  readonly listeners = new Map<string, ((event: Event) => void)[]>();
  closed = false;

  constructor(readonly url: string) {
    FakeStream.opened.push(this);
  }

  addEventListener(topic: string, listener: (event: Event) => void) {
    const existing = this.listeners.get(topic) ?? [];
    this.listeners.set(topic, [...existing, listener]);
  }

  close() {
    this.closed = true;
  }

  /** What the server sending on a topic looks like from in here. */
  announce(topic: string) {
    for (const listener of this.listeners.get(topic) ?? []) listener(new Event(topic));
  }
}

// The logic project runs under node, where there is no EventSource to speak of,
// so the global is the boundary and the only thing stubbed.
function connect() {
  FakeStream.opened = [];
  vi.stubGlobal('EventSource', FakeStream);
  const invalidateQueries = vi.fn();
  const close = connectEvents({ invalidateQueries } as unknown as QueryClient);
  return { close, invalidateQueries, stream: FakeStream.opened[0] };
}

/** The keys one notice asked for, flattened, so a test names views rather than
 * arrays of one string. */
function asked(invalidateQueries: ReturnType<typeof vi.fn>) {
  return invalidateQueries.mock.calls.map((call) => call[0].queryKey.join('/'));
}

afterEach(() => {
  vi.unstubAllGlobals();
});

describe('connectEvents', () => {
  it('opens the one stream the application has', () => {
    const { stream } = connect();

    expect(stream.url).toBe('/api/v1/events');
  });

  it('asks the review page for its work again when the library moves', () => {
    const { invalidateQueries, stream } = connect();

    stream.announce('library');

    // The three the page reads and nothing else publishes for: a file nothing
    // could identify, one matching could not settle, and one the library holds
    // twice. A scan that ends with any of them is a page that has new work.
    expect(asked(invalidateQueries)).toEqual(
      expect.arrayContaining(['review-identities', 'review-matches', 'review-duplicates'])
    );
  });

  it('keeps the library views the same notice already carried', () => {
    const { invalidateQueries, stream } = connect();

    stream.announce('library');

    expect(asked(invalidateQueries)).toEqual(
      expect.arrayContaining([
        'library',
        'library-files',
        'library-duplicates',
        'uploads',
        'dashboard',
        'library-layout-run',
        'library-tags'
      ])
    );
  });

  // The queue is the one view every topic reaches. Whatever moved was run by a
  // job, so a notice that something moved is also a notice that the queue is
  // not what the jobs view last drew. Written out here rather than folded into
  // an arrayContaining so these stay exact: a topic that starts invalidating
  // something else should still fail.
  const queue = ['jobs', 'jobs-failed', 'jobs-schedule'];

  it('reaches the playlists, the review queue, the wants and the dashboard when a want settles', () => {
    const { invalidateQueries, stream } = connect();

    stream.announce('acquisitions');

    expect(asked(invalidateQueries)).toEqual([
      'playlists',
      'review-queue',
      'wants',
      'dashboard',
      ...queue
    ]);
  });

  it('reaches the download views and the dashboard when a transfer moves', () => {
    const { invalidateQueries, stream } = connect();

    stream.announce('downloads');

    expect(asked(invalidateQueries)).toEqual([
      'downloads',
      'dashboard',
      'overview',
      'inbox-cleanups',
      ...queue
    ]);
  });

  it('reaches the catalogue views when an artist or a release is refreshed', () => {
    const { invalidateQueries, stream } = connect();

    stream.announce('catalogue');

    expect(asked(invalidateQueries)).toEqual([
      'artists',
      'releases',
      'dashboard',
      'global-search',
      ...queue
    ]);
  });

  // The palette asks one question of everything at once, so anything that
  // changes what Schall holds changes what it would answer.
  it('reaches the one search over everything when the library moves', () => {
    const { invalidateQueries, stream } = connect();

    stream.announce('library');

    expect(asked(invalidateQueries)).toContain('global-search');
  });

  it('invalidates nothing for a topic it has no views for', () => {
    const { invalidateQueries, stream } = connect();

    // The server can publish a topic this build has never heard of — a rollout
    // is two versions for a while — and a notice nobody mapped is one to ignore
    // rather than a reason to refetch everything.
    stream.announce('mystery');

    expect(invalidateQueries).not.toHaveBeenCalled();
  });

  it('closes the stream when the layout that opened it goes away', () => {
    const { close, stream } = connect();

    close();

    expect(stream.closed).toBe(true);
  });

  it('does nothing where the browser has no EventSource at all', () => {
    vi.stubGlobal('EventSource', undefined);
    const invalidateQueries = vi.fn();

    // A stream that cannot be opened leaves the refetch intervals to it, and
    // what it must not do is throw on the way past and take the layout down.
    expect(() => connectEvents({ invalidateQueries } as unknown as QueryClient)()).not.toThrow();
    expect(invalidateQueries).not.toHaveBeenCalled();
  });

  it('does nothing where opening the stream throws', () => {
    vi.stubGlobal(
      'EventSource',
      class {
        constructor() {
          throw new Error('blocked');
        }
      }
    );
    const invalidateQueries = vi.fn();

    expect(() => connectEvents({ invalidateQueries } as unknown as QueryClient)()).not.toThrow();
  });
});

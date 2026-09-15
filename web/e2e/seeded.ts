/** What internal/seed/fixture.sql put in the database, as the numbers a test is
 * allowed to assert. It is here rather than inline in the specs so that a
 * fixture that gains a release fails in one place instead of six.
 *
 * **The tests do not write.** One fixture is applied per run and every test
 * reads it, so a test that followed an artist or answered a review question
 * would change what the tests running beside it are looking at. Anything that
 * needs to change data needs its own seeded database, not a place in here.
 */
export const seeded = {
  artists: {
    /** Followed, and the library holds every release of theirs. */
    complete: { name: 'Aurora Lake', releases: 2, owned: 2 },
    /** Followed, missing most of it, with a transfer under way. */
    incomplete: { name: 'Vela Nine', releases: 2, owned: 0 },
    /** Followed, complete, but a want is waiting for an ear. */
    waiting: { name: 'Kestrel Grove', releases: 1, owned: 1 },
    /** Held rather than followed: their music is in the library, nobody asked. */
    held: { name: 'Marram', releases: 1, owned: 1 },
    /** Followed this morning, nothing fetched since, so there is no discography yet. */
    unfetched: { name: 'Petrichor Machine', releases: 0, owned: 0 }
  },
  /** The counts the completeness strip states, over the whole catalogue. */
  completeness: { all: 5, incomplete: 1, complete: 3, attention: 3 },
  releases: {
    complete: ['Glass Harbour', 'Tideline'],
    incomplete: ['Signal Fires', 'Nine Lanterns']
  },
  files: {
    total: 17,
    /** One of each thing that can stop and ask, plus the wants. */
    needsReview: 1,
    conflict: 1,
    ambiguous: 1,
    duplicatesToDecide: 1
  },
  /** Wants waiting on a person: one asking about copies, one about a recording. */
  wants: 2,
  /** The wants with nothing to answer and nothing transferring — the state a
   * want spends most of its life in, and what the Wanted tab shows. */
  wanted: {
    looking: { name: 'Marram — Salt Marsh', count: 1 },
    stopped: { name: 'Aurora Lake — Beacon Hill', count: 1 }
  },
  playlist: { name: 'Late night drive', entries: 4, owned: 2 },
  /** The stored answer from the recommendation source. One of the four
   * suggestions names a recording the library already holds, so the first
   * suppression rule holds it back and the other three are shown. */
  recommendations: {
    shown: ['Weather Report', 'Cold Sun', 'Slack Water'],
    heldBack: { title: 'Low Tide Signal', count: 1 }
  },
  download: { album: 'Signal Fires', username: 'harbourmaster' }
} as const;

/** What is waiting on a person: every want plus every file that stopped. */
export const waitingOnAPerson =
  seeded.wants +
  seeded.files.needsReview +
  seeded.files.conflict +
  seeded.files.ambiguous +
  seeded.files.duplicatesToDecide;

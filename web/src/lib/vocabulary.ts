/** The words Schall uses on screen for what it has and what it is doing.
 *
 * There were five sets of them. Artists said `Incomplete`, `Complete` and `Need
 * attention`; an artist's releases said `owned`, `partial`, `missing`,
 * `importing` and `failed`; playlists said `owned`, `wanted` and `resolving`;
 * Library said `Owned`, `Partial`, `Missing`, `No track list` and `Problems`;
 * Downloads said `Imported`, `Not used`, `Needs review`, `Queued at peer` and
 * `Failed`. Every one of those five was reasonable on its own screen, and
 * together they taught a reader four different dialects for one question.
 *
 * There are two questions, and they are kept apart on purpose.
 *
 * `holding` answers **how much of this do I have**. It is a fact about the
 * library and it does not change on its own.
 *
 * `progress` answers **what is happening to it**, or what happened. It is a
 * fact about the machinery and it does change on its own.
 *
 * A screen may show one of each. It may never invent a sixth word for either.
 */

/** How much of a thing the library holds. A release is complete when the
 * library has every track that counts toward it, partial when it has some, and
 * missing when it has none. A single song has no middle, so it uses the two
 * ends — which is why the word for "I have this" is the same at both sizes. */
export const holding = {
  complete: 'Complete',
  partial: 'Partial',
  missing: 'Missing'
} as const;

/** What is happening to a thing, or what became of it. */
export const progress = {
  /** A copy is on its way from a peer. */
  searching: 'Searching',
  /** Asked for, and the loop has not reached it yet. */
  wanted: 'Wanted',
  /** The peer has the request and has not started sending. */
  queuedAtPeer: 'Queued at peer',
  /** Schall is working out which recording this is. */
  resolving: 'Resolving',
  /** Arrived, and being written into the library. */
  importing: 'Importing',
  /** Written into the library. */
  imported: 'Imported',
  /** Arrived, and stopped: nothing could prove it is the right audio, so a
   * person has to look. Never a state the machinery leaves by itself. */
  needsReview: 'Needs review',
  /** Arrived and was not written into the library. This was `Not used`, then
   * `Not imported` — both say what did not happen and never what did, and the
   * downloads screen's own empty state already said "Nothing discarded", so one
   * pile carried two names. `Discarded` is the decision stated as an act. */
  discarded: 'Discarded',
  /** The user said they do not want it. A decision, not a gap. */
  dismissed: 'Dismissed',
  /** It stopped, and the reason is with it. */
  failed: 'Failed'
} as const;

/** The lower-case form, for the places a state is read as a fragment inside a
 * row rather than as a chip: an artist's album tiles and their key, where the
 * word sits in a line of running text. The word itself is never different. */
export function quiet(word: string): string {
  return word.toLocaleLowerCase();
}

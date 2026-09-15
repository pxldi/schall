import type { PlaylistEntry } from '$lib/api';
import { holding, progress, quiet } from '$lib/vocabulary';

/** The five roles every state in Schall is drawn in. Named here rather than a
 * pair of Tailwind class names, so that a row and the chip it is drawn as agree
 * without either restating the other's colours. */
export type EntryRole = 'ok' | 'idle' | 'decide' | 'busy' | 'fail';

export interface EntryState {
  /** The one word the row is read by. */
  label: string;
  role: EntryRole;
  /** What is happening and what happens next, in the want's own words, or ''
   * when the label has already said everything there is. */
  note: string;
}

/** One label per state a reader can act on. An entry is either answered by a
 * file, waiting on the machinery, or waiting on the person.
 *
 * A want Schall fetched and imported is music the user has, so it reads
 * 'complete', the same as a file that was in the library all along — where it
 * came from is not a difference the user has to hold in their head. A song has
 * no middle state, so it uses the same word a release uses when the library has
 * all of it. 'acquired' is
 * left for the gap between a copy being proven and the library having a row for
 * it, which is the only time a want can say it has the music while the library
 * cannot yet play it.
 *
 * The note is shown only while an entry is unanswered. A finished row has
 * nothing left to say that the label has not said, and thirty-odd sentences
 * repeating 'In your library.' would bury the four rows that are still moving. */
export function entryState(entry: PlaylistEntry): EntryState {
  const note = entry.ownedFileId ? '' : (entry.targetSummary ?? '');
  if (entry.ownedFileId) return { label: quiet(holding.complete), role: 'ok', note };
  switch (entry.targetStatus) {
    case 'acquired':
      return { label: 'acquired', role: 'ok', note };
    case 'awaiting_review':
      return { label: quiet(progress.needsReview), role: 'fail', note };
    case 'searching':
      return { label: quiet(progress.searching), role: 'busy', note };
    case 'pending':
      return { label: quiet(progress.wanted), role: 'busy', note };
    case 'unresolved':
      return { label: quiet(progress.resolving), role: 'idle', note };
    case 'not_wanted':
      return { label: quiet(progress.dismissed), role: 'idle', note };
    case 'superseded':
      return { label: 'merged', role: 'idle', note };
  }
  return { label: 'no want', role: 'idle', note };
}

/** How much of a list the user has, counted off the rows on screen rather than
 * read from the playlist's own count.
 *
 * Both numbers are the same reading in the end — the API counts owned entries
 * the same way it fills each entry in — but counting the rows is the only way
 * the header cannot contradict the chips underneath it, whatever happens to
 * either later. */
export function ownedOf(entries: PlaylistEntry[]): number {
  return entries.filter((entry) => entry.ownedFileId).length;
}

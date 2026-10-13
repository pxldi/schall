import type { Playlist, PlaylistEntry } from '$lib/api';
import type { IconName } from '$lib/icons';
import { calendarDate } from '$lib/utils';
import { holding, progress, quiet } from '$lib/vocabulary';

/** The five roles every state in Schall is drawn in. Named here rather than a
 * pair of Tailwind class names, so that a row and the chip it is drawn as agree
 * without either restating the other's colours. */
export type EntryRole = 'ok' | 'idle' | 'decide' | 'busy' | 'fail';

export interface EntryState {
  /** The one word the row is read by. */
  label: string;
  role: EntryRole;
  /** The icon a row draws in place of the word. The word is its tooltip. */
  icon: IconName;
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
  if (entry.ownedFileId) return { label: quiet(holding.complete), role: 'ok', icon: 'check', note };
  // A copy nobody has answered is a question in Review, whatever the want's
  // own status says while it waits for that answer.
  // The want's own sentence can be from before the copy arrived ("No recording
  // MusicBrainz knows…"), which contradicts the label, so this one says why.
  if (entry.copyWaiting)
    return {
      label: quiet(progress.needsReview),
      role: 'fail',
      icon: 'listen',
      note: 'Copy arrived. Listen to decide.'
    };
  switch (entry.targetStatus) {
    case 'acquired':
      return { label: quiet(progress.importing), role: 'ok', icon: 'check', note };
    case 'awaiting_review':
      return { label: quiet(progress.needsReview), role: 'fail', icon: 'listen', note };
    case 'searching':
      return { label: quiet(progress.searching), role: 'busy', icon: 'search', note };
    case 'pending':
      return { label: quiet(progress.wanted), role: 'busy', icon: 'want', note };
    // 'unresolved' sat on lists for months reading "resolving" while its note
    // said MusicBrainz knew no recording or no peer had a good copy. Schall
    // has asked; it is waiting for an answer from outside.
    case 'unresolved':
      return { label: quiet(progress.waiting), role: 'idle', icon: 'waiting', note };
    case 'not_wanted':
      return { label: quiet(progress.dismissed), role: 'idle', icon: 'dismiss', note };
    case 'superseded':
      return { label: 'merged', role: 'idle', icon: 'link', note };
  }
  return { label: 'not on wishlist', role: 'idle', icon: 'plus', note };
}

/** Where a row's question is answered: the want's own place in Review. */
export function reviewHref(entry: PlaylistEntry): string {
  return entry.targetId ? `/review?want=${entry.targetId}` : '/review';
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

/** Where a list came from, one part per fact. Only a Spotify import fills
 * owner and import date, so the other sources say where they came from
 * instead; asking all of them for an owner made the two lists Schall writes
 * read "Importing playlist…" for good. */
export function playlistOrigin(playlist: Playlist): string[] {
  switch (playlist.source) {
    case 'weekly':
    case 'new_releases':
      return ['Made by Schall'];
    case 'navidrome':
      return ['From Navidrome'];
    case 'file':
      return [playlist.sourceId ? `From ${playlist.sourceId}` : 'From a file'];
    case 'manual':
      return ['Made here'];
  }
  if (!playlist.ownerName && !playlist.importedAt) return ['Importing playlist…'];
  return [
    playlist.ownerName ? `by ${playlist.ownerName}` : 'Unknown owner',
    playlist.importedAt ? `imported ${calendarDate(playlist.importedAt)}` : 'importing…'
  ];
}

/** The lists with a cover the strip has not drawn yet, in list order. Two
 * lists that open on the same release drew that cover twice side by side. A
 * list without a release draws art seeded with its own id, so it always
 * counts as new. */
export function oncePerCover(playlists: Playlist[]): Playlist[] {
  const seen = new Set<string>();
  return playlists.filter((playlist) => {
    const key = playlist.coverReleaseId ?? playlist.id;
    if (seen.has(key)) return false;
    seen.add(key);
    return true;
  });
}

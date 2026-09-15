import type { AcquiredCopy, CopyGroup, DownloadRequest, ImportEvidence, ImportFileEvidence, ImportTags, ReviewItem } from '@/api/types';

/** One question on the review screen. The web draws the same three kinds and
 * no others: a want with copies to hear, a want with recordings to choose
 * between, and an album folder whose files did not all match. */
export type Question =
  | { kind: 'downloaded'; id: string; want: ReviewItem }
  | { kind: 'version'; id: string; want: ReviewItem }
  | { kind: 'folder'; id: string; download: DownloadRequest };

export const questionLabel: Record<Question['kind'], string> = {
  downloaded: 'Downloaded',
  version: 'Version',
  folder: 'Folder'
};

export function questions(queue: ReviewItem[] | undefined, folders: DownloadRequest[] | undefined): Question[] {
  const out: Question[] = [];
  for (const want of queue ?? []) {
    out.push(
      want.kind === 'resolution'
        ? { kind: 'version', id: `want:${want.target.id}`, want }
        : { kind: 'downloaded', id: `want:${want.target.id}`, want }
    );
  }
  for (const download of folders ?? []) {
    if (download.importEvidence) out.push({ kind: 'folder', id: `download:${download.id}`, download });
  }
  return out;
}

export interface CopyGroupView {
  best: AcquiredCopy;
  others: AcquiredCopy[];
  rips: number;
}

/** A held copy is one a person may still accept; a better bit rate is the
 * better rip of the same audio. The order is how the cards are read, and
 * says nothing about what may be accepted. */
function rank(copies: AcquiredCopy[]): AcquiredCopy[] {
  return copies.slice().sort((a, b) => {
    const held = Number(b.verdict === 'held') - Number(a.verdict === 'held');
    if (held) return held;
    return (b.evidence?.bitRate ?? 0) - (a.evidence?.bitRate ?? 0) || (b.sizeBytes ?? 0) - (a.sizeBytes ?? 0);
  });
}

/** The web's grouping: copies the server measured as the same audio read as
 * one card. Accept is about the card's best copy; None of these refuses them
 * all. A queue that sent no groups gives one card per copy. */
export function groupCopies(copies: AcquiredCopy[], groups?: CopyGroup[]): CopyGroupView[] {
  const byId = new Map(copies.map((copy) => [copy.id, copy]));
  const views: CopyGroupView[] = [];
  for (const group of groups ?? []) {
    const members = rank(group.copyIds.map((id) => byId.get(id)).filter((copy): copy is AcquiredCopy => !!copy));
    if (members.length === 0) continue;
    for (const member of members) byId.delete(member.id);
    views.push({ best: members[0], others: members.slice(1), rips: group.rips || members.length });
  }
  for (const copy of copies) {
    if (!byId.has(copy.id)) continue;
    byId.delete(copy.id);
    views.push({ best: copy, others: [], rips: 1 });
  }
  const order = rank(views.map((view) => view.best));
  return views.sort((left, right) => order.indexOf(left.best) - order.indexOf(right.best));
}

export function downloadTitle(download: DownloadRequest): { first: string; second: string } {
  if (download.albumTitle) return { first: download.albumTitle, second: download.artistName ?? '' };
  return { first: download.entryTitle ?? download.directory, second: download.entryArtist ?? '' };
}

/** Every catalogue track the evidence mentions, in playing order. A file can
 * only be resolved to a track of the release it was requested for, so this is
 * the whole set of answers the folder screen can give (the web's
 * ImportEvidence.svelte builds the same list). */
export function importCatalogue(evidence: ImportEvidence | undefined): ImportTags[] {
  const byId = new Map<string, ImportTags>();
  for (const tags of [...(evidence?.files ?? []).map((file) => file.expected), ...(evidence?.unmatchedTracks ?? [])]) {
    if (tags?.trackId) byId.set(tags.trackId, tags);
  }
  return [...byId.values()].sort(
    (left, right) => (left.discNumber ?? 1) - (right.discNumber ?? 1) || (left.trackNumber ?? 0) - (right.trackNumber ?? 0)
  );
}

/** Files that disagree are the reason the question exists, so they lead. */
export function orderFiles(files: ImportFileEvidence[]): ImportFileEvidence[] {
  return [...files.filter((file) => file.problems.length), ...files.filter((file) => !file.problems.length)];
}

/** The answer the evidence came closest to, so the common case is confirming
 * a judgement. Nothing is admitted by it: a person still presses Resolve. */
export function suggestedTrack(file: ImportFileEvidence, catalogue: ImportTags[]): string {
  return (
    file.expected?.trackId ||
    file.candidates?.find((candidate) => !candidate.takenBy)?.track.trackId ||
    catalogue[0]?.trackId ||
    ''
  );
}

export function trackLabel(track: ImportTags): string {
  const number = track.trackNumber ? `${track.discNumber && track.discNumber > 1 ? `${track.discNumber}-` : ''}${track.trackNumber}.` : '';
  return [number, track.title ?? 'Untitled'].filter(Boolean).join(' ');
}

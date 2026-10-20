import type { DownloadRequest } from '$lib/api';
import { relativeTime } from '$lib/utils';
import { progress } from '$lib/vocabulary';
import type { StateRole } from '$lib/states';

// What a transfer row says about a request, worked out once so the row and the
// page agree. The words are the ones the rows have always used; the state cell
// draws the role's glyph and carries the word as its tooltip.

// The columns every row and the header share. They are fixed widths so a row
// that is leaving, which the list takes out of flow, keeps its cells where
// they were. State, release, source (from lg), progress, size, buttons.
export const DOWNLOAD_COLUMNS =
  'md:grid-cols-[1.75rem_minmax(0,1fr)_12.5rem_5rem_10rem] lg:grid-cols-[1.75rem_minmax(0,1.3fr)_minmax(0,1fr)_12.5rem_5rem_10rem]';

// Release and artist, in the order the row draws them: the release named
// plainly, the artist beside it in a quieter ink only when there is a release
// to set it apart from. A request with no release names the wanted recording
// instead, and there is nothing left to dim beside it.
export function releaseTitle(item: DownloadRequest) {
  return item.albumTitle ?? item.entryTitle ?? '';
}
export function releaseArtist(item: DownloadRequest) {
  return item.artistName ?? item.entryArtist ?? '';
}
export function rowHeading(item: DownloadRequest) {
  return releaseTitle(item) || releaseArtist(item) || 'Unnamed download';
}
export function rowSubject(item: DownloadRequest) {
  return releaseTitle(item) && releaseArtist(item) ? releaseArtist(item) : '';
}

export function quality(item: DownloadRequest) {
  const format = item.format ? item.format.toUpperCase() : 'Unknown format';
  return item.averageBitRate ? `${format} · ${item.averageBitRate} kbps` : format;
}

// How much of what was asked for this request holds. A single-track request
// holds one track and "1 of 1 tracks" says nothing, so it is said only where
// the two figures can differ.
export function completeness(item: DownloadRequest) {
  if (!item.expectedTrackCount) {
    return item.fileCount === 1 ? '' : `${item.fileCount} files`;
  }
  if (item.expectedTrackCount === 1 && item.fileCount === 1) return '';
  return `${item.fileCount} of ${item.expectedTrackCount} tracks`;
}

// The share of the request's bytes that have arrived, 0 to 100.
export function transferred(item: DownloadRequest) {
  if (!item.totalSizeBytes) return 0;
  return Math.min(100, Math.round((item.progress.transferredBytes / item.totalSizeBytes) * 100));
}

// A started request whose every file is still sitting in the peer's upload
// queue is not downloading. slskd reports `Queued, Remotely` per file, and
// saying so is the difference between "this is slow" and "this is broken".
export function waiting(item: DownloadRequest) {
  return (
    item.status === 'started' &&
    item.progress.transferCount > 0 &&
    item.progress.queuedCount === item.progress.transferCount
  );
}

const statusLabels: Record<DownloadRequest['status'], string> = {
  requested: 'Requested',
  started: 'Downloading',
  completed: 'Transferred',
  failed: 'Failed',
  cancelled: 'Cancelled'
};

export function statusLabel(item: DownloadRequest) {
  if (waiting(item)) return 'Queued at peer';
  if (item.status !== 'completed') return statusLabels[item.status];
  switch (item.importStatus) {
    case 'validating':
      return 'Validating';
    case 'imported':
      return 'Imported';
    case 'needs_review':
      return 'Needs review';
    case 'discarded':
      return progress.discarded;
    default:
      return 'Awaiting import';
  }
}

// The status decides the row's mark, so it is graded once. A completed
// transfer is not yet an outcome: what happened to it afterwards is what the
// row reports.
export function stateRole(item: DownloadRequest): StateRole {
  switch (item.status) {
    case 'failed':
      return 'fail';
    case 'started':
      return 'busy';
    case 'requested':
      return 'idle';
    case 'cancelled':
      return 'idle';
  }
  switch (item.importStatus) {
    case 'imported':
      return 'ok';
    case 'needs_review':
      return 'decide';
    case 'discarded':
      return 'idle';
    default:
      return 'busy';
  }
}

// The mark is graded a step dimmer than the status when the only thing
// happening is a wait: a file in a peer's own queue is not the same fact as
// one moving, though both are `started` underneath.
export function markRole(item: DownloadRequest): StateRole {
  if (item.status === 'started' && waiting(item)) return 'idle';
  return stateRole(item);
}

// Where the row's Decide button goes. Evidence is recorded only while an import
// is paused for review.
export function decideHref(item: DownloadRequest) {
  return item.importEvidence ? `/review?download=${item.id}` : undefined;
}

// How long a request has been waiting, not the clock time it was made at: the
// line is read to decide whether a request is waiting normally or is stuck.
export function requestedOn(value: string) {
  return relativeTime(value);
}

export function subLine(item: DownloadRequest) {
  const parts = [item.username, quality(item), completeness(item)].filter(Boolean);
  if (item.status === 'requested') parts.push(`requested ${requestedOn(item.requestedAt)}`);
  return parts.join(' · ');
}

// The order the open piles are read in: what needs a person first, then what
// can be started or retried, then what is moving, then what only waits. Rows
// of one rank keep the newest request first.
export function nextAction(item: DownloadRequest): number {
  if (item.status === 'completed' && item.importStatus === 'needs_review') return 0;
  if (item.startable) return 1;
  if (item.status === 'failed' && item.retryableCount) return 2;
  if (item.status === 'started' && !waiting(item)) return 3;
  if (item.status === 'completed' && item.importStatus !== 'imported' && item.importStatus !== 'discarded') {
    return 4;
  }
  if (waiting(item) || item.status === 'requested') return 5;
  return 6;
}

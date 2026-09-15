import { request, type Session } from './client';
import type {
  AcquisitionTarget,
  AcquisitionTargets,
  Artist,
  ArtistSearchResult,
  ArtistSearchResults,
  DownloadRequest,
  DownloadRequests,
  DownloadView,
  Me,
  ReviewQueue
} from './types';

/** The wants nobody has to do anything about: the same three states the web's
 * Wants tab reads as one pile. */
export const lookingFor = ['unresolved', 'pending', 'searching'] as const;

const id = encodeURIComponent;

/** Every call the first version makes. Paths are the web's, from
 * web/src/lib/api.ts; the words on the buttons that post to them are the
 * web's as well. */
export function schall(session: Session) {
  return {
    me: () => request<Me>(session, '/api/v1/me'),

    reviewQueue: (limit = 100) => request<ReviewQueue>(session, `/api/v1/review-queue?limit=${limit}&offset=0`),
    acceptCopy: (copyId: string) =>
      request<AcquisitionTarget>(session, `/api/v1/review-queue/copies/${id(copyId)}/accept`, { method: 'POST' }),
    acceptFiledCopy: (targetId: string) =>
      request<AcquisitionTarget>(session, `/api/v1/review-queue/wants/${id(targetId)}/accept-file`, { method: 'POST' }),
    noneOfThese: (targetId: string) =>
      request<AcquisitionTarget>(session, `/api/v1/acquisition-targets/${id(targetId)}/none-of-these`, { method: 'POST' }),
    wrongSong: (targetId: string) =>
      request<AcquisitionTarget>(session, `/api/v1/acquisition-targets/${id(targetId)}/wrong-recording`, { method: 'POST' }),
    chooseRecording: (targetId: string, recordingId: string) =>
      request<AcquisitionTarget>(session, `/api/v1/acquisition-targets/${id(targetId)}/resolution`, {
        method: 'POST',
        body: JSON.stringify({ recordingId })
      }),
    stopLooking: (targetId: string) =>
      request<AcquisitionTarget>(session, `/api/v1/acquisition-targets/${id(targetId)}/not-wanted`, { method: 'POST' }),
    lookAgain: (targetId: string) =>
      request<AcquisitionTarget>(session, `/api/v1/acquisition-targets/${id(targetId)}/not-wanted`, { method: 'DELETE' }),
    wants: (statuses: readonly string[], limit = 100) =>
      request<AcquisitionTargets>(session, `/api/v1/acquisition-targets?status=${statuses.join(',')}&limit=${limit}`),

    downloads: (view: DownloadView, limit = 100) =>
      request<DownloadRequests>(session, `/api/v1/downloads?view=${view}&limit=${limit}`),
    startDownload: (requestId: string) =>
      request<DownloadRequest>(session, `/api/v1/downloads/${id(requestId)}/start`, { method: 'POST' }),
    retryDownload: (requestId: string) =>
      request<DownloadRequest>(session, `/api/v1/downloads/${id(requestId)}/retry`, { method: 'POST' }),
    cancelDownload: (requestId: string) =>
      request<DownloadRequest>(session, `/api/v1/downloads/${id(requestId)}`, { method: 'DELETE' }),
    revalidateDownload: (requestId: string) =>
      request<DownloadRequest>(session, `/api/v1/downloads/${id(requestId)}/revalidate`, { method: 'POST' }),
    resolveImportTrack: (requestId: string, body: { fileName: string; trackId: string }) =>
      request<DownloadRequest>(session, `/api/v1/downloads/${id(requestId)}/resolutions`, {
        method: 'POST',
        body: JSON.stringify(body)
      }),
    withdrawImportResolution: (requestId: string, decisionId: string) =>
      request<DownloadRequest>(session, `/api/v1/downloads/${id(requestId)}/resolutions/${id(decisionId)}`, {
        method: 'DELETE'
      }),

    searchArtists: (query: string) =>
      request<ArtistSearchResults>(session, `/api/v1/search/artists?q=${encodeURIComponent(query)}`),
    followArtist: (artist: ArtistSearchResult) =>
      request<Artist>(session, '/api/v1/artists', {
        method: 'POST',
        body: JSON.stringify({ musicbrainzId: artist.musicbrainzId, name: artist.name, sortName: artist.sortName })
      })
  };
}

export type Schall = ReturnType<typeof schall>;

/** The routes a row's media is fetched from, built from ids the row carries. */
export const media = {
  cover: (albumId: string) => `/api/v1/albums/${id(albumId)}/cover`,
  copyAudio: (copyId: string) => `/api/v1/review-queue/copies/${id(copyId)}/audio`
};

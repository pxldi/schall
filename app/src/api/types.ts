// The web's API types, read straight from the SvelteKit source. They are type
// imports only, so Metro never resolves the path; tsc does, through the
// include list in tsconfig.json.
export type {
  AcquiredCopy,
  AcquiredCopyEvidence,
  AcquisitionCandidate,
  AcquisitionTarget,
  AcquisitionTargets,
  Artist,
  ArtistSearchResult,
  ArtistSearchResults,
  CopyGroup,
  DownloadRequest,
  DownloadRequests,
  DownloadView,
  ImportCandidate,
  ImportDecision,
  ImportEvidence,
  ImportFileEvidence,
  ImportTags,
  Me,
  ReviewItem,
  ReviewQueue
} from '../../../web/src/lib/api-types';

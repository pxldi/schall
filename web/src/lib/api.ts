import type {
  AcquisitionTarget,
  AcquisitionTargets,
  Activity,
  Artist,
  ArtistDetail,
  ArtistFilters,
  ArtistList,
  ArtistSearchResult,
  ArtistSearchResults,
  ArtistWantMissing,
  CancelledJob,
  CancelledKind,
  DashboardSummary,
  DismissedRelease,
  DownloadFilters,
  DownloadRequest,
  DownloadRequests,
  DuplicateEvidence,
  DuplicateRecordings,
  DuplicateResolutionSettings,
  FailedJobs,
  FileResolution,
  GlobalSearchResults,
  IgnoredReleases,
  ImportSettings,
  InboxCleanup,
  JobQueue,
  JobSchedule,
  KeptCopy,
  Label,
  LabelDetail,
  LabelList,
  LabelSearchResult,
  LabelSearchResults,
  LibraryFileFilters,
  LibraryFiles,
  LibraryLayout,
  LibraryLayoutRun,
  LibraryRoot,
  LibraryRoots,
  LibrarySummary,
  LibraryTagRun,
  ListenBrainzSettings,
  ListenBrainzSettingsInput,
  MatchCandidates,
  Me,
  MintedPhone,
  MonitorLevel,
  NavidromeSettings,
  NavidromeSettingsInput,
  NewReleasesOverview,
  NewReleasesSettings,
  NotificationSettings,
  NotificationSettingsInput,
  Overview,
  Phone,
  PlayerPairing,
  Playlist,
  PlaylistEntry,
  PlaylistFilePreview,
  Recommendation,
  RecommendationFeedbackSignal,
  RecommendationList,
  RecommendationPageRequest,
  RecommendationSubject,
  RefreshJob,
  ReleaseCover,
  ReleaseDetail,
  ReleaseEditions,
  ReleaseFilters,
  ReleaseList,
  RematchedReleases,
  RetriedCause,
  RetriedJob,
  RetriedReleases,
  ReviewQueue,
  SlskdSettings,
  SlskdSettingsInput,
  SoundCloudNaming,
  SoundCloudNamingFields,
  SoundCloudTrack,
  SoundCloudUploadNaming,
  SourceCandidate,
  SourceOffer,
  SourcePreferences,
  SourcePreferencesInput,
  SourceResults,
  SourceSearchRun,
  SourceTrack,
  SpotifySettings,
  Storage,
  TrackList,
  TranscodeBitrate,
  TranscodeSweep,
  UnfollowResult,
  UpgradeSettings,
  Upload,
  UploadList,
  WantedArtist,
  WantedRelease,
  WeeklyKeepRead,
  WeeklyLease,
  WeeklyOverview,
  WeeklySettings,
} from './api-types';

export * from './api-types';

interface Problem {
  title?: string;
  status?: number;
  details?: string[];
  duplicates?: DuplicateEvidence;
  sourceChange?: SourceOffer;
}

// SourceChanged is a refusal, not a failure: the peer still offers the folder
// but not what was recorded for it, so starting would ask for names nobody can
// serve. It carries the offer so the caller can show what is gone.
export class SourceChanged extends Error {
  readonly offer: SourceOffer;

  constructor(message: string, offer: SourceOffer) {
    super(message);
    this.name = 'SourceChanged';
    this.offer = offer;
  }
}

// ApiError is what a request throws when it did not come back with what was
// asked for. It carries the status the server answered with, because a screen
// has to tell apart answers that mean different things and lead to different
// next steps: a thing that is not there is not there whoever asks again, while
// a server that answered with a failure may answer properly a minute later.
//
// `status` is 0 when nothing answered at all — nothing listening on the port,
// the connection dropped, the browser offline. `fetch` reports that by
// rejecting, and the words it rejects with are the browser's own.
//
// `title` and `details` are the problem document as it arrived, kept apart from
// `message` rather than folded into it. `message` is one string chosen for a
// caller that only wants one, and the choice loses which half it came from —
// but `$lib/errors` has to look the sentinel up in both, because the server puts
// it in `details` for a validation failure and in `title` everywhere else.
export class ApiError extends Error {
  readonly status: number;
  readonly title?: string;
  readonly details?: string[];

  constructor(
    message: string,
    status: number,
    options?: ErrorOptions & { title?: string; details?: string[] }
  ) {
    super(message, options);
    this.name = 'ApiError';
    this.status = status;
    this.title = options?.title;
    this.details = options?.details;
  }
}

// DuplicateProtection is a refusal, not a failure: Schall will not acquire
// music the library may already hold until someone answers for it. It carries
// the evidence so the caller can show what was found rather than only that
// something was.
export class DuplicateProtection extends Error {
  readonly evidence: DuplicateEvidence;

  constructor(message: string, evidence: DuplicateEvidence) {
    super(message);
    this.name = 'DuplicateProtection';
    this.evidence = evidence;
  }
}

async function request<T>(path: string, init?: RequestInit): Promise<T> {
  let response: Response;
  try {
    response = await fetch(path, {
      ...init,
      headers: {
        Accept: 'application/json',
        // A form sets its own type, and it has to: the boundary between the
        // parts is part of that header and only the browser knows it.
        ...(init?.body && !(init.body instanceof FormData)
          ? { 'Content-Type': 'application/json' }
          : {}),
        ...init?.headers
      }
    });
  } catch (cause) {
    // The request reached no server at all. `Failed to fetch` is what the
    // browser calls that, and it has been shown to people on more than one
    // screen, so the sentence is written here once and the status carries the
    // fact for callers that act on it.
    throw new ApiError('The server did not answer.', 0, { cause });
  }

  if (!response.ok) {
    const problem = (await response.json().catch(() => ({}))) as Problem;
    const message =
      problem.details?.[0] ?? problem.title ?? `Request failed (${response.status})`;
    if (problem.duplicates) {
      throw new DuplicateProtection(message, problem.duplicates);
    }
    if (problem.sourceChange) {
      throw new SourceChanged(problem.title ?? message, problem.sourceChange);
    }
    throw new ApiError(message, response.status, {
      title: problem.title,
      details: problem.details
    });
  }

  // Some answers carry nothing: 204, and the 202 of a button that only asks
  // for work to be queued. Reading those as JSON threw
  // `Unexpected end of JSON input`, and the screen reported a scan that had in
  // fact started as one that could not be started.
  const body = await response.text();
  if (body === '') return undefined as T;
  return JSON.parse(body) as T;
}

export const api = {
  events: () => new EventSource('/api/v1/events'),
  dashboard: () => request<DashboardSummary>('/api/v1/dashboard'),
  overview: (zone: string) =>
    request<Overview>(`/api/v1/overview?tz=${encodeURIComponent(zone)}`),
  artists: (filters: ArtistFilters = {}) => {
    const parameters = new URLSearchParams();
    if (filters.scope) parameters.set('scope', filters.scope);
    if (filters.query) parameters.set('q', filters.query);
    if (filters.completeness) parameters.set('completeness', filters.completeness);
    if (filters.genre) parameters.set('genre', filters.genre);
    if (filters.sort) parameters.set('sort', filters.sort);
    const query = parameters.toString();
    return request<ArtistList>(`/api/v1/artists${query ? `?${query}` : ''}`);
  },
  /** Every genre the catalogue files an artist under, alphabetically. It is
   * what the artists browser offers to filter by, so it names only genres that
   * would find somebody. */
  catalogueGenres: () => request<{ items: string[] }>('/api/v1/artists/genres'),
  artist: (id: string) => request<ArtistDetail>(`/api/v1/artists/${encodeURIComponent(id)}`),
  followHeldArtist: (id: string) =>
    request<Artist>(`/api/v1/artists/${encodeURIComponent(id)}/follow`, { method: 'POST' }),
  unfollowArtist: (id: string) =>
    request<UnfollowResult>(`/api/v1/artists/${encodeURIComponent(id)}/follow`, {
      method: 'DELETE'
    }),
  releases: (filters: ReleaseFilters = {}) => {
    const parameters = new URLSearchParams();
    if (filters.artistId) parameters.set('artistId', filters.artistId);
    if (filters.scope) parameters.set('scope', filters.scope);
    if (filters.query) parameters.set('q', filters.query);
    if (filters.status) parameters.set('status', filters.status);
    if (filters.completeness) parameters.set('completeness', filters.completeness);
    if (filters.sort) parameters.set('sort', filters.sort);
    if (filters.direction) parameters.set('direction', filters.direction);
    if (filters.limit) parameters.set('limit', String(filters.limit));
    if (filters.offset) parameters.set('offset', String(filters.offset));
    const query = parameters.toString();
    return request<ReleaseList>(`/api/v1/albums${query ? `?${query}` : ''}`);
  },
  release: (id: string) => request<ReleaseDetail>(`/api/v1/albums/${encodeURIComponent(id)}`),
  releaseTracks: (id: string) =>
    request<TrackList>(`/api/v1/albums/${encodeURIComponent(id)}/tracks`),
  /** Keeps the picture somebody chose for a release, from a file they picked.
   * The type of the image is read from the bytes by the server, so nothing here
   * has to name it. */
  setReleaseCoverFile: (id: string, file: File) => {
    const body = new FormData();
    body.append('image', file, file.name);
    return request<ReleaseCover>(`/api/v1/albums/${encodeURIComponent(id)}/cover`, {
      method: 'PUT',
      body
    });
  },

  /** The same, from an address. Schall fetches it under the same size bound as
   * every other cover, so the browser never loads the picture itself. */
  setReleaseCoverUrl: (id: string, url: string) =>
    request<ReleaseCover>(`/api/v1/albums/${encodeURIComponent(id)}/cover`, {
      method: 'PUT',
      body: JSON.stringify({ url })
    }),

  /** Takes back a cover somebody set, leaving the release to be asked about
   * again. A cover an archive gave is untouched. */
  removeReleaseCover: (id: string) =>
    request<void>(`/api/v1/albums/${encodeURIComponent(id)}/cover`, { method: 'DELETE' }),

  releaseEditions: (id: string) =>
    request<ReleaseEditions>(`/api/v1/albums/${encodeURIComponent(id)}/editions`),
  wantRelease: (id: string) =>
    request<WantedRelease>(`/api/v1/albums/${encodeURIComponent(id)}/wanted`, {
      method: 'POST'
    }),
  dismissReleaseRemainder: (id: string) =>
    request<DismissedRelease>(`/api/v1/albums/${encodeURIComponent(id)}/not-wanted`, {
      method: 'POST'
    }),
  wantTrack: (trackId: string) =>
    request<AcquisitionTarget>(`/api/v1/tracks/${encodeURIComponent(trackId)}/wanted`, {
      method: 'POST'
    }),
  dismissTrack: (trackId: string) =>
    request<AcquisitionTarget>(`/api/v1/tracks/${encodeURIComponent(trackId)}/not-wanted`, {
      method: 'POST'
    }),
  selectReleaseEdition: (id: string, musicbrainzReleaseId: string) =>
    request<RefreshJob>(`/api/v1/albums/${encodeURIComponent(id)}/edition`, {
      method: 'POST',
      body: JSON.stringify({ musicbrainzReleaseId })
    }),
  library: () => request<LibrarySummary>('/api/v1/library'),
  libraryRoots: () => request<LibraryRoots>('/api/v1/library/roots'),
  libraryFiles: (filters: LibraryFileFilters = {}) => {
    const parameters = new URLSearchParams();
    if (filters.status) parameters.set('status', filters.status);
    if (filters.resolution) parameters.set('resolution', filters.resolution);
    if (filters.query) parameters.set('q', filters.query);
    if (filters.limit) parameters.set('limit', String(filters.limit));
    if (filters.offset) parameters.set('offset', String(filters.offset));
    if (filters.id) parameters.set('id', filters.id);
    if (filters.setAside !== undefined) parameters.set('setAside', String(filters.setAside));
    if (filters.unidentified) parameters.set('unidentified', 'true');
    const query = parameters.toString();
    return request<LibraryFiles>(`/api/v1/library/files${query ? `?${query}` : ''}`);
  },
  matchCandidates: (fileId: string) =>
    request<MatchCandidates>(`/api/v1/library/files/${encodeURIComponent(fileId)}/matches`),
  searchTracks: (fileId: string, query: string) =>
    request<MatchCandidates>(
      `/api/v1/library/files/${encodeURIComponent(fileId)}/tracks?q=${encodeURIComponent(query)}`
    ),
  setManualMatch: (fileId: string, trackId: string, reassign = false) =>
    request<void>(`/api/v1/library/files/${encodeURIComponent(fileId)}/match`, {
      method: 'POST',
      body: JSON.stringify({ trackId, reassign })
    }),
  clearManualMatch: (fileId: string) =>
    request<void>(`/api/v1/library/files/${encodeURIComponent(fileId)}/match`, {
      method: 'DELETE'
    }),
  setAsideLibraryFile: (fileId: string) =>
    request<void>(`/api/v1/library/files/${encodeURIComponent(fileId)}/set-aside`, {
      method: 'POST'
    }),
  clearLibraryFileSetAside: (fileId: string) =>
    request<void>(`/api/v1/library/files/${encodeURIComponent(fileId)}/set-aside`, {
      method: 'DELETE'
    }),
  reconcileLibraryFile: (fileId: string) =>
    request<void>(`/api/v1/library/files/${encodeURIComponent(fileId)}/reconcile`, {
      method: 'POST'
    }),
  /** Deletes the file, from the library and from the disc. Nothing undoes it. */
  deleteLibraryFile: (fileId: string) =>
    request<void>(`/api/v1/library/files/${encodeURIComponent(fileId)}`, { method: 'DELETE' }),
  /** Says a second copy is meant to be there. Touches no music, and is undone
   * by askAboutDuplicateAgain. */
  keepDuplicate: (fileId: string) =>
    request<void>(`/api/v1/library/files/${encodeURIComponent(fileId)}/keep`, { method: 'POST' }),
  askAboutDuplicateAgain: (fileId: string) =>
    request<void>(`/api/v1/library/files/${encodeURIComponent(fileId)}/keep`, { method: 'DELETE' }),
  /** Where the library holds the same recording more than once. `kept` asks for
   * the pairs somebody said they meant to have instead. */
  duplicateRecordings: (kept = false, limit?: number, offset?: number) => {
    const parameters = new URLSearchParams();
    if (kept) parameters.set('kept', 'true');
    if (limit) parameters.set('limit', String(limit));
    if (offset) parameters.set('offset', String(offset));
    const query = parameters.toString();
    return request<DuplicateRecordings>(`/api/v1/library/duplicates${query ? `?${query}` : ''}`);
  },
  /** Says every copy of this recording is meant to be there. Moves no file, and
   * is undone by askAboutRecordingAgain. */
  keepRecordingCopies: (recordingId: string) =>
    request<void>(`/api/v1/library/duplicates/${encodeURIComponent(recordingId)}/keep`, {
      method: 'POST'
    }),
  askAboutRecordingAgain: (recordingId: string) =>
    request<void>(`/api/v1/library/duplicates/${encodeURIComponent(recordingId)}/keep`, {
      method: 'DELETE'
    }),
  // What SoundCloud says about one track. Asking changes nothing: it is what
  // the reader looks at before confirming.
  soundCloudTrack: (url: string) =>
    request<SoundCloudTrack>(`/api/v1/soundcloud/track?url=${encodeURIComponent(url)}`),
  // Records that a file in the library is the track at that address. Permanent.
  nameFromSoundCloud: (fileId: string, url: string, named: SoundCloudNamingFields) =>
    request<SoundCloudNaming>(
      `/api/v1/library/files/${encodeURIComponent(fileId)}/soundcloud`,
      { method: 'POST', body: JSON.stringify({ url, ...named }) }
    ),
  libraryFileCoverUrl: (fileId: string) =>
    `/api/v1/library/files/${encodeURIComponent(fileId)}/cover`,
  /** Keeps one copy of a recording and deletes the others, from the library and
   * from the disc. Nothing undoes it. `deleting` is the copies the screen showed
   * as going: the server refuses when the library no longer agrees with it. */
  keepOneCopy: (recordingId: string, fileId: string, deleting: string[]) =>
    request<KeptCopy>(`/api/v1/library/duplicates/${encodeURIComponent(recordingId)}/keep-one`, {
      method: 'POST',
      body: JSON.stringify({ fileId, deleting })
    }),
  /** Tries again on the files the library let go of and could not delete. */
  retryRemovals: () =>
    request<{ deleted: number }>('/api/v1/library/removals/retry', { method: 'POST' }),
  fileIdentity: (fileId: string) =>
    request<FileResolution>(`/api/v1/library/files/${encodeURIComponent(fileId)}/identity`),
  acceptIdentity: (fileId: string, recordingId: string) =>
    request<FileResolution>(`/api/v1/library/files/${encodeURIComponent(fileId)}/identity`, {
      method: 'POST',
      body: JSON.stringify({ recordingId })
    }),
  markLocalOnly: (fileId: string) =>
    request<FileResolution>(`/api/v1/library/files/${encodeURIComponent(fileId)}/identity`, {
      method: 'POST',
      body: JSON.stringify({ localOnly: true })
    }),
  clearIdentity: (fileId: string) =>
    request<FileResolution>(`/api/v1/library/files/${encodeURIComponent(fileId)}/identity`, {
      method: 'DELETE'
    }),
  resolveFileIdentity: (fileId: string) =>
    request<RefreshJob>(`/api/v1/library/files/${encodeURIComponent(fileId)}/resolve`, {
      method: 'POST'
    }),

  // Where a file the library holds is played from. Not a fetch, for the same
  // reason a held copy is not: it is set as an <audio> src so the browser can
  // ask for the piece it needs and the scrubber works.
  libraryFileAudioUrl: (fileId: string) =>
    `/api/v1/library/files/${encodeURIComponent(fileId)}/audio`,
  addLibraryRoot: (path: string) =>
    request<LibraryRoot>('/api/v1/library/roots', {
      method: 'POST',
      body: JSON.stringify({ path })
    }),
  scanLibrary: () =>
    request<RefreshJob>('/api/v1/library/scan', {
      method: 'POST'
    }),
  searchArtists: (query: string) =>
    request<ArtistSearchResults>(`/api/v1/search/artists?q=${encodeURIComponent(query)}`),
  /** Searches everything Schall holds. An empty query is answered with empty
   * groups without the collection being read, so it is safe to call while a
   * box is still being typed into. Not to be confused with searchArtists,
   * which asks MusicBrainz about artists Schall does not hold yet. */
  search: (query: string, limit?: number) => {
    const parameters = new URLSearchParams({ q: query });
    if (limit) parameters.set('limit', String(limit));
    return request<GlobalSearchResults>(`/api/v1/search?${parameters.toString()}`);
  },
  followArtist: (artist: ArtistSearchResult) =>
    request<Artist>('/api/v1/artists', {
      method: 'POST',
      body: JSON.stringify({
        musicbrainzId: artist.musicbrainzId,
        name: artist.name,
        sortName: artist.sortName
      })
    }),
  setArtistMonitorLevel: (id: string, level: MonitorLevel) =>
    request<{ artistId: string; monitorLevel: MonitorLevel }>(
      `/api/v1/artists/${encodeURIComponent(id)}/monitor`,
      { method: 'PUT', body: JSON.stringify({ level }) }
    ),
  refreshArtist: (id: string) =>
    request<RefreshJob>(`/api/v1/artists/${encodeURIComponent(id)}/refresh`, {
      method: 'POST'
    }),
  wantArtist: (id: string) =>
    request<WantedArtist>(`/api/v1/artists/${encodeURIComponent(id)}/wanted`, {
      method: 'POST'
    }),
  /** Turns the standing "want what's missing" on or off. On, it wants what is
   * missing now and keeps wanting as tracklists arrive; off, it stops future
   * wanting and leaves every want it made. */
  setArtistWantMissing: (id: string, on: boolean) =>
    request<ArtistWantMissing>(`/api/v1/artists/${encodeURIComponent(id)}/want-missing`, {
      method: 'PUT',
      body: JSON.stringify({ on })
    }),
  searchLabels: (query: string) =>
    request<LabelSearchResults>(`/api/v1/search/labels?q=${encodeURIComponent(query)}`),
  labels: () => request<LabelList>('/api/v1/labels'),
  label: (id: string) => request<LabelDetail>(`/api/v1/labels/${encodeURIComponent(id)}`),
  followLabel: (label: LabelSearchResult) =>
    request<Label>('/api/v1/labels', {
      method: 'POST',
      body: JSON.stringify({
        musicbrainzId: label.musicbrainzId,
        name: label.name,
        type: label.type ?? '',
        country: label.country ?? '',
        disambiguation: label.disambiguation ?? ''
      })
    }),
  unfollowLabel: (id: string) =>
    request<Label>(`/api/v1/labels/${encodeURIComponent(id)}/follow`, { method: 'DELETE' }),
  setLabelMonitorLevel: (id: string, level: MonitorLevel) =>
    request<{ labelId: string; monitorLevel: MonitorLevel }>(
      `/api/v1/labels/${encodeURIComponent(id)}/monitor`,
      { method: 'PUT', body: JSON.stringify({ level }) }
    ),
  refreshLabel: (id: string) =>
    request<RefreshJob>(`/api/v1/labels/${encodeURIComponent(id)}/refresh`, {
      method: 'POST'
    }),
  spotifySettings: () => request<SpotifySettings>('/api/v1/settings/spotify'),
  // An empty clientSecret keeps the stored secret; it is never sent back.
  saveSpotifySettings: (clientId: string, clientSecret: string) =>
    request<SpotifySettings>('/api/v1/settings/spotify', {
      method: 'PUT',
      body: JSON.stringify({ clientId, clientSecret })
    }),
  authorizeSpotify: () =>
    request<{ url: string }>('/api/v1/settings/spotify/authorize', { method: 'POST' }),
  playlists: () => request<{ items: Playlist[] }>('/api/v1/playlists'),
  playlist: (id: string) =>
    request<{ playlist: Playlist; entries: PlaylistEntry[] }>(
      `/api/v1/playlists/${encodeURIComponent(id)}`
    ),
  followPlaylist: (url: string) =>
    request<Playlist>('/api/v1/playlists', { method: 'POST', body: JSON.stringify({ url }) }),
  importPlaylist: (id: string) =>
    request<Playlist>(`/api/v1/playlists/${encodeURIComponent(id)}/import`, { method: 'POST' }),
  // The door for an entry an import never asks about on its own — every list
  // adopted as a file, today. A second press on an entry that already has a
  // want changes nothing.
  wantPlaylistEntry: (playlistId: string, entryId: string) =>
    request<{ status: string }>(
      `/api/v1/playlists/${encodeURIComponent(playlistId)}/entries/${encodeURIComponent(entryId)}/wanted`,
      { method: 'POST' }
    ),
  // Reads a CSV or M3U somebody chose and says what it holds. Nothing is
  // created: this is what a preview shows before the person confirms it.
  previewPlaylistFile: (file: File) => {
    const body = new FormData();
    body.append('file', file, file.name);
    return request<PlaylistFilePreview>('/api/v1/playlists/file/preview', {
      method: 'POST',
      body
    });
  },
  // Imports the file itself, not the preview: the server reads the bytes
  // again, so what becomes a playlist is what the file says and nothing a
  // browser reported about it. `name` is the list's name if the person
  // changed it from the file's own name.
  importPlaylistFile: (file: File, name: string) => {
    const body = new FormData();
    body.append('file', file, file.name);
    if (name.trim()) body.append('name', name.trim());
    return request<Playlist>('/api/v1/playlists/file', { method: 'POST', body });
  },
  deletePlaylist: (id: string) =>
    request<void>(`/api/v1/playlists/${encodeURIComponent(id)}`, { method: 'DELETE' }),
  // Both sync calls queue a job and answer at once: a pass talks to the player,
  // so what it did arrives through the playlist itself rather than here.
  syncPlaylistToNavidrome: (id: string) =>
    request<{ status: string }>(`/api/v1/playlists/${encodeURIComponent(id)}/navidrome-sync`, {
      method: 'POST'
    }),
  syncNavidromePlaylists: () =>
    request<{ status: string }>('/api/v1/navidrome/sync', { method: 'POST' }),
  // Asked for rather than loaded with the page: it talks to the player once per
  // file Schall holds, which is a conversation to start on purpose.
  playerPairing: (id: string) =>
    request<PlayerPairing>(`/api/v1/playlists/${encodeURIComponent(id)}/navidrome-pairing`),
  slskdSettings: () => request<SlskdSettings>('/api/v1/settings/slskd'),
  saveSlskdSettings: (settings: SlskdSettingsInput) =>
    request<SlskdSettings>('/api/v1/settings/slskd', {
      method: 'PUT',
      body: JSON.stringify(settings)
    }),
  testSlskdConnection: () =>
    request<SlskdSettings>('/api/v1/settings/slskd/test', { method: 'POST' }),
  navidromeSettings: () => request<NavidromeSettings>('/api/v1/settings/navidrome'),
  // An empty password keeps the stored one; it is never sent back.
  saveNavidromeSettings: (settings: NavidromeSettingsInput) =>
    request<NavidromeSettings>('/api/v1/settings/navidrome', {
      method: 'PUT',
      body: JSON.stringify(settings)
    }),
  testNavidromeConnection: () =>
    request<NavidromeSettings>('/api/v1/settings/navidrome/test', { method: 'POST' }),
  // Tells the player right now rather than waiting for whatever change queues
  // it next, and answers with the moment the telling landed.
  rescanNavidrome: () =>
    request<NavidromeSettings>('/api/v1/settings/navidrome/rescan', { method: 'POST' }),
  listenBrainzSettings: () => request<ListenBrainzSettings>('/api/v1/settings/listenbrainz'),
  // An empty token keeps the stored one; clearUserToken is how it is removed.
  saveListenBrainzSettings: (settings: ListenBrainzSettingsInput) =>
    request<ListenBrainzSettings>('/api/v1/settings/listenbrainz', {
      method: 'PUT',
      body: JSON.stringify(settings)
    }),
  testListenBrainzConnection: () =>
    request<ListenBrainzSettings>('/api/v1/settings/listenbrainz/test', { method: 'POST' }),

  // What to prefer among the copies peers are sharing, and what never to fetch.
  // These decide only what gets tried: the audio still decides what is admitted.
  storage: () => request<Storage>('/api/v1/library/storage'),

  activity: () => request<Activity>('/api/v1/what-happened'),

  sourcePreferences: () => request<SourcePreferences>('/api/v1/settings/source-preferences'),
  saveSourcePreferences: (preferences: SourcePreferencesInput) =>
    request<SourcePreferences>('/api/v1/settings/source-preferences', {
      method: 'PUT',
      body: JSON.stringify(preferences)
    }),

  // The sweep that looks back at files acquired before the floor above was
  // set, or before it was raised, and asks the ordinary acquisition loop for a
  // better copy of each one.
  upgradeSettings: () => request<UpgradeSettings>('/api/v1/settings/upgrade'),
  saveUpgradeSettings: (enabled: boolean) =>
    request<UpgradeSettings>('/api/v1/settings/upgrade', {
      method: 'PUT',
      body: JSON.stringify({ enabled })
    }),
  scanForUpgrades: () => request<void>('/api/v1/settings/upgrade/scan', { method: 'POST' }),

  // No scan route: saving with `enabled: true` queues the pass itself, server
  // side, the way turning the upgrade sweep on schedules a look right away.
  duplicateResolutionSettings: () =>
    request<DuplicateResolutionSettings>('/api/v1/settings/duplicates'),
  saveDuplicateResolutionSettings: (enabled: boolean) =>
    request<DuplicateResolutionSettings>('/api/v1/settings/duplicates', {
      method: 'PUT',
      body: JSON.stringify({ enabled })
    }),

  me: () => request<Me>('/api/v1/me'),

  // The phones that hold a token. Minting answers with the plain token once;
  // asking again only ever returns the row without it.
  phones: () => request<{ items: Phone[] }>('/api/v1/settings/phones'),
  addPhone: (name: string) =>
    request<MintedPhone>('/api/v1/settings/phones', {
      method: 'POST',
      body: JSON.stringify({ name })
    }),
  removePhone: (id: string) =>
    request<void>(`/api/v1/settings/phones/${encodeURIComponent(id)}`, { method: 'DELETE' }),

  notificationSettings: () => request<NotificationSettings>('/api/v1/settings/notifications'),
  // An empty token keeps the stored one; clearToken is how it is removed.
  saveNotificationSettings: (settings: NotificationSettingsInput) =>
    request<NotificationSettings>('/api/v1/settings/notifications', {
      method: 'PUT',
      body: JSON.stringify(settings)
    }),
  // Sends a real message to the saved address, because an address is only proved
  // by the thing that will use it.
  testNotification: () =>
    request<NotificationSettings>('/api/v1/settings/notifications/test', { method: 'POST' }),

  // What that account produced. The five suppression rules are applied as the
  // list is read, so a follow, a request, a dismissal or a new file in the
  // library takes effect here without waiting for the next sweep.
  //
  // A page is asked for one of three ways, and only one at a time. `after` and
  // `before` name the rank of a suggestion the reader was just shown, and are
  // what Previous and Next send: reading a page can take rows out of the list,
  // and a position would then move under the reader. `offset` addresses the two
  // ends of the list, which stay exact whatever the rules have removed.
  recommendations: (limit = 25, page: RecommendationPageRequest = {}) => {
    const asked = new URLSearchParams({ limit: String(limit) });
    if (page.after !== undefined) asked.set('after', String(page.after));
    else if (page.before !== undefined) asked.set('before', String(page.before));
    else asked.set('offset', String(page.offset ?? 0));
    return request<RecommendationList>(`/api/v1/recommendations?${asked}`);
  },

  // The reader saying they are not interested. Permanent at the level named,
  // and written to the recommendation store alone: it is a statement about
  // taste, and nothing here reaches a decision about a file's identity.
  dismissRecommendation: (subject: RecommendationSubject, musicBrainzId: string) =>
    request<{ subject: string; musicBrainzId: string; dismissedAt: string | null }>(
      '/api/v1/recommendations/dismissals',
      { method: 'POST', body: JSON.stringify({ subject, musicBrainzId }) }
    ),

  recordRecommendationFeedback: (recordingId: string, signal: RecommendationFeedbackSignal) =>
    request<{ recordingId: string; signal: RecommendationFeedbackSignal; createdAt: string }>(
      '/api/v1/recommendations/feedback',
      { method: 'POST', body: JSON.stringify({ recordingId, signal }) }
    ),

  clearRecommendationFeedback: () =>
    request<void>('/api/v1/recommendations/feedback', { method: 'DELETE' }),

  // What the reader was actually shown. Three showings a day or more apart with
  // no decision hold a recording back for ninety days, so this is sent by the
  // page that displayed the list rather than by the read that fetched it.
  recordRecommendationImpressions: (recordingIds: string[]) =>
    request<{ recorded: number }>('/api/v1/recommendations/impressions', {
      method: 'POST',
      body: JSON.stringify({ recordingIds })
    }),

  // The reader wanting a suggestion. This is the ordinary want-creation
  // endpoint, the one a release or a playlist uses; `origin` only records where
  // the want came from, and nothing in the acquisition loop branches on it.
  // Asking twice for one recording is one want, so the answer to the second
  // press is the want that already exists rather than a second one.
  wantRecommendation: (recommendation: Recommendation) =>
    request<AcquisitionTarget>('/api/v1/acquisition-targets', {
      method: 'POST',
      body: JSON.stringify({
        origin: 'recommendation',
        artist: recommendation.artistName,
        title: recommendation.recordingTitle,
        album: recommendation.releaseTitle,
        recordingId: recommendation.recordingId,
        releaseGroupId: recommendation.releaseGroupId
      })
    }),

  /** The weekly playlist in one read: how it is set up, what is on trial now,
   * and what the recent refreshes did. */
  weekly: () => request<WeeklyOverview>('/api/v1/weekly'),

  /** How the weekly playlist runs. Saving it with `enabled` on asks for a
   * refresh straight away, so switching it on does something visible instead of
   * waiting a week. */
  saveWeeklySettings: (settings: WeeklySettings) =>
    request<WeeklySettings>('/api/v1/weekly/settings', {
      method: 'PUT',
      body: JSON.stringify(settings)
    }),

  /** Keep this song. It works on a song already announced for removal, which is
   * what the week of notice is for. */
  keepWeeklySong: (fileId: string) =>
    request<WeeklyLease>(`/api/v1/weekly/songs/${encodeURIComponent(fileId)}/keep`, {
      method: 'POST'
    }),

  /** What was read about one song, and what each answer said. */
  weeklyLeaseReads: (leaseId: string) =>
    request<{ items: WeeklyKeepRead[] }>(
      `/api/v1/weekly/leases/${encodeURIComponent(leaseId)}/reads`
    ),

  /** The new-releases playlist in one read: how it is set up, and which list
   * to open. `playlistId` is null until the first refresh has made one. */
  newReleases: () => request<NewReleasesOverview>('/api/v1/new-releases'),

  /** How the new-releases playlist is kept: on or off, and how many days back
   * a release may have been published and still be on it. */
  saveNewReleasesSettings: (settings: NewReleasesSettings) =>
    request<NewReleasesSettings>('/api/v1/new-releases/settings', {
      method: 'PUT',
      body: JSON.stringify(settings)
    }),

  libraryLayout: () => request<LibraryLayout>('/api/v1/settings/library-layout'),
  saveLibraryLayout: (template: string) =>
    request<LibraryLayout>('/api/v1/settings/library-layout', {
      method: 'PUT',
      body: JSON.stringify({ template })
    }),
  // The most recent migration, or nothing at all when none has ever been
  // planned. Nothing is not an error: a library that was never migrated is the
  // ordinary state of one.
  // `null` and not `undefined`, because the query cache reads undefined as a
  // query that returned nothing by mistake and logs an error for it on every
  // load of the settings screen. Saying "there is no run" out loud is the whole
  // point of the sentence above.
  libraryLayoutRun: async () =>
    (await request<LibraryLayoutRun | null>('/api/v1/library/layout/moves')) ?? null,
  // Planning touches nothing. Reading it is the point: a migration is agreed to
  // before it happens, not explained afterwards.
  planLibraryLayoutRun: () =>
    request<LibraryLayoutRun>('/api/v1/library/layout/moves', { method: 'POST' }),
  applyLibraryLayoutRun: (runId: string) =>
    request<LibraryLayoutRun>(
      `/api/v1/library/layout/moves/${encodeURIComponent(runId)}/apply`,
      { method: 'POST' }
    ),
  // Drops what a run planned and never did, which is what frees the files for a
  // plan somebody would rather have.
  discardLibraryLayoutRun: (runId: string) =>
    request<LibraryLayoutRun | undefined>(
      `/api/v1/library/layout/moves/${encodeURIComponent(runId)}`,
      { method: 'DELETE' }
    ),
  // The most recent pass over the tags in the library's own files, or an idle
  // one where none has ever been asked for.
  libraryTags: () => request<LibraryTagRun>('/api/v1/library/tags'),
  writeLibraryTags: () => request<RefreshJob>('/api/v1/library/tags', { method: 'POST' }),
  importSettings: () => request<ImportSettings>('/api/v1/settings/imports'),
  // An empty acoustidApiKey leaves the stored key alone, so saving any other
  // setting cannot erase a key the interface was never shown. transcode is
  // saved the same way: leaving it out saves the non-destructive default
  // (off), so a caller that only means to change the retention policy or the
  // acoustic check must pass the currently stored transcode policy through
  // rather than omit it.
  saveImportSettings: (
    sourceRetention: ImportSettings['sourceRetention'],
    acoustid: { apiKey?: string; enabled?: boolean } = {},
    transcode: {
      enabled?: boolean;
      target?: ImportSettings['transcodeTarget'];
      bitrate?: TranscodeBitrate;
      when?: ImportSettings['transcodeWhen'];
    } = {}
  ) =>
    request<ImportSettings>('/api/v1/settings/imports', {
      method: 'PUT',
      body: JSON.stringify({
        sourceRetention,
        acoustidApiKey: acoustid.apiKey ?? '',
        acoustidEnabled: acoustid.enabled ?? false,
        transcodeEnabled: transcode.enabled ?? false,
        transcodeTarget: transcode.target ?? '',
        transcodeBitrate: transcode.bitrate ?? '',
        transcodeWhen: transcode.when ?? ''
      })
    }),
  // The most recent passes over the download inbox, newest first.
  inboxCleanups: () => request<{ items: InboxCleanup[] }>('/api/v1/inbox/cleanups'),
  // A dry run counts what can go and deletes nothing. A real one deletes what
  // the same rule chose, and every file it looked at is on record either way.
  cleanInbox: (dryRun: boolean) =>
    request<InboxCleanup>('/api/v1/inbox/cleanups', {
      method: 'POST',
      body: JSON.stringify({ dryRun })
    }),
  // The most recent pass shrinking the files already in the library, or an
  // idle one where none has ever been asked for.
  transcodeSweep: () => request<TranscodeSweep>('/api/v1/library/transcode'),
  queueTranscodeSweep: () =>
    request<RefreshJob>('/api/v1/library/transcode', { method: 'POST' }),
  releaseSources: (id: string, query?: string) =>
    request<SourceResults>(
      `/api/v1/albums/${encodeURIComponent(id)}/sources${query ? `?q=${encodeURIComponent(query)}` : ''}`
    ),
  // Searching several releases at once is a saving in waiting, not in judging:
  // it records candidates to look at and chooses none of them. Every download
  // that follows still goes through requestDownload, one release at a time.
  // autoRequest lets the run record the download itself for releases whose best
  // candidate has every catalogue track confirmed by name and length. It is
  // asked per run rather than stored as a setting: permission given by somebody
  // about to look at these results, not a switch left on and forgotten.
  createSourceSearch: (albumIds: string[], autoRequest = false) =>
    request<SourceSearchRun>('/api/v1/source-searches', {
      method: 'POST',
      body: JSON.stringify({ albumIds, autoRequest })
    }),
  sourceSearch: (runId: string) =>
    request<SourceSearchRun>(`/api/v1/source-searches/${encodeURIComponent(runId)}`),

  // The three decisions a selection of releases can be given at once. Each one
  // answers per release rather than with a bare success, because a selection of
  // six is six outcomes: four ignored and two already ignored is the true
  // sentence, and "6 releases ignored" is not.
  ignoreReleases: (releaseIds: string[]) =>
    request<IgnoredReleases>('/api/v1/releases/ignore', {
      method: 'POST',
      body: JSON.stringify({ releaseIds })
    }),
  rematchReleases: (releaseIds: string[]) =>
    request<RematchedReleases>('/api/v1/releases/rematch', {
      method: 'POST',
      body: JSON.stringify({ releaseIds })
    }),
  retryReleases: (releaseIds: string[]) =>
    request<RetriedReleases>('/api/v1/releases/retry', {
      method: 'POST',
      body: JSON.stringify({ releaseIds })
    }),
  // Stops the releases of a run that have not been searched yet and leaves the
  // ones it has answered exactly as they are. A release already searched is a
  // result somebody may act on, so cancelling never withdraws one; a run with
  // nothing left waiting is refused rather than reported as cancelled.
  cancelSourceSearch: (runId: string) =>
    request<SourceSearchRun>(`/api/v1/source-searches/${encodeURIComponent(runId)}/cancel`, {
      method: 'POST'
    }),
  // The list asks for the pile it is about to show. Asking for everything and
  // sieving the answer in the browser is how twenty open transfers became four
  // on screen: the answer was the hundred most recent requests, and sixteen
  // open ones were older than that.
  downloads: (filters: DownloadFilters = {}) => {
    const parameters = new URLSearchParams();
    if (filters.view) parameters.set('view', filters.view);
    if (filters.limit) parameters.set('limit', String(filters.limit));
    if (filters.offset) parameters.set('offset', String(filters.offset));
    const query = parameters.toString();
    return request<DownloadRequests>(`/api/v1/downloads${query ? `?${query}` : ''}`);
  },
  releaseDownloads: (id: string) =>
    request<DownloadRequests>(`/api/v1/albums/${encodeURIComponent(id)}/downloads`),
  // What the library already holds against a release, readable before the
  // download button is pressed rather than only in the refusal afterwards.
  releaseDuplicates: (id: string) =>
    request<DuplicateEvidence>(`/api/v1/albums/${encodeURIComponent(id)}/duplicates`),
  // Requesting a download records the chosen source against the release. It
  // starts no transfer; starting is a separate call.
  //
  // A release the library may already hold is refused with DuplicateProtection
  // until acknowledgeDuplicates says the question was answered.
  requestDownload: (id: string, candidate: SourceCandidate, acknowledgeDuplicates = false) =>
    request<DownloadRequest>(`/api/v1/albums/${encodeURIComponent(id)}/downloads`, {
      method: 'POST',
      body: JSON.stringify({
        acknowledgeDuplicates,
        provider: candidate.provider,
        username: candidate.username,
        directory: candidate.directory,
        format: candidate.format,
        averageBitRate: candidate.averageBitRate ?? 0,
        score: candidate.score,
        reasons: candidate.reasons,
        files: candidate.files.map((file) => ({
          path: file.path,
          name: file.name,
          extension: file.extension,
          sizeBytes: file.sizeBytes,
          bitRate: file.bitRate ?? 0,
          durationSeconds: file.durationSeconds ?? 0
        }))
      })
    }),
  // Starting is the only call in Schall that transfers anything, and it only
  // acts on a request that was already recorded. The library moves between
  // recording and starting, so duplicate protection is checked here too.
  // The peer's folder is re-checked here too, because a shared folder is not a
  // promise: between recording a decision and acting on it the peer may have
  // renamed, re-ripped, or stopped sharing the music.
  startDownload: (requestId: string, acknowledgeDuplicates = false, acknowledgeSourceChange = false) => {
    const body: Record<string, boolean> = {};
    if (acknowledgeDuplicates) body.acknowledgeDuplicates = true;
    if (acknowledgeSourceChange) body.acknowledgeSourceChange = true;
    return request<DownloadRequest>(`/api/v1/downloads/${encodeURIComponent(requestId)}/start`, {
      method: 'POST',
      ...(Object.keys(body).length ? { body: JSON.stringify(body) } : {})
    });
  },
  // What the peer offers for a recorded folder now, without starting anything.
  downloadSource: (requestId: string) =>
    request<SourceOffer>(`/api/v1/downloads/${encodeURIComponent(requestId)}/source`),
  // Retrying asks the peer only for the files that did not arrive. Passing no
  // paths retries every failed file; the files that transferred are left alone.
  retryDownload: (requestId: string, paths?: string[]) =>
    request<DownloadRequest>(`/api/v1/downloads/${encodeURIComponent(requestId)}/retry`, {
      method: 'POST',
      ...(paths?.length ? { body: JSON.stringify({ paths }) } : {})
    }),
  // The reply goes to the peer this request was made to, and to nobody else:
  // the server checks the name against the request before it sends anything.
  replyToPeer: (requestId: string, body: { username: string; message: string }) =>
    request<DownloadRequest>(`/api/v1/downloads/${encodeURIComponent(requestId)}/peer-reply`, {
      method: 'POST',
      body: JSON.stringify(body)
    }),
  cancelDownload: (requestId: string) =>
    request<DownloadRequest>(`/api/v1/downloads/${encodeURIComponent(requestId)}`, {
      method: 'DELETE'
    }),
  // Retrying runs the same validation again. It cannot accept files validation
  // rejected, so an import that is still uncertain simply pauses once more.
  resolveImportTrack: (requestId: string, body: { fileName: string; trackId: string; note?: string }) =>
    request<DownloadRequest>(`/api/v1/downloads/${encodeURIComponent(requestId)}/resolutions`, {
      method: 'POST',
      body: JSON.stringify(body)
    }),

  withdrawImportResolution: (requestId: string, decisionId: string) =>
    request<DownloadRequest>(
      `/api/v1/downloads/${encodeURIComponent(requestId)}/resolutions/${encodeURIComponent(decisionId)}`,
      { method: 'DELETE' }
    ),

  // The wants holding a copy nothing could decide about. Oldest question first:
  // the queue is worked through rather than browsed, and a held copy is a want
  // that has stopped until somebody looks at it.
  reviewQueue: (limit = 20, offset = 0) =>
    request<ReviewQueue>(`/api/v1/review-queue?limit=${limit}&offset=${offset}`),

  // Where a held copy is played from. Not a fetch: it is set as an <audio> src
  // so the browser can issue range requests and the scrubber works.
  reviewCopyAudioUrl: (copyId: string) =>
    `/api/v1/review-queue/copies/${encodeURIComponent(copyId)}/audio`,

  // The bars a copy's waveform draws, read once per copy. Absent (404) for a
  // copy nothing has generated one for yet; the card falls back to a flat line
  // rather than treating that as an error.
  reviewCopyWaveform: (copyId: string) =>
    request<{ peaks: number[] }>(
      `/api/v1/review-queue/copies/${encodeURIComponent(copyId)}/waveform`
    ),

  // The four persisted review outcomes. Each is permanent; skipping is the only
  // one that is not, and it never reaches the server.
  acceptReviewCopy: (copyId: string) =>
    request<{ accepted: boolean }>(
      `/api/v1/review-queue/copies/${encodeURIComponent(copyId)}/accept`,
      { method: 'POST' }
    ),

  // A stopped want's copy is already a library file under a different
  // recording. This is what a person accepts instead: the file stays where it
  // is, filed as the wanted recording, rather than being re-fetched.
  acceptStoppedWant: (targetId: string) =>
    request<{ accepted: boolean }>(
      `/api/v1/review-queue/wants/${encodeURIComponent(targetId)}/accept-file`,
      { method: 'POST' }
    ),

  refuseReviewCopies: (targetId: string) =>
    request<{ refused: boolean }>(
      `/api/v1/acquisition-targets/${encodeURIComponent(targetId)}/none-of-these`,
      { method: 'POST' }
    ),

  // The wants themselves, in the states asked for. The screen that shows the
  // ones being looked for asks for three states in one request, so that the
  // figure beside the list and the list under it are one answer.
  wants: (filters: { statuses?: readonly string[]; limit?: number; offset?: number } = {}) => {
    const parameters = new URLSearchParams();
    if (filters.statuses?.length) parameters.set('status', filters.statuses.join(','));
    if (filters.limit) parameters.set('limit', String(filters.limit));
    if (filters.offset) parameters.set('offset', String(filters.offset));
    const query = parameters.toString();
    return request<AcquisitionTargets>(`/api/v1/acquisition-targets${query ? `?${query}` : ''}`);
  },

  lookupSourceTrack: (url: string) =>
    request<SourceTrack>(`/api/v1/source-tracks?url=${encodeURIComponent(url)}`),

  keyTargetToSource: (targetId: string, body: { url: string; externalId: string }) =>
    request<AcquisitionTarget>(
      `/api/v1/acquisition-targets/${encodeURIComponent(targetId)}/source`,
      { method: 'POST', body: JSON.stringify(body) }
    ),

  setMinimumBitrate: (targetId: string, minimumBitrate: number) =>
    request<AcquisitionTarget>(
      `/api/v1/acquisition-targets/${encodeURIComponent(targetId)}/minimum-bitrate`,
      { method: 'PUT', body: JSON.stringify({ minimumBitrate }) }
    ),

  stopPursuingTarget: (targetId: string) =>
    request<AcquisitionTarget>(
      `/api/v1/acquisition-targets/${encodeURIComponent(targetId)}/not-wanted`,
      { method: 'POST' }
    ),

  // Nothing automatic may overturn a decision to stop, but the person who made
  // it may take it back.
  pursueTargetAgain: (targetId: string) =>
    request<AcquisitionTarget>(
      `/api/v1/acquisition-targets/${encodeURIComponent(targetId)}/not-wanted`,
      { method: 'DELETE' }
    ),

  rejectTargetRecording: (targetId: string) =>
    request<AcquisitionTarget>(
      `/api/v1/acquisition-targets/${encodeURIComponent(targetId)}/wrong-recording`,
      { method: 'POST' }
    ),

  takeBestAvailable: (targetId: string) =>
    request<AcquisitionTarget>(
      `/api/v1/acquisition-targets/${encodeURIComponent(targetId)}/take-best-available`,
      { method: 'POST' }
    ),

  keepAcquisitionFloor: (targetId: string) =>
    request<AcquisitionTarget>(
      `/api/v1/acquisition-targets/${encodeURIComponent(targetId)}/take-best-available`,
      { method: 'DELETE' }
    ),

  // The answer to the other question: which of the recordings this entry could
  // name is the one it does. Only a recording the want offered is accepted, so
  // this is always a choice between the candidates the queue showed.
  chooseTargetRecording: (targetId: string, recordingId: string) =>
    request<AcquisitionTarget>(
      `/api/v1/acquisition-targets/${encodeURIComponent(targetId)}/resolution`,
      { method: 'POST', body: JSON.stringify({ recordingId }) }
    ),

  revalidateDownload: (requestId: string) =>
    request<DownloadRequest>(`/api/v1/downloads/${encodeURIComponent(requestId)}/revalidate`, {
      method: 'POST'
    }),

  uploads: () => request<UploadList>('/api/v1/uploads'),

  // Uploading is the one request that does not go through fetch. A release is
  // hundreds of megabytes over somebody's domestic upstream, and fetch cannot
  // say how far along it is — so this is XMLHttpRequest, which can. The problem
  // document is read off the response exactly as it is everywhere else.
  //
  // soundCloud is sent only for a one-file upload: one address names one
  // track, so it rides beside the bytes rather than being asked for afterward
  // on the file's row.
  uploadFiles: (
    files: File[],
    onProgress?: (fraction: number) => void,
    soundCloud?: SoundCloudUploadNaming
  ) =>
    new Promise<Upload>((resolve, reject) => {
      const body = new FormData();
      for (const file of files) body.append('files', file, file.name);
      if (soundCloud) {
        body.append('sourceUrl', soundCloud.sourceUrl);
        body.append('artist', soundCloud.artist);
        body.append('title', soundCloud.title);
        body.append('remixer', soundCloud.remixer);
      }

      const transfer = new XMLHttpRequest();
      transfer.open('POST', '/api/v1/uploads');
      transfer.setRequestHeader('Accept', 'application/json');
      transfer.upload.addEventListener('progress', (event) => {
        if (event.lengthComputable && event.total > 0) onProgress?.(event.loaded / event.total);
      });
      transfer.addEventListener('load', () => {
        let payload: unknown;
        try {
          payload = JSON.parse(transfer.responseText);
        } catch {
          payload = undefined;
        }
        if (transfer.status >= 200 && transfer.status < 300 && payload) {
          resolve(payload as Upload);
          return;
        }
        const problem = (payload ?? {}) as Problem;
        reject(
          new Error(
            problem.details?.[0] ?? problem.title ?? `The upload was refused (${transfer.status})`
          )
        );
      });
      transfer.addEventListener('error', () => reject(new Error('The upload could not be sent.')));
      transfer.addEventListener('abort', () => reject(new Error('The upload was cancelled.')));
      transfer.send(body);
    }),

  discardUpload: (uploadId: string) =>
    request<void>(`/api/v1/uploads/${encodeURIComponent(uploadId)}`, { method: 'DELETE' }),

  /** What the worker is doing and what is waiting for it, one list per lane. */
  jobs: () => request<JobQueue>('/api/v1/jobs'),

  /** The jobs that will not be tried again unless somebody asks. */
  failedJobs: () => request<FailedJobs>('/api/v1/jobs/failed'),

  /** The recurring machinery's pulse: when each pass last ran and when it next
   * intends to. */
  jobSchedule: () => request<JobSchedule>('/api/v1/jobs/schedule'),

  /** Puts a job that spent its attempts back on the queue, with its attempts
   * given back. Pressing twice costs the queue nothing: the second press finds
   * a job already on its way and says so. */
  retryJob: (jobId: string) =>
    request<RetriedJob>(`/api/v1/jobs/${encodeURIComponent(jobId)}/retry`, { method: 'POST' }),

  /** Puts every job that one cause stopped back on the queue, each with its
   * attempts given back. Seventy-one jobs stopped by one outage are one press,
   * because they are one problem. */
  retryFailedCause: (cause: string) =>
    request<RetriedCause>('/api/v1/jobs/failed/retry', {
      method: 'POST',
      body: JSON.stringify({ cause })
    }),

  /** Removes one queued job before a worker ever claims it. Only a queued row
   * is touched; a running job is left for the worker doing it. */
  cancelJob: (jobId: string) =>
    request<CancelledJob>(`/api/v1/jobs/${encodeURIComponent(jobId)}/cancel`, { method: 'POST' }),

  /** Removes every queued job of one kind — the shape a runaway sweep takes.
   * A kind that is a recurring sweep's own schedule is refused rather than
   * cleared, so clearing an unrelated backlog cannot switch a sweep off. */
  cancelQueuedJobsByKind: (kind: string) =>
    request<CancelledKind>('/api/v1/jobs/queued/cancel', {
      method: 'POST',
      body: JSON.stringify({ kind })
    })
};

// Every exported type and interface the API surface uses, apart from the
// fetch client itself. It holds no `fetch`, no SvelteKit and no I/O, so code
// can import the types without pulling in the client.

export interface OverviewDay {
  date: string;
  count: number;
}

export interface OverviewPlayed {
  title: string;
  artist: string;
  listens: number;
  recordingMbid: string | null;
  coverUrl: string | null;
  /** The catalogue track with this recording, when there is one. */
  trackId: string | null;
  inLibrary: boolean;
  wanted: boolean;
}

export interface OverviewArtist {
  name: string;
  artistMbid: string | null;
  listens: number;
  pictureUrl: string | null;
}

export interface OverviewAlbum {
  title: string;
  artist: string;
  listens: number;
  releaseMbid: string | null;
  coverUrl: string | null;
}

export interface OverviewSession {
  startedAt: string;
  endedAt: string;
  /** The last listen is recent enough that the next one would still join. */
  ongoing: boolean;
  songs: number;
  artists: string[];
  /** Up to three distinct pictures among the session's songs. */
  coverUrls: string[];
}

export interface OverviewAdded {
  title: string;
  artist: string;
  addedAt: string;
  trackId: string | null;
  coverUrl: string | null;
}

/** The Overview's figures, every day, hour and month counted in the zone the
 * browser sent. `listening.available` is false until the listening history has
 * been read; the other fields are still present then. */
export interface Overview {
  listening: {
    available: boolean;
    listens: { total: number; previous: number; days: OverviewDay[] };
    mostPlayed: OverviewPlayed[];
    topArtists: OverviewArtist[];
    whenYouListen: { cells: number[][]; peakHour: number; busiestWeekday: string };
    topAlbums: OverviewAlbum[];
    sessions: OverviewSession[];
  };
  arrived: { today: number; downloading: number; days: OverviewDay[] };
  library: {
    fileCount: number;
    totalBytes: number;
    growth: { month: string; files: number }[];
    storage: { usedBytes: number; totalBytes: number } | null;
  };
  recentlyAdded: OverviewAdded[];
}

export interface DashboardSummary {
  artistCount: number;
  catalogueArtistCount: number;
  albumCount: number;
  trackCount: number;
  openWantCount: number;
  activeDownloadCount: number;
  /** Jobs a worker has in hand right now. There are five lanes, so this is
   * never more than five. */
  runningJobCount: number;
  /** Jobs waiting for a lane. A catalogue refresh queues one per album, so this
   * is routinely in the hundreds and says nothing is wrong. */
  queuedJobCount: number;
  /** When a real search was first refused because the download source is not
   * logged in to Soulseek, since the last one went through. Null while
   * nothing has refused this way — never set from one failed connection
   * check, only from acquisition actually hearing the refusal (#495). */
  sourceLoggedOutSince: string | null;
  /** When searching resumes, after the download source stopped asking because
   * Soulseek answered eight searches in a row with nothing. Null while
   * searching is going ahead (#193). */
  sourceSilencedUntil: string | null;
}

export interface Artist {
  id: string;
  musicbrainzId: string | null;
  name: string;
  sortName: string;
  /** Followed means the user asked for this artist's releases to be kept
   * complete. An artist Schall merely holds is in the catalogue because the
   * library contains their music, and catalogueSummary says why. */
  followed: boolean;
  followedAt: string | null;
  catalogueSummary?: string;
  /** The service this artist is an account on, where they are one —
   * 'soundcloud' for a SoundCloud uploader. MusicBrainz has never heard of
   * them, so there is no discography, nothing to refresh, and completeness is
   * not a question that can be asked. Absent for every other artist. */
  source?: string;
  lastRefreshedAt: string | null;
  refreshStatus: 'pending' | 'queued' | 'running' | 'completed' | 'failed';
  refreshError?: string;
}

/** An artist as the index shows them: how much of their catalogue the library
 * holds, and whether anything about them wants a person. `releaseCount` is the
 * catalogue's idea of their discography, which is zero until it has been
 * fetched — a card cannot call that complete or incomplete, only unknown.
 * `ownedReleaseCount` counts releases whose every track maps onto a file the
 * library still has, the same rule the releases browser counts by. */
export interface ArtistListItem extends Artist {
  releaseCount: number;
  ownedReleaseCount: number;
  trackCount: number;
  ownedTrackCount: number;
  /** Transfers moving right now, not requests on record. */
  inFlightCount: number;
  /** Wants whose copy nothing could identify, held for you to listen to. */
  reviewCount: number;
  needsAttention: boolean;
  /** Whether a picture of the artist is cached. The index asks for the
   * pictures that exist and not for every card. */
  hasImage: boolean;
}

/** One page of artists. `total` counts the current scope narrowed by
 * completeness — what the page is a page of. `followedCount` and `heldCount`
 * count both halves of the list whichever scope is showing, so a narrowed page
 * never makes the collection look smaller than it is, and the four completeness
 * counts label the tabs between which the user is choosing, so none of them
 * changes when a choice is made. `refreshingCount` covers artists on every page,
 * not just this one. */
export interface ArtistList {
  items: ArtistListItem[];
  total: number;
  limit: number;
  offset: number;
  followedCount: number;
  heldCount: number;
  refreshingCount: number;
  allCount: number;
  incompleteCount: number;
  completeCount: number;
  attentionCount: number;
  releaseTotal: number;
  ownedReleaseTotal: number;
}

/** Scope narrows the list to one of the two reasons an artist is in the
 * catalogue. Absent means everyone: both reasons are ones the user would
 * recognise, so there is nothing here a default scope should hide.
 *
 * `completeness` and `sort` are answered by the server rather than the browser
 * so narrowing and ordering happen before the list arrives. */
export interface ArtistFilters {
  scope?: 'followed' | 'held';
  query?: string;
  completeness?: 'incomplete' | 'complete' | 'attention';
  /** One genre, picked from the list the catalogue holds. It is matched whole
   * rather than searched for, so it is never a pattern. */
  genre?: string;
  sort?: 'name' | 'least-complete' | 'most-missing';
}

/** What counts as missing for an artist and what "want everything missing"
 * wants. A filter on counting and auto-wanting, never on what is shown:
 * unmonitored releases stay browsable and wantable by hand. */
export type MonitorLevel = 'everything' | 'main' | 'albums_eps' | 'owned';

export interface ArtistDetail extends Artist {
  albumCount: number;
  monitorLevel: MonitorLevel;
  /** The standing "want what's missing". While it is on, every missing track of
   * every monitored release is wanted, including the releases whose tracklist
   * has not arrived yet. Turning it off stops future wanting and leaves every
   * want it made. */
  wantMissing: boolean;
  discography: ArtistDiscography;
  /** What MusicBrainz's community voted this artist is, most voted first.
   * Display only: nothing that decides anything reads it. Empty both when
   * nobody voted and when nobody has asked yet. */
  genres: string[];
  /** A few lines from Wikipedia about who this is, and the article they came
   * from. The text is CC BY-SA, so anything showing it has to name Wikipedia
   * and link the article — the two arrive together or not at all. */
  biography?: string;
  biographySourceUrl?: string;
}

/** A record label, held the way an artist is: by its MusicBrainz identifier,
 * followed as a standing request to keep what it publishes complete. */
export interface Label {
  id: string;
  musicbrainzId: string;
  name: string;
  type?: string;
  country?: string;
  disambiguation?: string;
  monitorLevel: MonitorLevel;
  followed: boolean;
  followedAt: string | null;
  lastRefreshedAt: string | null;
  refreshStatus: 'pending' | 'queued' | 'running' | 'completed' | 'failed';
}

export interface LabelListItem extends Label {
  releaseCount: number;
  ownedReleaseCount: number;
  trackCount: number;
  ownedTrackCount: number;
}

export interface LabelList {
  items: LabelListItem[];
  total: number;
  followedCount: number;
}

export interface LabelRelease {
  id: string;
  title: string;
  musicbrainzReleaseGroupId: string | null;
  releaseDate?: string;
  albumType: string;
  artistId: string;
  artistName: string;
  trackCount: number;
  ownedTrackCount: number;
  /** Whether the label's monitor level counts this release. An unmonitored
   * release stays listed and can still be wanted by hand. */
  monitored: boolean;
}

export interface LabelDetail {
  label: Label;
  releases: LabelRelease[];
}

export interface LabelSearchResult {
  musicbrainzId: string;
  name: string;
  type?: string;
  country?: string;
  area?: string;
  disambiguation?: string;
  score: number;
}

export interface LabelSearchResults {
  items: LabelSearchResult[];
}

/** How much of an artist the library holds, counted over everything they have
 * rather than over the page of releases that happens to be showing. `owned`
 * over `counted` is the completeness bar; `missing` is what "want everything
 * missing" would want, and is not `counted` less `owned` because a release
 * still refreshing is neither yet. All but `releases` count only monitored
 * releases: the monitor level filters counting, never what is shown. */
export interface ArtistDiscography {
  releases: number;
  owned: number;
  missing: number;
  dismissed: number;
  counted: number;
  /** Monitored releases whose tracklist has not arrived. Following an artist
   * queues one release refresh per release group and they land one at a time
   * behind MusicBrainz's rate limit, so for the first hours a discography holds
   * releases that are neither owned nor missing yet. */
  loading: number;
}

/** Unfollowing withdraws the intent to keep an artist complete, and nothing
 * else. An artist whose music the library still reaches is `held` and keeps
 * every release; `released` means the library reached nothing of theirs, so
 * removing them took nothing with it. */
export interface UnfollowResult {
  id: string;
  musicbrainzId: string | null;
  name: string;
  sortName: string;
  followed: false;
  outcome: 'held' | 'released';
  catalogueSummary?: string;
  mappedTrackCount: number;
}

export interface Release {
  id: string;
  artistId: string;
  artistName: string;
  /** A release held only to describe an owned file is not missing its other
   * tracks; nobody asked for this artist to be kept complete. */
  artistFollowed: boolean;
  musicbrainzReleaseGroupId: string | null;
  title: string;
  firstReleaseDate?: string;
  albumType: string;
  trackCount: number;
  ownedTrackCount: number;
  /** Unowned tracks whose recordings the user decided not to want. They leave
   * the completion denominator: a dismissal is a decision, not a gap. */
  dismissedTrackCount: number;
  /** Whether the artist's monitor level counts this release toward
   * completeness. Unmonitored releases stay shown; they stop being missing. */
  monitored: boolean;
  /** The artist's monitor level, carried per row so pages compute the same
   * denominator the server does: at 'owned' the countable tracks are the
   * owned ones, not trackCount minus dismissals. */
  artistMonitorLevel: MonitorLevel;
  musicbrainzReleaseId: string | null;
  editionSelectionReason?: string;
  trackRefreshStatus: 'pending' | 'queued' | 'running' | 'completed' | 'failed';
  /** Whether a picture of the release is cached. A list asks for the covers
   * that exist and not for every row; a release nobody has pictured yet, or
   * one the archives have no sleeve for, has none. */
  hasCover: boolean;
}

/** Which releases the browser is asking for. Scope is why a release is worth
 * showing at all, status is the shape it is in, and both are decided on the
 * server: filtering a page would filter one page rather than the list.
 *
 * Leaving `limit` out asks for the whole list, which is what the views that
 * total the catalogue rather than browse it still need. */
export interface ReleaseFilters {
  artistId?: string;
  scope?: 'followed' | 'library';
  query?: string;
  status?: ReleaseStatus;
  /** The two halves of an artist's completeness, counted by their monitor level
   * the way the fraction on their page is. Not `status` by another name: that
   * says what shape a release is in, this says whether it is part of what the
   * user asked to keep complete. */
  completeness?: ReleaseCompleteness;
  sort?: 'artist' | 'title' | 'year' | 'owned';
  direction?: 'asc' | 'desc';
  limit?: number;
  offset?: number;
}

export type ReleaseStatus = 'owned' | 'partial' | 'missing' | 'untracked' | 'failed';

export type ReleaseCompleteness = 'owned' | 'missing';

export interface ReleaseList {
  items: Release[];
  total: number;
  limit: number;
  offset: number;
  /** What the list holds before `status` narrows it, so a page can say how much
   * a status filter is holding back rather than looking empty. */
  scopeTotal: number;
  /** How many releases each shape holds, counted in the same pass as
   * `scopeTotal`. Every one of them is arithmetic on columns the release
   * carries, so all six cost what one used to. Absent when the whole list was
   * asked for rather than a page of it, which is not counted at all — nought
   * would read as an answer. */
  ownedCount?: number;
  partialCount?: number;
  missingCount?: number;
  untrackedCount?: number;
  failedCount?: number;
}

export interface ReleaseDetail {
  id: string;
  artistId: string;
  artistName: string;
  artistFollowed: boolean;
  musicbrainzReleaseGroupId: string | null;
  title: string;
  firstReleaseDate?: string;
  albumType: string;
  musicbrainzReleaseId: string | null;
  editionStatus?: string;
  editionCountry?: string;
  barcode?: string;
  editionReleaseDate?: string;
  mediaCount: number;
  trackCount: number;
  selectedAutomatically: boolean;
  selectionReason?: string;
  // Which archive the cover came from. 'coverartarchive' answered about the very
  // release this is; 'itunes' was asked by name, which is a guess and is shown as
  // one. 'user' is a picture somebody set themselves, which is the only one the
  // page offers to take back. Absent until a cover has been looked for.
  coverSource?: string;
  /** Whether the tracks of this release are still on their way from MusicBrainz,
   * in the same five words a row in the releases list uses. A release with no
   * tracks is an ordinary permanent state for music held locally, so the tracks
   * alone cannot say whether waiting would ever end: 'queued' and 'running' mean
   * work is under way, and anything else means nothing is coming. */
  trackRefreshStatus: 'pending' | 'queued' | 'running' | 'completed' | 'failed';
  /** What MusicBrainz's community voted this release is, most voted first.
   * Display only. Empty both when nobody voted and when nobody has asked. */
  genres: string[];
}

/** What a release's cover became after somebody set one. The picture itself is
 * still asked for at the address it always is; this only says what was kept. */
export interface ReleaseCover {
  source: string;
  contentType?: string;
  sizeBytes?: number;
}

export interface ReleaseEdition {
  musicbrainzReleaseId: string;
  title: string;
  status?: string;
  country?: string;
  releaseDate?: string;
}

export interface ReleaseEditions {
  items: ReleaseEdition[];
}

export interface Track {
  id: string;
  musicbrainzRecordingId: string | null;
  title: string;
  discNumber: number;
  trackNumber: number | null;
  durationMs: number | null;
  isrc?: string;
  owned: boolean;
  /** The library holds this recording on a file mapped onto another release —
   * a single, an EP, a compilation. The music is here; the match details below
   * belong to whichever release has the mapping. */
  heldElsewhere: boolean;
  matchMethod?: string;
  matchConfidence?: number;
  matchManual: boolean;
  libraryFileId?: string;
  /** The want that governs this track's recording, when one exists: its id so
   * the page can act on it, and its status so the page can offer the toggle
   * that fits. Absent when nothing was ever asked for. */
  wantId?: string;
  wantStatus?: string;
}

// One release-wide dismissal press, accounted for the same way WantedTracks
// accounts the other direction.
export interface DismissedTracks {
  dismissed: number;
  alreadyDismissed: number;
  owned: number;
  unresolvable: number;
}

export interface DismissedRelease extends DismissedTracks {
  albumId: string;
}

// What became of every track somebody wanted in one press. A bare count of new
// wants would read as the rest having failed, so each outcome is counted
// separately. `unresolvable` is a track the catalogue holds no MusicBrainz
// recording against, which is the one thing that cannot be asked for.
export interface WantedTracks {
  wanted: number;
  alreadyWanted: number;
  owned: number;
  unresolvable: number;
}

export interface WantedRelease extends WantedTracks {
  albumId: string;
}

// A whole discography's worth, with the number of releases something was
// missing from.
export interface WantedArtist extends WantedTracks {
  artistId: string;
  releases: number;
}

// The same, from setting or clearing the standing "want what's missing".
// Clearing it walks nothing, so the counts are all nought.
export interface ArtistWantMissing extends WantedArtist {
  wantMissing: boolean;
}

export interface TrackList {
  items: Track[];
}

export interface RefreshJob {
  jobId: string;
  status: 'queued' | 'running';
  createdAt: string;
}

export interface ArtistSearchResult {
  musicbrainzId: string;
  name: string;
  sortName: string;
  type?: string;
  country?: string;
  area?: string;
  disambiguation?: string;
  score: number;
}

export interface ArtistSearchResults {
  items: ArtistSearchResult[];
}

/** One question asked of everything Schall holds, grouped by the kind of thing
 * each answer is. There is no single ranked list on purpose: an artist and a
 * file are not comparable, and a score that interleaved them would be a guess.
 * Every group is present even when empty. */
export interface GlobalSearchResults {
  query: string;
  limit: number;
  artists: GlobalSearchArtist[];
  releases: GlobalSearchRelease[];
  tracks: GlobalSearchTrack[];
  files: GlobalSearchFile[];
  playlists: GlobalSearchPlaylist[];
  totals: GlobalSearchTotals;
}

/** How many results each group has, which is not how many came back: the limit
 * is what a glance shows, and this is what it would have shown without one. It
 * is the only thing "All 214 in Files" can be written from. */
export interface GlobalSearchTotals {
  artists: number;
  releases: number;
  tracks: number;
  files: number;
  playlists: number;
}

export interface GlobalSearchArtist {
  id: string;
  name: string;
  sortName: string;
  /** Followed on purpose, as against held because their music is here. */
  followed: boolean;
  /** What the library holds of them: files it still has, and the releases at
   * least one of those files sits on. Neither is the artists page's
   * completeness fraction, which counts releases the library holds entirely. */
  fileCount: number;
  releaseCount: number;
}

export interface GlobalSearchRelease {
  id: string;
  title: string;
  artistId: string;
  artistName: string;
  /** What kind of release it is, as MusicBrainz files it: album, ep, single. */
  albumType: string;
  releaseDate?: string;
  trackCount: number;
  ownedTrackCount: number;
}

/** A catalogue track has no page of its own, so it is linked to through the
 * release that holds it. */
export interface GlobalSearchTrack {
  id: string;
  title: string;
  discNumber: number;
  trackNumber?: number;
  /** What the catalogue says the recording lasts, when it says anything. */
  durationMs?: number;
  albumId: string;
  albumTitle: string;
  artistId: string;
  artistName: string;
  /** The library holds this recording somewhere — mapped onto this track, or
   * proven on a file that is mapped onto nothing, or carried by another
   * release's track. It never says which file is this track. */
  owned: boolean;
}

export interface GlobalSearchFile {
  id: string;
  path: string;
  artistTag?: string;
  albumTag?: string;
  titleTag?: string;
  /** The size the scanner recorded. */
  sizeBytes: number;
  matchStatus: string;
  /** A file the last scan could not find. It is still answered, because the
   * library list still lists it. */
  missing: boolean;
}

export interface GlobalSearchPlaylist {
  id: string;
  name: string;
  source: string;
  ownerName: string;
  trackCount: number;
  importedAt: string | null;
}

export interface LibrarySummary {
  rootCount: number;
  fileCount: number;
  missingCount: number;
  errorCount: number;
  matchedCount: number;
  unmatchedCount: number;
  ambiguousCount: number;
  duplicateCount: number;
  /** The part of duplicateCount nobody has answered yet — what the queue holds,
   * as against how much of the library is a second copy. */
  duplicatesToDecideCount: number;
  resolvedCount: number;
  needsReviewCount: number;
  conflictCount: number;
  localOnlyCount: number;
  pendingCount: number;
  failedCount: number;
  totalSizeBytes: number;
  scanStatus: 'idle' | 'queued' | 'running' | 'completed' | 'failed';
  scanError?: string;
  scanStartedAt: string | null;
  scanCompletedAt: string | null;
}

export interface LibraryRoot {
  id: string;
  path: string;
  enabled: boolean;
  lastScannedAt: string | null;
  createdAt: string;
}

export interface LibraryRoots {
  items: LibraryRoot[];
}

export interface LibraryFile {
  id: string;
  path: string;
  sizeBytes: number;
  modifiedAt: string;
  artistTag?: string;
  albumTag?: string;
  titleTag?: string;
  discNumber?: number;
  trackNumber?: number;
  durationMs?: number;
  musicbrainzRecordingId?: string;
  isrc?: string;
  missingAt?: string;
  scanError?: string;
  matchStatus: 'matched' | 'unmatched' | 'ambiguous' | 'duplicate';
  matchCandidateCount: number;
  /** Set when somebody said they meant to have this twice. Only meaningful
   * while matchStatus is 'duplicate'. */
  duplicateKeptAt?: string;
  mappingMethod?: string;
  mappingConfidence?: number;
  mappingManual: boolean;
  mappedTrackId?: string;
  resolutionStatus: ResolutionStatus;
  resolutionSummary?: string;
  identityTitle?: string;
  identityArtist?: string;
  identityManual: boolean;
  /** The service a file held by address lives on — 'soundcloud' — and the page
   * it is published at. Both absent for a file the catalogue can name. */
  identitySource?: string;
  identityUrl?: string;
  /** Who edited the track, where the person naming it said somebody other than
   * the uploader recorded it. */
  identityRemixer?: string;
  identityCandidateCount: number;
  /** Set when the person asked Schall to stop asking about this file. */
  setAside?: boolean;
}

/** One file answering a recording the library holds more than one copy of.
 *
 * `format` is read off the file's extension, because that is the only place it
 * is written down: nothing stores the codec, bitrate or sample rate, so the
 * list can say FLAC or MP3 and no more than that. */
export interface DuplicateCopy {
  id: string;
  path: string;
  format?: string;
  sizeBytes: number;
  durationMs?: number;
  /** What the audio is, read off the stream rather than off the name. Absent
   * until a scan has listened to the file. */
  bitRateKbps?: number;
  sampleRateHz?: number;
  channels?: number;
  /** Where the music in this copy stops, and whether that stop is where a lossy
   * encoder would have put it — audio squeezed before it reached the container
   * it is in. Absent until the audio has been decoded, and absent is never
   * agreement that a copy is fine. Shown to the reader and counted by nothing:
   * which copy is the better one is decided on the numbers above. */
  spectralCutoffHz?: number;
  transcodeSuspected?: boolean;
  /** The copy worth keeping, and the copies proven to be lesser versions of it.
   * Both are absent on every copy of a group nothing could separate. */
  keeper?: boolean;
  redundant?: boolean;
  /** This file is also a copy of different music, so keeping one copy of this
   * recording never deletes it. */
  answersOther?: boolean;
  /** Where this copy sits: the catalogue release it is matched on, or failing
   * that the album its own tags claim. Usually what tells an album copy from a
   * single. */
  release?: string;
  /** How this file came to answer the recording. */
  reason: string;
  /** Set when somebody said the library is meant to hold this copy too. */
  keptAt?: string;
}

export interface DuplicateRecording {
  recordingId: string;
  title?: string;
  artist?: string;
  copies: DuplicateCopy[];
  /** One sentence: which copy is worth keeping and what makes it the better
   * one, or why nothing stored separates them. */
  verdict: string;
  /** Whether one copy was proven better than every other, which is the whole
   * condition for offering to remove the rest. */
  decided: boolean;
}

/** Every recording the library holds twice. `total` counts every group,
 * including the ones past the end of the page. */
export interface DuplicateRecordings {
  recordings: DuplicateRecording[];
  total: number;
  limit: number;
  offset: number;
  /** Files the library let go of and could not delete. Normally empty. */
  stillOnDisc: RemovedFileStillOnDisc[];
}

/** One audio file the library removed and the disc kept. */
export interface RemovedFileStillOnDisc {
  path: string;
  reason?: string;
  removedAt: string;
}

/** What one press of "Keep this copy" left standing and what it deleted. */
export interface KeptCopy {
  keptFileId: string;
  keptPath: string;
  removedPaths: string[];
  /** The removed files whose audio is still on disc. Normally empty. */
  stillOnDisc: string[];
  /** Set when a failure stopped the press before every copy in removedPaths'
   * complement was deleted. What removedPaths names is really gone either
   * way; this says why the rest is not. */
  stopped?: string;
}

// What Schall knows about the external identity of one file, which is a
// separate question from whether the local catalogue holds a track for it.
export type ResolutionStatus =
  | 'pending'
  | 'resolved'
  | 'needs_review'
  | 'conflict'
  | 'local_only'
  | 'failed'
  // The file is named by its address on another service instead of by a
  // recording. Somebody pasted that address and confirmed it.
  | 'source';

/** What SoundCloud's oEmbed endpoint said about one track.
 *
 * `title` is the whole string it sent, which is how it is stored. `trackTitle`
 * is that string without the " by <uploader>" oEmbed itself adds, and it is
 * what goes into the file. */
export interface SoundCloudTrack {
  externalId: string;
  title: string;
  trackTitle: string;
  uploader: string;
  uploaderUrl?: string;
  artworkUrl?: string;
  permalink: string;
  /** What Schall reads off the title: who the track is by, what it is called,
   * and who edited it. It is what the dialog's three fields start at, and the
   * person sends back whatever they made of it. */
  suggested: SoundCloudNamingFields;
}

/** One naming, going either way: out as the suggestion and back as the answer. */
export interface SoundCloudNamingFields {
  artist: string;
  title: string;
  remixer: string;
}

export interface SoundCloudNaming {
  track: SoundCloudTrack;
  /** What was written, which is what the person confirmed. */
  named: SoundCloudNamingFields;
  artistId: string;
  summary: string;
  pictured: boolean;
}

/** The address and naming a one-file upload carries, sent alongside the bytes
 * so the file lands already named rather than waiting to be named from its
 * row. */
export interface SoundCloudUploadNaming extends SoundCloudNamingFields {
  sourceUrl: string;
}

export interface FileIdentity {
  recordingId?: string;
  releaseGroupId?: string;
  artistName?: string;
  releaseTitle?: string;
  trackTitle?: string;
  durationMs?: number;
  isrc?: string;
  kind: 'external' | 'local_only';
  /** The short name Schall records the evidence under, such as `recording-id`.
   * It is for code to read. `methodText` is the same thing written out, and it
   * is the only one of the two that may be shown to a person. */
  method: string;
  methodText: string;
  confidence: number;
  manual: boolean;
  summary: string;
  evidence: string[];
  decidedAt: string;
}

export interface IdentityCandidate {
  recordingId: string;
  releaseGroupId?: string;
  artistName: string;
  releaseTitle?: string;
  trackTitle: string;
  durationMs?: number;
  isrc?: string;
  rank: number;
  agrees: string[];
  differs: string[];
  summary: string;
}

export interface FileResolution {
  fileId: string;
  status: ResolutionStatus;
  summary?: string;
  error?: string;
  attemptedAt?: string;
  matchStatus: LibraryFile['matchStatus'];
  identity: FileIdentity | null;
  candidates: IdentityCandidate[];
}

export interface LibraryFiles {
  items: LibraryFile[];
  total: number;
  limit: number;
  offset: number;
}

/** What the file list can be narrowed to.
 *
 * The four matching states, plus the two states the scanner records. Those two
 * are not matching states — they say a file is not on disc, or that it is and
 * could not be read — and they are in the same field because the reader picking
 * one is asking one question: which files am I looking at. */
export type LibraryFileStatus = LibraryFile['matchStatus'] | 'missing' | 'unreadable';

export interface LibraryFileFilters {
  status?: LibraryFileStatus;
  resolution?: ResolutionStatus;
  query?: string;
  limit?: number;
  offset?: number;
  /** One file by name, read through the list rather than through a second
   * endpoint. */
  id?: string;
  /** Include only files with or without a set-aside row. */
  setAside?: boolean;
  /** Include present files that still need an identity or matching decision. */
  unidentified?: boolean;
}

export interface MatchCandidate {
  trackId: string;
  title: string;
  albumTitle: string;
  artistName: string;
  discNumber: number;
  trackNumber?: number;
  method: string;
  confidence: number;
  alreadyMapped: boolean;
  manuallyMapped: boolean;
  /** The file this track already belongs to. Choosing the track takes it away
   * from that file, which nobody can weigh without being told what it is —
   * most of the time it turns out to be the same music twice. */
  heldByFileId?: string;
  heldByPath?: string;
}

export interface MatchCandidates {
  items: MatchCandidate[];
}

export interface SpotifySettings {
  configured: boolean;
  clientId: string;
  clientSecretSet: boolean;
  connected: boolean;
  accountName?: string;
  connectionStatus: 'unknown' | 'ok' | 'failed';
  connectionError?: string;
  lastCheckedAt: string | null;
  // What must be registered on the Spotify application for the connect flow
  // to find its way back here.
  redirectUri: string;
}

export interface Playlist {
  id: string;
  // 'navidrome' is a list created in the player and adopted by Schall; its
  // sourceId is the remote playlist. 'file' is a CSV or M3U somebody
  // uploaded; its sourceId is the file's own name. 'weekly' and
  // 'new_releases' are the two Schall makes for itself, one row each for the
  // life of the installation.
  source: 'spotify' | 'manual' | 'navidrome' | 'file' | 'weekly' | 'new_releases';
  sourceId?: string;
  sourceRevision?: string;
  name: string;
  description: string;
  ownerName: string;
  trackCount: number;
  entryCount: number;
  ownedCount: number;
  importedAt: string | null;
  createdAt: string | null;
}

// MusicBrainz's release editor, filled in from an entry, as the form fields it
// asks for. It arrives as data rather than as an address because the editor
// reads a seed out of a request body and nothing but shortcuts out of a query
// string, so what the interface can offer is a form somebody submits.
export interface MusicBrainzSeed {
  url: string;
  fields: { name: string; value: string }[];
}

export interface PlaylistEntry {
  id: string;
  position: number;
  artist: string;
  title: string;
  album: string;
  durationMs?: number;
  isrc?: string;
  ownedFileId?: string;
  targetId?: string;
  targetStatus?: string;
  targetSummary?: string;
  source?: string;
  externalId?: string;
  externalUrl?: string;
  sourceLookup?: SourceLookup;
  minimumBitrate?: number;
  // Present only for the entries MusicBrainz has had no recording for, which is
  // the only state where adding the release is what helps. An entry still
  // waiting to be asked about carries none, and looks the same on the wire
  // otherwise — the API draws that line, because the summary is prose and the
  // status says 'unresolved' for both.
  musicbrainzSeed?: MusicBrainzSeed;
}

export interface SourceLookup {
  title: string;
  artist: string;
  uploader: string;
  accountId?: string;
  durationMs: number;
  artworkUrl?: string;
}

export interface SourceTrack {
  source: string;
  externalId: string;
  url: string;
  title: string;
  artist: string;
  uploader: string;
  accountId?: string;
  durationMs: number | null;
  artworkUrl?: string;
}

// What the player was found to hold for one list, read now rather than
// remembered from the last push. The three counts are three different
// questions: how long the list is, how much of it Schall has a file for, and
// how much of that the player answered for.
export interface PlayerPairing {
  configured: boolean;
  entryCount: number;
  acquiredCount: number;
  // How many acquired files the player was asked about. Below acquiredCount the
  // rest ran out of time and is unknown rather than missing.
  checked: number;
  pairedCount: number;
  pushed: number;
  lastPushedAt: string | null;
  remotePlaylistId?: string;
  unpaired: UnpairedFile[];
}

export interface UnpairedFile {
  entryId: string;
  position: number;
  path: string;
  // The same path with its non-ASCII escaped and its length in bytes, so a
  // difference that does not survive being displayed is still visible.
  pathEscaped: string;
  reason: 'no_search_term' | 'no_candidates' | 'no_path_match' | 'path_repeated' | string;
  searched: string[];
  candidates: number;
  offered: string[];
}

export interface SlskdSettings {
  configured: boolean;
  baseUrl: string;
  apiKeySet: boolean;
  enabled: boolean;
  searchTimeoutSeconds: number;
  connectionStatus: 'unknown' | 'ok' | 'failed';
  connectionDetail?: string;
  connectionError?: string;
  lastCheckedAt: string | null;
}

export interface SlskdSettingsInput {
  baseUrl: string;
  apiKey: string;
  enabled: boolean;
  searchTimeoutSeconds: number;
}

export interface NavidromeSettings {
  configured: boolean;
  baseUrl: string;
  username: string;
  passwordSet: boolean;
  enabled: boolean;
  connectionStatus: 'unknown' | 'ok' | 'failed';
  connectionDetail?: string;
  connectionError?: string;
  lastCheckedAt: string | null;
  // When the player last accepted a request to look at the library again. A
  // scan that changed nothing never asks, so this trailing the last import is
  // ordinary rather than a sign anything failed.
  lastNotifiedAt: string | null;
}

export interface NavidromeSettingsInput {
  baseUrl: string;
  username: string;
  password: string;
  enabled: boolean;
}

// Where the listening history recommendations are drawn from lives. Schall only
// ever reads it: a dismissal is Schall's own record of the user's taste, not
// something published under their name.
// Where Schall says that the review queue has a question, and whether to say
// anything at all. Off with no address is what every installation starts with.
export interface NotificationSettings {
  configured: boolean;
  // 'ntfy' is the push service whose app puts the message on a phone; 'webhook'
  // is anything else, and is sent a JSON object.
  kind: 'ntfy' | 'webhook';
  endpoint: string;
  tokenSet: boolean;
  enabled: boolean;
  connectionStatus: 'unknown' | 'ok' | 'failed';
  connectionError?: string;
  lastCheckedAt: string | null;
}

// Who the server took this request to be, and how it knows. 'open' is an
// installation that asks nobody for a credential.
export interface Me {
  actor: string;
  auth: 'browser' | 'token' | 'open';
}

// One phone that holds a token. The token itself is not here: only its hash is
// stored, so nothing can read one back.
export interface Phone {
  id: string;
  name: string;
  createdBy: string;
  createdAt: string;
  lastUsedAt: string | null;
}

// The answer to minting, and the only place the plain token exists. `server` is
// the address the browser reached Schall on, so the QR code can carry somewhere
// the phone can reach.
export interface MintedPhone extends Phone {
  token: string;
  server: string;
}

export interface NotificationSettingsInput {
  kind: 'ntfy' | 'webhook';
  endpoint: string;
  // Empty keeps the stored token, which is why removing one needs its own flag:
  // the page is never shown the token, so it cannot send it back to keep it.
  token: string;
  clearToken: boolean;
  enabled: boolean;
}

export interface ListenBrainzSettings {
  configured: boolean;
  baseUrl: string;
  labsUrl: string;
  username: string;
  userTokenSet: boolean;
  // The labs similarity algorithm is a closed enum the dataset host may rename
  // between deployments, so it is stored rather than compiled in.
  similarityAlgorithm: string;
  enabled: boolean;
  connectionStatus: 'unknown' | 'ok' | 'failed';
  connectionDetail?: string;
  connectionError?: string;
  lastCheckedAt: string | null;
}

// The addresses are absent on purpose: they are read-only from here. A caller
// that could name the host could point it at one of its own and read the stored
// token off the connection check's Authorization header, so the write path does
// not accept an address at all.
export interface ListenBrainzSettingsInput {
  username: string;
  // Empty keeps the stored token, which is why removing one needs its own flag:
  // the page is never shown the token, so it cannot send it back to keep it.
  userToken: string;
  clearUserToken: boolean;
  similarityAlgorithm: string;
  enabled: boolean;
}

// One suggestion: music the library does not hold, offered because of what the
// account has listened to. Every identifier is a MusicBrainz identifier, and
// they are what a dismissal names.
export interface Recommendation {
  recordingId: string;
  releaseGroupId: string;
  artistIds: string[];
  recordingTitle: string;
  releaseTitle: string;
  artistName: string;
  rank: number;
  // The source's own words for why it offered this recording, carried through
  // untouched. Schall does not decide what they mean.
  reasonCodes: string[];
}

// Where the list came from and when. `fetched` is false until a sweep has
// stored an answer, which is what tells "nothing read yet" apart from "nothing
// to suggest".
export interface RecommendationSnapshot {
  source: string;
  fetched: boolean;
  status: string;
  detail: string;
  fetchedAt: string | null;
}

// Whether the background sweep is still refreshing this list, and when it next
// tries. A sweep that cannot reach ListenBrainz leaves the last complete list
// where it is, so without this the screen cannot tell a list read this morning
// from one read last week.
//
// `attempted` is false when no sweep has ever run to an end here, which is
// ordinary rather than a fault. `succeeded` is false only when the last attempt
// left the list unrefreshed and no later one is under way.
export interface RecommendationRefresh {
  attempted: boolean;
  succeeded: boolean;
  lastAttemptAt: string | null;
  nextAttemptAt: string | null;
}

// How many suggestions the seven rules held back, and which reason held each
// one. The second rule has two: a recording can be held back because a want is
// being pursued, or because one was stopped.
// Counted over the whole stored list rather than the page being read.
export interface RecommendationsHidden {
  total: number;
  owned: number;
  requested: number;
  notWanted: number;
  followedArtist: number;
  followedLabel: number;
  dismissed: number;
  unkept: number;
  impressionFatigue: number;
}

export interface RecommendationList {
  items: Recommendation[];
  total: number;
  limit: number;
  offset: number;
  hidden: RecommendationsHidden;
  snapshot: RecommendationSnapshot;
  refresh: RecommendationRefresh;
}

// Which page of the suggestions to read, asked one of three ways. `after` and
// `before` are the rank of a suggestion the reader was just shown; `offset` is
// a position in the list they may see now. Naming two of them is refused,
// because two of them name two different pages.
export interface RecommendationPageRequest {
  offset?: number;
  after?: number;
  before?: number;
}

/** The level a dismissal applies at. It is permanent at that level. */
export type RecommendationSubject = 'recording' | 'release_group' | 'artist';

export type RecommendationFeedbackSignal = 'more_like_this' | 'less_like_this';

// The weekly playlist. Schall picks a few suggestions a week, obtains them, and
// puts them here; a song nobody kept is removed again a week later.
//
// `mode` is the switch that decides whether anything is ever deleted. In
// `report` a refresh says what it would have removed and removes nothing.
export interface WeeklySettings {
  enabled: boolean;
  songsPerWeek: number;
  mode: 'report' | 'remove';
  /** Percentage of the week's songs taken from the collection. */
  libraryShare: number;
  /** Keep at most one song from each artist in the week. */
  onePerArtist: boolean;
}

// One song on trial.
//
// `held` is a song whose week is still running. `leaving` is one that was
// announced for removal and goes when `removesAt` arrives — that is the week of
// notice, and pressing Keep during it stops the removal for good.
//
// `extendedReason` is why a song got more time. It is filled when a week could
// not be read: an unread signal keeps the song, and the reader is owed the
// reason rather than a date that moved for no visible cause.
export interface WeeklyLease {
  id: string;
  libraryFileId: string | null;
  path: string;
  artist: string;
  title: string;
  state: 'held' | 'leaving';
  grantedAt: string;
  expiresAt: string;
  removesAt: string | null;
  extensions: number;
  extendedReason: string;
}

/** What one refresh did. `leaving` counts the songs it announced, `removed` the
 * announced songs it carried out. `library` counts the songs it took from the
 * collection, which are never on trial and never removed. `replaced` counts the
 * slots it took back from wants that had a week and could not be proven. */
export interface WeeklyRun {
  id: string;
  mode: 'report' | 'remove';
  status: 'running' | 'complete' | 'partial' | 'failed';
  detail: string;
  chosen: number;
  wanted: number;
  arrived: number;
  kept: number;
  extended: number;
  leaving: number;
  removed: number;
  library: number;
  replaced: number;
  startedAt: string;
  finishedAt: string | null;
}

export interface WeeklyOverview {
  settings: WeeklySettings;
  playlistId: string | null;
  leases: WeeklyLease[];
  runs: WeeklyRun[];
}

/** One answer about one signal for one song. `unreadable` is not `absent`: a
 * song nothing could be read about is kept, never removed. */
export interface WeeklyKeepRead {
  signal: 'navidrome_star' | 'schall_keep';
  outcome: 'kept' | 'absent' | 'unreadable';
  detail: string;
  readAt: string;
}

// The new-releases playlist: what following an artist or a label actually
// buys, as something to press play on. It moves playlist rows only — no
// file, no want and no decision is in reach of it, so there is nothing here
// to consent to before it happens; it ships switched on.
export interface NewReleasesSettings {
  enabled: boolean;
  windowDays: number;
}

export interface NewReleasesOverview {
  settings: NewReleasesSettings;
  playlistId: string | null;
  name: string;
  trackCount: number;
}

// Where Schall files the music it keeps. Saving one moves nothing: the library
// on disc stays where it is until a migration is asked for.
export interface LibraryLayout {
  template: string;
  default: string;
  // Where the template would file one invented release, rendered by the code an
  // import would use.
  example: string;
  isDefault: boolean;
  updatedAt?: string;
}

// A pass that writes what Schall proved into the files the library already
// holds. `eligible` is every file matched to a catalogue track, which is what a
// pass covers; a file matched to nothing is never read or written.
export interface LibraryTagRun {
  status: 'idle' | 'queued' | 'running' | 'completed' | 'failed' | 'cancelled';
  error?: string;
  startedAt?: string;
  completedAt?: string;
  written: number;
  // Files that already said exactly what Schall would have written.
  unchanged: number;
  // Files it would have had to guess about, left as they are.
  skipped: number;
  // The two reasons a file is left alone, both counted in `skipped` as well:
  // the file answers the same recording on more than one release, or the
  // catalogue holds too little about it to describe it.
  skippedAmbiguous: number;
  skippedUnproved: number;
  failed: number;
  eligible: number;
}

// One planned or finished rename. from and to are both absolute: a move is
// judged by where the file goes, not by a fragment of it.
export interface LibraryMove {
  id: string;
  libraryFileId: string;
  fromPath: string;
  toPath: string;
  status: 'planned' | 'moved' | 'failed' | 'reverted';
  error?: string;
  movedAt?: string;
}

// A migration of the library to the current layout: what is left, what
// happened, and the first page of the rows.
export interface LibraryLayoutRun {
  runId: string;
  plannedAt: string;
  planned: number;
  moved: number;
  failed: number;
  reverted: number;
  // Whether files are still waiting to be renamed, which is also what makes the
  // view worth refetching.
  active: boolean;
  moves: LibraryMove[];
  // What planning decided, answered by the plan itself and by nothing
  // afterwards: the journal records moves, and a file that is not moving leaves
  // no row to count later.
  plan?: {
    planned: number;
    // Files that would land on a path another file is already going to.
    colliding: number;
    // Files already where the template files them.
    inPlace: number;
    // Files Schall did not file, under a template that asks for the identifier
    // it would have filed them under.
    unfiled: number;
    // Files recorded as gone from the disc.
    absent: number;
  };
}

// The bit rates a re-encode may be written at, in the order a picker shows
// them. 'V0' is LAME's variable-rate setting, which spends bits where the
// music needs them rather than at a fixed rate.
export const transcodeBitrates = [
  'V0',
  '128',
  '160',
  '192',
  '224',
  '256',
  '288',
  '320'
] as const;

export type TranscodeBitrate = (typeof transcodeBitrates)[number];

export interface ImportSettings {
  // What Schall does with the provider's copy once it has imported a release.
  // 'keep' is the default and takes nothing away.
  sourceRetention: 'keep' | 'delete';
  inboxPath?: string;
  // Whether Schall could remove anything from that folder at all. The
  // completed-download folder is usually mounted read-only.
  inboxWritable: boolean;
  inboxDetail?: string;
  // Whether an AcoustID key is stored, and whether imports are verified against
  // the audio. The key itself is never sent back to the interface.
  acoustidKeySet: boolean;
  acoustidEnabled: boolean;
  // Whether a higher format is re-encoded to a smaller one on the way into the
  // library, and how. It runs after a file is proven and imported, never
  // before, so it is never evidence of what a file is. 'mp3' is the only
  // target implemented. 'lossless' re-encodes only a format that lost nothing
  // to begin with; 'above_target' also takes a lossy file whose bit rate is
  // higher than the target's.
  transcodeEnabled: boolean;
  transcodeTarget: 'mp3';
  transcodeBitrate: TranscodeBitrate;
  transcodeWhen: 'lossless' | 'above_target';
  // Whether an encoder was found on this machine. transcodeEnabled cannot be
  // saved as true without one; this is why, and it is shown rather than
  // discovered as a background job that never does anything.
  transcoderPresent: boolean;
  transcoderDetail?: string;
  updatedAt?: string;
}

// What one pass over the download inbox did to one class of file. `deletes`
// says which side of the rule the class is on, so the screen does not have to
// keep its own list of the four that go.
export interface InboxCleanupClass {
  class:
    | 'imported'
    | 'refused'
    | 'settled'
    | 'unknown'
    | 'kept_question'
    | 'kept_open'
    | 'kept_recent'
    | 'kept_unconfirmed'
    | 'kept_undecided'
    | 'kept_unsafe_path';
  files: number;
  bytes: number;
  // What this class chose and could not remove. Those files are still there.
  failed: number;
  deletes: boolean;
}

// One pass over the download inbox. A dry run classifies every file and deletes
// none of them; a real one deletes the four classes marked `deletes`. The file
// rows behind these counts are the record and are not served.
export interface InboxCleanup {
  id: string;
  requestedBy?: string;
  requestedAt: string;
  dryRun: boolean;
  status: 'queued' | 'running' | 'finished' | 'failed';
  startedAt?: string;
  finishedAt?: string;
  error?: string;
  classes: InboxCleanupClass[];
  deleteFiles: number;
  deleteBytes: number;
  keptFiles: number;
  keptBytes: number;
  failedFiles: number;
}

// A pass that shrinks the files already in the library under the stored
// transcoding policy, the same shape LibraryTagRun is: idle until asked for,
// then a batch at a time. eligible is every present file with a resolved
// MusicBrainz recording the policy would still shrink, whether or not a pass
// has ever been asked for.
export interface TranscodeSweep {
  status: 'idle' | 'queued' | 'running' | 'completed' | 'failed' | 'cancelled';
  error?: string;
  startedAt?: string;
  completedAt?: string;
  transcoded: number;
  skipped: number;
  failed: number;
  eligible: number;
}

export interface SourceFile {
  // The remote name the peer advertised. A download request must carry it,
  // because that is what a transfer asks for.
  path: string;
  name: string;
  extension: string;
  sizeBytes: number;
  bitRate?: number;
  durationSeconds?: number;
  variableBitRate: boolean;
}

// How strongly a candidate is evidenced as the release that was searched for.
// Deliberately separate from `score`: this says whether the music is right,
// `score` says only how good a copy it is. `checked` is false when the
// catalogue had no tracks to compare against, which is not the same as
// evidence that the candidate is wrong.
export interface SourceMatch {
  checked: boolean;
  expected: number;
  confirmed: number;
  partial: number;
  absent: number;
  complete: boolean;
  summary: string;
}

export interface SourceCandidate {
  provider: string;
  username: string;
  directory: string;
  trackCount: number;
  totalSizeBytes: number;
  format: string;
  averageBitRate?: number;
  freeUploadSlot: boolean;
  queueLength: number;
  uploadSpeed: number;
  score: number;
  match: SourceMatch;
  reasons: string[];
  files: SourceFile[];
}

export interface SourceResults {
  query: string;
  expectedTrackCount: number;
  items: SourceCandidate[];
}

// One release of a bulk search. 'none' and 'failed' are deliberately separate:
// no peer sharing it and nobody having looked are different answers, and only
// one of them is worth trying again.
// One copy the stored format preferences kept a search from fetching.
export interface SourceRefusal {
  username: string;
  directory: string;
  format: string;
  // The sentence to show. 'kind' is the rule that produced it: 'bit_rate' for
  // the floor, 'format' for the never-fetch list.
  reason: string;
  kind: 'format' | 'bit_rate';
}

export interface SourceSearchResult {
  albumId: string;
  albumTitle: string;
  artistId: string;
  artistName: string;
  trackCount: number;
  ownedTrackCount: number;
  firstReleaseDate: string;
  // 'cancelled' is a release the run was stopped before reaching. It is not an
  // answer and never becomes one: nothing looked for it, and nothing will.
  status: 'queued' | 'searching' | 'found' | 'none' | 'failed' | 'cancelled';
  query: string;
  candidates: SourceCandidate[];
  // The copies the format preferences took out of this search. A release whose
  // every offer was under the bitrate floor found nothing, and that is a
  // different answer from nobody sharing it.
  refused: SourceRefusal[];
  // How many of those were under the bitrate floor.
  refusedBelowBitRate: number;
  error?: string;
  searchedAt: string | null;
  // The download already recorded for this release, if there is one. A review
  // reopened after acting on it says so rather than inviting a second request
  // for the same folder.
  requestedDownloadId?: string;
  // When the run recorded that download itself, if it did. A download nobody
  // remembers asking for has to be able to say where it came from.
  autoRequestedAt: string | null;
}

// What the ranking should prefer, and what it must never fetch. An installation
// that has said nothing has two empty lists and no floor, which ranks exactly as
// Schall always has: lossless first, then lossy by bitrate.
// What the collection weighs and what the discs holding it have left.
//
// A volume that could not be measured says so and carries no figures. Printing
// a number nothing measured would be worse than the gap.
/** One thing that happened, or a run of the same thing. Everything here was
 * already recorded elsewhere; the feed reads it and writes nothing. */
export interface ActivityEvent {
  kind: 'arrived' | 'refused' | 'asked' | 'removed';
  /** When it happened, and for a run the most recent of them. */
  at: string;
  /** When a run started. The same as `at` for a single event. */
  since: string;
  /** How many ran together. One for a single event. */
  count: number;
  title?: string;
  artist?: string;
  /** The sentence the record already carried — a verdict's own summary, or a
   * removal licence's reason. Never composed by the interface. */
  detail?: string;
  /** Where this can be acted on, empty where there is nothing to act on. */
  href?: string;
}

export interface Activity {
  items: ActivityEvent[];
  windowHours: number;
}

export interface Storage {
  collectionBytes: number;
  volumes: StorageVolume[];
}

export interface StorageVolume {
  // The music folders on this disc. Two folders on one disc are one entry.
  roots: string[];
  totalBytes: number;
  freeBytes: number;
  measured: boolean;
  error?: string;
}

export interface SourcePreferences {
  // Formats to put first, best first.
  preferred: string[];
  // Formats never to fetch. This is a floor, not a preference: a copy in one of
  // these is taken out of the results rather than ranked low.
  unacceptable: string[];
  // The lowest bitrate a lossy copy may have, in kbps, where its format has no
  // floor of its own. Zero is no floor.
  minimumBitRate: number;
  // The floor for one named format, keyed by the bare extension: { mp3: 320 }.
  // A format named here is held to its own number instead of minimumBitRate.
  formatMinimumBitRate: Record<string, number>;
  // Every format the screen may offer, so the list is the server's and there is
  // no second copy to disagree with it.
  known: string[];
  // The formats that carry a bitrate, each with the highest floor it can take.
  // A format missing from here keeps every bit of the recording and has no
  // bitrate to set a floor on.
  lossy: Record<string, number>;
}

export type SourcePreferencesInput = Omit<SourcePreferences, 'known' | 'lossy'>;

// Whether the sweep that looks for library files below the source-preference
// bit-rate floor is switched on, and how many files it would find right now.
export interface UpgradeSettings {
  enabled: boolean;
  belowFloor: number;
}

// Whether Schall settles the recordings the library holds more than once by
// itself. It only settles what it can prove: copies that are the same audio,
// or one copy proven better than every other. Off by default; a save with
// `enabled: true` queues a pass over the library, the same way turning on the
// upgrade sweep does.
export interface DuplicateResolutionSettings {
  enabled: boolean;
}

export interface SourceSearchRun {
  id: string;
  // How many releases have not been searched yet. The run is done at zero.
  pending: number;
  total: number;
  foundCount: number;
  noneCount: number;
  // Stopped before anyone looked, as against looked for and not managed.
  cancelledCount: number;
  failedCount: number;
  requestedAt: string;
  items: SourceSearchResult[];
}

// What a bulk press on a selection of releases came to, one row per release.
//
// Every one of them is per release on purpose. A page-level "done" would hide
// the ordinary case: most of a selection was already decided, and the reader
// wants to know how much of it their press actually changed.
export interface IgnoredRelease {
  albumId: string;
  dismissed: number;
  alreadyDismissed: number;
  owned: number;
  unresolvable: number;
}

export interface IgnoredReleases {
  releases: IgnoredRelease[];
  // Releases the catalogue no longer holds. A selection is read from a page
  // drawn a moment earlier.
  notFound: string[];
}

export interface RematchedRelease {
  releaseId: string;
  // False where a pass was already queued or already running for this release.
  queued: boolean;
}

export interface RematchedReleases {
  releases: RematchedRelease[];
  // Releases there is nothing to ask MusicBrainz about. Kept apart from a pass
  // already running, because the two read the same and are not the same.
  unaskable: string[];
}

export interface RetriedRelease {
  releaseId: string;
  retried: number;
}

export interface RetriedReleases {
  releases: RetriedRelease[];
}

export interface DownloadRequestFile {
  path?: string;
  name: string;
  extension: string;
  sizeBytes: number;
  bitRate?: number;
  durationSeconds?: number;
  // What became of this file, once a transfer for it exists.
  transfer?: DownloadFileTransfer;
}

export interface DownloadFileTransfer {
  // The remote name, which is what a retry has to ask for.
  path: string;
  status: 'queued' | 'searching' | 'downloading' | 'importing' | 'completed' | 'failed' | 'cancelled';
  transferredBytes: number;
  error?: string;
  // Whether this file alone can be asked for again.
  retryable: boolean;
  // Which try this file is on, and what the settled ones came to. A file that
  // arrived first time is attempt 1 with a single entry.
  attempt: number;
  attempts: TransferAttempt[];
}

export interface TransferAttempt {
  attempt: number;
  outcome: 'completed' | 'failed' | 'cancelled';
  detail?: string;
  transferredBytes: number;
  recordedAt: string;
}

/** One want waiting on a person.
 *
 * `kind` says which question it is. 'copies' asks whether one of these files is
 * the recording; 'resolution' asks which recording the entry names at all, and
 * carries `candidates` instead — an entry that fits several recordings never got
 * as far as fetching anything; 'stopped' is a want waiting for a person after a
 * copy was imported under a different recording or every copy disagreed only on
 * artist credit. `ruledOut` counts the offers already struck off for this want.
 */
export interface ReviewItem {
  kind: 'copies' | 'resolution' | 'stopped';
  target: AcquisitionTarget;
  copies: AcquiredCopy[];
  candidates: AcquisitionCandidate[];
  ruledOut: number;
  /** The identity already recorded for a stopped want's imported file. */
  fileRecordingId?: string;
  fileTitle?: string;
  fileArtist?: string;
  fileReleaseTitle?: string;
  /** Which of the copies are the same piece of audio, as the server measured
   * it. Every copy is named by exactly one group, and a copy nothing could
   * compare is a group of one. It says how to read the list and nothing about
   * what may be accepted. */
  copyGroups?: CopyGroup[];
  /** Set when the want is not asking anything yet: something could still
   * arrive or recover, and it is judged again by itself when it does.
   * 'musicbrainz' is a copy whose audio named another recording nobody could
   * ask about; 'library_identity' is a file the library has not finished
   * saying what it is. Absent means the evidence is in and a person is the
   * only thing that can move it. */
  waitingFor?: 'musicbrainz' | 'library_identity';
  /** When the next automatic look is due, for the kind that waits on a clock. */
  waitingUntil?: string;
}

/** One set of copies that sound alike. `rips` is how many separate rips the set
 * holds rather than how many files: copies of identical size are one upload that
 * spread, so they count once. */
export interface CopyGroup {
  copyIds: string[];
  rips: number;
}

/** One recording an entry could name, with what agreed and what did not. The
 * lists are the grader's own verdicts, read back rather than worked out again:
 * this is the evidence that could not choose, shown to somebody who can. */
export interface AcquisitionCandidate {
  recordingId: string;
  releaseGroupId?: string;
  artistName: string;
  releaseTitle?: string;
  trackTitle: string;
  durationMs: number | null;
  isrc: string | null;
  rank: number;
  agrees: string[];
  differs: string[];
  summary: string;
}

export interface ReviewQueue {
  items: ReviewItem[];
  total: number;
  limit: number;
  offset: number;
}

/** The recording a want is waiting for. Only the fields the review queue and
 * the list of wants read are declared; the endpoint returns the whole target. */
export interface AcquisitionTarget {
  id: string;
  origin: string;
  artist: string;
  title: string;
  album: string;
  durationMs: number | null;
  isrc: string | null;
  /** The album the target came from, when it did; the review cover is `/albums/{id}/cover`. */
  originAlbumId?: string;
  /** The source saying the track asked for is the explicit recording. Absent
   * where no source said. It only ever excludes a clean recording from being
   * the answer; nothing is shown for it. */
  entryExplicit?: boolean;
  recordingId: string | null;
  status: string;
  summary: string;
  attempts: number;
  /** When somebody asked for this recording. */
  createdAt?: string;
  /** When the want reached three consecutive below-floor answers. */
  belowFloorSince?: string;
  /** When the person waived the bitrate floor for this want. */
  floorWaivedAt?: string;
  /** When a copy was last looked for, and when the next look is due. Both are
   * null on a want nothing has searched for yet. */
  lastAttemptAt?: string | null;
  nextAttemptAt?: string | null;
  /** What went wrong on the last attempt, if anything did. */
  lastError?: string;
  /** Why the sample is unavailable, when the want has no sample. */
  anchorUnavailable?: string;
  /** How many times Schall has tried to get a sample. */
  anchorAttempts: number;
  /** When Schall will try to get the sample again, when it is deferred. */
  anchorNextAttemptAt?: string | null;
  /** Nothing is looking for this want and nothing will: it holds a copy that is
   * already a file, and the library calls that file a different recording. Only
   * the list of wants works this out. */
  waitingOnYou?: boolean;
  /** Where the file this want exists to replace sits, set only when origin is
   * "upgrade". Empty once the replacement has already happened. */
  upgradeOfPath?: string;
  source?: string;
  externalId?: string;
  externalUrl?: string;
  sourceLookup?: SourceLookup;
  minimumBitrate?: number;
}

/** A page of wants, and how many there are in the states that were asked for. */
export interface AcquisitionTargets {
  items: AcquisitionTarget[];
  total: number;
  limit: number;
  offset: number;
  /** One line the list carries while wants are waiting and nothing on the
   * installation could prove a copy is the recording somebody asked for. The
   * API sends it only while it is true, so it is shown whenever it is here. */
  notice?: string;
}

/** The states a want passes through while nobody has to do anything about it:
 * waiting to be resolved to a recording, waiting for the next attempt, and out
 * searching this minute. One pile to whoever asked for the music. */
export const lookingFor = ['unresolved', 'pending', 'searching'] as const;

/** One copy a peer offered for a want, and what it came to. */
export interface AcquiredCopy {
  id: string;
  provider: string;
  username: string;
  path: string;
  name: string;
  sizeBytes?: number;
  verdict: 'fetching' | 'undelivered' | 'accepted' | 'discarded_audio' | 'discarded_tags' | 'held';
  decidedBy: 'schall' | 'user';
  summary: string;
  evidence?: AcquiredCopyEvidence;
  decidedAt: string;
  origin?: { kind: 'soulseek' | 'address'; label: string };
  /** The library file this copy became, once the scan has made one. It is what
   * lets a screen point at the file rather than only describe it. */
  libraryFileId?: string;
}

/** Everything one copy came to against the recording that was wanted.
 * `observed` is the file's own account of itself and `wanted` is
 * MusicBrainz's, which is what makes a field comparable at all. */
export interface AcquiredCopyEvidence {
  name: string;
  observed?: ImportTags;
  wanted?: ImportTags;
  acoustic?: AcquiredCopyAcoustic;
  anchor?: AcquiredCopyAnchor;
  witness?: AcquiredCopyWitness;
  agrees: string[];
  differs: string[];
  method?: string;
  problems: string[];
  sizeBytes?: number;
  bitRate?: number;
  audio?: AcquiredCopyAudio;
}

/** What this copy's own sound turned out to be: where the music stops, what the
 * file says made it, and whether the two together look like audio that was
 * squeezed before it reached this container. Quality and never identity —
 * nothing that admitted or refused the copy read any of it. */
export interface AcquiredCopyAudio {
  cutoffHz?: number;
  encoder?: string;
  transcodeSuspected?: boolean;
}

export interface AcquiredCopyAcoustic {
  recordingIds: string[];
  /** AcoustID's leading cluster, which is the only cluster that can identify
   * the copy. */
  clusters?: { recordingIds: string[]; score: number }[];
  agrees?: boolean;
  unavailable?: string;
  /** What the decoder complained about while reading audio it got through
   * anyway. The fingerprint covered the file regardless, so this says something
   * about the quality of the copy and nothing about which recording it is. */
  damaged?: string;
}

/** This copy beside the short sample of the wanted recording. It is the second
 * thing that can speak about the audio itself, and unlike AcoustID it works for
 * music no catalogue has ever heard of. Absent means the want has no sample.
 *
 * The sample is the distributor's own excerpt, fetched by the recording's
 * registration code. For a want no distributor carries it is instead a YouTube
 * upload chosen by the want's name and length. A plain upload has source
 * 'youtube' and leaves a copy a question; a matching Topic upload has source
 * 'youtube-topic' and can admit it. Neither source refuses a copy. */
export interface AcquiredCopyAnchor {
  source?: string;
  reference?: string;
  seconds?: number;
  /** The upload's own title and channel, and how many views it had when it was
   * chosen. Only for a sample found by name, and shown because such a sample can
   * be another version of the song and the reader is who can tell. */
  label?: string;
  views?: number;
  /** How far apart the two pieces of audio measured, and whether the comparison
   * ran at all. Zero is a real answer, so `measured` is what tells them apart. */
  rate: number;
  measured: boolean;
  /** Set only where the comparison reached one of its two answers. Absent means
   * nothing was concluded. */
  agrees?: boolean;
  unavailable?: string;
}

/** This copy beside a file already in the library that is proven to be the
 * wanted recording. `path` is that file and `proof` is what settled it, because
 * a copy admitted this way is admitted on the older file's proof. Absent means
 * the library holds no settled file for the recording. */
export interface AcquiredCopyWitness {
  fileId?: string;
  path?: string;
  proof?: string;
  rate: number;
  measured: boolean;
  /** Set only where the copy holds the same music. It is never false: a copy
   * unlike an owned file may be another master of the same recording, so this
   * only ever admits. */
  agrees?: boolean;
  considered?: number;
  unavailable?: string;
}

export interface DownloadRequest {
  id: string;
  // A request is about a release somebody chose a folder for, or about one
  // wanted recording a file is being looked for. The release fields are absent
  // on the second kind, and the entry names the music instead.
  albumId?: string;
  albumTitle?: string;
  artistId?: string;
  artistName?: string;
  acquisitionTargetId?: string;
  entryArtist?: string;
  entryTitle?: string;
  provider: string;
  username: string;
  directory: string;
  status: 'requested' | 'started' | 'completed' | 'failed' | 'cancelled';
  fileCount: number;
  expectedTrackCount: number;
  totalSizeBytes: number;
  format: string;
  averageBitRate?: number;
  score: number;
  reasons: string[];
  files: DownloadRequestFile[];
  progress: DownloadProgress;
  startable: boolean;
  // How many files failed and can be asked for again without re-downloading
  // the ones that arrived.
  retryableCount: number;
  requestedAt: string;
  startedAt: string | null;
  cancelledAt: string | null;
  error?: string;
  // 'discarded' is a copy fetched for a want that was not the recording it was
  // fetched for. Nothing is waiting on it and nobody has to look at it.
  importStatus: 'pending' | 'validating' | 'imported' | 'needs_review' | 'discarded';
  importError?: string;
  importPath?: string;
  importedAt: string | null;
  importReviews: ImportReview[];
  // The comparison behind the current pause, present only while the import is
  // waiting for review.
  importEvidence?: ImportEvidence;
  // Per-track judgements recorded so far. They apply the next time validation
  // runs; recording one imports nothing by itself.
  importDecisions: ImportDecision[];
  revalidatable: boolean;
  // Present when this request was made against a library that already held
  // something of the release, together with what the person who asked for it
  // anyway was shown.
  duplicateAcknowledgedAt?: string;
  duplicateEvidence?: DuplicateEvidence;
  // The last thing this peer said in private chat that Schall could not read.
  // Carried only while the files could still be held by it: a request that was
  // imported or called off gets none.
  peerQuestion?: PeerQuestion;
}

// One message from a peer, kept as the peer wrote it. Schall reads the
// challenges it knows and sends the word back by itself, and it knows the notes
// those peers send as well; this is what is left over, and what to do about it
// is a person's to decide.
export interface PeerQuestion {
  username: string;
  message: string;
  askedAt: string;
}

// What the library already holds against one release. An owned file proves the
// release is partly there; an unresolved file proves only that something says
// it is this release and nothing settled which track it is. Neither decides the
// request — they are what the user is shown so the user can decide.
export interface DuplicateEvidence {
  albumId: string;
  albumTitle: string;
  artistName: string;
  trackCount: number;
  ownedTrackCount: number;
  unresolvedCount: number;
  // Reads as one sentence, for one file as well as for many.
  summary: string;
  files: DuplicateFile[];
}

export interface DuplicateFile {
  id: string;
  path: string;
  state: 'owned' | 'unresolved';
  matchStatus: 'matched' | 'unmatched' | 'ambiguous' | 'duplicate';
  // The catalogue track an owned file is matched to.
  track?: string;
  // What the file itself says, when it says anything.
  tags?: string;
  // Why this file counts against the request, as a sentence.
  evidence: string;
}

// One entry in an import's review history, oldest first. The latest reason is
// also on importError; this is the record of every earlier one.
export interface ImportReview {
  kind:
    | 'paused'
    | 'revalidated'
    | 'imported'
    | 'resolved'
    | 'withdrawn'
    | 'source_removed'
    | 'source_retained'
    | 'discarded';
  detail?: string;
  recordedAt: string;
  evidence?: ImportEvidence;
}

// Everything one validation attempt observed: each downloaded file beside the
// catalogue track it claimed, plus the tracks nothing claimed at all.
export interface ImportEvidence {
  files: ImportFileEvidence[];
  unmatchedTracks: ImportTags[];
  problems: string[];
}

export interface ImportFileEvidence {
  name: string;
  position: string;
  observed?: ImportTags;
  expected?: ImportTags;
  // How this file was tied to the track in expected. Absent when nothing
  // matched it, which is what candidates is then for.
  match?: ImportMatch;
  // The tracks an unmatched file might still be, best first. Matching refused
  // to choose between them; review can.
  candidates?: ImportCandidate[];
  problems: string[];
  // Whether a person could settle this file by naming the track it belongs to.
  // False for a file that is missing, damaged, or unreadable.
  resolvable: boolean;
  decision?: ImportFileDecision;
}

// How one downloaded file was identified. Notes are what the file says that
// the catalogue does not — recorded because it is worth knowing, not because
// it stood in the way.
export interface ImportMatch {
  method: 'recording-id' | 'isrc' | 'position-and-title' | 'title-and-duration';
  summary: string;
  notes?: string[];
}

// A catalogue track one file might be, with the evidence for and against it.
export interface ImportCandidate {
  track: ImportTags;
  agrees: string[];
  differs: string[];
  takenBy?: string;
}

export interface ImportFileDecision {
  trackId: string;
  trackTitle: string;
  note?: string;
  decidedAt: string;
}

export interface ImportDecision {
  id: string;
  fileName: string;
  trackId: string;
  trackTitle: string;
  note?: string;
  decidedAt: string;
}

export interface ImportTags {
  trackId?: string;
  artist?: string;
  album?: string;
  title?: string;
  discNumber?: number;
  trackNumber?: number;
  durationMs?: number;
  musicbrainzRecordingId?: string;
  isrc?: string;
}

export interface DownloadProgress {
  transferCount: number;
  completedCount: number;
  failedCount: number;
  /** Files the peer has accepted but not begun sending. A request whose files
   * are all queued is waiting on somebody else's upload slot, which is not the
   * same as one that is moving — and reading `Downloading` at 0 B for an hour
   * is how it looked before this was carried through. */
  queuedCount: number;
  transferredBytes: number;
}

/** The piles the downloads list is read in. `open` is a transfer still under
 * way; the three after it are what became of a copy once it arrived; `failed`
 * is a transfer that did not; `all` is every request, whichever pile it is in
 * and whether or not it is in one. */
export type DownloadView = 'open' | 'review' | 'imported' | 'discarded' | 'failed' | 'all';

/** How many requests each pile holds. The server counts them in the same pass
 * that reads the page, so a figure on a filter and the rows behind it are one
 * answer. */
export interface DownloadCounts {
  open: number;
  review: number;
  imported: number;
  discarded: number;
  failed: number;
  all: number;
}

export interface DownloadFilters {
  view?: DownloadView;
  limit?: number;
  offset?: number;
}

export interface DownloadRequests {
  items: DownloadRequest[];
  total: number;
  limit: number;
  offset: number;
  counts: DownloadCounts;
}

// SourceOffer is what the peer offers for a recorded folder now. `offered` is
// false when the peer said nothing at all, which is what being offline looks
// like — it is not a report that the folder is gone.
export interface SourceOffer {
  offered: boolean;
  missing: string[];
  changed: string[];
  stale: boolean;
}

/** What an upload came to. `staged` is one whose bytes are on the volume with
 * nothing queued to look at them — a restart caught it between the last byte
 * landing and the job being written, and startup asks for it again. `failed`
 * keeps its files, so the reason and the files it names are both still there.
 */
export type UploadStatus = 'staged' | 'queued' | 'validating' | 'imported' | 'failed';

export type UploadFileState = 'importing' | 'imported' | 'waiting' | 'set_aside' | 'failed';

/** One file in an upload, read back from its exact landed path once scanning
 * has made a library row for it. */
export interface UploadFile {
  name: string;
  state: UploadFileState;
  file?: LibraryFile;
}

/** One upload of music the user already owns. Nothing here is a claim about
 * what the files are: an upload is filed into the library and identified
 * afterwards, by the same resolution every other file on the volume goes
 * through. */
export interface Upload {
  id: string;
  status: UploadStatus;
  files: string[];
  sizeBytes: number;
  fileStates?: UploadFile[];
  /** Where it was filed, once it was. */
  importPath?: string;
  /** Why it failed, in the words the import recorded. */
  detail?: string;
  /** Whether the bytes are still in the staging folder. */
  staged: boolean;
  createdAt: string;
  completedAt?: string;
}

export interface UploadList {
  items: Upload[];
  /** Shown so that an operator can see which volume is filling up. */
  stagingPath: string;
  /** The most one upload may carry, so a selection can be refused before an
   * hour is spent on it. */
  maxBytes: number;
}

/** The five lanes the worker divides its work into: searching, downloads,
 * anchors, judging, and everything else. */
export type JobLane = 'acquisition' | 'transfers' | 'anchors' | 'judging' | 'general';

/** One job as the queue reads it. `error` is what the last attempt failed with,
 * which a queued job carries while it waits out its backoff — so a queued job
 * with an error is between retries rather than new.
 *
 * `subject` is what the job is about, resolved by the API from the identifiers
 * in its payload: the release, artist, file or list behind it, with
 * `subjectArtist` beside it where the thing has one and `subjectFileCount`
 * where the job knows how many files it carries. All three are absent for a
 * kind whose payload names nothing and for one whose subject has since been
 * deleted, and absent is the answer: the row reads as its bare kind rather
 * than as a kind and an identifier. */
export interface Job {
  id: string;
  kind: string;
  lane: JobLane;
  status: 'queued' | 'running';
  attempts: number;
  maxAttempts: number;
  runAfter: string | null;
  startedAt?: string;
  createdAt: string | null;
  error?: string;
  subject?: string;
  subjectArtist?: string;
  subjectFileCount?: number;
}

/** One lane and what is on it. The counts are of the lane rather than of the
 * list, so a truncated answer still says how much is behind it. */
export interface JobLaneQueue {
  lane: JobLane;
  label: string;
  runningCount: number;
  queuedCount: number;
  jobs: Job[];
}

export interface JobQueue {
  lanes: JobLaneQueue[];
  /** Every running and queued job there is, listed or not. */
  total: number;
  /** The lists are the head of the queue rather than all of it. */
  truncated: boolean;
}

/** A job that spent every attempt. `subject` is what it was about, read the
 * same way a running job's is; `payload` is that as the queue recorded it, and
 * it stays — the subject is what somebody reads, the payload is what they
 * quote, and the payload is the only thing left when whatever it named has
 * since been deleted. */
export interface FailedJob {
  id: string;
  kind: string;
  lane: JobLane;
  attempts: number;
  maxAttempts: number;
  error: string;
  /** Which group of failures this job belongs to, decided by the server from
   * the error it recorded. Nothing here reads a Go error string. */
  cause: string;
  failedAt: string | null;
  createdAt: string | null;
  payload?: Record<string, unknown>;
  subject?: string;
  subjectArtist?: string;
  subjectFileCount?: number;
}

/** One reason jobs stopped, and how much of the failed list it accounts for.
 * `title` says what happened and `detail` says what the named thing is before
 * it says what went wrong with it; both are written by the server, because the
 * cause is read out of a Go error and a browser must never be the thing that
 * reads one. */
export interface FailureCause {
  cause: string;
  title: string;
  detail: string;
  service?: string;
  jobCount: number;
  firstFailedAt: string | null;
  lastFailedAt: string | null;
}

export interface FailedJobs {
  /** The causes, biggest first. Each job in `items` carries the cause it
   * belongs to. */
  causes: FailureCause[];
  items: FailedJob[];
  total: number;
  truncated: boolean;
}

/** What became of a press on "Retry all". `requeued` is how many jobs moved,
 * which is not always how many were asked for: a job already on its way is left
 * alone. */
export interface RetriedCause {
  cause: string;
  asked: number;
  requeued: number;
  detail: string;
}

/** One of the passes that runs on its own schedule. `nextRunAt` is the queued
 * row's own due time and nothing else: a pass with nothing queued has no next
 * time, and `detail` says why rather than leaving the absence to be read as a
 * stopped worker. */
export interface RecurringJob {
  kind: string;
  label: string;
  lastRunAt: string | null;
  lastCompletedAt: string | null;
  running: boolean;
  runningSince?: string;
  nextRunAt: string | null;
  detail?: string;
}

export interface JobSchedule {
  items: RecurringJob[];
}

/** What became of a press on Retry. `requeued` is false when the job was
 * already on its way, which is the second press and not a failure. */
export interface RetriedJob {
  job: Job;
  requeued: boolean;
  detail: string;
}

/** What became of a press on Cancel. `cancelled` is false when nothing queued
 * was found under that id — the second press, or a job that started running
 * in the moment between the two — which is not a failure. */
export interface CancelledJob {
  job: Job;
  cancelled: boolean;
  detail: string;
}

/** What became of a press on Cancel all. `cancelled` is how many rows moved,
 * which answers zero both when nothing of that kind was queued and when the
 * kind named a recurring sweep's own schedule — `refused` says which. */
export interface CancelledKind {
  kind: string;
  cancelled: number;
  refused: boolean;
  detail: string;
}

// A file read but not yet imported: what the reading found, for somebody to
// look at before anything is created. Rows and skipped lines both come back
// verbatim, exactly as the file said them.
export interface PlaylistFileRow {
  position: number;
  artist: string;
  title: string;
  album: string;
  durationMs: number;
  isrc: string;
  // A file the row named outright, if it named one. Whether the library
  // holds it is answered only by the import, not by this preview.
  path: string;
}

export interface PlaylistFileSkippedRow {
  line: number;
  reason: string;
}

export interface PlaylistFilePreview {
  name: string;
  format: 'csv' | 'm3u';
  columns: string[];
  rowCount: number;
  rows: PlaylistFileRow[];
  skipped: PlaylistFileSkippedRow[];
}

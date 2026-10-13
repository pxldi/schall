/** What the interface says when a request fails.
 *
 * A request that fails answers with a problem document: a `title`, a status,
 * and sometimes a `details` list. The title is a Go sentinel — a sentence
 * written for somebody reading the source, and `CLAUDE.md` asks it to read as a
 * full sentence, which it does. "album has no MusicBrainz release group
 * identity" is right in Go and wrong on screen, because a person reading it is
 * in the middle of a task and the sentence would sit unchanged in the README.
 *
 * Eighty-three places used to draw that sentinel straight onto the page. They
 * now draw what this module returns: one sentence saying what happened to the
 * thing in front of the reader, one saying what to do and naming a control they
 * can see, and the server's own words kept behind a disclosure so a bug report
 * is still possible.
 *
 * There is no error code in the problem document, so the sentence has to be
 * found by the words. That is fragile in one direction only: a sentinel
 * reworded in Go would fall through to the status backstop, and quietly. So
 * `errors.sentinels.test.ts` reads every key below out of the Go source and
 * fails the build when one is no longer there.
 */

import { ApiError, DuplicateProtection, SourceChanged } from '$lib/api';

/** How the notice is drawn, in the roles `StatusBadge` already knows.
 *
 * `fail` is something that went wrong. `decide` is a refusal — Schall could
 * have done it and would not, because something else is on record. A refusal is
 * not a fault and is never drawn as one. */
export type ErrorRole = 'fail' | 'decide';

export interface ErrorNotice {
  /** What happened to the thing the reader is looking at. */
  sentence: string;
  /** What to do about it, naming a control on screen. Empty only where nothing
   * a person does changes the outcome and the machinery carries on by itself,
   * in which case `retrying` says so instead. */
  action: string;
  /** The exact words the server answered with, for the disclosure. Never
   * dropped, whichever layer wrote the sentence. */
  raw: string;
  role: ErrorRole;
  /** Which layer wrote the sentence: the words a failure carried, the status it
   * answered with, or neither. `none` is the catch-all. Whether a screen may
   * say better is `subjectless`, not this. */
  layer: 'sentinel' | 'status' | 'none';
  /** Whether the sentence names the thing that failed. Two of them do not:
   * the catch-all, and the one a 500 gets, which both say only that something
   * did not finish. On a page of one panel that is enough; on a page of six it
   * does not say which panel, so a screen that knows may say instead. Every
   * other sentence is written for this failure and is not overruled. */
  subjectless: boolean;
  /** Whether asking again may answer differently. A reader is never handed a
   * button whose only job is to repeat what just failed: where this is true and
   * the caller can ask again by itself, it does, and says so. */
  transient: boolean;
}

interface Written {
  sentence: string;
  action?: string;
  role?: ErrorRole;
  transient?: boolean;
  /** Set on the two sentences that name nothing. See `ErrorNotice`. */
  subjectless?: boolean;
}

/** The failures worth a sentence of their own, keyed on the exact words the Go
 * source uses. Keys are literals as they appear under `internal/`; the pinning
 * test greps for each one, so a key that is not in the source is a red build
 * rather than a silent fall-through. */
export const sentinels: Record<string, Written> = {
  // ── Nothing is set up yet ────────────────────────────────────────────────
  // Not a failure of the request so much as of the account: something the user
  // has never filled in. The action is always the settings page that holds it.
  'slskd is not configured': {
    sentence: 'No download source set up.',
    action: 'Add slskd under Settings → Connections.'
  },
  'no download source is configured': {
    sentence: 'No download source set up.',
    action: 'Add slskd under Settings → Connections.'
  },
  'could not reach slskd': {
    sentence: "slskd didn't answer.",
    action: "Check it's running, then press Test connection in Settings."
  },
  'navidrome is not configured': {
    sentence: "Navidrome isn't set up.",
    action: 'Add the Navidrome address under Settings → Connections.'
  },
  'ListenBrainz is not configured': {
    sentence: 'Recommendations need a ListenBrainz account.',
    action: 'Add the username under Settings → Connections.'
  },
  'ListenBrainz recommendations are disabled': {
    sentence: 'Recommendations are off.',
    action: 'Turn them on under Settings → Connections.'
  },
  'playlist sync is not configured': {
    sentence: 'No player to sync with.',
    action: 'Add the Navidrome address under Settings → Connections.'
  },
  'playlists are not configured': {
    sentence: 'No player to sync with.',
    action: 'Add the Navidrome address under Settings → Connections.'
  },
  'uploading is not configured': {
    sentence: 'No upload folder set.',
    action: 'Set the staging folder under Settings → Library.'
  },
  'library paths are not configured': {
    sentence: 'No library folder set.',
    action: 'Add one under Settings → Library.'
  },
  'the library layout is not configured': {
    sentence: 'No folder layout set.',
    action: 'Choose a layout under Settings → Library.'
  },
  'acquisition targets are not configured': {
    sentence: 'No download source set up.',
    action: 'Add a download source under Settings → Connections.'
  },
  'connect a Spotify account first': {
    sentence: "Spotify isn't connected.",
    action: 'Connect it under Settings → Connections.'
  },
  'enter the Spotify client credentials first': {
    sentence: 'Spotify client ID and secret missing.',
    action: 'Enter them under Settings → Connections.'
  },
  'there is nowhere to send to': {
    sentence: 'No notification address set.',
    action: 'Add one under Settings → Connections.'
  },

  // ── Refusals ────────────────────────────────────────────────────────────
  // Schall could have done it and would not, because a decision is already on
  // record. The second sentence is not optional here: without it a refusal is a
  // dead end, and `CLAUDE.md` keeps that as an explicit exception to the rule
  // that error text is short.
  'this wishlist entry is already following another copy': {
    sentence: 'Another copy is already on its way.',
    action: 'Wait for it, or cancel it on Downloads.',
    role: 'decide'
  },
  'artist is already followed': {
    sentence: 'Already following this artist.',
    action: 'Open them from Artists to change it.',
    role: 'decide'
  },
  'artist is not followed': {
    sentence: "You don't follow this artist.",
    action: 'Press Follow to track their releases.',
    role: 'decide'
  },
  'a layout migration is already planned': {
    sentence: 'A library move is already planned.',
    action: 'Apply or discard it first.',
    role: 'decide'
  },
  'that layout migration has nothing left to move': {
    sentence: 'Every file is already moved.',
    action: 'Discard the plan.',
    role: 'decide'
  },
  'this target is already finished with': {
    sentence: 'Schall is done with this song.',
    action: 'Add it again from the release to start over.',
    role: 'decide'
  },
  'music folder already exists': {
    sentence: 'That folder already exists.',
    action: 'Choose another name.',
    role: 'decide'
  },
  'this upload is still being imported': {
    sentence: 'This upload is still being read.',
    action: 'Wait for the row to update.',
    role: 'decide'
  },
  'that upload is not staged': {
    sentence: 'The upload files are gone.',
    action: 'Upload them again.',
    role: 'decide'
  },
  'this copy is not waiting for a decision': {
    sentence: 'This copy was already decided.',
    action: 'Reload to see the decision.',
    role: 'decide'
  },
  'this wishlist entry is not waiting on an answer': {
    sentence: "This song doesn't need you.",
    action: 'Reload to see where it is.',
    role: 'decide'
  },
  'this wishlist entry has no copies waiting for a decision': {
    sentence: 'No copy left to decide.',
    action: 'Reload Review.',
    role: 'decide'
  },
  'this wishlist entry has no resolution to reject': {
    sentence: 'Nothing to reject for this song.',
    action: 'Reload to see where it is.',
    role: 'decide'
  },
  'no import with that ID is waiting for review': {
    sentence: 'This import was already decided.',
    action: 'Reload Review.',
    role: 'decide'
  },
  'no decision has been recorded for this file': {
    sentence: 'Nothing to undo for this file.',
    action: 'Reload to see its state.',
    role: 'decide'
  },
  'that answer is not on record': {
    sentence: 'That decision has changed.',
    action: 'Reload to see the current one.',
    role: 'decide'
  },
  'there is nothing to retry': {
    sentence: 'Nothing left to retry.',
    action: 'Reload the page.',
    role: 'decide'
  },
  'there is nothing left to cancel': {
    sentence: 'This has already stopped.',
    action: 'Reload to see how it ended.',
    role: 'decide'
  },
  'this job was stopped rather than tried': {
    sentence: 'This job was cancelled.',
    action: 'Start it again from its page.',
    role: 'decide'
  },
  'this request cannot be retried': {
    sentence: "This download can't be retried now.",
    action: 'Reload Downloads.',
    role: 'decide'
  },
  'this request cannot be started': {
    sentence: "This download can't be started now.",
    action: 'Reload Downloads.',
    role: 'decide'
  },
  'this song is not on the weekly playlist': {
    sentence: "This song isn't on this week’s playlist.",
    action: 'Reload the playlist.',
    role: 'decide'
  },
  'only a Spotify playlist can be imported': {
    sentence: "That link isn't a Spotify playlist.",
    action: 'Paste a Spotify playlist link or ID.',
    role: 'decide'
  },

  // ── The thing is gone ───────────────────────────────────────────────────
  'artist not found': {
    sentence: 'This artist is gone.',
    action: 'Go back to Artists.'
  },
  'release not found': {
    sentence: 'This release is gone.',
    action: 'Go back to Library.'
  },
  'library file not found': {
    sentence: 'This file is gone from the library.',
    action: 'Reload the page.'
  },
  'library file or track not found': {
    sentence: 'This file is gone from the library.',
    action: 'Reload the page.'
  },
  'playlist not found': {
    sentence: 'This playlist is gone.',
    action: 'Go back to Playlists.'
  },
  'track not found': {
    sentence: 'This track is gone.',
    action: 'Reload the page.'
  },
  'recording not found': {
    sentence: 'This song is gone.',
    action: 'Reload the page.'
  },
  'copy not found': {
    sentence: 'This copy is gone.',
    action: 'Go back to Downloads.'
  },
  'download request not found': {
    sentence: 'This download is gone.',
    action: 'Go back to Downloads.'
  },
  'acquisition target not found': {
    sentence: 'This song is off the wishlist.',
    action: 'Reload the page.'
  },
  'job not found': {
    sentence: 'This job is gone.',
    action: 'Reload Jobs.'
  },
  'the file is no longer there': {
    sentence: 'The file is gone from disk.',
    action: 'Run a scan.'
  },
  'the copy is no longer there': {
    sentence: 'The downloaded copy is gone.',
    action: 'Add it again from the release.'
  },

  // ── The other end did not answer ────────────────────────────────────────
  // Transient: asking again may answer differently, so nobody is asked to press
  // a button that only repeats what just failed.
  'database unavailable': {
    sentence: "Can't reach the database.",
    transient: true
  },
  'MusicBrainz is temporarily unavailable': {
    sentence: "MusicBrainz isn't answering.",
    transient: true
  },
  'could not reach AcoustID': {
    sentence: "AcoustID isn't answering.",
    transient: true
  },
  'no AcoustID API key is configured': {
    sentence: 'No AcoustID key set.',
    action: 'Add one under Settings → Connections.'
  },
  'listenbrainz did not respond in time; check the address and try again': {
    sentence: 'ListenBrainz took too long to answer.',
    action: 'Check the address under Settings → Connections.'
  },
  'navidrome did not respond in time; check the base URL and that it is running': {
    sentence: 'Navidrome took too long to answer.',
    action: 'Check the address under Settings → Connections.'
  },
  'slskd did not respond in time; check the base URL and that slskd is running': {
    sentence: 'slskd took too long to answer.',
    action: "Check it's running, then press Test connection in Settings."
  },
  'AcoustID is rate limiting requests': {
    sentence: 'AcoustID is rate limiting.',
    action: 'Wait a minute; Schall slows down.'
  },
  'listenbrainz is being asked too often': {
    sentence: 'ListenBrainz is rate limiting.',
    action: 'Wait a minute and try again.'
  },
  'listenbrainz rejected the user token; clear it or replace it': {
    sentence: 'ListenBrainz refused the token.',
    action: 'Clear or replace it under Settings → Connections.'
  },
  'listenbrainz does not know that account': {
    sentence: 'ListenBrainz has no account by that name.',
    action: 'Check the username under Settings → Connections.'
  },
  'listenbrainz has not built a model for this account yet': {
    sentence: 'ListenBrainz has no recommendations for you yet.',
    action: 'Listen for a while and check back.'
  },
  'settings are unavailable': {
    sentence: "Couldn't load settings.",
    transient: true
  },
  'library is unavailable': {
    sentence: "Couldn't load the library.",
    transient: true
  },
  'downloads are unavailable': {
    sentence: "Couldn't load downloads.",
    transient: true
  },
  'source searches are unavailable': {
    sentence: "Couldn't load searches.",
    transient: true
  },
  'starting transfers is unavailable': {
    sentence: "Couldn't start the transfer.",
    transient: true
  },
  'could not start the transfer': {
    sentence: "The peer didn't start sending.",
    transient: true
  },
  'could not retry the transfer': {
    sentence: "The peer didn't start sending.",
    transient: true
  },
  'could not stop the transfer': {
    sentence: "The transfer didn't stop.",
    action: 'Reload Downloads to check.'
  },
  'could not check the source': {
    sentence: "The peer didn't say what it has.",
    transient: true
  },
  'source search failed': {
    sentence: "The search didn't finish.",
    transient: true
  },
  'artist search provider failed': {
    sentence: "MusicBrainz didn't answer the search.",
    transient: true
  },
  'recommendations are unavailable': {
    sentence: "Couldn't load recommendations.",
    transient: true
  },
  'the recommendation source is unavailable': {
    sentence: "ListenBrainz isn't answering.",
    transient: true
  },
  'identity resolution is unavailable': {
    sentence: "Couldn't load this file's match.",
    transient: true
  },
  'edition lookup is unavailable': {
    sentence: "Couldn't load the editions.",
    transient: true
  },
  'edition selection is unavailable': {
    sentence: "Couldn't save the edition.",
    transient: true
  },
  'the weekly playlist is unavailable': {
    sentence: "Couldn't load this week’s playlist.",
    transient: true
  },
  'the job queue cannot be read': {
    sentence: "Couldn't load the job queue.",
    transient: true
  },
  'acquisition targets are unavailable': {
    sentence: "Couldn't load the wishlist.",
    transient: true
  },
  'notifications are unavailable': {
    sentence: "Couldn't send the notification.",
    transient: true
  },
  'notification settings are unavailable': {
    sentence: "Couldn't load notification settings.",
    transient: true
  },
  'the message could not be sent': {
    sentence: "The test message didn't arrive.",
    action: 'Check the address under Settings → Connections.'
  },
  'deleting files is unavailable': {
    sentence: "Couldn't delete the files.",
    transient: true
  },
  'event streaming is unavailable': {
    sentence: 'Live updates stopped.',
    action: 'Reload the page.'
  },
  'audio preview is unavailable': {
    sentence: "Can't play this file.",
    action: 'Reload the page.'
  },
  'the upload was refused': {
    sentence: 'Upload refused.',
    action: 'Check the files and try again.'
  },
  'the upload is too large': {
    sentence: 'Upload too large.',
    action: 'Upload fewer files at a time.'
  },
  'the upload contains no files': {
    sentence: 'Upload has no files.',
    action: 'Choose files and try again.'
  },
  'no cover art': {
    sentence: 'No cover yet.',
    action: 'Schall fetches one when MusicBrainz has it.'
  },

  // ── Recorded against a job rather than thrown at a request ───────────────
  // These reach the screen as the text a failed job or a failed refresh left
  // behind, not as an answer to something the reader just pressed. They are the
  // sentences the owner named: correct as Go, and the documentation voice on
  // screen.
  'artist has no MusicBrainz identity': {
    sentence: 'This artist has no MusicBrainz link.',
    action: 'Find them with Follow artist on Artists.'
  },
  'album has no MusicBrainz release group identity': {
    sentence: 'This release has no MusicBrainz link.',
    action: 'Refresh the artist.'
  },
  'artist identity changed during refresh': {
    sentence: 'MusicBrainz merged this artist into another.',
    action: 'The next refresh follows the merge.'
  },
  'MusicBrainz has no such entity': {
    sentence: "MusicBrainz doesn't have this anymore.",
    action: 'The next refresh follows any merge.'
  },
  'MusicBrainz artist response is missing a name': {
    sentence: 'MusicBrainz sent an artist with no name.',
    action: 'The next refresh asks again.'
  },
  'MusicBrainz does not know the recording this was to be checked against': {
    sentence: "MusicBrainz doesn't know this song.",
    action: 'Listen on Review to decide.'
  },
  'LRCLIB has no lyrics for this recording': {
    sentence: 'No lyrics found.',
    action: 'Schall asks again on the next scan.'
  },
  'downloaded folder escapes the configured inbox': {
    sentence: 'Download landed outside the inbox.',
    action: 'Check the download folder under Settings → Library.'
  },
  'downloaded folder is not a directory': {
    sentence: "The download isn't a folder.",
    action: 'Check the download folder under Settings → Library.'
  },
  'is no longer readable audio': {
    sentence: "This file isn't readable audio anymore.",
    action: 'Run a scan.'
  }
};

/** The backstop. Every failure reaches written words, including one nobody
 * anticipated: a status nothing above matched still says what happened and what
 * to do. `0` is `api.ts`'s own case — nothing answered at all, which is a
 * different sentence from a server that answered with a refusal. */
const byStatus: Record<number, Written> = {
  0: {
    sentence: "Schall didn't answer. It may be restarting.",
    transient: true
  },
  400: {
    sentence: "Schall couldn't read that request.",
    action: 'Reload and try again.'
  },
  401: {
    sentence: 'Not signed in.',
    action: 'Open Schall at its usual address and sign in.'
  },
  403: {
    sentence: "This account can't do that.",
    action: 'Ask whoever runs Schall for access.'
  },
  404: {
    sentence: "That's gone.",
    action: 'Reload the page.'
  },
  409: {
    sentence: 'This was already decided.',
    action: 'Reload to see the decision.',
    role: 'decide'
  },
  422: {
    sentence: 'A field is wrong.',
    action: 'Check the fields and save again.'
  },
  429: {
    sentence: 'Too many requests.',
    action: 'Wait a minute and try again.'
  }
};

const serverFault: Written = {
  sentence: "Schall couldn't finish that.",
  transient: true,
  subjectless: true
};

const unknown: Written = {
  sentence: "Schall couldn't finish that.",
  action: 'Try again in a moment.',
  subjectless: true
};

/** Longest first, so a short sentinel never matches inside a longer one when a
 * wrapped Go error carries its own words around the sentinel. */
const sentinelKeys = Object.keys(sentinels).sort((a, b) => b.length - a.length);

/** The strings a failure might carry the sentinel in. `request` in `api.ts`
 * prefers `details[0]` over `title`, so the sentinel is in either place, and ten
 * handlers pass a wrapped `err.Error()` as the title, so it may be inside the
 * string rather than the whole of it. */
function candidates(error: unknown): string[] {
  if (typeof error === 'string') return [error];
  const found: string[] = [];
  if (error instanceof ApiError) {
    if (error.title) found.push(error.title);
    if (error.details) found.push(...error.details);
  }
  if (error instanceof Error) found.push(error.message);
  return found;
}

function match(texts: string[]): Written | undefined {
  for (const text of texts) {
    const exact = sentinels[text];
    if (exact) return exact;
  }
  for (const key of sentinelKeys) {
    if (texts.some((text) => text.includes(key))) return sentinels[key];
  }
  return undefined;
}

function statusOf(error: unknown): number | undefined {
  return error instanceof ApiError ? error.status : undefined;
}

/** The words the server actually said, for the disclosure. Never a sentence
 * this module wrote: the point of the disclosure is that a bug report can quote
 * the server rather than the interface. */
function rawOf(error: unknown): string {
  if (typeof error === 'string') return error;
  if (error instanceof ApiError) {
    const parts = [error.title, ...(error.details ?? [])].filter(Boolean) as string[];
    if (parts.length > 0) return parts.join(' — ');
  }
  if (error instanceof Error) return error.message;
  return String(error);
}

function written(from: Written, raw: string, layer: ErrorNotice['layer']): ErrorNotice {
  return {
    sentence: from.sentence,
    action: from.action ?? '',
    raw,
    role: from.role ?? 'fail',
    transient: from.transient ?? false,
    subjectless: from.subjectless ?? false,
    layer
  };
}

/** Turn anything a failed request threw — or any string a job left behind —
 * into the two sentences and the raw words. */
export function describeError(error: unknown): ErrorNotice {
  const raw = rawOf(error);

  // A refusal Schall makes on purpose, before any status is read. Its second
  // sentence names what to do instead, which is what keeps a refusal from being
  // a dead end.
  if (error instanceof DuplicateProtection) {
    return {
      sentence: 'You may already have this.',
      action: 'Check what Schall found below, then keep it or fetch anyway.',
      raw,
      role: 'decide',
      transient: false,
      subjectless: false,
      layer: 'sentinel'
    };
  }
  if (error instanceof SourceChanged) {
    return {
      sentence: 'The peer changed this folder.',
      action: 'See what changed below, then search for another copy.',
      raw,
      role: 'decide',
      transient: false,
      subjectless: false,
      layer: 'sentinel'
    };
  }

  const sentinel = match(candidates(error));
  if (sentinel) return written(sentinel, raw, 'sentinel');

  const status = statusOf(error);
  if (status !== undefined) {
    const known = byStatus[status];
    if (known) return written(known, raw, 'status');
    if (status >= 500) return written(serverFault, raw, 'status');
  }

  return written(unknown, raw, 'none');
}

/** Whether a failure is the browser's sign-in not being accepted. A direct
 * connection never carries the forward-auth session (ADR 0033),
 * so reloading or retrying asks the same unauthenticated request again. A
 * screen that sees this stops offering to ask again and stops polling. */
export function isAuthError(error: unknown): boolean {
  return error instanceof ApiError && error.status === 401;
}

/** The query client's default `retry`: once, unless the browser's sign-in was
 * refused. A 401 comes back the same on every attempt, so a second try only
 * repeats the request that just failed. */
export function queryRetry(failureCount: number, error: unknown): boolean {
  return !isAuthError(error) && failureCount < 1;
}

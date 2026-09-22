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
    sentence: 'Schall has no download source, so nothing can be fetched.',
    action: 'Add the slskd address and key under Settings → Connections.'
  },
  'no download source is configured': {
    sentence: 'Schall has no download source, so nothing can be fetched.',
    action: 'Add the slskd address and key under Settings → Connections.'
  },
  'could not reach slskd': {
    sentence: 'slskd did not answer.',
    action: 'Check that it is running, then press Test connection under Settings → Connections.'
  },
  'navidrome is not configured': {
    sentence: 'Navidrome is not set up.',
    action: 'Add the Navidrome address under Settings → Connections.'
  },
  'ListenBrainz is not configured': {
    sentence: 'Recommendations need a ListenBrainz account.',
    action: 'Add the username under Settings → Connections.'
  },
  'ListenBrainz recommendations are disabled': {
    sentence: 'Recommendations are switched off.',
    action: 'Turn them on under Settings → Connections.'
  },
  'playlist sync is not configured': {
    sentence: 'Playlist sync has no player to sync with.',
    action: 'Add the Navidrome address under Settings → Connections.'
  },
  'playlists are not configured': {
    sentence: 'Playlist sync has no player to sync with.',
    action: 'Add the Navidrome address under Settings → Connections.'
  },
  'uploading is not configured': {
    sentence: 'Schall has no upload folder to write to.',
    action: 'Set the staging folder under Settings → Library.'
  },
  'library paths are not configured': {
    sentence: 'Schall has no library folder to read.',
    action: 'Add one under Settings → Library.'
  },
  'the library layout is not configured': {
    sentence: 'The library has no folder layout.',
    action: 'Choose a layout under Settings → Library.'
  },
  'acquisition targets are not configured': {
    sentence: 'Nothing is set up to fetch wanted music.',
    action: 'Add a download source under Settings → Connections.'
  },
  'connect a Spotify account first': {
    sentence: 'Spotify is not connected.',
    action: 'Connect it under Settings → Connections.'
  },
  'enter the Spotify client credentials first': {
    sentence: 'Spotify has no client ID and secret yet.',
    action: 'Enter them under Settings → Connections.'
  },
  'there is nowhere to send to': {
    sentence: 'No notification address is set.',
    action: 'Add one under Settings → Connections.'
  },

  // ── Refusals ────────────────────────────────────────────────────────────
  // Schall could have done it and would not, because a decision is already on
  // record. The second sentence is not optional here: without it a refusal is a
  // dead end, and `CLAUDE.md` keeps that as an explicit exception to the rule
  // that error text is short.
  'this wishlist entry is already following another copy': {
    sentence: 'Another copy of this song is already on its way.',
    action: 'Wait for it, or cancel it on Downloads and ask again.',
    role: 'decide'
  },
  'artist is already followed': {
    sentence: 'This artist is already followed.',
    action: 'Open them from Artists to change what is monitored.',
    role: 'decide'
  },
  'artist is not followed': {
    sentence: 'This artist is not followed, so nothing of theirs counts as missing.',
    action: 'Press Follow to keep their releases complete.',
    role: 'decide'
  },
  'a layout migration is already planned': {
    sentence: 'A move of the library folders is already planned.',
    action: 'Apply or discard that plan before planning another.',
    role: 'decide'
  },
  'that layout migration has nothing left to move': {
    sentence: 'Every file in this plan has already been moved.',
    action: 'Discard the plan to clear it.',
    role: 'decide'
  },
  'this target is already finished with': {
    sentence: 'This song is settled, so nothing more will be fetched for it.',
    action: 'Want it again from the release to start over.',
    role: 'decide'
  },
  'music folder already exists': {
    sentence: 'A folder with that name is already there.',
    action: 'Choose a different name and save.',
    role: 'decide'
  },
  'this upload is still being imported': {
    sentence: 'This upload is still being read.',
    action: 'Wait for it; the row changes by itself when it is done.',
    role: 'decide'
  },
  'that upload is not staged': {
    sentence: 'The files for this upload are no longer on the volume.',
    action: 'Upload them again.',
    role: 'decide'
  },
  'this copy is not waiting for a decision': {
    sentence: 'Somebody has already answered for this copy.',
    action: 'Reload the page to see the answer on record.',
    role: 'decide'
  },
  'this wishlist entry is not waiting on an answer': {
    sentence: 'This song is not waiting on a person.',
    action: 'Reload the page to see where it got to.',
    role: 'decide'
  },
  'this wishlist entry has no copies waiting for a decision': {
    sentence: 'No copy of this song is waiting for an answer.',
    action: 'Reload Review to see what is left.',
    role: 'decide'
  },
  'this wishlist entry has no resolution to reject': {
    sentence: 'There is nothing recorded for this song to turn down.',
    action: 'Reload the page to see where it got to.',
    role: 'decide'
  },
  'no import with that ID is waiting for review': {
    sentence: 'This import has already been answered.',
    action: 'Reload Review to see what is left.',
    role: 'decide'
  },
  'no decision has been recorded for this file': {
    sentence: 'Nobody has decided anything about this file yet, so there is nothing to undo.',
    action: 'Reload the page to see its current state.',
    role: 'decide'
  },
  'that answer is not on record': {
    sentence: 'That answer is not the one on record.',
    action: 'Reload the page to see what was decided.',
    role: 'decide'
  },
  'there is nothing to retry': {
    sentence: 'There is nothing here left to try again.',
    action: 'Reload the page to see the current state.',
    role: 'decide'
  },
  'there is nothing left to cancel': {
    sentence: 'This has already stopped.',
    action: 'Reload the page to see how it ended.',
    role: 'decide'
  },
  'this job was stopped rather than tried': {
    sentence: 'This job was cancelled and has nothing to repeat.',
    action: 'Ask for the work again from the page that started it.',
    role: 'decide'
  },
  'this request cannot be retried': {
    sentence: 'This download is not in a state that can be tried again.',
    action: 'Reload Downloads to see where it got to.',
    role: 'decide'
  },
  'this request cannot be started': {
    sentence: 'This download is not in a state that can be started.',
    action: 'Reload Downloads to see where it got to.',
    role: 'decide'
  },
  'this song is not on the weekly playlist': {
    sentence: 'This song is not on this week’s playlist.',
    action: 'Reload the playlist to see what is on it.',
    role: 'decide'
  },
  'only a Spotify playlist can be imported': {
    sentence: 'That link is not a Spotify playlist.',
    action: 'Paste a Spotify playlist link or ID.',
    role: 'decide'
  },

  // ── The thing is gone ───────────────────────────────────────────────────
  'artist not found': {
    sentence: 'This artist is not in the catalogue any more.',
    action: 'Go back to Artists.'
  },
  'release not found': {
    sentence: 'This release is not in the catalogue any more.',
    action: 'Go back to Library.'
  },
  'library file not found': {
    sentence: 'This file is not in the library any more.',
    action: 'Reload the page to see the current list.'
  },
  'library file or track not found': {
    sentence: 'This file is not in the library any more.',
    action: 'Reload the page to see the current list.'
  },
  'playlist not found': {
    sentence: 'This playlist is gone.',
    action: 'Go back to Playlists.'
  },
  'track not found': {
    sentence: 'This track is not in the catalogue any more.',
    action: 'Reload the page to see the current track list.'
  },
  'recording not found': {
    sentence: 'This recording is not in the catalogue any more.',
    action: 'Reload the page to see the current track list.'
  },
  'copy not found': {
    sentence: 'This copy is not on record any more.',
    action: 'Go back to Downloads.'
  },
  'download request not found': {
    sentence: 'This download is not on record any more.',
    action: 'Go back to Downloads.'
  },
  'acquisition target not found': {
    sentence: 'This song is no longer wanted.',
    action: 'Reload the page to see what is still wanted.'
  },
  'job not found': {
    sentence: 'This job is no longer on the queue.',
    action: 'Reload Jobs to see what is left.'
  },
  'the file is no longer there': {
    sentence: 'The file has gone from the volume.',
    action: 'Run a scan to bring the library up to date.'
  },
  'the copy is no longer there': {
    sentence: 'The downloaded copy has gone from the volume.',
    action: 'Ask for it again from the release.'
  },

  // ── The other end did not answer ────────────────────────────────────────
  // Transient: asking again may answer differently, so nobody is asked to press
  // a button that only repeats what just failed.
  'database unavailable': {
    sentence: 'Schall cannot reach its database.',
    transient: true
  },
  'MusicBrainz is temporarily unavailable': {
    sentence: 'MusicBrainz is not answering.',
    transient: true
  },
  'could not reach AcoustID': {
    sentence: 'AcoustID is not answering.',
    transient: true
  },
  'no AcoustID API key is configured': {
    sentence: 'Schall cannot identify audio without an AcoustID key.',
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
    action: 'Check that it is running, then press Test connection under Settings → Connections.'
  },
  'AcoustID is rate limiting requests': {
    sentence: 'AcoustID is being asked too often.',
    action: 'Wait a minute; Schall slows down by itself.'
  },
  'listenbrainz is being asked too often': {
    sentence: 'ListenBrainz is being asked too often.',
    action: 'Wait a minute before asking again.'
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
    sentence: 'ListenBrainz has not worked out recommendations for this account yet.',
    action: 'Listen for a while and come back.'
  },
  'settings are unavailable': {
    sentence: 'Schall could not read the settings.',
    transient: true
  },
  'library is unavailable': {
    sentence: 'Schall could not read the library.',
    transient: true
  },
  'downloads are unavailable': {
    sentence: 'Schall could not read the downloads.',
    transient: true
  },
  'source searches are unavailable': {
    sentence: 'Schall could not read the source searches.',
    transient: true
  },
  'starting transfers is unavailable': {
    sentence: 'Schall could not start the transfer.',
    transient: true
  },
  'could not start the transfer': {
    sentence: 'The peer did not start sending.',
    transient: true
  },
  'could not retry the transfer': {
    sentence: 'The peer did not start sending.',
    transient: true
  },
  'could not stop the transfer': {
    sentence: 'The transfer did not stop.',
    action: 'Reload Downloads to see whether it stopped anyway.'
  },
  'could not check the source': {
    sentence: 'The peer did not say what it still holds.',
    transient: true
  },
  'source search failed': {
    sentence: 'The search for copies did not finish.',
    transient: true
  },
  'artist search provider failed': {
    sentence: 'MusicBrainz did not answer the artist search.',
    transient: true
  },
  'recommendations are unavailable': {
    sentence: 'Schall could not read the recommendations.',
    transient: true
  },
  'the recommendation source is unavailable': {
    sentence: 'ListenBrainz is not answering.',
    transient: true
  },
  'identity resolution is unavailable': {
    sentence: 'Schall could not read what this file was identified as.',
    transient: true
  },
  'edition lookup is unavailable': {
    sentence: 'Schall could not read the editions of this release.',
    transient: true
  },
  'edition selection is unavailable': {
    sentence: 'Schall could not record the edition.',
    transient: true
  },
  'the weekly playlist is unavailable': {
    sentence: 'Schall could not read this week’s playlist.',
    transient: true
  },
  'the job queue cannot be read': {
    sentence: 'Schall could not read the job queue.',
    transient: true
  },
  'acquisition targets are unavailable': {
    sentence: 'Schall could not read what is wanted.',
    transient: true
  },
  'notifications are unavailable': {
    sentence: 'Schall could not send the notification.',
    transient: true
  },
  'notification settings are unavailable': {
    sentence: 'Schall could not read the notification settings.',
    transient: true
  },
  'the message could not be sent': {
    sentence: 'The test message did not arrive.',
    action: 'Check the address under Settings → Connections.'
  },
  'deleting files is unavailable': {
    sentence: 'Schall could not delete the files.',
    transient: true
  },
  'event streaming is unavailable': {
    sentence: 'Schall stopped sending live updates, so this page may be behind.',
    action: 'Reload the page.'
  },
  'audio preview is unavailable': {
    sentence: 'This file cannot be played here.',
    action: 'Reload the page; if it stays, the audio is unreadable.'
  },
  'the upload was refused': {
    sentence: 'The upload was not accepted.',
    action: 'Check the files and upload them again.'
  },
  'the upload is too large': {
    sentence: 'This upload is bigger than Schall accepts.',
    action: 'Upload fewer files at a time.'
  },
  'the upload contains no files': {
    sentence: 'This upload has no files in it.',
    action: 'Choose the files and upload again.'
  },
  'no cover art': {
    sentence: 'There is no cover for this release.',
    action: 'Schall fetches one when MusicBrainz has it.'
  },

  // ── Recorded against a job rather than thrown at a request ───────────────
  // These reach the screen as the text a failed job or a failed refresh left
  // behind, not as an answer to something the reader just pressed. They are the
  // sentences the owner named: correct as Go, and the documentation voice on
  // screen.
  'artist has no MusicBrainz identity': {
    sentence: 'Schall has no MusicBrainz link for this artist, so it cannot read their releases.',
    action: 'Find them with Follow artist on Artists.'
  },
  'album has no MusicBrainz release group identity': {
    sentence: 'Schall has no MusicBrainz link for this release, so it cannot read its track list.',
    action: 'Refresh the artist.'
  },
  'artist identity changed during refresh': {
    sentence: 'MusicBrainz merged this artist into another one while Schall was reading them.',
    action: 'The next refresh reads the artist they were merged into.'
  },
  'MusicBrainz has no such entity': {
    sentence: 'MusicBrainz no longer has this entry.',
    action: 'It may have been merged; the next refresh follows the merge.'
  },
  'MusicBrainz artist response is missing a name': {
    sentence: 'MusicBrainz answered with an artist that has no name, which Schall cannot file.',
    action: 'Nothing to do here; the next refresh asks again.'
  },
  'MusicBrainz does not know the recording this was to be checked against': {
    sentence: 'MusicBrainz has no recording to check this copy against.',
    action: 'Listen to the copy on Review and decide by ear.'
  },
  'LRCLIB has no lyrics for this recording': {
    sentence: 'No lyrics were found for this song.',
    action: 'Schall asks again when the song is next scanned.'
  },
  'downloaded folder escapes the configured inbox': {
    sentence: 'This download landed outside the folder Schall is allowed to read.',
    action: 'Check the download folder under Settings → Library.'
  },
  'downloaded folder is not a directory': {
    sentence: 'What arrived is not a folder Schall can import.',
    action: 'Check the download folder under Settings → Library.'
  },
  'is no longer readable audio': {
    sentence: 'This file can no longer be read as audio.',
    action: 'Run a scan to update the library.'
  }
};

/** The backstop. Every failure reaches written words, including one nobody
 * anticipated: a status nothing above matched still says what happened and what
 * to do. `0` is `api.ts`'s own case — nothing answered at all, which is a
 * different sentence from a server that answered with a refusal. */
const byStatus: Record<number, Written> = {
  0: {
    sentence: 'Nothing answered. Schall may be restarting.',
    transient: true
  },
  400: {
    sentence: 'Schall could not read that request.',
    action: 'Reload the page and try once more.'
  },
  401: {
    sentence: 'No sign-in on this connection.',
    action: 'Open Schall at its usual address and sign in there.'
  },
  403: {
    sentence: 'This account is not allowed to do that.',
    action: 'Ask whoever runs this Schall for access.'
  },
  404: {
    sentence: 'That is not there any more.',
    action: 'Reload the page to see what is.'
  },
  409: {
    sentence: 'Something about this has already been decided.',
    action: 'Reload the page to see what is on record.',
    role: 'decide'
  },
  422: {
    sentence: 'Something in the form is not right.',
    action: 'Check the fields and save again.'
  },
  429: {
    sentence: 'Schall is asking too fast and has been told to wait.',
    action: 'Wait a minute before asking again.'
  }
};

const serverFault: Written = {
  sentence: 'Schall could not finish that.',
  transient: true,
  subjectless: true
};

const unknown: Written = {
  sentence: 'Schall could not finish that.',
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
      sentence: 'Your library may already hold this music, so Schall did not fetch it again.',
      action: 'Read what it found below, then keep it or fetch it anyway.',
      raw,
      role: 'decide',
      transient: false,
      subjectless: false,
      layer: 'sentinel'
    };
  }
  if (error instanceof SourceChanged) {
    return {
      sentence: 'The peer no longer offers what was recorded for this folder.',
      action: 'Read what changed below, then search for another copy.',
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

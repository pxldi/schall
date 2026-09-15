import { afterEach, describe, expect, it, vi } from 'vitest';
import { api, DuplicateProtection, SourceChanged } from '$lib/api';
import type { DuplicateEvidence, SourceCandidate, SourceOffer } from '$lib/api';

/** The client's one boundary is `fetch`, so that is the only thing stubbed. Each
 * test reads back what was sent rather than what the client returned: the value
 * is the server's, the request is the client's, and the request is what drifts. */
function stubFetch(response = new Response('{}', { status: 200 })) {
  const sent = vi.fn(async (_path: string, _init?: RequestInit) => response.clone());
  vi.stubGlobal('fetch', sent);
  return sent;
}

function problemResponse(problem: unknown, status = 400) {
  return new Response(JSON.stringify(problem), {
    status,
    headers: { 'Content-Type': 'application/problem+json' }
  });
}

type Sent = ReturnType<typeof stubFetch>;

function pathOf(sent: Sent) {
  return sent.mock.calls[0][0];
}

function initOf(sent: Sent) {
  return sent.mock.calls[0][1] as RequestInit;
}

function headersOf(sent: Sent) {
  return initOf(sent).headers as Record<string, string>;
}

const offer: SourceOffer = { offered: true, missing: ['04 Taphead.flac'], changed: [], stale: false };

function candidate(overrides: Partial<SourceCandidate> = {}): SourceCandidate {
  return {
    provider: 'slskd',
    username: 'peer',
    directory: 'Music\\Talk Talk\\Laughing Stock',
    trackCount: 6,
    totalSizeBytes: 300_000_000,
    format: 'flac',
    freeUploadSlot: true,
    queueLength: 0,
    uploadSpeed: 1_000_000,
    score: 88,
    match: {
      checked: true,
      expected: 6,
      confirmed: 6,
      partial: 0,
      absent: 0,
      complete: true,
      summary: 'every track confirmed'
    },
    reasons: ['complete'],
    files: [],
    ...overrides
  };
}

const duplicates: DuplicateEvidence = {
  albumId: 'album-1',
  albumTitle: 'Laughing Stock',
  artistName: 'Talk Talk',
  trackCount: 6,
  ownedTrackCount: 2,
  unresolvedCount: 0,
  summary: 'the library already holds two of these six tracks',
  files: []
};

afterEach(() => {
  vi.unstubAllGlobals();
});

describe('request', () => {
  it('asks for JSON on every request', async () => {
    const sent = stubFetch();

    await api.dashboard();

    expect(headersOf(sent).Accept).toBe('application/json');
  });

  it('declares a JSON body when it sends one', async () => {
    const sent = stubFetch();

    await api.addLibraryRoot('/music');

    expect(headersOf(sent)['Content-Type']).toBe('application/json');
  });

  it('declares no content type on a request with no body', async () => {
    const sent = stubFetch();

    await api.scanLibrary();

    expect(headersOf(sent)['Content-Type']).toBeUndefined();
  });

  it('returns nothing for a 204', async () => {
    stubFetch(new Response(null, { status: 204 }));

    await expect(api.clearManualMatch('file-1')).resolves.toBeUndefined();
  });

  // The scan is queued and the answer carries nothing. Reading that as JSON
  // threw, and the settings screen said the scan could not be started when it
  // had started.
  it('returns nothing for an accepted request with an empty body', async () => {
    stubFetch(new Response('', { status: 202 }));

    await expect(api.scanForUpgrades()).resolves.toBeUndefined();
  });

  it('parses the body of a 200', async () => {
    stubFetch(new Response(JSON.stringify({ total: 3 }), { status: 200 }));

    await expect(api.library()).resolves.toEqual({ total: 3 });
  });

  it('raises the first detail of a problem', async () => {
    stubFetch(problemResponse({ title: 'Bad Request', details: ['path is not a directory'] }));

    await expect(api.addLibraryRoot('/nowhere')).rejects.toThrow('path is not a directory');
  });

  it('raises the title of a problem carrying no details', async () => {
    stubFetch(problemResponse({ title: 'library root already exists' }));

    await expect(api.addLibraryRoot('/music')).rejects.toThrow('library root already exists');
  });

  it('raises the status when the failure says nothing readable', async () => {
    stubFetch(new Response('<html>502</html>', { status: 502 }));

    await expect(api.dashboard()).rejects.toThrow('Request failed (502)');
  });

  it('raises a refusal that carries what the library already holds', async () => {
    stubFetch(problemResponse({ title: 'the library may already hold this', duplicates }, 409));

    await expect(api.wantRelease('album-1')).rejects.toBeInstanceOf(DuplicateProtection);
  });

  it('carries the duplicate evidence through to the caller', async () => {
    stubFetch(problemResponse({ title: 'the library may already hold this', duplicates }, 409));

    const refusal = await api.wantRelease('album-1').catch((error: unknown) => error);

    expect((refusal as DuplicateProtection).evidence).toEqual(duplicates);
  });

  it('raises a refusal that carries what the peer offers now', async () => {
    stubFetch(problemResponse({ title: 'the folder has changed', sourceChange: offer }, 409));

    await expect(api.startDownload('download-1')).rejects.toBeInstanceOf(SourceChanged);
  });

  it('carries the changed offer through to the caller', async () => {
    stubFetch(problemResponse({ title: 'the folder has changed', sourceChange: offer }, 409));

    const refusal = await api.startDownload('download-1').catch((error: unknown) => error);

    expect((refusal as SourceChanged).offer).toEqual(offer);
  });

  it('prefers the title over the details for a changed source', async () => {
    stubFetch(
      problemResponse(
        { title: 'the folder has changed', details: ['04 Taphead.flac'], sourceChange: offer },
        409
      )
    );

    await expect(api.startDownload('download-1')).rejects.toThrow('the folder has changed');
  });
});

describe('query strings', () => {
  it('asks for artists with no query string when nothing is filtered', async () => {
    const sent = stubFetch();

    await api.artists();

    expect(pathOf(sent)).toBe('/api/v1/artists');
  });

  it('sends the filters that were set', async () => {
    const sent = stubFetch();

    await api.artists({ scope: 'followed' });

    expect(pathOf(sent)).toBe('/api/v1/artists?scope=followed');
  });

  it('escapes a search term that would otherwise break the query string', async () => {
    const sent = stubFetch();

    await api.artists({ query: 'Simon & Garfunkel' });

    expect(pathOf(sent)).toBe('/api/v1/artists?q=Simon+%26+Garfunkel');
  });

  it('asks for library files under the library path', async () => {
    const sent = stubFetch();

    await api.libraryFiles({ status: 'duplicate' });

    expect(pathOf(sent)).toBe('/api/v1/library/files?status=duplicate');
  });

  it('asks for one library file by id when review names one', async () => {
    const sent = stubFetch();

    await api.libraryFiles({ id: 'file-1' });

    expect(pathOf(sent)).toBe('/api/v1/library/files?id=file-1');
  });

  it('asks for every download when no pile is named', async () => {
    const sent = stubFetch();

    await api.downloads();

    expect(pathOf(sent)).toBe('/api/v1/downloads');
  });

  // The page shows one pile of the list at a time, and it asks for that pile
  // rather than for everything: the open transfers it was sieving for were
  // older than the hundred most recent requests it received.
  it('asks for the page of the pile the screen is showing', async () => {
    const sent = stubFetch();

    await api.downloads({ view: 'open', limit: 25, offset: 25 });

    expect(pathOf(sent)).toBe('/api/v1/downloads?view=open&limit=25&offset=25');
  });

  // The states a want passes through while nobody has to act on it are one pile
  // to the reader, so they are one request rather than three lists to add up.
  it('asks for several states of a want in one request', async () => {
    const sent = stubFetch();

    await api.wants({ statuses: ['unresolved', 'pending', 'searching'], limit: 25 });

    expect(pathOf(sent)).toBe(
      '/api/v1/acquisition-targets?status=unresolved%2Cpending%2Csearching&limit=25'
    );
  });

  it('asks for every want when no state is named', async () => {
    const sent = stubFetch();

    await api.wants();

    expect(pathOf(sent)).toBe('/api/v1/acquisition-targets');
  });

  it('always says which page of the review queue it wants', async () => {
    const sent = stubFetch();

    await api.reviewQueue();

    expect(pathOf(sent)).toBe('/api/v1/review-queue?limit=20&offset=0');
  });

  it('escapes an identifier that would otherwise change the path', async () => {
    const sent = stubFetch();

    await api.artist('mbid/with/slashes');

    expect(pathOf(sent)).toBe('/api/v1/artists/mbid%2Fwith%2Fslashes');
  });

  it('escapes a track search term into the path it searches', async () => {
    const sent = stubFetch();

    await api.searchTracks('file-1', 'a/b c');

    expect(pathOf(sent)).toBe('/api/v1/library/files/file-1/tracks?q=a%2Fb%20c');
  });

  it('names where a held copy is played from', () => {
    stubFetch();

    expect(api.reviewCopyAudioUrl('copy-1')).toBe('/api/v1/review-queue/copies/copy-1/audio');
  });

  // The audio is an <audio> src rather than a fetch, so the browser can issue
  // range requests and the scrubber works.
  it('does not fetch the audio of a held copy itself', () => {
    const sent = stubFetch();

    api.reviewCopyAudioUrl('copy-1');

    expect(sent).not.toHaveBeenCalled();
  });
});

describe('what a decision sends', () => {
  it('names the track a file was matched to by hand', async () => {
    const sent = stubFetch();

    await api.setManualMatch('file-1', 'track-9');

    expect(JSON.parse(initOf(sent).body as string)).toEqual({
      trackId: 'track-9',
      reassign: false
    });
  });

  it('takes a track off another file only when told to', async () => {
    const sent = stubFetch();

    await api.setManualMatch('file-1', 'track-9', true);

    expect(JSON.parse(initOf(sent).body as string).reassign).toBe(true);
  });

  it('clears a manual match by deleting it', async () => {
    const sent = stubFetch(new Response(null, { status: 204 }));

    await api.clearManualMatch('file-1');

    expect(initOf(sent).method).toBe('DELETE');
  });

  it('records a file as local only rather than as a recording', async () => {
    const sent = stubFetch();

    await api.markLocalOnly('file-1');

    expect(JSON.parse(initOf(sent).body as string)).toEqual({ localOnly: true });
  });

  it('records which recording a file was said to be', async () => {
    const sent = stubFetch();

    await api.acceptIdentity('file-1', 'recording-7');

    expect(JSON.parse(initOf(sent).body as string)).toEqual({ recordingId: 'recording-7' });
  });

  it('keeps a duplicate by posting to the file that is kept', async () => {
    const sent = stubFetch(new Response(null, { status: 204 }));

    await api.keepDuplicate('file-1');

    expect([pathOf(sent), initOf(sent).method]).toEqual(['/api/v1/library/files/file-1/keep', 'POST']);
  });

  it('asks about a duplicate again by deleting that answer', async () => {
    const sent = stubFetch(new Response(null, { status: 204 }));

    await api.askAboutDuplicateAgain('file-1');

    expect(initOf(sent).method).toBe('DELETE');
  });

  it('asks for the recordings still to be decided about by default', async () => {
    const sent = stubFetch(new Response('{"recordings":[],"total":0}', { status: 200 }));

    await api.duplicateRecordings();

    expect(pathOf(sent)).toBe('/api/v1/library/duplicates');
  });

  it('asks for the pairs kept on purpose separately', async () => {
    const sent = stubFetch(new Response('{"recordings":[],"total":0}', { status: 200 }));

    await api.duplicateRecordings(true);

    expect(pathOf(sent)).toBe('/api/v1/library/duplicates?kept=true');
  });

  it('keeps every copy of a recording by posting to the recording', async () => {
    const sent = stubFetch(new Response(null, { status: 204 }));

    await api.keepRecordingCopies('recording-7');

    expect([pathOf(sent), initOf(sent).method]).toEqual([
      '/api/v1/library/duplicates/recording-7/keep',
      'POST'
    ]);
  });

  it('keeps one copy by naming the recording, the copy that stays and the copies that go', async () => {
    const sent = stubFetch(
      new Response('{"keptFileId":"file-3","keptPath":"/music/a.flac","removedPaths":[]}', {
        status: 200
      })
    );

    await api.keepOneCopy('recording-7', 'file-3', ['file-4', 'file-5']);

    expect([pathOf(sent), initOf(sent).method, initOf(sent).body]).toEqual([
      '/api/v1/library/duplicates/recording-7/keep-one',
      'POST',
      '{"fileId":"file-3","deleting":["file-4","file-5"]}'
    ]);
  });

  it('tries the removals that could not be deleted again', async () => {
    const sent = stubFetch(new Response('{"deleted":1}', { status: 200 }));

    await api.retryRemovals();

    expect([pathOf(sent), initOf(sent).method]).toEqual([
      '/api/v1/library/removals/retry',
      'POST'
    ]);
  });

  it('asks about a recording held twice again by deleting that answer', async () => {
    const sent = stubFetch(new Response(null, { status: 204 }));

    await api.askAboutRecordingAgain('recording-7');

    expect(initOf(sent).method).toBe('DELETE');
  });

  it('does not auto-request a source search unless permission was given', async () => {
    const sent = stubFetch();

    await api.createSourceSearch(['album-1']);

    expect(JSON.parse(initOf(sent).body as string).autoRequest).toBe(false);
  });

  it('acknowledges nothing about duplicates on a plain download request', async () => {
    const sent = stubFetch();

    await api.requestDownload('album-1', candidate());

    expect(JSON.parse(initOf(sent).body as string).acknowledgeDuplicates).toBe(false);
  });

  it('sends a bit rate of zero for a candidate whose bit rate is unknown', async () => {
    const sent = stubFetch();

    await api.requestDownload('album-1', candidate());

    expect(JSON.parse(initOf(sent).body as string).averageBitRate).toBe(0);
  });

  it('sends a duration of zero for a file whose length is unknown', async () => {
    const sent = stubFetch();

    await api.requestDownload(
      'album-1',
      candidate({
        files: [
          {
            path: 'Music\\Laughing Stock\\01 Myrrhman.flac',
            name: '01 Myrrhman.flac',
            extension: 'flac',
            sizeBytes: 40_000_000,
            variableBitRate: false
          }
        ]
      })
    );

    expect(JSON.parse(initOf(sent).body as string).files[0].durationSeconds).toBe(0);
  });
});

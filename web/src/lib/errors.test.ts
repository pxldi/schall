import { describe, expect, it } from 'vitest';
import { ApiError, DuplicateProtection, SourceChanged } from '$lib/api';
import { describeError, isAuthError, queryRetry, sentinels } from '$lib/errors';

describe('describeError', () => {
  it('writes the sentence for a sentinel it knows', () => {
    const notice = describeError(
      new ApiError('slskd is not configured', 409, { title: 'slskd is not configured' })
    );
    expect(notice.sentence).toBe('Schall has no download source, so nothing can be fetched.');
    expect(notice.action).toContain('Settings → Connections');
  });

  it('finds the sentinel inside a wrapped Go error', () => {
    // Ten handlers answer with `err.Error()`, and a Go error is wrapped as
    // `verb noun id: %w`, so the sentinel arrives inside a longer string.
    const notice = describeError(
      new ApiError('refresh artist 91f2: artist has no MusicBrainz identity', 500, {
        title: 'refresh artist 91f2: artist has no MusicBrainz identity'
      })
    );
    expect(notice.sentence).toContain('no MusicBrainz link for this artist');
  });

  it('finds the sentinel in details, which the message prefers', () => {
    const notice = describeError(
      new ApiError('artist not found', 404, {
        title: 'validation failed',
        details: ['artist not found']
      })
    );
    expect(notice.sentence).toBe('This artist is not in the catalogue any more.');
  });

  it('falls back to the status when no sentinel matches', () => {
    const notice = describeError(new ApiError('something nobody wrote a sentence for', 404));
    expect(notice.sentence).toBe('That is not there any more.');
    expect(notice.action).toBe('Reload the page to see what is.');
  });

  it('says nothing answered when nothing answered', () => {
    const notice = describeError(new ApiError('The server did not answer.', 0));
    expect(notice.sentence).toBe('Nothing answered. Schall may be restarting.');
    expect(notice.transient).toBe(true);
  });

  it('covers every status a server answers with', () => {
    for (const status of [401, 403, 404, 409, 422, 429]) {
      const notice = describeError(new ApiError('unknown words', status));
      expect(notice.sentence.length, `status ${status}`).toBeGreaterThan(0);
      expect(notice.action.length, `status ${status}`).toBeGreaterThan(0);
    }
  });

  it('treats any 5xx as a fault Schall may recover from', () => {
    for (const status of [500, 502, 503, 504, 599]) {
      const notice = describeError(new ApiError('unknown words', status));
      expect(notice.sentence, `status ${status}`).toBe('Schall could not finish that.');
      expect(notice.transient, `status ${status}`).toBe(true);
    }
  });

  it('still writes words for a total miss', () => {
    const notice = describeError(new Error('something that is not an ApiError at all'));
    expect(notice.sentence).toBe('Schall could not finish that.');
    expect(notice.action).toBe('Try again in a moment.');
    expect(notice.raw).toBe('something that is not an ApiError at all');
  });

  it('marks the two sentences that name nothing, and only those', () => {
    // A screen may say what failed where the sentence does not. Which sentences
    // those are is decided here rather than by each screen guessing, because a
    // screen that stood in for a written sentence would be replacing the one
    // that knows more with the one that knows less.
    expect(describeError(new ApiError('m', 500)).subjectless).toBe(true);
    expect(describeError(new Error('nothing wrote a sentence for this')).subjectless).toBe(true);
    expect(
      describeError(new ApiError('m', 409, { title: 'artist is already followed' })).subjectless
    ).toBe(false);
    expect(describeError(new ApiError('m', 429)).subjectless).toBe(false);
  });

  it('keeps the raw words whichever layer answered', () => {
    const cases: unknown[] = [
      new ApiError('m', 409, { title: 'artist is already followed' }),
      new ApiError('m', 404, { title: 'nothing anyone wrote a sentence for' }),
      new ApiError('m', 0),
      new Error('a plain error'),
      'a job left this behind',
      { not: 'an error at all' }
    ];
    for (const failure of cases) {
      expect(describeError(failure).raw.length).toBeGreaterThan(0);
    }
  });

  it('reads a string a job left behind', () => {
    // A failed refresh records its reason as text on the artist, and a failed
    // job records one on the job. Neither is thrown, and both used to be drawn
    // in the words Go wrote them in.
    const notice = describeError('artist identity changed during refresh');
    expect(notice.sentence).toContain('merged this artist into another one');
  });

  it('draws a refusal as a decision, with the sentence naming what to do', () => {
    const duplicate = describeError(
      new DuplicateProtection('release may already be held', {
        matches: [],
        summary: ''
      } as never)
    );
    expect(duplicate.role).toBe('decide');
    expect(duplicate.action.length).toBeGreaterThan(0);

    const changed = describeError(
      new SourceChanged('the source changed', {
        offered: true,
        missing: [],
        changed: [],
        stale: false
      })
    );
    expect(changed.role).toBe('decide');
    expect(changed.action.length).toBeGreaterThan(0);

    const another = describeError(
      new ApiError('m', 409, { title: 'this wishlist entry is already following another copy' })
    );
    expect(another.role).toBe('decide');
    expect(another.action).toContain('Downloads');
  });

  it('never leaves a refusal without its second sentence', () => {
    for (const [key, written] of Object.entries(sentinels)) {
      if (written.role !== 'decide') continue;
      expect(written.action ?? '', key).not.toBe('');
    }
  });

  it('says either what to do or that asking again may work', () => {
    // The standing rule: nobody is handed something they can only press retry
    // on. An entry with no action has to be one where asking again is the
    // answer, and the caller retries rather than the reader.
    for (const [key, written] of Object.entries(sentinels)) {
      expect(Boolean(written.action) || written.transient === true, key).toBe(true);
    }
  });

  it('writes no sentence longer than two of them', () => {
    for (const [key, written] of Object.entries(sentinels)) {
      const full = `${written.sentence} ${written.action ?? ''}`.trim();
      const stops = full.match(/[.!?](\s|$)/g) ?? [];
      expect(stops.length, `${key}: ${full}`).toBeLessThanOrEqual(2);
    }
  });
});

describe('isAuthError', () => {
  it('is true only for a 401', () => {
    expect(isAuthError(new ApiError('unauthorized', 401))).toBe(true);
    expect(isAuthError(new ApiError('not found', 404))).toBe(false);
    expect(isAuthError(new Error('unauthorized'))).toBe(false);
  });
});

describe('queryRetry', () => {
  it('does not retry a 401', () => {
    expect(queryRetry(0, new ApiError('unauthorized', 401))).toBe(false);
  });

  it('retries once for anything else', () => {
    expect(queryRetry(0, new ApiError('not found', 404))).toBe(true);
    expect(queryRetry(1, new ApiError('not found', 404))).toBe(false);
  });
});

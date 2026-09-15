import { afterEach, describe, expect, it, vi } from 'vitest';
import { cleanup, render, screen } from '@testing-library/svelte';
import { ApiError } from '$lib/api';
import ErrorNote from './ErrorNote.svelte';

// The panel every failure is drawn in. What matters is that the reader gets
// written words rather than the Go sentence, that the Go sentence is still
// reachable underneath, and that nothing is put in front of them whose only use
// is to repeat what just failed.

afterEach(() => {
  cleanup();
  vi.useRealTimers();
});

describe('ErrorNote', () => {
  it('writes the sentence rather than the words the server used', () => {
    render(ErrorNote, {
      error: new ApiError('slskd is not configured', 409, { title: 'slskd is not configured' })
    });

    expect(screen.getByText('Schall has no download source, so nothing can be fetched.')).toBeTruthy();
    expect(screen.getByText('Add the slskd address and key under Settings → Connections.')).toBeTruthy();
  });

  it('keeps the server’s own words behind a disclosure', () => {
    render(ErrorNote, {
      error: new ApiError('artist not found', 404, { title: 'artist not found' })
    });

    const disclosure = screen.getByText('What the server said').closest('details');
    expect(disclosure).toBeTruthy();
    // Closed, so it is not the first thing read, and there, so a bug report can
    // quote it.
    expect((disclosure as HTMLDetailsElement).open).toBe(false);
    expect(disclosure?.textContent).toContain('artist not found');
  });

  it('holds the raw words for a failure nobody wrote a sentence for', () => {
    render(ErrorNote, {
      error: new ApiError('a reason nobody anticipated', 500, {
        title: 'a reason nobody anticipated'
      })
    });

    expect(screen.getByText('Schall could not finish that.')).toBeTruthy();
    expect(screen.getByText('What the server said').closest('details')?.textContent).toContain(
      'a reason nobody anticipated'
    );
  });

  it('lets the screen say the second sentence in its own words', () => {
    render(ErrorNote, {
      error: new ApiError('artist not found', 404, { title: 'artist not found' }),
      action: 'Press Follow again once the list has reloaded.'
    });

    expect(screen.getByText('Press Follow again once the list has reloaded.')).toBeTruthy();
    expect(screen.queryByText('Go back to Artists.')).toBeNull();
  });

  it('asks again by itself where asking again is the only answer', async () => {
    vi.useFakeTimers();
    const retry = vi.fn();
    render(ErrorNote, { error: new ApiError('The server did not answer.', 0), retry });

    expect(screen.getByText('Nothing answered. Schall may be restarting.')).toBeTruthy();
    expect(screen.getByText('Trying again.')).toBeTruthy();

    await vi.advanceTimersByTimeAsync(4000);
    expect(retry).toHaveBeenCalledTimes(1);
  });

  it('never asks again for a failure a person has to act on', async () => {
    vi.useFakeTimers();
    const retry = vi.fn();
    render(ErrorNote, {
      error: new ApiError('slskd is not configured', 409, { title: 'slskd is not configured' }),
      retry
    });

    await vi.advanceTimersByTimeAsync(60_000);
    expect(retry).not.toHaveBeenCalled();
    expect(screen.queryByText('Trying again.')).toBeNull();
  });

  it('stops asking rather than asking forever', async () => {
    vi.useFakeTimers();
    const retry = vi.fn();
    render(ErrorNote, { error: new ApiError('The server did not answer.', 0), retry });

    await vi.advanceTimersByTimeAsync(120_000);
    expect(retry).toHaveBeenCalledTimes(2);
    expect(screen.getByText('Reload the page to ask again.')).toBeTruthy();
  });

  it('says where to sign in on a 401, naming no reload', () => {
    render(ErrorNote, {
      error: new ApiError('unauthorized', 401, { title: 'unauthorized' })
    });

    expect(screen.getByText('No sign-in on this connection.')).toBeTruthy();
    expect(
      screen.getByText('Open Schall at its usual address and sign in there.')
    ).toBeTruthy();
  });

  it('never asks again for a 401, because retrying repeats the same request', async () => {
    vi.useFakeTimers();
    const retry = vi.fn();
    render(ErrorNote, {
      error: new ApiError('unauthorized', 401, { title: 'unauthorized' }),
      retry
    });

    await vi.advanceTimersByTimeAsync(60_000);
    expect(retry).not.toHaveBeenCalled();
    expect(screen.queryByText('Trying again.')).toBeNull();
  });

  it('still asks again by itself for a 500', async () => {
    vi.useFakeTimers();
    const retry = vi.fn();
    render(ErrorNote, {
      error: new ApiError('a reason nobody anticipated', 500, {
        title: 'a reason nobody anticipated'
      }),
      retry
    });

    expect(screen.getByText('Trying again.')).toBeTruthy();
    await vi.advanceTimersByTimeAsync(4000);
    expect(retry).toHaveBeenCalledTimes(1);
  });
});

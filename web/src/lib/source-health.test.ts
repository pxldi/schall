import { describe, expect, it } from 'vitest';
import type { FailedJob, FailedJobs } from '$lib/api';
import { checkIsStale, failedThisWeek } from '$lib/source-health';

const now = Date.parse('2026-10-15T12:00:00Z');
const daysAgo = (days: number) => new Date(now - days * 24 * 60 * 60 * 1000).toISOString();

function job(kind: string, cause: string, failedAt: string): FailedJob {
  return {
    id: `${kind}-${failedAt}`,
    kind,
    lane: 'general',
    attempts: 6,
    maxAttempts: 6,
    error: 'x',
    cause,
    failedAt,
    createdAt: failedAt
  };
}

const failed: FailedJobs = {
  causes: [
    { cause: 'refused:slskd', title: '', detail: '', service: 'slskd', jobCount: 1, firstFailedAt: null, lastFailedAt: null },
    { cause: 'other', title: '', detail: '', jobCount: 3, firstFailedAt: null, lastFailedAt: null }
  ],
  items: [
    job('sync_listens', 'other', daysAgo(1)),
    job('sync_listens', 'other', daysAgo(2)),
    job('sync_listens', 'other', daysAgo(9)),
    job('search_album_sources', 'refused:slskd', daysAgo(3))
  ],
  total: 4,
  truncated: false
};

describe('failedThisWeek', () => {
  it('counts a sync job against its service when the error names no host', () => {
    expect(failedThisWeek(failed, 'ListenBrainz', now)).toBe(2);
  });

  it('counts a job against the service its cause names', () => {
    expect(failedThisWeek(failed, 'slskd', now)).toBe(1);
  });

  it('leaves out a service nothing failed against', () => {
    expect(failedThisWeek(failed, 'Navidrome', now)).toBe(0);
  });
});

describe('checkIsStale', () => {
  it('reads a check from two months ago as stale', () => {
    expect(checkIsStale(daysAgo(61), now)).toBe(true);
  });

  it('reads a check from a minute ago as current', () => {
    expect(checkIsStale(new Date(now - 60_000).toISOString(), now)).toBe(false);
  });

  it('reads a service never checked as stale', () => {
    expect(checkIsStale(null, now)).toBe(true);
  });
});

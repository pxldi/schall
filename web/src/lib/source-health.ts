import type { FailedJobs } from '$lib/api';

/** The services Settings › Sources reports on, by the name the server gives
 * a failure's cause. */
export type SourceName = 'slskd' | 'Navidrome' | 'ListenBrainz' | 'Spotify';

/** Jobs that talk to one service and nothing else. Their failures belong to
 * that service even when the error text names no host, which is how most
 * ListenBrainz sync failures read. */
const ownKinds: Record<SourceName, string[]> = {
  slskd: [],
  Navidrome: ['notify_player', 'sync_navidrome_playlists'],
  ListenBrainz: ['sync_listens'],
  Spotify: []
};

/** A connection check is a minute's truth. Older than this, Sources checks
 * again when it opens rather than repeating a reading from weeks ago. */
export const CHECK_STALE_MS = 60 * 60 * 1000;

const WEEK_MS = 7 * 24 * 60 * 60 * 1000;

export function checkIsStale(lastCheckedAt: string | null | undefined, now = Date.now()): boolean {
  if (!lastCheckedAt) return true;
  const at = Date.parse(lastCheckedAt);
  return Number.isNaN(at) || now - at > CHECK_STALE_MS;
}

/** How many jobs that needed this service failed in the last seven days. A
 * passing connection check says the service answers; this says whether the
 * work that uses it went through. */
export function failedThisWeek(
  failed: FailedJobs | undefined,
  source: SourceName,
  now = Date.now()
): number {
  const causes = new Set(
    (failed?.causes ?? []).filter((cause) => cause.service === source).map((cause) => cause.cause)
  );
  return (failed?.items ?? []).filter((job) => {
    if (!causes.has(job.cause) && !ownKinds[source].includes(job.kind)) return false;
    const at = job.failedAt ? Date.parse(job.failedAt) : NaN;
    return !Number.isNaN(at) && now - at <= WEEK_MS;
  }).length;
}

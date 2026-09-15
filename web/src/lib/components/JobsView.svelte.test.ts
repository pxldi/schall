import { afterEach, beforeAll, beforeEach, describe, expect, it, vi } from 'vitest';
import { cleanup, fireEvent, render, screen, setup, within } from '@testing-library/svelte';
import { QueryClient, QueryClientProvider } from '@tanstack/svelte-query';
import JobsView from '$lib/components/JobsView.svelte';
import type { FailedJob, FailureCause, Job, JobQueue, RecurringJob } from '$lib/api';

// The one thing this view has to get right is the difference between patience
// and a stall. A queue is full of jobs that have failed and are fine — the retry
// ladder is the design — and a view that painted every one of them red would
// make the operator stop reading it inside a day. So what is asserted here is
// which sentence a row tells and what colour it tells it in, not that the rows
// arrived.
//
// The failed half of the page is read as causes rather than as jobs. Seventy-one
// jobs stopped by one outage are one row, one explanation and one press, and the
// errors they recorded sit whole behind a disclosure on that row.
//
// The only boundary stubbed is `fetch`. Everything else is the component in the
// query client it lives in on the page.

const clock = new Date('2026-08-07T14:22:18Z').getTime();

function job(overrides: Partial<Job> = {}): Job {
  return {
    id: '48211000-0000-4000-8000-000000000001',
    kind: 'search_album_sources',
    lane: 'acquisition',
    status: 'queued',
    attempts: 0,
    maxAttempts: 5,
    runAfter: new Date(clock - 6_000).toISOString(),
    createdAt: new Date(clock - 6_000).toISOString(),
    ...overrides
  };
}

function queueOf(jobs: Job[]): JobQueue {
  return {
    lanes: [
      {
        lane: 'acquisition',
        label: 'Searching',
        runningCount: jobs.filter((one) => one.status === 'running').length,
        queuedCount: jobs.filter((one) => one.status === 'queued').length,
        jobs
      },
      { lane: 'transfers', label: 'Downloads', runningCount: 0, queuedCount: 0, jobs: [] },
      { lane: 'anchors', label: 'Anchors', runningCount: 0, queuedCount: 0, jobs: [] },
      { lane: 'judging', label: 'Judging', runningCount: 0, queuedCount: 0, jobs: [] },
      { lane: 'general', label: 'Everything else', runningCount: 0, queuedCount: 0, jobs: [] }
    ],
    total: jobs.length,
    truncated: false
  };
}

function failure(overrides: Partial<FailedJob> = {}): FailedJob {
  return {
    id: '48044000-0000-4000-8000-000000000002',
    kind: 'poll_downloads',
    lane: 'transfers',
    attempts: 5,
    maxAttempts: 5,
    error: 'Peer closed the connection after 9 of 11 files.',
    cause: 'unknown_host:musicbrainz',
    failedAt: new Date(clock - 12 * 60_000).toISOString(),
    createdAt: new Date(clock - 30 * 60_000).toISOString(),
    payload: { requestId: 'a1b2c3d4-0000-4000-8000-000000000003' },
    ...overrides
  };
}

// The causes the server would have grouped these failures into, written the way
// it writes them: what happened, then what the named thing is before what went
// wrong with it.
function causesOf(failed: FailedJob[]): FailureCause[] {
  const held = new Map<string, FailedJob[]>();
  for (const item of failed) {
    held.set(item.cause, [...(held.get(item.cause) ?? []), item]);
  }
  return [...held].map(([key, jobs]) => ({
    cause: key,
    title: 'Schall could not reach MusicBrainz.',
    detail:
      'Schall looks up artists, releases and recordings at musicbrainz.org. The name of that ' +
      'server could not be turned into an address from this machine, so no request was sent.',
    service: 'MusicBrainz',
    jobCount: jobs.length,
    firstFailedAt: jobs[jobs.length - 1]?.failedAt ?? null,
    lastFailedAt: jobs[0]?.failedAt ?? null
  }));
}

function sweep(overrides: Partial<RecurringJob> = {}): RecurringJob {
  return {
    kind: 'sweep_acquisition_targets',
    label: 'Acquisition sweep',
    lastRunAt: new Date(clock - 7 * 60_000).toISOString(),
    lastCompletedAt: new Date(clock - 7 * 60_000).toISOString(),
    running: false,
    nextRunAt: null,
    detail: 'No wanted recording is due.',
    ...overrides
  };
}

type Answers = {
  queue?: JobQueue;
  failed?: FailedJob[];
  causes?: FailureCause[];
  schedule?: RecurringJob[];
  retry?: (id: string) => unknown;
  retryCause?: (cause: string) => unknown;
  cancel?: (id: string) => unknown;
  cancelKind?: (kind: string) => unknown;
  // A queue that changes once the job it holds has been cancelled, so a test
  // can watch the row leave the page rather than only that the request was
  // sent. undefined means the answer never changes.
  queueAfterCancel?: JobQueue;
};

let pressed: string[] = [];
let pressedCauses: string[] = [];
let pressedCancels: string[] = [];
let pressedKinds: string[] = [];

function answering({
  queue,
  failed = [],
  causes,
  schedule = [],
  retry,
  retryCause,
  cancel,
  cancelKind,
  queueAfterCancel
}: Answers) {
  pressed = [];
  pressedCauses = [];
  pressedCancels = [];
  pressedKinds = [];
  let currentQueue = queue ?? queueOf([]);
  vi.stubGlobal(
    'fetch',
    vi.fn(async (path: string, init?: RequestInit) => {
      if (init?.method === 'POST' && path.endsWith('/failed/retry')) {
        const { cause } = JSON.parse(String(init.body ?? '{}')) as { cause: string };
        pressedCauses.push(cause);
        return new Response(
          JSON.stringify(
            retryCause?.(cause) ?? {
              cause,
              asked: failed.length,
              requeued: failed.length,
              detail: 'They were queued again, with all of their attempts back.'
            }
          ),
          { status: 200 }
        );
      }
      if (init?.method === 'POST' && path.endsWith('/queued/cancel')) {
        const { kind } = JSON.parse(String(init.body ?? '{}')) as { kind: string };
        pressedKinds.push(kind);
        return new Response(
          JSON.stringify(cancelKind?.(kind) ?? { kind, cancelled: 0, refused: false, detail: '' }),
          { status: 200 }
        );
      }
      if (init?.method === 'POST' && path.endsWith('/cancel')) {
        const id = path.split('/')[4] ?? '';
        pressedCancels.push(id);
        if (queueAfterCancel) currentQueue = queueAfterCancel;
        return new Response(JSON.stringify(cancel?.(id) ?? {}), { status: 200 });
      }
      if (init?.method === 'POST') {
        const id = path.split('/')[4] ?? '';
        pressed.push(id);
        return new Response(JSON.stringify(retry?.(id) ?? {}), { status: 200 });
      }
      if (path.endsWith('/failed')) {
        return new Response(
          JSON.stringify({
            causes: causes ?? causesOf(failed),
            items: failed,
            total: failed.length,
            truncated: false
          })
        );
      }
      if (path.endsWith('/schedule')) {
        return new Response(JSON.stringify({ items: schedule }));
      }
      return new Response(JSON.stringify(currentQueue));
    })
  );
}

function opened() {
  // The client is per test: a cache shared between them would answer the second
  // one from the first one's queue without asking anything.
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  return render(JobsView, undefined, {
    wrapper: QueryClientProvider,
    wrapperProps: { client }
  });
}

/** The row a piece of text sits in, so an assertion about colour is about that
 * row rather than about the page. */
function rowOf(element: Element | null) {
  return element?.closest('div.border-b') ?? null;
}

/** The button on one job, which lives inside the disclosure on its cause. The
 * one press a cause offers is outside it. */
function jobPress(name: string | RegExp = /Retry/) {
  const disclosure = document.querySelector('details') as HTMLElement;
  return within(disclosure).getByRole('button', { name });
}

function causePress(name: string | RegExp = /Retry/) {
  return screen.getAllByRole('button', { name })[0] as HTMLButtonElement;
}

beforeAll(setup);

beforeEach(() => {
  vi.useFakeTimers({ shouldAdvanceTime: true });
  vi.setSystemTime(clock);
});

afterEach(() => {
  vi.useRealTimers();
  vi.unstubAllGlobals();
  cleanup();
});

describe('JobsView', () => {
  // An error that stopped nothing is not reported in red. This job failed once,
  // has attempts left and is waiting on purpose, so it takes the idle role, the
  // neutral detail treatment, and a subtitle that counts down to the next
  // attempt rather than up from the last failure.
  it('reads a queued job with one failure behind it as patience rather than a failure', async () => {
    answering({
      queue: queueOf([
        job({
          attempts: 1,
          error: 'no source responded within 30s',
          runAfter: new Date(clock + 252_000).toISOString()
        })
      ])
    });
    opened();

    const chip = await screen.findByText('Backing off');

    expect(chip.getAttribute('data-tone')).toBe('neutral');
    expect(screen.getByText(/next attempt in 4m 12s/)).toBeTruthy();
    // The reason is written for a reader now, with the words the job recorded
    // one press away underneath, and the whole row stays in the quiet ink.
    expect(screen.getByText('Attempt 1 did not finish.')).toBeTruthy();
    expect(screen.getByText(/no source responded within 30s/).getAttribute('data-monospace')).toBe(
      'true'
    );
    expect(screen.getByText('What the server said')).toBeTruthy();
    expect(screen.queryByText('All attempts used')).toBeNull();
    expect(screen.getByRole('status').getAttribute('data-frame')).toBe('bare');
  });

  // The row shows the attempt it is about, not the attempts behind it: one
  // failure spent means the next try is the second of five.
  it('counts the attempt a backing-off job is waiting to make', async () => {
    answering({
      queue: queueOf([
        job({
          attempts: 1,
          error: 'no source responded within 30s',
          runAfter: new Date(clock + 252_000).toISOString()
        })
      ])
    });
    opened();

    expect(await screen.findByText('2 of 5')).toBeTruthy();
  });

  // Seventy-one jobs stopped by one outage are one problem. The page says what
  // that problem is, in words that begin at the beginning, and says how much of
  // the queue it took with it.
  it('reads the jobs one outage stopped as one cause rather than one row each', async () => {
    const stopped = [
      failure(),
      failure({ id: '48044000-0000-4000-8000-000000000003' }),
      failure({ id: '48044000-0000-4000-8000-000000000004' })
    ];
    answering({ failed: stopped });
    opened();

    expect(await screen.findByText('Schall could not reach MusicBrainz.')).toBeTruthy();
    expect(screen.getByText(/^3 jobs stopped on /)).toBeTruthy();
    // What MusicBrainz is and how it failed is kept, behind the disclosure. The
    // row itself says what happened, how much it stopped and what to press.
    expect(
      screen.getByText(/Schall looks up artists, releases and recordings/).closest('details')
    ).toBeTruthy();
    expect(screen.getAllByText('Schall could not reach MusicBrainz.')).toHaveLength(1);
  });

  // Attempts spent, nothing further will happen without somebody. That is the
  // one state the red is reserved for, and it is worn by the cause rather than
  // repeated on every job under it.
  it('reports a spent cause in red, with one press for every job it stopped', async () => {
    answering({ failed: [failure(), failure({ id: '48044000-0000-4000-8000-000000000003' })] });
    opened();

    // The cause wears the state; the jobs under it repeat it rather than
    // announce it, which is why the first chip on the page is the one asserted.
    const chip = (await screen.findAllByText('All attempts used'))[0];

    expect(chip.getAttribute('data-tone')).toBe('broken');
    expect(screen.getByRole('button', { name: 'Retry 2' })).toBeTruthy();
  });

  // The recorded error is evidence, not an alarm. It is never the first thing
  // shown, it is never shortened, and the whole of it is on the page to be read
  // and copied.
  it('keeps the error a job recorded whole, under the cause rather than in front of it', async () => {
    answering({ failed: [failure()] });
    opened();

    const error = await screen.findByText(/Peer closed the connection after 9 of 11 files\./);

    expect(error.getAttribute('data-tone')).toBe('quiet');
    expect(error.getAttribute('data-truncated')).toBe('false');
    expect(error.closest('details')).toBeTruthy();
  });

  // One press for the problem, not seventy-one presses for its symptoms.
  it('retries every job a cause stopped with one press', async () => {
    answering({ failed: [failure(), failure({ id: '48044000-0000-4000-8000-000000000003' })] });
    opened();

    await fireEvent.click(await screen.findByRole('button', { name: 'Retry 2' }));

    await vi.waitFor(() => expect(pressedCauses).toEqual(['unknown_host:musicbrainz']));
    expect(await screen.findByText('Queued again')).toBeTruthy();
  });

  // A second press is answered, not punished. The button reads Queued and
  // disables, and the neutral detail line says what happened. No red, no fail
  // chip, no toast.
  it('answers a second press on an already-queued job without reporting a failure', async () => {
    const queuedAt = new Date(clock - 4_000).toISOString();
    answering({
      failed: [failure()],
      retry: () => ({
        job: { ...job({ id: failure().id, kind: 'poll_downloads', runAfter: queuedAt }) },
        requeued: false,
        detail: 'Already queued. Nothing was changed.'
      })
    });
    opened();

    await screen.findByText('Schall could not reach MusicBrainz.');
    await fireEvent.click(jobPress());

    const button = await vi.waitFor(() => jobPress('Queued'));

    expect((button as HTMLButtonElement).disabled).toBe(true);
    const detail = screen.getByText(/Pressing again does not queue it twice/);
    expect(detail.textContent).toContain('Already queued 4 seconds ago');
    expect(detail.getAttribute('data-tone')).toBe('quiet');
  });

  // The error stays: until the retry resolves it is the only context the
  // operator has for whether the retry was reasonable.
  it('keeps the error that ended a job readable after it has been retried', async () => {
    answering({
      failed: [failure()],
      retry: () => ({
        job: { ...job({ id: failure().id, kind: 'poll_downloads' }) },
        requeued: true,
        detail: 'Queued again, with all of its attempts back.'
      })
    });
    opened();

    await screen.findByText('Schall could not reach MusicBrainz.');
    await fireEvent.click(jobPress());
    await vi.waitFor(() => jobPress('Queued'));

    expect(screen.getByText(/Peer closed the connection/)).toBeTruthy();
    // A retried job goes back to the first rung and to the busy role. It is new
    // work, not a wounded version of the old work.
    expect(screen.getByText('1 of 5')).toBeTruthy();
  });

  // No next time is an answer, so it is written as one. A dash would say what a
  // dash says everywhere else in Schall — a value that should be here is
  // missing — and the operator would go looking for a fault that is not there.
  it('answers the Next column with a condition when a sweep has no next time', async () => {
    answering({ schedule: [sweep()] });
    opened();

    const condition = await screen.findByText(/a release to become due/);

    expect(condition.textContent).toContain('Waits for');
    expect(screen.queryByText('—')).toBeNull();
    // Idle is the colour of a thing that is fine and not doing anything, which
    // is exactly what a sweep with nothing due is.
    expect(condition.getAttribute('data-role')).toBe('idle');
  });

  // The schedule grows with the database's one-row passes, so every added row
  // needs to keep its label and work visible in the same scrolling list.
  it('renders the additional scheduled passes in the longer list', async () => {
    answering({
      schedule: [
        sweep({ kind: 'sweep_duplicates' }),
        sweep({ kind: 'sweep_loudness' }),
        sweep({ kind: 'sweep_recommendations' }),
        sweep({ kind: 'sweep_copy_fingerprints' }),
        sweep({ kind: 'sweep_upgrade_candidates' }),
        sweep({ kind: 'sweep_transcode' }),
        sweep({ kind: 'refresh_weekly_playlist' }),
        sweep({ kind: 'refresh_new_releases_playlist' })
      ]
    });
    opened();

    expect(await screen.findByText('Duplicate sweep')).toBeTruthy();
    expect(screen.getByText('Measures loudness for library files')).toBeTruthy();
    expect(screen.getByText('Fetches and publishes listening recommendations')).toBeTruthy();
    expect(screen.getByText('Measures held copies for duplicate review')).toBeTruthy();
    expect(screen.getByText('Finds library files below the quality floor')).toBeTruthy();
    expect(screen.getByText('Shrinks library files to the configured format')).toBeTruthy();
    expect(screen.getByText('Refreshes the weekly playlist and its trials')).toBeTruthy();
    expect(screen.getByText('Updates the playlist of new releases the library owns')).toBeTruthy();
  });

  // The artist goes in front of the subject and the count after it, so the two
  // read the same way round rather than as one field the API happened to fill.
  it('names what a job is about, with the artist before it and the files after', async () => {
    answering({
      queue: queueOf([
        job({ subject: 'Spirit of Eden', subjectArtist: 'Talk Talk' }),
        job({
          id: '48180000-0000-4000-8000-000000000002',
          kind: 'import_download',
          subject: 'Kid A',
          subjectFileCount: 10
        })
      ])
    });
    opened();

    expect(await screen.findByText('search · Talk Talk — Spirit of Eden')).toBeTruthy();
    expect(screen.getByText('transfer · Kid A — 10 files')).toBeTruthy();
  });

  // A kind whose payload names nothing arrives without a subject, and the row
  // says the kind and stops. It never falls back to the identifier: a row
  // reading `ingest 8f4e2c1a` has said only that Schall cannot say.
  it('leaves a job with no subject as its bare kind', async () => {
    answering({ queue: queueOf([job({ kind: 'scan_library' })]) });
    opened();

    expect(await screen.findByText('library scan')).toBeTruthy();
  });

  // Following a download is its own lane in the worker, so a reader watching an
  // import land can see it apart from the catalogue backlog it used to sit
  // behind. The lane's own name is what says which queue is which.
  it('names the download lane apart from the search and general ones', async () => {
    const queue = queueOf([]);
    queue.lanes[1].jobs = [job({ kind: 'import_download', lane: 'transfers', status: 'running' })];
    queue.lanes[1].runningCount = 1;
    queue.total = 1;
    answering({ queue });
    opened();

    expect(await screen.findByText('Download lane')).toBeTruthy();
    expect(screen.queryByText('Search lane')).toBeNull();
  });

  // The judging lane has its own heading, so a long copy pass is visible apart
  // from the search and general work it no longer holds up.
  it('names the judging lane', async () => {
    const queue = queueOf([]);
    queue.lanes[3].jobs = [job({ kind: 'judge_want_copies', lane: 'judging', status: 'running' })];
    queue.lanes[3].runningCount = 1;
    queue.total = 1;
    answering({ queue });
    opened();

    expect(await screen.findByText('Judging lane')).toBeTruthy();
  });

  // A queue with nothing in it is the healthy state and says so plainly, with
  // no action: the operator did not come here to make work, only to see it.
  it('says the queue is empty rather than showing five empty lanes', async () => {
    answering({ queue: queueOf([]) });
    opened();

    expect(await screen.findByText('The queue is empty')).toBeTruthy();
    expect(screen.queryByText('Search lane')).toBeNull();
  });

  // A queue that cannot be read is either the server or the database behind it,
  // and neither is settled by asking a fourth time — the query has already asked
  // three. What the message carries instead is the one screen that says which,
  // because a reader handed a failure and no control is at a dead end.
  it('sends the reader to sources status when the queue cannot be read', async () => {
    vi.stubGlobal(
      'fetch',
      vi.fn(async () => new Response('read job queue: connection refused', { status: 503 }))
    );
    opened();

    // All three sections read the same queue, so all three refuse together and
    // each carries the same way out.
    const refusals = await screen.findAllByText(/The job queue could not be read\./);
    expect(refusals).toHaveLength(3);
    for (const link of screen.getAllByRole('link', { name: 'Check sources status' })) {
      expect(link.getAttribute('href')).toBe('/settings/sources');
    }

    // A queue that failed to load has no totals to report. 0 running and 0
    // queued is what an empty, healthy queue looks like, and drawing that next
    // to the refusal above would read as both at once.
    expect(screen.getByText('Unavailable')).toBeTruthy();
    expect(screen.queryByText(/0 running/)).toBeNull();
    expect(screen.queryByText(/0 queued/)).toBeNull();
  });

  it('presses retry against the job whose row the button is in', async () => {
    answering({
      failed: [failure()],
      retry: () => ({ job: job({ id: failure().id }), requeued: true, detail: '' })
    });
    opened();

    await screen.findByText('Schall could not reach MusicBrainz.');
    await fireEvent.click(jobPress());

    await vi.waitFor(() => expect(pressed).toEqual([failure().id]));
  });

  // A queued job can be cancelled from its own row, and the row leaves the
  // page once it is: cancelling deletes it rather than changing what it says.
  it('cancels the job whose row the Cancel button is in and removes it from the queue', async () => {
    answering({
      queue: queueOf([job()]),
      queueAfterCancel: queueOf([]),
      cancel: () => ({ job: job(), cancelled: true, detail: 'Cancelled. It will not run.' })
    });
    opened();

    await fireEvent.click(await screen.findByRole('button', { name: 'Cancel' }));

    await vi.waitFor(() => expect(pressedCancels).toEqual([job().id]));
    expect(await screen.findByText('The queue is empty')).toBeTruthy();
  });

  // A running job is being worked by a worker, so cancelling it is a different
  // problem: the row has no Cancel button to press.
  it('offers no Cancel button on a running job', async () => {
    answering({ queue: queueOf([job({ status: 'running' })]) });
    opened();

    await screen.findByText('search');
    expect(screen.queryByRole('button', { name: 'Cancel' })).toBeNull();
  });

  // One queued job of a kind is ordinary work, not a backlog, so the bulk
  // control has nothing to offer.
  it('offers no bulk cancel when only one job of a kind is queued', async () => {
    answering({ queue: queueOf([job()]) });
    opened();

    await screen.findByRole('button', { name: 'Cancel' });
    expect(screen.queryByRole('button', { name: 'Cancel all' })).toBeNull();
  });

  // Many queued jobs of one kind are the shape a runaway sweep takes, so the
  // bulk control appears with the count and cancels every one of them with a
  // single press, after asking first.
  it('cancels every queued job of a kind with one press, once confirmed', async () => {
    vi.stubGlobal('confirm', vi.fn(() => true));
    answering({
      queue: queueOf([
        job({ id: '48211000-0000-4000-8000-000000000001' }),
        job({ id: '48211000-0000-4000-8000-000000000002' })
      ]),
      cancelKind: (kind) => ({ kind, cancelled: 2, refused: false, detail: '2 jobs were cancelled.' })
    });
    opened();

    expect(await screen.findByText('search — 2 queued')).toBeTruthy();
    await fireEvent.click(screen.getByRole('button', { name: 'Cancel all' }));

    await vi.waitFor(() => expect(pressedKinds).toEqual(['search_album_sources']));
    expect(confirm).toHaveBeenCalledTimes(1);
  });

  // The list this counts from is the head of the queue, truncated at 200. A
  // bug that queued 1,494 jobs must not be undersold as 200 on the row or in
  // the question that asks before deleting every one of them — Cancel all
  // still removes the whole backlog, the server has no such limit.
  it('says "at least" rather than a total the page cannot see, once the queue is truncated', async () => {
    vi.stubGlobal('confirm', vi.fn(() => true));
    const queue = queueOf([
      job({ id: '48211000-0000-4000-8000-000000000001' }),
      job({ id: '48211000-0000-4000-8000-000000000002' })
    ]);
    queue.truncated = true;
    answering({ queue });
    opened();

    expect(await screen.findByText('search — at least 2 queued')).toBeTruthy();
    await fireEvent.click(screen.getByRole('button', { name: 'Cancel all' }));

    expect(confirm).toHaveBeenCalledWith(expect.stringContaining('at least 2 are queued now'));
  });

  // Declining the confirmation sends nothing: a bulk cancel deletes rows a
  // person cannot see one by one, so it must not act on a press alone.
  it('sends nothing when the bulk cancel confirmation is declined', async () => {
    vi.stubGlobal('confirm', vi.fn(() => false));
    answering({
      queue: queueOf([
        job({ id: '48211000-0000-4000-8000-000000000001' }),
        job({ id: '48211000-0000-4000-8000-000000000002' })
      ])
    });
    opened();

    await fireEvent.click(await screen.findByRole('button', { name: 'Cancel all' }));

    expect(pressedKinds).toEqual([]);
  });

  // A kind that is a recurring sweep's own schedule answers zero cancelled and
  // says why, so the press does not read as though nothing had been queued.
  it('says why a scheduled sweep kind was refused rather than reporting nothing changed', async () => {
    vi.stubGlobal('confirm', vi.fn(() => true));
    answering({
      queue: queueOf([
        job({ id: '48211000-0000-4000-8000-000000000001', kind: 'sweep_genres' }),
        job({ id: '48211000-0000-4000-8000-000000000002', kind: 'sweep_genres' })
      ]),
      cancelKind: (kind) => ({
        kind,
        cancelled: 0,
        refused: true,
        detail: 'That kind runs on its own schedule and cannot be bulk-cancelled. ' +
          'Cancel it from its own row instead.'
      })
    });
    opened();

    await fireEvent.click(await screen.findByRole('button', { name: 'Cancel all' }));

    expect(await screen.findByText(/runs on its own schedule/)).toBeTruthy();
  });
});

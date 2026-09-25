<script lang="ts">
  import { createMutation, createQuery, useQueryClient } from '@tanstack/svelte-query';
  import { ChevronRight, LoaderCircle, RotateCcw, X } from '@lucide/svelte';
  import {
    api,
    type FailedJob,
    type FailureCause,
    type Job,
    type JobLaneQueue,
    type RecurringJob
  } from '$lib/api';
  import Button from '$lib/components/Button.svelte';
  import EmptyPanel from '$lib/components/EmptyPanel.svelte';
  import ErrorNote from '$lib/components/ErrorNote.svelte';
  import StateTag from '$lib/components/StateTag.svelte';
  import StatusBadge from '$lib/components/StatusBadge.svelte';
  import { clockTime, elapsedInWords, relativeTime } from '$lib/utils';

  // Everything Schall does runs through one PostgreSQL job queue, and this is
  // the view that lets the operator see it. Three things, in this order: what is
  // running and queued, split by the five lanes the worker already divides; what
  // failed terminally, read as the problems behind it rather than as the jobs in
  // it; and the pulse of the recurring sweeps.
  //
  // Nothing here is a new component. Every job line is the compact row grid, the
  // lanes are a label and a hairline, state is the chip and the glyph set, a
  // cause is the panel, Retry and Cancel are the xs button, and a quiet queue
  // is the empty panel.
  //
  // Nothing here is ever graded `decide`. A decision is Review's job, and this
  // view makes none: there is no reprioritise. Cancel removes a row rather than
  // changing what it does, on one row at a time or on every queued row of one
  // kind — the shape a runaway sweep takes (#492). A queue the operator can
  // reorder is a queue whose behaviour cannot be reasoned about from the code
  // that fills it; a queue the operator can empty is not that.

  type Role = 'ok' | 'idle' | 'busy' | 'fail';

  const queryClient = useQueryClient();

  // The two intervals are the safety net rather than the mechanism: every job
  // boundary publishes a notice, and the event stream invalidates these three
  // keys on all of them. A jobs view is exactly where a permanent poll would be
  // tempting and wrong, so each one asks again only while its own answer is
  // moving — a queue with work in it, and a sweep that is running.
  const queue = createQuery({
    queryKey: ['jobs'],
    queryFn: api.jobs,
    refetchInterval: (query) => ((query.state.data?.total ?? 0) > 0 ? 15_000 : false)
  });

  // The failed list has no interval at all. A job can only newly fail at a job
  // boundary, and a job boundary is the one thing that always publishes, so
  // there is nothing a timer here would catch that the stream does not.
  const failed = createQuery({ queryKey: ['jobs-failed'], queryFn: api.failedJobs });

  const schedule = createQuery({
    queryKey: ['jobs-schedule'],
    queryFn: api.jobSchedule,
    refetchInterval: (query) =>
      (query.state.data?.items ?? []).some((item) => item.running) ? 15_000 : false
  });

  // A countdown that only moves when the server answers is a stopped countdown,
  // and the difference between a job waiting on purpose and a job that is stuck
  // is the only thing this view has to get right.
  //
  // The wall clock is the one thing on this page that cannot be derived from
  // anything else, so it is an effect over an external system rather than state
  // an assignment could have avoided. It costs one refetch of nothing per
  // second and no request at all.
  let now = $state(Date.now());
  $effect(() => {
    const tick = setInterval(() => (now = Date.now()), 1000);
    return () => clearInterval(tick);
  });

  // StateTag's tones, mapped from this view's own roles: attention while a
  // job is running or work is back in flight, broken once its attempts are
  // spent, neutral for a job waiting its turn — patience and a stall are not
  // told apart by colour here, only by the words in the tag and beside it.
  const tones: Record<Role, 'neutral' | 'attention' | 'broken'> = {
    ok: 'neutral',
    idle: 'neutral',
    busy: 'attention',
    fail: 'broken'
  };

  // Searching, following a download, anchoring, judging, and everything else, because that is the
  // split the worker already makes. The endpoint names the lanes for the wire;
  // these are the words the reader sees, and the reader is asking "is search
  // backed up" far more often than "what is this job's lane".
  const laneNames: Record<string, string> = {
    acquisition: 'Search lane',
    transfers: 'Download lane',
    anchors: 'Anchor lane',
    judging: 'Judging lane',
    general: 'General lane'
  };

  // What a job kind is called out loud. Every kind Schall can queue is named
  // here: the queue's own word is a database column with its underscores taken
  // out, and `sweep follow feed` tells a reader nothing.
  //
  // What the two import kinds are called follows the card rather than the
  // table: a completed download being taken in is `transfer · Kid A — 10 files`
  // and a folder somebody handed over is `import · /incoming/talk-talk`. The
  // word that separates them is where it came from, not which function runs.
  //
  // The recurring passes take the same names here as in the pulse below.
  const kindNames: Record<string, string> = {
    answer_peer_challenges: 'answer peer',
    apply_library_layout: 'move files',
    clean_inbox: 'inbox cleanup',
    import_download: 'transfer',
    import_playlist: 'playlist import',
    import_upload: 'import',
    ingest_release_group: 'add release',
    notify_player: 'tell player',
    poll_downloads: 'transfer',
    refresh_album_metadata: 'release details',
    refresh_artist_metadata: 'artist details',
    refresh_label_metadata: 'label releases',
    resolve_library_file: 'identify file',
    scan_library: 'library scan',
    search_album_sources: 'search',
    sweep_acquisition_targets: 'acquisition sweep',
    sweep_cover_art: 'cover-art sweep',
    sweep_copy_fingerprints: 'copy fingerprint sweep',
    sweep_duplicates: 'duplicate sweep',
    sweep_follow_feed: 'follow-feed sweep',
    sweep_genres: 'genre backfill',
    sweep_loudness: 'loudness measurement',
    sweep_lyrics: 'lyrics sweep',
    sweep_preview_anchors: 'preview sweep',
    sweep_recommendations: 'suggestions sweep',
    sweep_own_recommendations: 'Schall suggestions sweep',
    sync_listens: 'listening history sync',
    sweep_audio_spectrum: 'audio measurement',
    sweep_transcode: 'transcode sweep',
    sweep_upgrade_candidates: 'upgrade sweep',
    refresh_new_releases_playlist: 'new releases refresh',
    refresh_weekly_playlist: 'weekly playlist refresh',
    sync_navidrome_playlists: 'playlist sync',
    tag_file: 'write tags',
    tag_library: 'write tags'
  };

  // The recurring passes. The endpoint carries a label and a full sentence
  // about why a pass has no next time; what it has no room for is the one line
  // saying what each pass is for and the short condition the Next column reads
  // as prose. Both of those are copy, so they live here, keyed by the kind the
  // endpoint names.
  type SweepCopy = { label: string; does: string; waitsFor: string; whileNext?: string };

  const sweepCopy: Record<string, SweepCopy> = {
    poll_downloads: {
      label: 'Transfer poller',
      does: 'Follows transfers that are moving',
      waitsFor: 'a transfer to start',
      // Safe to say beside a time: the poller only reschedules itself while a
      // transfer is moving, so having a next time is what makes it true.
      whileNext: 'while it is moving'
    },
    sweep_follow_feed: {
      label: 'Follow-feed sweep',
      does: 'Checks followed artists and labels for new releases',
      waitsFor: 'a worker to run it'
    },
    sweep_acquisition_targets: {
      label: 'Acquisition sweep',
      does: 'Queues searches for releases that are due',
      waitsFor: 'a release to become due'
    },
    sweep_cover_art: {
      label: 'Cover-art sweep',
      does: 'Fetches art for releases that have none',
      waitsFor: 'an import to finish'
    },
    sweep_lyrics: {
      label: 'Lyrics sweep',
      does: 'Writes song lyrics beside the music',
      waitsFor: 'a worker to run it'
    },
    sweep_preview_anchors: {
      label: 'Preview sweep',
      does: 'Fetches a sample of each wanted song',
      waitsFor: 'a worker to run it'
    },
    tag_library: {
      label: 'Tag write sweep',
      does: 'Rewrites tags for files whose catalogue data changed since they were matched',
      waitsFor: 'a worker to run it'
    },
    sweep_duplicates: {
      label: 'Duplicate sweep',
      does: 'Keeps one proven copy of duplicate recordings',
      waitsFor: 'a worker to run it'
    },
    sweep_loudness: {
      label: 'Loudness measurement',
      does: 'Measures loudness for library files',
      waitsFor: 'a worker to run it'
    },
    sweep_recommendations: {
      label: 'Recommendation sweep',
      does: 'Fetches and publishes listening recommendations',
      waitsFor: 'an enabled ListenBrainz account'
    },
    sweep_own_recommendations: {
      label: 'Schall recommendation sweep',
      does: 'Builds the Schall recommendations from your listens',
      waitsFor: 'a listen that names a recording'
    },
    sync_listens: {
      label: 'Listening history sync',
      does: 'Copies listens from ListenBrainz for the Overview',
      waitsFor: 'an enabled ListenBrainz account'
    },
    sweep_copy_fingerprints: {
      label: 'Copy fingerprint sweep',
      does: 'Measures held copies for duplicate review',
      waitsFor: 'a worker to run it'
    },
    sweep_upgrade_candidates: {
      label: 'Upgrade sweep',
      does: 'Finds library files below the quality floor',
      waitsFor: 'a worker to run it'
    },
    sweep_audio_spectrum: {
      label: 'Audio measurement',
      does: 'Measures the audio spectrum of library files',
      waitsFor: 'a worker to run it'
    },
    sweep_genres: {
      label: 'Genre backfill',
      does: 'Fills missing genres from catalogue metadata',
      waitsFor: 'a worker to run it'
    },
    sweep_transcode: {
      label: 'Transcode sweep',
      does: 'Shrinks library files to the configured format',
      waitsFor: 'a transcode scan'
    },
    refresh_weekly_playlist: {
      label: 'Weekly playlist refresh',
      does: 'Refreshes the weekly playlist and its trials',
      waitsFor: 'the weekly playlist to be enabled'
    },
    refresh_new_releases_playlist: {
      label: 'New releases refresh',
      does: 'Updates the playlist of new releases the library owns',
      waitsFor: 'a feed pass or library scan'
    }
  };

  // The fallback is for a kind added to the queue after this table was written,
  // and it is the one case where the reader sees the queue's own word. A kind
  // that reaches it belongs in the table above.
  function kindName(kind: string) {
    return kindNames[kind] ?? kind.replaceAll('_', ' ');
  }

  // A job row says `job 48044` out loud, and a Schall job is a UUID. The leading
  // group is enough to tell two rows apart and to find one in the server log,
  // which is what an identifier in a row is for.
  function shortId(id: string) {
    return id.split('-')[0] ?? id;
  }

  // The three fields both job shapes carry, named once so the two rows that
  // write a primary can take either.
  type JobSubject = Pick<Job, 'subject' | 'subjectArtist' | 'subjectFileCount'>;

  // What the job is about, in the words the catalogue holds. The API resolves
  // it from the payload's identifiers, because the join back to a title lives
  // there and a row that read `import · request 8f4e2c1a` told nobody anything.
  //
  // The artist goes in front of the subject and a count after it, which is the
  // difference between `search · Talk Talk — Spirit of Eden` and
  // `transfer · Kid A — 10 files`. Both are absent as often as not.
  function subjectOf(job: JobSubject) {
    if (!job.subject) return '';
    const parts = [job.subject];
    if (job.subjectArtist) parts.unshift(job.subjectArtist);
    if (job.subjectFileCount) {
      parts.push(`${job.subjectFileCount} ${job.subjectFileCount === 1 ? 'file' : 'files'}`);
    }
    return parts.join(' — ');
  }

  // A kind whose payload names nothing, and one whose subject has since been
  // deleted, arrive without one. The row is its bare kind then, which says less
  // than it could and nothing that is untrue.
  function primaryOf(job: JobSubject & { kind: string }) {
    const subject = subjectOf(job);
    return subject ? `${kindName(job.kind)} · ${subject}` : kindName(job.kind);
  }

  // Always shown, including on the first try: a number that appears only once
  // something has gone wrong makes the reader hunt for the rows that have it.
  //
  // It counts the attempt this row is about rather than the attempts behind it.
  // Claim adds one as it takes the job, so a running job is already on the
  // attempt it shows, and a queued one is showing the attempt it will make.
  function attemptsOf(status: string, attempts: number, maxAttempts: number) {
    const at = status === 'queued' ? attempts + 1 : attempts;
    return `${Math.min(Math.max(at, 1), maxAttempts)} of ${maxAttempts}`;
  }

  // How many tries are behind this one. Written as a word because it is prose
  // in a sentence rather than a figure in a column.
  const numbers = ['', 'One', 'Two', 'Three', 'Four', 'Five', 'Six', 'Seven', 'Eight', 'Nine'];

  function attemptsLeft(remaining: number) {
    if (remaining < 1) return 'This is its last attempt.';
    const count = numbers[remaining] ?? String(remaining);
    const noun = remaining === 1 ? 'attempt' : 'attempts';
    return `${count} ${noun} left; the wait doubles each time.`;
  }

  type ActiveState = { role: Role; chip: string; when: string };

  // Which sentence the row tells. Patience and a stall are told apart here and
  // nowhere else: a job that failed once, has attempts left and is waiting on
  // purpose takes the idle role and counts down to its next attempt, rather
  // than counting up from its last failure in red.
  function stateOf(job: Job, at: number): ActiveState {
    if (job.status === 'running') {
      return { role: 'busy', chip: 'Running', when: `started ${relativeTime(job.startedAt, at)}` };
    }
    const due = job.runAfter ? new Date(job.runAfter).getTime() : 0;
    if (due > at) {
      return job.attempts > 0
        ? { role: 'idle', chip: 'Backing off', when: `next attempt ${relativeTime(job.runAfter, at)}` }
        : { role: 'idle', chip: 'Queued', when: `due ${relativeTime(job.runAfter, at)}` };
    }
    return {
      role: 'idle',
      chip: 'Queued',
      when: `queued ${relativeTime(job.runAfter ?? job.createdAt, at)}`
    };
  }

  const lanes = $derived($queue.data?.lanes ?? []);
  const quiet = $derived(($queue.data?.total ?? 0) === 0);

  // The "In flight" heading's own dim readout: every lane the worker divides
  // work into, whether or not it currently has anything queued in it.
  const runningTotal = $derived(lanes.reduce((sum, lane) => sum + lane.runningCount, 0));
  const queuedTotal = $derived(lanes.reduce((sum, lane) => sum + lane.queuedCount, 0));

  const placeholderRowCount = 30;

  // --- Cancel ---

  // Cancelling removes the row rather than changing what it does, so there is
  // nothing to hold on the page the way a retried job's answer is held: the
  // job leaves the list the moment the query is invalidated, and the refusal
  // is the only thing worth keeping past that.
  let jobCancelRefusal = $state<{ id: string; error: unknown } | null>(null);

  const cancel = createMutation({
    mutationFn: (job: Job) => api.cancelJob(job.id),
    onMutate: () => {
      jobCancelRefusal = null;
    },
    onSuccess: async () => {
      await Promise.all([
        queryClient.invalidateQueries({ queryKey: ['jobs'] }),
        queryClient.invalidateQueries({ queryKey: ['jobs-schedule'] })
      ]);
    },
    onError: (error: Error, job: Job) => {
      jobCancelRefusal = { id: job.id, error };
    }
  });

  // A kind with more than one queued row in the visible page is the shape a
  // runaway sweep takes: a bug queueing the same kind over and over rather
  // than the ordinary handful of concurrent work. Running jobs are not
  // counted — cancelling never reaches one — and a kind held to one queued
  // row by its own schedule never shows more than one here either.
  //
  // `atLeast` carries whether the count is the true size of the backlog or
  // only the head of it: the list this counts from is truncated at 200, so a
  // bug that queued 1,494 jobs is read here as 200 unless the row and the
  // confirmation both say so. Cancel all still removes every one of them —
  // the server has no such limit — so the count is the one thing that must
  // not overstate what the button is about to do.
  function kindBacklogs(lane: JobLaneQueue, truncated: boolean) {
    const counts = new Map<string, number>();
    for (const job of lane.jobs) {
      if (job.status !== 'queued') continue;
      counts.set(job.kind, (counts.get(job.kind) ?? 0) + 1);
    }
    return [...counts.entries()]
      .filter(([, count]) => count > 1)
      .map(([kind, count]) => ({ kind, count, atLeast: truncated }));
  }

  function backlogCount(backlog: { count: number; atLeast: boolean }) {
    return backlog.atLeast ? `at least ${backlog.count}` : `${backlog.count}`;
  }

  // What a press against a kind found. Held against the kind rather than
  // discarded on success, because a refusal — a kind that is a recurring
  // sweep's own schedule — leaves the same backlog row on screen with nothing
  // cancelled, and the row has to say why or the press reads as though it did
  // nothing at all.
  let kindCancels = $state<Record<string, { pending: boolean; detail?: string }>>({});
  let kindCancelRefusal = $state<{ kind: string; error: unknown } | null>(null);

  const cancelKind = createMutation({
    mutationFn: (kind: string) => api.cancelQueuedJobsByKind(kind),
    onMutate: (kind: string) => {
      kindCancelRefusal = null;
      kindCancels = { ...kindCancels, [kind]: { pending: true } };
    },
    onSuccess: async (result, kind: string) => {
      kindCancels = { ...kindCancels, [kind]: { pending: false, detail: result.detail } };
      await Promise.all([
        queryClient.invalidateQueries({ queryKey: ['jobs'] }),
        queryClient.invalidateQueries({ queryKey: ['jobs-schedule'] })
      ]);
    },
    onError: (error: Error, kind: string) => {
      const { [kind]: _dropped, ...rest } = kindCancels;
      kindCancels = rest;
      kindCancelRefusal = { kind, error };
    }
  });

  // Bulk-cancelling deletes rows a person cannot see one by one — the whole
  // point of the control — so it asks first, the same way unfollowing a
  // playlist does. The count in the question is the same one on the row: at
  // least, rather than a total the page cannot see.
  function confirmCancelKind(backlog: { kind: string; count: number; atLeast: boolean }) {
    const question =
      `Cancel every queued “${kindName(backlog.kind)}” job? ` +
      `${backlogCount(backlog)} are queued now. This cannot be undone.`;
    if (confirm(question)) {
      $cancelKind.mutate(backlog.kind);
    }
  }

  // --- Retry ---

  // What a press came to, held on the page rather than read back from the
  // server. A retried job leaves the failed list the moment it is queued again,
  // and the failure that caused it is the only context the operator has for
  // whether the retry was reasonable — so the row stays, with its error, until
  // the page is left.
  type Press = {
    failure: FailedJob;
    pending: boolean;
    at: number;
    job?: Job;
    requeued?: boolean;
    detail?: string;
  };

  let presses = $state<Record<string, Press>>({});
  // What the press was refused with, held against the row it was pressed on.
  // The thrown error itself rather than its text: `ErrorNote` is what turns a
  // failure into words, and it needs the whole thing to do it.
  let refusal = $state<{ id: string; error: unknown } | null>(null);

  const retry = createMutation({
    mutationFn: (failure: FailedJob) => api.retryJob(failure.id),
    onMutate: (failure: FailedJob) => {
      refusal = null;
      presses = { ...presses, [failure.id]: { failure, pending: true, at: Date.now() } };
    },
    onSuccess: async (result, failure: FailedJob) => {
      presses = {
        ...presses,
        [failure.id]: {
          failure,
          pending: false,
          at: Date.now(),
          job: result.job,
          requeued: result.requeued,
          detail: result.detail
        }
      };
      await Promise.all([
        queryClient.invalidateQueries({ queryKey: ['jobs-failed'] }),
        queryClient.invalidateQueries({ queryKey: ['jobs'] })
      ]);
    },
    // The press itself was refused — a cancelled job, or the queue unreachable.
    // That is a failure that happened, so it is reported in red at the row it
    // was pressed on, and the row goes back to being a failure.
    onError: (error: Error, failure: FailedJob) => {
      const { [failure.id]: _dropped, ...rest } = presses;
      presses = rest;
      refusal = { id: failure.id, error };
    }
  });

  // The failed list, with anything retried held in front of it. Retried rows
  // float to the top because they are what was just acted on; everything the
  // server still calls failed follows in the order it answered with.
  const failures = $derived.by(() => {
    const live = $failed.data?.items ?? [];
    const listed = new Set(live.map((item) => item.id));
    const held = Object.values(presses)
      .filter((press) => !listed.has(press.failure.id))
      .sort((first, second) => second.at - first.at)
      .map((press) => press.failure);
    return [...held, ...live];
  });

  type FailureRow = {
    role: Role;
    chip: string;
    when: string;
    figure: string;
    label: string;
    /** True while the job is on its way again, which is what disables Retry. */
    held: boolean;
    spinning: boolean;
    /** The neutral line under the row, empty when there is nothing to say. */
    detail: string;
  };

  // One failed row, read as one thing rather than as six conditionals in the
  // markup. A row that has been retried wears the busy role and a fresh ladder,
  // because that is what it now is: new work rather than a wounded version of
  // the old work.
  function failureRow(failure: FailedJob, press: Press | undefined, at: number): FailureRow {
    const job = press?.job;
    const running = job?.status === 'running';
    // A press whose answer says the job is neither queued nor running found one
    // that had moved on — it has since finished, or it has failed again — so
    // the row goes back to being a failure with something left to press, and
    // the server's own sentence about what it found is kept under it.
    const held = press ? press.pending || running || job?.status === 'queued' : false;

    if (!press || !held) {
      return {
        role: 'fail',
        chip: 'All attempts used',
        when: `last tried ${relativeTime(failure.failedAt, at)}`,
        figure: attemptsOf('failed', failure.attempts, failure.maxAttempts),
        label: 'Retry',
        held: false,
        spinning: false,
        detail: press?.detail ?? ''
      };
    }

    // While the request is in flight there is no answer to read, so the row
    // shows the first rung of the ladder it is about to be put back on.
    const queued = job?.runAfter ?? new Date(press.at).toISOString();

    return {
      role: 'busy',
      chip: running ? 'Running' : 'Queued',
      when: `queued again ${relativeTime(queued, at)}`,
      figure: job
        ? attemptsOf(job.status, job.attempts, job.maxAttempts)
        : `1 / ${failure.maxAttempts}`,
      label: press.pending ? 'Retrying…' : running ? 'Running' : 'Queued',
      held,
      spinning: press.pending,
      // A second press is answered, not punished. Retrying something already
      // queued is the most ordinary mistake in this view and it is not a
      // failure: the button reads Queued and disables, and the neutral detail
      // line says what happened. No red, no fail chip, no toast — red is for a
      // failure that happened, and pressing a button twice is not one.
      detail:
        press.pending || press.requeued !== false
          ? ''
          : `Already queued ${elapsedInWords(queued, at)}. Pressing again does not queue it twice.`
    };
  }

  // --- Causes ---

  // The failed list is read as the problems behind it rather than as the jobs
  // in it. Seventy-one jobs that could not resolve the name musicbrainz.org are
  // one outage: one row, one sentence about what MusicBrainz is and what went
  // wrong with it, and one press that puts all seventy-one back on the queue.
  //
  // Which jobs belong to which cause is decided by the server, from the error
  // each job recorded. Nothing here reads a Go error string, and nothing here
  // shortens one: the recorded text sits behind the disclosure, whole.
  type CausePress = {
    cause: FailureCause;
    // The jobs the press covered, kept because they leave the failed list the
    // moment they are queued again and the reader is still looking at the row.
    jobs: FailedJob[];
    pending: boolean;
    at: number;
    requeued?: number;
    detail?: string;
  };

  let causePresses = $state<Record<string, CausePress>>({});
  let causeRefusal = $state<{ cause: string; error: unknown } | null>(null);

  const retryCause = createMutation({
    mutationFn: (cause: FailureCause) => api.retryFailedCause(cause.cause),
    onMutate: (cause: FailureCause) => {
      causeRefusal = null;
      causePresses = {
        ...causePresses,
        [cause.cause]: { cause, jobs: jobsFor(cause), pending: true, at: Date.now() }
      };
    },
    onSuccess: async (result, cause: FailureCause) => {
      causePresses = {
        ...causePresses,
        [cause.cause]: {
          ...causePresses[cause.cause],
          cause,
          pending: false,
          at: Date.now(),
          requeued: result.requeued,
          detail: result.detail
        }
      };
      await Promise.all([
        queryClient.invalidateQueries({ queryKey: ['jobs-failed'] }),
        queryClient.invalidateQueries({ queryKey: ['jobs'] })
      ]);
    },
    onError: (error: Error, cause: FailureCause) => {
      const { [cause.cause]: _dropped, ...rest } = causePresses;
      causePresses = rest;
      causeRefusal = { cause: cause.cause, error };
    }
  });

  // A cause that has just been retried stays on the page. Its jobs are back on
  // the queue, so the server no longer lists it, and the reader would otherwise
  // press a button and watch the answer disappear.
  const causes = $derived.by(() => {
    const live = $failed.data?.causes ?? [];
    const listed = new Set(live.map((cause) => cause.cause));
    const held = Object.values(causePresses)
      .filter((press) => !listed.has(press.cause.cause))
      .sort((first, second) => second.at - first.at)
      .map((press) => press.cause);
    return [...held, ...live];
  });

  // The jobs one cause holds. While the server still lists them they are read
  // from the list; once they have been queued again they are what the press
  // took with it, so the disclosure under a retried cause is not empty.
  function jobsFor(cause: FailureCause): FailedJob[] {
    const live = failures.filter((failure) => failure.cause === cause.cause);
    if (live.length > 0) return live;
    return causePresses[cause.cause]?.jobs ?? [];
  }

  // The line under the cause: how much it stopped, and over what span. Both
  // dates come from the server, because neither is on any single row.
  function dayName(value: string | null) {
    if (!value) return '';
    const at = new Date(value);
    if (!Number.isFinite(at.getTime())) return '';
    return at.toLocaleDateString(undefined, { day: 'numeric', month: 'long' });
  }

  function stopped(cause: FailureCause) {
    const jobs = cause.jobCount === 1 ? 'One job' : `${cause.jobCount} jobs`;
    const first = dayName(cause.firstFailedAt);
    const last = dayName(cause.lastFailedAt);
    if (!first || !last) return `${jobs} stopped.`;
    if (first === last) return `${jobs} stopped on ${last}.`;
    return `${jobs} stopped between ${first} and ${last}.`;
  }

  type CauseRow = { role: Role; chip: string; label: string; held: boolean; spinning: boolean; detail: string };

  // One cause row. A press puts it in the busy role, because what it now is, is
  // work in flight rather than a failure that is happening.
  function causeRow(cause: FailureCause, press: CausePress | undefined): CauseRow {
    const label = cause.jobCount === 1 ? 'Retry' : `Retry ${cause.jobCount}`;
    if (!press) {
      return { role: 'fail', chip: 'All attempts used', label, held: false, spinning: false, detail: '' };
    }
    if (press.pending) {
      return { role: 'busy', chip: 'Retrying', label: 'Retrying…', held: true, spinning: true, detail: '' };
    }
    // Nothing moved. That is the second press, or a cause whose jobs went back
    // on the queue by themselves while this page was open — neither is a
    // failure, so it is said in the neutral line rather than in red.
    return {
      role: press.requeued ? 'busy' : 'fail',
      chip: press.requeued ? 'Queued again' : 'All attempts used',
      label,
      held: Boolean(press.requeued),
      spinning: false,
      detail: press.detail ?? ''
    };
  }

  // --- The sweep pulse ---

  function sweepLabel(item: RecurringJob) {
    return sweepCopy[item.kind]?.label ?? item.label;
  }

  // No next time is an answer, so it is written as one. A dash would say what a
  // dash says everywhere else in Schall — a value that should be here is
  // missing — and the operator would go looking for a fault that is not there.
  // So the column answers in one of two grammars: a time, set as a tabular
  // figure, or a condition, set as prose with a quieter lead.
  function waitsFor(item: RecurringJob) {
    if (item.running) return { lead: 'Asks for', rest: 'its next time when this pass finishes' };
    const copy = sweepCopy[item.kind];
    if (copy) return { lead: 'Waits for', rest: copy.waitsFor };
    return { lead: '', rest: item.detail ?? 'Nothing is scheduled' };
  }

  function nextClause(item: RecurringJob) {
    const clause = sweepCopy[item.kind]?.whileNext;
    return clause ? `, ${clause}` : '';
  }

  function causeTitle(cause: FailureCause) {
    return cause.cause === 'other' ? 'These jobs failed for different reasons.' : cause.title;
  }

  function causeDetail(cause: FailureCause) {
    return cause.cause === 'other'
      ? 'Review each recorded error below, then retry the jobs you want to run again.'
      : cause.detail;
  }
</script>

<div class="flex flex-col gap-11">
  <!-- ── In flight ────────────────────────────────────────────────────── -->
  <section>
    <div class="mb-[18px] flex items-baseline justify-between border-b border-line-thin pb-2">
      <h2 class="font-display text-lead font-bold text-ink">In flight</h2>
      <span class="numeric text-meta text-ink-4">
        <!-- A queue that has not loaded, or failed to, has no totals to report:
             0 lanes, 0 running and 0 queued reads as an empty queue rather than
             as a read nothing has answered yet. -->
        {#if $queue.isPending}
          loading…
        {:else if $queue.isError}
          Unavailable
        {:else}
          {lanes.length} {lanes.length === 1 ? 'lane' : 'lanes'} ·
          {runningTotal} running · {queuedTotal} queued
        {/if}
      </span>
    </div>

    {#if $queue.isPending}
      <div
        class="flex flex-col gap-[26px]"
        style="max-height: calc(100dvh - 16rem); overflow: hidden;"
        aria-hidden="true"
      >
        <div>
          <span class="label mb-1 block border-b border-line-thin pb-2">
            Search lane
            <span class="numeric ml-2 tracking-normal text-ink-4">0 running · 0 queued</span>
          </span>
          <div class="flex flex-col">
            {#each Array(placeholderRowCount) as _, index (index)}
              <div class="border-b border-line-thin last:border-b-0">
                <div class="flex items-center gap-3 rounded-row px-3 py-2">
                  <span class="h-5 min-w-0 flex-1 rounded-row bg-surface-regular"></span>
                  <span class="h-5 w-16 rounded-row bg-surface-regular"></span>
                  <span class="h-4 w-12 rounded-row bg-surface-regular"></span>
                  <span class="h-4 min-w-0 flex-1 rounded-row bg-surface-regular"></span>
                  <span class="h-7 w-16 rounded-row bg-surface-regular"></span>
                </div>
              </div>
            {/each}
          </div>
        </div>
      </div>
    {:else if $queue.error}
      {@render Refused(($queue.error as Error).message)}
    {:else if quiet}
      <!-- An empty queue is the healthy state, so it takes the ok role and says
           so plainly. It carries no action: the operator did not come here to
           make work, only to see it. The sweeps below stay on screen, which is
           what stops an empty queue reading as a stopped worker. -->
      <EmptyPanel role="ok" level={3} heading="The queue is empty">
        <!-- The heading already says the queue is empty. What is worth adding is
             the one thing that stops an empty queue reading as a dead worker. -->
        <p class="text-meta leading-[1.65] text-ink-2">
          The sweeps below are still on their own clocks.
        </p>
      </EmptyPanel>
    {:else}
      <!-- Only the lanes with something on them. The worker always answers with
           all five, so a queue holding four general jobs and no searches drew
           `Search lane · 0 running · 0 queued` as a heading with nothing under
           it — a label for an absence, on a panel that is already the list of
           what is running. design-plan 3.6 is the rule: a screen says the
           nothing once, and the empty-queue panel above is where it says it. -->
      <div class="flex flex-col gap-[26px]">
        {#each lanes.filter((lane) => lane.jobs.length > 0) as lane (lane.lane)}
          <div>
            <span class="label mb-1 block border-b border-line-thin pb-2">
              {laneNames[lane.lane] ?? lane.label}
              <span class="numeric ml-2 tracking-normal text-ink-4">
                {lane.runningCount} running · {lane.queuedCount} queued
              </span>
            </span>

            <!-- Many queued rows of one kind are the shape a runaway sweep
                 takes, so the backlog and the way out of it sit together
                 rather than making the operator find and select every row by
                 hand. A kind held to one queued row by its own schedule never
                 reaches this: kindBacklogs only counts a kind past one. -->
            {#each kindBacklogs(lane, $queue.data?.truncated ?? false) as backlog (backlog.kind)}
              {@const press = kindCancels[backlog.kind]}
              <div class="mb-1 flex flex-col gap-1.5 rounded-row bg-surface-regular px-3 py-2">
                <div class="flex items-center justify-between gap-3">
                  <span class="text-meta text-ink-3">
                    {kindName(backlog.kind)} — {backlogCount(backlog)} queued
                  </span>
                  <Button
                    variant="outline"
                    size="xs"
                    disabled={press?.pending}
                    onclick={() => confirmCancelKind(backlog)}
                  >
                    {#if press?.pending}
                      <LoaderCircle size={12} class="animate-spin" />
                    {:else}
                      <X size={12} />
                    {/if}
                    Cancel all
                  </Button>
                </div>
                <!-- What the press found. A refusal — a kind that is a
                     recurring sweep's own schedule — leaves this row on
                     screen with nothing cancelled, so it has to say why. -->
                {#if press?.detail}
                  <p class="text-meta text-ink-3">{press.detail}</p>
                {/if}
              </div>
              {#if kindCancelRefusal?.kind === backlog.kind}
                <ErrorNote error={kindCancelRefusal.error} class="mb-2" />
              {/if}
            {/each}

            <div
              class="hidden text-ink-3 md:grid md:grid-cols-[minmax(0,1.4fr)_92px_64px_minmax(0,1fr)_84px] md:gap-x-3 md:border-b md:border-line-thin md:px-3 md:pb-1.5"
            >
              <span class="label">Job</span>
              <span class="label">Status</span>
              <span class="label text-right">Attempt</span>
              <span class="label"></span>
              <span class="label"></span>
            </div>

            {#each lane.jobs as job (job.id)}
              {@const state = stateOf(job, now)}
              <!-- The hairline sits on the whole entry, so a detail line under a
                   row stays inside that row's rule rather than starting a new
                   one. -->
              <div class="border-b border-line-thin last:border-b-0">
                <div
                  class="flex flex-wrap items-center gap-x-3 gap-y-2 rounded-row px-3 py-2 transition hover:bg-surface-thick md:grid md:grid-cols-[minmax(0,1.4fr)_92px_64px_minmax(0,1fr)_84px] md:gap-y-0"
                >
                  <span class="truncate text-body text-ink" title={primaryOf(job)}>
                    {primaryOf(job)}
                  </span>

                  <span><StateTag tone={tones[state.role]}>{state.chip}</StateTag></span>

                  <span class="numeric text-right text-micro text-ink-3">
                    {attemptsOf(job.status, job.attempts, job.maxAttempts)}
                  </span>

                  <span class="numeric truncate text-meta text-ink-3">
                    job {shortId(job.id)} · {state.when}
                  </span>

                  <!-- A running job has an empty action column: it is being
                       run by a worker, and stopping it is a different problem.
                       The column stays empty rather than absent so the rows
                       below it do not reflow. -->
                  <span class="flex justify-end gap-1">
                    {#if job.status === 'queued'}
                      <Button
                        variant="outline"
                        size="xs"
                        disabled={$cancel.isPending && $cancel.variables?.id === job.id}
                        onclick={() => $cancel.mutate(job)}
                      >
                        {#if $cancel.isPending && $cancel.variables?.id === job.id}
                          <LoaderCircle size={12} class="animate-spin" />
                        {:else}
                          <X size={12} />
                        {/if}
                        Cancel
                      </Button>
                    {/if}
                  </span>
                </div>

                {#if jobCancelRefusal?.id === job.id}
                  <ErrorNote error={jobCancelRefusal.error} class="mx-3 mb-2" />
                {/if}

                {#if state.chip === 'Backing off' && job.error}
                  <!-- An error that stopped nothing is not reported in red. This
                       job failed once, has attempts left and is waiting on
                       purpose. -->
                  <div class="mx-3 mb-2.5 flex flex-col gap-1 rounded-row bg-surface-regular px-2.5 py-2 text-meta leading-[1.5] text-ink-3">
                    <!-- The reason a job recorded is written in the same words
                         the server's own refusals are, and the words it recorded
                         are one press away underneath. Bare, because this row is
                         deliberately quiet: the job is waiting on purpose and
                         nothing here has stopped, so the next line counts the
                         tries left rather than asking anybody to do anything. -->
                    <ErrorNote
                      bare
                      error={job.error}
                      fallback="Attempt {job.attempts} did not finish."
                      action={attemptsLeft(job.maxAttempts - job.attempts - 1)}
                    />
                  </div>
                {/if}
              </div>
            {/each}
          </div>
        {/each}
      </div>

      {#if $queue.data?.truncated}
        <p class="mt-3 text-meta text-ink-4">
          Top of the queue — {$queue.data.total.toLocaleString()} jobs are running or waiting in all.
        </p>
      {/if}
    {/if}
  </section>

  <!-- ── Failed for good ──────────────────────────────────────────────── -->
  <section>
    <div class="mb-[18px] flex items-baseline justify-between border-b border-line-thin pb-2">
      <h2 class="font-display text-lead font-bold text-ink">Failed for good</h2>
      <!-- Schall deletes a spent failure after a fortnight. Saying so here is
           what makes this list trustworthy: without it a row that vanishes
           reads as a row that was fixed. -->
      <span class="text-meta text-ink-4">forgotten after 14 days</span>
    </div>

    {#if $failed.isPending}
      <div
        class="flex flex-col"
        style="max-height: calc(100dvh - 16rem); overflow: hidden;"
        aria-hidden="true"
      >
        {#each Array(placeholderRowCount) as _, index (index)}
          <div class="border-b border-line-thin py-2.5 last:border-b-0">
            <div class="flex items-start gap-3">
              <span class="h-5 min-w-0 flex-1 rounded-row bg-surface-regular"></span>
              <span class="h-7 w-16 rounded-row bg-surface-regular"></span>
            </div>
          </div>
        {/each}
      </div>
    {:else if $failed.error}
      {@render Refused(($failed.error as Error).message)}
    {:else if causes.length === 0}
      <p class="text-meta leading-[1.65] text-ink-2">Nothing has spent its attempts.</p>
    {:else}
      <!-- One row per cause, not per job. Seventy-one jobs stopped by one
           outage are one fact, and seventy-one rows saying it seventy-one times
           — each ending in a URL-encoded query string cut off mid-word — is
           this page saying nothing at length. The recorded errors are still
           here, whole, under the disclosure on the cause they belong to. -->
      <div class="flex flex-col">
        {#each causes as cause (cause.cause)}
          {@const press = causePresses[cause.cause]}
          {@const row = causeRow(cause, press)}
          {@const held = jobsFor(cause)}
          <div class="border-b border-line-thin py-2.5 last:border-b-0">
            <div class="flex flex-wrap items-start gap-x-3 gap-y-2">
              <div class="flex min-w-0 flex-1 flex-col gap-1.5">
                <div class="flex flex-wrap items-center gap-x-2 gap-y-1">
                  <StateTag tone={tones[row.role]}>{row.chip}</StateTag>
                  <h3 class="text-body font-medium text-ink">{causeTitle(cause)}</h3>
                  <span class="text-meta text-ink-3">· {cause.jobCount} {cause.jobCount === 1 ? 'job' : 'jobs'}</span>
                </div>
                <p class="text-meta text-ink-3">{stopped(cause)}</p>
                {#if row.detail}
                  <!-- What the press found. Pressing twice, or pressing a cause
                       whose jobs went back on the queue by themselves, is not a
                       failure and is not written in red. -->
                  <p class="text-meta text-ink-3" data-tone="quiet">
                    {row.detail}
                  </p>
                {/if}
              </div>

              <Button
                variant="outline"
                size="xs"
                disabled={row.held}
                onclick={() => $retryCause.mutate(cause)}
              >
                {#if row.spinning}
                  <LoaderCircle size={12} class="animate-spin" />
                {:else if !row.held}
                  <RotateCcw size={12} />
                {/if}
                {row.label}
              </Button>
            </div>

            {#if causeRefusal?.cause === cause.cause}
              <ErrorNote error={causeRefusal.error} class="mx-3 mb-2" />
            {/if}

            <!-- What the named service is, the condition that stopped it, and
                 the recorded errors exactly as the queue wrote them. All of it
                 behind the press: the row above already says what happened, how
                 much it stopped and what to do, and the four sentences about
                 what slskd is are worth having and not worth reading first.
                 Nothing is cut — what is quoted in a bug report has to be the
                 whole string. -->
            <details class="group mt-3 border-t border-line-thin pt-2">
              <!-- This press is what reveals the recorded error, which is what
                   the reader came for, and the words and the chevron are both
                   12px. `tap-tall` gives a thumb the 44px it needs by taking
                   padding inside the summary rather than by an invisible box
                   over it: the panel that opens is a scrolled surface, and a box
                   is clipped by whatever hides its overflow. -->
              <summary
                class="tap-tall flex cursor-pointer list-none items-center gap-1.5 text-meta text-ink-3 transition hover:text-ink-2 [&::-webkit-details-marker]:hidden"
              >
                <ChevronRight size={12} class="transition-transform group-open:rotate-90" />
                What went wrong
              </summary>

              <!-- `reveal` is the disclosure's own arrival: the panel is not
                   drawn at all while the element is shut, so it runs on the
                   press that opens it and never on the page load. -->
              <div class="reveal mt-1 flex flex-col">
                <p class="px-3 pb-2 text-meta leading-[1.65] text-ink-2">{causeDetail(cause)}</p>
                {#each held as failure (failure.id)}
                  {@const jobRow = failureRow(failure, presses[failure.id], now)}
                  <div class="border-b border-line-thin last:border-b-0">
                    <div
                      class="flex flex-wrap items-center gap-x-3 gap-y-2 rounded-row px-3 py-2 transition hover:bg-surface-thick md:grid md:grid-cols-[minmax(0,1.7fr)_124px_76px_96px] md:gap-y-0"
                    >
                      <span class="flex min-w-0 flex-1 flex-col gap-0.5">
                        <!-- The primary truncates on one line like every other
                             row primary: a row that grows to fit its title
                             breaks the grid for every row beside it. -->
                        <span class="truncate text-body text-ink" title={primaryOf(failure)}>
                          {primaryOf(failure)}
                        </span>
                        <span class="numeric truncate text-meta text-ink-3">
                          job {shortId(failure.id)} · {jobRow.when}
                        </span>
                      </span>

                      <span><StateTag tone={tones[jobRow.role]}>{jobRow.chip}</StateTag></span>

                      <span class="numeric text-right text-micro text-ink-3">{jobRow.figure}</span>

                      <span class="flex justify-end gap-1">
                        <Button
                          variant="outline"
                          size="xs"
                          disabled={jobRow.held}
                          onclick={() => $retry.mutate(failure)}
                        >
                          {#if jobRow.spinning}
                            <LoaderCircle size={12} class="animate-spin" />
                          {:else if !jobRow.held}
                            <RotateCcw size={12} />
                          {/if}
                          {jobRow.label}
                        </Button>
                      </span>
                    </div>

                    {#if refusal?.id === failure.id}
                      <ErrorNote error={refusal.error} class="mx-3 mb-2" />
                    {:else}
                      <!-- Not in red. The alarm is the cause above; this is the
                           evidence under it, and it is set as reference text so
                           that the whole of it can be read and copied. -->
                      <div
                        class="mx-3 mb-2.5 rounded-row bg-surface-regular px-2.5 py-2 text-meta leading-[1.5] break-words whitespace-pre-wrap text-ink-3"
                        data-tone="quiet"
                        data-truncated="false"
                      >
                        {#if jobRow.detail}{jobRow.detail}{/if}
                        <span class="text-ink-2">It failed with:</span>
                        {failure.error}
                      </div>
                    {/if}
                  </div>
                {:else}
                  <p class="px-3 py-2 text-meta text-ink-4">
                    The jobs this cause stopped are further back than the list reaches. Retrying the
                    cause still retries every one of them.
                  </p>
                {/each}
              </div>
            </details>
          </div>
        {/each}
      </div>

      {#if $failed.data?.truncated}
        <p class="mt-3 text-meta text-ink-4">
          The most recent of {$failed.data.total.toLocaleString()} jobs that have spent their attempts.
          Retrying a cause still retries every one of them.
        </p>
      {/if}
    {/if}
  </section>

  <!-- ── The sweep pulse ──────────────────────────────────────────────── -->
  <section>
    <!-- `The sweep pulse` was a title in the project's own voice: it reads
         well in a document about how Schall works, and on the screen it names
         nothing a reader can point at. The section is a list of the recurring
         sweeps, so it is called that. -->
    <h2 class="font-display text-lead font-bold text-ink mb-[18px] block border-b border-line-thin pb-2">Sweeps</h2>

    {#if $schedule.isPending}
      <div
        class="flex flex-col"
        style="max-height: calc(100dvh - 16rem); overflow: hidden;"
        aria-hidden="true"
      >
        <div
          class="hidden border-b border-line-thin px-3 pb-2 md:grid md:grid-cols-[minmax(0,1fr)_128px_188px] md:items-center md:gap-x-3"
        >
          <span class="label">Sweep</span>
          <span class="label text-right">Last run</span>
          <span class="label text-right">Next</span>
        </div>
        {#each Array(placeholderRowCount) as _, index (index)}
          <div
            class="flex flex-wrap items-center gap-x-3 gap-y-2 rounded-row border-b border-line-thin px-3 py-2 md:grid md:grid-cols-[minmax(0,1fr)_128px_188px] md:gap-y-0"
          >
            <span class="h-9 min-w-0 flex-1 rounded-row bg-surface-regular"></span>
            <span class="h-7 w-24 rounded-row bg-surface-regular"></span>
            <span class="h-7 w-40 rounded-row bg-surface-regular"></span>
          </div>
        {/each}
      </div>
    {:else if $schedule.error}
      {@render Refused(($schedule.error as Error).message)}
    {:else}
      <div class="flex flex-col">
        <div
          class="hidden border-b border-line-thin px-3 pb-2 md:grid md:grid-cols-[minmax(0,1fr)_128px_188px] md:items-center md:gap-x-3"
        >
          <span class="label">Sweep</span>
          <span class="label text-right">Last run</span>
          <span class="label text-right">Next</span>
        </div>

        {#each $schedule.data?.items ?? [] as item (item.kind)}
          {@const condition = waitsFor(item)}
          <div
            class="flex flex-wrap items-center gap-x-3 gap-y-2 rounded-row border-b border-line-thin px-3 py-2 transition last:border-b-0 hover:bg-surface-thick md:grid md:grid-cols-[minmax(0,1fr)_128px_188px] md:gap-y-0"
          >
            <span class="flex min-w-0 flex-1 flex-col gap-0.5">
              <span class="truncate text-body text-ink">{sweepLabel(item)}</span>
              <span class="truncate text-meta text-ink-3">
                {sweepCopy[item.kind]?.does ?? ''}
              </span>
            </span>

            <!-- Absolute time in figures with the relative age underneath,
                 because "7m ago" answers the question and the clock time
                 settles the argument. -->
            <span class="min-w-0 text-right">
              {#if item.running}
                <span class="numeric text-micro text-ink-2">{clockTime(item.runningSince)}</span>
                <span class="mt-0.5 block text-meta text-ink-4">running now</span>
              {:else if item.lastRunAt}
                <span class="numeric text-micro text-ink-2">{clockTime(item.lastRunAt)}</span>
                <span class="mt-0.5 block text-meta text-ink-4">
                  {relativeTime(item.lastRunAt, now)}
                </span>
              {:else}
                <span class="text-meta leading-[1.4] text-ink-3">
                  <span class="text-ink-4">Has not</span> run yet
                </span>
              {/if}
            </span>

            <span class="min-w-0 text-right">
              {#if item.nextRunAt}
                <span class="numeric text-micro text-ink-2">{clockTime(item.nextRunAt)}</span>
                <span class="mt-0.5 block text-meta text-ink-4">
                  {relativeTime(item.nextRunAt, now)}{nextClause(item)}
                </span>
              {:else}
                <!-- Prose in a figure column is deliberate: the shape of the
                     cell tells you which kind of answer you are reading before
                     you have read it. A sweep with no next time is idle, never
                     fail — idle is the colour of a thing that is fine and not
                     doing anything. -->
                <span
                  class="whitespace-nowrap text-meta leading-[1.4] text-ink-3"
                  data-role="idle"
                  title={item.detail}
                >
                  {#if condition.lead}<span class="text-ink-4">{condition.lead}</span>{/if}
                  {condition.rest}
                </span>
              {/if}
            </span>
          </div>
        {/each}
      </div>
    {/if}
  </section>
</div>

<!-- All three sections read the same job queue, so all three fail the same way
     and say so in the same words. The server's sentence follows the one naming
     what could not be read: a Go error alone says what broke and never what it
     was doing. -->
{#snippet Refused(message: string)}
  <!-- Somewhere to go, and not a second attempt at the same request. Every
       section here reads the one job queue, so a queue that cannot be read is
       either the server or the database behind it — and the status card on
       Sources is the closest reading of whether the server is up. TanStack
       has already asked again three times by the time this is drawn. -->
  <StatusBadge wrap>
    <span class="text-meta text-ink">The job queue could not be read. {message}</span>
    <Button href="/settings/sources" variant="outline" size="sm" class="ml-auto shrink-0">
      Check sources status
    </Button>
  </StatusBadge>
{/snippet}

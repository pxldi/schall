import type {
  AcquiredCopy,
  AcquisitionCandidate,
  CopyGroup,
  DownloadRequest,
  IdentityCandidate,
  LibraryFile,
  ReviewItem
} from '$lib/api';
import type { IconName } from '$lib/icons';

/** The three questions Review holds.
 *
 * 'downloaded' asks whether one of a want's fetched files is the wanted
 * recording. It also carries the want whose accepted copy is already a
 * library file under a different recording — a person has to say whether that
 * file is this one — because that card looks and answers the same way. Only a
 * want held up by a disagreement on artist credit alone renders as an
 * ordinary downloaded card rather than the library one; see
 * `isCreditOnlyStopped`.
 *
 * 'version' asks which MusicBrainz recording a playlist entry means.
 *
 * 'folder' asks what a downloaded album folder's odd files belong to.
 *
 * Identity, match and duplicate questions used to be asked here too. They are
 * a file already on disk, not a want, and they moved to Library: identity and
 * match behind a filter there, duplicates in the held-twice view. */
export type QuestionKind = 'downloaded' | 'version' | 'folder';

export interface Question {
  /** Stable across refetches, so stepping through the queue does not lose its
   * place when one of the two sources reloads underneath it. */
  id: string;
  kind: QuestionKind;
  /** The thing being decided about. */
  title: string;
  /** Who and where, in one line. */
  detail: string;
  want?: ReviewItem;
  download?: DownloadRequest;
}

export function isCreditOnlyRefusal(copy: AcquiredCopy): boolean {
  return copy.verdict === 'discarded_tags' &&
    copy.evidence?.differs.length === 1 &&
    copy.evidence.differs[0] === 'artist';
}

/** A stopped want held up by nothing more than a disagreement on artist
 * credit. It renders as an ordinary grid of copies rather than as the
 * library card, because there is still a copy to choose between rather than
 * one file already filed. */
export function isCreditOnlyStopped(want: ReviewItem | undefined): boolean {
  const copies = want?.copies ?? [];
  return want?.kind === 'stopped' && copies.length > 0 && copies.every(isCreditOnlyRefusal);
}

/** What a want is still waiting for, when it is waiting rather than asking.
 * The queue holds both, and a reader working through it should be able to see
 * which rows are theirs to answer. */
export const WAITING_ON: Record<'musicbrainz' | 'library_identity', string> = {
  musicbrainz: 'Waiting on MusicBrainz',
  library_identity: 'Waiting on your library'
};

export function waitingLabel(want: ReviewItem | undefined): string {
  const waiting = want?.waitingFor;
  return waiting ? WAITING_ON[waiting] : '';
}

/** What is being decided, as a noun phrase rather than a question. Used by
 * Overview's waiting-on-you list, which reads it beside the row it names. */
export const ASKS: Record<QuestionKind, string> = {
  downloaded: 'Which copy',
  version: 'Which version you meant',
  folder: 'What these files are'
};

const WANT_KINDS: Record<ReviewItem['kind'], QuestionKind> = {
  copies: 'downloaded',
  resolution: 'version',
  stopped: 'downloaded'
};

/** A row's title and detail, for a file that is not a want or a download —
 * used by Overview to describe the identity, match and duplicate questions
 * that live in Library now. */
export function fileName(file: LibraryFile) {
  return file.titleTag || file.path.split('/').pop() || file.path;
}

export function fileDetail(file: LibraryFile) {
  return [file.artistTag, file.albumTag].filter(Boolean).join(' · ') || file.path;
}

/** Everything Review holds, as one list.
 *
 * The two sources keep their own order and follow one another rather than
 * being interleaved: wants oldest first, folders in the order the downloads
 * list already reads them. */
export function questionsFrom(sources: {
  wants: ReviewItem[];
  imports: DownloadRequest[];
}): Question[] {
  const questions: Question[] = [];

  for (const want of sources.wants) {
    questions.push({
      id: `want:${want.target.id}`,
      kind: WANT_KINDS[want.kind] ?? 'downloaded',
      title: want.target.title || 'Untitled',
      detail: [want.target.artist, want.target.album].filter(Boolean).join(' · '),
      want
    });
  }

  // Only the imports that recorded what they found. An import that gave up
  // before it could compare anything — the folder was gone, the database was
  // unreachable, the edition could not be fetched — is a failure rather than a
  // question: there is nothing in it for a reader to decide, and the only thing
  // they could do is ask the machine to try what it already tried. That belongs
  // beside the other failures on the download, not in a queue of decisions.
  for (const download of sources.imports) {
    if (!download.importEvidence) continue;
    questions.push({
      id: `import:${download.id}`,
      kind: 'folder',
      title: download.entryTitle || download.directory.split('/').pop() || download.directory,
      detail: [download.entryArtist, download.username].filter(Boolean).join(' · '),
      download
    });
  }

  return questions;
}

// ---- why a question is here ----------------------------------------------

/** Why a question sits in Review, as one name a reader can filter by. It is
 * read from the evidence the question already carries and decides nothing:
 * every question is answered the same way whatever its reason. The order
 * follows `review-reasons/review-now.sql`, which sorted the live queue the
 * same way, so a reason here names the same pile there. */
export type ReasonKey =
  | 'nocheck'
  | 'acoustid'
  | 'same'
  | 'sample'
  | 'live'
  | 'credit'
  | 'library'
  | 'version'
  | 'folder'
  | 'waiting';

/** The rail lists them in this order, the piles a person answers first at
 * the top and the ones still waiting on something else at the foot. */
export const REASONS: Record<ReasonKey, { label: string; icon: IconName; tip: string }> = {
  nocheck: {
    label: 'No audio check',
    icon: 'listen',
    tip: 'Nothing could check the audio: no sample and no AcoustID answer. Listen and decide.'
  },
  acoustid: {
    label: 'AcoustID unsure',
    icon: 'fingerprint',
    tip: 'AcoustID named another recording, or its answer was too weak to count.'
  },
  same: {
    label: 'Audio also elsewhere',
    icon: 'link',
    tip: 'AcoustID says this audio also belongs to another recording.'
  },
  sample: {
    label: 'Sample unsure',
    icon: 'search-check',
    tip: 'Against the shop preview the copy sits between the same and different.'
  },
  live: { label: 'Sounds live', icon: 'alert', tip: 'The copy looks like a live recording.' },
  credit: {
    label: 'Credit differs',
    icon: 'artist',
    tip: 'Only the artist credit differs from what you asked for.'
  },
  library: {
    label: 'Filed as other song',
    icon: 'library',
    tip: 'The file is already in your library, filed as another recording.'
  },
  version: {
    label: 'Which version',
    icon: 'track',
    tip: 'Several recordings fit this entry. Choose the one you meant.'
  },
  folder: { label: 'Folder', icon: 'folder-add', tip: 'Some files in a downloaded folder need a track.' },
  waiting: {
    label: 'Waiting',
    icon: 'waiting',
    tip: 'Still waiting on something else. It is judged again by itself when that arrives.'
  }
};

const LIVE = /\blive\b/i;

export function reasonOf(question: Question): ReasonKey {
  if (question.kind === 'folder') return 'folder';
  if (question.kind === 'version') return 'version';
  const want = question.want;
  if (!want) return 'nocheck';
  if (want.waitingFor) return 'waiting';
  if (want.kind === 'stopped') return isCreditOnlyStopped(want) ? 'credit' : 'library';
  const held = want.copies.filter((copy) => copy.verdict === 'held');
  const copies = held.length > 0 ? held : want.copies;
  if (copies.some((copy) => (copy.evidence?.acoustic?.clusters?.[0]?.recordingIds?.length ?? 0) > 1)) {
    return 'same';
  }
  if (copies.some((copy) => copy.evidence?.anchor?.measured && copy.evidence.anchor.agrees === undefined)) {
    return 'sample';
  }
  if (!LIVE.test(want.target.title) && copies.some((copy) => LIVE.test(copy.path))) return 'live';
  const unheard = (copy: AcquiredCopy) =>
    (copy.evidence?.acoustic?.recordingIds?.length ?? 0) === 0 && !copy.evidence?.anchor?.measured;
  if (copies.every(unheard)) return 'nocheck';
  return 'acoustid';
}

/** One column of Review's comparison: a copy, or the want's file already in
 * the library. */
export type CopyColumn = {
  heading: string;
  copy: AcquiredCopy;
  /** Separate rips that sound like this one, the copy shown included. */
  rips: number;
  /** The want's file already in the library, compared by its own credit. */
  filed?: boolean;
  /** Something in the evidence speaks against this copy, so its Accept is
   * not drawn as the main action. */
  doubted: boolean;
};

// ---- deciding an album at once, and listening through -------------------

/** The wants of one album, as the rail groups them. */
export interface AlbumGroup {
  key: string;
  album: string;
  artist: string;
  questions: Question[];
}

/** Artist and album, from the want's own target. A folder question or a want
 * that names no album has no key and stays loose. */
export function albumKey(question: Question): string {
  const target = question.want?.target;
  if (!target?.album) return '';
  return `${target.artist}\u0000${target.album}`;
}

/** The questions as album groups in the order each album first appears,
 * followed by the ones no album holds. */
export function groupByAlbum(questions: Question[]): { groups: AlbumGroup[]; loose: Question[] } {
  const groups = new Map<string, AlbumGroup>();
  const loose: Question[] = [];
  for (const question of questions) {
    const key = albumKey(question);
    if (!key) {
      loose.push(question);
      continue;
    }
    let group = groups.get(key);
    if (!group) {
      group = {
        key,
        album: question.want!.target.album,
        artist: question.want!.target.artist,
        questions: []
      };
      groups.set(key, group);
    }
    group.questions.push(question);
  }
  return { groups: [...groups.values()], loose };
}

/** Whether a question can be listened through: a want with held copies to
 * accept or refuse. A stopped want whose file is already filed asks something
 * else and is answered on its own card. */
export function listenable(question: Question): boolean {
  const want = question.want;
  if (question.kind !== 'downloaded' || !want || want.copies.length === 0) return false;
  return want.kind === 'copies' || isCreditOnlyStopped(want);
}

/** The copy an album batch would accept for this question: the one the single
 * view preselects. Only a 'copies' want with its evidence in has one; stopped,
 * waiting and resolution questions are decided one by one. */
export function batchChoice(question: Question): AcquiredCopy | undefined {
  const want = question.want;
  if (question.kind !== 'downloaded' || want?.kind !== 'copies' || want.waitingFor) return undefined;
  return groupCopies(want.copies, want.copyGroups)[0]?.best;
}

/** What an album batch checks a copy for before the reader looks: an
 * identifier the file carries that agreed, the want's sample that the audio
 * matched, or a file the library already proved. Anything that disagreed, and
 * any copy with none of the three, starts unchecked and is answered '' here.
 * A name, never a score: the reader still decides, this only says which rows
 * they can accept without opening them. */
export function batchProof(copy: AcquiredCopy): string {
  const evidence = copy.evidence;
  // contradicted() includes a sample that disagreed, which voids the copy
  // however well its tags agree.
  if (!evidence || contradicted(copy)) return '';
  if (verdict(copy, 'MusicBrainz recording ID') === 'ok') return 'Recording ID';
  if (verdict(copy, 'ISRC') === 'ok') return 'ISRC';
  // A plain upload found by name can be another version of the song.
  if (evidence.anchor?.agrees === true && evidence.anchor.source !== 'youtube') return 'Sample';
  if (evidence.witness?.agrees === true) return 'Library file';
  return '';
}

/** Whether something in a copy's evidence speaks against it: a field that
 * differs, or audio that named another recording. Such a copy can still be
 * accepted, but it is not offered as the main action. */
export function contradicted(copy: AcquiredCopy | undefined): boolean {
  const evidence = copy?.evidence;
  if (!evidence) return false;
  return (
    (evidence.differs ?? []).length > 0 ||
    evidence.acoustic?.agrees === false ||
    evidence.anchor?.agrees === false
  );
}

/** Whether two credits or titles name different things. An empty side is
 * silence, so it differs from nothing. */
export function namesDiffer(a?: string, b?: string): boolean {
  const left = a?.trim().toLocaleLowerCase() ?? '';
  const right = b?.trim().toLocaleLowerCase() ?? '';
  return left !== '' && right !== '' && left !== right;
}

/** Where a copy starts when nobody chose a spot: a third of the way in. The
 * opening is often a count-in or silence that sounds alike on every copy. */
export function listenFrom(duration: number): number {
  return Number.isFinite(duration) && duration > 0 ? duration / 3 : 0;
}

// ---- reading the evidence behind a fetched copy --------------------------

/** The evidence tiers, strongest first. A tier is named rather than scored:
 * what it says is which kind of agreement was found, and no arithmetic over
 * several weak agreements ever adds up to a strong one. */
export const TIERS = [
  'Recording ID',
  'ISRC',
  'Title + duration',
  'Tags only',
  'Audio unidentified'
] as const;

function present(value: string | undefined | null) {
  return typeof value === 'string' && value.trim() !== '';
}

/** What the grader concluded about one field, read back rather than worked out
 * again here. The labels are the ones `describeVerdicts` writes, and a field in
 * neither list was never decided — an absent value is silence, so nothing here
 * may turn it into either an agreement or a disagreement.
 *
 * Some labels carry a parenthetical ("duration (3s out)"), so the match is on
 * the leading word rather than the whole string. */
export function verdict(copy: AcquiredCopy, label: string): 'ok' | 'bad' | 'none' {
  const named = (list: string[] | undefined) =>
    (list ?? []).some((entry) => entry === label || entry.startsWith(label + ' ('));
  if (named(copy.evidence?.agrees)) return 'ok';
  if (named(copy.evidence?.differs)) return 'bad';
  return 'none';
}

/** Which kind of agreement was found, from the grader's own verdicts. A copy
 * proven by its ISRC and merely never listened to is held exactly like one
 * nothing could decide (ADR 0002), so without this it would read as the weakest
 * evidence there is while being the strongest a tag can carry. */
export function tierOf(copy: AcquiredCopy) {
  if (verdict(copy, 'MusicBrainz recording ID') === 'ok') return TIERS[0];
  if (verdict(copy, 'ISRC') === 'ok') return TIERS[1];
  if (verdict(copy, 'title') === 'ok' && verdict(copy, 'duration') === 'ok') return TIERS[2];
  const observed = copy.evidence?.observed ?? {};
  if (present(observed.artist) || present(observed.title)) return TIERS[3];
  return TIERS[4];
}

/** Best first: by the strength of what agreed, then by how good the copy is.
 * The second term never promotes across tiers — a pristine file that agrees on
 * nothing stays below a modest one that agrees on an identifier. */
export function rankCopies(all: AcquiredCopy[]) {
  return [...all].sort((left, right) => {
    const byTier = TIERS.indexOf(tierOf(left)) - TIERS.indexOf(tierOf(right));
    if (byTier !== 0) return byTier;
    return (right.evidence?.bitRate ?? 0) - (left.evidence?.bitRate ?? 0);
  });
}

/** One card of the copies grid: the copy shown, and the copies that sound the
 * same as it. */
export interface CopyGroupView {
  /** The copy on the card, and the one an answer of Accept is about: the best
   * of the group by the ranking the queue has always used. */
  best: AcquiredCopy;
  /** The rest of the group, best first, played and accepted with it. */
  others: AcquiredCopy[];
  /** How many separate rips the group holds, as the server counted them. Copies
   * of identical size are one upload that spread, so five files can be one rip
   * and are worth reading as one. */
  rips: number;
}

/** The copies of one want, as one card per distinct audio.
 *
 * One want commonly holds many copies nothing could decide about — eleven, in
 * the queue this was built against, which were five copies of one master and six
 * of another, with nothing in the file names saying so. The server measures
 * which of them sound alike; this turns that into what the grid draws.
 *
 * Grouping changes what is read and never what is answered. Accept is about the
 * copy on the card and leaves the rest of its group exactly where they are; None
 * of these refuses every copy of the want, as it always has.
 *
 * A queue that sent no groups — an older server, or a want whose copies nothing
 * could measure — gives one card per copy. */
export function groupCopies(copies: AcquiredCopy[], groups?: CopyGroup[]): CopyGroupView[] {
  const byId = new Map(copies.map((copy) => [copy.id, copy]));
  const views: CopyGroupView[] = [];

  for (const group of groups ?? []) {
    const members = rankCopies(
      group.copyIds.map((id) => byId.get(id)).filter((copy): copy is AcquiredCopy => !!copy)
    );
    if (members.length === 0) continue;
    for (const member of members) byId.delete(member.id);
    views.push({ best: members[0], others: members.slice(1), rips: group.rips || members.length });
  }

  // Anything the groups did not name. A copy that fell out of the answer would
  // be a file a person never sees, which is worse than a card too many.
  for (const copy of copies) {
    if (!byId.has(copy.id)) continue;
    byId.delete(copy.id);
    views.push({ best: copy, others: [], rips: 1 });
  }

  const order = rankCopies(views.map((view) => view.best));
  return views
    .slice()
    .sort((left, right) => order.indexOf(left.best) - order.indexOf(right.best));
}

// ---- the evidence, as one table (Version, and Library's identity/match) --

/** What one verdict word means, in one sentence, on demand. Kept for
 * ReviewEvidence, which Library's identity and match panels still use — the
 * only two cell values `candidateRow` can produce that carry a meaning at
 * all. */
export const MEANINGS: Record<string, string> = {
  'listened to': 'Audio compared with this song.',
  'not listened': 'Audio not compared.'
};

/** One column of the evidence table. Every candidate answers every column, so a
 * reader compares two of them by reading straight down rather than by holding
 * one card in their head while they read the next. */
export type EvidenceColumn = {
  key: string;
  label: string;
  /** Figures, which are set in the mono face and lined up on the units. */
  numeric?: boolean;
  /** Never cut this one to fit. A value whose end is the only thing that
   * differs from the row above says nothing at all once it is cut, which is
   * what a shortened identifier does. */
  whole?: boolean;
  /** Let this one run onto a second line instead of being cut. */
  wrap?: boolean;
};

/** What one candidate says in one column. `differs` is the grader's own verdict
 * about that field and is what marks the token; `note` is the measurement the
 * grader put in brackets, which is the only number a reader gets. */
export type EvidenceCell = {
  value: string;
  differs: boolean;
  note: string;
  /** The one sentence this value means, where it is a verdict word rather than
   * something the file said about itself. */
  meaning: string;
};

/** One candidate, as a row. The ledger travels with it because the agreements
 * and the silences go behind the disclosure rather than into a column. */
export type EvidenceRow = {
  id: string;
  cells: Record<string, EvidenceCell>;
  ledger: Ledger;
};

/** The columns a candidate recording answers. Fewer than a copy's, because a
 * candidate is a row in MusicBrainz rather than a file: it has no peer, no
 * format and no size.
 *
 * The identifier is a column of its own and is never cut short. MusicBrainz
 * keeps one performance as several rows, and two of them can agree on every
 * word a person reads — then the identifier is the whole of the difference. */
export const CANDIDATE_COLUMNS: EvidenceColumn[] = [
  { key: 'title', label: 'Recording' },
  { key: 'artist', label: 'Artist' },
  { key: 'release', label: 'Release' },
  { key: 'length', label: 'Length', numeric: true },
  { key: 'isrc', label: 'ISRC', numeric: true },
  { key: 'audio', label: 'Audio' },
  { key: 'recordingId', label: 'Recording ID', whole: true }
];

/** Which column a grader's field name belongs to.
 *
 * The two graders name their fields slightly differently — one says 'album' and
 * the other 'release' — and both may hang a measurement off the end in
 * brackets. Both readings point at one column here, so a mark lands on the
 * token a reader can see whichever grader spoke. */
const FIELD_COLUMNS: Record<string, string> = {
  isrc: 'isrc',
  title: 'title',
  artist: 'artist',
  album: 'album',
  release: 'release',
  duration: 'length',
  audio: 'audio',
  acoustid: 'audio'
};

/** The column a verdict is about, and the measurement it carried. */
function graded(entry: string) {
  const opened = entry.indexOf(' (');
  const label = opened < 0 ? entry : entry.slice(0, opened);
  const note = opened < 0 || !entry.endsWith(')') ? '' : entry.slice(opened + 2, -1);
  return { key: FIELD_COLUMNS[label.toLocaleLowerCase()] ?? '', note };
}

/** Which columns this candidate is in the queue for, and what each of them
 * measured. Only disagreements are marked: an agreement is the background and
 * a silence is neither answer, so neither may mark a cell. */
function marks(differs: string[]) {
  const found = new Map<string, string>();
  for (const entry of differs) {
    const { key, note } = graded(entry);
    if (key) found.set(key, note);
  }
  return found;
}

function cells(
  values: Record<string, string>,
  differs: string[]
): Record<string, EvidenceCell> {
  const marked = marks(differs);
  const row: Record<string, EvidenceCell> = {};
  for (const [key, value] of Object.entries(values)) {
    row[key] = {
      value: value === '' ? '—' : value,
      differs: marked.has(key),
      note: key === 'length' && value.includes(' vs ') ? '' : (marked.get(key) ?? ''),
      meaning: MEANINGS[value] ?? ''
    };
  }
  return row;
}

/** Two durations that disagree, as one cell: "3:12 vs 4:05".
 *
 * Only where the grader itself called it a disagreement. Two lengths side by
 * side where nothing decided between them would be this screen inventing a
 * verdict, which is the one thing it may never do. */
function lengths(
  mine: number | null | undefined,
  theirs: number | null | undefined,
  differs: boolean
) {
  if (!mine) return '';
  const value = clock(mine / 1000);
  if (!differs || !theirs) return value;
  return `${value} vs ${clock(theirs / 1000)}`;
}

/** One candidate recording as a row, from the grader's two lists of field
 * names. A candidate carries no values of its own for the fields it was judged
 * on beyond the four MusicBrainz holds, so a column nothing was said about
 * reads as a dash rather than as agreement. */
export function candidateRow(
  candidate: IdentityCandidate | AcquisitionCandidate,
  /** How long the thing being identified runs, where anything knows. */
  expectedMs?: number | null
): EvidenceRow {
  const answered = new Set(
    [...candidate.agrees, ...candidate.differs].map((entry) => graded(entry).key)
  );
  const disagrees = candidate.differs.some((entry) => graded(entry).key === 'length');
  return {
    id: candidate.recordingId,
    cells: cells(
      {
        title: candidate.trackTitle,
        artist: candidate.artistName,
        release: candidate.releaseTitle ?? '',
        length: lengths(candidate.durationMs, expectedMs, disagrees),
        isrc: candidate.isrc ?? '',
        audio: answered.has('audio') ? 'listened to' : 'not listened',
        recordingId: candidate.recordingId
      },
      candidate.differs
    ),
    ledger: ledgerOfVerdicts(candidate.agrees, candidate.differs)
  };
}

export function clock(seconds: number) {
  if (!Number.isFinite(seconds) || seconds < 0) return '0:00';
  const whole = Math.floor(seconds);
  return `${Math.floor(whole / 60)}:${String(whole % 60).padStart(2, '0')}`;
}

/** One line of a comparison. `value` is what this candidate says and `was` is
 * what the target said, so a row can always be read as a sentence without the
 * reader holding either side in their head. */
export type LedgerRow = {
  label: string;
  value: string;
  was: string;
};

/** A candidate's evidence, sorted into the three answers the grader can give.
 *
 * They are kept apart because they are read at different weights and for
 * different reasons. `differs` is why the item is in the queue and is the thing
 * a person came to see. `undecided` is silence — a field nothing could conclude
 * about — and it must never be dressed as either answer, but it is not an
 * agreement either, so it is not filed with them. `agrees` is the background
 * against which the other two mean anything, and it is the part that can be
 * said in one line. */
export type Ledger = {
  differs: LedgerRow[];
  undecided: LedgerRow[];
  agrees: string[];
};

/** The ledger for a candidate recording, whose evidence arrives as the grader's
 * own two lists of field names rather than as values.
 *
 * Some names carry a parenthetical — `describeVerdicts` writes
 * "duration (3 seconds out)" — and that parenthetical is the only measurement
 * the reader gets, so it is lifted out of the name and shown as the value
 * rather than left at the end of a label nobody reads to the end of. */
export function ledgerOfVerdicts(agrees: string[], differs: string[]): Ledger {
  const split = (entry: string): LedgerRow => {
    const opened = entry.indexOf(' (');
    if (opened < 0 || !entry.endsWith(')')) return { label: entry, value: '', was: '' };
    return { label: entry.slice(0, opened), value: entry.slice(opened + 2, -1), was: '' };
  };
  return { differs: differs.map(split), undecided: [], agrees: [...agrees] };
}

import { useEffect, useMemo, useState } from 'react';
import { Alert, Image, Pressable, ScrollView, StyleSheet, View } from 'react-native';
import { useLocalSearchParams, useRouter } from 'expo-router';
import { useMutation, useQueryClient } from '@tanstack/react-query';
import { useAudioPlayer, useAudioPlayerStatus } from 'expo-audio';
import { authHeaders, url } from '@/api/client';
import { media } from '@/api/schall';
import type { AcquiredCopy, AcquisitionCandidate, DownloadRequest, ImportFileEvidence, ImportTags, ReviewItem } from '@/api/types';
import { useSchall } from '@/auth/session';
import { useReviewFolders, useReviewQueue } from '@/queries';
import { downloadTitle, groupCopies, importCatalogue, orderFiles, questions, suggestedTrack, trackLabel, type CopyGroupView } from '@/review';
import { theme, space } from '@/design/theme';
import { Actions, Button, Loading, Notice, T, Tag, errorText, formatBytes, formatDuration } from '@/ui/primitives';

const volumes = [0.25, 0.5, 0.75, 1];

/** One question, its evidence, and the same answers the web offers in the same
 * words. Answering goes back to the list; the list refetches, so the next
 * question is whatever the server says is next. */
export default function ReviewQuestion() {
  const { id } = useLocalSearchParams<{ id: string }>();
  const router = useRouter();
  const client = useQueryClient();
  const { api, session } = useSchall();
  const queue = useReviewQueue();
  const folders = useReviewFolders();
  const question = questions(queue.data?.items, folders.data?.items).find((candidate) => candidate.id === id);

  const [error, setError] = useState('');
  const decide = useMutation({
    mutationFn: (run: () => Promise<unknown>) => run(),
    onSuccess: async () => {
      await client.invalidateQueries({ queryKey: ['review-queue'] });
      await client.invalidateQueries({ queryKey: ['downloads'] });
      router.back();
    },
    onError: (thrown) => setError(errorText(thrown))
  });

  if (!question) {
    if (queue.isPending || folders.isPending) return <Loading />;
    return <Notice>This question has been answered.</Notice>;
  }

  const cover = (albumId?: string) =>
    albumId ? (
      <Image source={{ uri: url(session, media.cover(albumId)), headers: authHeaders(session) }} style={styles.cover} />
    ) : (
      <View style={[styles.cover, { backgroundColor: theme.surface }]} />
    );

  if (question.kind === 'folder') {
    return <FolderQuestion download={question.download} cover={cover} error={error} decide={decide.mutate} busy={decide.isPending} />;
  }

  return <WantQuestion want={question.want} kind={question.kind} cover={cover} error={error} decide={decide.mutate} busy={decide.isPending} />;
}

function WantQuestion({
  want,
  kind,
  cover,
  error,
  decide,
  busy
}: {
  want: ReviewItem;
  kind: 'downloaded' | 'version';
  cover: (albumId?: string) => React.ReactNode;
  error: string;
  decide: (run: () => Promise<unknown>) => void;
  busy: boolean;
}) {
  const { api } = useSchall();
  const target = want.target;
  const filed = want.kind === 'stopped' && !!want.fileRecordingId;
  const groups = useMemo(() => groupCopies(want.copies.filter((copy) => copy.verdict === 'held' || want.kind === 'stopped'), want.copyGroups), [want]);
  const [chosen, setChosen] = useState<string | null>(null);

  function confirmRemove() {
    Alert.alert('Remove from Wishlist?', `${target.artist} – ${target.title} stops being looked for.`, [
      { text: 'Keep', style: 'cancel' },
      { text: 'Remove', style: 'destructive', onPress: () => decide(() => api.stopLooking(target.id)) }
    ]);
  }

  return (
    <ScrollView contentContainerStyle={styles.content}>
      <View style={styles.head}>
        {cover(target.originAlbumId)}
        <View style={{ flex: 1, gap: 2 }}>
          <Tag tone="decide">{kind === 'version' ? 'Version' : 'Downloaded'}</Tag>
          <T kind="lead">{target.title}</T>
          <T kind="meta" tone="ink2">{target.artist}</T>
          <T kind="meta" tone="ink3">{[target.album, formatDuration(target.durationMs), target.isrc].filter(Boolean).join(' · ')}</T>
        </View>
      </View>

      {kind === 'downloaded' && !filed ? (
        <>
          <Player copy={groups.find((group) => group.best.id === chosen)?.best ?? null} />
          {groups.length === 0 ? <Notice>No copy is left to hear.</Notice> : null}
          {groups.map((group) => (
            <CopyCard key={group.best.id} group={group} chosen={chosen === group.best.id} onPress={() => setChosen(group.best.id)} />
          ))}
        </>
      ) : null}

      {kind === 'downloaded' && filed ? (
        <View style={styles.card}>
          <T kind="micro" tone="ink3">Already a file</T>
          <T kind="body">{want.fileTitle}</T>
          <T kind="meta" tone="ink2">{[want.fileArtist, want.fileReleaseTitle].filter(Boolean).join(' · ')}</T>
          <T kind="meta" tone="ink3">The library calls this file another recording. Accept says it is this one.</T>
        </View>
      ) : null}

      {kind === 'version'
        ? want.candidates.map((candidate) => (
            <CandidateCard key={candidate.recordingId} candidate={candidate} chosen={chosen === candidate.recordingId} onPress={() => setChosen(candidate.recordingId)} />
          ))
        : null}

      {want.ruledOut ? <T kind="meta" tone="ink3">{want.ruledOut} ruled out already.</T> : null}
      {error ? <T kind="meta" tone="fail">{error}</T> : null}

      <Actions>
        {kind === 'downloaded' && !filed ? (
          <Button variant="solid" busy={busy} disabled={!chosen} onPress={() => chosen && decide(() => api.acceptCopy(chosen))}>Accept</Button>
        ) : null}
        {kind === 'downloaded' && filed ? (
          <Button variant="solid" busy={busy} onPress={() => decide(() => api.acceptFiledCopy(target.id))}>Accept</Button>
        ) : null}
        {kind === 'version' ? (
          <Button variant="solid" busy={busy} disabled={!chosen} onPress={() => chosen && decide(() => api.chooseRecording(target.id, chosen))}>Use</Button>
        ) : null}
        {kind === 'downloaded' && !filed ? (
          <Button busy={busy} onPress={() => decide(() => api.noneOfThese(target.id))}>None of these</Button>
        ) : null}
        {kind === 'downloaded' ? (
          <Button busy={busy} onPress={() => decide(() => api.wrongSong(target.id))}>Wrong song</Button>
        ) : null}
        <Button tone="fail" busy={busy} onPress={confirmRemove}>Remove from Wishlist</Button>
      </Actions>
    </ScrollView>
  );
}

/** An album folder whose files did not all match. Each file that stopped the
 * import can be named as one track of the release; that settles the file
 * without leaving the folder, because there is usually another file left, so
 * it refetches instead of going back. Check again runs the import with the
 * decisions applied. */
function FolderQuestion({
  download,
  cover,
  error,
  decide,
  busy
}: {
  download: DownloadRequest;
  cover: (albumId?: string) => React.ReactNode;
  error: string;
  decide: (run: () => Promise<unknown>) => void;
  busy: boolean;
}) {
  const { api } = useSchall();
  const client = useQueryClient();
  const [settleError, setSettleError] = useState('');
  const settle = useMutation({
    mutationFn: (run: () => Promise<unknown>) => run(),
    onSuccess: () => client.invalidateQueries({ queryKey: ['downloads'] }),
    onError: (thrown) => setSettleError(errorText(thrown))
  });
  const evidence = download.importEvidence;
  const catalogue = useMemo(() => importCatalogue(evidence), [evidence]);
  const files = orderFiles(evidence?.files ?? []);
  const { first, second } = downloadTitle(download);
  const missing = evidence?.unmatchedTracks ?? [];

  return (
    <ScrollView contentContainerStyle={styles.content}>
      <View style={styles.head}>
        {cover(download.albumId)}
        <View style={{ flex: 1, gap: 2 }}>
          <Tag tone="decide">Folder</Tag>
          <T kind="lead">{first}</T>
          <T kind="meta" tone="ink2">{second}</T>
          <T kind="meta" tone="ink3">{download.username} · {download.fileCount} files</T>
        </View>
      </View>
      {download.importError ? <T kind="meta" tone="fail">{download.importError}</T> : null}
      {(evidence?.problems ?? []).map((problem) => (
        <T key={problem} kind="meta" tone="fail">{problem}</T>
      ))}
      {files.map((file) => (
        <FileCard
          key={file.name}
          file={file}
          catalogue={catalogue}
          decision={download.importDecisions?.find((decision) => decision.fileName === file.name)}
          busy={settle.isPending}
          onResolve={(trackId) => settle.mutate(() => api.resolveImportTrack(download.id, { fileName: file.name, trackId }))}
          onWithdraw={(decisionId) => settle.mutate(() => api.withdrawImportResolution(download.id, decisionId))}
        />
      ))}
      {missing.length ? (
        <T kind="meta" tone="ink3">
          No file for {missing.map(trackLabel).join(', ')}.
        </T>
      ) : null}
      {settleError || error ? <T kind="meta" tone="fail">{settleError || error}</T> : null}
      <Actions>
        <Button busy={busy} onPress={() => decide(() => api.revalidateDownload(download.id))}>Check again</Button>
      </Actions>
    </ScrollView>
  );
}

/** One file of the folder. A file that matched says how; one that did not
 * lists the tracks it could be, the evidence's own candidates first and the
 * rest of the release behind "All tracks". */
function FileCard({
  file,
  catalogue,
  decision,
  busy,
  onResolve,
  onWithdraw
}: {
  file: ImportFileEvidence;
  catalogue: ImportTags[];
  decision?: DownloadRequest['importDecisions'][number];
  busy: boolean;
  onResolve: (trackId: string) => void;
  onWithdraw: (decisionId: string) => void;
}) {
  const [chosen, setChosen] = useState('');
  const [all, setAll] = useState(false);
  const open = file.problems.length > 0 && !decision;
  const selected = chosen || suggestedTrack(file, catalogue);
  const candidateIds = new Set((file.candidates ?? []).map((candidate) => candidate.track.trackId));
  if (file.expected?.trackId) candidateIds.add(file.expected.trackId);
  const shown = all || candidateIds.size === 0 ? catalogue : catalogue.filter((track) => candidateIds.has(track.trackId));

  return (
    <View style={styles.card}>
      <T kind="meta" numberOfLines={2}>{file.name}</T>
      {decision ? (
        <T kind="meta" tone="busy">Resolved as “{decision.trackTitle}”. Check again to apply it.</T>
      ) : file.match ? (
        <T kind="meta" tone="ok">{file.match.summary}</T>
      ) : null}
      {open ? file.problems.map((problem) => <T key={problem} kind="meta" tone="decide">{problem}</T>) : null}
      {open && file.resolvable && catalogue.length ? (
        <>
          <T kind="micro" tone="ink3" style={{ marginTop: space.s }}>This file is</T>
          {shown.map((track) => {
            const candidate = file.candidates?.find((entry) => entry.track.trackId === track.trackId);
            const checked = selected === track.trackId;
            return (
              <Pressable
                key={track.trackId}
                onPress={() => setChosen(track.trackId ?? '')}
                accessibilityRole="radio"
                accessibilityState={{ checked }}
                style={[styles.choice, checked && styles.cardChosen]}
              >
                <T kind="meta">{trackLabel(track)}</T>
                {candidate?.agrees.length ? <T kind="micro" tone="ok">Agrees: {candidate.agrees.join(', ')}</T> : null}
                {candidate?.differs.length ? <T kind="micro" tone="fail">Differs: {candidate.differs.join(', ')}</T> : null}
                {candidate?.takenBy ? <T kind="micro" tone="ink3">Already matched by {candidate.takenBy}</T> : null}
              </Pressable>
            );
          })}
          <View style={{ flexDirection: 'row', gap: space.s, marginTop: space.s }}>
            <Button busy={busy} disabled={!selected} onPress={() => onResolve(selected)}>Resolve</Button>
            {!all && shown.length < catalogue.length ? (
              <Button variant="quiet" onPress={() => setAll(true)}>All tracks</Button>
            ) : null}
          </View>
        </>
      ) : null}
      {open && !file.resolvable ? <T kind="meta" tone="ink3">Not a judgement call. Download the file again.</T> : null}
      {decision ? (
        <View style={{ flexDirection: 'row', marginTop: space.s }}>
          <Button variant="quiet" busy={busy} onPress={() => onWithdraw(decision.id)}>Undo</Button>
        </View>
      ) : null}
    </View>
  );
}

/** The preview player. One player for the screen; choosing a card replaces
 * its source. The audio route answers Range, so seeking works. */
function Player({ copy }: { copy: AcquiredCopy | null }) {
  const { session } = useSchall();
  const player = useAudioPlayer(null);
  const status = useAudioPlayerStatus(player);
  const [volume, setVolume] = useState(1);

  useEffect(() => {
    if (!copy) return;
    player.replace({ uri: url(session, media.copyAudio(copy.id)), headers: authHeaders(session) });
    player.volume = volume;
    player.play();
    // volume is applied on the change below; here only the source changes.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [copy?.id]);

  useEffect(() => {
    player.volume = volume;
  }, [player, volume]);

  const position = status.currentTime ?? 0;
  const duration = status.duration || 0;
  const fraction = duration ? Math.min(position / duration, 1) : 0;

  return (
    <View style={styles.player}>
      <View style={{ flexDirection: 'row', alignItems: 'center', gap: space.m }}>
        <Button variant="solid" disabled={!copy} onPress={() => (status.playing ? player.pause() : player.play())} style={{ minWidth: 80 }}>
          {status.playing ? 'Pause' : 'Play'}
        </Button>
        <View style={{ flex: 1, gap: 4 }}>
          <T kind="meta" tone="ink2" numberOfLines={1}>{copy ? copy.name : 'Choose a copy to hear it.'}</T>
          <Pressable
            onPress={(event) => {
              if (!duration) return;
              const { locationX } = event.nativeEvent;
              event.currentTarget.measure((_x, _y, width) => player.seekTo((locationX / width) * duration));
            }}
            style={styles.track}
          >
            <View style={[styles.trackFill, { width: `${fraction * 100}%` }]} />
          </Pressable>
          <T kind="meta" tone="ink3">{formatDuration(position * 1000)} / {formatDuration(duration * 1000)}</T>
        </View>
      </View>
      <View style={{ flexDirection: 'row', alignItems: 'center', gap: space.s }}>
        <T kind="micro" tone="ink3">Volume</T>
        {volumes.map((step) => (
          <Pressable key={step} onPress={() => setVolume(step)} accessibilityRole="button" accessibilityLabel={`Volume ${step * 100} percent`}>
            <View style={[styles.volume, { height: 6 + step * 14, backgroundColor: volume >= step ? theme.ink2 : theme.line }]} />
          </Pressable>
        ))}
      </View>
    </View>
  );
}

function CopyCard({ group, chosen, onPress }: { group: CopyGroupView; chosen: boolean; onPress: () => void }) {
  const copy = group.best;
  const evidence = copy.evidence;
  return (
    <Pressable onPress={onPress} accessibilityRole="radio" accessibilityState={{ checked: chosen }} style={[styles.card, chosen && styles.cardChosen]}>
      <T kind="body" numberOfLines={2}>{copy.name}</T>
      <T kind="meta" tone="ink3">
        {[
          evidence?.bitRate ? `${evidence.bitRate} kbps` : null,
          formatBytes(copy.sizeBytes ?? evidence?.sizeBytes),
          evidence?.observed?.durationMs ? formatDuration(evidence.observed.durationMs) : null,
          group.rips > 1 ? `${group.rips} rips of this audio` : null,
          copy.username
        ]
          .filter(Boolean)
          .join(' · ')}
      </T>
      {evidence?.agrees?.length ? <T kind="meta" tone="ok">Agrees: {evidence.agrees.join(', ')}</T> : null}
      {evidence?.differs?.length ? <T kind="meta" tone="fail">Differs: {evidence.differs.join(', ')}</T> : null}
      {copy.summary ? <T kind="meta" tone="ink2">{copy.summary}</T> : null}
    </Pressable>
  );
}

function CandidateCard({ candidate, chosen, onPress }: { candidate: AcquisitionCandidate; chosen: boolean; onPress: () => void }) {
  return (
    <Pressable onPress={onPress} accessibilityRole="radio" accessibilityState={{ checked: chosen }} style={[styles.card, chosen && styles.cardChosen]}>
      <T kind="body">{candidate.trackTitle}</T>
      <T kind="meta" tone="ink2">{[candidate.artistName, candidate.releaseTitle].filter(Boolean).join(' · ')}</T>
      <T kind="meta" tone="ink3">{[formatDuration(candidate.durationMs), candidate.isrc].filter(Boolean).join(' · ')}</T>
      {candidate.agrees.length ? <T kind="meta" tone="ok">Agrees: {candidate.agrees.join(', ')}</T> : null}
      {candidate.differs.length ? <T kind="meta" tone="fail">Differs: {candidate.differs.join(', ')}</T> : null}
    </Pressable>
  );
}

const styles = StyleSheet.create({
  content: { padding: space.l, gap: space.m, paddingBottom: space.xl * 2 },
  head: { flexDirection: 'row', gap: space.m },
  cover: { width: 88, height: 88, borderRadius: 4 },
  card: { padding: space.m, borderRadius: 6, borderWidth: 1, borderColor: theme.line, backgroundColor: theme.surface, gap: 2 },
  cardChosen: { borderColor: theme.accent },
  choice: { padding: space.s, borderRadius: 4, borderWidth: 1, borderColor: theme.line, gap: 2 },
  player: { padding: space.m, borderRadius: 6, backgroundColor: theme.surfaceRaised, gap: space.m },
  track: { height: 6, borderRadius: 3, backgroundColor: theme.line, overflow: 'hidden' },
  trackFill: { height: 6, backgroundColor: theme.ink2 },
  volume: { width: 10, borderRadius: 2 }
});

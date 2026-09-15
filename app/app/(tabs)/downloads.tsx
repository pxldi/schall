import { useState } from 'react';
import { Alert, FlatList, RefreshControl, View } from 'react-native';
import { useRouter } from 'expo-router';
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query';
import type { DownloadRequest, DownloadView } from '@/api/types';
import { useSchall } from '@/auth/session';
import { downloadTitle } from '@/review';
import { theme } from '@/design/theme';
import { Actions, Button, Loading, Notice, Row, Segments, T, Tag, errorText, formatBytes } from '@/ui/primitives';

type Pile = Extract<DownloadView, 'open' | 'review' | 'failed'>;

function state(download: DownloadRequest): { word: string; tone: 'busy' | 'decide' | 'fail' | 'idle' | 'done' } {
  if (download.status === 'failed') return { word: 'Failed', tone: 'fail' };
  if (download.status === 'cancelled') return { word: 'Cancelled', tone: 'idle' };
  if (download.importStatus === 'needs_review') return { word: 'Needs review', tone: 'decide' };
  if (download.importStatus === 'imported') return { word: 'Imported', tone: 'done' };
  if (download.status === 'requested') return { word: download.startable ? 'Ready' : 'Waiting', tone: 'idle' };
  if (download.progress.transferredBytes === 0 && download.progress.queuedCount > 0) return { word: 'Queued at peer', tone: 'idle' };
  if (download.status === 'completed') return { word: 'Checking', tone: 'busy' };
  return { word: 'Downloading', tone: 'busy' };
}

function DownloadRow({ download }: { download: DownloadRequest }) {
  const router = useRouter();
  const client = useQueryClient();
  const { api } = useSchall();
  const [error, setError] = useState('');
  const act = useMutation({
    mutationFn: (run: () => Promise<unknown>) => run(),
    onSuccess: () => client.invalidateQueries({ queryKey: ['downloads'] }),
    onError: (thrown) => setError(errorText(thrown))
  });
  const { first, second } = downloadTitle(download);
  const { word, tone } = state(download);
  const progress = download.progress;
  const open = download.status === 'requested' || download.status === 'started';

  function confirmCancel() {
    Alert.alert('Cancel this download?', first, [
      { text: 'Keep', style: 'cancel' },
      { text: 'Cancel download', style: 'destructive', onPress: () => act.mutate(() => api.cancelDownload(download.id)) }
    ]);
  }

  return (
    <Row>
      <T kind="body" numberOfLines={1}>{first}</T>
      <T kind="meta" tone="ink2" numberOfLines={1}>{second}</T>
      <View style={{ flexDirection: 'row', gap: 8, alignItems: 'center', flexWrap: 'wrap' }}>
        <Tag tone={tone}>{word}</Tag>
        <T kind="meta" tone="ink3">
          {progress.completedCount}/{download.fileCount} files · {formatBytes(progress.transferredBytes)} of {formatBytes(download.totalSizeBytes)} · {download.username}
        </T>
      </View>
      {download.error ? <T kind="meta" tone="fail">{download.error}</T> : null}
      {error ? <T kind="meta" tone="fail">{error}</T> : null}
      <Actions>
        {download.startable ? <Button variant="solid" busy={act.isPending} onPress={() => act.mutate(() => api.startDownload(download.id))}>Start</Button> : null}
        {download.importStatus === 'needs_review' ? (
          <Button variant="solid" onPress={() => router.push({ pathname: '/review/[id]', params: { id: `download:${download.id}` } })}>Decide</Button>
        ) : null}
        {download.retryableCount > 0 ? <Button busy={act.isPending} onPress={() => act.mutate(() => api.retryDownload(download.id))}>Retry</Button> : null}
        {open ? <Button tone="fail" busy={act.isPending} onPress={confirmCancel}>Cancel</Button> : null}
      </Actions>
    </Row>
  );
}

export default function Downloads() {
  const { api } = useSchall();
  const [pile, setPile] = useState<Pile>('open');
  const list = useQuery({ queryKey: ['downloads', pile], queryFn: () => api.downloads(pile) });
  const counts = list.data?.counts;

  return (
    <View style={{ flex: 1 }}>
      <Segments
        value={pile}
        onChange={setPile}
        options={[
          { key: 'open', label: 'Open', count: counts?.open },
          { key: 'review', label: 'Needs review', count: counts?.review },
          { key: 'failed', label: 'Failed', count: counts?.failed }
        ]}
      />
      <FlatList
        data={list.data?.items ?? []}
        keyExtractor={(download) => download.id}
        renderItem={({ item }) => <DownloadRow download={item} />}
        refreshControl={<RefreshControl refreshing={list.isRefetching} onRefresh={() => list.refetch()} tintColor={theme.ink3} />}
        ListEmptyComponent={list.isPending ? <Loading /> : list.error ? <Notice tone="fail">{errorText(list.error)}</Notice> : <Notice>Nothing here.</Notice>}
      />
    </View>
  );
}

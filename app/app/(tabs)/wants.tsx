import { useState } from 'react';
import { FlatList, RefreshControl, View } from 'react-native';
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query';
import type { AcquisitionTarget } from '@/api/types';
import { lookingFor } from '@/api/schall';
import { useSchall } from '@/auth/session';
import { theme } from '@/design/theme';
import { Actions, Button, Loading, Notice, Row, Segments, T, Tag, errorText, formatDuration } from '@/ui/primitives';

type Pile = 'looking' | 'stopped';
const statuses: Record<Pile, readonly string[]> = { looking: lookingFor, stopped: ['not_wanted'] };

function when(iso: string | null | undefined): string {
  if (!iso) return '';
  const minutes = Math.round((new Date(iso).getTime() - Date.now()) / 60_000);
  if (Math.abs(minutes) < 60) return minutes >= 0 ? `in ${minutes} min` : `${-minutes} min ago`;
  const hours = Math.round(minutes / 60);
  if (Math.abs(hours) < 48) return hours >= 0 ? `in ${hours} h` : `${-hours} h ago`;
  const days = Math.round(hours / 24);
  return days >= 0 ? `in ${days} d` : `${days * -1} d ago`;
}

function WantRow({ want, pile }: { want: AcquisitionTarget; pile: Pile }) {
  const client = useQueryClient();
  const { api } = useSchall();
  const [error, setError] = useState('');
  const act = useMutation({
    mutationFn: (run: () => Promise<unknown>) => run(),
    onSuccess: () => client.invalidateQueries({ queryKey: ['wants'] }),
    onError: (thrown) => setError(errorText(thrown))
  });
  const searching = want.status === 'searching';
  return (
    <Row>
      <T kind="body" numberOfLines={1}>{want.title}</T>
      <T kind="meta" tone="ink2" numberOfLines={1}>{[want.artist, want.album, formatDuration(want.durationMs)].filter(Boolean).join(' · ')}</T>
      <View style={{ flexDirection: 'row', gap: 8, alignItems: 'center', flexWrap: 'wrap' }}>
        {want.waitingOnYou ? <Tag tone="decide">Waiting on you</Tag> : pile === 'stopped' ? <Tag tone="idle">Stopped</Tag> : <Tag tone={searching ? 'busy' : 'idle'}>{searching ? 'Searching' : 'Looking'}</Tag>}
        <T kind="meta" tone="ink3">
          {[want.attempts ? `${want.attempts} tries` : 'not tried yet', pile === 'looking' && want.nextAttemptAt ? `next ${when(want.nextAttemptAt)}` : null].filter(Boolean).join(' · ')}
        </T>
      </View>
      {want.summary ? <T kind="meta" tone="ink2" numberOfLines={2}>{want.summary}</T> : null}
      {want.lastError ? <T kind="meta" tone="fail" numberOfLines={2}>{want.lastError}</T> : null}
      {error ? <T kind="meta" tone="fail">{error}</T> : null}
      <Actions>
        {pile === 'looking' ? (
          <Button busy={act.isPending} onPress={() => act.mutate(() => api.stopLooking(want.id))}>Stop looking</Button>
        ) : (
          <Button busy={act.isPending} onPress={() => act.mutate(() => api.lookAgain(want.id))}>Look again</Button>
        )}
      </Actions>
    </Row>
  );
}

export default function Wants() {
  const { api } = useSchall();
  const [pile, setPile] = useState<Pile>('looking');
  const list = useQuery({ queryKey: ['wants', pile], queryFn: () => api.wants(statuses[pile]) });
  const other = useQuery({
    queryKey: ['wants', pile === 'looking' ? 'stopped' : 'looking', 'count'],
    queryFn: () => api.wants(statuses[pile === 'looking' ? 'stopped' : 'looking'], 1)
  });
  const counts = { [pile]: list.data?.total, [pile === 'looking' ? 'stopped' : 'looking']: other.data?.total } as Record<Pile, number | undefined>;

  return (
    <View style={{ flex: 1 }}>
      <Segments
        value={pile}
        onChange={setPile}
        options={[
          { key: 'looking', label: 'Looking', count: counts.looking },
          { key: 'stopped', label: 'Stopped', count: counts.stopped }
        ]}
      />
      {list.data?.notice ? <Notice>{list.data.notice}</Notice> : null}
      <FlatList
        data={list.data?.items ?? []}
        keyExtractor={(want) => want.id}
        renderItem={({ item }) => <WantRow want={item} pile={pile} />}
        refreshControl={<RefreshControl refreshing={list.isRefetching} onRefresh={() => list.refetch()} tintColor={theme.ink3} />}
        ListEmptyComponent={list.isPending ? <Loading /> : list.error ? <Notice tone="fail">{errorText(list.error)}</Notice> : <Notice>Nothing here.</Notice>}
      />
    </View>
  );
}

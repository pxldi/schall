import { FlatList, RefreshControl, View } from 'react-native';
import { useRouter } from 'expo-router';
import { useQueryClient } from '@tanstack/react-query';
import { useReviewFolders, useReviewQueue } from '@/queries';
import { downloadTitle, questionLabel, questions, type Question } from '@/review';
import { Loading, Notice, Row, T, Tag, errorText } from '@/ui/primitives';
import { theme } from '@/design/theme';

function QuestionRow({ question, onPress }: { question: Question; onPress: () => void }) {
  if (question.kind === 'folder') {
    const { first, second } = downloadTitle(question.download);
    return (
      <Row onPress={onPress}>
        <T kind="body" numberOfLines={1}>{first}</T>
        <T kind="meta" tone="ink2" numberOfLines={1}>{second}</T>
        <View style={{ flexDirection: 'row', gap: 8, alignItems: 'center' }}>
          <Tag tone="decide">{questionLabel.folder}</Tag>
          <T kind="meta" tone="ink3">{question.download.importEvidence?.files?.length ?? 0} files</T>
        </View>
      </Row>
    );
  }
  const { target, copies, candidates } = question.want;
  const count = question.kind === 'version' ? `${candidates.length} recordings` : `${copies.filter((copy) => copy.verdict === 'held').length} copies`;
  return (
    <Row onPress={onPress}>
      <T kind="body" numberOfLines={1}>{target.title}</T>
      <T kind="meta" tone="ink2" numberOfLines={1}>{[target.artist, target.album].filter(Boolean).join(' · ')}</T>
      <View style={{ flexDirection: 'row', gap: 8, alignItems: 'center' }}>
        <Tag tone="decide">{questionLabel[question.kind]}</Tag>
        <T kind="meta" tone="ink3">{count}</T>
      </View>
    </Row>
  );
}

export default function Review() {
  const router = useRouter();
  const client = useQueryClient();
  const queue = useReviewQueue();
  const folders = useReviewFolders();
  const rows = questions(queue.data?.items, folders.data?.items);
  const loading = queue.isPending || folders.isPending;
  const refreshing = queue.isRefetching || folders.isRefetching;

  return (
    <FlatList
      data={rows}
      keyExtractor={(question) => question.id}
      renderItem={({ item }) => <QuestionRow question={item} onPress={() => router.push({ pathname: '/review/[id]', params: { id: item.id } })} />}
      refreshControl={<RefreshControl refreshing={refreshing} onRefresh={() => client.invalidateQueries()} tintColor={theme.ink3} />}
      ListEmptyComponent={
        loading ? <Loading /> : queue.error || folders.error ? <Notice tone="fail">{errorText(queue.error ?? folders.error)}</Notice> : <Notice>Nothing to decide.</Notice>
      }
    />
  );
}

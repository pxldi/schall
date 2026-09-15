import { useState } from 'react';
import { FlatList, StyleSheet, TextInput, View } from 'react-native';
import { useMutation, useQuery } from '@tanstack/react-query';
import type { ArtistSearchResult } from '@/api/types';
import { useSchall } from '@/auth/session';
import { theme, type, space } from '@/design/theme';
import { Actions, Button, Loading, Notice, Row, T, errorText } from '@/ui/primitives';

function ArtistRow({ artist }: { artist: ArtistSearchResult }) {
  const { api } = useSchall();
  const follow = useMutation({ mutationFn: () => api.followArtist(artist) });
  const detail = [artist.type, artist.area ?? artist.country, artist.disambiguation].filter(Boolean).join(' · ');
  return (
    <Row>
      <T kind="body" numberOfLines={1}>{artist.name}</T>
      {detail ? <T kind="meta" tone="ink2" numberOfLines={2}>{detail}</T> : null}
      {follow.error ? <T kind="meta" tone="fail">{errorText(follow.error)}</T> : null}
      <Actions>
        {follow.isSuccess ? (
          <T kind="meta" tone="ok">Following</T>
        ) : (
          <Button variant="solid" busy={follow.isPending} onPress={() => follow.mutate()}>Follow</Button>
        )}
      </Actions>
    </Row>
  );
}

/** Artist search against MusicBrainz, through the server. The query runs on
 * submit, not on every keystroke: MusicBrainz is rate limited and a search
 * per letter would spend the whole allowance on half-typed names. */
export default function Search() {
  const { api } = useSchall();
  const [typed, setTyped] = useState('');
  const [query, setQuery] = useState('');
  const results = useQuery({
    queryKey: ['search', 'artists', query],
    queryFn: () => api.searchArtists(query),
    enabled: query.length > 0,
    refetchInterval: false,
    staleTime: 5 * 60_000
  });

  return (
    <View style={{ flex: 1 }}>
      <View style={styles.bar}>
        <TextInput
          value={typed}
          onChangeText={setTyped}
          onSubmitEditing={() => setQuery(typed.trim())}
          placeholder="Artist name"
          placeholderTextColor={theme.ink3}
          returnKeyType="search"
          autoCorrect={false}
          style={styles.input}
        />
        <Button onPress={() => setQuery(typed.trim())} disabled={!typed.trim()}>Search</Button>
      </View>
      <FlatList
        data={results.data?.items ?? []}
        keyExtractor={(artist) => artist.musicbrainzId}
        renderItem={({ item }) => <ArtistRow artist={item} />}
        keyboardShouldPersistTaps="handled"
        ListEmptyComponent={
          !query ? null : results.isPending ? <Loading /> : results.error ? <Notice tone="fail">{errorText(results.error)}</Notice> : <Notice>MusicBrainz knows no artist by that name.</Notice>
        }
      />
    </View>
  );
}

const styles = StyleSheet.create({
  bar: { flexDirection: 'row', gap: space.s, padding: space.l, borderBottomWidth: StyleSheet.hairlineWidth, borderBottomColor: theme.line },
  input: {
    ...type.body,
    flex: 1,
    color: theme.ink,
    backgroundColor: theme.surface,
    borderColor: theme.line,
    borderWidth: 1,
    borderRadius: 6,
    paddingHorizontal: space.m,
    paddingVertical: space.s
  }
});

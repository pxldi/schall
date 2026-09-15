import { Alert, ScrollView, StyleSheet, View } from 'react-native';
import Constants from 'expo-constants';
import { useSession } from '@/auth/session';
import { space } from '@/design/theme';
import { Button, T } from '@/ui/primitives';

export default function Settings() {
  const { session, signOut } = useSession();
  if (!session) return null;

  function confirmSignOut() {
    Alert.alert('Sign out?', 'The token stays valid on the server until it is removed under Settings → Phone there.', [
      { text: 'Stay', style: 'cancel' },
      { text: 'Sign out', style: 'destructive', onPress: () => void signOut() }
    ]);
  }

  return (
    <ScrollView contentContainerStyle={styles.content}>
      <View style={styles.field}>
        <T kind="micro" tone="ink3">Server</T>
        <T kind="body">{session.server}</T>
      </View>
      <View style={styles.field}>
        <T kind="micro" tone="ink3">This phone</T>
        <T kind="body">{session.actor}</T>
        <T kind="meta" tone="ink3">The name the token was minted under.</T>
      </View>
      <View style={styles.field}>
        <T kind="micro" tone="ink3">App</T>
        <T kind="body">Schall {Constants.expoConfig?.version ?? ''}</T>
      </View>
      <Button tone="fail" onPress={confirmSignOut}>Sign out</Button>
    </ScrollView>
  );
}

const styles = StyleSheet.create({
  content: { padding: space.l, gap: space.xl },
  field: { gap: space.xs }
});

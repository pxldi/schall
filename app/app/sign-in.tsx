import { useEffect, useState } from 'react';
import { KeyboardAvoidingView, Platform, ScrollView, StyleSheet, TextInput, View } from 'react-native';
import { useLocalSearchParams, useRouter } from 'expo-router';
import { useSession } from '@/auth/session';
import { theme, type, space } from '@/design/theme';
import { Button, T, errorText } from '@/ui/primitives';

/** Server and token, typed or scanned. The scan screen comes back here with
 * both as route params. Sign in calls /me; a wrong token is a 401 from the
 * server, said in one line under the fields. */
export default function SignIn() {
  const router = useRouter();
  const scanned = useLocalSearchParams<{ server?: string; token?: string }>();
  const { signIn } = useSession();
  const [server, setServer] = useState('');
  const [token, setToken] = useState('');
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState('');

  useEffect(() => {
    if (scanned.server) setServer(scanned.server);
    if (scanned.token) setToken(scanned.token);
  }, [scanned.server, scanned.token]);

  async function submit() {
    setBusy(true);
    setError('');
    try {
      await signIn(server, token);
    } catch (thrown) {
      setError(errorText(thrown));
    } finally {
      setBusy(false);
    }
  }

  return (
    <KeyboardAvoidingView behavior={Platform.OS === 'ios' ? 'padding' : undefined} style={styles.screen}>
      <ScrollView contentContainerStyle={styles.content} keyboardShouldPersistTaps="handled">
        <T kind="display">Schall</T>
        <T kind="meta" tone="ink2">
          Open Settings → Phone on the web, add a phone, and scan the code it shows. Or type both in.
        </T>
        <Button variant="solid" onPress={() => router.push('/scan')}>
          Scan code
        </Button>
        <View style={styles.field}>
          <T kind="micro" tone="ink3">
            Server
          </T>
          <TextInput
            value={server}
            onChangeText={setServer}
            placeholder="https://schall.example"
            placeholderTextColor={theme.ink3}
            autoCapitalize="none"
            autoCorrect={false}
            keyboardType="url"
            style={styles.input}
          />
        </View>
        <View style={styles.field}>
          <T kind="micro" tone="ink3">
            Token
          </T>
          <TextInput
            value={token}
            onChangeText={setToken}
            placeholder="schall_…"
            placeholderTextColor={theme.ink3}
            autoCapitalize="none"
            autoCorrect={false}
            secureTextEntry
            style={styles.input}
          />
        </View>
        {error ? (
          <T kind="meta" tone="fail">
            {error}
          </T>
        ) : null}
        <Button onPress={submit} busy={busy} disabled={!server || !token}>
          Sign in
        </Button>
      </ScrollView>
    </KeyboardAvoidingView>
  );
}

const styles = StyleSheet.create({
  screen: { flex: 1, backgroundColor: theme.ground },
  content: { padding: space.xl, gap: space.l },
  field: { gap: space.xs },
  input: {
    ...type.body,
    color: theme.ink,
    backgroundColor: theme.surface,
    borderColor: theme.line,
    borderWidth: 1,
    borderRadius: 6,
    paddingHorizontal: space.m,
    paddingVertical: space.s
  }
});

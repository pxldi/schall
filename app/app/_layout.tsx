import { useEffect } from 'react';
import { AppState, type AppStateStatus } from 'react-native';
import { Stack } from 'expo-router';
import { StatusBar } from 'expo-status-bar';
import { QueryClient, QueryClientProvider, focusManager, useQueryClient } from '@tanstack/react-query';
import { SessionProvider, useSession } from '@/auth/session';
import { theme } from '@/design/theme';
import { connectEvents } from '@/events';

// The event stream (src/events.ts) is the update path; the 15 s poll is the
// safety net, the same as on the web. Polling stops in the background:
// focusManager follows AppState, so refetchInterval only ticks while the app
// is in front.
const queryClient = new QueryClient({
  defaultOptions: { queries: { staleTime: 5_000, refetchInterval: 15_000, retry: 1 } }
});

function onAppState(status: AppStateStatus) {
  focusManager.setFocused(status === 'active');
}

/** Keeps the event stream open while somebody is signed in and the app is in
 * front. Coming back to the front refetches every stale query through
 * focusManager, so a notice missed in the background costs nothing. */
function LiveUpdates() {
  const { session } = useSession();
  const client = useQueryClient();
  useEffect(() => {
    if (!session) return;
    let close = AppState.currentState === 'active' ? connectEvents(client, session) : () => {};
    const subscription = AppState.addEventListener('change', (status) => {
      close();
      close = status === 'active' ? connectEvents(client, session) : () => {};
    });
    return () => {
      close();
      subscription.remove();
    };
  }, [client, session]);
  return null;
}

function Gate() {
  const { loaded, session } = useSession();
  if (!loaded) return null;
  const signedIn = session !== null;
  return (
    <Stack
      screenOptions={{
        headerStyle: { backgroundColor: theme.ground },
        headerTintColor: theme.ink,
        headerShadowVisible: false,
        contentStyle: { backgroundColor: theme.ground }
      }}
    >
      <Stack.Protected guard={signedIn}>
        <Stack.Screen name="(tabs)" options={{ headerShown: false }} />
        <Stack.Screen name="review/index" options={{ headerShown: false }} />
        <Stack.Screen name="review/[id]" options={{ title: 'Review' }} />
      </Stack.Protected>
      <Stack.Protected guard={!signedIn}>
        <Stack.Screen name="sign-in" options={{ title: 'Sign in' }} />
        <Stack.Screen name="scan" options={{ title: 'Scan the code', presentation: 'modal' }} />
      </Stack.Protected>
    </Stack>
  );
}

export default function RootLayout() {
  useEffect(() => {
    const subscription = AppState.addEventListener('change', onAppState);
    return () => subscription.remove();
  }, []);
  return (
    <QueryClientProvider client={queryClient}>
      <SessionProvider>
        <StatusBar style="light" />
        <LiveUpdates />
        <Gate />
      </SessionProvider>
    </QueryClientProvider>
  );
}

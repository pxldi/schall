import { createContext, useCallback, useContext, useEffect, useMemo, useState, type ReactNode } from 'react';
import * as SecureStore from 'expo-secure-store';
import { normaliseServer, type Session } from '@/api/client';
import { schall } from '@/api/schall';

const KEY = 'schall.session';

interface Stored extends Session {
  actor: string;
}

interface SessionState {
  /** Nothing is known until SecureStore has answered once. */
  loaded: boolean;
  session: Stored | null;
  signIn: (server: string, token: string) => Promise<void>;
  signOut: () => Promise<void>;
}

const Context = createContext<SessionState | null>(null);

export function SessionProvider({ children }: { children: ReactNode }) {
  const [loaded, setLoaded] = useState(false);
  const [session, setSession] = useState<Stored | null>(null);

  useEffect(() => {
    let live = true;
    SecureStore.getItemAsync(KEY)
      .then((raw) => {
        if (!live) return;
        if (raw) setSession(JSON.parse(raw) as Stored);
      })
      .catch(() => undefined)
      .finally(() => live && setLoaded(true));
    return () => {
      live = false;
    };
  }, []);

  // Signing in is one call to /me. It proves the token works and gives back
  // the name the token was minted under, which Settings shows.
  const signIn = useCallback(async (serverInput: string, tokenInput: string) => {
    const server = normaliseServer(serverInput);
    const token = tokenInput.trim();
    if (!server) throw new Error('Type the server address.');
    if (!token) throw new Error('Paste the token.');
    const me = await schall({ server, token }).me();
    const next: Stored = { server, token, actor: me.actor };
    await SecureStore.setItemAsync(KEY, JSON.stringify(next));
    setSession(next);
  }, []);

  const signOut = useCallback(async () => {
    await SecureStore.deleteItemAsync(KEY);
    setSession(null);
  }, []);

  const value = useMemo(() => ({ loaded, session, signIn, signOut }), [loaded, session, signIn, signOut]);
  return <Context.Provider value={value}>{children}</Context.Provider>;
}

export function useSession(): SessionState {
  const state = useContext(Context);
  if (!state) throw new Error('useSession needs a SessionProvider above it');
  return state;
}

/** The signed-in client. Screens behind the sign-in gate call this; it throws
 * if one renders without a session, which the gate prevents. */
export function useSchall() {
  const { session } = useSession();
  if (!session) throw new Error('no session');
  return useMemo(() => ({ api: schall(session), session }), [session]);
}

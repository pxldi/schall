// The request layer is the app's own: a base URL and a Bearer header. The web
// has neither, because the browser carries the forward-auth cookie for it.

export interface Session {
  server: string;
  token: string;
}

interface Problem {
  title?: string;
  details?: string[];
}

export class ApiError extends Error {
  readonly status: number;
  readonly title?: string;
  readonly details?: string[];

  constructor(status: number, message: string, problem?: Problem) {
    super(message);
    this.name = 'ApiError';
    this.status = status;
    this.title = problem?.title;
    this.details = problem?.details;
  }
}

/** The address as the person typed it, made into something fetch accepts: a
 * scheme when none was given, no trailing slash. */
export function normaliseServer(input: string): string {
  let server = input.trim().replace(/\/+$/, '');
  if (server && !/^https?:\/\//i.test(server)) server = `https://${server}`;
  return server;
}

export function authHeaders(session: Session): Record<string, string> {
  return { Authorization: `Bearer ${session.token}` };
}

export function url(session: Session, path: string): string {
  return `${session.server}${path}`;
}

export async function request<T>(session: Session, path: string, init: RequestInit = {}): Promise<T> {
  const headers: Record<string, string> = {
    Accept: 'application/json',
    ...authHeaders(session),
    ...((init.headers as Record<string, string> | undefined) ?? {})
  };
  if (init.body && !headers['Content-Type']) headers['Content-Type'] = 'application/json';

  const response = await fetch(url(session, path), { ...init, headers });
  if (!response.ok) {
    let problem: Problem | undefined;
    try {
      problem = (await response.json()) as Problem;
    } catch {
      problem = undefined;
    }
    const message =
      response.status === 401
        ? 'The server did not accept this token.'
        : problem?.details?.[0] ?? problem?.title ?? `Request failed (${response.status})`;
    throw new ApiError(response.status, message, problem);
  }
  if (response.status === 204) return undefined as T;
  const text = await response.text();
  return (text ? JSON.parse(text) : undefined) as T;
}

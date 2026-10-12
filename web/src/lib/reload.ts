// A tab that was open across a restart or a deploy asks for page code the
// server cannot give it: during a restart nothing answers, after a deploy the
// file names have changed. Chrome remembers a failed `import()` for the life
// of the document, so every later page change failed the same way and the tab
// stayed on the error page until somebody reloaded it by hand. A full page
// load is the one thing that clears it, so the router gets one.

const MESSAGES = [
  'Failed to fetch dynamically imported module', // Chrome
  'error loading dynamically imported module', // Firefox
  'Importing a module script failed', // Safari
  'Unable to preload CSS' // a stylesheet SvelteKit preloads with the page code
];

/** Whether an error is the browser failing to load a piece of page code. */
export function isStaleCode(error: unknown): boolean {
  const message = error instanceof Error ? error.message : String(error ?? '');
  return MESSAGES.some((part) => message.includes(part));
}

const KEY = 'schall:reloaded';
// A reload that lands on the same failure again inside this window is left on
// the error page, so a server that stays down cannot put the tab in a loop.
const LOOP_MS = 15_000;

export interface Reloader {
  now(): number;
  read(): string | null;
  write(value: string): void;
  /** Whether the server answers at all. */
  reachable(): Promise<boolean>;
  wait(ms: number): Promise<void>;
  go(href: string): void;
}

/** Loads `href` in full once the server answers. Returns false, and leaves the
 *  error page to be drawn, when it already tried this address a moment ago or
 *  the server did not come back within `tries` checks. */
export async function reloadOnce(href: string, reloader: Reloader, tries = 20): Promise<boolean> {
  try {
    const last = JSON.parse(reloader.read() ?? 'null') as { href: string; at: number } | null;
    if (last && last.href === href && reloader.now() - last.at < LOOP_MS) return false;
  } catch {
    // A value that does not parse is treated as no earlier reload.
  }
  // During a restart the server is gone for a few seconds. Loading the page
  // then would swap the tab for the browser's own offline page, so the router
  // holds the page that is showing until the server answers.
  for (let attempt = 0; ; attempt++) {
    if (await reloader.reachable()) break;
    if (attempt + 1 >= tries) return false;
    await reloader.wait(1_500);
  }
  reloader.write(JSON.stringify({ href, at: reloader.now() }));
  reloader.go(href);
  return true;
}

/** The browser's own storage, network and address bar. */
export const browserReloader: Reloader = {
  now: () => Date.now(),
  read: () => {
    try {
      return sessionStorage.getItem(KEY);
    } catch {
      return null;
    }
  },
  write: (value) => {
    try {
      sessionStorage.setItem(KEY, value);
    } catch {
      // Without storage the loop guard is gone, and the reload still helps.
    }
  },
  reachable: async () => {
    try {
      const response = await fetch('/_app/version.json', { cache: 'no-store' });
      return response.ok;
    } catch {
      return false;
    }
  },
  wait: (ms) => new Promise((resolve) => setTimeout(resolve, ms)),
  go: (href) => location.assign(href)
};

/** A tab opened before a deploy keeps running the interface it loaded, so new
 *  fixes stayed invisible until somebody reloaded by hand. Once SvelteKit has
 *  seen a newer build, the next page change is made as a full page load.
 *  Returns whether it took the navigation over. */
export function loadNewerBuild(
  navigation: { willUnload: boolean; to: { url: URL } | null },
  newer: boolean,
  go: (href: string) => void
): boolean {
  if (!newer || navigation.willUnload || !navigation.to) return false;
  go(navigation.to.url.href);
  return true;
}

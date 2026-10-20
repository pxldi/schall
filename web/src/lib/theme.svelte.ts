// The appearance a reader chose in Settings, kept per browser. `system`
// follows `prefers-color-scheme`. The resolved theme is written to <html> as
// `data-theme`; app.html does the same before the first paint, so the page
// never flashes the wrong ground.

export type Appearance = 'system' | 'light' | 'dark';

export const APPEARANCE_KEY = 'schall:appearance';

function stored(): Appearance {
  try {
    const value = localStorage.getItem(APPEARANCE_KEY);
    return value === 'light' || value === 'dark' ? value : 'system';
  } catch {
    return 'system';
  }
}

function systemTheme(): 'light' | 'dark' {
  return typeof window !== 'undefined' &&
    typeof window.matchMedia === 'function' &&
    window.matchMedia('(prefers-color-scheme: light)').matches
    ? 'light'
    : 'dark';
}

export const appearance = $state<{ choice: Appearance }>({
  choice: typeof window === 'undefined' ? 'system' : stored()
});

/** The theme a choice comes to on this machine. */
export function resolve(choice: Appearance): 'light' | 'dark' {
  return choice === 'system' ? systemTheme() : choice;
}

function apply() {
  if (typeof document === 'undefined') return;
  document.documentElement.dataset.theme = resolve(appearance.choice);
}

export function chooseAppearance(choice: Appearance) {
  appearance.choice = choice;
  try {
    if (choice === 'system') localStorage.removeItem(APPEARANCE_KEY);
    else localStorage.setItem(APPEARANCE_KEY, choice);
  } catch {
    // Best effort: the choice still holds for this visit.
  }
  apply();
}

/** Follows the system theme while the choice is `system`. The shell calls it
 *  once. */
export function followSystemTheme(): () => void {
  apply();
  if (typeof window.matchMedia !== 'function') return () => {};
  const query = window.matchMedia('(prefers-color-scheme: light)');
  const changed = () => {
    if (appearance.choice === 'system') apply();
  };
  query.addEventListener?.('change', changed);
  return () => query.removeEventListener?.('change', changed);
}

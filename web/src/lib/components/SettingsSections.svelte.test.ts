import { afterEach, beforeAll, beforeEach, describe, expect, it, vi } from 'vitest';
import { cleanup, fireEvent, render, screen, setup, within } from '@testing-library/svelte';
import { QueryClient, QueryClientProvider } from '@tanstack/svelte-query';
import type {
  DuplicateResolutionSettings,
  NavidromeSettings,
  SourcePreferences,
  UpgradeSettings,
  WeeklyOverview,
  WeeklySettings
} from '$lib/api';

// The Library settings screen, and the one card on it these tests are
// about: Formats.
//
// A source search asks peer networks for copies of music, and this card is where
// somebody says which formats to prefer, which never to fetch, and the lowest
// bit rate a copy may have. The bit rate can be said once for everything and
// again per format, because one number cannot serve both: 320 kbps is the top of
// MP3 and out of reach for Opus.
//
// Nothing here decides what a file is. A copy that meets every floor is still
// identified by its audio after it arrives.
//
// The boundaries stubbed are the two the screen has: SvelteKit's address bar,
// which the section tabs read, and `fetch`.
vi.mock('$app/state', () => ({
  page: { params: {}, url: new URL('http://localhost/settings/sources') }
}));

const { default: SettingsSections } = await import('$lib/components/SettingsSections.svelte');

function preferences(overrides: Partial<SourcePreferences> = {}): SourcePreferences {
  return {
    preferred: [],
    unacceptable: [],
    minimumBitRate: 0,
    formatMinimumBitRate: {},
    known: ['flac', 'wav', 'mp3', 'aac', 'm4a', 'ogg', 'opus', 'wma'],
    lossy: { mp3: 320, aac: 512, m4a: 512, ogg: 500, opus: 510, wma: 384 },
    ...overrides
  };
}

/** What was PUT to the settings, decoded, so a test can say what was saved
 * rather than which field was typed in. */
let saved: unknown = null;
let stored: SourcePreferences = preferences();

function answering() {
  saved = null;
  vi.stubGlobal(
    'fetch',
    vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
      const url = new URL(String(input), 'http://localhost');
      if (url.pathname === '/api/v1/settings/source-preferences') {
        if (init?.method === 'PUT') {
          saved = JSON.parse(String(init.body));
          return new Response(JSON.stringify(stored), { status: 200 });
        }
        return new Response(JSON.stringify(stored), { status: 200 });
      }
      // The other cards sharing the Library screen. Given shapes their own
      // effects can load without crashing — see the Duplicate resolution
      // block below, which needs the same ones for the same reason.
      if (url.pathname === '/api/v1/settings/library-layout') {
        return new Response(
          JSON.stringify({ template: '', default: '', example: '', isDefault: true }),
          { status: 200 }
        );
      }
      if (url.pathname === '/api/v1/library/tags') {
        return new Response(
          JSON.stringify({
            status: 'idle',
            written: 0,
            unchanged: 0,
            skipped: 0,
            skippedAmbiguous: 0,
            skippedUnproved: 0,
            failed: 0,
            eligible: 0
          }),
          { status: 200 }
        );
      }
      if (url.pathname === '/api/v1/library/transcode') {
        return new Response(
          JSON.stringify({ status: 'idle', transcoded: 0, skipped: 0, failed: 0, eligible: 0 }),
          { status: 200 }
        );
      }
      if (url.pathname === '/api/v1/library/layout/moves') {
        return new Response('null', { status: 200 });
      }
      // Every other card on the screen. They are drawn from their own requests
      // and none of them is what these tests are about.
      return new Response(JSON.stringify({}), { status: 200 });
    })
  );
}

/** The Save button of the card the bit rate fields sit on. The screen has one
 * per card, so pressing "the" Save button would be ambiguous. */
function saveFormats(field: HTMLElement) {
  const card = field.closest('form');
  if (!card) throw new Error('the bit rate field is not on a form');
  return within(card).getByRole('button', { name: 'Save' });
}

function openedSources() {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  return render(SettingsSections, { show: 'sources' } as never, {
    wrapper: QueryClientProvider,
    wrapperProps: { client }
  });
}

function openedLibrary() {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  return render(SettingsSections, { show: 'library' } as never, {
    wrapper: QueryClientProvider,
    wrapperProps: { client }
  });
}

/** The switch on the Duplicate resolution card, told apart from every other
 * checkbox on the library screen by its own label text. */
async function duplicatesSwitch() {
  const checkbox = (await screen.findByRole('checkbox', {
    name: /delete proven duplicates automatically|leave every duplicate for you to decide/
  })) as HTMLInputElement;
  await vi.waitFor(() => expect(checkbox.disabled).toBe(false));
  return checkbox;
}

beforeAll(setup);

describe('the Formats card', () => {
  beforeEach(() => {
    stored = preferences();
    answering();
  });

  afterEach(() => {
    cleanup();
    vi.unstubAllGlobals();
  });

  // The setting the owner asked for: a field per format, so "at least 320 kbps
  // MP3" can be said without refusing Opus, which never reaches 320.
  it('offers a bit rate field for every format that has one', async () => {
    openedLibrary();

    expect(await screen.findByLabelText('Lowest bit rate for MP3')).toBeTruthy();
    expect(screen.getByLabelText('Lowest bit rate for OPUS')).toBeTruthy();
  });

  // A lossless format keeps every bit of the recording, so there is no bit rate
  // to set a floor on and no field to offer.
  it('offers no bit rate field for a lossless format', async () => {
    openedLibrary();

    await screen.findByLabelText('Lowest bit rate for MP3');
    expect(screen.queryByLabelText('Lowest bit rate for FLAC')).toBeNull();
  });

  it('shows the floors that were stored', async () => {
    stored = preferences({ formatMinimumBitRate: { mp3: 320 } });
    openedLibrary();

    const field = (await screen.findByLabelText('Lowest bit rate for MP3')) as HTMLInputElement;
    expect(field.value).toBe('320');
  });

  it('saves the floor that was typed', async () => {
    openedLibrary();

    const field = await screen.findByLabelText('Lowest bit rate for MP3');
    await fireEvent.input(field, { target: { value: '320' } });
    await fireEvent.click(saveFormats(field));

    await vi.waitFor(() => expect(saved).not.toBeNull());
    expect((saved as { formatMinimumBitRate: Record<string, number> }).formatMinimumBitRate.mp3).toBe(
      320
    );
  });

  // Clearing a field is how a floor is removed, so an empty field has to reach
  // the server as zero. Leaving it out would keep the floor that was there.
  it('sends a cleared field as no floor at all', async () => {
    stored = preferences({ formatMinimumBitRate: { mp3: 320 } });
    openedLibrary();

    const field = await screen.findByLabelText('Lowest bit rate for MP3');
    await fireEvent.input(field, { target: { value: '' } });
    await fireEvent.click(saveFormats(field));

    await vi.waitFor(() => expect(saved).not.toBeNull());
    expect((saved as { formatMinimumBitRate: Record<string, number> }).formatMinimumBitRate.mp3).toBe(
      0
    );
  });

  // A copy that never said its bit rate does not meet a floor, and a variable
  // bit rate is read as its average. Both are surprising enough to be written
  // down, and both sit behind a press rather than in front of the fields.
  it('explains how a bit rate is read, behind a disclosure', async () => {
    openedLibrary();

    const summary = await screen.findByText('How a bit rate is read');
    expect(summary.closest('details')?.open).toBe(false);
    expect(
      screen.getByText(/does not say its bit rate does not meet a floor/)
    ).toBeTruthy();
  });
});

describe('settings loading', () => {
  afterEach(() => {
    cleanup();
    vi.unstubAllGlobals();
  });

  it('renders the slskd form with disabled controls while its query is pending', async () => {
    let answer!: (response: Response) => void;
    const pending = new Promise<Response>((resolve) => {
      answer = resolve;
    });

    answering();
    const normalFetch = globalThis.fetch as typeof fetch;
    vi.stubGlobal(
      'fetch',
      vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
        const url = new URL(String(input), 'http://localhost');
        if (url.pathname === '/api/v1/settings/slskd') return pending;
        return normalFetch(input, init);
      })
    );

    openedSources();

    const url = screen.getByLabelText('API URL') as HTMLInputElement;
    expect(url.disabled).toBe(true);
    const form = url.closest('form');
    if (!form) throw new Error('slskd form not found');
    expect((within(form).getByRole('button', { name: 'Save' }) as HTMLButtonElement).disabled).toBe(
      true
    );

    answer(
      new Response(
        JSON.stringify({
          baseUrl: '',
          apiKeySet: false,
          configured: false,
          enabled: true,
          searchTimeoutSeconds: 20,
          connectionStatus: 'unknown',
          lastCheckedAt: null
        }),
        { status: 200 }
      )
    );
    await vi.waitFor(() => expect(url.disabled).toBe(false));
  });
});

describe('the Weekly playlist card', () => {
  let weeklySaved: unknown = null;
  let weeklyStored: WeeklySettings = {
    enabled: true,
    songsPerWeek: 20,
    mode: 'report',
    libraryShare: 25,
    onePerArtist: false
  };

  function weeklyOverview(): WeeklyOverview {
    return { settings: weeklyStored, playlistId: null, leases: [], runs: [] };
  }

  beforeEach(() => {
    weeklySaved = null;
    weeklyStored = {
      enabled: true,
      songsPerWeek: 20,
      mode: 'report',
      libraryShare: 25,
      onePerArtist: false
    };
    vi.stubGlobal(
      'fetch',
      vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
        const url = new URL(String(input), 'http://localhost');
        if (url.pathname === '/api/v1/weekly') {
          return new Response(JSON.stringify(weeklyOverview()), { status: 200 });
        }
        if (url.pathname === '/api/v1/weekly/settings' && init?.method === 'PUT') {
          weeklySaved = JSON.parse(String(init.body));
          return new Response(JSON.stringify(weeklyStored), { status: 200 });
        }
        if (url.pathname === '/api/v1/settings/source-preferences') {
          return new Response(JSON.stringify(preferences()), { status: 200 });
        }
        return new Response(JSON.stringify({}), { status: 200 });
      })
    );
  });

  afterEach(() => {
    cleanup();
    vi.unstubAllGlobals();
  });

  function openedWeekly() {
    const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
    return render(SettingsSections, { show: 'automation' } as never, {
      wrapper: QueryClientProvider,
      wrapperProps: { client }
    });
  }

  function saveButton() {
    return screen.getByRole('heading', { name: 'Weekly playlist' }).closest('div')
      ?.parentElement?.querySelector('button[type="submit"]');
  }

  it('saves the library share and one-per-artist settings', async () => {
    openedWeekly();

    const share = (await screen.findByLabelText('Library share')) as HTMLInputElement;
    await vi.waitFor(() => expect(share.disabled).toBe(false));
    await fireEvent.input(share, { target: { value: '60' } });
    await fireEvent.click(screen.getByRole('checkbox', { name: 'only one song per artist' }));
    const button = saveButton();
    if (!button) throw new Error('weekly Save button not found');
    await fireEvent.click(button);

    await vi.waitFor(() => expect(weeklySaved).toEqual({
      enabled: true,
      songsPerWeek: 20,
      mode: 'report',
      libraryShare: 60,
      onePerArtist: true
    }));
  });

  it('refuses a library share outside 0 to 100', async () => {
    openedWeekly();

    const share = (await screen.findByLabelText('Library share')) as HTMLInputElement;
    await fireEvent.input(share, { target: { value: '101' } });
    expect(share.validity.valid).toBe(false);
    const button = saveButton();
    if (!button) throw new Error('weekly Save button not found');
    await fireEvent.click(button);

    expect(weeklySaved).toBeNull();
  });
});

// The sweep that looks back at files the library obtained before the floor
// above was set, or before it was raised.
describe('the Upgrade low-quality files card', () => {
  let upgradeSaved: unknown = null;
  let upgradeStored: UpgradeSettings = { enabled: false, belowFloor: 0 };
  let scanned = false;

  beforeEach(() => {
    upgradeSaved = null;
    upgradeStored = { enabled: false, belowFloor: 0 };
    scanned = false;
    vi.stubGlobal(
      'fetch',
      vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
        const url = new URL(String(input), 'http://localhost');
        if (url.pathname === '/api/v1/settings/source-preferences') {
          return new Response(JSON.stringify(preferences()), { status: 200 });
        }
        if (url.pathname === '/api/v1/settings/upgrade') {
          if (init?.method === 'PUT') {
            upgradeSaved = JSON.parse(String(init.body));
            upgradeStored = { ...upgradeStored, enabled: (upgradeSaved as { enabled: boolean }).enabled };
            return new Response(JSON.stringify(upgradeStored), { status: 200 });
          }
          return new Response(JSON.stringify(upgradeStored), { status: 200 });
        }
        if (url.pathname === '/api/v1/settings/upgrade/scan' && init?.method === 'POST') {
          scanned = true;
          return new Response(null, { status: 202 });
        }
        // The other cards sharing the Library screen. Given shapes their own
        // effects can load without crashing, the same reason the Duplicate
        // resolution block below needs them.
        if (url.pathname === '/api/v1/settings/library-layout') {
          return new Response(
            JSON.stringify({ template: '', default: '', example: '', isDefault: true }),
            { status: 200 }
          );
        }
        if (url.pathname === '/api/v1/library/tags') {
          return new Response(
            JSON.stringify({
              status: 'idle',
              written: 0,
              unchanged: 0,
              skipped: 0,
              skippedAmbiguous: 0,
              skippedUnproved: 0,
              failed: 0,
              eligible: 0
            }),
            { status: 200 }
          );
        }
        if (url.pathname === '/api/v1/library/transcode') {
          return new Response(
            JSON.stringify({ status: 'idle', transcoded: 0, skipped: 0, failed: 0, eligible: 0 }),
            { status: 200 }
          );
        }
        if (url.pathname === '/api/v1/library/layout/moves') {
          return new Response('null', { status: 200 });
        }
        return new Response(JSON.stringify({}), { status: 200 });
      })
    );
  });

  afterEach(() => {
    cleanup();
    vi.unstubAllGlobals();
  });

  it('shows how many files are below the floor right now', async () => {
    upgradeStored = { enabled: true, belowFloor: 3 };
    openedLibrary();

    expect(await screen.findByText('3 files')).toBeTruthy();
  });

  it('saves the switch that was set', async () => {
    openedLibrary();

    const checkbox = (await screen.findByRole('checkbox', {
      name: /look for better copies|leave files as they are/
    })) as HTMLInputElement;
    await vi.waitFor(() => expect(checkbox.disabled).toBe(false));
    await fireEvent.click(checkbox);
    const card = checkbox.closest('form');
    if (!card) throw new Error('the switch is not on a form');
    await fireEvent.click(within(card).getByRole('button', { name: 'Save' }));

    await vi.waitFor(() => expect(upgradeSaved).not.toBeNull());
    expect((upgradeSaved as { enabled: boolean }).enabled).toBe(true);
  });

  it('asks for an immediate scan when Scan now is pressed', async () => {
    openedLibrary();

    await fireEvent.click(await screen.findByRole('button', { name: 'Scan now' }));

    await vi.waitFor(() => expect(scanned).toBe(true));
  });
});

describe('the Navidrome card', () => {
  function navidromeSettings(overrides: Partial<NavidromeSettings> = {}): NavidromeSettings {
    return {
      configured: true,
      baseUrl: 'http://navidrome:4533',
      username: 'schall',
      passwordSet: true,
      enabled: true,
      connectionStatus: 'ok',
      lastCheckedAt: null,
      lastNotifiedAt: null,
      ...overrides
    };
  }

  let navidromeStored: NavidromeSettings;
  let rescanned: number;

  beforeEach(() => {
    navidromeStored = navidromeSettings();
    rescanned = 0;
    vi.stubGlobal(
      'fetch',
      vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
        const url = new URL(String(input), 'http://localhost');
        if (
          url.pathname === '/api/v1/settings/navidrome/rescan' &&
          init?.method === 'POST'
        ) {
          rescanned += 1;
          navidromeStored = { ...navidromeStored, lastNotifiedAt: '2026-08-28T12:00:00Z' };
          return new Response(JSON.stringify(navidromeStored), { status: 200 });
        }
        if (url.pathname === '/api/v1/settings/navidrome') {
          return new Response(JSON.stringify(navidromeStored), { status: 200 });
        }
        if (url.pathname === '/api/v1/settings/source-preferences') {
          return new Response(JSON.stringify(preferences()), { status: 200 });
        }
        // Every other card on the screen. Drawn from its own requests and none
        // of them is what these tests are about.
        return new Response(JSON.stringify({}), { status: 200 });
      })
    );
  });

  afterEach(() => {
    cleanup();
    vi.unstubAllGlobals();
  });

  // Pressing the button asks the player right now, and the line at the bottom
  // of the card shows the press landed rather than staying on "never".
  it('tells the player now and updates the line', async () => {
    openedSources();

    const button = await screen.findByRole('button', { name: /Tell it now/ });
    expect(await screen.findByText(/told about new music never/)).toBeTruthy();

    await fireEvent.click(button);

    await vi.waitFor(() => expect(rescanned).toBe(1));
    await vi.waitFor(() =>
      expect(screen.queryByText(/told about new music never/)).toBeNull()
    );
  });

  it('posts to the rescan endpoint', async () => {
    openedSources();

    await fireEvent.click(await screen.findByRole('button', { name: /Tell it now/ }));

    await vi.waitFor(() => expect(rescanned).toBe(1));
  });

  // Nobody is there to tell when Navidrome is switched off, so the button does
  // not offer to press.
  it('is disabled when Navidrome is switched off', async () => {
    navidromeStored = navidromeSettings({ enabled: false });
    openedSources();

    const button = (await screen.findByRole('button', {
      name: /Tell it now/
    })) as HTMLButtonElement;
    expect(button.disabled).toBe(true);
  });

  // Nor when it is configured but has never answered: there is nobody proven
  // to be listening yet.
  it('is disabled when Navidrome is not connected', async () => {
    navidromeStored = navidromeSettings({ connectionStatus: 'unknown' });
    openedSources();

    const button = (await screen.findByRole('button', {
      name: /Tell it now/
    })) as HTMLButtonElement;
    expect(button.disabled).toBe(true);
  });
});

// Automatic duplicate resolution: settles a recording the library holds more
// than once by itself, but only what it can prove — the same audio, or one
// copy proven better than every other. Off by default; there is no scan
// route, saving with the switch on queues the sweep server-side.
describe('the Duplicate resolution card', () => {
  let duplicatesSaved: unknown = null;
  let duplicatesStored: DuplicateResolutionSettings = { enabled: false };
  let duplicatesGetStatus = 200;
  let saveShouldFail = false;
  // Held open for the one test that has to observe the switch mid-save; every
  // other test leaves it resolved so the save completes on its own.
  let saveGate = Promise.resolve();

  beforeEach(() => {
    duplicatesSaved = null;
    duplicatesStored = { enabled: false };
    duplicatesGetStatus = 200;
    saveShouldFail = false;
    saveGate = Promise.resolve();
    vi.stubGlobal(
      'fetch',
      vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
        const url = new URL(String(input), 'http://localhost');
        if (url.pathname === '/api/v1/settings/duplicates') {
          if (init?.method === 'PUT') {
            await saveGate;
            if (saveShouldFail) {
              return new Response(JSON.stringify({ title: 'this setting is unavailable' }), {
                status: 503
              });
            }
            duplicatesSaved = JSON.parse(String(init.body));
            duplicatesStored = { enabled: (duplicatesSaved as { enabled: boolean }).enabled };
            return new Response(JSON.stringify(duplicatesStored), { status: 200 });
          }
          if (duplicatesGetStatus !== 200) {
            return new Response(JSON.stringify({ title: 'this setting is unavailable' }), {
              status: duplicatesGetStatus
            });
          }
          return new Response(JSON.stringify(duplicatesStored), { status: 200 });
        }
        // The other cards sharing this screen. Given shapes their own effects
        // can load without crashing — an empty object leaves `template` or
        // `written` undefined, which is a fault in those cards and not what
        // these tests are about.
        if (url.pathname === '/api/v1/settings/source-preferences') {
          return new Response(JSON.stringify(preferences()), { status: 200 });
        }
        if (url.pathname === '/api/v1/settings/library-layout') {
          return new Response(
            JSON.stringify({ template: '', default: '', example: '', isDefault: true }),
            { status: 200 }
          );
        }
        if (url.pathname === '/api/v1/library/tags') {
          return new Response(
            JSON.stringify({
              status: 'idle',
              written: 0,
              unchanged: 0,
              skipped: 0,
              skippedAmbiguous: 0,
              skippedUnproved: 0,
              failed: 0,
              eligible: 0
            }),
            { status: 200 }
          );
        }
        if (url.pathname === '/api/v1/library/transcode') {
          return new Response(
            JSON.stringify({ status: 'idle', transcoded: 0, skipped: 0, failed: 0, eligible: 0 }),
            { status: 200 }
          );
        }
        // No run has ever been planned; `null` is what "nothing to show" reads
        // as here, unlike the other cards' shapes above.
        if (url.pathname === '/api/v1/library/layout/moves') {
          return new Response('null', { status: 200 });
        }
        return new Response(JSON.stringify({}), { status: 200 });
      })
    );
  });

  afterEach(() => {
    cleanup();
    vi.unstubAllGlobals();
  });

  it('reads the stored value into the switch', async () => {
    duplicatesStored = { enabled: true };
    openedLibrary();

    const checkbox = (await duplicatesSwitch()) as HTMLInputElement;
    expect(checkbox.checked).toBe(true);
  });

  it('saves the switch that was set', async () => {
    openedLibrary();

    const checkbox = await duplicatesSwitch();
    await fireEvent.click(checkbox);
    const card = checkbox.closest('form');
    if (!card) throw new Error('the switch is not on a form');
    await fireEvent.click(within(card).getByRole('button', { name: 'Save' }));

    await vi.waitFor(() => expect(duplicatesSaved).not.toBeNull());
    expect((duplicatesSaved as { enabled: boolean }).enabled).toBe(true);
  });

  it('disables the switch while the save is in flight', async () => {
    let release: () => void = () => {};
    saveGate = new Promise((resolve) => (release = resolve));
    openedLibrary();

    const checkbox = (await duplicatesSwitch()) as HTMLInputElement;
    // Save is gated on the form differing from what was loaded, so the switch
    // has to actually move before there is anything to save.
    await fireEvent.click(checkbox);
    const card = checkbox.closest('form');
    if (!card) throw new Error('the switch is not on a form');
    fireEvent.click(within(card).getByRole('button', { name: 'Save' }));

    await vi.waitFor(() => expect(checkbox.disabled).toBe(true));

    release();
    await vi.waitFor(() => expect(checkbox.disabled).toBe(false));
  });

  it('shows an error when the save is refused', async () => {
    saveShouldFail = true;
    openedLibrary();

    const checkbox = await duplicatesSwitch();
    await fireEvent.click(checkbox);
    const card = checkbox.closest('form');
    if (!card) throw new Error('the switch is not on a form');
    await fireEvent.click(within(card).getByRole('button', { name: 'Save' }));

    expect(await screen.findByText('The setting was not saved.')).toBeTruthy();
  });

  it('renders without a live toggle when the setting is unavailable on the server', async () => {
    duplicatesGetStatus = 503;
    openedLibrary();

    await screen.findByText('This setting is not available on this server.');
    expect(
      screen.queryByRole('checkbox', {
        name: /delete proven duplicates automatically|leave every duplicate for you to decide/
      })
    ).toBeNull();
  });
});

// The three categories the component renders. Jobs is a separate component,
// and Matching folded into Library, so what is left here is Sources, Library
// and Automation. Every query fires regardless of which category is open —
// they are all declared at the top of the component — so what tells a
// category apart is which cards it draws from what has loaded, not which
// requests it makes.
describe('what each category shows', () => {
  beforeEach(() => {
    vi.stubGlobal(
      'fetch',
      vi.fn(async (input: RequestInfo | URL) => {
        const url = new URL(String(input), 'http://localhost');
        if (url.pathname === '/api/v1/settings/source-preferences') {
          return new Response(JSON.stringify(preferences()), { status: 200 });
        }
        // The same shapes the Library-only cards need in every other block
        // above, so their effects load without crashing here too.
        if (url.pathname === '/api/v1/settings/library-layout') {
          return new Response(
            JSON.stringify({ template: '', default: '', example: '', isDefault: true }),
            { status: 200 }
          );
        }
        if (url.pathname === '/api/v1/library/tags') {
          return new Response(
            JSON.stringify({
              status: 'idle',
              written: 0,
              unchanged: 0,
              skipped: 0,
              skippedAmbiguous: 0,
              skippedUnproved: 0,
              failed: 0,
              eligible: 0
            }),
            { status: 200 }
          );
        }
        if (url.pathname === '/api/v1/library/transcode') {
          return new Response(
            JSON.stringify({ status: 'idle', transcoded: 0, skipped: 0, failed: 0, eligible: 0 }),
            { status: 200 }
          );
        }
        if (url.pathname === '/api/v1/library/layout/moves') {
          return new Response('null', { status: 200 });
        }
        return new Response(JSON.stringify({}), { status: 200 });
      })
    );
  });

  afterEach(() => {
    cleanup();
    vi.unstubAllGlobals();
  });

  function opened(show: 'sources' | 'library' | 'automation') {
    const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
    return render(SettingsSections, { show } as never, {
      wrapper: QueryClientProvider,
      wrapperProps: { client }
    });
  }

  it('the sources category shows only its own cards', async () => {
    opened('sources');

    // Each card resolves its own query on its own tick, so every expected
    // heading is awaited rather than read the instant the first one lands.
    expect(await screen.findByRole('heading', { name: 'slskd' })).toBeTruthy();
    expect(await screen.findByRole('heading', { name: 'Navidrome' })).toBeTruthy();
    expect(await screen.findByRole('heading', { name: 'Spotify' })).toBeTruthy();
    expect(await screen.findByRole('heading', { name: 'ListenBrainz' })).toBeTruthy();
    expect(screen.queryByRole('heading', { name: 'Formats' })).toBeNull();
    expect(screen.queryByRole('heading', { name: 'Weekly playlist' })).toBeNull();
    expect(screen.queryByRole('heading', { name: 'Verify imports by audio' })).toBeNull();
  });

  // Matching folded into Library: the one card it held, verifying an import
  // by its audio, sits beside the other settings that decide what the
  // collection keeps.
  it('the library category shows its own cards and the audio check', async () => {
    opened('library');

    expect(await screen.findByRole('heading', { name: 'Formats' })).toBeTruthy();
    expect(await screen.findByRole('heading', { name: 'Upgrade low-quality files' })).toBeTruthy();
    expect(await screen.findByRole('heading', { name: 'Verify imports by audio' })).toBeTruthy();
    expect(await screen.findByRole('heading', { name: 'Library layout' })).toBeTruthy();
    expect(await screen.findByRole('heading', { name: 'Migrate library' })).toBeTruthy();
    expect(await screen.findByRole('heading', { name: 'Tags in your files' })).toBeTruthy();
    expect(await screen.findByRole('heading', { name: 'Resolve duplicates' })).toBeTruthy();
    expect(screen.queryByRole('heading', { name: 'slskd' })).toBeNull();
    expect(screen.queryByRole('heading', { name: 'Weekly playlist' })).toBeNull();
  });

  it('the automation category shows only its own cards', async () => {
    opened('automation');

    expect(await screen.findByRole('heading', { name: 'Weekly playlist' })).toBeTruthy();
    expect(await screen.findByRole('heading', { name: 'New releases' })).toBeTruthy();
    expect(await screen.findByRole('heading', { name: 'Notifications' })).toBeTruthy();
    expect(screen.queryByRole('heading', { name: 'slskd' })).toBeNull();
    expect(screen.queryByRole('heading', { name: 'Formats' })).toBeNull();
    expect(screen.queryByRole('heading', { name: 'Verify imports by audio' })).toBeNull();
  });
});

// A read that fails is not the same fact as a read that came back empty: the
// server answering "0 B" or "not configured" is a fact about the collection,
// and the server not answering at all is not. Before this, both looked the
// same on screen.
describe('a settings read that fails', () => {
  function withRejected(paths: string[]) {
    vi.stubGlobal(
      'fetch',
      vi.fn(async (input: RequestInfo | URL) => {
        const url = new URL(String(input), 'http://localhost');
        if (paths.includes(url.pathname)) {
          return new Response(JSON.stringify({ title: 'unauthorized' }), { status: 401 });
        }
        // The other cards sharing the same screen, so their own effects load
        // without crashing while the one under test is left failing.
        if (url.pathname === '/api/v1/settings/source-preferences') {
          return new Response(JSON.stringify(preferences()), { status: 200 });
        }
        if (url.pathname === '/api/v1/settings/library-layout') {
          return new Response(
            JSON.stringify({ template: '', default: '', example: '', isDefault: true }),
            { status: 200 }
          );
        }
        if (url.pathname === '/api/v1/library/tags') {
          return new Response(
            JSON.stringify({
              status: 'idle',
              written: 0,
              unchanged: 0,
              skipped: 0,
              skippedAmbiguous: 0,
              skippedUnproved: 0,
              failed: 0,
              eligible: 0
            }),
            { status: 200 }
          );
        }
        if (url.pathname === '/api/v1/library/transcode') {
          return new Response(
            JSON.stringify({ status: 'idle', transcoded: 0, skipped: 0, failed: 0, eligible: 0 }),
            { status: 200 }
          );
        }
        if (url.pathname === '/api/v1/library/layout/moves') {
          return new Response('null', { status: 200 });
        }
        return new Response(JSON.stringify({}), { status: 200 });
      })
    );
  }

  afterEach(() => {
    cleanup();
    vi.unstubAllGlobals();
  });

  function opened(show: 'sources' | 'library' | 'automation') {
    const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
    return render(SettingsSections, { show } as never, {
      wrapper: QueryClientProvider,
      wrapperProps: { client }
    });
  }

  it('shows Unavailable for the library facts instead of 0 B or never run', async () => {
    withRejected(['/api/v1/library', '/api/v1/library/storage']);
    opened('library');

    expect((await screen.findAllByText('Unavailable')).length).toBeGreaterThan(0);
    expect(screen.queryByText('0 B')).toBeNull();
    expect(screen.queryByText('never run')).toBeNull();
    expect(screen.queryByText('not set')).toBeNull();
  });

  it('does not say Not configured for a notification connection it could not read', async () => {
    withRejected(['/api/v1/settings/notifications']);
    opened('automation');

    const heading = await screen.findByRole('heading', { name: 'Notifications' });
    const card = heading.closest('div')?.parentElement;
    if (!card) throw new Error('notifications card not found');
    expect(await within(card).findByText('Unavailable')).toBeTruthy();
    expect(within(card).queryByText('Not configured')).toBeNull();
  });

  it('disables Save on the slskd form once the settings read has failed', async () => {
    withRejected(['/api/v1/settings/slskd']);
    opened('sources');

    const heading = await screen.findByRole('heading', { name: 'slskd' });
    const form = heading.parentElement?.querySelector('form');
    if (!form) throw new Error('slskd form not found');
    await vi.waitFor(() =>
      expect(
        (within(form).getByRole('button', { name: 'Save' }) as HTMLButtonElement).disabled
      ).toBe(true)
    );
  });
});

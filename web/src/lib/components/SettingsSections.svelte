<script lang="ts">
  import type { Snippet } from 'svelte';
  import { createMutation, createQuery, useQueryClient } from '@tanstack/svelte-query';
  import Icon from '$lib/components/Icon.svelte';
  import { page } from '$app/state';
  import { untrack } from 'svelte';
  import {
    api,
    ApiError,
    type DuplicateResolutionSettings,
    type ImportSettings,
    type LibraryLayout,
    type LibraryLayoutRun,
    type LibraryMove,
    type ListenBrainzSettings,
    type NewReleasesSettings,
    type NotificationSettings,
    type NavidromeSettings,
    type SlskdSettings,
    type Storage,
    type SourcePreferences,
    type SpotifySettings,
    type UpgradeSettings,
    type WeeklySettings
  } from '$lib/api';
  import { formatBytes, sinceTime } from '$lib/utils';
  import { checkIsStale, failedThisWeek, type SourceName } from '$lib/source-health';
  import Button from '$lib/components/Button.svelte';
  import Chip from '$lib/components/Chip.svelte';
  import FormGrid from '$lib/components/FormGrid.svelte';
  import * as Select from '$lib/components/ui/select';
  import ErrorNote from '$lib/components/ErrorNote.svelte';
  import Settle from '$lib/components/Settle.svelte';
  import Skeleton from '$lib/components/Skeleton.svelte';
  import SettingsSaveButton from '$lib/components/SettingsSaveButton.svelte';
  import StatusBadge from '$lib/components/StatusBadge.svelte';
  import StateMark from '$lib/components/StateMark.svelte';
  import TranscodeSettings from '$lib/components/TranscodeSettings.svelte';
  import InboxCleanup from '$lib/components/InboxCleanup.svelte';

  type Role = 'ok' | 'idle' | 'busy' | 'fail';

  // Which category of settings is on screen. The sections are all wired here
  // rather than split across separate files because they are not independent:
  // the two status cards summarise the sections around them, read from the
  // same queries those sections are saved through, so a stale summary cannot
  // disagree with what it summarises. Splitting the wiring would mean either
  // duplicating those queries or passing them down, and both are ways of
  // letting the two drift apart. What is split is the page — each route
  // renders its own category and nothing else.
  //
  // Matching used to be a category of its own, for the one card that verifies
  // an import by its audio. It is folded into Library here, next to the other
  // settings that decide what the collection keeps.
  let { show }: { show: 'sources' | 'library' | 'automation' } = $props();

  const queryClient = useQueryClient();
  // Each settings route renders its own instance, so the category is fixed
  // for the life of this component.
  const showing = untrack(() => show);
  const settings = createQuery({ queryKey: ['slskd-settings'], queryFn: api.slskdSettings });
  const imports = createQuery({ queryKey: ['import-settings'], queryFn: api.importSettings });
  const spotify = createQuery({ queryKey: ['spotify-settings'], queryFn: api.spotifySettings });
  const navidrome = createQuery({
    queryKey: ['navidrome-settings'],
    queryFn: api.navidromeSettings
  });
  const listenbrainz = createQuery({
    queryKey: ['listenbrainz-settings'],
    queryFn: api.listenBrainzSettings
  });

  // The release count is the Library page's own figure, read under the key
  // that page reads it by. The dashboard's album count is every catalogue
  // release, followed artists' missing ones included, and read 11,853 beside
  // a library of 3,352.
  const libraryReleases = createQuery({
    queryKey: ['releases', 'library-head'],
    queryFn: () => api.releases({ scope: 'library', limit: 8 }),
    enabled: showing === 'library'
  });

  // A passing connection check says a service answers. The jobs that use it
  // say whether the work went through, so Sources counts their failures too.
  const failedJobs = createQuery({
    queryKey: ['jobs-failed'],
    queryFn: api.failedJobs,
    enabled: showing === 'sources'
  });
  const failures = (source: SourceName) => failedThisWeek($failedJobs.data, source);

  const library = createQuery({
    queryKey: ['library'],
    queryFn: api.library,
    // A running scan announces its progress; this is the fallback for a stream
    // that never arrived.
    refetchInterval: (query) =>
      ['queued', 'running'].includes(query.state.data?.scanStatus ?? '') ? 15_000 : false
  });

  // What the collection weighs and what the discs holding it have left. Read
  // where it is rather than kept: a stored figure would be wrong the first time
  // anything else on the machine wrote a file. A scan changes the weight, so
  // this follows the scan rather than a clock.
  const storage = createQuery({
    queryKey: ['library-storage'],
    queryFn: api.storage,
    refetchInterval: (query) => (query.state.data ? false : 15_000)
  });

  // Why a folder could not be measured, in words rather than in the operating
  // system's. Schall asks Linux how much room a folder's disc has; Linux answers
  // a failure with the name of a C error code — `no such file or directory`,
  // `permission denied` — and that sentence was being drawn on the screen as it
  // stood. It reads as a fault in Schall, and it names no folder and no action.
  //
  // Three causes cover every case worth a sentence: the folder is gone, Schall
  // may not look inside it, or the path is a file. Anything else keeps the
  // general answer. The exact words are kept on the row's title either way, so
  // an unusual cause is still readable by hovering.
  //
  // Three of them are faults and one is not, and the row is marked accordingly.
  // A folder that is gone, one Schall may not open, and a path that turns out to
  // be a file are all the same thing to somebody using Schall: the library
  // folder named in the settings is not usable, and nothing will be scanned into
  // it or written out of it. Drawn in the same grey as "not linked", that read as
  // a fact about a feature nobody had turned on. Only the case with no known
  // cause stays grey, because a reading Schall could not take is not the same as
  // a folder Schall can say is wrong.
  function unmeasured(error: string | undefined): { detail: string; role: Role } {
    const cause = (error ?? '').toLowerCase();
    if (cause.includes('no such file')) return { detail: 'folder not found', role: 'fail' };
    if (cause.includes('permission denied')) {
      return { detail: 'no permission to read', role: 'fail' };
    }
    if (cause.includes('not a directory')) return { detail: 'not a folder', role: 'fail' };
    return { detail: "couldn't measure", role: 'idle' };
  }

  // One line per disc. A disc that could not be measured says so and shows no
  // figures at all, because a number nothing measured is one somebody plans
  // around.
  const discs = $derived(
    ($storage.data?.volumes ?? []).map((volume) => {
      const name = volume.roots.length === 1 ? volume.roots[0] : volume.roots.join(', ');
      if (!volume.measured) {
        const said = unmeasured(volume.error);
        return {
          name,
          role: said.role,
          detail: said.detail,
          title: volume.error
        };
      }
      const used = volume.totalBytes - volume.freeBytes;
      const share = volume.totalBytes > 0 ? used / volume.totalBytes : 0;
      return {
        name,
        role: (share >= 0.95 ? 'fail' : share >= 0.85 ? 'busy' : 'ok') as Role,
        detail: `${formatBytes(volume.freeBytes)} free of ${formatBytes(volume.totalBytes)}`
      };
    })
  );

  let baseUrl = $state('');
  let apiKey = $state('');
  let enabled = $state(true);
  let searchTimeoutSeconds = $state(20);
  let loaded = $state(false);

  // What was last loaded or saved, to tell a form somebody is still editing
  // from one nobody has touched since. The API key is never in it: it is
  // never returned, so an empty field always means "keep the stored key"
  // rather than "unchanged since load".
  let settingsBaseline = $state<{
    baseUrl: string;
    enabled: boolean;
    searchTimeoutSeconds: number;
  } | null>(null);

  // Load the stored values once. The API key is never returned, so the field
  // stays empty and an empty submission keeps the stored key.
  $effect(() => {
    const data = $settings.data;
    if (!data || loaded) return;
    baseUrl = data.baseUrl;
    enabled = data.enabled;
    searchTimeoutSeconds = data.searchTimeoutSeconds || 20;
    settingsBaseline = { baseUrl, enabled, searchTimeoutSeconds };
    loaded = true;
  });

  const settingsDirty = $derived(
    settingsBaseline
      ? baseUrl !== settingsBaseline.baseUrl ||
          enabled !== settingsBaseline.enabled ||
          searchTimeoutSeconds !== settingsBaseline.searchTimeoutSeconds ||
          apiKey.trim() !== ''
      : undefined
  );

  const save = createMutation({
    mutationFn: () => api.saveSlskdSettings({ baseUrl, apiKey, enabled, searchTimeoutSeconds }),
    onSuccess: async (data: SlskdSettings) => {
      apiKey = '';
      settingsBaseline = { baseUrl, enabled, searchTimeoutSeconds };
      queryClient.setQueryData(['slskd-settings'], data);
    }
  });

  const test = createMutation({
    mutationFn: api.testSlskdConnection,
    onSuccess: (data: SlskdSettings) => queryClient.setQueryData(['slskd-settings'], data)
  });

  // Import settings are saved as a whole, so changing the retention policy
  // cannot silently switch the acoustic check off and vice versa.
  const saveRetention = createMutation({
    mutationFn: (input: {
      retention?: ImportSettings['sourceRetention'];
      apiKey?: string;
      enabled?: boolean;
    }) =>
      api.saveImportSettings(
        input.retention ?? retention,
        {
          apiKey: input.apiKey ?? '',
          enabled: input.enabled ?? acoustidEnabled
        },
        // Not this card's business, so it is carried through unchanged: a
        // press here must not reset the re-encoding policy saved on the
        // other one.
        {
          enabled: $imports.data?.transcodeEnabled,
          target: $imports.data?.transcodeTarget,
          bitrate: $imports.data?.transcodeBitrate,
          when: $imports.data?.transcodeWhen
        }
      ),
    onSuccess: (data: ImportSettings) => {
      acoustidKey = '';
      queryClient.setQueryData(['import-settings'], data);
    }
  });

  // The stored key is never sent back, so the field starts empty and an empty
  // field means "leave it alone" rather than "erase it".
  let acoustidKey = $state('');
  const acoustidEnabled = $derived(Boolean($imports.data?.acoustidEnabled));
  const acoustidKeySet = $derived(Boolean($imports.data?.acoustidKeySet));

  const canTest = $derived(Boolean($settings.data?.configured && $settings.data?.enabled));
  const retention = $derived($imports.data?.sourceRetention ?? 'keep');
  // Removal is offered only where it could be carried out. An inbox nobody may
  // write to is a reason to explain the option, not to present it as a choice.
  const canDeleteSources = $derived(Boolean($imports.data?.inboxWritable));

  // Untested, testing, connected and failed are four different things, and the
  // difference between the first and the last is the whole point of the button.
  const connection = $derived.by((): { role: Role; label: string } => {
    if ($test.isPending) return { role: 'busy', label: 'Testing…' };
    if ($settings.isError) return { role: 'idle', label: 'Unavailable' };
    if (!$settings.data?.configured) return { role: 'idle', label: 'Not configured' };
    if ($settings.data.connectionStatus === 'ok') return { role: 'ok', label: 'Connected' };
    if ($settings.data.connectionStatus === 'failed') return { role: 'fail', label: 'Failed' };
    return { role: 'idle', label: 'Untested' };
  });

  // The status row's state word is coloured only for the two states worth
  // colouring: a source answering and a source refusing. Untested and
  // testing are as ordinary as a row gets.
  const stateColors: Record<Role, string> = {
    ok: 'text-ok',
    idle: 'text-ink-2',
    busy: 'text-ink-2',
    fail: 'text-fail'
  };

  function ago(verb: string, value: string | null | undefined) {
    // Relative, like every other time in the product. The absolute form read
    // "checked 8/3/2026, 4:33:20 PM" — second-level precision on a fact whose
    // whole meaning is "recently or not".
    // Past only: a check the server stamped a second ahead of this clock read
    // "checked in 0s".
    const when = sinceTime(value);
    return when ? `${verb} ${when}` : `never ${verb}`;
  }

  // What a refused file move says to the person who asked for it.
  //
  // A move is Schall renaming one file into the folder shape the template
  // describes. When one is refused the server hands back whatever failed, and
  // for the commonest refusal that was a line of Postgres:
  // `ERROR: duplicate key value violates unique constraint
  // "downloads_local_path_idx" (SQLSTATE 23505)`. That names a database index.
  // It does not name the file, it does not say the path was already taken, and
  // there is nothing on screen to press — which made it the one place in the
  // product a reader could arrive at and have no next move.
  //
  // So the known refusals are given a sentence about the file and what to do
  // about it. The server's own words are never thrown away: they go behind a
  // disclosure, where somebody writing a bug report can still reach them and
  // nobody else has to read them first.
  function explainMove(said: string): string {
    const text = said.toLocaleLowerCase();
    if (text.includes('duplicate key') || text.includes('23505')) {
      return 'Another file is at that path. Rename or remove it, then plan again.';
    }
    if (text.includes('permission denied') || text.includes('read-only')) {
      return 'The folder is read-only. Mount it writable, then plan again.';
    }
    if (text.includes('no space left') || text.includes('enospc')) {
      return 'The disk is full. Free some space, then plan again.';
    }
    if (text.includes('no such file') || text.includes('enoent')) {
      return 'The file has moved or gone. Scan the library, then plan again.';
    }
    if (text.includes('exists')) {
      return 'That path is taken. Rename or remove what is there, then plan again.';
    }
    return 'File not moved.';
  }

  // --- Status ---
  //
  // Two short readouts, one per category, each answered by queries that
  // category's own cards already load — so a stale readout cannot disagree
  // with the cards below it. This used to be one card mixing library facts
  // and connection facts under a single heading; split so each sits with the
  // cards it reports on.

  const scanning = $derived(['queued', 'running'].includes($library.data?.scanStatus ?? ''));
  const rootCount = $derived($library.data?.rootCount ?? 0);
  const scanned = $derived(($library.data?.scanCompletedAt ?? null) !== null);

  // Each list only reports what Schall can actually observe. A row it cannot
  // check says so rather than showing a reassuring tick.
  //
  // `title` is the longer version of the reading, for the two rows whose reading
  // is a shortening of something recorded elsewhere. It is what the hover shows,
  // and it is absent on every row whose reading is already the whole answer.
  type HealthRow = {
    name: string;
    role: Role;
    detail: string;
    title?: string;
    placeholder?: boolean;
  };

  const libraryHealth = $derived<HealthRow[]>([
    {
      name: 'Library path',
      role: ($library.isError ? 'idle' : rootCount > 0 ? 'ok' : 'idle') as Role,
      detail: $library.isError
        ? 'Unavailable'
        : rootCount === 0
          ? 'not set'
          : `${rootCount} ${rootCount === 1 ? 'folder' : 'folders'}`
    },
    {
      name: 'Collection size',
      role: ($library.isError ? 'idle' : 'ok') as Role,
      detail: $library.isError ? 'Unavailable' : formatBytes($library.data?.totalSizeBytes ?? 0)
    },
    // One row per disc. The list is keyed by name, so a second disc carries
    // its folder in the name rather than colliding with the first. A storage
    // read that failed outright has no volumes to report on, so it draws one
    // row rather than none.
    ...($storage.isError
      ? [{ name: 'Room left', role: 'idle' as Role, detail: 'Unavailable' }]
      : discs.map((disc) => ({
          name: discs.length === 1 ? 'Room left' : `Room left · ${disc.name}`,
          role: disc.role,
          detail: disc.detail
        }))),
    {
      name: 'Last scan',
      role: ($library.isError
        ? 'idle'
        : scanning
          ? 'busy'
          : $library.data?.scanStatus === 'failed'
            ? 'fail'
            : scanned
              ? 'ok'
              : 'idle') as Role,
      // A failed scan showed the error the scanner recorded, which is a Go
      // sentence written for a log. The row says the short fact and keeps the
      // recorded words on the hover, the same way an unmeasured disc does.
      detail: $library.isError
        ? 'Unavailable'
        : scanning
          ? 'running'
          : $library.data?.scanStatus === 'failed'
            ? 'last scan failed'
            : scanned
              ? since($library.data?.scanCompletedAt ?? null)
              : 'never run',
      title:
        $library.data?.scanStatus === 'failed' ? ($library.data?.scanError ?? undefined) : undefined
    }
  ]);

  // The status rows at the top of Sources: a dot, the name, a coloured state
  // word and a dim detail, one row per service.
  type SourceStatusRow = {
    name: string;
    role: Role;
    state: string;
    detail: string;
    failed: number;
  };

  const sourceStatusRows = $derived<SourceStatusRow[]>([
    {
      name: 'slskd',
      role: ($settings.isError
        ? 'idle'
        : !$settings.data?.configured
          ? 'idle'
          : $settings.data.connectionStatus === 'failed'
            ? 'fail'
            : $settings.data.connectionStatus === 'ok'
              ? 'ok'
              : 'idle') as Role,
      state: $settings.isError
        ? 'Unavailable'
        : !$settings.data?.configured
          ? 'Not configured'
          : $settings.data.connectionStatus === 'failed'
            ? 'Failed'
            : $settings.data.connectionStatus === 'ok'
              ? 'Connected'
              : 'Untested',
      detail: $settings.isError
        ? "couldn't read"
        : !$settings.data?.configured
          ? 'not linked'
          : ago('checked', $settings.data?.lastCheckedAt),
      failed: failures('slskd')
    },
    {
      name: 'Navidrome',
      role: ($navidrome.isError
        ? 'idle'
        : !$navidrome.data?.configured
          ? 'idle'
          : $navidrome.data.connectionStatus === 'failed'
            ? 'fail'
            : $navidrome.data.connectionStatus === 'ok'
              ? 'ok'
              : 'idle') as Role,
      state: $navidrome.isError
        ? 'Unavailable'
        : !$navidrome.data?.configured
          ? 'Not configured'
          : $navidrome.data.connectionStatus === 'failed'
            ? 'Failed'
            : $navidrome.data.connectionStatus === 'ok'
              ? 'Connected'
              : 'Untested',
      // Navidrome's own fact is not whether it was last checked, it is
      // whether it was last told about new music — the whole reason this
      // section exists.
      detail: $navidrome.isError
        ? "couldn't read"
        : !$navidrome.data?.configured
          ? 'not linked'
          : `${ago('checked', $navidrome.data?.lastCheckedAt)} · ${ago('notified', $navidrome.data?.lastNotifiedAt)}`,
      failed: failures('Navidrome')
    },
    {
      name: 'ListenBrainz',
      role: ($listenbrainz.isError
        ? 'idle'
        : !$listenbrainz.data?.configured
          ? 'idle'
          : $listenbrainz.data.connectionStatus === 'failed'
            ? 'fail'
            : $listenbrainz.data.connectionStatus === 'ok'
              ? 'ok'
              : 'idle') as Role,
      state: $listenbrainz.isError
        ? 'Unavailable'
        : !$listenbrainz.data?.configured
          ? 'Not configured'
          : $listenbrainz.data.connectionStatus === 'failed'
            ? 'Failed'
            : $listenbrainz.data.connectionStatus === 'ok'
              ? 'Connected'
              : 'Not checked',
      detail: $listenbrainz.isError
        ? "couldn't read"
        : !$listenbrainz.data?.configured
          ? 'not linked'
          : ago('checked', $listenbrainz.data?.lastCheckedAt),
      failed: failures('ListenBrainz')
    },
    // Spotify has no test to run. Its line says when Schall last talked to
    // it, which is when the stored state was last true.
    {
      name: 'Spotify',
      role: ($spotify.isError || !$spotify.data?.configured || !$spotify.data.connected
        ? 'idle'
        : $spotify.data.connectionStatus === 'failed'
          ? 'fail'
          : 'ok') as Role,
      state: $spotify.isError
        ? 'Unavailable'
        : !$spotify.data?.configured
          ? 'Not configured'
          : !$spotify.data.connected
            ? 'Not connected'
            : $spotify.data.connectionStatus === 'failed'
              ? 'Failed'
              : 'Connected',
      detail: $spotify.data?.configured ? ago('used', $spotify.data?.lastCheckedAt) : 'not linked',
      failed: failures('Spotify')
    }
  ]);

  // While the reads are out, the rows the answer will have, named, with a
  // placeholder where each reading goes. A disc is assumed, the common case.
  // Thirty blank rows that collapsed to four moved the whole page up.
  const libraryHealthRows = $derived<HealthRow[]>(
    $library.isPending || $storage.isPending
      ? ['Library path', 'Collection size', 'Room left', 'Last scan'].map((name) => ({
          name,
          role: 'idle' as Role,
          detail: '',
          placeholder: true
        }))
      : libraryHealth
  );

  function since(value: string | null) {
    if (!value) return 'never';
    const minutes = Math.round((Date.now() - new Date(value).getTime()) / 60000);
    if (minutes < 1) return 'just now';
    if (minutes < 60) return `${minutes}m ago`;
    const hours = Math.round(minutes / 60);
    return hours < 24 ? `${hours}h ago` : `${Math.round(hours / 24)}d ago`;
  }

  function size(bytes: number) {
    if (bytes <= 0) return '—';
    const units = ['B', 'KB', 'MB', 'GB', 'TB'];
    const power = Math.min(Math.floor(Math.log(bytes) / Math.log(1024)), units.length - 1);
    return `${(bytes / 1024 ** power).toFixed(power > 1 ? 1 : 0)} ${units[power]}`;
  }

  // --- Spotify ---

  let spotifyClientId = $state('');
  let spotifyClientSecret = $state('');
  let spotifyLoaded = $state(false);
  let spotifyBaseline = $state<{ clientId: string } | null>(null);

  // Load the stored client ID once; the secret is never returned, so an empty
  // field means "keep the stored one".
  $effect(() => {
    const data = $spotify.data;
    if (!data || spotifyLoaded) return;
    spotifyClientId = data.clientId;
    spotifyBaseline = { clientId: data.clientId };
    spotifyLoaded = true;
  });

  const spotifyDirty = $derived(
    spotifyBaseline
      ? spotifyClientId !== spotifyBaseline.clientId || spotifyClientSecret.trim() !== ''
      : undefined
  );

  const saveSpotify = createMutation({
    mutationFn: () => api.saveSpotifySettings(spotifyClientId.trim(), spotifyClientSecret.trim()),
    onSuccess: (data: SpotifySettings) => {
      spotifyClientSecret = '';
      spotifyBaseline = { clientId: spotifyClientId };
      queryClient.setQueryData(['spotify-settings'], data);
    }
  });

  // Connecting leaves this page for Spotify's consent screen; the callback
  // lands back here with ?spotify= saying how it went.
  const connectSpotify = createMutation({
    mutationFn: api.authorizeSpotify,
    onSuccess: (data: { url: string }) => {
      window.location.href = data.url;
    }
  });

  const spotifyConnection = $derived.by((): { role: Role; label: string } => {
    if ($connectSpotify.isPending) return { role: 'busy', label: 'Connecting…' };
    if ($spotify.isError) return { role: 'idle', label: 'Unavailable' };
    if (!$spotify.data?.configured) return { role: 'idle', label: 'Not configured' };
    if (!$spotify.data.connected) return { role: 'idle', label: 'Not connected' };
    if ($spotify.data.connectionStatus === 'failed') return { role: 'fail', label: 'Failed' };
    return { role: 'ok', label: 'Connected' };
  });

  // --- Navidrome ---

  let navidromeBaseUrl = $state('');
  let navidromeUsername = $state('');
  let navidromePassword = $state('');
  let navidromeEnabled = $state(true);
  let navidromeLoaded = $state(false);
  let navidromeBaseline = $state<{
    baseUrl: string;
    username: string;
    enabled: boolean;
  } | null>(null);

  // Load the stored values once. The password is never returned, so the field
  // stays empty and an empty submission keeps the stored one.
  $effect(() => {
    const data = $navidrome.data;
    if (!data || navidromeLoaded) return;
    navidromeBaseUrl = data.baseUrl;
    navidromeUsername = data.username;
    navidromeEnabled = data.enabled;
    navidromeBaseline = { baseUrl: data.baseUrl, username: data.username, enabled: data.enabled };
    navidromeLoaded = true;
  });

  const navidromeDirty = $derived(
    navidromeBaseline
      ? navidromeBaseUrl !== navidromeBaseline.baseUrl ||
          navidromeUsername !== navidromeBaseline.username ||
          navidromeEnabled !== navidromeBaseline.enabled ||
          navidromePassword.trim() !== ''
      : undefined
  );

  const saveNavidrome = createMutation({
    mutationFn: () =>
      api.saveNavidromeSettings({
        baseUrl: navidromeBaseUrl.trim(),
        username: navidromeUsername.trim(),
        password: navidromePassword,
        enabled: navidromeEnabled
      }),
    onSuccess: (data: NavidromeSettings) => {
      navidromePassword = '';
      navidromeBaseline = {
        baseUrl: navidromeBaseUrl,
        username: navidromeUsername,
        enabled: navidromeEnabled
      };
      queryClient.setQueryData(['navidrome-settings'], data);
    }
  });

  const testNavidrome = createMutation({
    mutationFn: api.testNavidromeConnection,
    onSuccess: (data: NavidromeSettings) => queryClient.setQueryData(['navidrome-settings'], data)
  });

  // Tells the player right now instead of waiting for whatever change queues
  // it next, or for the player's own schedule.
  const rescanNavidrome = createMutation({
    mutationFn: api.rescanNavidrome,
    onSuccess: (data: NavidromeSettings) => queryClient.setQueryData(['navidrome-settings'], data)
  });

  const canTestNavidrome = $derived(Boolean($navidrome.data?.configured));
  // Only worth pressing once Navidrome is switched on and answering: off, or
  // never reached, there is nobody there to tell.
  const canRescanNavidrome = $derived(
    Boolean($navidrome.data?.enabled && $navidrome.data?.connectionStatus === 'ok')
  );

  const navidromeConnection = $derived.by((): { role: Role; label: string } => {
    if ($testNavidrome.isPending) return { role: 'busy', label: 'Testing…' };
    if ($navidrome.isError) return { role: 'idle', label: 'Unavailable' };
    if (!$navidrome.data?.configured) return { role: 'idle', label: 'Not configured' };
    if ($navidrome.data.connectionStatus === 'ok') return { role: 'ok', label: 'Connected' };
    if ($navidrome.data.connectionStatus === 'failed') return { role: 'fail', label: 'Failed' };
    return { role: 'idle', label: 'Untested' };
  });

  // --- ListenBrainz ---

  let listenbrainzUsername = $state('');
  let listenbrainzToken = $state('');
  let listenbrainzClearToken = $state(false);
  let listenbrainzEnabled = $state(true);
  let listenbrainzLoaded = $state(false);
  let listenbrainzBaseline = $state<{ username: string; enabled: boolean } | null>(null);

  // Load the stored values once. The token is never returned, so the field stays
  // empty and an empty submission keeps the stored one.
  //
  // The addresses and the similarity algorithm are not fields here. The service
  // is hosted, so those have a right answer the API already fills in, and the
  // only real question is which account to read.
  $effect(() => {
    const data = $listenbrainz.data;
    if (!data || listenbrainzLoaded) return;
    listenbrainzUsername = data.username;
    listenbrainzEnabled = data.enabled;
    listenbrainzBaseline = { username: data.username, enabled: data.enabled };
    listenbrainzLoaded = true;
  });

  const listenbrainzDirty = $derived(
    listenbrainzBaseline
      ? listenbrainzUsername !== listenbrainzBaseline.username ||
          listenbrainzEnabled !== listenbrainzBaseline.enabled ||
          listenbrainzToken.trim() !== '' ||
          listenbrainzClearToken
      : undefined
  );

  const saveListenBrainz = createMutation({
    mutationFn: () =>
      api.saveListenBrainzSettings({
        username: listenbrainzUsername.trim(),
        userToken: listenbrainzToken,
        clearUserToken: listenbrainzClearToken,
        similarityAlgorithm: $listenbrainz.data?.similarityAlgorithm ?? '',
        enabled: listenbrainzEnabled
      }),
    onSuccess: (data: ListenBrainzSettings) => {
      listenbrainzToken = '';
      listenbrainzClearToken = false;
      listenbrainzBaseline = { username: listenbrainzUsername, enabled: listenbrainzEnabled };
      queryClient.setQueryData(['listenbrainz-settings'], data);
    }
  });

  const testListenBrainz = createMutation({
    mutationFn: api.testListenBrainzConnection,
    onSuccess: (data: ListenBrainzSettings) =>
      queryClient.setQueryData(['listenbrainz-settings'], data)
  });

  const canTestListenBrainz = $derived(Boolean($listenbrainz.data?.configured));

  // "Connected · checked 64d ago" was a reading from two months back. Opening
  // Sources checks each switched-on service again when its last check is more
  // than an hour old. Each check is one small read (slskd's state, one
  // recommendation, a ping), never a search, and runs once per visit.
  let rechecked = false;
  $effect(() => {
    if (show !== 'sources' || rechecked) return;
    const slskd = $settings.data;
    const player = $navidrome.data;
    const listens = $listenbrainz.data;
    if (!slskd || !player || !listens) return;
    rechecked = true;
    if (slskd.configured && slskd.enabled && checkIsStale(slskd.lastCheckedAt)) $test.mutate();
    if (player.configured && player.enabled && checkIsStale(player.lastCheckedAt)) {
      $testNavidrome.mutate();
    }
    if (listens.configured && listens.enabled && checkIsStale(listens.lastCheckedAt)) {
      $testListenBrainz.mutate();
    }
  });

  const listenbrainzConnection = $derived.by((): { role: Role; label: string } => {
    if ($testListenBrainz.isPending) return { role: 'busy', label: 'Testing…' };
    if ($listenbrainz.isError) return { role: 'idle', label: 'Unavailable' };
    if (!$listenbrainz.data?.configured) return { role: 'idle', label: 'Not configured' };
    if ($listenbrainz.data.connectionStatus === 'ok') return { role: 'ok', label: 'Connected' };
    if ($listenbrainz.data.connectionStatus === 'failed') return { role: 'fail', label: 'Failed' };
    return { role: 'idle', label: 'Not checked' };
  });

  // --- Format preferences ---
  //
  // What Schall should prefer among the copies peers are sharing, and what it
  // must never fetch. It decides only what gets tried; the audio still decides
  // what is admitted, and no preference has ever deleted anything.

  const preferences = createQuery({
    queryKey: ['source-preferences'],
    queryFn: api.sourcePreferences
  });

  let preferred = $state<string[]>([]);
  let unacceptable = $state<string[]>([]);
  let bitRateFloor = $state(0);
  // A floor per format, as typed. An empty field is no floor, which is why this
  // holds the string as well as the number: clearing a field must not read as
  // zero the moment the last digit goes.
  let formatFloors = $state<Record<string, number | ''>>({});
  let preferencesLoaded = $state(false);
  let addFormat = $state('');
  let preferencesBaseline = $state('');

  // The four fields together, in one comparable string. The ranking, the
  // refusal list and every per-format floor all move together when Save is
  // pressed, so there is no per-field notion of "changed" worth keeping
  // separate from "the form differs from what was loaded".
  function preferencesSnapshot() {
    return JSON.stringify({ preferred, unacceptable, bitRateFloor, formatFloors });
  }

  $effect(() => {
    const data = $preferences.data;
    if (!data || preferencesLoaded) return;
    preferred = [...data.preferred];
    unacceptable = [...data.unacceptable];
    bitRateFloor = data.minimumBitRate;
    formatFloors = Object.fromEntries(
      Object.keys(data.lossy).map((format) => [format, data.formatMinimumBitRate[format] ?? ''])
    );
    preferencesBaseline = preferencesSnapshot();
    preferencesLoaded = true;
  });

  const preferencesDirty = $derived(
    preferencesLoaded ? preferencesSnapshot() !== preferencesBaseline : undefined
  );

  // What is left to add: every format the server knows, minus the ones already
  // named on either list. A format cannot be both preferred and refused.
  const addable = $derived(
    ($preferences.data?.known ?? []).filter(
      (format) => !preferred.includes(format) && !unacceptable.includes(format)
    )
  );

  // The formats left to add, written as choices. The first is the state the
  // picker starts in — nothing added yet — and the picker needs the list twice:
  // once to draw the rows, once to answer a letter typed while the list is shut.
  // The formats that can take a floor, in the order the server lists its known
  // formats, so the fields do not move about between loads.
  const floorFormats = $derived(
    ($preferences.data?.known ?? []).filter((format) => format in ($preferences.data?.lossy ?? {}))
  );

  const knownFormats = $derived($preferences.data?.known ?? []);

  const addableChoices = $derived([
    { value: '', label: 'Add a format…' },
    ...addable.map((format) => ({ value: format, label: format.toUpperCase() }))
  ]);

  const notifyKinds: { value: 'ntfy' | 'webhook'; label: string }[] = [
    { value: 'ntfy', label: 'ntfy' },
    { value: 'webhook', label: 'A webhook' }
  ];

  function moveFormat(from: number, to: number) {
    if (to < 0 || to >= preferred.length) return;
    const moved = [...preferred];
    [moved[from], moved[to]] = [moved[to], moved[from]];
    preferred = moved;
  }

  function refuse(format: string, refused: boolean) {
    unacceptable = refused
      ? [...unacceptable, format]
      : unacceptable.filter((named) => named !== format);
  }

  const savePreferences = createMutation({
    mutationFn: () =>
      api.saveSourcePreferences({
        preferred,
        unacceptable,
        minimumBitRate: Number(bitRateFloor) || 0,
        // An empty field is no floor. It is sent as zero rather than left out,
        // because leaving it out would keep the floor that was there before.
        formatMinimumBitRate: Object.fromEntries(
          Object.entries(formatFloors).map(([format, floor]) => [format, Number(floor) || 0])
        )
      }),
    onSuccess: (data: SourcePreferences) => {
      preferencesBaseline = preferencesSnapshot();
      queryClient.setQueryData(['source-preferences'], data);
    }
  });

  // --- Upgrade low-quality files ---
  //
  // The bit-rate floor above already refuses a candidate under it; this looks
  // back at what the library obtained before that floor existed, or before it
  // was raised, and asks the ordinary acquisition loop for a better copy.

  const upgrades = createQuery({ queryKey: ['upgrade-settings'], queryFn: api.upgradeSettings });

  let upgradesEnabled = $state(false);
  let upgradesLoaded = $state(false);
  let upgradesBaseline = $state<boolean | null>(null);

  $effect(() => {
    const data = $upgrades.data;
    if (!data || upgradesLoaded) return;
    upgradesEnabled = data.enabled;
    upgradesBaseline = data.enabled;
    upgradesLoaded = true;
  });

  const upgradesDirty = $derived(
    upgradesBaseline === null ? undefined : upgradesEnabled !== upgradesBaseline
  );

  const saveUpgrades = createMutation({
    mutationFn: () => api.saveUpgradeSettings(upgradesEnabled),
    onSuccess: (data: UpgradeSettings) => {
      upgradesBaseline = upgradesEnabled;
      queryClient.setQueryData(['upgrade-settings'], data);
    }
  });

  const scanForUpgrades = createMutation({
    mutationFn: api.scanForUpgrades,
    onSuccess: () => queryClient.invalidateQueries({ queryKey: ['upgrade-settings'] })
  });

  // --- Notifications ---
  //
  // The review queue is the one screen where a person answers what nothing could
  // decide, and it can only be answered quickly if somebody knows it has
  // something in it. This is where they say where to be told.

  const notifications = createQuery({
    queryKey: ['notification-settings'],
    queryFn: api.notificationSettings
  });

  let notifyKind = $state<'ntfy' | 'webhook'>('ntfy');
  let notifyEndpoint = $state('');
  let notifyToken = $state('');
  let notifyClearToken = $state(false);
  let notifyEnabled = $state(false);
  let notifyLoaded = $state(false);
  let notifyBaseline = $state<{
    kind: 'ntfy' | 'webhook';
    endpoint: string;
    enabled: boolean;
  } | null>(null);

  // Load the stored values once. The token is never returned, so the field stays
  // empty and an empty submission keeps the stored one.
  $effect(() => {
    const data = $notifications.data;
    if (!data || notifyLoaded) return;
    notifyKind = data.kind;
    notifyEndpoint = data.endpoint;
    notifyEnabled = data.enabled;
    notifyBaseline = { kind: data.kind, endpoint: data.endpoint, enabled: data.enabled };
    notifyLoaded = true;
  });

  const notifyDirty = $derived(
    notifyBaseline
      ? notifyKind !== notifyBaseline.kind ||
          notifyEndpoint !== notifyBaseline.endpoint ||
          notifyEnabled !== notifyBaseline.enabled ||
          notifyToken.trim() !== '' ||
          notifyClearToken
      : undefined
  );

  const saveNotifications = createMutation({
    mutationFn: () =>
      api.saveNotificationSettings({
        kind: notifyKind,
        endpoint: notifyEndpoint.trim(),
        token: notifyToken,
        clearToken: notifyClearToken,
        enabled: notifyEnabled
      }),
    onSuccess: (data: NotificationSettings) => {
      notifyToken = '';
      notifyClearToken = false;
      notifyBaseline = { kind: notifyKind, endpoint: notifyEndpoint, enabled: notifyEnabled };
      queryClient.setQueryData(['notification-settings'], data);
    }
  });

  const testNotification = createMutation({
    mutationFn: api.testNotification,
    onSuccess: (data: NotificationSettings) =>
      queryClient.setQueryData(['notification-settings'], data)
  });

  const canTestNotification = $derived(Boolean($notifications.data?.endpoint));

  const notificationConnection = $derived.by((): { role: Role; label: string } => {
    if ($testNotification.isPending) return { role: 'busy', label: 'Sending…' };
    if ($notifications.isError) return { role: 'idle', label: 'Unavailable' };
    if (!$notifications.data?.endpoint) return { role: 'idle', label: 'Not configured' };
    if (!$notifications.data.enabled) return { role: 'idle', label: 'Off' };
    if ($notifications.data.connectionStatus === 'ok') return { role: 'ok', label: 'Delivering' };
    if ($notifications.data.connectionStatus === 'failed') return { role: 'fail', label: 'Failed' };
    return { role: 'idle', label: 'Not checked' };
  });

  // --- The weekly playlist ---
  //
  // It sits beside Navidrome and ListenBrainz because those two are what it is
  // made of: ListenBrainz says what to fetch, and a star in the player is the
  // signal that keeps a song. It ships off, and the mode decides whether a
  // refresh ever deletes anything.

  const weekly = createQuery({ queryKey: ['weekly'], queryFn: api.weekly });

  let weeklyEnabled = $state(false);
  let weeklySongs = $state(20);
  let weeklyMode = $state<WeeklySettings['mode']>('report');
  let weeklyLibraryShare = $state(0);
  let weeklyOnePerArtist = $state(false);
  let weeklyLoaded = $state(false);
  let weeklyBaseline = $state('');

  function weeklySnapshot() {
    return JSON.stringify({
      weeklyEnabled,
      weeklySongs: Number(weeklySongs),
      weeklyMode,
      weeklyLibraryShare: Number(weeklyLibraryShare),
      weeklyOnePerArtist
    });
  }

  $effect(() => {
    const data = $weekly.data?.settings;
    if (!data || weeklyLoaded) return;
    weeklyEnabled = data.enabled;
    weeklySongs = data.songsPerWeek;
    weeklyMode = data.mode;
    weeklyLibraryShare = data.libraryShare;
    weeklyOnePerArtist = data.onePerArtist;
    weeklyBaseline = weeklySnapshot();
    weeklyLoaded = true;
  });

  const weeklyDirty = $derived(weeklyLoaded ? weeklySnapshot() !== weeklyBaseline : undefined);

  // Saving with the feature on asks for a refresh straight away, and a refresh
  // changes the list and the runs the Playlists screen reads. So the whole
  // overview is invalidated rather than written over with the answer.
  const saveWeekly = createMutation({
    mutationFn: () =>
      api.saveWeeklySettings({
        enabled: weeklyEnabled,
        songsPerWeek: Number(weeklySongs),
        mode: weeklyMode,
        libraryShare: Number(weeklyLibraryShare),
        onePerArtist: weeklyOnePerArtist
      }),
    onSuccess: () => {
      weeklyBaseline = weeklySnapshot();
      queryClient.invalidateQueries({ queryKey: ['weekly'] });
    }
  });

  // --- The new-releases playlist ---
  //
  // What following an artist or a label buys, as something to press play on.
  // It moves playlist rows only — no file, no want and no decision is in
  // reach of it — so unlike the weekly playlist it ships on and there is
  // nothing here to consent to before it happens.

  const newReleases = createQuery({ queryKey: ['new-releases'], queryFn: api.newReleases });

  let newReleasesEnabled = $state(true);
  let newReleasesWindow = $state(90);
  let newReleasesLoaded = $state(false);
  let newReleasesBaseline = $state<{ enabled: boolean; windowDays: number } | null>(null);

  $effect(() => {
    const data = $newReleases.data?.settings;
    if (!data || newReleasesLoaded) return;
    newReleasesEnabled = data.enabled;
    newReleasesWindow = data.windowDays;
    newReleasesBaseline = { enabled: data.enabled, windowDays: data.windowDays };
    newReleasesLoaded = true;
  });

  const newReleasesDirty = $derived(
    newReleasesBaseline
      ? newReleasesEnabled !== newReleasesBaseline.enabled ||
          Number(newReleasesWindow) !== newReleasesBaseline.windowDays
      : undefined
  );

  const saveNewReleases = createMutation({
    mutationFn: () =>
      api.saveNewReleasesSettings({
        enabled: newReleasesEnabled,
        windowDays: Number(newReleasesWindow)
      }),
    onSuccess: () => {
      newReleasesBaseline = { enabled: newReleasesEnabled, windowDays: Number(newReleasesWindow) };
      queryClient.invalidateQueries({ queryKey: ['new-releases'] });
    }
  });

  // --- Library layout ---

  const layout = createQuery({ queryKey: ['library-layout'], queryFn: api.libraryLayout });

  // Watched only while files are still moving; SSE on the library topic is what
  // usually brings the news, and this is the safety net behind it.
  const layoutRun = createQuery({
    queryKey: ['library-layout-run'],
    queryFn: api.libraryLayoutRun,
    refetchInterval: (query) => (query.state.data?.active ? 15_000 : false)
  });

  let template = $state('');
  let templateLoaded = $state(false);
  let templateBaseline = $state<string | null>(null);

  // Load the stored template once, so typing is never overwritten by a refetch.
  $effect(() => {
    const data = $layout.data;
    if (!data || templateLoaded) return;
    template = data.template;
    templateBaseline = data.template;
    templateLoaded = true;
  });

  const templateDirty = $derived(
    templateBaseline === null ? undefined : template.trim() !== templateBaseline
  );

  // Saving a template moves nothing: the library stays exactly where it is until
  // a migration is planned and applied.
  const saveLayout = createMutation({
    mutationFn: () => api.saveLibraryLayout(template.trim()),
    onSuccess: (data: LibraryLayout) => {
      templateBaseline = template.trim();
      queryClient.setQueryData(['library-layout'], data);
    }
  });

  const planLayout = createMutation({
    mutationFn: api.planLibraryLayoutRun,
    onSuccess: (data: LibraryLayoutRun) => queryClient.setQueryData(['library-layout-run'], data)
  });

  const applyLayout = createMutation({
    mutationFn: (runId: string) => api.applyLibraryLayoutRun(runId),
    onSuccess: (data: LibraryLayoutRun) => queryClient.setQueryData(['library-layout-run'], data)
  });

  const discardLayout = createMutation({
    mutationFn: (runId: string) => api.discardLibraryLayoutRun(runId),
    onSuccess: (data: LibraryLayoutRun | undefined) =>
      queryClient.setQueryData(['library-layout-run'], data)
  });

  const run = $derived($layoutRun.data);
  const layoutBusy = $derived(
    $planLayout.isPending || $applyLayout.isPending || $discardLayout.isPending
  );
  const migration = $derived.by((): { role: Role; label: string } => {
    if ($planLayout.isPending) return { role: 'busy', label: 'Planning…' };
    if ($applyLayout.isPending) return { role: 'busy', label: 'Starting…' };
    if ($layoutRun.isError) return { role: 'idle', label: 'Unavailable' };
    if (!run) return { role: 'idle', label: 'Never migrated' };
    if (run.active) return { role: 'idle', label: 'Planned' };
    if (run.failed > 0) return { role: 'fail', label: `${run.failed} failed` };
    return { role: 'ok', label: 'Migrated' };
  });

  // A row badge says what became of that one file, which is not what the run as
  // a whole is doing.
  const moveRoles: Record<LibraryMove['status'], Role> = {
    moved: 'ok',
    planned: 'idle',
    failed: 'fail',
    reverted: 'idle'
  };

  // --- Tags in the files ---

  const tags = createQuery({
    queryKey: ['library-tags'],
    queryFn: api.libraryTags,
    // A running pass announces itself over the event stream; this is the
    // fallback for a stream that never arrived.
    refetchInterval: (query) =>
      ['queued', 'running'].includes(query.state.data?.status ?? '') ? 15_000 : false
  });

  const writeTags = createMutation({
    mutationFn: api.writeLibraryTags,
    onSuccess: async () => {
      await queryClient.invalidateQueries({ queryKey: ['library-tags'] });
    }
  });

  const writing = $derived(['queued', 'running'].includes($tags.data?.status ?? ''));
  const tagged = $derived.by((): { role: Role; label: string } => {
    if (writing || $writeTags.isPending) return { role: 'busy', label: 'Writing' };
    if ($tags.isError) return { role: 'idle', label: 'Unavailable' };
    if ($tags.data?.status === 'failed') return { role: 'fail', label: 'Failed' };
    if ($tags.data?.status === 'completed') return { role: 'ok', label: 'Written' };
    return { role: 'idle', label: 'Never run' };
  });
  // What the pass has got through, which is only worth a line while it is
  // running or once it has run.
  const tagsDone = $derived(
    ($tags.data?.written ?? 0) +
      ($tags.data?.unchanged ?? 0) +
      ($tags.data?.skipped ?? 0) +
      ($tags.data?.failed ?? 0)
  );

  // --- Duplicate resolution ---
  //
  // The duplicates screen answers one recording at a time. This lets Schall
  // settle them itself: only a copy proven the same audio as another, or one
  // copy proven better than every other. There is no scan route. Saving with
  // `enabled: true` queues the sweep server-side by itself.

  const duplicates = createQuery({
    queryKey: ['duplicate-resolution-settings'],
    queryFn: api.duplicateResolutionSettings
  });

  let duplicatesEnabled = $state(false);
  let duplicatesLoaded = $state(false);
  let duplicatesBaseline = $state<boolean | null>(null);

  $effect(() => {
    const data = $duplicates.data;
    if (!data || duplicatesLoaded) return;
    duplicatesEnabled = data.enabled;
    duplicatesBaseline = data.enabled;
    duplicatesLoaded = true;
  });

  const duplicatesDirty = $derived(
    duplicatesBaseline === null ? undefined : duplicatesEnabled !== duplicatesBaseline
  );

  const saveDuplicates = createMutation({
    mutationFn: () => api.saveDuplicateResolutionSettings(duplicatesEnabled),
    onSuccess: (data: DuplicateResolutionSettings) => {
      duplicatesBaseline = duplicatesEnabled;
      queryClient.setQueryData(['duplicate-resolution-settings'], data);
    }
  });

  // A 503 means the feature is not wired on this server, so retrying the
  // request cannot answer differently.
  const duplicatesUnavailable = $derived(
    $duplicates.isError && $duplicates.error instanceof ApiError && $duplicates.error.status === 503
  );

  // What the OAuth callback redirect reported, worth one sentence and only
  // until the next navigation.
  const callbackResult = $derived(page.url.searchParams.get('spotify'));
  const callbackNotice = $derived.by(() => {
    switch (callbackResult) {
      case 'connected':
        return null; // the connected chip already says it
      case 'denied':
        return 'Spotify access was denied.';
      case 'state-mismatch':
        return "Spotify's reply didn't match. Connect again.";
      case 'failed':
        return "Couldn't connect Spotify. Check the client ID and secret, then try again.";
    }
    return null;
  });
</script>

<div class="flex max-w-[880px] flex-col">
  {#if show === 'sources'}
    <div class="flex flex-col">
      {@render SourcesStatus()}
      {@render Slskd()}
      {@render Navidrome()}
      {@render Spotify()}
      {@render ListenBrainz()}
    </div>
  {/if}

  {#if show === 'library'}
    <div class="flex flex-col gap-3.5">
      {@render LibraryStatus()}
      {@render FormatPreferences()}
      {@render UpgradeLowQuality()}

      <TranscodeSettings />

      {@render Acoustid()}
      {@render Retention()}
      <InboxCleanup />

      {@render LibraryLayoutSection()}
      {@render LibraryMigration()}
      {@render FileTagsSection()}
      {@render DuplicateResolution()}

      <!-- Retention and the audio check are saved together, so a failure of
           either is reported here, where both cards live. -->
      {#if $saveRetention.isError}
        <ErrorNote error={$saveRetention.error} action="Press Save again." />
      {/if}
    </div>
  {/if}

  {#if show === 'automation'}
    <div class="flex flex-col gap-3.5">
      {@render Weekly()}
      {@render NewReleases()}
      {@render Notifications()}
    </div>
  {/if}
</div>

{#snippet StatusRows(rows: HealthRow[])}
  <!-- Hairlines and space rather than a panel: these rows are a set of
       readings that belong together, not a surface anything acts on, and a
       bordered box here would be the second border on this section. -->
  <div class="flex max-h-[calc(100dvh-18rem)] flex-col overflow-hidden">
    {#each rows as entry, index (entry.name || `placeholder-${index}`)}
      <div class="flex items-center gap-2.5 border-b border-rule py-2">
        <StateMark role={entry.role} />
        <!-- The name never gives ground. A flex child is allowed to shrink to
             its content by default, and the reading beside it does not wrap,
             so on a phone the name was the only thing that could give: slskd
             was drawn as three lines of two letters. The name is what the row
             is about, so the reading is the one that shortens now. -->
        <span
          class="shrink-0 text-body font-medium {entry.role === 'idle'
            ? 'text-ink-2'
            : 'text-ink'}"
        >
          {entry.name}
        </span>
        {#if entry.placeholder}
          <Skeleton class="ml-auto h-3 w-20" />
        {:else}
          <span
            class="ml-auto min-w-0 truncate text-meta font-medium text-ink-3"
            title={entry.title}
          >
            {entry.detail}
          </span>
        {/if}
      </div>
    {/each}
  </div>
{/snippet}

{#snippet SourcesStatus()}
  <!-- No card and no heading: this is the answer to "is Schall talking to
       anything", read in one glance before the forms below it say how. -->
  <div class="flex flex-col pb-6">
    {#each sourceStatusRows as entry (entry.name)}
      <!-- Below `sm` there is no room for a dot, a name column and a state
           column on one line, so the state moves in beside the name (it is
           short) and the detail — the one thing here that runs long — drops
           to a line of its own. `sm` and up are the original four cells in a
           row, unchanged. -->
      <div class="flex flex-col gap-0.5 border-b border-rule py-2.5 @xl:flex-row @xl:items-center @xl:gap-3.5">
        <div class="flex min-w-0 items-center gap-3.5">
          <StateMark role={entry.role} />
          <span class="min-w-0 flex-1 truncate text-body text-ink @xl:w-[8.125rem] @xl:flex-none">
            {entry.name}
          </span>
          <span class="shrink-0 text-body font-medium @xl:hidden {stateColors[entry.role]}">
            {entry.state}
          </span>
        </div>
        <span class="hidden shrink-0 text-body font-medium @xl:block @xl:w-[6.875rem] {stateColors[entry.role]}">
          {entry.state}
        </span>
        <span class="min-w-0 truncate text-meta text-ink-3 @xl:flex-1">{entry.detail}</span>
        {#if entry.failed > 0}
          <a href="/settings/jobs" class="shrink-0 text-meta font-medium text-fail hover:underline">
            {entry.failed.toLocaleString()} failed this week
          </a>
        {/if}
      </div>
    {/each}
  </div>
{/snippet}

<!-- The label column of a source form: 130px, a `<label>` where an id gives it
     something to point at, a plain span for the checkbox rows below it (whose
     own `<label>` already wraps the control and would read its name twice). -->
{#snippet SourceLabel(text: string, id: string)}
  {#if id}
    <label for={id} class="text-meta font-medium text-ink">{text}</label>
  {:else}
    <span class="text-meta font-medium text-ink">{text}</span>
  {/if}
{/snippet}

<!-- The third column: dim help text beside the field rather than under the
     label, as the board draws it. Always rendered, even empty, so the grid's
     three columns stay in step from row to row. -->
{#snippet SourceHint(text: string, id = '')}
  <span id={id ? `${id}-hint` : undefined} class="text-pretty text-meta leading-snug text-ink-3">
    {text}
  </span>
{/snippet}

{#snippet LibraryStatus()}
  <section class="flex flex-col gap-3.5 pb-9">
    <div class="flex flex-wrap items-center gap-2.5 border-b border-rule pb-1.5">
      <h2 class="label">Status</h2>
    </div>

    {#if $library.isError || $storage.isError || $libraryReleases.isError}
      <ErrorNote
        error={$library.error ?? $storage.error ?? $libraryReleases.error}
        retry={() => {
          if ($library.isError) $library.refetch();
          if ($storage.isError) $storage.refetch();
          if ($libraryReleases.isError) $libraryReleases.refetch();
        }}
        fallback="Couldn't load the library."
      />
    {/if}

    {@render StatusRows(libraryHealthRows)}

    <div class="flex flex-col gap-1">
      <div class="flex items-baseline justify-between">
        <span class="label">Library storage</span>
        <!-- A placeholder while the reads are out: "0 B" and "0 files" are
             answers, and wrong ones. -->
        {#if $library.isPending}
          <Skeleton class="h-3 w-14" />
        {:else}
          <span class="numeric text-meta font-medium text-ink-2">
            {$library.isError ? 'Unavailable' : size($library.data?.totalSizeBytes ?? 0)}
          </span>
        {/if}
      </div>
      {#if $library.isPending || $libraryReleases.isPending}
        <Skeleton class="h-3 w-36" />
      {:else}
        <span class="numeric text-meta font-medium text-ink-4">
          {#if $library.isError}
            Unavailable
          {:else}
            {($library.data?.fileCount ?? 0).toLocaleString()} files ·
            {$libraryReleases.data
              ? `${$libraryReleases.data.total.toLocaleString()} releases`
              : 'releases unavailable'}
          {/if}
        </span>
      {/if}
    </div>
  </section>
{/snippet}

{#snippet Slskd()}
  <!-- Connected or not is already said in the status rows above, so the form
       carries only what changes it: the fields, and Test and Save beside
       each other, indented under the field column. -->
  <section class="flex flex-col gap-3 pb-9">
    <div class="border-b border-rule pb-1.5"><h2 class="label">slskd</h2></div>

    <form
      class="grid grid-cols-[minmax(0,1fr)] items-center gap-x-3.5 gap-y-1.5 @xl:grid-cols-[130px_340px_minmax(0,1fr)]"
      onsubmit={(event) => {
        event.preventDefault();
        $save.mutate();
      }}
    >
      {@render SourceLabel('API URL', 'slskd-api-url')}
      <input
        id="slskd-api-url"
        aria-describedby="slskd-api-url-hint"
        bind:value={baseUrl}
        type="url"
        required
        disabled={!$settings.isSuccess}
        placeholder="http://slskd:5030"
        class="field w-full"
      />
      {@render SourceHint("slskd's address", 'slskd-api-url')}

      {@render SourceLabel('API key', 'slskd-api-key')}
      <input
        id="slskd-api-key"
        bind:value={apiKey}
        type="password"
        autocomplete="off"
        disabled={!$settings.isSuccess}
        placeholder={$settings.data?.apiKeySet ? 'stored' : 'slskd API key'}
        class="field w-full"
      />
      {@render SourceHint('')}

      {@render SourceLabel('Search timeout', 'slskd-search-timeout')}
      <div class="flex items-center gap-2">
        <input
          id="slskd-search-timeout"
          aria-describedby="slskd-search-timeout-hint"
          bind:value={searchTimeoutSeconds}
          type="number"
          min="5"
          max="120"
          disabled={!$settings.isSuccess}
          class="field w-20"
        />
        <span class="text-meta text-ink-4">seconds</span>
      </div>
      {@render SourceHint('how long to wait for peers', 'slskd-search-timeout')}

      {@render SourceLabel('Enabled', '')}
      <label class="flex w-fit cursor-pointer items-center gap-2 text-meta font-medium text-ink-3">
        <input
          type="checkbox"
          bind:checked={enabled}
          disabled={!$settings.isSuccess}
          class="check cursor-pointer"
        />
        {enabled ? 'searching enabled' : 'searching off'}
      </label>
      {@render SourceHint('off stops all searches')}

      <span class="hidden @xl:block"></span>
      <div class="flex flex-wrap items-center gap-2 pt-1">
        <Button
          type="button"
          variant="outline"
          size="sm"
          disabled={!canTest || $test.isPending || !$settings.isSuccess}
          onclick={() => $test.mutate()}
        >
          {#if $test.isPending}<Icon name="busy" size={12} class="animate-spin" />{/if}
          Test
        </Button>
        <SettingsSaveButton
          variant="primary"
          pending={$save.isPending}
          disabled={!$settings.isSuccess}
          saved={$save.isSuccess}
          dirty={settingsDirty}
        />
      </div>
    </form>

    {#if $settings.isError}
      <StatusBadge border={false} wrap>
        <ErrorNote
          bare
          error={$settings.error}
          retry={() => $settings.refetch()}
          fallback="Couldn't load settings. Retry before saving."
        />
      </StatusBadge>
    {:else if connection.role === 'fail' || $save.isError || $test.isError}
      <StatusBadge border={false} wrap>
        <ErrorNote
          bare
          error={$save.error ?? $test.error ?? $settings.data?.connectionError}
          fallback="Couldn't save or check slskd."
        />
      </StatusBadge>
    {:else if connection.role === 'ok' && $settings.data?.connectionDetail}
      <span class="text-meta text-ink-3">{$settings.data.connectionDetail}</span>
    {:else if connection.role === 'idle'}
      <span class="text-meta text-ink-3">
        {$settings.data?.configured
          ? 'press Test to check the connection'
          : 'add the address and API key'}
      </span>
    {/if}
  </section>
{/snippet}

{#snippet Navidrome()}
  <section class="flex flex-col gap-3 pb-9">
    <div class="border-b border-rule pb-1.5"><h2 class="label">Navidrome</h2></div>

    <form
      class="grid grid-cols-[minmax(0,1fr)] items-center gap-x-3.5 gap-y-1.5 @xl:grid-cols-[130px_340px_minmax(0,1fr)]"
      onsubmit={(event) => {
        event.preventDefault();
        $saveNavidrome.mutate();
      }}
    >
      {@render SourceLabel('Server URL', 'navidrome-url')}
      <input
        id="navidrome-url"
        aria-describedby="navidrome-url-hint"
        bind:value={navidromeBaseUrl}
        type="url"
        required
        disabled={!$navidrome.isSuccess}
        placeholder="http://navidrome:4533"
        class="field w-full"
      />
      {@render SourceHint("Navidrome's address", 'navidrome-url')}

      {@render SourceLabel('Username', 'navidrome-username')}
      <input
        id="navidrome-username"
        aria-describedby="navidrome-username-hint"
        bind:value={navidromeUsername}
        type="text"
        required
        disabled={!$navidrome.isSuccess}
        class="field w-full"
      />
      {@render SourceHint('must be allowed to start a scan', 'navidrome-username')}

      {@render SourceLabel('Password', 'navidrome-password')}
      <input
        id="navidrome-password"
        bind:value={navidromePassword}
        type="password"
        autocomplete="off"
        disabled={!$navidrome.isSuccess}
        placeholder={$navidrome.data?.passwordSet ? 'stored' : 'Navidrome password'}
        class="field w-full"
      />
      {@render SourceHint('')}

      {@render SourceLabel('Enabled', '')}
      <label class="flex w-fit cursor-pointer items-center gap-2 text-meta font-medium text-ink-3">
        <input
          type="checkbox"
          bind:checked={navidromeEnabled}
          disabled={!$navidrome.isSuccess}
          class="check cursor-pointer"
        />
        {navidromeEnabled ? 'notify of new music' : 'off'}
      </label>
      {@render SourceHint('off stops all contact')}

      <span class="hidden @xl:block"></span>
      <div class="flex flex-wrap items-center gap-2 pt-1">
        <Button
          type="button"
          variant="outline"
          size="sm"
          disabled={!canTestNavidrome || $testNavidrome.isPending || !$navidrome.isSuccess}
          onclick={() => $testNavidrome.mutate()}
        >
          {#if $testNavidrome.isPending}<Icon name="busy" size={12} class="animate-spin" />{/if}
          Test
        </Button>
        <Button
          type="button"
          variant="outline"
          size="sm"
          disabled={!canRescanNavidrome || $rescanNavidrome.isPending || !$navidrome.isSuccess}
          onclick={() => $rescanNavidrome.mutate()}
        >
          {#if $rescanNavidrome.isPending}<Icon name="busy" size={12} class="animate-spin" />{/if}
          Rescan now
        </Button>
        <SettingsSaveButton
          variant="primary"
          pending={$saveNavidrome.isPending}
          disabled={!$navidrome.isSuccess}
          saved={$saveNavidrome.isSuccess}
          dirty={navidromeDirty}
        />
      </div>
    </form>

    {#if $navidrome.isError}
      <StatusBadge border={false} wrap>
        <ErrorNote
          bare
          error={$navidrome.error}
          retry={() => $navidrome.refetch()}
          fallback="Couldn't load settings. Retry before saving."
        />
      </StatusBadge>
    {:else if navidromeConnection.role === 'fail' || $saveNavidrome.isError || $testNavidrome.isError || $rescanNavidrome.isError}
      <StatusBadge border={false} wrap>
        <ErrorNote
          bare
          error={$saveNavidrome.error ??
            $testNavidrome.error ??
            $rescanNavidrome.error ??
            $navidrome.data?.connectionError}
          fallback="Couldn't save or check Navidrome."
        />
      </StatusBadge>
    {:else if navidromeConnection.role === 'ok' && $navidrome.data?.connectionDetail}
      <span class="text-meta text-ink-3">{$navidrome.data.connectionDetail}</span>
    {:else if navidromeConnection.role === 'idle'}
      <span class="text-meta text-ink-3">
        {$navidrome.data?.configured
          ? 'press Test to check the connection'
          : 'add the address and an account'}
      </span>
    {/if}
  </section>
{/snippet}

{#snippet FormatPreferences()}
  <section class="flex flex-col gap-3.5 pb-9">
    <div class="flex flex-wrap items-center gap-2.5 border-b border-rule pb-1.5">
      <h2 class="label">Formats</h2>
      <span class="text-meta font-medium text-ink-3">
        preferred and blocked formats
      </span>
    </div>

    {#if $preferences.isError}
      <ErrorNote
        error={$preferences.error}
        retry={() => $preferences.refetch()}
        fallback="Couldn't load settings. Retry before saving."
      />
    {/if}

    <FormGrid
      onsubmit={(event) => {
        event.preventDefault();
        $savePreferences.mutate();
      }}
    >
      {@render Label('Prefer', 'best first; unlisted formats follow')}
      <!-- An order, drawn as one. The list used to be a column of rows carrying
           three word buttons each, which read as a form to fill in rather than
           as a ranking; the ranking is the whole content of this setting. Chips
           left to right are the order, and the arrows move a chip along it, so
           the direction on the control is the direction on the screen. -->
      <div class="flex flex-col gap-2">
        <!-- The default sentence describes a saved setting, so it waits for the
             read; shown early it claimed an order nobody had loaded yet. -->
        {#if $preferences.isPending}
          <Skeleton class="h-3.5 w-60" />
        {:else if preferred.length === 0}
          <span class="text-meta text-ink-3">Lossless first, then lossy by bitrate.</span>
        {/if}
        {#if preferred.length > 0}
          <div class="flex flex-wrap items-center gap-1.5">
            {#each preferred as format, position (format)}
              <span
                class="inline-flex items-center gap-1 border border-rule bg-panel py-1 pl-2.5 pr-1"
              >
                <span class="numeric text-micro text-ink-3">{position + 1}</span>
                <span class="text-meta font-medium uppercase text-ink">{format}</span>
                <button
                  type="button"
                  class="tap grid size-5 place-items-center text-ink-3 transition hover:text-ink disabled:pointer-events-none disabled:opacity-50"
                  aria-label="Move {format.toUpperCase()} up"
                  disabled={!$preferences.isSuccess || position === 0}
                  onclick={() => moveFormat(position, position - 1)}
                >
                  <Icon name="chevron-left" size={13} />
                </button>
                <button
                  type="button"
                  class="tap grid size-5 place-items-center text-ink-3 transition hover:text-ink disabled:pointer-events-none disabled:opacity-50"
                  aria-label="Move {format.toUpperCase()} down"
                  disabled={!$preferences.isSuccess || position === preferred.length - 1}
                  onclick={() => moveFormat(position, position + 1)}
                >
                  <Icon name="chevron-right" size={13} />
                </button>
                <button
                  type="button"
                  class="tap grid size-5 place-items-center text-ink-3 transition hover:text-fail"
                  aria-label="Remove {format.toUpperCase()}"
                  disabled={!$preferences.isSuccess}
                  onclick={() => (preferred = preferred.filter((named) => named !== format))}
                >
                  <Icon name="close" size={13} />
                </button>
              </span>
            {/each}
          </div>
        {/if}
        <div class="flex items-center gap-2">
          <Select.Root type="single" items={addableChoices} bind:value={addFormat}>
            <Select.Trigger
              class="w-40"
              aria-label="Format to add"
              disabled={!$preferences.isSuccess}
            >
              {addableChoices.find((choice) => choice.value === addFormat)?.label}
            </Select.Trigger>
            <Select.Content>
              {#each addableChoices as choice (choice.value)}
                <Select.Item value={choice.value} label={choice.label}>{choice.label}</Select.Item>
              {/each}
            </Select.Content>
          </Select.Root>
          <Button
            variant="outline"
            size="sm"
            disabled={!$preferences.isSuccess || !addFormat}
            onclick={() => {
              if (!addFormat) return;
              preferred = [...preferred, addFormat];
              addFormat = '';
            }}
          >
            Add
          </Button>
        </div>
      </div>

      {@render Label('Never fetch', 'copies in these are skipped')}
      <!-- A grid of columns, not a wrapped line. The names are three, four and
           five characters long, so a wrapped line put every box at a different
           distance from the left and the fourteen of them read as a paragraph
           of words rather than as a list somebody ticks down. A column of a
           fixed width puts every box under the one above it. -->
      <Settle pending={$preferences.isPending}>
        {#snippet placeholder()}
          <div
            class="grid max-w-[34rem] grid-cols-[repeat(auto-fill,minmax(5.25rem,1fr))] gap-x-3 gap-y-2"
            role="status"
            aria-busy="true"
            aria-label="Loading formats"
          >
            {#each Array(8) as _, index (index)}
              <span class="flex items-center gap-2"><Skeleton class="size-4" /><Skeleton class="h-3 w-10" /></span>
            {/each}
          </div>
        {/snippet}
        <div
          class="grid max-h-[calc(100dvh-18rem)] max-w-[34rem] grid-cols-[repeat(auto-fill,minmax(5.25rem,1fr))] gap-x-3 gap-y-2 overflow-hidden"
        >
          {#each knownFormats as format (format)}
            <label class="flex cursor-pointer items-center gap-2 text-meta font-medium text-ink-3">
              <input
                type="checkbox"
                checked={$preferences.isSuccess && unacceptable.includes(format)}
                disabled={!$preferences.isSuccess || preferred.includes(format)}
                onchange={(event) => refuse(format, event.currentTarget.checked)}
                class="check cursor-pointer"
              />
              {format.toUpperCase()}
            </label>
          {/each}
        </div>
      </Settle>

      {@render Label('Lowest bitrate', '0 for none; lossless is exempt', 'bitrate-floor')}
      <div class="flex items-center gap-2">
        <input
          id="bitrate-floor"
          aria-describedby="bitrate-floor-hint"
          bind:value={bitRateFloor}
          type="number"
          min="0"
          max="3000"
          step="32"
          disabled={!$preferences.isSuccess}
          class="field w-28"
        />
        <span class="text-meta text-ink-3">kbps</span>
      </div>

      <!-- One number cannot say what people want: 320 is the top of MP3 and out
           of reach for Opus. A format with its own number is held to that one
           and to nothing else. -->
      {@render Label('Per format', 'overrides the lowest bitrate')}
      <div class="flex flex-col gap-2">
        <Settle pending={$preferences.isPending}>
          {#snippet placeholder()}
            <div class="flex flex-wrap items-center gap-x-4 gap-y-2" role="status" aria-busy="true" aria-label="Loading format floors">
              {#each Array(4) as _, index (index)}
                <span class="flex items-center gap-1.5"><Skeleton class="h-3 w-11" /><Skeleton class="h-8 w-20" /></span>
              {/each}
            </div>
          {/snippet}
        <div class="flex max-h-[calc(100dvh-18rem)] flex-wrap items-center gap-x-4 gap-y-2 overflow-hidden">
          {#each floorFormats as format (format)}
            <label class="flex items-center gap-1.5 text-meta text-ink-2">
              <span class="w-11">{format.toUpperCase()}</span>
              <input
                aria-label={`Lowest bit rate for ${format.toUpperCase()}`}
                bind:value={formatFloors[format]}
                type="number"
                min="0"
                max={$preferences.data?.lossy?.[format] ?? 3000}
                step="16"
                placeholder="—"
                disabled={!$preferences.isSuccess}
                class="field numeric w-20"
              />
            </label>
          {/each}
        </div>
        </Settle>
        <details class="group">
          <summary
            class="tap-tall flex w-fit cursor-pointer list-none items-center gap-1.5 text-meta text-ink-3 transition hover:text-ink-2 [&::-webkit-details-marker]:hidden"
          >
            <Icon name="chevron-right" size={12} class="transition-transform group-open:rotate-90" />
            How a bit rate is read
          </summary>
          <div class="reveal mt-1 flex flex-col gap-1.5 text-meta leading-[1.5] text-ink-3">
            <span>A variable bit rate counts as its average.</span>
            <span>
              A copy with no stated bit rate fails any minimum. Search results show how many were
              skipped.
            </span>
            <span>This sets what is downloaded. Transcode sets what is stored.</span>
          </div>
        </details>
      </div>

      <span class="hidden @xl:block"></span>
      <div class="flex flex-wrap items-center gap-2 pt-1">
        <SettingsSaveButton
          pending={$savePreferences.isPending}
          disabled={!$preferences.isSuccess}
          saved={$savePreferences.isSuccess}
          dirty={preferencesDirty}
        />
        {#if $savePreferences.isError}
          <span class="text-meta font-medium text-fail">
            {($savePreferences.error as Error).message}
          </span>
        {/if}
      </div>
    </FormGrid>
  </section>
{/snippet}

{#snippet UpgradeLowQuality()}
  <section class="flex flex-col gap-3.5 pb-9">
    <div class="flex flex-wrap items-baseline gap-2.5 border-b border-rule pb-1.5">
      <h2 class="label">Upgrade low-quality files</h2>
    </div>

    {#if $upgrades.isError}
      <ErrorNote
        error={$upgrades.error}
        retry={() => $upgrades.refetch()}
        fallback="Couldn't load settings. Retry before saving."
      />
    {/if}

    <span class="text-meta leading-snug text-ink-2">
      Find better copies of files below the lowest bitrate.
    </span>

    <FormGrid
      onsubmit={(event) => {
        event.preventDefault();
        $saveUpgrades.mutate();
      }}
    >
      {@render Label('Enabled', 'off leaves every file as it is')}
      <label class="flex w-fit cursor-pointer items-center gap-2 text-meta font-medium text-ink-3">
        <input
          type="checkbox"
          bind:checked={upgradesEnabled}
          disabled={!$upgrades.isSuccess}
          class="check cursor-pointer"
        />
        {upgradesEnabled ? 'look for better copies' : 'leave files as they are'}
      </label>

      {@render Label('Below the minimum', 'files that qualify now')}
      <span class="text-meta text-ink-3">
        {#if $upgrades.isError}
          Unavailable
        {:else}
          {$upgrades.data?.belowFloor ?? 0}
          {$upgrades.data?.belowFloor === 1 ? 'file' : 'files'}
        {/if}
      </span>

      <span class="hidden @xl:block"></span>
      <div class="flex flex-wrap items-center gap-2 pt-1">
        <SettingsSaveButton
          pending={$saveUpgrades.isPending}
          disabled={!$upgrades.isSuccess}
          saved={$saveUpgrades.isSuccess}
          dirty={upgradesDirty}
        />
        <Button
          type="button"
          variant="outline"
          disabled={$scanForUpgrades.isPending || !$upgrades.isSuccess}
          onclick={() => $scanForUpgrades.mutate()}
        >
          {#if $scanForUpgrades.isPending}
            <Icon name="busy" size={13} class="animate-spin" />
          {/if}
          Scan now
        </Button>
      </div>
    </FormGrid>

    {#if $saveUpgrades.isError}
      <StatusBadge border={false} wrap>
        <ErrorNote
          bare
          error={$saveUpgrades.error}
          fallback="Setting not saved."
          action="Press Save again."
        />
      </StatusBadge>
    {:else if $scanForUpgrades.isError}
      <StatusBadge border={false} wrap>
        <ErrorNote
          bare
          error={$scanForUpgrades.error}
          fallback="Couldn't start the scan."
          action="Try again."
        />
      </StatusBadge>
    {:else}
      <span class="text-meta text-ink-4">a file is replaced only once the new copy is verified</span>
    {/if}
  </section>
{/snippet}

{#snippet Notifications()}
  <section class="flex flex-col gap-3.5 pb-9">
    <div class="flex flex-wrap items-center gap-2.5 border-b border-rule pb-1.5">
      <h2 class="label">Notifications</h2>
      <Chip role={notificationConnection.role}>{notificationConnection.label}</Chip>
      <span class="ml-auto text-meta font-medium text-ink-3">
        {ago('sent', $notifications.data?.lastCheckedAt)}
      </span>
      <Button
        variant="outline"
        size="sm"
        disabled={!canTestNotification || $testNotification.isPending || !$notifications.isSuccess}
        onclick={() => $testNotification.mutate()}
      >
        {#if $testNotification.isPending}<Icon name="busy" size={12} class="animate-spin" />{/if}
        Send test
      </Button>
    </div>

    <FormGrid
      onsubmit={(event) => {
        event.preventDefault();
        $saveNotifications.mutate();
      }}
    >
      {@render Label('Send to', 'ntfy puts it on your phone', 'notify-kind')}
      <Select.Root
        type="single"
        items={notifyKinds}
        value={notifyKind}
        disabled={!$notifications.isSuccess}
        onValueChange={(next) => (notifyKind = next as 'ntfy' | 'webhook')}
      >
        <Select.Trigger id="notify-kind" disabled={!$notifications.isSuccess}>
          {notifyKinds.find((kind) => kind.value === notifyKind)?.label}
        </Select.Trigger>
        <Select.Content>
          {#each notifyKinds as kind (kind.value)}
            <Select.Item value={kind.value} label={kind.label}>{kind.label}</Select.Item>
          {/each}
        </Select.Content>
      </Select.Root>

      {@render Label(
        'Address',
        notifyKind === 'ntfy' ? 'the full topic address' : 'where the JSON is posted',
        'notify-endpoint'
      )}
      <input
        id="notify-endpoint"
        aria-describedby="notify-endpoint-hint"
        bind:value={notifyEndpoint}
        type="url"
        placeholder="https://ntfy.sh/your-topic"
        disabled={!$notifications.isSuccess}
        class="field w-full"
      />

      {@render Label('Token', 'only for a private topic', 'notify-token')}
      <div class="flex flex-col gap-1.5">
        <input
          id="notify-token"
          aria-describedby="notify-token-hint"
          bind:value={notifyToken}
          type="password"
          autocomplete="off"
          disabled={notifyClearToken || !$notifications.isSuccess}
          placeholder={$notifications.data?.tokenSet
            ? 'stored'
            : 'optional'}
          class="field w-full"
        />
        {#if $notifications.data?.tokenSet}
          <label
            class="flex w-fit cursor-pointer items-center gap-2 text-meta font-medium text-ink-3"
          >
            <input
              type="checkbox"
              checked={notifyClearToken}
              disabled={!$notifications.isSuccess}
              onchange={(event) => {
                notifyClearToken = event.currentTarget.checked;
                if (notifyClearToken) notifyToken = '';
              }}
              class="check cursor-pointer"
            />
            clear stored token
          </label>
        {/if}
      </div>

      {@render Label('Enabled', 'off sends nothing')}
      <label class="flex w-fit cursor-pointer items-center gap-2 text-meta font-medium text-ink-3">
        <input
          type="checkbox"
          bind:checked={notifyEnabled}
          disabled={!$notifications.isSuccess}
          class="check cursor-pointer"
        />
        {notifyEnabled ? 'notify me' : 'off'}
      </label>

      <span class="hidden @xl:block"></span>
      <div class="flex flex-wrap items-center gap-2 pt-1">
        <SettingsSaveButton
          pending={$saveNotifications.isPending}
          disabled={!$notifications.isSuccess}
          saved={$saveNotifications.isSuccess}
          dirty={notifyDirty}
        />
      </div>
    </FormGrid>

    {#if $notifications.isError}
      <StatusBadge border={false} wrap>
        <ErrorNote
          bare
          error={$notifications.error}
          retry={() => $notifications.refetch()}
          fallback="Couldn't load settings. Retry before saving."
        />
      </StatusBadge>
    {:else if notificationConnection.role === 'fail' || $saveNotifications.isError || $testNotification.isError}
      <StatusBadge border={false} wrap>
        <ErrorNote
          bare
          error={$saveNotifications.error ?? $testNotification.error ?? $notifications.data?.connectionError}
          fallback="Couldn't send the notification."
        />
      </StatusBadge>
    {:else if notificationConnection.role === 'idle'}
      <span class="text-meta text-ink-3">
        {$notifications.data?.endpoint
          ? 'tick Enabled to get notified'
          : 'add an address to get notified'}
      </span>
    {/if}

    <!-- Two moments and no others: a fetched copy nothing could identify, and an
         import that stopped and asked. Both are where the machinery is waiting on
         a person; everything else Schall does is on a screen already. -->
    <span class="text-meta text-ink-4">
      sent only when a copy or an import needs your decision
    </span>
  </section>
{/snippet}

{#snippet ListenBrainz()}
  <section class="flex flex-col gap-3 pb-9">
    <div class="border-b border-rule pb-1.5"><h2 class="label">ListenBrainz</h2></div>

    <form
      class="grid grid-cols-[minmax(0,1fr)] items-center gap-x-3.5 gap-y-1.5 @xl:grid-cols-[130px_340px_minmax(0,1fr)]"
      onsubmit={(event) => {
        event.preventDefault();
        $saveListenBrainz.mutate();
      }}
    >
      {@render SourceLabel('Username', 'listenbrainz-username')}
      <input
        id="listenbrainz-username"
        aria-describedby="listenbrainz-username-hint"
        bind:value={listenbrainzUsername}
        type="text"
        required
        disabled={!$listenbrainz.isSuccess}
        class="field w-full"
      />
      {@render SourceHint('the account your listens go to', 'listenbrainz-username')}

      {@render SourceLabel('User token', 'listenbrainz-token')}
      <div class="flex flex-col gap-1.5">
        <input
          id="listenbrainz-token"
          aria-describedby="listenbrainz-token-hint"
          bind:value={listenbrainzToken}
          type="password"
          autocomplete="off"
          disabled={listenbrainzClearToken || !$listenbrainz.isSuccess}
          placeholder={$listenbrainz.data?.userTokenSet
            ? 'stored'
            : 'optional'}
          class="field w-full"
        />
        {#if $listenbrainz.data?.userTokenSet}
          <label
            class="flex w-fit cursor-pointer items-center gap-2 text-meta font-medium text-ink-3"
          >
            <input
              type="checkbox"
              checked={listenbrainzClearToken}
              disabled={!$listenbrainz.isSuccess}
              onchange={(event) => {
                listenbrainzClearToken = event.currentTarget.checked;
                // Forgetting and typing a replacement are opposite intentions.
                // Leaving a typed token in a disabled field would submit it and
                // then drop it, which reads as the save having lost it.
                if (listenbrainzClearToken) listenbrainzToken = '';
              }}
              class="check cursor-pointer"
            />
            clear stored token
          </label>
        {/if}
      </div>
      {@render SourceHint('optional; raises the rate limit', 'listenbrainz-token')}

      {@render SourceLabel('Enabled', '')}
      <label class="flex w-fit cursor-pointer items-center gap-2 text-meta font-medium text-ink-3">
        <input
          type="checkbox"
          bind:checked={listenbrainzEnabled}
          disabled={!$listenbrainz.isSuccess}
          class="check cursor-pointer"
        />
        {listenbrainzEnabled ? 'get recommendations' : 'off'}
      </label>
      {@render SourceHint('off stops all contact')}

      <span class="hidden @xl:block"></span>
      <div class="flex flex-wrap items-center gap-2 pt-1">
        <Button
          type="button"
          variant="outline"
          size="sm"
          disabled={!canTestListenBrainz || $testListenBrainz.isPending || !$listenbrainz.isSuccess}
          onclick={() => $testListenBrainz.mutate()}
        >
          {#if $testListenBrainz.isPending}<Icon name="busy" size={12} class="animate-spin" />{/if}
          Test
        </Button>
        <SettingsSaveButton
          variant="primary"
          pending={$saveListenBrainz.isPending}
          disabled={!$listenbrainz.isSuccess}
          saved={$saveListenBrainz.isSuccess}
          dirty={listenbrainzDirty}
        />
      </div>
    </form>

    {#if $listenbrainz.isError}
      <StatusBadge border={false} wrap>
        <ErrorNote
          bare
          error={$listenbrainz.error}
          retry={() => $listenbrainz.refetch()}
          fallback="Couldn't load settings. Retry before saving."
        />
      </StatusBadge>
    {:else if listenbrainzConnection.role === 'fail' || $saveListenBrainz.isError || $testListenBrainz.isError}
      <StatusBadge border={false} wrap>
        <ErrorNote
          bare
          error={$saveListenBrainz.error ?? $testListenBrainz.error ?? $listenbrainz.data?.connectionError}
          fallback="Couldn't save or check ListenBrainz."
        />
      </StatusBadge>
    {:else if listenbrainzConnection.role === 'ok' && $listenbrainz.data?.connectionDetail}
      <span class="text-meta text-ink-3">{$listenbrainz.data.connectionDetail}</span>
    {:else if listenbrainzConnection.role === 'idle'}
      <span class="text-meta text-ink-3">
        {$listenbrainz.data?.configured
          ? 'press Test to check the account'
          : 'add your ListenBrainz username'}
      </span>
    {/if}

    <!-- Schall only ever reads this account. A dismissal is Schall's own record
         of what the user does not want to be shown, and is never published to
         ListenBrainz under their name. -->
    <span class="text-meta text-ink-4">read-only; Schall never writes to ListenBrainz</span>
  </section>
{/snippet}

{#snippet Weekly()}
  <section class="flex flex-col gap-3.5 pb-9">
    <div class="flex flex-wrap items-baseline gap-2.5 border-b border-rule pb-1.5">
      <h2 class="label">Weekly playlist</h2>
    </div>

    {#if $weekly.isError}
      <ErrorNote
        error={$weekly.error}
        retry={() => $weekly.refetch()}
        fallback="Couldn't load settings. Retry before saving."
      />
    {/if}

    <!-- The one thing a person has to know before the switch goes on. It is the
         only form on this page that can end with a file leaving the disc, so it
         says so above the field rather than under it. -->
    <span class="text-meta leading-snug text-ink-2">
      Fetches new songs each week and deletes the ones you don't keep.
    </span>

    <FormGrid
      onsubmit={(event) => {
        event.preventDefault();
        $saveWeekly.mutate();
      }}
    >
      {@render Label('Enabled', 'off keeps every song')}
      <label class="flex w-fit cursor-pointer items-center gap-2 text-meta font-medium text-ink-3">
        <input
          type="checkbox"
          bind:checked={weeklyEnabled}
          disabled={!$weekly.isSuccess}
          class="check cursor-pointer"
        />
        {weeklyEnabled ? 'a list every week' : 'no list'}
      </label>

      {@render Label('Songs a week', 'how many to fetch, 1 to 50', 'weekly-songs')}
      <input
        id="weekly-songs"
        aria-describedby="weekly-songs-hint"
        bind:value={weeklySongs}
        type="number"
        min="1"
        max="50"
        disabled={!$weekly.isSuccess}
        class="field w-20"
      />

      {@render Label('Library share', '% from your library; never deleted', 'weekly-library-share')}
      <div class="flex flex-col gap-1">
        <div class="flex items-center gap-2">
          <input
            id="weekly-library-share"
            aria-describedby="weekly-library-share-hint"
            bind:value={weeklyLibraryShare}
            type="number"
            min="0"
            max="100"
            disabled={!$weekly.isSuccess}
            class="field w-20"
          />
          <span class="text-meta text-ink-3">%</span>
        </div>
      </div>

      {@render Label('One per artist', 'avoid repeats in a week')}
      <label class="flex w-fit cursor-pointer items-center gap-2 text-meta font-medium text-ink-3">
        <input
          type="checkbox"
          bind:checked={weeklyOnePerArtist}
          disabled={!$weekly.isSuccess}
          class="check cursor-pointer"
        />
        only one song per artist
      </label>

      {@render Label('Unkept songs', 'what happens to them each week')}
      <div class="flex flex-col">
        {@render WeeklyChoice('report', 'Report only', 'list them, delete nothing')}
        {@render WeeklyChoice('remove', 'Remove', 'delete them from disk; library songs stay')}
      </div>

      <span class="hidden @xl:block"></span>
      <div class="flex flex-wrap items-center gap-2 pt-1">
        <SettingsSaveButton
          pending={$saveWeekly.isPending}
          disabled={!$weekly.isSuccess}
          saved={$saveWeekly.isSuccess}
          dirty={weeklyDirty}
        />
      </div>
    </FormGrid>

    {#if $saveWeekly.isError}
      <StatusBadge border={false} wrap>
        <ErrorNote
          bare
          error={$saveWeekly.error}
          fallback="Weekly playlist not saved."
          action="Nothing was deleted. Press Save again."
        />
      </StatusBadge>
    {:else}
      <span class="text-meta text-ink-4">
        starred or kept songs are never deleted
      </span>
    {/if}
  </section>
{/snippet}

{#snippet NewReleases()}
  <section class="flex flex-col gap-3.5 pb-9">
    <div class="flex flex-wrap items-baseline gap-2.5 border-b border-rule pb-1.5">
      <h2 class="label">New releases</h2>
    </div>

    {#if $newReleases.isError}
      <ErrorNote
        error={$newReleases.error}
        retry={() => $newReleases.refetch()}
        fallback="Couldn't load settings. Retry before saving."
      />
    {/if}

    <span class="text-meta leading-snug text-ink-2">
      Recent releases you have from artists and labels you follow. Deletes nothing.
    </span>

    <FormGrid
      onsubmit={(event) => {
        event.preventDefault();
        $saveNewReleases.mutate();
      }}
    >
      {@render Label('Enabled', 'off empties the playlist')}
      <label class="flex w-fit cursor-pointer items-center gap-2 text-meta font-medium text-ink-3">
        <input
          type="checkbox"
          bind:checked={newReleasesEnabled}
          disabled={!$newReleases.isSuccess}
          class="check cursor-pointer"
        />
        {newReleasesEnabled ? 'keep the playlist filled' : 'no playlist'}
      </label>

      {@render Label('Window', 'days a release stays on it', 'new-releases-window')}
      <input
        id="new-releases-window"
        aria-describedby="new-releases-window-hint"
        bind:value={newReleasesWindow}
        type="number"
        min="1"
        max="365"
        disabled={!$newReleases.isSuccess}
        class="field w-20"
      />

      <span class="hidden @xl:block"></span>
      <div class="flex flex-wrap items-center gap-2 pt-1">
        <SettingsSaveButton
          pending={$saveNewReleases.isPending}
          disabled={!$newReleases.isSuccess}
          saved={$saveNewReleases.isSuccess}
          dirty={newReleasesDirty}
        />
      </div>
    </FormGrid>

    {#if $saveNewReleases.isError}
      <StatusBadge border={false} wrap>
        <ErrorNote
          bare
          error={$saveNewReleases.error}
          fallback="New releases playlist not saved."
          action="Press Save again."
        />
      </StatusBadge>
    {:else}
      <span class="text-meta text-ink-4">
        tracks you remove in your player stay out
      </span>
    {/if}
  </section>
{/snippet}

<!-- The same two-way choice the retention section draws, on the mode. This one
     holds the answer until Save, because the rest of this form is saved that way
     and a mode that wrote itself would be the one destructive setting on the
     page with no confirming press. -->
{#snippet WeeklyChoice(value: WeeklySettings['mode'], title: string, hint: string)}
  {@const chosen = weeklyMode === value}
  <button
    type="button"
    class="group flex items-start gap-2.5 border-b border-rule py-2.5 text-left last:border-b-0"
    disabled={!$weekly.isSuccess}
    onclick={() => (weeklyMode = value)}
  >
    <span class="mt-px grid size-6 shrink-0 place-items-center">
      <span
        class="grid size-6 place-items-center border transition {chosen
          ? 'border-ink bg-ink'
          : 'border-rule-strong bg-inset group-hover:border-ink-3'}"
      >
        {#if chosen}<Icon name="check" size={12} class="text-ground" />{/if}
      </span>
    </span>
    <span class="flex min-w-0 flex-col gap-1">
      <span class="text-body font-medium {chosen ? 'text-ink' : 'text-ink-2'}">{title}</span>
      <span class="text-meta text-ink-3">{hint}</span>
    </span>
  </button>
{/snippet}

{#snippet Spotify()}
  <section class="flex flex-col gap-3 pb-9">
    <div class="flex flex-wrap items-center gap-2.5 border-b border-rule pb-1.5">
      <h2 class="label">Spotify</h2>
      <Chip role={spotifyConnection.role}>{spotifyConnection.label}</Chip>
      {#if $spotify.data?.accountName}
        <span class="text-meta font-medium text-ink-3">as {$spotify.data.accountName}</span>
      {/if}
    </div>

    <form
      class="grid grid-cols-[minmax(0,1fr)] items-center gap-x-3.5 gap-y-1.5 @xl:grid-cols-[130px_340px_minmax(0,1fr)]"
      onsubmit={(event) => {
        event.preventDefault();
        $saveSpotify.mutate();
      }}
    >
      {@render SourceLabel('Client ID', 'spotify-client-id')}
      <input
        id="spotify-client-id"
        aria-describedby="spotify-client-id-hint"
        bind:value={spotifyClientId}
        type="text"
        required
        disabled={!$spotify.isSuccess}
        class="field w-full"
      />
      {@render SourceHint('from your app on developer.spotify.com', 'spotify-client-id')}

      {@render SourceLabel('Client secret', 'spotify-client-secret')}
      <input
        id="spotify-client-secret"
        bind:value={spotifyClientSecret}
        type="password"
        autocomplete="off"
        disabled={!$spotify.isSuccess}
        placeholder={$spotify.data?.clientSecretSet ? 'stored' : 'Spotify client secret'}
        class="field w-full"
      />
      {@render SourceHint('')}

      <span class="hidden @xl:block"></span>
      <div class="flex flex-wrap items-center gap-2 pt-1">
        <Button
          type="button"
          variant="outline"
          size="sm"
          disabled={!$spotify.data?.configured || $connectSpotify.isPending || !$spotify.isSuccess}
          onclick={() => $connectSpotify.mutate()}
        >
          {#if $connectSpotify.isPending}<Icon name="busy" size={12} class="animate-spin" />{/if}
          {$spotify.data?.connected ? 'Reconnect' : 'Connect account'}
        </Button>
        <SettingsSaveButton
          variant="primary"
          pending={$saveSpotify.isPending}
          disabled={!$spotify.isSuccess}
          saved={$saveSpotify.isSuccess}
          dirty={spotifyDirty}
        />
      </div>
    </form>

    {#if callbackNotice}
      <!-- What came back from Spotify's own authorization page. It is this
           screen's sentence rather than anything the server said, so it is not
           read through the map: there is no server wording to translate and
           nothing to keep behind a disclosure. -->
      <StatusBadge border={false} wrap>
        <span class="text-meta leading-relaxed text-ink">{callbackNotice}</span>
      </StatusBadge>
    {:else if $spotify.isError}
      <StatusBadge border={false} wrap>
        <ErrorNote
          bare
          error={$spotify.error}
          retry={() => $spotify.refetch()}
          fallback="Couldn't load settings. Retry before saving."
        />
      </StatusBadge>
    {:else if spotifyConnection.role === 'fail' || $saveSpotify.isError || $connectSpotify.isError}
      <StatusBadge border={false} wrap>
        <ErrorNote
          bare
          error={$saveSpotify.error ?? $connectSpotify.error ?? $spotify.data?.connectionError}
          fallback="Couldn't save Spotify or connect the account."
        />
      </StatusBadge>
    {:else if spotifyConnection.role === 'idle'}
      <span class="text-meta text-ink-3">
        {$spotify.data?.configured
          ? 'connect the account to read its playlists'
          : 'add the client ID and secret, then connect'}
      </span>
    {/if}

    {#if $spotify.data?.redirectUri}
      <!-- The sentence is prose and the address is data, so only the address is
           set in mono. It breaks anywhere it has to: an address is one word to a
           browser, and one word 324px wide made the whole page scroll sideways
           on a phone. -->
      <span class="text-meta leading-relaxed text-ink-4">
        add this redirect URI to your Spotify app:
        <span class="numeric break-all">{$spotify.data.redirectUri}</span>
      </span>
    {/if}
  </section>
{/snippet}

<!-- The left column of a settings form. The control it names is the next child
     of the same grid, not a child of this snippet, so the two are joined by an
     `id` on the control and `for` here. A row whose control is a checkbox
     passes no id: that checkbox already sits inside its own label, and a second
     one would read its name out twice. The hint keeps an id of its own so a
     control can point `aria-describedby` at it without the hint becoming part
     of the control's name. -->
{#snippet Label(text: string, hint = '', id = '')}
  <div class="flex flex-col gap-1">
    {#if id}
      <label for={id} class="text-meta font-medium text-ink">{text}</label>
    {:else}
      <span class="text-meta font-medium text-ink">{text}</span>
    {/if}
    {#if hint}
      <!-- text-pretty keeps the last word of a hint from sitting alone on its
           own line, which the 180px label column otherwise produces often. -->
      <span id={id ? `${id}-hint` : undefined} class="text-pretty text-meta leading-snug text-ink-3">
        {hint}
      </span>
    {/if}
  </div>
{/snippet}

{#snippet Retention()}
  <section class="flex flex-col gap-3.5 pb-9">
    <div class="flex flex-wrap items-baseline gap-2.5 border-b border-rule pb-1.5">
      <h2 class="label">
        Imported downloads
      </h2>
    </div>

    <div class="flex flex-col">
      {@render Choice('keep', 'Keep downloads', 'imports leave the downloaded files in place', true)}
      {@render Choice(
        'delete',
        'Delete after import',
        'deletes the downloaded files once the import is confirmed',
        canDeleteSources
      )}
    </div>

    {#if $imports.isError}
      <ErrorNote
        error={$imports.error}
        retry={() => $imports.refetch()}
        fallback="Couldn't load settings. Retry before saving."
      />
      <!-- Why the second option is greyed out. The server's sentence names the
         folder and the reason it refused; this adds the one thing that changes
         it. The path is on the line below, so it is not said twice. -->
    {:else if !canDeleteSources}
      <span class="text-meta text-ink-3">
        {$imports.data?.inboxDetail ?? 'the download folder is read-only'}. Mount it writable to
        use this.
      </span>
    {/if}

    {#if $imports.data?.inboxPath}
      <span class="text-meta text-ink-4">
        downloads are read from
        <span class="numeric break-all">{$imports.data.inboxPath}</span>
      </span>
    {/if}
  </section>
{/snippet}

{#snippet DuplicateResolution()}
  <section class="flex flex-col gap-3.5 pb-9">
    <div class="flex flex-wrap items-baseline gap-2.5 border-b border-rule pb-1.5">
      <h2 class="label">Resolve duplicates</h2>
    </div>

    {#if $duplicates.isError && !duplicatesUnavailable}
      <ErrorNote
        error={$duplicates.error}
        retry={() => $duplicates.refetch()}
        fallback="Couldn't load this setting."
      />
    {/if}

    {#if duplicatesUnavailable}
      <span class="text-meta leading-snug text-ink-2">
        Not available on this server.
      </span>
    {:else}
      <span class="text-meta leading-snug text-ink-2">
        Deletes extra copies when they are proven identical, or one is proven better.
      </span>

      <details class="group">
        <summary
          class="tap-tall flex w-fit cursor-pointer list-none items-center gap-1.5 text-meta text-ink-3 transition hover:text-ink-2 [&::-webkit-details-marker]:hidden"
        >
          <Icon name="chevron-right" size={12} class="transition-transform group-open:rotate-90" />
          What counts as proven
        </summary>
        <div class="reveal mt-1 flex flex-col gap-1.5 text-meta leading-[1.5] text-ink-3">
          <span>
            Same audio: same format, bit rate, sample rate and channels, and lengths within 5
            seconds.
          </span>
          <span>Anything unproven stays on the Duplicates screen for you.</span>
          <span>Each deletion is recorded like one you make there.</span>
        </div>
      </details>

      <FormGrid
        onsubmit={(event) => {
          event.preventDefault();
          $saveDuplicates.mutate();
        }}
      >
        {@render Label('Enabled', 'off leaves them to you')}
        <label class="flex w-fit cursor-pointer items-center gap-2 text-meta font-medium text-ink-3">
          <input
            type="checkbox"
            bind:checked={duplicatesEnabled}
            disabled={!$duplicates.isSuccess || $saveDuplicates.isPending}
            class="check cursor-pointer"
          />
          {duplicatesEnabled ? 'delete proven duplicates' : 'decide each one myself'}
        </label>

        <span class="hidden @xl:block"></span>
        <div class="flex flex-wrap items-center gap-2 pt-1">
          <SettingsSaveButton
            pending={$saveDuplicates.isPending}
            disabled={!$duplicates.isSuccess}
            saved={$saveDuplicates.isSuccess}
            dirty={duplicatesDirty}
          />
        </div>
      </FormGrid>

      {#if $saveDuplicates.isError}
        <StatusBadge border={false} wrap>
          <ErrorNote
            bare
            error={$saveDuplicates.error}
            fallback="Setting not saved."
            action="Press Save again."
          />
        </StatusBadge>
      {/if}
    {/if}
  </section>
{/snippet}

{#snippet Acoustid()}
  <!-- Every other check reads what a file says about itself. This one decodes
       the audio, so it is the only one a mislabelled file cannot satisfy. -->
  <section class="flex flex-col gap-3.5 pb-9">
    <div class="flex flex-wrap items-center gap-2.5 border-b border-rule pb-1.5">
      <h2 class="label">
        Verify imports by audio
      </h2>
      <label
        class="ml-auto flex cursor-pointer items-center gap-2 text-meta font-medium text-ink-2"
      >
        <input
          type="checkbox"
          checked={acoustidEnabled}
          disabled={
            !$imports.isSuccess || ((!acoustidKeySet && !acoustidKey.trim()) || $saveRetention.isPending)
          }
          onchange={(event) =>
            $saveRetention.mutate({
              apiKey: acoustidKey.trim(),
              enabled: event.currentTarget.checked
            })}
          class="check cursor-pointer"
        />
        enabled
      </label>
    </div>

    {#if $imports.isError}
      <ErrorNote
        error={$imports.error}
        retry={() => $imports.refetch()}
        fallback="Couldn't load settings. Retry before saving."
      />
    {/if}

    <div class="flex flex-wrap items-center gap-2.5">
      <!-- This box stands on its own rather than in a form grid, so there is no
           label beside it to point at. The name is carried on the box itself
           and says the same as the placeholder, which a stored key replaces. -->
      <input
        id="acoustid-api-key"
        aria-label="AcoustID API key"
        aria-describedby="acoustid-api-key-hint"
        type="password"
        bind:value={acoustidKey}
        disabled={!$imports.isSuccess}
        placeholder={acoustidKeySet ? 'stored' : 'AcoustID API key'}
        class="field min-w-52 flex-1"
      />
      <Button
        variant="ghost"
        disabled={!$imports.isSuccess || !acoustidKey.trim() || $saveRetention.isPending}
        onclick={() => $saveRetention.mutate({ apiKey: acoustidKey.trim() })}
      >
        Save key
      </Button>
    </div>

    <span id="acoustid-api-key-hint" class="text-meta text-ink-3">
      needs a key from acoustid.org and fpcalc on the server
    </span>

    <!-- What is stopped while this is off, said where it can be turned on. A
         want is one recording Schall looks for on Soulseek, and this check is
         the only thing that can prove a stranger's file is that recording, so
         with it off nothing is fetched at all and every want waits (issue
         #381). -->
    {#if !acoustidEnabled || !acoustidKeySet}
      <span class="text-meta text-ink-3">
        Wishlist searches are paused while this is off.
        {acoustidKeySet ? 'Tick enabled to start.' : 'Save a key and tick enabled.'}
      </span>
    {/if}
  </section>
{/snippet}

<!-- The 15px box carries "chosen"; the two options are told apart by a hairline
     rather than a box each, which would be the second border on this section. -->
{#snippet Choice(value: 'keep' | 'delete', title: string, hint: string, allowed: boolean)}
  {@const chosen = retention === value}
  <button
    type="button"
    class="group flex items-start gap-2.5 border-b border-rule py-2.5 text-left last:border-b-0 {allowed
      ? ''
      : 'opacity-50'}"
    disabled={!allowed || !$imports.isSuccess || $saveRetention.isPending}
    onclick={() => $saveRetention.mutate({ retention: value })}
  >
    <span class="mt-px grid size-6 shrink-0 place-items-center">
      <span
        class="grid size-6 place-items-center border transition {chosen
          ? 'border-ink bg-ink'
          : 'border-rule-strong bg-inset group-hover:border-ink-3'}"
      >
        {#if chosen}<Icon name="check" size={12} class="text-ground" />{/if}
      </span>
    </span>
    <span class="flex min-w-0 flex-col gap-1">
      <span class="text-body font-medium {chosen ? 'text-ink' : 'text-ink-2'}">{title}</span>
      <span class="text-meta text-ink-3">{hint}</span>
    </span>
  </button>
{/snippet}

{#snippet LibraryLayoutSection()}
  <section class="flex flex-col gap-3.5 pb-9">
    <div class="flex flex-wrap items-baseline gap-2.5 border-b border-rule pb-1.5">
      <h2 class="label">Library layout</h2>
    </div>

    {#if $layout.isError}
      <ErrorNote
        error={$layout.error}
        retry={() => $layout.refetch()}
        fallback="Couldn't load settings. Retry before saving."
      />
    {/if}

    <FormGrid
      onsubmit={(event) => {
        event.preventDefault();
        $saveLayout.mutate();
      }}
    >
      {@render Label(
        'Folder template',
        'folder and file names for your music',
        'library-layout-template'
      )}
      <input
        id="library-layout-template"
        bind:value={template}
        placeholder={$layout.data?.default}
        disabled={!$layout.isSuccess}
        class="field w-full"
      />

      <span class="hidden @xl:block"></span>
      <div class="flex flex-wrap items-center gap-2.5 pt-1">
        <SettingsSaveButton
          pending={$saveLayout.isPending}
          disabled={!$layout.isSuccess || !template.trim()}
          saved={$saveLayout.isSuccess}
          dirty={templateDirty}
        />
        <!-- Saving only writes down where future music goes; nothing on disc
             moves until a migration is planned and applied, and the button does
             not look like it. -->
        <span class="text-meta text-ink-3">saving moves nothing</span>
      </div>
    </FormGrid>

    {#if $layout.data?.example}
      <span class="text-meta text-ink-3">
        example: <span class="numeric">{$layout.data.example}</span>
      </span>
    {/if}

    {#if $saveLayout.isError}
      <StatusBadge border={false} wrap>
        <ErrorNote
          bare
          error={$saveLayout.error}
          fallback="Template not saved."
          action="Nothing was moved. Press Save again."
        />
      </StatusBadge>
    {/if}
  </section>
{/snippet}

<!-- Saving a template and migrating the files on disk are two different jobs:
     one person edits a pattern and presses Save, another watches a run of
     moves progress and decides whether to keep it. They used to share a card;
     now the migration gets its own, so a plan in progress does not read as
     part of the form above it. -->
{#snippet LibraryMigration()}
  <section class="flex flex-col gap-3.5 pb-9">
    <div class="flex flex-wrap items-center gap-2.5 border-b border-rule pb-1.5">
      <h2 class="label">Migrate library</h2>
      <Chip role={migration.role}>{migration.label}</Chip>
      <Button
        variant="outline"
        size="sm"
        class="ml-auto"
        disabled={!$layout.isSuccess || !$layoutRun.isSuccess || layoutBusy || run?.active}
        onclick={() => $planLayout.mutate()}
      >
        {#if $planLayout.isPending}<Icon name="busy" size={12} class="animate-spin" />{/if}
        Plan
      </Button>
      <!-- The one control on this screen drawn inverted, and only while
           there is a plan to apply. Button.svelte's rule is one inverted button
           per view, and this screen had six: five Saves and this. Six is none,
           because a page where everything is emphasised points nowhere.

           Settings offers no single action — it is a stack of separate
           panels, each with its own Save — so the inverted button goes to the one
           thing here that changes something outside the database. Applying a
           plan moves files on the disk. Saving a form does not. -->
      {#if run?.active}
        <Button
          size="sm"
          disabled={!$layoutRun.isSuccess || layoutBusy}
          onclick={() => $applyLayout.mutate(run.runId)}
        >
          {#if $applyLayout.isPending}<Icon name="busy" size={12} class="animate-spin" />{/if}
          Apply
        </Button>
        <Button
          variant="ghost"
          size="sm"
          disabled={!$layoutRun.isSuccess || layoutBusy}
          onclick={() => $discardLayout.mutate(run.runId)}
        >
          Discard
        </Button>
      {/if}
    </div>

    <Settle pending={$layoutRun.isPending} layer="flex flex-col gap-3.5">
      {#snippet placeholder()}
        <div class="flex flex-col gap-2" role="status" aria-busy="true" aria-label="Loading the migration">
          <Skeleton class="h-3.5 w-56" />
          <Skeleton class="h-3.5 w-72 max-w-full" />
          <Skeleton class="h-3.5 w-40" />
        </div>
      {/snippet}
    {#if run}
      <span class="numeric text-meta text-ink-3">
        {run.planned} to move · {run.moved} moved · {run.failed} failed
      </span>

      {#if run.plan}
        <span class="numeric text-meta leading-relaxed text-ink-3">
          {run.plan.inPlace} already in place · {run.plan.colliding} blocked by another file ·
          {run.plan.unfiled} stay put
        </span>
        {#if run.plan.unfiled > 0}
          <!-- &#123;id&#125; is the release identifier the template asks for, and
               Schall has one only for a file it matched to a release. -->
          <span class="text-meta leading-relaxed text-ink-3">
            &#123;id&#125; is empty for these. Remove it from the template to move those with an
            artist and album.
          </span>
        {/if}
      {/if}

      {#if run.moves.length > 0}
        <div class="flex flex-col gap-1">
          {#each run.moves.slice(0, 8) as move (move.id)}
            <div class="flex flex-wrap items-baseline gap-2">
              <Chip role={moveRoles[move.status]} class="shrink-0">
                {move.status}
              </Chip>
              <span class="numeric min-w-0 truncate text-meta text-ink-3">
                {move.fromPath}
                <Icon name="arrow-right" size="sm" class="mx-1 inline align-[-0.125em]" />
                {move.toPath}
              </span>
              {#if move.status === 'failed' && move.error}
                <span class="w-full text-meta leading-relaxed text-fail">
                  {explainMove(move.error)}
                </span>
                <details class="w-full">
                  <summary class="tap w-fit cursor-pointer text-meta text-ink-4">
                    What the server said
                  </summary>
                  <span class="numeric mt-1 block text-meta break-all text-ink-4">
                    {move.error}
                  </span>
                </details>
              {/if}
            </div>
          {/each}
          {#if run.moves.length > 8}
            <span class="numeric text-meta text-ink-4">+{run.moves.length - 8} more</span>
          {/if}
        </div>
      {/if}
    {:else if $layoutRun.isError}
      <ErrorNote
        error={$layoutRun.error}
        retry={() => $layoutRun.refetch()}
        fallback="Couldn't load the migration."
      />
    {:else}
      <!-- The chip already reads "Never migrated"; what is worth saying here is
           what the button will not do. -->
      <span class="text-meta text-ink-3">planning moves nothing</span>
    {/if}
    </Settle>

    {#if $planLayout.isError || $applyLayout.isError || $discardLayout.isError}
      {@const said = $planLayout.error ?? $applyLayout.error ?? $discardLayout.error}
      <StatusBadge border={false} wrap>
        <ErrorNote
          bare
          error={said}
          fallback="Layout not changed."
          action="Nothing was moved. Try again."
        />
      </StatusBadge>
    {/if}
  </section>
{/snippet}

{#snippet FileTagsSection()}
  <section class="flex flex-col gap-3.5 pb-9">
    <div class="flex flex-wrap items-center gap-2.5 border-b border-rule pb-1.5">
      <h2 class="label">
        Tags in your files
      </h2>
      <Chip role={tagged.role}>{tagged.label}</Chip>
      <Button
        variant="outline"
        size="sm"
        class="ml-auto"
        disabled={!$tags.isSuccess || writing || $writeTags.isPending || ($tags.data?.eligible ?? 0) === 0}
        onclick={() => $writeTags.mutate()}
      >
        {#if writing || $writeTags.isPending}<Icon name="busy" size={12} class="animate-spin" />{/if}
        Write tags
      </Button>
    </div>

    {#if $tags.isError}
      <ErrorNote
        error={$tags.error}
        retry={() => $tags.refetch()}
        fallback="Couldn't load tag status."
      />
    {/if}

    <!-- What Schall already does on import is how the feature works. What this
         button does is what the reader is here for. -->
    <span class="text-meta text-ink-3">
      Writes the matched artist, album and date into your existing files.
    </span>

    <!-- Nothing is said about the count until it is read: "no files are
         matched" is an answer, and a wrong one while the read is out. -->
    {#if $tags.isPending}
      <Skeleton class="h-3.5 w-64 max-w-full" />
    {:else}
    <span class="text-meta text-ink-3">
      {#if $tags.isError}
        Unavailable
      {:else if ($tags.data?.eligible ?? 0) === 0}
        no matched files yet
      {:else}
        {($tags.data?.eligible ?? 0).toLocaleString()} matched files to write
      {/if}
    </span>
    {/if}

    {#if $tags.data && $tags.data.status !== 'idle'}
      <span class="numeric text-meta text-ink-3">
        {$tags.data.written.toLocaleString()} written · {$tags.data.unchanged.toLocaleString()}
        already correct · {$tags.data.skipped.toLocaleString()} skipped ·
        {$tags.data.failed.toLocaleString()} failed
      </span>
    {/if}

    {#if writing}
      <span class="numeric text-meta text-ink-3">
        {tagsDone.toLocaleString()} of {($tags.data?.eligible ?? 0).toLocaleString()} files
      </span>
    {/if}

    {#if ($tags.data?.skippedAmbiguous ?? 0) > 0}
      <span class="text-meta text-ink-3">
        {($tags.data?.skippedAmbiguous ?? 0).toLocaleString()} files skipped: on several releases,
        source unknown.
      </span>
    {/if}

    {#if ($tags.data?.skippedUnproved ?? 0) > 0}
      <span class="text-meta text-ink-3">
        {($tags.data?.skippedUnproved ?? 0).toLocaleString()} files skipped: no title or artist in
        the catalogue.
      </span>
    {/if}

    {#if ($tags.data?.failed ?? 0) > 0}
      <span class="text-meta text-ink-3">
        Failed files are listed in the server log.
      </span>
    {/if}

    {#if $writeTags.isError || $tags.data?.status === 'failed'}
      <StatusBadge border={false} wrap>
        <ErrorNote
          bare
          error={$writeTags.error ?? $tags.data?.error}
          fallback="Tags not written."
        />
      </StatusBadge>
    {/if}
  </section>
{/snippet}

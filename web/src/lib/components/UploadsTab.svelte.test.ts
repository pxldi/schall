import { afterEach, beforeAll, expect, it, vi } from 'vitest';
import { cleanup, fireEvent, render, screen, setup, waitFor } from '@testing-library/svelte';
import { QueryClient, QueryClientProvider } from '@tanstack/svelte-query';
import UploadsTab from '$lib/components/UploadsTab.svelte';
import { api, type SoundCloudTrack, type Upload } from '$lib/api';

// An upload lands in the library unmatched, and everything a person does with it
// next — naming a SoundCloud edit, matching it by hand — happens on its row in
// Library. This page used to send them there with a bare link to /library and
// twenty thousand files to look through: the file that had just been uploaded
// was nowhere named, and its folder is /music/Unknown, so browsing did not find
// it either. So what is asserted here is that the row says where its file went.

beforeAll(setup);
afterEach(() => {
  cleanup();
  vi.restoreAllMocks();
});

function upload(overrides: Partial<Upload> = {}): Upload {
  return {
    id: '1bebfa12-0000-4000-8000-000000000001',
    status: 'imported',
    files: ['PinkPantheress - Illegal (Abo Edit).wav'],
    sizeBytes: 41_238_012,
    staged: false,
    createdAt: '2026-08-29T16:04:00Z',
    completedAt: '2026-08-29T16:05:00Z',
    ...overrides
  };
}

function opened(items: Upload[]) {
  vi.stubGlobal(
    'fetch',
    vi.fn(
      async () =>
        new Response(JSON.stringify({ items, stagingPath: '/staging', maxBytes: 2_000_000_000 }))
    )
  );
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  return render(UploadsTab, undefined, {
    wrapper: QueryClientProvider,
    wrapperProps: { client }
  });
}

function openedLoading() {
  let release = () => {};
  const blocked = new Promise<void>((resolve) => (release = resolve));
  vi.stubGlobal(
    'fetch',
    vi.fn(async () => {
      await blocked;
      return new Response(
        JSON.stringify({ items: [], stagingPath: '/staging', maxBytes: 2_000_000_000 })
      );
    })
  );
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  render(UploadsTab, undefined, {
    wrapper: QueryClientProvider,
    wrapperProps: { client }
  });
  return release;
}

it('does not show the empty state while recent uploads are loading', async () => {
  const release = openedLoading();

  expect(screen.queryByText('Nothing uploaded yet')).toBeNull();
  expect(document.querySelectorAll('.animate-pulse')).toHaveLength(160);

  release();
  expect(await screen.findByText('Nothing uploaded yet')).toBeTruthy();
});

it('takes an imported upload to its own file in Library', async () => {
  opened([upload()]);

  const link = await screen.findByRole('link', { name: 'Open in Library' });
  expect(link.getAttribute('href')).toBe(
    '/library?view=files&q=PinkPantheress%20-%20Illegal%20(Abo%20Edit)'
  );
});

// A file still being read has not been filed anywhere yet, so a link to look for
// it would find nothing.
it('offers no link until the upload has been imported', async () => {
  opened([upload({ status: 'validating', completedAt: undefined })]);

  await screen.findByText('Validating');
  expect(screen.queryByRole('link', { name: 'Open in Library' })).toBeNull();
});

// The extension is dropped because the library search matches on what the file
// is called, and a person searching by hand would not type ".wav" either.
it('searches for the file name without its extension', async () => {
  opened([upload({ files: ['02 - Some Track.flac'] })]);

  const link = await screen.findByRole('link', { name: 'Open in Library' });
  expect(link.getAttribute('href')).toBe('/library?view=files&q=02%20-%20Some%20Track');
});

// Library opens on Releases, and an upload that has not been identified yet
// belongs to no release. A link without the files view lands on a search that
// cannot find the file whatever it is given, which is how somebody came to
// report an uploaded track as missing while it sat in the library the whole
// time.
it('names the files view, not the release list Library opens on', async () => {
  opened([upload()]);

  const link = await screen.findByRole('link', { name: 'Open in Library' });
  expect(new URL(link.getAttribute('href') ?? '', 'http://x').searchParams.get('view')).toBe(
    'files'
  );
});

// One address names one track: the field beside the picker is only worth
// asking about a single chosen file, since a selection of several has no one
// file for the address to be about.

const aboEdit: SoundCloudTrack = {
  externalId: 'soundcloud:293',
  title: 'PinkPantheress - Illegal (Abo Edit) by Abo',
  trackTitle: 'PinkPantheress - Illegal (Abo Edit)',
  uploader: 'Abo',
  uploaderUrl: 'https://soundcloud.com/abo',
  artworkUrl: 'https://i1.sndcdn.com/artworks-abc-t500x500.jpg',
  permalink: 'https://soundcloud.com/abo/illegal',
  suggested: { artist: 'PinkPantheress', title: 'Illegal (Abo Edit)', remixer: 'Abo' }
};

function chooseFiles(...names: string[]) {
  const picker = screen.getByLabelText('Choose files to upload') as HTMLInputElement;
  const files = names.map((name) => new File(['audio'], name, { type: 'audio/flac' }));
  return fireEvent.change(picker, { target: { files } });
}

it('offers the SoundCloud link only when exactly one file is chosen', async () => {
  opened([]);

  await chooseFiles('illegal.flac');
  expect(screen.getByLabelText('SoundCloud link')).toBeTruthy();

  await chooseFiles('one.flac', 'two.flac');
  expect(screen.queryByLabelText('SoundCloud link')).toBeNull();
});

it('looks up a pasted SoundCloud address and shows what it read off the title', async () => {
  const look = vi.spyOn(api, 'soundCloudTrack').mockResolvedValue(aboEdit);
  opened([]);
  await chooseFiles('illegal.flac');

  await fireEvent.input(screen.getByLabelText('SoundCloud link'), {
    target: { value: aboEdit.permalink }
  });
  await fireEvent.click(screen.getByRole('button', { name: 'Look up' }));

  expect(await screen.findByLabelText('Artist')).toBeTruthy();
  expect(look).toHaveBeenCalledWith(aboEdit.permalink);
  expect((screen.getByLabelText('Artist') as HTMLInputElement).value).toBe('PinkPantheress');
  expect((screen.getByLabelText('Track') as HTMLInputElement).value).toBe('Illegal (Abo Edit)');
  expect((screen.getByLabelText('Remixer') as HTMLInputElement).value).toBe('Abo');
});

it('sends the confirmed SoundCloud naming with the upload', async () => {
  vi.spyOn(api, 'soundCloudTrack').mockResolvedValue(aboEdit);
  const send = vi.spyOn(api, 'uploadFiles').mockResolvedValue(upload());
  opened([]);
  await chooseFiles('illegal.flac');

  await fireEvent.input(screen.getByLabelText('SoundCloud link'), {
    target: { value: aboEdit.permalink }
  });
  await fireEvent.click(screen.getByRole('button', { name: 'Look up' }));
  await screen.findByLabelText('Artist');

  await fireEvent.click(screen.getByRole('button', { name: 'Upload' }));

  await waitFor(() =>
    expect(send).toHaveBeenCalledWith(expect.any(Array), expect.any(Function), {
      sourceUrl: aboEdit.permalink,
      artist: 'PinkPantheress',
      title: 'Illegal (Abo Edit)',
      remixer: 'Abo'
    })
  );
});

it('exposes upload progress and announces a failed upload', async () => {
  vi.spyOn(api, 'uploadFiles').mockImplementation((_files, onProgress) => {
    onProgress?.(0.5);
    return new Promise<Upload>(() => {});
  });
  opened([upload({ status: 'failed' })]);
  await chooseFiles('illegal.flac');
  await fireEvent.click(screen.getByRole('button', { name: 'Upload' }));

  const progressbar = await screen.findByRole('progressbar', { name: 'Upload progress' });
  expect(progressbar.getAttribute('aria-valuenow')).toBe('50');
  const failure = await screen.findByText('Refused');
  expect(failure.getAttribute('aria-live')).toBe('polite');
});

// A file that was never sent under this naming should never carry it, so
// growing the selection back past one file has to put the address down, not
// just hide it on screen.
it('does not send a SoundCloud address once a second file is chosen', async () => {
  vi.spyOn(api, 'soundCloudTrack').mockResolvedValue(aboEdit);
  const send = vi.spyOn(api, 'uploadFiles').mockResolvedValue(upload());
  opened([]);
  await chooseFiles('illegal.flac');
  await fireEvent.input(screen.getByLabelText('SoundCloud link'), {
    target: { value: aboEdit.permalink }
  });
  await fireEvent.click(screen.getByRole('button', { name: 'Look up' }));
  await screen.findByLabelText('Artist');

  await chooseFiles('illegal.flac', 'other.flac');
  await fireEvent.click(screen.getByRole('button', { name: 'Upload' }));

  await waitFor(() =>
    expect(send).toHaveBeenCalledWith(expect.any(Array), expect.any(Function), undefined)
  );
});

// A pasted address nobody looked up would otherwise be sent as nothing at
// all, silently — so Upload waits rather than dropping it without a word.
it('will not upload a pasted SoundCloud address until it has been looked up', async () => {
  opened([]);
  await chooseFiles('illegal.flac');

  await fireEvent.input(screen.getByLabelText('SoundCloud link'), {
    target: { value: aboEdit.permalink }
  });

  expect((screen.getByRole('button', { name: 'Upload' }) as HTMLButtonElement).disabled).toBe(
    true
  );
});

import { afterEach, describe, expect, it } from 'vitest';
import { cleanup, render, screen } from '@testing-library/svelte';
import AddToMusicBrainz from '$lib/components/AddToMusicBrainz.svelte';
import type { MusicBrainzSeed } from '$lib/api';

// MusicBrainz's release editor is seeded by what is posted to it, so this is a
// form and not a link, and the values it carries have to reach the page exactly
// as the API wrote them.

afterEach(cleanup);

function seed(fields: { name: string; value: string }[] = []): MusicBrainzSeed {
  return { url: 'https://musicbrainz.org/release/add', fields };
}

function form(): HTMLFormElement {
  const element = screen.getByRole('button', { name: /Add to MusicBrainz/ }).closest('form');
  if (!element) throw new Error('the control is not inside a form');
  return element;
}

describe('AddToMusicBrainz', () => {
  it('posts to the release editor', () => {
    render(AddToMusicBrainz, { seed: seed() });

    expect(form().getAttribute('method')).toBe('post');
    expect(form().getAttribute('action')).toBe('https://musicbrainz.org/release/add');
  });

  it('leaves Schall in a new tab', () => {
    render(AddToMusicBrainz, { seed: seed() });

    expect(form().getAttribute('target')).toBe('_blank');
  });

  it('carries every seeded field', () => {
    render(AddToMusicBrainz, {
      seed: seed([
        { name: 'name', value: 'France 98' },
        { name: 'mediums.0.track.0.length', value: '372000' }
      ])
    });

    const fields = form().querySelectorAll('input[type="hidden"]');
    expect([...fields].map((field) => (field as HTMLInputElement).name)).toEqual([
      'name',
      'mediums.0.track.0.length'
    ]);
  });

  it('keeps a title full of awkward characters intact', () => {
    const title = `50% Off & "Free" / <untitled> #3 + ?`;
    render(AddToMusicBrainz, { seed: seed([{ name: 'name', value: title }]) });

    const field = form().querySelector('input[name="name"]') as HTMLInputElement;
    expect(field.value).toBe(title);
  });

  it('waits to be submitted by the person reading the row', () => {
    render(AddToMusicBrainz, { seed: seed() });

    const control = screen.getByRole('button', { name: /Add to MusicBrainz/ });
    expect(control.getAttribute('type')).toBe('submit');
  });
});

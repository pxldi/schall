import { afterEach, describe, expect, it, vi } from 'vitest';
import { cleanup, render, screen } from '@testing-library/svelte';
import type { EvidenceColumn, EvidenceRow } from '$lib/review';
import ReviewEvidence from './ReviewEvidence.svelte';

// The table a person reads before deciding something permanent. Two things it
// owes them: the column that disagreed near the front, rather than sixth of
// ten, and a sentence for every verdict word it speaks in.

afterEach(cleanup);

const columns: EvidenceColumn[] = [
  { key: 'file', label: 'File' },
  { key: 'artist', label: 'Artist' },
  { key: 'title', label: 'Title' },
  { key: 'audio', label: 'Audio' }
];

function cell(value: string, differs = false, meaning = '') {
  return { value, differs, note: '', meaning };
}

const rows: EvidenceRow[] = [
  {
    id: 'one',
    cells: {
      file: cell('03 Evensong.flac'),
      artist: cell('Talk Talk'),
      title: cell('Ascension Day'),
      // The one that disagreed, and the one the schema puts last.
      audio: cell('could not listen', true, 'Schall never heard this file: it is switched off.')
    },
    ledger: { differs: [], undecided: [], agrees: ['artist'] }
  }
];

function drawn() {
  render(ReviewEvidence, {
    props: { columns, rows, chosen: 0, label: 'Copies', onchoose: vi.fn() }
  });
}

describe('the order of the columns', () => {
  // A reader is here because something disagreed, and the column that disagreed
  // was wherever the schema put it. They read across the cells that agreed to
  // reach the one that did not, on every row.
  it('puts the column that disagreed ahead of the ones that did not', () => {
    drawn();

    const headers = screen.getAllByRole('columnheader').map((head) => head.textContent?.trim());

    expect(headers).toEqual(['Choose', 'File', 'Audio', 'Artist', 'Title']);
  });

  // What names the row. A table whose first column moves about is one a reader
  // has to find their place in again on every question.
  it('leaves the column that names the row first', () => {
    drawn();

    const headers = screen.getAllByRole('columnheader').map((head) => head.textContent?.trim());

    expect(headers[1]).toBe('File');
  });
});

describe('the words the table speaks in', () => {
  // "could not listen" covers Schall never having heard the file at all, and
  // differs from "not recognised" and "no agreement" in a way nothing on the
  // screen said. A person deciding permanently between two copies needs that
  // difference, and the disclosure is where a keyboard can reach it.
  it('says in one sentence what a verdict word means', () => {
    drawn();

    expect(screen.getByText(/Schall never heard this file/)).toBeTruthy();
  });

  it('says nothing extra about a value that explains itself', () => {
    drawn();

    expect(screen.queryByText(/03 Evensong.flac —/)).toBeNull();
  });
});

import { afterEach, describe, expect, it } from 'vitest';
import { cleanup, render, screen } from '@testing-library/svelte';
import type { AcquisitionCandidate } from '$lib/api';
import VersionCandidates from '$lib/components/VersionCandidates.svelte';

afterEach(() => {
  cleanup();
});

function candidate(overrides: Partial<AcquisitionCandidate> = {}): AcquisitionCandidate {
  return {
    recordingId: 'cccccccc-0000-4000-8000-000000000003',
    releaseGroupId: 'dddddddd-0000-4000-8000-000000000004',
    artistName: 'Kestrel Grove',
    releaseTitle: 'Nine Lanterns',
    trackTitle: 'Evensong',
    durationMs: 281_000,
    isrc: 'GBAAA0000001',
    rank: 1,
    agrees: ['artist'],
    differs: [],
    summary: 'One recording fits this entry.',
    ...overrides
  };
}

describe('the identifiers behind a disclosure', () => {
  it('keeps the ISRC and MusicBrainz IDs out of the compared columns, behind an Identifiers summary', () => {
    render(VersionCandidates, {
      props: { candidates: [candidate()], chosen: 0, onchoose: () => {} }
    });

    // The only "ISRC" text on the row is the disclosure's own label, not a
    // column header beside Recording, Release and Length.
    expect(screen.getAllByText('ISRC')).toHaveLength(1);

    const summary = screen.getByText('Identifiers');
    const details = summary.closest('details');
    expect(details).toBeTruthy();
    expect(details?.textContent).toContain('GBAAA0000001');
    expect(details?.textContent).toContain('cccccccc-0000-4000-8000-000000000003');
  });
});

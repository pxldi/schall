import { describe, expect, it } from 'vitest';
import { isStaleCode, loadNewerBuild, reloadOnce, type Reloader } from './reload';

function fake(answers: boolean[], stored: string | null = null) {
  const went: string[] = [];
  let clock = 1_000_000;
  let value = stored;
  const reloader: Reloader = {
    now: () => clock,
    read: () => value,
    write: (next) => (value = next),
    reachable: async () => answers.shift() ?? false,
    wait: async (ms) => void (clock += ms),
    go: (href) => void went.push(href)
  };
  return { reloader, went, advance: (ms: number) => (clock += ms) };
}

describe('isStaleCode', () => {
  it('knows the three browsers’ words for a failed page import', () => {
    expect(isStaleCode(new TypeError('Failed to fetch dynamically imported module: https://x/_app/immutable/nodes/9.BHBD3GLb.js'))).toBe(true);
    expect(isStaleCode(new TypeError('error loading dynamically imported module: x'))).toBe(true);
    expect(isStaleCode(new TypeError('Importing a module script failed.'))).toBe(true);
  });

  it('leaves every other failure alone', () => {
    expect(isStaleCode(new Error('Not found: /nowhere'))).toBe(false);
    expect(isStaleCode(undefined)).toBe(false);
  });
});

describe('reloadOnce', () => {
  it('loads the address it was going to in full', async () => {
    const { reloader, went } = fake([true]);
    expect(await reloadOnce('https://schall/releases/1', reloader)).toBe(true);
    expect(went).toEqual(['https://schall/releases/1']);
  });

  it('waits for a restarting server before it loads', async () => {
    const { reloader, went } = fake([false, false, true]);
    expect(await reloadOnce('https://schall/', reloader)).toBe(true);
    expect(went).toEqual(['https://schall/']);
  });

  it('gives up when the server stays away', async () => {
    const { reloader, went } = fake([]);
    expect(await reloadOnce('https://schall/', reloader, 3)).toBe(false);
    expect(went).toEqual([]);
  });

  it('does not load the same address twice in a row', async () => {
    const { reloader, went, advance } = fake([true, true, true]);
    await reloadOnce('https://schall/artists', reloader);
    expect(await reloadOnce('https://schall/artists', reloader)).toBe(false);
    expect(await reloadOnce('https://schall/review', reloader)).toBe(true);
    advance(60_000);
    expect(await reloadOnce('https://schall/review', reloader)).toBe(true);
    expect(went).toEqual(['https://schall/artists', 'https://schall/review', 'https://schall/review']);
  });
});

describe('loadNewerBuild', () => {
  const to = { url: new URL('https://schall/artists') };

  it('loads the next page in full once a newer build is out', () => {
    const went: string[] = [];
    expect(loadNewerBuild({ willUnload: false, to }, true, (href) => went.push(href))).toBe(true);
    expect(went).toEqual(['https://schall/artists']);
  });

  it('leaves page changes to the router while the build is current', () => {
    const went: string[] = [];
    expect(loadNewerBuild({ willUnload: false, to }, false, (href) => went.push(href))).toBe(false);
    expect(went).toEqual([]);
  });

  it('leaves a navigation that already leaves the page alone', () => {
    const went: string[] = [];
    expect(loadNewerBuild({ willUnload: true, to }, true, (href) => went.push(href))).toBe(false);
    expect(went).toEqual([]);
  });
});

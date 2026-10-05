import { afterEach, beforeAll, describe, expect, it, vi } from "vitest";
import { cleanup, render, screen, setup } from "@testing-library/svelte";
import { QueryClient, QueryClientProvider } from "@tanstack/svelte-query";
import type { LabelDetail, LabelRelease } from "$lib/api";

// One label's page: its releases as tiles, the ones the library holds nothing
// of faded, and only cached covers asked for.

vi.mock("$app/state", () => ({
  page: {
    params: { id: "l1" },
    url: new URL("http://localhost/artists/labels/l1"),
  },
}));

vi.mock("$app/navigation", () => ({
  afterNavigate: () => {},
  goto: vi.fn(),
  replaceState: vi.fn(),
}));

const { default: LabelPage } = await import("./+page.svelte");

function release(overrides: Partial<LabelRelease> = {}): LabelRelease {
  return {
    id: "r1",
    title: "Geogaddi",
    musicbrainzReleaseGroupId: null,
    releaseDate: "2002-02-18",
    albumType: "album",
    artistId: "a1",
    artistName: "Boards of Canada",
    trackCount: 23,
    ownedTrackCount: 23,
    monitored: true,
    hasCover: true,
    ...overrides,
  };
}

function answering(detail: LabelDetail) {
  vi.stubGlobal(
    "fetch",
    vi.fn(async () => new Response(JSON.stringify(detail), { status: 200 })),
  );
}

function opened() {
  const client = new QueryClient({
    defaultOptions: { queries: { retry: false } },
  });
  return render(LabelPage, undefined, {
    wrapper: QueryClientProvider,
    wrapperProps: { client },
  });
}

beforeAll(setup);

afterEach(() => {
  cleanup();
  vi.unstubAllGlobals();
});

describe("a label page", () => {
  it("names the label in its level-1 heading", async () => {
    answering({
      label: {
        id: "l1",
        musicbrainzId: "46f0f4cd-8aab-4b33-b698-f459faf64190",
        name: "Warp Records",
        monitorLevel: "main",
        followed: true,
        followedAt: "2026-01-01T00:00:00Z",
        lastRefreshedAt: null,
        refreshStatus: "completed",
      },
      releases: [
        release(),
        release({
          id: "r2",
          title: "Tomorrow’s Harvest",
          ownedTrackCount: 0,
          hasCover: false,
        }),
      ],
    });
    opened();

    expect(
      await screen.findByRole("heading", { level: 1, name: "Warp Records" }),
    ).toBeTruthy();
    expect(screen.getByText("2002 · 23 of 23")).toBeTruthy();

    const held = Array.from(document.querySelectorAll("[data-held]")).map(
      (node) => node.getAttribute("data-held"),
    );
    expect(held).toEqual(["some", "none"]);

    const pictures = Array.from(document.querySelectorAll("img")).map((img) =>
      img.getAttribute("src"),
    );
    expect(pictures).toEqual(["/api/v1/albums/r1/cover?cached=1"]);
  });
});

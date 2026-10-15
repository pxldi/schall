// The artists list the reader last looked at, so the list beside an artist
// page is the one they picked the artist from: the same scope, search, tab,
// genre and order, and the same cached answer. The values are the artists
// page's own, empty meaning "any", and so is the query key built from them.
export interface ArtistsListChoice {
  scope: "" | "followed" | "held";
  search: string;
  tab: "" | "incomplete" | "complete" | "attention";
  genre: string;
  sort: "name" | "least-complete" | "most-missing";
}

class ArtistsListMemory {
  choice = $state<ArtistsListChoice>({
    scope: "",
    search: "",
    tab: "",
    genre: "",
    sort: "name",
  });
}

export const artistsList = new ArtistsListMemory();

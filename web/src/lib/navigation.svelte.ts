import { afterNavigate } from '$app/navigation';

// Where the reader was before the screen they are on, remembered for the whole
// session rather than by whichever component happens to draw an arrow.
//
// `afterNavigate` reads like a way of asking where a navigation came from, but
// it is a subscription with no initial value: SvelteKit adds the callback to a
// set when the component mounts and calls it only for navigations after that,
// and there is no replay for anyone who subscribed late. A back arrow drawn
// behind a query — the artist page draws its header only once the artist has
// arrived — mounts after the navigation that brought the reader there has
// finished, so it is never told anything at all and can only fall back. One
// subscription, taken out by the root layout, which mounts once when the
// application starts and is never torn down, hears every navigation; a
// component then reads the answer whenever it appears.
let screen = $state<string | null>(null);
let oneStepBack = $state(false);
let started = $state(false);

export const previousScreen = {
  // The address of the screen the reader came from, or null when there is none
  // — which is what a page somebody opened directly looks like, from a link
  // they were sent, a reload, or a tab opened straight onto it.
  get href() {
    return screen;
  },
  // Whether that screen is the history entry immediately behind this one. It is
  // the licence to pop history rather than push a new entry, and it has to be
  // asked because the two can disagree: after a change of parameters the screen
  // the reader came from is further back than one entry, and popping would land
  // somewhere other than the address the arrow names.
  get oneStepBack() {
    return oneStepBack;
  }
};

// Whether SvelteKit has finished starting the application, which it says by
// making that first arrival. Until then its router is still being assembled and
// nothing may write the address bar: the write throws, and the throw is taken
// as the start-up itself failing, after which no link is ever intercepted again
// and every navigation is a full page load. Read inside an effect this is
// enough on its own, because the effect runs again the moment it turns true —
// so a write that came too early is held rather than lost.
export function applicationHasStarted() {
  return started;
}

export function trackNavigation() {
  afterNavigate((navigation) => {
    const previous = navigation.from?.url;
    // The application starting is the one arrival with nothing behind it: a
    // link somebody was sent, a reload, a tab opened straight onto a page.
    // There is no screen before it, and this is the case the fallback exists
    // for.
    if (navigation.type === 'enter') {
      screen = null;
      oneStepBack = false;
      started = true;
      return;
    }

    // Any other navigation that cannot say where the reader came from is not
    // evidence that they came from nowhere, so it leaves the answer alone.
    if (!previous) return;

    // The reader worked the browser's own back or forward control. Where they
    // came from is now ahead of them rather than behind, and what lies behind
    // is the browser's own business, so there is no answer of ours to give and
    // the arrow returns to naming its section.
    if (navigation.type === 'popstate') {
      screen = null;
      oneStepBack = false;
      return;
    }

    // A change of parameters within one screen is not somewhere to go back to —
    // the arrow would send the reader in a circle. The screen they came from
    // before it is still the right answer and is kept, but it is no longer the
    // entry immediately behind them.
    if (previous.pathname === navigation.to?.url.pathname) {
      oneStepBack = false;
      return;
    }

    screen = previous.pathname + previous.search;
    oneStepBack = true;
  });
}

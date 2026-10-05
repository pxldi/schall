// Opens the command palette from anywhere: the top bar's search field, the
// error page, a keyboard hint. The palette listens for this event, so nothing
// has to hold a reference to it.
export function openSearch() {
  window.dispatchEvent(new CustomEvent('schall:search'));
}

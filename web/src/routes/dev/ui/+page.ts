import { error } from '@sveltejs/kit';

// The gallery is a workbench, not a screen. It draws every component in the
// `ui/` directory in every variant so that somebody can check the theming
// without a real page to put them on, and it is reachable only while the
// development server is running: in a built image this load throws and the
// route is a 404 like any address that does not exist.
//
// `import.meta.env.DEV` is decided when the bundle is built, so the check is
// not a runtime test that could be got around. It is false in the shipped
// bundle and the branch below is all that is left of this page.
export function load() {
  if (!import.meta.env.DEV) error(404, 'Not found');
  return {};
}

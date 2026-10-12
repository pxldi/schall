import type { HandleClientError } from '@sveltejs/kit';
import { browserReloader, isStaleCode, reloadOnce } from '$lib/reload';

export const handleError: HandleClientError = async ({ error, event, message }) => {
  // The router waits for this before it draws the error page, so a reload
  // started here replaces the page before the error is shown.
  if (isStaleCode(error)) await reloadOnce(event.url.href, browserReloader);
  return { message };
};

import { Redirect } from 'expo-router';

/** Where the ntfy notification's Click link, schall://review, lands. The
 * Review tab is the (tabs) index, so this only forwards. */
export default function ReviewLink() {
  return <Redirect href="/" />;
}

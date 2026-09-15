/** What every player on a page agrees about.
 *
 * A player is the control that plays one file so somebody can decide about it.
 * More than one can stand on a page at once — the duplicate question puts two
 * side by side, because "which of these two copies is better" is a comparison
 * and a comparison needs both — and two players that each kept their own
 * settings would make that comparison lie.
 *
 * Two things are shared, for two different reasons.
 *
 * The volume, because a person sets it once for the room they are in. A second
 * player that started at a level they did not choose is the blast the volume
 * control exists to prevent, in a new place.
 *
 * Which player is sounding, because comparing two copies means hearing one at a
 * time. Starting the second used to leave the first running, and what came out
 * of the speakers was both files at once — which answers nothing.
 */
const VOLUME_KEY = 'schall:preview-volume';

/** The volume last chosen, or 0.7 the first time anything plays. Read once at
 * module load; a private window or a browser that blocks storage falls back
 * to the default rather than throwing. */
function storedVolume(): number {
  try {
    const raw = localStorage.getItem(VOLUME_KEY);
    const value = raw === null ? NaN : Number(raw);
    return Number.isFinite(value) && value >= 0 && value <= 1 ? value : 0.7;
  } catch {
    return 0.7;
  }
}

export const listening = $state({
  /** Between 0 and 1, the value an <audio> element wants. */
  volume: storedVolume(),
  /** The source of the player that is sounding, empty when nothing is. */
  sounding: ''
});

/** Sets the shared volume and remembers it for next time. Every player writes
 * through this rather than assigning `listening.volume` directly, so the
 * setting a person leaves the room with is the one they come back to. */
export function setVolume(volume: number) {
  listening.volume = volume;
  try {
    localStorage.setItem(VOLUME_KEY, String(volume));
  } catch {
    // Best effort — the volume still applies for the rest of this visit.
  }
}

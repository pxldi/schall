#!/usr/bin/env bash
#
# Runs the screenshot tests in web/e2e/visual.
#
# The servers run on this machine, as they do for the browser tests, but the
# browser runs in Playwright's own image. A screenshot depends on the fonts,
# freetype and chromium that drew it, and a laptop, a cloud sandbox and the CI
# runner each have different ones. Drawing every baseline in the same pinned
# image is what lets a 0.1% pixel budget stay meaningful. The image tag follows
# the @playwright/test version in web/package-lock.json, so a Playwright bump
# moves both and the baselines are regenerated in the same PR.
#
#   scripts/visual.sh                       compare against the baselines
#   scripts/visual.sh --update-snapshots    redraw the baselines
#
# Any other arguments go to `playwright test` as well.

set -euo pipefail

cd "$(dirname "$0")/../web"

VERSION="$(node -p "require('./package-lock.json').packages['node_modules/@playwright/test'].version")"
IMAGE="mcr.microsoft.com/playwright:v${VERSION}-noble"
PORT="${SCHALL_VISUAL_BROWSER_PORT:-3333}"
NAME="schall-visual-$$"

# The run-server comes from the checkout's own node_modules, so the container
# needs no registry access and the server is exactly the client's version.
docker run -d --rm --init --name "$NAME" --network host \
	-v "$PWD/node_modules:/schall/node_modules:ro" \
	--workdir /schall \
	"$IMAGE" \
	node node_modules/playwright-core/cli.js run-server --port "$PORT" --host 127.0.0.1 >/dev/null
trap 'docker stop "$NAME" >/dev/null 2>&1 || true' EXIT

for _ in $(seq 1 60); do
	if (exec 3<>"/dev/tcp/127.0.0.1/$PORT") 2>/dev/null; then
		break
	fi
	sleep 1
done

npx svelte-kit sync
SCHALL_VISUAL_WS_ENDPOINT="ws://127.0.0.1:$PORT/" npx playwright test --project=visual "$@"

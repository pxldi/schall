#!/usr/bin/env bash
#
# Fails when a SCHALL_* variable a deployment can set is absent from
# .env.example, so a knob added without being written down fails the pull
# request that added it rather than surfacing months later on somebody's deploy.
#
# There are two namespaces, and the check reads each from the one file that owns
# it. They must not be conflated — they are set in different places and mean
# different things — but both are things an operator configures, and either can
# drift out of .env.example the same way:
#
#   Application config — read by internal/config/config.go through os.Getenv and
#   delivered to the container by compose.yaml's `environment:`.
#
#   Compose host settings — the ${...} interpolations in compose.yaml that set
#   the container user and the host folders it binds. Never read by Go.
#
# Taking the first set from the three call shapes in config.go that read an
# environment variable, rather than grepping SCHALL_ across internal/, is what
# keeps three other kinds of name out of it: the test-only variables that gate
# skipped suites, the SCHALL_TAGGED_AT and SCHALL_TAGS_AS_ARRIVED tag keys
# written into audio files, and the SCHALL_* values compose.yaml hard-codes into
# `environment:`, which are that file's own business and not an operator's.
#
# The check runs in one direction only, and deliberately. A name in .env.example
# that neither file demands is not stale — it may be documenting something for a
# reader — so it is never reported, and nothing here ever asks for a line to be
# deleted.

set -euo pipefail

cd "$(dirname "$0")/.."

CONFIG_FILE=internal/config/config.go
COMPOSE_FILE=compose.yaml
ENV_EXAMPLE=.env.example

# Variables one of those files demands and that are intentionally left out of
# .env.example. Empty, and it should stay that way — an entry here is an
# undocumented knob somebody signed for, which is why it lives in the diff
# rather than in their head. Add the name and a comment saying why.
INTENTIONALLY_UNDOCUMENTED=()

# The names config.go reads. The newlines go first because gofmt splits the
# longer calls across lines, putting the name on a line of its own.
config_variables() {
	tr '\n' ' ' <"$CONFIG_FILE" |
		grep -oE '(os\.Getenv|envOrDefault|envDuration)\([[:space:]]*"SCHALL_[A-Z0-9_]+"' |
		grep -oE 'SCHALL_[A-Z0-9_]+' |
		sort -u || true
}

# The names compose.yaml interpolates: ${SCHALL_MUSIC_PATH:-./music} and its
# kind. A default written into the interpolation does not make the variable
# undocumentable — it is exactly the default .env.example should be showing.
compose_variables() {
	grep -oE '\$\{SCHALL_[A-Z0-9_]+' "$COMPOSE_FILE" |
		grep -oE 'SCHALL_[A-Z0-9_]+' |
		sort -u || true
}

# Keys .env.example carries, commented or not. A commented default is
# documentation — it is how a variable gets described without changing what an
# installation that copies this file does.
documented_variables() {
	grep -oE '^#? *SCHALL_[A-Z0-9_]+=' "$ENV_EXAMPLE" |
		grep -oE 'SCHALL_[A-Z0-9_]+' |
		sort -u || true
}

for file in "$CONFIG_FILE" "$COMPOSE_FILE" "$ENV_EXAMPLE"; do
	if [ ! -f "$file" ]; then
		echo "check-env-example: $file not found; this check needs updating" >&2
		exit 1
	fi
done

config_names=$(config_variables)
compose_names=$(compose_variables)
required=$(printf '%s\n%s\n' "$config_names" "$compose_names" | grep . | sort -u || true)
documented=$(documented_variables)

# A refactor that moved configuration out of config.go, or renamed its helpers,
# would otherwise leave the check passing on an empty set and saying nothing.
# Compose is not asserted the same way: a deployment that stopped making its
# mounts configurable is a real change rather than a broken check.
if [ -z "$config_names" ]; then
	echo "check-env-example: no variables found in $CONFIG_FILE; this check needs updating" >&2
	exit 1
fi

status=0

for name in "${INTENTIONALLY_UNDOCUMENTED[@]+"${INTENTIONALLY_UNDOCUMENTED[@]}"}"; do
	if ! grep -qx "$name" <<<"$required"; then
		echo "check-env-example: $name is listed as intentionally undocumented but nothing demands it any more; drop it from the list" >&2
		status=1
	elif grep -qx "$name" <<<"$documented"; then
		echo "check-env-example: $name is listed as intentionally undocumented but $ENV_EXAMPLE documents it; drop it from the list" >&2
		status=1
	fi
done

missing=()
while IFS= read -r name; do
	[ -n "$name" ] || continue
	grep -qx "$name" <<<"$documented" && continue
	for allowed in "${INTENTIONALLY_UNDOCUMENTED[@]+"${INTENTIONALLY_UNDOCUMENTED[@]}"}"; do
		[ "$name" = "$allowed" ] && continue 2
	done
	if grep -qx "$name" <<<"$config_names"; then
		missing+=("$name (read by $CONFIG_FILE)")
	else
		missing+=("$name (interpolated in $COMPOSE_FILE)")
	fi
done <<<"$required"

if [ ${#missing[@]} -gt 0 ]; then
	{
		echo "check-env-example: $ENV_EXAMPLE does not document:"
		printf '  %s\n' "${missing[@]}"
		echo
		echo "Add each one to $ENV_EXAMPLE with its default and a line saying what changes"
		echo "when it is unset. A commented default documents it without changing behaviour."
		echo "If a variable is meant to stay undocumented, add it to"
		echo "INTENTIONALLY_UNDOCUMENTED in $0 with a comment saying why."
	} >&2
	exit 1
fi

exit $status

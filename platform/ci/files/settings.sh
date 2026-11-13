#!/bin/sh
# The golden path's settings (templates/pipeline.yaml, docs/how-to-guides/ci.md): what to build from the repository
# checked out in $SRC, and whether this run publishes. A repository chooses only context, containerfile and image in
# an optional .ci.yaml; anything else fails the run. Writes build, publish, context, containerfile and image to $OUT.
# Needs yq (mikefarah). Inputs: SRC, OUT, REPO (owner/name), EVENT, REF, DEFAULT_BRANCH.
set -eu

fail() {
	echo "settings: $*" >&2
	exit 1
}

# A path inside the repository: relative, no .. segment
inside() {
	case "/$1/" in
	//* | */../* | *//*) return 1 ;;
	esac
	case "$1" in /*) return 1 ;; esac
	return 0
}

context=.
containerfile=
image=${REPO##*/}
if [ -e "$SRC/.ci.yaml" ]; then
	yq -e 'tag == "!!map"' "$SRC/.ci.yaml" >/dev/null 2>&1 || fail ".ci.yaml is not a map"
	unknown=$(yq -r 'keys | map(select(. != "context" and . != "containerfile" and . != "image")) | join(" ")' "$SRC/.ci.yaml")
	[ -z "$unknown" ] || fail ".ci.yaml: unknown keys: $unknown (known: context, containerfile, image)"
	for key in context containerfile image; do
		if [ "$(yq -r "has(\"$key\")" "$SRC/.ci.yaml")" = true ]; then
			[ "$(yq -r ".$key | tag" "$SRC/.ci.yaml")" = '!!str' ] || fail ".ci.yaml: $key is not a string"
			value=$(yq -r ".$key" "$SRC/.ci.yaml")
			case $key in
			context) context=$value ;;
			containerfile) containerfile=$value ;;
			image) image=$value ;;
			esac
		fi
	done
fi

inside "$context" || fail "context $context leaves the repository"
[ -d "$SRC/$context" ] || fail "context $context is not a directory"
if [ -n "$containerfile" ]; then
	inside "$containerfile" || fail "containerfile $containerfile leaves the repository"
	[ -f "$SRC/$containerfile" ] || fail "containerfile $containerfile does not exist"
elif [ -f "$SRC/$context/Containerfile" ]; then
	containerfile=${context%/}/Containerfile
elif [ -f "$SRC/$context/Dockerfile" ]; then
	containerfile=${context%/}/Dockerfile
fi
containerfile=${containerfile#./}
echo "$image" | grep -Eq '^[a-z0-9][a-z0-9._-]*$' || fail "image $image is not a repository name ([a-z0-9][a-z0-9._-]*)"

build=false
[ -z "$containerfile" ] || build=true
publish=false
if [ "$EVENT" = push ] && [ "$REF" = "refs/heads/$DEFAULT_BRANCH" ]; then
	publish=true
fi

printf '%s' "$build" >"$OUT/build"
printf '%s' "$publish" >"$OUT/publish"
printf '%s' "$context" >"$OUT/context"
printf '%s' "$containerfile" >"$OUT/containerfile"
printf '%s' "$image" >"$OUT/image"
echo "settings: build=$build publish=$publish context=$context containerfile=${containerfile:-none} image=$image"

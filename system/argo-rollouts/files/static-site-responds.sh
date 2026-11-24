#!/bin/sh
# Before a blue-green switch (static-site-responds): the preview's page and every same-origin stylesheet, script, icon
# and image it names must answer 2xx. A page whose hashed stylesheet is missing would load unstyled; other origins (a
# CDN font) are not ours to check. Env: URL, the page (http://<service>.<namespace>:<port>/). POSIX sh, curl, grep, sed:
# what the curl image has
set -u
origin=$(printf '%s' "$URL" | sed -E 's#^(https?://[^/]+).*#\1#')
dir=$(printf '%s' "$URL" | sed -E 's#[^/]*$##')
get() { curl -fsS -o "$2" --max-time 10 --retry 3 --retry-all-errors "$1"; }

page=$(mktemp)
if ! get "$URL" "$page"; then
	echo "$URL: no 2xx"
	exit 1
fi
failed=0
checked=0
# A value double-quoted, single-quoted or bare (hugo --minify drops the quotes: href=/assets/css/stylesheet.<hash>.css)
refs=$(mktemp)
grep -oiE '<(link|script|img)[^>]*>' "$page" |
	grep -oiE "(href|src)=(\"[^\"]*\"|'[^']*'|[^ >\"']+)" |
	sed -E "s/^[^=]*=//; s/^[\"']//; s/[\"']\$//" >"$refs"
while IFS= read -r ref; do
	case $ref in
	data:* | '#'* | mailto:*) continue ;;
	"$origin"/*) target=$ref ;;
	http://* | https://* | //*) continue ;; # another origin
	/*) target=$origin$ref ;;
	*) target=$dir$ref ;;
	esac
	checked=$((checked + 1))
	if ! get "$target" /dev/null 2>/dev/null; then
		echo "$ref ($target): no 2xx"
		failed=1
	fi
done <"$refs"
[ "$failed" = 0 ] && echo "$URL and its $checked assets answered"
exit "$failed"

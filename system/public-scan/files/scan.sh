#!/bin/sh
# The weekly scan of the public hosts (docs/how-to-guides/public-surface.md). Each host must first answer an ordinary
# request normally, before and after its own scan: an error or a Cloudflare challenge (the WAF may start one mid-run)
# means the scan would not see the app, so the run fails (PublicScanFailed) instead of reporting a clean scan. nuclei then scans each host with the templates accepted on it
# excluded, into a file in the pod; the log (Loki: PublicScanFinding) gets one short JSON line per finding with its
# template, host and severity only, since nuclei's own lines hold the request as a curl command and what the template
# extracted from the response. A run that loaded too few templates fails too.
#
# PUBLIC_SCAN_TARGETS: a file of "<label> <accepted ids, comma-separated, or ->" lines; PUBLIC_SCAN_DOMAIN: the hosts'
# domain; PUBLIC_SCAN_TEMPLATES_VERSION and _SHA256: the templates' release; PUBLIC_SCAN_RATE: requests per second;
# PUBLIC_SCAN_MIN_TEMPLATES; PUBLIC_SCAN_CHECK_ONLY=1 runs the preflight alone (test/public_scan_test.go);
# PUBLIC_SCAN_SCHEME (https) and PUBLIC_SCAN_TEMPLATES_URL for the tests; PUBLIC_SCAN_REPORT=<file> <label> prints a
# nuclei JSONL file's report lines
set -eu

scheme="${PUBLIC_SCAN_SCHEME:-https}"
work="${TMPDIR:-/tmp}/public-scan"
mkdir -p "$work"

# host <label>: the host name of a target
host() { echo "$1${PUBLIC_SCAN_DOMAIN:+.$PUBLIC_SCAN_DOMAIN}"; }

# preflight <label>: the host answers an ordinary request with the app (wget follows redirects and fails on 4xx/5xx)
preflight() {
	name=$(host "$1")
	: >"$work/body"
	answered=0
	wget -q -S -T 30 -O "$work/body" "$scheme://$name/" 2>"$work/headers" || answered=$?
	# A challenge page served with 200; busybox wget prints no headers on an error status, so a 403 challenge is the
	# error below
	if grep -qi '^ *cf-mitigated:' "$work/headers" || grep -q '<title>Just a moment...</title>' "$work/body"; then
		echo "public-scan: Cloudflare challenged the scan on $name" >&2
		return 1
	fi
	if [ "$answered" != 0 ]; then
		echo "public-scan: $name did not answer normally (an error, or Cloudflare blocking or challenging the scan): the scan cannot see it" >&2
		return 1
	fi
}

# report <label> <nuclei's JSONL>: one line per finding, template id and severity from nuclei's line (both plain
# words); a finding it cannot read fails rather than disappears
report() {
	name=$(host "$1")
	sed -n 's/.*"template-id":"\([A-Za-z0-9_.-]*\)".*"severity":"\([a-z]*\)".*/\1 \2/p' "$2" >"$work/report"
	while read -r template severity; do
		echo "{\"public_scan\":\"finding\",\"template\":\"$template\",\"host\":\"$name\",\"severity\":\"$severity\"}"
	done <"$work/report"
	# grep -c exits 1 on a count of 0
	unread=$(($(grep -c . "$2" || true) - $(grep -c . "$work/report" || true)))
	if [ "$unread" != 0 ]; then
		echo "public-scan: $unread findings on $name could not be read" >&2
		return 1
	fi
}

if [ -n "${PUBLIC_SCAN_REPORT:-}" ]; then
	report "$1" "$PUBLIC_SCAN_REPORT"
	exit 0
fi

failed=0
while read -r label _; do
	[ -n "$label" ] || continue
	preflight "$label" || failed=1
done <"$PUBLIC_SCAN_TARGETS"
[ "$failed" = 0 ] || exit 1
[ "${PUBLIC_SCAN_CHECK_ONLY:-0}" != 1 ] || exit 0

templates="$work/templates"
wget -q -T 60 -O "$work/templates.tar.gz" \
	"${PUBLIC_SCAN_TEMPLATES_URL:-https://github.com/projectdiscovery/nuclei-templates/archive/refs/tags/$PUBLIC_SCAN_TEMPLATES_VERSION.tar.gz}"
echo "$PUBLIC_SCAN_TEMPLATES_SHA256  $work/templates.tar.gz" | sha256sum -c -s || {
	echo "public-scan: the templates' tarball does not match its sha256" >&2
	exit 1
}
mkdir -p "$templates"
tar -xzf "$work/templates.tar.gz" -C "$templates" --strip-components 1

loaded=0
while read -r label accepted; do
	[ -n "$label" ] || continue
	# Each host again right before its scan: the hosts before it took hours
	preflight "$label" || exit 1
	rm -f "$work/found.jsonl"
	set -- -u "$scheme://$(host "$label")" -t "$templates" -ud "$templates" -jsonl-export "$work/found.jsonl" -omit-raw \
		-omit-template -no-color -rl "$PUBLIC_SCAN_RATE" -c 5 \
		-etags dos,fuzz,intrusive -no-interactsh -disable-update-check
	[ "$accepted" = - ] || set -- "$@" -exclude-id "$accepted"
	# stderr holds nuclei's progress: only its template count is kept
	# stdin from /dev/null: nuclei reads targets from a stdin that is not a terminal, which would eat the loop's lines
	nuclei "$@" </dev/null >/dev/null 2>"$work/nuclei.err" || {
		echo "public-scan: nuclei failed on $(host "$label")" >&2
		exit 1
	}
	count=$(sed -n 's/.*Templates loaded for current scan: \([0-9]*\).*/\1/p' "$work/nuclei.err" | tail -n 1)
	if [ "${count:-0}" -lt "$PUBLIC_SCAN_MIN_TEMPLATES" ]; then
		echo "public-scan: nuclei loaded ${count:-no} templates for $(host "$label"), under $PUBLIC_SCAN_MIN_TEMPLATES" >&2
		exit 1
	fi
	# And after it: a host Cloudflare began challenging mid-scan answered nuclei with challenges only
	preflight "$label" || exit 1
	[ ! -f "$work/found.jsonl" ] || report "$label" "$work/found.jsonl" || exit 1
	loaded=$count
done <"$PUBLIC_SCAN_TARGETS"
echo "{\"public_scan\":\"done\",\"templates\":$loaded}"

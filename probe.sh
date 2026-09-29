#!/usr/bin/env bash
set -uo pipefail

usage() {
  echo "usage: [TOKEN=<access token>] $0 [-m hold|drip] [-i interval] [-n] <base-url> <duration>..." >&2
  echo "example: TOKEN=\$token $0 https://alpha.test.felleskomponent.no/core/gateway-timeout 60 110 130 180" >&2
  exit 1
}

mode=hold
interval=10
no_keepalive=""

while getopts "m:i:n" opt; do
  case "$opt" in
    m) mode="$OPTARG" ;;
    i) interval="$OPTARG" ;;
    n) no_keepalive=yes ;;
    *) usage ;;
  esac
done
shift $((OPTIND - 1))

[ $# -ge 2 ] || usage
[ "$mode" = hold ] || [ "$mode" = drip ] || usage

base_url="${1%/}"
shift
run_id="$(date +%H%M%S)"

for duration in "$@"; do
  url="$base_url/$mode/$duration?probe=$run_id-$mode-$duration"
  if [ "$mode" = drip ]; then
    url="$url&interval=$interval"
  fi

  curl_args=(-sS --http1.1 -o /dev/null -w 'status=%{http_code} first_byte=%{time_starttransfer}s total=%{time_total}s')
  if [ -n "$no_keepalive" ]; then
    curl_args+=(--no-keepalive)
  fi
  if [ -n "${TOKEN:-}" ]; then
    curl_args+=(-H "Authorization: Bearer $TOKEN")
  fi

  error_file="$(mktemp)"
  timings="$(curl "${curl_args[@]}" "$url" 2>"$error_file")"
  error="$(tr -d '\n' < "$error_file")"
  rm -f "$error_file"

  echo "$mode=$duration probe=$run_id-$mode-$duration $timings${error:+ error=\"$error\"}"
done

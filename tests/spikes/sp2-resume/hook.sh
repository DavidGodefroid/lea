#!/bin/sh
# SessionStart hooks for spike SP2 (#155). $1 = sync | async-ctx | async-wake.
# Logs the source; on source=resume injects a secret word, one way per mode.
mode="$1"; in=$(cat)
src=$(printf '%s' "$in" | python3 -c 'import json,sys; print(json.load(sys.stdin).get("source",""))')
printf '%s %s source=%s\n' "$(date +%T)" "$mode" "$src" >> "$(dirname "$0")/hooks.log"
[ "$src" = resume ] || exit 0
case "$mode" in
sync)       printf '{"hookSpecificOutput":{"hookEventName":"SessionStart","additionalContext":"The secret word is PAPAYA."}}\n' ;;
async-ctx)  sleep "${SLEEP:-2}"; printf '{"hookSpecificOutput":{"hookEventName":"SessionStart","additionalContext":"The secret word is MANGO."}}\n' ;;
async-wake) sleep 2; echo "The secret word is GUAVA. Reply with it." >&2; exit 2 ;;
esac
exit 0

#!/bin/sh
# Logs every hook event to hooks.log; on PostToolUse ends the turn with continue:false.
ev="$1"; in=$(cat)
printf '%s %s %s\n' "$(date +%T)" "$ev" "$in" >> "$(dirname "$0")/hooks.log"
if [ "$ev" = PostToolUse ]; then
  printf '{"continue":false,"stopReason":"waiting for approval of ticket 42"}\n'
fi
exit 0

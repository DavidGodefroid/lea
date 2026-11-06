# shellcheck shell=sh
# lea update's dependencies: the Locks' clients in lea's image come from one set of releases,
# config/locks.lock (lea#134), which lea's CI bumps as a whole and each lea release ships as it is:
# updating lea is updating them, together. lea installs, builds and starts no Lock: lox does (the
# Homebrew tap, lox add, lox run), and lea doctor compares each lock lox runs with lea's client.

# lea_update_deps ask|all|none: the last step of lea update, run by the installation lea update
# just installed: it says which set of Lock clients this lea has, and what is lox's to update. It
# installs nothing, whatever the mode (kept for lea update --deps). Never fatal.
lea_update_deps() {
  case ${1:-ask} in ask|all|none) ;; *) lea_die "usage: lea update [--deps ask|all|none]" ;; esac
  lea_log "Lock clients in lea's image: $(lea__lockset_summary) (config/locks.lock, one set: lea update moves them together)"
  for _ud_d in locksql lockmq lockfile; do
    _ud_r=$(eval "printf '%s' \"\${$(lea__tool_var "$_ud_d")_REF:-}\"")
    if [ -n "$_ud_r" ]; then
      lea_log "warning: $(lea__tool_var "$_ud_d")_REF=$_ud_r overrides the set's $_ud_d $(lea__lock_version "$_ud_d") (a dev override, for this release only): remove it from $(lea_user_config_path)"
    fi
  done
  lea_log "the locks themselves are lox's: brew upgrade lox, then lox add TYPE NAME --version vX.Y.Z (lea doctor says which)"
}

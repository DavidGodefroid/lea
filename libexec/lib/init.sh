# shellcheck shell=sh
# lea init: launcher link, projects dir, lea's git SSH key (only with LEA_GIT_SSH), Claude
# configuration copy, images, locksql's profile wizard (with LEA_LOCKSQL=1), lockmq and lockfile
# wiring (asked; their consoles run under lox, libexec/lib/lockhost.sh), AppArmor profile. lea init
# installs and builds no Lock: lox does.

lea_init_apparmor() {
  docker info --format '{{json .SecurityOptions}}' 2>/dev/null | grep -q 'name=apparmor' || return 0
  command -v apparmor_parser >/dev/null 2>&1 || lea_die "Docker uses AppArmor but apparmor_parser is missing on this host"
  if [ -e "${LEA_APPARMOR_ABI_DIR:-/etc/apparmor.d/abi}/4.0" ]; then _in_abi=4.0 _in_userns='  userns,'; else _in_abi=3.0 _in_userns=''; fi
  _in_out=$(lea_host_dir)/apparmor-lea
  lea_mkdirs "$(lea_data_dir)" "$(lea_host_dir)"
  _in_prof=$(sed -e "s|@ABI@|$_in_abi|" -e "s|^@USERNS@\$|$_in_userns|" "$LEA_ROOT/apparmor/lea.tmpl")
  lea_write_file "$(lea_data_dir)" "$_in_out" <<EOT
$_in_prof
EOT
  _in_dst=${LEA_APPARMOR_DIR:-/etc/apparmor.d}/lea
  if cmp -s "$_in_out" "$_in_dst"; then
    if [ -n "${LEA_FIRST_RUN:-}" ]; then lea_log "first run: AppArmor profile 'lea' up to date"; else lea_log "the AppArmor profile 'lea' is up to date"; fi
    return 0
  fi
  if [ -n "${LEA_FIRST_RUN:-}" ] && ! sudo -n true 2>/dev/null; then
    lea_die "the AppArmor profile 'lea' needs sudo once: run lea init, then lea"
  fi
  lea_log "installing the AppArmor profile 'lea' in ${_in_dst%/*} (needs sudo)"
  sudo install -m 0644 "$_in_out" "$_in_dst"
  sudo apparmor_parser -r "$_in_dst"
}

# The host keys of every LEA_GIT_SSH endpoint come from LEA_GIT_KNOWN_HOSTS (default
# ~/.config/lea/git_known_hosts), pinned by you from a source you trust: the host's own
# known_hosts is never read.
lea__init_known_hosts() {
  _in_kh=$LEA_GIT_KNOWN_HOSTS _in_all=
  set -f
  for _in_ep in $LEA_GIT_SSH; do
    _in_g=${_in_ep%%:*}
    _in_found=$(ssh-keygen -F "$_in_g" -f "$_in_kh" 2>/dev/null | grep -v '^#' || true)
    if [ -z "$_in_found" ]; then
      set +f
      lea_die "no $_in_g key in $_in_kh: pin the host keys of $_in_g there (see the README, SSH)"
    fi
    _in_all="$_in_all$_in_found
"
  done
  set +f
  printf '%s' "$_in_all" | lea_write_file "$(lea_data_dir)" "$1/ssh/known_hosts"
}

# Links the launcher into ~/.local/bin (LEA_BIN_DIR) unless another file already has that name. The
# locksql link an older lea made goes (lea__unlink_locksql).
lea__init_launcher() {
  _in_bd=${LEA_BIN_DIR:-$HOME/.local/bin}
  _in_l=$_in_bd/lea
  if [ -L "$_in_l" ] && [ "$(readlink "$_in_l")" = "$LEA_ROOT/bin/lea" ]; then :
  elif [ -e "$_in_l" ] || [ -L "$_in_l" ]; then
    lea_log "$_in_l exists and is not the lea launcher: left as is"
  else
    mkdir -p "$_in_bd"
    ln -s "$LEA_ROOT/bin/lea" "$_in_l"
  fi
  lea__unlink_locksql "$_in_bd"
  case ":$PATH:" in
    *":$_in_bd:"*) ;;
    *) lea_log "$_in_bd is not on your PATH: add it in your shell profile to run lea by name" ;;
  esac
}

# lea__unlink_locksql DIR: removes DIR/locksql when it is the link an older lea made to its own
# bin/locksql (in an installation that still holds a lea, or one lea update has since pruned):
# locksql on the host is locksql's own launcher (lea uses it no more). Any other file,
# such as that launcher, is left as is.
lea__unlink_locksql() {
  _ul_l=$1/locksql
  [ -L "$_ul_l" ] || return 0
  _ul_t=$(readlink "$_ul_l") || return 0
  case $_ul_t in
    */bin/locksql) if [ -e "${_ul_t%/locksql}/lea" ] || [ ! -e "$_ul_t" ]; then rm -f "$_ul_l"; fi ;;
  esac
}

# Asks for the projects directory (default: the current value, else the working directory),
# checks it with the projects-dir guards and records it in the user config.
lea__init_projects() {
  _in_pd=${LEA_PROJECTS_DIR:-$(pwd -P)}
  _in_pa=
  if [ -z "${LEA_INIT_NONINTERACTIVE:-}" ]; then
    printf 'Projects directory [%s]: ' "$_in_pd" >&2
    IFS= read -r _in_pa || _in_pa=
  fi
  _in_p=${_in_pa:-$_in_pd}
  # A leading ~ typed at the prompt is not expanded by read: expand it here.
  # shellcheck disable=SC2088
  case $_in_p in "~") _in_p=$HOME ;; "~/"*) _in_p=$HOME/${_in_p#"~/"} ;; esac
  if [ -n "${LEA_FIRST_RUN:-}" ]; then
    _in_why="$_in_p is not a directory"
    if [ -d "$_in_p" ]; then
      _in_p=$(lea_abspath "$_in_p")
      _in_why=
      # The guard dies with its reason: take it, and say what to run instead.
      _in_g=$( (lea__guard_projects "$_in_p") 2>&1 ) || _in_why=${_in_g#lea: error: }
    fi
    [ -z "$_in_why" ] || lea_die "first run: $_in_why: cd into the directory that holds your projects and run lea again, or LEA_PROJECTS_DIR=DIR lea"
  else
    [ -d "$_in_p" ] || lea_die "$_in_p is not a directory"
    _in_p=$(lea_abspath "$_in_p")
    lea__guard_projects "$_in_p"
  fi
  lea_user_config_set LEA_PROJECTS_DIR "$_in_p"
  LEA_PROJECTS_DIR=$_in_p
  export LEA_PROJECTS_DIR
  if [ -n "${LEA_FIRST_RUN:-}" ]; then
    lea_log "first run: projects directory $_in_p (saved in $(lea_user_config_path))"
  else
    lea_log "projects directory: $_in_p (saved in $(lea_user_config_path))"
  fi
}

lea__init_ssh_key() {
  lea_safe_path "$(lea_data_dir)" "$1/ssh/id_ed25519"
  lea_safe_path "$(lea_data_dir)" "$1/ssh/id_ed25519.pub"
  if [ ! -f "$1/ssh/id_ed25519" ]; then
    lea_log "generating lea's git SSH key (a passphrase is optional)"
    if [ -n "${LEA_INIT_NONINTERACTIVE:-}" ]; then
      ssh-keygen -q -t ed25519 -N '' -C "lea-$(lea_user_name)@$(uname -n)" -f "$1/ssh/id_ed25519"
    else
      ssh-keygen -t ed25519 -C "lea-$(lea_user_name)@$(uname -n)" -f "$1/ssh/id_ed25519"
    fi
  fi
  lea_log "lea's public key: add it to your SSH keys on $(printf '%s' "$LEA_GIT_SSH" | sed 's/:[0-9]*//g'):"
  cat "$1/ssh/id_ed25519.pub" >&2
}

lea__init_claude_json() {
  lea_safe_path "$(lea_data_dir)" "$1/claude.json"
  [ -f "$1/claude.json" ] && return 0
  if [ -f "$HOME/.claude.json" ]; then
    lea_write_file "$(lea_data_dir)" "$1/claude.json" <"$HOME/.claude.json"
  else
    lea_write_file "$(lea_data_dir)" "$1/claude.json" <<'EOT'
{}
EOT
  fi
  chmod 600 "$1/claude.json"
}

# The locksql configuration of the projects directory: lox up reviews it and copies it into the
# lock (lockhub's docs/lox-up.md).
lea__locksql_config() { printf '%s' "$LEA_PROJECTS_DIR/.locksql/config.toml"; }

# Whether a profile of the projects directory reads its login from Vault: the console image then
# needs the vault CLI (locksql's vault build target).
lea__locksql_wants_vault() {
  [ -n "${LEA_PROJECTS_DIR:-}" ] && grep -q '^[[:space:]]*credentials[[:space:]]*=[[:space:]]*"vault"' "$(lea__locksql_config)" 2>/dev/null
}

# lea__declared_locks TYPE: the locks of TYPE that the projects directory's
# .lockfamily/lockfamily.yaml declares (lox add writes it), found by their type, never their name:
# one line per lock, NAME IMAGE UIDS, IMAGE - when the lock pins none, UIDS its allowed_uids
# separated by commas (- for none). Nothing without the file.
lea__declared_locks() {
  [ -n "${LEA_PROJECTS_DIR:-}" ] && [ -f "$LEA_PROJECTS_DIR/.lockfamily/lockfamily.yaml" ] || return 0
  awk -v want="$1" '
    function ind(s) { match(s, /^ */); return RLENGTH }
    function unq(s) { gsub(/^["'\'']|["'\'']$/, "", s); return s }
    function val(s) { sub(/^[^:]*:[ \t]*/, "", s); sub(/[ \t]+#.*$/, "", s); return unq(s) }
    function flush() {
      if (name != "" && typ == want) printf "%s %s %s\n", name, (img == "" ? "-" : img), (uids == "" ? "-" : uids)
      name = ""; typ = ""; img = ""; uids = ""; inuids = 0; fi = -1
    }
    { sub(/\r$/, "") }
    /^[ \t]*(#|$)/ { next }
    { i = ind($0); line = substr($0, i + 1) }
    i == 0 { flush(); inlocks = (line ~ /^locks:[ \t]*$/); li = -1; next }
    !inlocks { next }
    li < 0 { li = i }
    i <= li { flush(); n = line; sub(/[ \t]*:.*$/, "", n); name = unq(n); next }
    inuids && line ~ /^-/ { v = line; sub(/^-[ \t]*/, "", v); uids = uids (uids == "" ? "" : ",") v; next }
    fi < 0 { fi = i }
    i != fi { next }
    { inuids = 0 }
    line ~ /^type:/ { typ = val(line) }
    line ~ /^image:/ { img = val(line) }
    line ~ /^allowed_uids:/ { v = val(line); if (v == "") inuids = 1; else { gsub(/[][ \t]/, "", v); uids = v } }
    END { flush() }
  ' "$LEA_PROJECTS_DIR/.lockfamily/lockfamily.yaml"
}

# lea__locksql_declared_hint LOCKS: lea init's lines for the locksql locks lea__declared_locks
# found: each one whose allowed_uids lacks your uid (its console refuses lea's agents), then the
# one step left.
lea__locksql_declared_hint() {
  _ld_u=$(id -u)
  _ld_n=$(printf '%s\n' "$1" | awk '{ printf "%s%s", (NR > 1 ? ", " : ""), $1 }')
  printf '%s\n' "$1" | while read -r _ld_name _ _ld_uids; do
    case ,$_ld_uids, in
      *,"$_ld_u",*) ;;
      *) lea_log "warning: the locksql lock $_ld_name has no allowed_uids [$_ld_u] in .lockfamily/lockfamily.yaml: its console refuses lea's agents (they run as your uid); add it, then lox up" ;;
    esac
  done
  lea_log "locksql: lox up runs the locksql lock(s) $_ld_n of .lockfamily/lockfamily.yaml; next: lea stop, then lea, so lea's agents get the socket"
}

# lea__ask QUESTION: a yes/no question, yes by default; no answer (end of input) is no.
lea__ask() {
  printf '%s [Y/n] ' "$1" >&2
  IFS= read -r _ak_a || return 1
  case $_ak_a in ''|[yY]|[yY][eE][sS]) return 0 ;; *) return 1 ;; esac
}

# lea__ask_no QUESTION: a yes/no question, no by default.
lea__ask_no() {
  printf '%s [y/N] ' "$1" >&2
  IFS= read -r _ak_a || return 1
  case $_ak_a in [yY]|[yY][eE][sS]) return 0 ;; *) return 1 ;; esac
}

# lea__init_tty: whether lea init talks to a terminal (the wizard and vault login need one).
lea__init_tty() { [ -t 0 ]; }

# lea init's guided locksql setup: a first database profile for the projects directory when it
# has none (the wizard of locksql profile add, run as you with the locksql client of lea's image,
# config/locks.lock's, and only the projects directory mounted: it asks for no password), then
# what a Vault profile needs. Skipped without a terminal; never fatal.
lea__init_locksql_profile() {
  [ -z "${LEA_INIT_NONINTERACTIVE:-}" ] && lea__init_tty || return 0
  docker image inspect "$LEA_IMAGE" >/dev/null 2>&1 || return 0
  _ip_cfg=$(lea__locksql_config)
  if [ -L "$LEA_PROJECTS_DIR/.locksql" ] || [ -L "$_ip_cfg" ]; then
    lea_log "warning: $LEA_PROJECTS_DIR/.locksql or its config.toml is a symbolic link: locksql profile add not offered (remove it, then lea init)"
    return 0
  fi
  if ! grep -q '^[[:space:]]*\[profiles\.' "$_ip_cfg" 2>/dev/null; then
    lea_log "locksql has no database profile for $LEA_PROJECTS_DIR yet"
    lea__ask "Add one now (engine, host, where the password comes from; never the password)?" || {
      lea_log "later: lea init again, or edit $_ip_cfg"
      return 0
    }
    if ! docker run -it --rm --network none --user "$(id -u):$(id -g)" -e HOME=/tmp -v "$LEA_PROJECTS_DIR:$LEA_PROJECTS_DIR" \
      -w "$LEA_PROJECTS_DIR" --entrypoint locksql "$LEA_IMAGE" profile add --project "$LEA_PROJECTS_DIR"; then
      lea_log "warning: no profile added (lea init again to retry)"
      return 0
    fi
  fi
  if lea__locksql_wants_vault; then
    lea_log "locksql: a profile reads its login from Vault: lox up passes VAULT_ADDR to locks with vault: true (lox add locksql)"
  fi
  lea__locksql_console_hint
}

# lea__locksql_console_hint: how the locksql console runs (lea never starts it): hub-attached
# only, under lox up, as lockmq's and lockfile's (lea__lockhost_console_hint).
lea__locksql_console_hint() {
  lea_log "locksql console: it runs under lox up only: no such lock yet: \`lox add locksql\` (lox add fills allowed_uids: [$(id -u)] in .lockfamily/lockfamily.yaml when lea is on your PATH; lea's agents get its socket once lea restarts: lea stop, then lea)"
  lea_log "  its own questions (policy review, credentials): lox attach NAME, or lea lock open locksql in the deck; each query of an agent: lea's deck (hub view) or lox approve"
}

# lea__init_wants TOOL: whether this lea init configures TOOL: every tool on a bare lea init; only
# the tools named by a --TOOL or --no-TOOL flag otherwise (the others keep their setting, unasked).
lea__init_wants() {
  [ -z "$_in_only" ] && return 0
  case " $_in_only " in *" $1 "*) return 0 ;; *) return 1 ;; esac
}

lea_init() {
  _in_only=
  for _in_a in "$@"; do
    case $_in_a in
      --no-locksql | --locksql | --lockmq | --no-lockmq | --lockfile | --no-lockfile)
        _in_t=${_in_a#--}
        _in_only="$_in_only ${_in_t#no-}" ;;
    esac
    case $_in_a in
      --no-locksql) lea_user_config_set LEA_LOCKSQL 0; LEA_LOCKSQL=0 ;;
      --locksql) lea_user_config_set LEA_LOCKSQL 1; LEA_LOCKSQL=1 ;;
      --lockmq) lea__lockhost_set lockmq 1 ;;
      --no-lockmq) lea__lockhost_set lockmq 0 ;;
      --lockfile) lea__lockhost_set lockfile 1 ;;
      --no-lockfile) lea__lockhost_set lockfile 0 ;;
      --first-run) LEA_INIT_NONINTERACTIVE=1 LEA_FIRST_RUN=1 ;;
      *) lea_die "usage: lea init [--no-locksql | --locksql] [--lockmq | --no-lockmq] [--lockfile | --no-lockfile] [--first-run]" ;;
    esac
  done
  _in_s=$(lea_state_dir)
  _in_um=$(umask)
  umask 077
  _in_h=$(lea_host_dir)
  lea__init_launcher
  lea__init_projects
  lea_mkdirs "$(lea_data_dir)" "$_in_s/ssh" "$_in_s/git" "$_in_s/gh" "$_in_s/containers" "$_in_s/glab-cli" "$_in_s/go" "$_in_s/kube" \
    "$_in_s/claude" "$_in_h/scan" "$_in_h/sessions" "$_in_h/locks"
  if [ -n "$LEA_GIT_SSH" ]; then
    lea__init_known_hosts "$_in_s"
    lea__init_ssh_key "$_in_s"
  fi
  lea__init_claude_json "$_in_s"
  lea_ensure_images
  if [ "$LEA_LOCKSQL" = 1 ] && lea__init_wants locksql; then
    _in_l=$(lea__declared_locks locksql)
    if [ -n "$_in_l" ]; then lea__locksql_declared_hint "$_in_l"; else lea__init_locksql_profile; fi
  fi
  if lea__init_wants lockmq; then lea__init_lockhost lockmq; fi
  if lea__init_wants lockfile; then lea__init_lockhost lockfile; fi
  lea_init_apparmor
  if [ -z "${LEA_FIRST_RUN:-}" ]; then lea_log "ready. Run: lea"; fi
  umask "$_in_um"
}

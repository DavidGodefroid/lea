# shellcheck shell=sh
# lea init: launcher link, projects dir, lea's git SSH key (only with LEA_GIT_SSH), Claude
# configuration copy, images, the locksql console image (unless LEA_LOCKSQL=0), AppArmor profile
# (last: the only sudo step).

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
    lea_log "the AppArmor profile 'lea' is up to date"
    return 0
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

# Links the launcher into ~/.local/bin (LEA_BIN_DIR) unless another file already has that name.
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
  case ":$PATH:" in
    *":$_in_bd:"*) ;;
    *) lea_log "$_in_bd is not on your PATH: add it in your shell profile to run lea by name" ;;
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
  [ -d "$_in_p" ] || lea_die "$_in_p is not a directory"
  _in_p=$(lea_abspath "$_in_p")
  lea__guard_projects "$_in_p"
  lea_user_config_set LEA_PROJECTS_DIR "$_in_p"
  LEA_PROJECTS_DIR=$_in_p
  export LEA_PROJECTS_DIR
  lea_log "projects directory: $_in_p (saved in $(lea_user_config_path))"
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

# The locksql console image (LEA_LOCKSQL_IMAGE), built from locksql's own repository at
# LEA_LOCKSQL_REF when it is not there yet. Never fatal: lea runs without it. lea init and lea
# update (lea_refresh_images) both call it.
lea__init_locksql() {
  if docker image inspect "$LEA_LOCKSQL_IMAGE" >/dev/null 2>&1; then
    lea_log "locksql console image $LEA_LOCKSQL_IMAGE: present"
    return 0
  fi
  lea_log "building the locksql console image $LEA_LOCKSQL_IMAGE from locksql $LEA_LOCKSQL_REF (LEA_LOCKSQL=0 skips it)"
  if docker build -t "$LEA_LOCKSQL_IMAGE" "https://github.com/DavidGodefroid/locksql.git#$LEA_LOCKSQL_REF" >&2; then
    return 0
  fi
  lea_log "warning: could not build $LEA_LOCKSQL_IMAGE; lea works without it (rerun lea init or lea update, or set LEA_LOCKSQL=0)"
}

lea_init() {
  for _in_a in "$@"; do
    case $_in_a in
      --no-locksql) lea_user_config_set LEA_LOCKSQL 0; LEA_LOCKSQL=0 ;;
      --locksql) lea_user_config_set LEA_LOCKSQL 1; LEA_LOCKSQL=1 ;;
      *) lea_die "usage: lea init [--no-locksql | --locksql]" ;;
    esac
  done
  _in_s=$(lea_state_dir)
  _in_um=$(umask)
  umask 077
  _in_h=$(lea_host_dir)
  lea__init_launcher
  lea__init_projects
  lea_mkdirs "$(lea_data_dir)" "$_in_s/ssh" "$_in_s/git" "$_in_s/gh" "$_in_s/containers" "$_in_s/glab-cli" "$_in_s/kube" \
    "$_in_s/claude" "$_in_h/scan" "$_in_h/sessions" "$_in_h/locks"
  if [ -n "$LEA_GIT_SSH" ]; then
    lea__init_known_hosts "$_in_s"
    lea__init_ssh_key "$_in_s"
  fi
  lea__init_claude_json "$_in_s"
  lea_ensure_images
  if [ "$LEA_LOCKSQL" = 1 ]; then lea__init_locksql; fi
  lea_init_apparmor
  lea_log "ready. Run: lea"
  umask "$_in_um"
}

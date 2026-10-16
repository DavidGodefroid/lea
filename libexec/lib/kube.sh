# shellcheck shell=sh
# lea kube refresh: mints short-lived read-only tokens with the developer's host kubeconfig.

lea_kube_refresh() ( lea__kube_refresh_run )

# lea__yaml_quote VALUE: VALUE as a double-quoted YAML scalar.
lea__yaml_quote() { printf '"%s"' "$(printf '%s' "$1" | sed 's/[\\"]/\\&/g')"; }

lea__kube_refresh_run() {
  umask 077
  command -v kubectl >/dev/null 2>&1 || lea_die "kubectl not found on the host"
  _ku=${LEA_K8S_USER:-$(lea_user_name)}
  _kd=$(lea_state_dir)/kube
  lea_mkdirs "$(lea_data_dir)" "$_kd"
  lea_safe_path "$(lea_data_dir)" "$_kd/config.new"
  lea_safe_path "$(lea_data_dir)" "$_kd/config"
  _kt=$(mktemp -d)
  trap 'rm -r "$_kt" 2>/dev/null || true' EXIT
  trap 'exit 1' INT TERM
  rm -f "$_kd/config.new"
  : >"$_kt/clusters"; : >"$_kt/users"; : >"$_kt/contexts"
  _kfirst=
  kubectl config get-contexts -o name >"$_kt/names"
  while IFS= read -r _kctx <&3; do
    [ -n "$_kctx" ] || continue
    _kq=$(lea__yaml_quote "$_kctx"); _kqu=$(lea__yaml_quote "lea-$_kctx")
    if ! _ktok=$(kubectl --context "$_kctx" create token "lea-$_ku" -n "$LEA_K8S_NAMESPACE" --duration=8h 2>"$_kt/err"); then
      lea_log "skipped context $_kctx: $(head -n1 "$_kt/err")"
      continue
    fi
    _kserver=$(kubectl --context "$_kctx" config view --raw --minify -o 'jsonpath={.clusters[0].cluster.server}')
    _kca=$(kubectl --context "$_kctx" config view --raw --minify -o 'jsonpath={.clusters[0].cluster.certificate-authority-data}')
    _kcafile=$(kubectl --context "$_kctx" config view --raw --minify -o 'jsonpath={.clusters[0].cluster.certificate-authority}')
    _kinsec=$(kubectl --context "$_kctx" config view --raw --minify -o 'jsonpath={.clusters[0].cluster.insecure-skip-tls-verify}')
    if [ -n "$_kcafile" ] || [ "$_kinsec" = true ]; then
      lea_log "warning: context $_kctx uses certificate-authority <path> or insecure-skip-tls-verify; lea kubeconfig only carries certificate-authority-data"
    fi
    printf -- '- name: %s\n  cluster:\n    server: %s\n' "$_kq" "$(lea__yaml_quote "$_kserver")" >>"$_kt/clusters"
    if [ -n "$_kca" ]; then printf '    certificate-authority-data: %s\n' "$_kca" >>"$_kt/clusters"; fi
    printf -- '- name: %s\n  user:\n    token: %s\n' "$_kqu" "$_ktok" >>"$_kt/users"
    printf -- '- name: %s\n  context:\n    cluster: %s\n    user: %s\n' "$_kq" "$_kq" "$_kqu" >>"$_kt/contexts"
    _kfirst=${_kfirst:-$_kctx}
  done 3<"$_kt/names"
  if [ -z "$_kfirst" ]; then
    lea_die "no cluster issued a token for lea-$_ku; ask the Kubernetes admin to apply k8s/lea-readonly.yaml.tmpl for you (k8s/render.sh)"
  fi
  (
    umask 077
    {
      printf 'apiVersion: v1\nkind: Config\nclusters:\n'; cat "$_kt/clusters"
      printf 'users:\n'; cat "$_kt/users"
      printf 'contexts:\n'; cat "$_kt/contexts"
      printf 'current-context: %s\n' "$(lea__yaml_quote "$_kfirst")"
    } >"$_kd/config.new"
  )
  mv "$_kd/config.new" "$_kd/config"
  lea_log "read-only kubeconfig written, valid 8 h; running lea sessions see it at once"
}

load helpers
setup() { setup_fakes; load_lea; K="$(lea_state_dir)/kube/config"; }

@test "writes a read-only kubeconfig for every context that issues a token" {
  export FAKE_KUBE_CONTEXTS="cluster-uat cluster-prod"
  lea_kube_refresh 2>/dev/null
  grep -q 'token: token-cluster-uat' "$K"
  grep -q 'token: token-cluster-prod' "$K"
  grep -q 'server: "https://cluster-prod.example:443"' "$K"
  grep -q 'certificate-authority-data: Q0EtREFUQQ==' "$K"
  grep -q '^current-context: "cluster-uat"$' "$K"
  [ "$(stat -c %a "$K")" = 600 ]
  grep -q "\[create\] \[token\] \[lea-$(lea_user_name)\] \[-n\] \[lea\] \[--duration=8h\]" "$FAKE_LOG"
}

@test "skips a context that refuses and keeps the others" {
  export FAKE_KUBE_CONTEXTS="cluster-uat cluster-prod"
  export FAKE_KUBE_FAIL_cluster_prod=1
  run lea_kube_refresh
  [ "$status" -eq 0 ]
  [[ "$output" == *"skipped context cluster-prod"* ]]
  run grep -q 'cluster-prod' "$K"
  [ "$status" -ne 0 ]
}

@test "fails clearly when no context issues a token" {
  export FAKE_KUBE_CONTEXTS="cluster-prod"
  export FAKE_KUBE_FAIL_cluster_prod=1
  run lea_kube_refresh
  [ "$status" -eq 1 ]
  [[ "$output" == *"k8s/lea-readonly.yaml.tmpl"* ]]
}

@test "manifest grants view and token minting for one developer only" {
  run sh "$LEA_ROOT/k8s/render.sh" alice User alice@example.com
  [ "$status" -eq 0 ]
  [[ "$output" == *"name: lea-alice"* ]]
  [[ "$output" == *"name: view"* ]]
  [[ "$output" == *'resourceNames: ["lea-alice"]'* ]]
  [[ "$output" == *"kind: User"*"name: alice@example.com"*"apiGroup: rbac.authorization.k8s.io"* ]]
  [[ "$output" != *"secrets"* && "$output" != *"pods/exec"* ]]
}

@test "manifest refuses a bad developer name or subject kind" {
  run sh "$LEA_ROOT/k8s/render.sh" "Alice!" User a
  [ "$status" -eq 2 ]
  run sh "$LEA_ROOT/k8s/render.sh" alice Robot a
  [ "$status" -eq 2 ]
  run sh "$LEA_ROOT/k8s/render.sh" alice ServiceAccount a
  [ "$status" -eq 2 ]
}

@test "manifest refuses an unsafe subject name or namespace" {
  run sh "$LEA_ROOT/k8s/render.sh" alice User 'a|b'
  [ "$status" -eq 2 ]
  run sh "$LEA_ROOT/k8s/render.sh" alice User 'a&b'
  [ "$status" -eq 2 ]
  run sh "$LEA_ROOT/k8s/render.sh" alice User 'a b'
  [ "$status" -eq 2 ]
  run sh "$LEA_ROOT/k8s/render.sh" alice User 'a
b'
  [ "$status" -eq 2 ]
  run sh "$LEA_ROOT/k8s/render.sh" alice ServiceAccount sa 'Bad_NS'
  [ "$status" -eq 2 ]
}

@test "manifest uses the configured namespace" {
  LEA_K8S_NAMESPACE=custom-ns run sh "$LEA_ROOT/k8s/render.sh" alice User alice@example.com
  [ "$status" -eq 0 ]
  [[ "$output" == *"namespace: custom-ns"* ]]
  [[ "$output" != *"namespace: lea"* ]]
}

@test "warns when a context uses a CA file or skips TLS verification" {
  export FAKE_KUBE_CONTEXTS="cluster-uat"
  export FAKE_KUBE_CA_PATH=/home/x/ca.crt
  run lea_kube_refresh
  [ "$status" -eq 0 ]
  [[ "$output" == *"context cluster-uat"*"certificate-authority"* ]]
}

@test "leaves no stale config.new and no temp dir with tokens" {
  export FAKE_KUBE_CONTEXTS="cluster-uat"
  mkdir -p "$(dirname "$K")"; echo stale >"$K.new"
  TMPDIR="$BATS_TEST_TMPDIR" lea_kube_refresh 2>/dev/null
  [ ! -e "$K.new" ]
  [ -z "$(ls -A "$BATS_TEST_TMPDIR" | grep '^tmp\.')" ]
}

@test "a symlinked kube state dir is refused, nothing written through it" {
  export FAKE_KUBE_CONTEXTS="cluster-uat"
  mkdir -p "$(lea_state_dir)" "$BATS_TEST_TMPDIR/out"
  ln -s "$BATS_TEST_TMPDIR/out" "$(lea_state_dir)/kube"
  run lea_kube_refresh
  [ "$status" -eq 1 ]
  [ -z "$(ls -A "$BATS_TEST_TMPDIR/out")" ]
}

@test "a symlinked kubeconfig is refused" {
  export FAKE_KUBE_CONTEXTS="cluster-uat"
  mkdir -p "$(dirname "$K")"; echo keep >"$BATS_TEST_TMPDIR/cfg"; ln -s "$BATS_TEST_TMPDIR/cfg" "$K"
  run lea_kube_refresh
  [ "$status" -eq 1 ]
  [ "$(cat "$BATS_TEST_TMPDIR/cfg")" = keep ]
}

@test "the refresh message says running sessions see the new token" {
  export FAKE_KUBE_CONTEXTS="cluster-uat"
  run lea_kube_refresh
  [[ "$output" == *"running lea sessions see it at once"* ]]
}

@test "context names are quoted in the kubeconfig" {
  export FAKE_KUBE_CONTEXTS="a:b#c"
  lea_kube_refresh 2>/dev/null
  grep -qF -- '- name: "a:b#c"' "$K"
  grep -qF -- '    cluster: "a:b#c"' "$K"
  grep -qF -- '    user: "lea-a:b#c"' "$K"
  grep -qF -- 'current-context: "a:b#c"' "$K"
}

@test "quotes and backslashes in a context name are escaped" {
  run lea__yaml_quote 'a"b\c'
  [ "$output" = '"a\"b\\c"' ]
}

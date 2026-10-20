load helpers
setup() { setup_fakes; }

@test "fake docker records arguments with spaces" {
  docker run "a b" c
  run calls
  [ "$output" = "docker [run] [a b] [c]" ]
}

@test "fake docker answers from env" {
  export FAKE_DOCKER_OUT_NETWORK_INSPECT='{"x":1}'
  export FAKE_DOCKER_RC_NETWORK_INSPECT=3
  run docker network inspect lea-internal
  [ "$status" -eq 3 ]
  [ "$output" = '{"x":1}' ]
}

@test "fake docker follows a status sequence" {
  export FAKE_DOCKER_SEQ_NETWORK_CREATE="0 1 0"
  docker network create a
  run docker network create b
  [ "$status" -eq 1 ]
  docker network create c
}

# The 7 pid-file tests compare pids with /proc: under lea's Podman a nested container binds lea's
# /proc (it cannot mount its own), so the suite shares lea's pid namespace to match it. Podman
# needs cgroups for that (lea's containers.conf disables them); Docker has no --cgroups flag.
@test "tests/run.sh runs the suite in the host pid namespace, with cgroups under Podman" {
  export FAKE_DOCKER_OUT___VERSION_="podman version 5.8.7"
  run sh "$LEA_ROOT/tests/run.sh"
  [ "$status" -eq 0 ]
  run grep '^docker \[run\]' "$FAKE_LOG"
  [[ "$output" == *"[--pid=host] [--cgroups=enabled]"* ]]
}

@test "tests/run.sh gives Docker the host pid namespace without --cgroups" {
  export FAKE_DOCKER_OUT___VERSION_="Docker version 27.3.1, build ce12230"
  run sh "$LEA_ROOT/tests/run.sh"
  [ "$status" -eq 0 ]
  run grep '^docker \[run\]' "$FAKE_LOG"
  [[ "$output" == *"[--pid=host]"* ]]
  [[ "$output" != *"--cgroups"* ]]
}

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

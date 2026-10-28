# lea functional checklist

Run on a workstation after `lea init` (the images under test are those recorded in the user config).
Each line: the command, run from `lea shell` unless noted, and the expected result.
Run it also on macOS (Docker Desktop, OrbStack) and Windows/WSL2 (Docker Desktop, projects directory
on the WSL filesystem) before relying on lea there.

1. `ssh -T git@$GITLAB_HOST` prints the GitLab welcome for your user.
2. `git -C <a repo> fetch` succeeds.
3. `glab api user | jq -r .username` prints your GitLab username.
4. `docker pull <a private image of $LEA_REGISTRY>` succeeds.
5. A project's test target that runs in a container (`make test` or similar) runs and passes.
6. In a project whose package manager fetches private git dependencies over SSH, run in a nested container: the install succeeds through the forwarded ssh-agent.
7. `docker compose up -d` in a multi-service project brings the services up; a request from one service to another by service name over plain HTTP succeeds.
8. In Claude Code: an HTTP MCP server reached through the proxy answers a simple query.
9. After `lea kube refresh` on the host, in an already running session: `kubectl get pods -n <ns>` works and `kubectl exec` is refused (`Forbidden`).
10. With a locksql console started by the printed command: the `locksql_status` MCP tool answers.
11. In Claude Code: `/plugin` lists the same plugins as on the host, and the output style is the same.
12. Against a local OTel collector (set `LEA_OTEL_UPSTREAM` to an address the proxy can reach): the backend shows `plugin_loaded` and `hook_registered` events whose resource carries `service.name=lea`.
13. Plant a hook (`echo '#!/bin/sh' > <repo>/.git/hooks/pre-push`) inside lea, close the last session: the launcher prints the WARNING with the path.
14. `docker inspect -f '{{.HostConfig.Memory}} {{.HostConfig.MemorySwap}} {{.HostConfig.PidsLimit}}' lea-<id>` on the host prints half of Docker's memory (twice: no swap) and 16384.
15. Have an agent run `docker compose up -d` and `podman pod create x && podman run -d --pod x <image> sleep 600`, then end it (`/exit`, or x in the deck): from `lea shell`, `podman ps -a --filter name=<its project>` and `podman pod ps` no longer list them.
16. Simulated reboot: `docker kill lea-<id>`, then `docker start lea-<id>`: the container becomes ready (`docker exec lea-<id> test -e /run/lea/ready`) and `docker exec -u $(id -u) lea-<id> podman ps` works. Then `lea`: a container left by an older lea is reported ("containers that no agent started").

# Static tmux

The deck needs tmux 3.2 or later. When the host has none, lea never installs a package and never
runs `sudo`; it can build its own instead:

    make tmux-build

This builds tmux 3.5a (libevent 2.1.12-stable, ncurses 6.5) in an `alpine:3.20` container from
upstream release tarballs, each checked against the SHA-256 pinned in `Containerfile`, links it
statically (with common terminfo entries compiled in) and writes `build/tmux` (git-ignored).

lea uses the host `tmux` when it is 3.2 or later, otherwise `build/tmux`. `lea doctor` says which
one is in use. To bump a version, change the `ARG`s in `Containerfile` and record the new hash
from the official release page.

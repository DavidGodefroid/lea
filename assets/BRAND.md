# lea brand

lea is part of the [Lock Family](https://github.com/DavidGodefroid/lockfamily), and wears its
identity: "a printed ticket that a human signs". The family's
[`docs/identity.md`](https://github.com/DavidGodefroid/lockfamily/blob/main/docs/identity.md) is
the source of truth for colours, type, shapes and logos; this file keeps only what is lea's own.

## Name

**lea**, always lowercase, also at the start of a sentence. Not "LEA", not "Lea".
The deck is "the deck". Agents are "agents", never "bots".

## Promise

> Many agents. None of your machine.

One-liner: *lea runs your AI agents side by side in containers (Claude Code by default, Codex or a
model of your own too), and you drive them all from one terminal deck.* In the Lock Family table:
"Runs many agents in containers, wired to the Locks."

Agent-neutral: name Claude Code as the default, never as the only agent lea runs.

## The website (leadeck.dev)

[`site/`](../site) follows the family's site kit
([`site-kit/KIT.md`](https://github.com/DavidGodefroid/lockfamily/blob/main/site-kit/KIT.md)):

- `site/assets/tokens.css` (the paper and carbon themes, `--ink`, `--sign`, `--stamp`…),
  `fonts/` (Zilla Slab, Source Serif 4, IBM Plex Mono, self-hosted woff2), `lock-site.css` and
  `copy.js` are lockfamily's files, byte for byte: change them in lockfamily, then copy them
  here and update the hashes in `tests/site/test_site.py`.
- `site/assets/logo.svg` is lockfamily's `assets/lea.svg` (light and dark inside the file).
- `site/assets/og.png` (1200x630) and `apple-touch-icon.png` (180x180) are rendered from the
  kit's `tools/og.html`, as KIT.md step 9 says.
- `tools/site/assemble.sh` builds what `.github/workflows/pages.yml` publishes: `site/`, plus the
  demo and `install.sh` from the repository root.
- `make test-site` runs the site's unit test and its end-to-end test (headless Chrome).

## Logo in the README and the deck

| File | Use |
|---|---|
| `logo.svg` | wordmark on dark backgrounds (README) |
| `logo-light.svg` | wordmark on light backgrounds (README) |
| `logo-mark.svg` | the deck's own mark: three agents (needs you, working, done) in a box |

## The deck's terminal palette

The deck itself, and the demo recorded from it, keep their own terminal palette,
[Catppuccin](https://catppuccin.com) Mocha (dark) and Latte (light): red needs you, peach works,
green is done, blue is the boundary. That palette is the deck's, not the website's.

## Demo

`demo.svg` is the animated demo (12 s loop): the README shows it, and the site frames it with an
ink border and a caption.

```sh
python3 tools/brand/demo.py > assets/demo.svg   # edit the story here
```

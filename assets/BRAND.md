# lea brand

## Name

**lea**, always lowercase, also at the start of a sentence. Not "LEA", not "Lea".
The deck is "the deck". Agents are "agents", never "bots".

## Promise

> Many Claude Code agents. None of your machine.

One-liner: *lea runs Claude Code agents side by side in an isolated container, and you drive them
all from one terminal deck.*

## Logo

| File | Use |
|---|---|
| `logo.svg` | wordmark on dark backgrounds |
| `logo-light.svg` | wordmark on light backgrounds |
| `logo-mark.svg`, `favicon.svg` | the mark alone: avatars, favicons, app icons |

The mark is the deck in a sealed box: three agents (needs you, working, done) inside a blue
boundary. Keep clear space of one dot diameter around it; do not recolour the dots, they are the
deck's states.

## Colours

The deck's palette, [Catppuccin](https://catppuccin.com) Mocha (dark) and Latte (light).

| Role | Mocha | Latte |
|---|---|---|
| background | `#11111b` crust, `#1e1e2e` base | `#eff1f5` base |
| text | `#cdd6f4` | `#4c4f69` |
| accent, the boundary | `#89b4fa` blue | `#1e66f5` |
| needs you, blocked | `#f38ba8` red | `#d20f39` |
| working | `#fab387` peach | `#fe640b` |
| done, allowed | `#a6e3a1` green | `#40a02b` |

## Type

Inter for text, JetBrains Mono for commands and the deck. Both free (OFL).

## Demo

`demo.svg` is the animated hero (12 s loop, plays on GitHub and the site); `demo.gif` and
`social-card.png` (1200×630, Open Graph) are rendered from it for places that do not play SVG.

```sh
python3 tools/brand/demo.py > assets/demo.svg   # edit the story here
python3 tools/brand/render.py                   # demo.gif + social-card.png (Chrome + Pillow)
```

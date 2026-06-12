# AgentBox demo

A ~60-second, fully real (no mocks) terminal demo of **enforced network
egress** — the v0.4.0 headline. It's the fastest way to show a visitor that
AgentBox *contains* an agent instead of just advising it.

## The script

[`egress-demo.sh`](egress-demo.sh) is self-contained and recordable. It needs
`docker` running and `agentbox` on your `PATH` (or `AGENTBOX=/path/to/agentbox`);
it builds the stub runtime image automatically, runs in a throwaway temp dir,
and cleans up after itself.

### Storyboard (what each beat shows)

| Beat | Command | Point |
|------|---------|-------|
| 1 | `cat agentbox.yaml` | Policy allows exactly **one** domain (`github.com`). |
| 2 | `agentbox exec 'git ls-remote …github.com…'` | ✓ Allowed domain is reachable (exit 0). |
| 3 | `agentbox exec 'getent hosts gitlab.com'` | DNS is **dead** — no resolution side channel (exit 2). |
| 4 | `agentbox exec 'git ls-remote …gitlab.com…'` | ✗ Connection **fails closed** at the proxy (403, exit 128). |
| 5 | `agentbox session show latest \| grep …` | Every attempt is in the **audit trail**. |

The closing line ties it back: *allowlist your model API, deny the rest.*

### Try it (no recording)

```bash
agentbox --version >/dev/null   # ensure it's installed and on PATH
demo/egress-demo.sh
```

Faster, pause-free dry run while iterating:

```bash
TYPE=0 HOLD=0 AGENTBOX="$PWD/bin/agentbox" demo/egress-demo.sh
```

## Record it

Install the tools (macOS shown; all are also on Linux):

```bash
brew install asciinema agg     # asciinema records; agg turns .cast into .gif
```

Record into a `.cast` file (the `-c` runs the demo as the recorded command):

```bash
asciinema rec egress.cast \
  --idle-time-limit 1.5 \
  --cols 92 --rows 28 \
  --overwrite \
  -c demo/egress-demo.sh
```

- `--idle-time-limit 1.5` trims the real proxy-setup pauses so playback stays
  tight even though each sandbox spins up a real container + network.
- `92×28` fits GitHub's README width without wrapping the command lines.
- Use a clean terminal theme (dark, ligature-free) for a crisp result.

## Turn it into something you can embed

**GIF (simplest for a README):**

```bash
agg --theme monokai --font-size 22 egress.cast demo/egress.gif
```

**SVG (sharper, smaller, selectable text):**

```bash
npx svg-term-cli --in egress.cast --out demo/egress.svg --window --width 92 --height 28
```

**Hosted player (best UX — real copy-pasteable text, no huge binary in the repo):**

```bash
asciinema upload egress.cast      # prints an asciinema.org URL
```

## Embed in the top of the main README

GIF:

```markdown
![AgentBox enforces network egress](demo/egress.gif)
```

SVG:

```markdown
<p align="center"><img src="demo/egress.svg" alt="AgentBox enforces network egress" width="800"></p>
```

asciinema badge (click-to-play, keeps the repo lean):

```markdown
[![asciicast](https://asciinema.org/a/<ID>.svg)](https://asciinema.org/a/<ID>)
```

Put it right under the tagline, above **## Add AgentBox to your agent harness** —
the demo *is* the pitch.

## Tips

- Keep the GIF under ~2–3 MB (drop `--font-size`, or prefer SVG/asciinema) so it
  loads fast on the repo page.
- If a run looks slow, it's the real per-run network + proxy setup — that's the
  point; `--idle-time-limit` hides the wait without faking the result.
- Re-run is idempotent: the temp workspace and all `agentbox-net-*/ext-*`
  networks are torn down on exit.

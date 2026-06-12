# AgentBox demos

Two ~60-second, fully real (no mocks) terminal demos. Both *contain* an agent
instead of just advising it; both build the stub runtime image automatically,
run in a throwaway temp dir, and clean up after themselves. Each needs `docker`
running and `agentbox` on your `PATH` (or `AGENTBOX=/path/to/agentbox`).

| Demo | Script | GIF | Shows |
|------|--------|-----|-------|
| **Blocked exfiltration** (hero) | [`exfil-demo.sh`](exfil-demo.sh) | [`exfil.gif`](exfil.gif) | An agent holds a live API key but can't leak it — attacker host refused, audit record redacts the key. |
| **Enforced egress** | [`egress-demo.sh`](egress-demo.sh) | _record from [`egress.cast`](egress.cast)_ | Allowlist one domain; the sandbox reaches it and nothing else; DNS dead; fail closed; audited. |

## Blocked exfiltration — `exfil-demo.sh`

The hero story: AgentBox injects a (fake) API key into the sandbox so the agent
can do real work against ONE allowed API, then proves the key has nowhere to go.

| Beat | Command | Point |
|------|---------|-------|
| 1 | `cat agentbox.yaml` | One allowed domain + an injected credential. |
| 2 | `agentbox exec '… $AGENTBOX_FAKE_API_KEY …'` | The key is really inside the sandbox (value never printed). |
| 3 | `agentbox exec 'git ls-remote …github.com…'` | ✓ The one allowed API stays reachable. |
| 4 | `agentbox exec 'git ls-remote …evil.example.com…'` | ✗ Exfil host **refused at the proxy** (403, fail closed). |
| 5 | `agentbox session show latest \| grep 'BLOCKED egress'` | The attempt is in the **audit trail**. |
| 6 | dump the key to output → `grep REDACTED …/logs.txt` | The saved record **redacts** the key; the raw value never hits disk. |

The fake key value never appears on screen or in the recording — verify with
`grep -c 'sk-DEMO-fake-key-not-real' demo/exfil.cast` → `0`.

## Enforced egress — `egress-demo.sh`

[`egress-demo.sh`](egress-demo.sh) is self-contained and recordable.

### Storyboard (what each beat shows)

| Beat | Command | Point |
|------|---------|-------|
| 1 | `cat agentbox.yaml` | Policy allows exactly **one** domain (`github.com`). |
| 2 | `agentbox exec 'git ls-remote …github.com…'` | ✓ Allowed domain is reachable (exit 0). |
| 3 | `agentbox exec 'getent hosts gitlab.com'` | DNS is **dead** — no resolution side channel (exit 2). |
| 4 | `agentbox exec 'git ls-remote …gitlab.com…'` | ✗ Connection **fails closed** at the proxy (403, exit 128). |
| 5 | `agentbox session show latest \| grep …` | Every attempt is in the **audit trail**. |

The closing line ties it back: *allowlist your model API, deny the rest.*

### Try them (no recording)

```bash
agentbox --version >/dev/null   # ensure it's installed and on PATH
demo/exfil-demo.sh              # the hero demo
demo/egress-demo.sh            # the egress-mechanics demo
```

Faster, pause-free dry run while iterating (either script):

```bash
TYPE=0 HOLD=0 AGENTBOX="$PWD/bin/agentbox" demo/exfil-demo.sh
```

## Record it

Install the tools (macOS shown; all are also on Linux):

```bash
brew install asciinema agg     # asciinema records; agg turns .cast into .gif
```

Record into a `.cast` file with **asciinema 3.x** (the `-c` runs the demo as the
recorded command; `--headless` records without taking over your terminal):

```bash
export AGENTBOX="$PWD/bin/agentbox"   # so the recorded run finds your build
asciinema rec demo/exfil.cast \
  --headless --window-size 92x30 --idle-time-limit 1.5 --overwrite \
  -c demo/exfil-demo.sh
```

- `--idle-time-limit 1.5` trims the real proxy-setup pauses so playback stays
  tight even though each sandbox spins up a real container + network.
- `92×30` fits GitHub's README width without wrapping the command lines (the
  egress demo uses `92x28`).
- Use a clean terminal theme (dark, ligature-free) for a crisp result.

## Turn it into something you can embed

**GIF (simplest for a README):**

```bash
agg --theme monokai --font-size 22 demo/exfil.cast demo/exfil.gif
```

**SVG (sharper, smaller, selectable text):**

```bash
npx svg-term-cli --in demo/exfil.cast --out demo/exfil.svg --window --width 92 --height 30
```

**Hosted player (best UX — real copy-pasteable text, no huge binary in the repo):**

```bash
asciinema upload egress.cast      # prints an asciinema.org URL
```

## Embed in the top of the main README

GIF:

```markdown
![AgentBox blocks exfiltration](demo/exfil.gif)
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

# M6 — real-program verification runbook

M6 is the one milestone that cannot run in a nested sandbox: it needs a host
where bwrap can create user + mount namespaces (`box --doctor` ends with
`ok: box can run sandboxes here`). Run it on a normal WSL2/Ubuntu or native
Linux machine.

Nothing below is guesswork about box's behaviour — the sandbox recipe, safety
rules and exit codes are already proven by the unit + e2e suites. M6 confirms
box's **presets** are right for real programs and records what each program
actually needs.

## 0. Prerequisites

```sh
make install
box --doctor          # must end with: ok: box can run sandboxes here
```

If it fails, follow the printed fix (on native Ubuntu 24.04+:
`sudo sysctl -w kernel.apparmor_restrict_unprivileged_userns=0`; nothing is
needed on WSL).

Then run the automated checks on this host:

```sh
./scripts/m6-check.sh                          # 10 quick boundary checks
BOX_E2E_REQUIRED=1 go test ./test/e2e -count=1 -v
```

Everything must pass. `BOX_E2E_REQUIRED` turns skips into failures, so a
broken setup can't pass silently; only Landlock (needs Linux 6.12+) and the
WSL-only tests may still skip. The `TestCtrlC/plain` log line records D2.

## 1. Kiro CLI

```sh
cd ~/some-project
box kiro-cli                      # first run opens the TUI; accept the preset
# inside the box:
kiro-cli login --use-device-flow  # browser can't open in the box; use the code
kiro-cli chat                     # run a task
```

Check, from inside the box (`box bash` in the same project, or a shell escape).
`box --dry-run kiro-cli` shows exactly what is mounted:
- [ ] `~/.ssh` is invisible: `ls ~/.ssh` → No such file or directory
- [ ] a sibling project is invisible
- [ ] `/mnt/c` is invisible (WSL)
- [ ] `~/.kiro` is **read-only** (D3): `touch ~/.kiro/x` fails
- [ ] `~/.local/share/kiro-cli` is writable (login persists across runs)
- [ ] Ctrl+C while a reply streams — does it kill the box? **Records D2**: if the
      box dies before Kiro can clean up, add `init = true` under `[sandbox]` in
      the kiro-cli preset (the B3 init already exists) and try again.
- [ ] terminal resize reaches Kiro (redraw follows the new size)
- [ ] an MCP server via `npx`/`uvx` starts (tick its runtime folder if under `~`)
- [ ] note everything Kiro writes under `~/.kiro` — feeds the D3 decision.

## 2. Kiro Crew (Python venv)

```sh
box kirocrew doctor               # venv resolved via shebang handling (S4)
box kirocrew gateway              # then, from the HOST:
curl -s http://127.0.0.1:5476/... # gateway reachable at 127.0.0.1:5476
```

- [ ] `kirocrew doctor` passes inside the box
- [ ] the gateway is reachable from the host at `127.0.0.1:5476`
- [ ] its nested agent sandbox works (validates S10 — nested userns allowed)
- [ ] `kiro-cli` is found inside (the preset lists it under `tools`)
- [ ] `~/.kiro` is writable here (kirocrew preset needs it; accepted risk)

## 3. A Node program under ~/.nvm or ~/.local

```sh
box <node-tool>
```
- [ ] resolves and runs: `#!/usr/bin/env node` scripts get node's install
      folder mounted automatically (S4); list extra commands under `tools`.

## 4. A Burrito app (self-extracting)

```sh
box <burrito-app>   # first run
box <burrito-app>   # second run
```
- [ ] first run unpacks the payload into the private home
- [ ] second run reuses it (no re-unpack), confirming the private home persists.

## 5. A static binary and a shell

```sh
box bash            # network off by default
box <static-bin>
```
- [ ] both run; `bash` has job control (`sleep 100 &; jobs; fg`).
- [ ] on WSL: `box cmd.exe` is refused (exit 126), and `/mnt/c` is invisible.

## Recording results

For each program, note what failed and whether a **generic rule** or only a
**preset tweak** fixes it — prefer generic rules. Update the preset TOML under
`internal/profile/presets/` and re-run the e2e suite. Record the D2 (Ctrl+C) and
D3 (`~/.kiro` writes) outcomes in `docs/decisions.md`.

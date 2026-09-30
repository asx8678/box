# box — Architecture & Implementation Plan

Sep 30, 2026

> Some facts and design choices below are corrected in [implementation-plan.md](implementation-plan.md), which is the plan to build from.

## Overview

`box` is a single Go binary that starts any installed program inside a bubblewrap sandbox, limited to the folder you run it in. You type `box kiro-cli` in a project folder; the first time, a mouse-clickable TUI asks what to allow, saves it as a profile, and then the program starts sandboxed.

**Goals**

- One command to run Kiro CLI, Kiro Crew, a Burrito app or a shell sandboxed: `box <program> [args]`.
- Uses the programs already installed on the system; no images, no daemon.
- Per-program profiles, created and edited in a TUI with mouse support, stored as readable TOML.
- Safe by default: only the current folder is writable, the real home is hidden, the environment is wiped, it fails closed.
- Small: about 1,500 lines of Go including the TUI.

**Non-goals for v1**

- macOS and Windows (bubblewrap is Linux-only).
- Per-domain network rules (needs an egress proxy; planned as v2).
- Hiding individual commands inside `/usr` (all installed commands stay visible read-only).
- Resource limits (CPU, memory).

## Collected facts

Everything the design relies on, with where it comes from. "Tested" means checked in this session with bubblewrap on Linux.

| Area | Fact | Source |
| --- | --- | --- |
| Go | Go 1.27 is released; current patch is go1.27.1. New in 1.27: generic methods, `encoding/json/v2`, `strings.CutLast`. None are needed by box, but the module targets `go 1.27`. | [go.dev/dl](https://go.dev/dl/), [Go 1.27 notes](https://go.dev/doc/go1.27) |
| Go | `syscall.Exec` replaces the Go process with `bwrap`, so box leaves nothing running and the program gets the terminal directly. | Go standard library |
| TUI | Bubble Tea v2 is stable (latest v2.0.9), imported as `charm.land/bubbletea/v2`. Mouse clicks arrive as `MouseClickMsg`; mouse mode is set in `View()` via `MouseModeCellMotion`. Companion libraries: Bubbles v2, Lip Gloss v2. | [Bubble Tea releases](https://github.com/charmbracelet/bubbletea/releases) |
| bubblewrap | Shows a program only the paths you mount: `--ro-bind` (read-only), `--bind` (read-write), `--ro-bind-try` (skip if missing). Everything else does not exist inside. | Tested |
| bubblewrap | `--unshare-all` isolates network, PIDs, IPC, UTS; `--share-net` gives the network back. Network is all-or-nothing. | Tested |
| bubblewrap | `--clearenv` + `--setenv` gives a clean environment; a variable set outside did not leak in. | Tested |
| bubblewrap | `--die-with-parent` kills the sandbox when box's terminal dies; `--new-session` blocks terminal-injection tricks. | Tested |
| bubblewrap | Ubuntu 23.10+ restricts unprivileged user namespaces through AppArmor; the distro `bubblewrap` package ships the profile that allows it. | Earlier research |
| Kiro CLI | Binary `kiro-cli`, installed by `install.sh` into a folder on PATH. | [Arm install guide](https://learn.arm.com/install-guides/kiro-cli/) |
| Kiro CLI | Global config in `~/.kiro/` (`settings/cli.json`, `settings/mcp.json`, `agents/`, `steering/`, `skills/`, `hooks/`, `powers/`, `settings/permissions.yaml`); override with `KIRO_HOME`. Per-project config in `<project>/.kiro/`. | [Kiro configuration](https://kiro.dev/docs/configuration/) |
| Kiro CLI | Login uses device flow (URL + code in a browser); the login state lives in `~/.local/share/kiro-cli/data.sqlite3`. | [Docker Kiro sandbox docs](https://docs.docker.com/ai/sandboxes/agents/kiro/) |
| Kiro Crew | Command `kirocrew` (`setup`, `doctor`, `gateway`). Needs Python 3.12+. All data under `~/.kiro/crew/` (config, `.env` credentials, memory, logs), override with `KIROCREW_HOME`. Gateway listens on port 5476 (`KIROCREW_PORT`). | [Crew configuration](https://kiro.dev/docs/crew/configuration/), [Crew installation](https://kiro.dev/docs/crew/installation/) |
| Burrito | On Linux the payload unpacks to `$XDG_DATA_HOME/.burrito` or `~/.local/share/.burrito`; `<APP>_INSTALL_DIR` overrides it. Inside box it lands in the profile's private home. | [Burrito wrapper.zig](https://github.com/burrito-elixir/burrito/blob/main/src/wrapper.zig) |
| Burrito | Old versions are deleted when a new one runs, so persistent app data must live outside the payload folder. | [Burrito README](https://github.com/burrito-elixir/burrito) |

Open facts: which network domains Kiro CLI and Kiro Crew need (only matters for the v2 allowlist), and whether Kiro Crew's gateway must be reachable from outside the sandbox.

## Architecture

```
box sets up the sandbox, then hands over to bwrap

┌─ box process (outside the sandbox) ────────────────────────────────────────┐
│                                                                            │
│  1 cli ───────▶ 2 profile ──────▶ 3 tui ─────────▶ 4 sandbox               │
│  parses flags   picks profile:    only if none,    checks safety rules,    │
│                 -p, folder        or -n / -e / -r; builds bwrap args,      │
│                 memory            saves the TOML   then syscall.Exec       │
│                      ▲                 │                │                  │
└──────────────────────┼─────────────────┼────────────────┼──────────────────┘
                       │                 ▼                │
                ~/.config/box/: profiles + folder         │ exec: box is
                memory (never mounted)                    │ replaced by bwrap
                                                          ▼
┌─ bwrap sandbox (the program's whole world) ────────────────────────────────┐
│  Program:      kiro-cli, kirocrew, Burrito app or bash; network on/off     │
│  Read-write:   project folder, private home, program folders (~/.kiro)     │
│  Read-only:    /usr, /bin, /lib, DNS and TLS files, extra folders          │
│  Not visible:  real home, ~/.ssh, other projects, ~/.config/box            │
└────────────────────────────────────────────────────────────────────────────┘
```

box is a short-lived launcher: it resolves a profile (opening the TUI only when needed), turns it into bwrap arguments, and then `syscall.Exec` replaces box with `bwrap`. Nothing of box keeps running, so the program owns the terminal (mouse, colours, Ctrl+C) exactly as without box. Profiles stay on the host in `~/.config/box/`, out of the program's reach.

## CLI design

box's own flags go **before** the program name; everything after the program name is passed to the program untouched. So `box -r kiro-cli` resets, while `box kiro-cli -r` passes `-r` to Kiro.

```
box [flags] <program> [program args...]
box -l | --list
box -h | --help | --version
```

| Command | What it does |
| --- | --- |
| `box kiro-cli` | Run with the profile remembered for this folder, else the program's `default` profile. No profile yet → TUI opens first. |
| `box -p online kiro-cli` | Run with the named profile `online`. |
| `box -n online kiro-cli` | Create a new profile `online` in the TUI (pre-filled from `default`), save it, run. |
| `box -e kiro-cli` | Edit the profile that would be used, then run. |
| `box -r kiro-cli` | Reset: delete the program's profiles and the folder memory, open the TUI fresh. Asks for confirmation. |
| `box -l` | List programs and their profiles, with network and extra folders summarised. |
| `box --net kiro-cli` / `--no-net` | One-off network override for this run only; the profile is not changed. |
| `box --dry-run kiro-cli` | Print the full `bwrap` command instead of running it. |
| `box --no-tui kiro-cli` | Never open the TUI; fail if no profile exists (for scripts). |

If a program has several profiles and nothing is remembered for this folder, box shows a one-screen picker before running. Exit code is the program's own; box's own errors exit with 125, like `docker run`.

## Profiles

A profile is one TOML file per program and name. Everything lives under `~/.config/box/` (or `$XDG_CONFIG_HOME/box/`), which is **never** mounted into a sandbox, so a program cannot change its own permissions.

```
~/.config/box/
  profiles/
    kiro-cli/default.toml
    kiro-cli/online.toml
    kirocrew/default.toml
    my_agent/default.toml
  presets/              # built-in starting points, written on first run
    kiro-cli.toml  kirocrew.toml  bash.toml
  folders.toml          # which profile each folder used last
~/.local/share/box/homes/<program>-<profile>/   # private HOME per profile
```

Example `profiles/kiro-cli/default.toml`:

```toml
version = 1
program = "kiro-cli"          # looked up on PATH at run time
network = true

[workdir]
mode = "rw"                   # rw | ro

[home]
# host folders mapped into the sandbox at the same path
rw = ["~/.kiro", "~/.local/share/kiro-cli"]
ro = []

[extra]
rw = []
ro = ["~/code/shared-lib"]

[env]
pass = ["TERM", "COLORTERM", "LANG", "AWS_PROFILE"]
set = { EDITOR = "vi" }

[system]
ro = ["/usr", "/bin", "/sbin", "/lib", "/lib64", "/etc/resolv.conf", "/etc/hosts", "/etc/ssl", "/etc/ca-certificates", "/etc/passwd", "/etc/group", "/etc/localtime", "/etc/alternatives"]
```

**Resolution order** for which profile runs: `-p NAME` → the entry for this exact folder in `folders.toml` → the only profile of that program → picker if several → TUI if none. After a successful start, box records the folder → profile pair.

**Presets** give new profiles sensible defaults:

| Program | Home folders mounted | Network |
| --- | --- | --- |
| `kiro-cli` | rw `~/.kiro`, rw `~/.local/share/kiro-cli` (login state) | on |
| `kirocrew` | rw `~/.kiro/crew` | on |
| Burrito app | none (payload unpacks into the private home) | on |
| `bash` | none | off |
| anything else | none | off |

Mounting `~/.kiro` read-write lets Kiro edit its own global agents and settings; choose `ro` there in the TUI if that should not happen (login still works through the separate data folder).

## Sandbox

The profile becomes one `bwrap` argument list, built in a fixed order because later mounts sit on top of earlier ones. Paths keep their real names inside, so `~/.kiro` and the project path look the same to the program as outside.

| Order | Profile part | bwrap arguments |
| --- | --- | --- |
| 1 | always | `--unshare-all --die-with-parent --new-session` |
| 2 | `network = true` | `--share-net` |
| 3 | `[system] ro` | `--ro-bind-try P P` for each path |
| 4 | always | `--proc /proc --dev /dev --tmpfs /tmp --tmpfs /run` |
| 5 | always | `--dir /run/box` + `--ro-bind <info file> /run/box/profile` (lets a program see it is boxed) |
| 6 | private home | `--bind ~/.local/share/box/homes/<prog>-<profile> $HOME` |
| 7 | `[home] rw` / `ro` | `--bind` / `--ro-bind` each path, same path inside (on top of the private home) |
| 8 | program binary | `--ro-bind <resolved path> <same path>` when it is outside `/usr` |
| 9 | `[extra] rw` / `ro` | `--bind` / `--ro-bind` each path, same path inside |
| 10 | `[workdir]` | `--bind` (or `--ro-bind`) `$PWD $PWD --chdir $PWD` |
| 11 | `[env]` | `--clearenv`, then `--setenv` for HOME, PATH, PWD, TMPDIR, each `pass` variable that is set, and each `set` pair |
| 12 | program | the resolved program path, then the user's arguments |

**Program lookup.** box resolves the program on the host PATH and follows symlinks (`filepath.EvalSymlinks`). If the real file lives outside `/usr` (for example `~/.local/bin/kiro-cli` pointing into `~/.local/share/...`), box mounts both the link and the target folder read-only. Kiro Crew is a Python app, so its preset also mounts its installation folder read-only; box finds it from the shebang line of the `kirocrew` script.

**Safety rules** (checked before anything runs; any failure → exit 125, nothing runs):

- Refuse when the working folder is `/`, `$HOME`, or a parent of `$HOME`.
- Refuse any rw path that is `$HOME` itself, `~/.config/box`, `~/.ssh`, `~/.gnupg`, or a parent of them.
- Refuse when a profile path does not exist (except `--ro-bind-try` system paths).
- Fail closed: if `bwrap` is missing or `syscall.Exec` fails, print the reason and never fall back to running unsandboxed.
- Profile files must be owned by the user and not group/world-writable; otherwise refuse to use them.
- `--dry-run` prints the exact argument list, so every rule can be inspected.

## TUI

One editor screen, plus two small helpers (profile picker, reset confirmation). Everything works with both mouse and keyboard: click a checkbox or button, or use Tab / arrows / Space / Enter.

```
 box · kiro-cli · profile "default"                      ~/code/my-project
┌ Project folder ─────────────────┐ ┌ Network ───────────────────────┐
│ (•) read-write   ( ) read-only  │ │ ( ) off   (•) on               │
└─────────────────────────────────┘ └────────────────────────────────┘
┌ Program's own folders ───────────────────────────────────────────────┐
│ [x] ~/.kiro                      rw ▾                                 │
│ [x] ~/.local/share/kiro-cli      rw ▾                                 │
├ Extra folders ───────────────────────────────────────────────────────┤
│ [x] ~/code/shared-lib            ro ▾                        [remove] │
│ [+ add folder]                                                        │
├ Environment variables passed in ─────────────────────────────────────┤
│ [x] TERM  [x] LANG  [x] AWS_PROFILE  [ ] ANTHROPIC_API_KEY  [+ add]   │
└───────────────────────────────────────────────────────────────────────┘
  Profile name: [default      ]   [ Preview command ]  [ Cancel ]  [ ▶ Save & run ]
```

- **Add folder** opens a path input with Tab completion (Bubbles `textinput` with a directory suggester); the safety rules are checked as you type and shown in red.
- **rw ▾ / ro ▾** toggles on click.
- **Environment variables** lists the preset's suggestions plus any variable you add by name; values are read at run time, never stored in the profile.
- **Preview command** shows the `--dry-run` output in a scrollable pane.
- **Save & run** validates, writes the TOML atomically (temp file + rename, mode 0600), then runs.

Implementation: Bubble Tea v2 with the mouse enabled in `View()` (`MouseModeCellMotion`), Bubbles v2 for inputs and viewport, Lip Gloss v2 for boxes. Click targets are tracked as rectangles recorded while rendering, so a `MouseClickMsg` maps back to the widget under the cursor.

## Project layout

One module, `go 1.27`, four internal packages. Only the `tui` package imports Charm libraries, so the core (config → bwrap args) stays small and fully unit-testable.

```
box/
  go.mod                     # module github.com/<you>/box, go 1.27
  main.go                    # calls cli.Run(os.Args), exits with its code
  internal/
    cli/
      run.go                 # flag parsing, command dispatch, profile resolution
      list.go                # box -l
    profile/
      profile.go             # Profile struct, Load/Save (atomic, 0600), Validate
      presets.go             # built-in presets (embedded TOML via go:embed)
      folders.go             # folders.toml: folder → last profile
      paths.go               # ~ expansion, XDG dirs, safety-rule checks
    sandbox/
      resolve.go             # find program on PATH, follow symlinks, shebang lookup
      args.go                # Profile + workdir + overrides → []string for bwrap
      exec.go                # locate bwrap, syscall.Exec, fail closed
    tui/
      editor.go              # main editor screen (Bubble Tea model)
      picker.go              # profile picker
      confirm.go             # reset confirmation
      widgets.go             # checkbox, radio, toggle, click-target registry
  presets/                   # kiro-cli.toml, kirocrew.toml, bash.toml, burrito.toml
  test/
    e2e_test.go              # runs real bwrap: can/can't read, write, reach network
  Makefile                   # build, test, install (CGO_ENABLED=0, -trimpath)
```

**Dependencies:** `charm.land/bubbletea/v2`, `charm.land/bubbles/v2`, `charm.land/lipgloss/v2`, `github.com/BurntSushi/toml`. Flags use the standard `flag` package (box has few flags, and stopping at the first non-flag argument is its default behaviour). Build: `CGO_ENABLED=0 go build -trimpath -ldflags='-s -w'` → one static binary of roughly 8 MB.

## Implementation plan

Six milestones, each ending in something usable. The sandbox works from the command line after milestone 2; the TUI is layered on top. Rough effort for one developer: 4–6 working days.

**1. Core: profiles and arguments** (no running yet)

- [ ] `go mod init`, `go 1.27`, Makefile, `main.go`
- [ ] `profile`: struct, TOML load/save, `~` expansion, XDG paths, `Validate` with the safety rules
- [ ] `sandbox/args.go`: profile → bwrap argument list in the fixed order
- [ ] Unit tests: golden argument lists for each preset; every safety rule has a refusing test

**2. Run from the command line**

- [ ] `sandbox/resolve.go`: PATH lookup, symlink following, shebang interpreter lookup
- [ ] `sandbox/exec.go`: find `bwrap`, `syscall.Exec`, exit 125 on any failure
- [ ] `cli`: `box <program>`, `-p`, `--dry-run`, `--net` / `--no-net`, `--no-tui`
- [ ] End-to-end tests with real bwrap (see Testing)
- [ ] Done when `box --no-tui -p default bash` gives a shell that sees only the project

**3. Presets and folder memory**

- [ ] Embedded presets: `kiro-cli`, `kirocrew`, `bash`, generic Burrito app, fallback
- [ ] `folders.toml` read/write; resolution order from the Profiles section
- [ ] `box -l`, `box -r` (with a plain y/N prompt for now)

**4. TUI editor**

- [ ] Widgets: checkbox, radio, rw/ro toggle, button, click-target registry
- [ ] Editor screen: all sections, keyboard focus order, mouse clicks
- [ ] Add-folder input with completion and live safety-rule errors
- [ ] Preview pane showing the dry-run command
- [ ] Wire `-n`, `-e`, first-run flow; atomic save

**5. Picker and polish**

- [ ] Profile picker when several profiles exist; reset confirmation screen
- [ ] Clear error messages (missing bwrap, AppArmor hint on Ubuntu, missing path)
- [ ] `--help`, `--version`, README

**6. Try it on real programs**

- [ ] Kiro CLI: login inside the box, run a task, confirm `~/.ssh` and other projects are invisible
- [ ] Kiro Crew: `kirocrew doctor` inside the box; check whether the gateway on port 5476 must be reachable from outside
- [ ] Burrito agent: first-run unpack into the private home, second run reuses it
- [ ] Adjust presets with whatever those runs show

**Later (v2):** per-domain network allowlist through a small egress proxy started by box, with the sandbox's only way out being that proxy.

## Testing, risks and open questions

**Testing.** Unit tests cover everything up to the argument list, which is where security lives. End-to-end tests run real `bwrap` (skipped when it is unavailable) with a small probe script inside the box, and assert:

- can write the project folder and the private home;
- cannot see the real home, `~/.ssh`, a sibling project, or `~/.config/box`;
- cannot write `/usr`;
- only whitelisted environment variables exist;
- network works with `--net` and fails with `--no-net`;
- arguments with spaces and quotes arrive intact;
- refuses to start in `/` and `$HOME`, and exits 125 when `bwrap` is missing.

The TUI gets model-level tests (send key and mouse messages, check the resulting profile) rather than screen snapshots.

| Risk | Mitigation |
| --- | --- |
| A program needs a host path nobody thought of, and fails oddly | Preset tuning in milestone 6; `--dry-run`; error hint suggests adding the folder in `box -e` |
| Ubuntu AppArmor blocks user namespaces | Detect the error text and print the fix (install the distro `bubblewrap` package) |
| Project folder is fully writable, including `.git` | Documented; optional `ro` workdir mode; suggest a git worktree |
| API keys passed in are readable by everything in the box | Only variables ticked in the profile are passed; values are never stored in profiles |
| Network "on" means everywhere, including the LAN | v2 allowlist proxy; until then, choose "off" where possible |
| Kiro Crew's gateway may need inbound access | Shared network namespace already allows local listening; confirm in milestone 6 |

**Open questions**

- Which domains do Kiro CLI and Kiro Crew call? Needed only for the v2 allowlist.
- Should `~/.kiro` default to read-write or read-only? It decides whether Kiro may change its own global agents and settings from inside the box.
- Should box also offer a Docker/Podman backend later for macOS, or stay Linux-only?

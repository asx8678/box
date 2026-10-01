# box

`box` runs any installed Linux program inside a [bubblewrap](https://github.com/containers/bubblewrap) sandbox, limited to the folder you run it in. It's built for Ubuntu on WSL2, and works on other Linux distributions too.

```
cd ~/code/my-project
box kiro-cli
```

The first time, a one-screen editor (mouse or keyboard) asks what the program may use and saves the answers as a profile. After that, `box kiro-cli` starts straight away. box then replaces itself with `bwrap`, so nothing of box keeps running and the program gets the terminal directly.

On a terminal, box first prints one line saying what it starts and how, which stays in the scrollback:

```
 ✻ box  kiro-cli · profile default · network on · project read-write
```

After the editor or the picker it holds that line for about two seconds under "launching kiro-cli in the sandbox", so the hand-over is seen; Ctrl+C during the hold stops box before anything runs. `--no-tui`, or a redirected stderr, keeps box quiet.

> **Status:** milestones 1–6 of [the implementation plan](docs/implementation-plan.md) are written, and the unit tests pass. The end-to-end tests with real bubblewrap (`make test-linux`, also run by CI) haven't run yet; open choices are in [docs/decisions.md](docs/decisions.md).

## Install

```
sudo apt install bubblewrap
make install                             # builds bin/box and installs it to ~/.local/bin/box
box --doctor                             # checks bubblewrap and this machine
```

`make install PREFIX=/usr/local` installs elsewhere; `make build-linux` cross-compiles amd64 and arm64 binaries from any machine. On WSL no other setup is needed. On native Ubuntu 24.04 or later, `box --doctor` explains the AppArmor step if user namespaces are blocked.

## What the program can see

| | |
| --- | --- |
| Read-write | the project folder, the profile's private home, the folders you tick as read-write |
| Read-only | `/usr` and the system files programs need (certificates, DNS, time zone), the program's own install folder, the folders you tick as read-only |
| Hidden | your real home, `~/.ssh`, other projects, box's own settings, `/run/user`, Windows drives and Windows interop |
| Environment | rebuilt from scratch: `HOME`, `PATH`, the terminal and locale, and only the variables you tick |
| Network | off, on, or restricted to named hosts, per profile; `--net` / `--no-net` for one run |

Also:
- `.git/config` and `.git/hooks` stay read-only inside a read-write project, so a boxed program can't plant a command that runs the next time you use git.
- box refuses read-write access to folders on your `PATH`, shell startup files, `~/.ssh` and box's own settings, and it never mounts Docker or other daemon sockets.
- box refuses to run a Windows program (`.exe` or anything under `/mnt/c`), because it would run outside Linux.

## Usage

```
box [flags] <program> [program args...]
```

box's flags go **before** the program name; everything after it goes to the program untouched.

| Command | What it does |
| --- | --- |
| `box kiro-cli` | Run with this folder's profile, or the only one. The editor opens if there's none yet, and a picker if there are several |
| `box -p online kiro-cli` | Run with the profile `online` |
| `box -n online kiro-cli` | Create the profile `online` in the editor, then run |
| `box -n online --no-tui kiro-cli` | Save the preset as `online` without the editor (for scripts) |
| `box -e kiro-cli` | Edit the profile that would be used, then run |
| `box --dry-run kiro-cli` | Print the exact `bwrap` command instead of running it |
| `box -l` | List programs and their profiles |
| `box -r kiro-cli` | Delete the program's profiles and folder memory; asks separately about its private home |
| `box --doctor` | Check bubblewrap, the kernel and WSL |

Exit codes:
- the program's own code, or 128+N if a signal killed it;
- 125 for box's own errors (nothing ran);
- 126 when the program can't run, or is a Windows program;
- 127 when it isn't found.

## Profiles

Profiles are TOML files in `~/.config/box/profiles/<program>/<name>.toml`. That folder is never visible inside a sandbox, so a program can't change its own permissions. Each profile gets a private home folder in `~/.local/share/box/homes/<program>/<name>/`.

Any program starts from the same default: project read-write, network off, nothing else. A profile can also list `tools`, other commands the program runs (such as `kiro-cli`, `node` or `uvx` for MCP servers); box mounts their install folders read-only, like the program's own. Program folders are always read-only outside the project, so a boxed program can't rewrite itself or its tools. AppImages are extracted to `/tmp` instead of mounted with FUSE, and snaps are refused, because they can't start inside a sandbox. The editor suggests existing folders named after the program, such as `~/.<name>` and `~/.config/<name>`. Presets pre-fill `kiro-cli`, `kirocrew` and the shells.

## Known limits

- **Network on** means the whole network: the internet, your LAN, the Windows host on WSL and local services.
- **Network restricted** isn't enforced yet. The editor lets you limit a program to its own servers plus the documentation sites and services (AWS, Azure DevOps) you tick and the custom domains or IP addresses you add, and `--dry-run` shows the list. box refuses to run such a profile until the proxy in [docs/v2-network-design.md](docs/v2-network-design.md) is built, because it won't open the whole network in its place.
- **Ctrl+C** can end the whole box, not just the program, when the program isn't a full-screen app: bwrap passes the signal on and then stops the sandbox. Full-screen programs and shells aren't affected. `init = true` under `[sandbox]` in a profile runs the program under box's own small init, which gives it the terminal so Ctrl+C reaches only the program (Ctrl+Z is then ignored).
- **No GPU** inside the box on WSL.
- **Terminal injection** is blocked by current kernels, including WSL's. On kernels that still allow it (before Linux 6.2, such as Debian 12) box blocks it with a seccomp filter. On WSL the same filter blocks the VM's sockets to the Windows host (`AF_VSOCK`).
- **Abstract Unix sockets** (an X11 server's, for example) are reachable with network on. On Linux 6.12 or later, `landlock = true` under `[sandbox]` blocks them.

## Development

```
make test          # quick unit tests, run anywhere (macOS included)
make test-linux    # every test, uncached; on Linux/WSL that includes the end-to-end tests with real bwrap
make vet
./scripts/m6-check.sh   # 10 quick checks against real bwrap (Linux/WSL)
```

Real-program checks (Kiro CLI, Kiro Crew, Node, Burrito) are in [docs/m6-runbook.md](docs/m6-runbook.md); the planned per-domain network allowlist is in [docs/v2-network-design.md](docs/v2-network-design.md).

The design is in [docs/architecture.md](docs/architecture.md), and the corrections and milestones are in [docs/implementation-plan.md](docs/implementation-plan.md).

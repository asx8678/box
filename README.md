# box

`box` runs any installed Linux program inside a [bubblewrap](https://github.com/containers/bubblewrap) sandbox, limited to the folder you run it in. It's built for Ubuntu on WSL2, and works on other Linux distributions too.

```
cd ~/code/my-project
box kiro-cli
```

The first time, a one-screen editor (mouse or keyboard) asks what the program may use and saves the answers as a profile. After that, `box kiro-cli` starts straight away. box then replaces itself with `bwrap`, so nothing of box keeps running and the program gets the terminal directly.

> **Status:** milestones 1–5 of [the implementation plan](docs/implementation-plan.md) are written. The unit tests ran for milestones 1–2 on macOS; the end-to-end tests with real bubblewrap haven't run yet, and milestones 3–5 are compiled but untested. Run `make test-linux` on the WSL machine before relying on it.

## Install

```
sudo apt install bubblewrap
make build-linux                         # bin/box-linux-amd64 and bin/box-linux-arm64
install -m 755 bin/box-linux-amd64 ~/.local/bin/box
box --doctor                             # checks bubblewrap and this machine
```

On WSL no other setup is needed. On native Ubuntu 24.04 or later, `box --doctor` explains the AppArmor step if user namespaces are blocked.

## What the program can see

| | |
| --- | --- |
| Read-write | the project folder, the profile's private home, the folders you tick as read-write |
| Read-only | `/usr` and the system files programs need (certificates, DNS, time zone), the program's own install folder, the folders you tick as read-only |
| Hidden | your real home, `~/.ssh`, other projects, box's own settings, `/run/user`, Windows drives and Windows interop |
| Environment | rebuilt from scratch: `HOME`, `PATH`, the terminal and locale, and only the variables you tick |
| Network | on or off per profile; `--net` / `--no-net` for one run |

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

Any program starts from the same default: project read-write, network off, nothing else. The editor suggests existing folders named after the program, such as `~/.<name>` and `~/.config/<name>`. Presets pre-fill `kiro-cli`, `kirocrew` and the shells.

## Known limits

- **Network on** means the whole network: the internet, your LAN, the Windows host on WSL and local services. Per-domain rules are planned for v2.
- **Ctrl+C** can end the whole box, not just the program, when the program isn't a full-screen app: bwrap passes the signal on and then stops the sandbox. Full-screen programs and shells aren't affected.
- **No GPU** inside the box on WSL.
- **Older kernels** that allow terminal injection (before Linux 6.2, such as Debian 12) make box detach the program from the terminal, so resizing and job control don't work there.

## Development

```
make test          # unit tests, run anywhere (macOS included)
make test-linux    # adds the end-to-end tests with real bwrap; run on Linux/WSL
make vet
```

The design is in [docs/architecture.md](docs/architecture.md), and the corrections and milestones are in [docs/implementation-plan.md](docs/implementation-plan.md).

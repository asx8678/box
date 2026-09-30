# box — Implementation Plan

Draft for review · Sep 30, 2026 · based on [box — Architecture & Implementation Plan](architecture.md) (Sep 30, 2026)

**Target:** any installed Linux program, run from Ubuntu on WSL2 on Windows. Native Linux distributions work too.

This plan checks the scope of the architecture plan, corrects the parts that don't hold, and splits the work into milestones with acceptance criteria. It repeats what an implementer needs (CLI, profile schema, sandbox recipe, safety rules), so it can be followed without the architecture plan open.

## 1. Scope verdict

- **The design holds, and nothing in it has to be specific to one program.** A short-lived Go launcher that resolves a TOML profile and then `exec`s `bwrap` is the right shape. Keep it generic: every program gets the same handling (program lookup, private home, folders you tick). Presets are optional data files for the few programs named in the goals, and no box code knows about any particular program (S13).
- **WSL2 is a good host for box.** Microsoft's kernel has user namespaces on, doesn't have Ubuntu's AppArmor user-namespace restriction, and already blocks terminal injection (`TIOCSTI`). bwrap works right after `apt install bubblewrap`, and box needs no seccomp filter. WSL adds one escape route that box must close: Windows interop (B9).
- **Nine things must change before coding starts** (section 3.1). The most important:
  - `--new-session` breaks the "program owns the terminal" goal (B2);
  - Ubuntu's bwrap has an unfixed CVE (a program can plant symlinks that bwrap then follows) that matches box's private-home layout (B4);
  - Windows interop must stay out of reach (B9).
- **The work is about twice the estimate.** Expect about 3,100 lines of Go plus about 2,200 lines of tests, and 11–14 working days for one developer, instead of 1,500 lines and 4–6 days. The TUI alone is about 1,150 lines (section 5).
- **Only a Linux machine can run the sandbox.** This Mac has Go 1.27.1 but no Linux VM or container runtime. The core up to the bwrap argument list, and the whole TUI, can be built and tested here. Anything that runs bwrap is tested in WSL (B1).

## 2. Fact check

| Claim in the architecture plan | Result | Evidence or correction |
| --- | --- | --- |
| Go 1.27 released, current patch go1.27.1 | Confirmed | go.dev/dl lists go1.27.1 and go1.26.8; the local toolchain is go1.27.1 darwin/arm64. The "new in 1.27" list was not checked; box needs none of it. |
| Bubble Tea v2 at `charm.land/bubbletea/v2`, `MouseClickMsg`, mouse mode set in `View()` | Confirmed; version moved | Latest is **v2.0.10** (Sep 24), not v2.0.9. `View()` returns a struct with a `MouseMode` field (`MouseModeCellMotion`) and an `OnMouse` hook. |
| Bubbles v2 and Lip Gloss v2 | Confirmed | `charm.land/bubbles/v2` v2.2.1, `charm.land/lipgloss/v2` v2.0.6. `textinput` has `SetSuggestions`, and Tab is bound to `AcceptSuggestion`. Suggestions are a fixed list, so directory completion means recomputing them on every keystroke. |
| Click targets tracked as rectangles while rendering | Simpler option exists | Lip Gloss v2 has `Layer.ID()` and `Compositor.Hit(x, y)`. Give every clickable widget a layer ID and hit-test the click, with no hand-written registry. |
| `github.com/BurntSushi/toml` | Confirmed | v1.6.0 is the latest release. |
| `--ro-bind`, `--bind`, `--ro-bind-try`, `--unshare-all`, `--share-net` | Confirmed (man page) | `--unshare-all` means user-try, ipc, pid, net, uts and cgroup-try. |
| `--clearenv` + `--setenv` | Confirmed, not needed | box builds the final environment and passes it to `execve` itself (section 4.4). That works with any bwrap version. |
| `--new-session` blocks terminal injection | True, but the wrong trade-off | The man page says it "disconnects the sandbox from the controlling terminal". Without a controlling terminal the program gets no `SIGWINCH` on resize, `/dev/tty` fails to open, and `bash` has no job control. The target kernels already block injection (next row). See B2. |
| *(new)* Terminal injection on the target | Blocked by the kernel | `CONFIG_LEGACY_TIOCSTI` is off in the WSL2 kernel configs (6.6.123.2 and 6.18.40.1 checked), Ubuntu 24.04+ kernels, Debian 13, and Fedora 43–45. The check is `capable(CAP_SYS_ADMIN)` in the initial user namespace, so nothing inside a sandbox passes it. Debian 12's 6.1 kernel is older than the switch. |
| `--die-with-parent` kills the sandbox when box's terminal dies | Confirmed, with a side effect | It sets `PR_SET_PDEATHSIG` to SIGKILL, tied to bwrap's outer process. That process handles only `SIGCHLD` (`monitor_child` in `bubblewrap.c`). So a Ctrl+C that reaches it kills it, and the whole sandbox gets SIGKILL. See B3. |
| bubblewrap version | New facts | Latest is 0.13.0 (Sep 22). 0.12.0 (Aug 26) fixed **CVE-2026-87766**: files and directories that bwrap creates during setup could follow symlinks out of the sandbox. **Ubuntu's bwrap is unfixed:** 22.04 ships 0.6.1, 24.04 ships 0.9.0 and 26.04 ships 0.11.1, and USN-8779-2 (Sep 18) reverted the fix because it broke Flatpak. Debian 13, Fedora 43–45 and Arch are fixed; Debian 12 and RHEL 9 and 10 are not. See B4. |
| bwrap options box uses | Available | `--ro-bind-data` since 0.1.2, `--new-session` 0.1.7, `--die-with-parent` 0.1.8, `--clearenv` 0.5.0, `--disable-userns` 0.8.0, and idempotent `--symlink` 0.9.0. Ubuntu 22.04's 0.6.1 has everything except `--disable-userns`. |
| `--dev` gives a usable `/dev` | Confirmed (source) | It creates null, zero, full, random, urandom and tty, mounts a new devpts, and creates a `/dev/shm` directory. |
| Exit code is the program's own | Confirmed, with a caveat | bwrap returns the program's status, or 128+N if a signal killed it. bwrap's own setup errors exit 1, so they look like a program exit 1 (B7). |
| Ubuntu 23.10+ restricts user namespaces, and the bubblewrap package ships the profile that allows it | Doesn't apply on WSL; wrong for 24.04 | The restriction is an Ubuntu kernel patch, and WSL runs Microsoft's kernel, which doesn't have it. On native Ubuntu the restriction is on from 24.04 to 26.04, but the `bwrap-userns-restrict` profile is loaded by default only from 25.04. In 24.04 and 24.10 it sits unloaded among the extra profiles of `apparmor-profiles`. Even when loaded, it denies capabilities to nested sandboxes. |
| Kiro CLI binary `kiro-cli` installed into a folder on PATH | Confirmed, plus two helpers | The installer puts `kiro-cli`, `kiro-cli-chat` and `kiro-cli-term` in `~/.local/bin`, and `chat` runs the `kiro-cli-chat` binary next to it. Mounting the program's whole folder covers these (S4). |
| Kiro CLI config in `~/.kiro`, login state in `~/.local/share/kiro-cli/data.sqlite3` | Confirmed | `KIRO_HOME` overrides the config folder. The data path follows `XDG_DATA_HOME`. Logs go to `$XDG_RUNTIME_DIR`, or `/tmp/kiro-log/` when that isn't set. |
| Kiro CLI login uses device flow | Partly | By default it opens a browser (strings in the binary point to a localhost callback). `--use-device-flow` is there for remote and SSH use, and `KIRO_API_KEY` for headless use. Inside box on WSL the Windows browser can't be opened (B9), so use device flow. |
| Kiro Crew: `kirocrew`, Python 3.12+, data in `~/.kiro/crew/`, gateway on 5476 | Confirmed, more paths | The install script uses pipx, or a venv at `~/.kiro/crew-venv`. Crew also writes `~/.kiro/agents/`, `~/.kiro/settings/mcp.json` and `~/workplace/kirocrew-workspace`. It runs `kiro-cli acp` and reads Kiro CLI's login database. Its own agent sandbox needs nested user and mount namespaces (S10). |
| Open question: must the Kiro Crew gateway be reachable from outside? | Answered | By default it binds `127.0.0.1` with token auth (`KIROCREW_BIND` changes that), and chat apps connect outbound. Only Teams (a public HTTPS URL) and `POST /api/hooks/agent` need inbound access. With network on, the host reaches it at `127.0.0.1:5476`. |
| Burrito unpack folder and cleanup | Confirmed | The payload unpacks to `$XDG_DATA_HOME/.burrito` or `~/.local/share/.burrito`. `<APP>_INSTALL_DIR` replaces only the base folder. Older versions of the same app are deleted at launch. Each launch also writes `/tmp/libc-musl-<hash>.so` if it's missing; box gives each run a fresh `/tmp`, so that happens on every run. Burrito binaries have no reliable fingerprint, so box doesn't detect them; the generic default already works. |
| The "Tested" rows (mounts, environment, network, die-with-parent) | Not re-checked | There is no Linux on this machine. Milestone 0 re-runs them on WSL, and they become end-to-end tests. |

**Prior art.** Anthropic's sandbox-runtime (the sandbox behind Claude Code):
- Runs bwrap with `--new-session`, which suits it because it runs non-interactive commands.
- Binds `/` read-only.
- Filters network per domain through proxies on the host, bridged into an `--unshare-net` namespace over Unix sockets.
- Blocks `AF_UNIX` with seccomp, which it needs because `/run/WSL` is visible under a `/` bind.

box's layout, which mounts only what it names and nothing else, doesn't need that seccomp filter. box's v2 network proxy can copy the proxy part.

## 3. Design changes

### 3.1 Blocking: decide before milestone 1

**B1. Test where box will run.** The target is Ubuntu on WSL2, and there is no Linux on this Mac.
- **On the Mac:** milestone 1 (profiles and argument list) and milestone 4 (TUI) are plain Go, so they can be built and tested here.
- **On the WSL machine:** milestones 0, 2, 3 and 6 run there. Clone the repository and run `make test-linux`, or run Claude Code inside WSL for those milestones.
- **In CI (optional):** GitHub Actions (`ubuntu-24.04`, with the sysctl from section 4.8) is a useful second gate for native Ubuntu, but it can't test WSL interop.
- **In the code:** Linux-only files get `//go:build linux`, and a stub `exec_other.go` returns "box needs Linux" (exit 125). That keeps `go test ./...`, the TUI and `--dry-run` working on macOS.
- **Builds:** `linux/amd64` by default, plus `linux/arm64` for Windows on Arm.

**B2. Drop `--new-session`; rely on the kernel's TIOCSTI block.** The plan promises that the program owns the terminal (mouse, colours, Ctrl+C). `--new-session` takes the terminal away: no resize signal, no `/dev/tty`, no job control. It exists only to stop `TIOCSTI` keystroke injection, and the target kernels already stop that.
- box reads `/proc/sys/dev/tty/legacy_tiocsti`. If it is `0`, box does not pass `--new-session`.
- If it is `1`, or missing (kernels before 6.2, such as Debian 12), box passes `--new-session` and prints a one-line notice that resizing and job control won't work.
- A seccomp filter that blocks `TIOCSTI` would give those old kernels a working terminal too. Leave it for later. It needs a compare on only the low 32 bits of the ioctl request: Flatpak's CVE-2019-10063 was a 64-bit compare that could be bypassed.

**B3. Decide what Ctrl+C does.** bwrap's outer process stays in the terminal's foreground process group and handles only SIGCHLD. So when the program leaves the terminal in normal (cooked) mode, Ctrl+C kills the outer bwrap, and `--die-with-parent` then SIGKILLs the whole box before the program can clean up. That affects a Python REPL, a server printing logs, and possibly Kiro CLI while it streams a reply. Full-screen raw-mode programs receive Ctrl+C as a keypress and are fine. So is `bash`, which moves itself into its own process group.
- (a) Accept it and document it. No code.
- (b) Add a small init inside the sandbox. box bind-mounts its own static binary at `/run/box/box` and runs `/run/box/box --box-init -- <program> <args>`. The init puts the program in its own process group and makes that the terminal's foreground group (ignoring SIGTTOU while it does). It waits for the program, resumes it if Ctrl+Z stops it (Ctrl+Z isn't supported, just as in `docker run -it`), forwards SIGTERM and SIGHUP, and exits with the program's status. About 150 lines. box still leaves nothing running on the host.

Decide in the milestone 0 spike: run Kiro CLI and press Ctrl+C while it streams a reply. If that kills the box, build (b) in milestone 2.

**B4. Create mount points under writable folders in box itself (CVE-2026-87766).** bwrap creates every mount destination that doesn't exist yet. A destination can lie inside something the program can write:
- the private home (`~/.kiro`, `~/.local/share/kiro-cli`, or the project path when it's under `$HOME`);
- the project folder (the `.git/config` protection, or extra folders inside the project);
- any other read-write folder.

In that case, a program that plants a symlink there during one run can make bwrap create files and directories anywhere the user can write during the next run. bwrap 0.12.0 fixes this, but Ubuntu's bwrap is unfixed on every release, so on the target this guard is the only protection.

Before exec, box handles every destination under a read-write mount:
1. Walk the destination path from that mount's host folder, using `openat(..., O_NOFOLLOW | O_DIRECTORY)` at each step.
2. Create any missing directory (0700) or file (`O_CREAT | O_EXCL | O_NOFOLLOW`) in box itself.
3. Refuse to run if any path component is a symlink or the wrong type.

bwrap then finds every mount point already in place. Remaining risk: two boxes with the same profile running at once could race. Document that, and have `box --doctor` suggest bwrap 0.12 or later (for example, built into `/usr/local/bin`).

**B5. Order mounts by path depth, not by category.** The fixed order in the architecture plan binds `[extra]` (step 9) before `[workdir]` (step 10). An extra path inside the project, such as `ro ./secrets` or the `.git/config` protection (S2), is then hidden under the later workdir bind and silently does nothing. Build one list of mount operations and stable-sort it by the number of components in the destination path, so a parent always mounts before its children. Ties keep the category order. `Validate` rejects the same destination listed twice.

**B6. Resolve symlinks before checking safety rules.** The rules compare paths as strings. So if `~/code/link` is a symlink to `~/.ssh` and is added as a read-write extra folder, it passes every check, and bwrap then follows the link and mounts `~/.ssh` read-write. Canonicalize everything first with `filepath.EvalSymlinks`: `$HOME`, the working folder and every profile path. Run the rules on the real paths, give bwrap the real paths, and key folder memory by the real path.

**B7. Account for exec-and-vanish.** Once box calls `syscall.Exec`, it is gone. The architecture plan doesn't cover five consequences:
- **Error hints need a pre-flight probe.** box can't read bwrap's error text after exec. So first run `bwrap --unshare-all --ro-bind / / true` and turn its stderr into a hint. In the same step, read `bwrap --version`, `/proc/sys/kernel/apparmor_restrict_unprivileged_userns` (absent on WSL, which means skip it) and `legacy_tiocsti`. Cache success per bwrap binary (path + mtime), kernel release and boot ID. `box --doctor` runs the probe without the cache.
- **Exit codes.** 125 only covers failures before exec. bwrap's own setup failures exit 1, which looks the same as the program's exit 1. Add 127 (program not found) and 126 (not executable, or a Windows program), as `docker run` does.
- **Folder memory** is written just before exec, not "after a successful start".
- **No temporary files.** The `/run/box/profile` info file reaches bwrap as a memfd (`--ro-bind-data FD`) with close-on-exec cleared. A temporary file would be left behind with nobody to delete it.
- **Trust only a system bwrap.** Take it from `/usr/local/bin` or `/usr/bin`, require it to be owned by root and not writable by the user, and never pick it up from a user folder on `PATH`.

**B8. Put embedded presets next to the code that embeds them.** `go:embed` patterns can't contain `..`, so `internal/profile/presets.go` can't embed a top-level `presets/` folder. Put the TOML files in `internal/profile/presets/`. Treat the embedded presets as the source of truth, rather than writing copies to `~/.config/box/presets/` on first run: those copies go stale when box is upgraded. An optional user folder can override them.

**B9. Keep Windows interop out of the box (WSL).** On WSL, running any Windows program (`cmd.exe`, `explorer.exe`, anything under `/mnt/c`) hands it to Windows through a Unix socket: the one named by `$WSL_INTEROP`, or `/run/WSL/<pid>_interop`. The Windows process runs completely outside the sandbox, and turning network off doesn't stop it. Claude Code's sandbox had exactly this hole (claude-code issue #45072). box's layout already hides both routes, because it rebuilds the environment and `/run` is a fresh tmpfs. Make that a rule rather than a side effect:
- Never pass `WSL_INTEROP` (the `pass` list rejects it), and never mount `/run`, `/run/WSL` or `/mnt/wslg`.
- Refuse programs that resolve under `/mnt/<drive>/` or whose file starts with the `MZ` Windows header. Exit 126 with "Windows programs run outside Linux; box can't sandbox them".
- Treat `/mnt/<drive>`, `/mnt/<drive>/Users` and each `/mnt/<drive>/Users/<name>` like `$HOME`: never the working folder, never a read-write mount. Protect `…/Users/<name>/AppData` like the shell startup files in S1.
- Drop the `/mnt/<drive>` entries that WSL's `appendWindowsPath` adds from `PATH` inside the box.

Side effect: boxed programs can't open the Windows browser or clipboard, so logins must use device flow or a copied URL. An end-to-end test copies a Windows `.exe` into the project and expects it to fail to start.

### 3.2 Should-have: build these in, but don't block on them

**S1. Protect more than `~/.ssh` and `~/.gnupg`.** A read-write mount anywhere in these places means code that runs *outside* the box next time. So no read-write mount (the working folder included) may be `$HOME` or a parent of it, and none may equal, contain or sit inside:
- `~/.config/box` and `~/.local/share/box` (the profiles and every profile's private home);
- `~/.ssh` and `~/.gnupg`;
- every host `PATH` directory under `$HOME` (`~/.local/bin`, `~/bin`, `~/go/bin`, …) and the folder holding the box binary;
- shell startup files (`~/.bashrc`, `~/.bash_profile`, `~/.profile`, `~/.zshrc`, `~/.zshenv`, `~/.zprofile`, `~/.config/fish`);
- `~/.config/systemd` and `~/.config/autostart`.

Refuse daemon sockets in any mode: `/var/run/docker.sock` (Docker Desktop's WSL integration puts one in the distro), `/run/docker.sock`, `$XDG_RUNTIME_DIR/podman/podman.sock` and `/run/user/<uid>`. A read-only bind does **not** stop `connect()` on a socket. Show a warning in the TUI for any other socket file, and for read-only mounts of credential folders.

**S2. Keep `.git/config` and `.git/hooks` read-only** inside a read-write project. On by default; `protect_git = false` turns it off. A boxed agent that can write `.git/config` (`core.fsmonitor`, `core.hooksPath`, aliases) or a hook gets code execution the next time you run git outside the box. Commits inside the box still work; `git config` and `git remote add` inside fail. Skip it when `.git` is a file (worktrees).

**S3. Say plainly what `network = true` means.** On WSL, `--share-net` gives the program the WSL VM's network. In NAT mode that includes the LAN and the Windows host; in mirrored mode it also includes every Windows localhost service. It also shares the VM's abstract Unix sockets: list them in milestone 0 with `ss -xlp | grep @`. Say this in the TUI.

Landlock can close the abstract-socket gap. Just before exec, box applies a ruleset with no filesystem rules and `LANDLOCK_SCOPE_ABSTRACT_UNIX_SOCKET` (available in `golang.org/x/sys/unix`).
- WSL kernels enable Landlock, but only the 6.18 line has the needed ABI (6, from Linux 6.12). The 6.6 line doesn't.
- Milestone 0 must confirm that a ruleset with only this scope doesn't stop bwrap from mounting.

The v2 proxy design (`--unshare-net` plus a single socket to the proxy) removes the problem entirely.

**S4. Program lookup that works for real installs.** Mount the binary's *directory* read-only, so helper binaries next to it work (Kiro CLI's `kiro-cli-chat`). When the binary was reached through a symlink in another directory outside the system paths, mount that directory too.

For scripts, follow the shebang, including `/usr/bin/env NAME`. If the interpreter lives in a virtualenv, also mount the venv root and the base interpreter named by `home =` in `pyvenv.cfg`; that is how pipx, `uv tool` and Kiro Crew's `~/.kiro/crew-venv` install Python apps.

Build `PATH` inside the sandbox from the host `PATH` entries that are visible there, so the program's own subcommands and `npx`/`uvx` MCP servers resolve (section 4.6).

**S5. Create the program's folders instead of refusing.** `[home]` paths such as `~/.kiro` may not exist before the program's first run. Create them with mode 0700 rather than failing with "path does not exist". Keep refusing missing `[extra]` paths.

**S6. Nest private homes** as `homes/<program>/<profile>/`. With the flat `<program>-<profile>` name, `kiro-cli` with profile `x-y` and `kiro-cli-x` with profile `y` share one folder. Validate program and profile names (`[A-Za-z0-9._-]`, no leading dot). Also document that every folder using a profile shares its private home, so the home is a channel between projects. Per-folder homes can come later.

**S7. System mounts that real programs need.** Add `/etc/ld.so.cache`, `/etc/ld.so.conf` and `/etc/ld.so.conf.d` (WSL's GPU libraries are registered there), `/etc/nsswitch.conf`, `/etc/host.conf`, `/etc/gai.conf`, `/etc/os-release` and `/etc/terminfo`. For Fedora and RHEL hosts, also add `/etc/pki` and `/etc/crypto-policies`.
- On merged-`/usr` systems, recreate `/bin`, `/sbin` and `/lib*` with `--symlink usr/…` rather than binding them a second time.
- Bind `/etc` entries through their symlinks (bwrap follows the source). Never recreate `/etc/resolv.conf` as a symlink: on WSL and systemd hosts it points into `/mnt/wsl` or `/run`, which don't exist inside the box.
- Keep the default list in box and let profiles add `system.extra_ro`, so upgrading box fixes the list for every existing profile.

**S8. No terminal, no TUI.** If stdin or stdout isn't a terminal, behave like `--no-tui`: exit 125 with a hint, rather than drawing a TUI into a pipe. Reset asks for confirmation only on a terminal, and `-y` skips it. `-n NAME --no-tui` saves the preset unchanged, for scripts and tests.

**S9. Clean up the terminal between the TUI and exec.** After the Bubble Tea program exits, clear `O_NONBLOCK` on fds 0–2. Check in the end-to-end tests that no terminal replies (capability queries) are left in the input queue for the boxed program to read.

**S10. Allow nested sandboxes by default.** Kiro Crew's own agent sandbox, Chromium, Electron apps and Flatpak all create user namespaces, and box has to run any program. A nested namespace gives the program nothing it couldn't get outside the box. `sandbox.strict = true` adds `--disable-userns` (bwrap 0.8 or later) for programs that don't need them.

**S11. `box -r` keeps the private home unless asked.** The home holds Burrito payloads and possibly login state, so reset asks separately before deleting it.

**S12. Detect nesting.** If `/run/box/profile` exists, refuse with "already inside box".

**S13. Generic first.** Every program starts from the same default: project folder read-write, network off, a private home, nothing else. The TUI makes the rest one click away:
- It lists the existing folders named after the program, unticked: `~/.<name>`, `~/.config/<name>`, `~/.local/share/<name>`, `~/.local/state/<name>` and `~/.cache/<name>`, also trying the part of the name before the first `-`.
- It shows the network switch.

Presets are small TOML files that pre-tick folders and switches for known programs (`kiro-cli`, `kirocrew`, shells; section 4.8). box's code never checks for a program by name.

## 4. Corrected reference

### 4.1 CLI

As in the architecture plan: box's flags come before the program name, and everything after it goes to the program untouched. `box <program>`, `-p NAME`, `-n NAME`, `-e`, `-r`, `-l/--list`, `--net/--no-net`, `--dry-run`, `--no-tui`, `-h/--help` and `--version` keep their meanings. New:

| Command | What it does |
| --- | --- |
| `box --doctor` | Runs the bwrap probe without the cache. Prints the bwrap version and where it came from, user-namespace and AppArmor status, WSL and interop status, Landlock ABI and TIOCSTI state, and the fix for anything missing. |
| `box -r -y kiro-cli` | Resets without the confirmation prompt, for scripts. |
| `box -n NAME --no-tui kiro-cli` | Saves a new profile from the preset or generic default without opening the TUI. |

Parse with the standard `flag` package using `ContinueOnError`: its default `ExitOnError` exits with 2, which breaks the 125 rule. Define both spellings of each flag (`-l` and `--list`). Passing `--net` and `--no-net` together is an error.

Exit codes: the program's own (128+N if a signal killed it); 125 for a box error before exec or Cancel in the TUI; 126 if the program isn't executable or is a Windows program; 127 if it isn't found. A 1 may also be a bwrap setup failure.

### 4.2 Files and profile schema

```
~/.config/box/                                  0700, never mounted
  profiles/<program>/<name>.toml                0600
  folders.toml                                  (real folder path, program) → profile
  presets/<program>.toml                        optional overrides of the embedded presets
~/.local/share/box/homes/<program>/<profile>/   private HOME, 0700
~/.local/state/box/probe.json                   cached bwrap probe result
```

Changes from the architecture plan: `[system]` holds additions only, plus `protect_git` and an optional `[sandbox]` table. The `[home]` values below follow decision D3; the architecture plan had both folders read-write.

```toml
version = 1
program = "kiro-cli"          # must match the folder name
network = true

[workdir]
mode = "rw"                   # rw | ro
protect_git = true            # .git/config and .git/hooks stay read-only

[home]                        # the program's own folders, same path inside; created if missing
rw = ["~/.local/share/kiro-cli"]
ro = ["~/.kiro"]

[extra]                       # must exist
rw = []
ro = ["~/code/shared-lib"]

[env]
pass = ["TERM", "COLORTERM", "LANG", "KIRO_API_KEY"]   # values read at run time, never stored
set = { EDITOR = "vi" }

[system]
extra_ro = []                 # added to box's built-in system list

[sandbox]                     # advanced; not shown in the TUI
strict = false                # true adds --disable-userns (no nested sandboxes)
```

Load checks: the file is owned by the user and not group- or world-writable, `version` is known, and `program` matches the folder name.

### 4.3 Which profile runs

`-p NAME` → the `folders.toml` entry for (real working folder, program) → the program's only profile → the picker if there are several (on a terminal; otherwise an error that lists them and suggests `-p`) → the TUI if there are none (on a terminal; otherwise exit 125). box records the (folder, program) → profile pair just before exec.

A new profile starts from the preset with the program's name if there is one, and from the generic default (S13) otherwise.

### 4.4 Sandbox recipe

Namespace and process options:

```
--unshare-all [--share-net] --die-with-parent
[--new-session]      only when legacy TIOCSTI is on or unknown (B2)
[--disable-userns]   only with sandbox.strict (S10)
```

Mount operations go into one list. The list is stable-sorted by destination depth (ties keep the order below), then emitted:

| Category | Operations |
| --- | --- |
| system | `--ro-bind /usr /usr`; each of `/bin /sbin /lib /lib32 /lib64 /libx32` → `--symlink usr/X /X` if it's a symlink on the host, else `--ro-bind-try`; `/etc` entries → `--ro-bind-try` (bwrap follows the host symlink); then `system.extra_ro` |
| fresh | `--proc /proc`, `--dev /dev`, `--tmpfs /tmp`, `--tmpfs /run` |
| info | `--ro-bind-data <memfd> /run/box/profile` |
| private home | `--bind ~/.local/share/box/homes/<program>/<profile> $HOME` |
| program folders | `[home]` rw / ro → `--bind` / `--ro-bind`, same path |
| program binary | directories from section 4.6 → `--ro-bind`, same path (plus `/run/box/box` if the init from B3 is used) |
| extra | `[extra]` rw / ro → `--bind` / `--ro-bind` |
| workdir | `--bind` or `--ro-bind` `$PWD $PWD` |
| workdir protection | `--ro-bind` over `$PWD/.git/config` and `$PWD/.git/hooks`, if they exist |

Then `--chdir $PWD`, `--`, the program (or the init) and the user's arguments. All paths are canonical (B6), and box has already created every destination under a read-write mount (B4).

**Environment.** box builds the environment and passes it to `syscall.Exec`, so no `--clearenv` or `--setenv` is needed. It contains:
- `HOME`, `PATH` (the visible host `PATH` directories, without `/mnt/<drive>`), `PWD` and `TMPDIR=/tmp`;
- `TERM`, `COLORTERM`, `LANG`, `LC_*`, and `TZ` if set;
- the profile's `pass` variables that are set, and the `set` pairs;
- `BOX_PROFILE=<program>/<profile>`.

`--dry-run` prints `env -i K=V … bwrap …`, shell-quoted, so the output can be pasted and run.

### 4.5 Safety rules

All rules run on canonical paths. Any failure means exit 125 (126 for a Windows program), and nothing runs. Rules 1–3 check the working folder and the paths a profile configures (their host sources). box's own mounts are exempt: the private-home bind of `~/.local/share/box/homes/<program>/<profile>` onto `$HOME`, and the fresh `/tmp` and `/run`.

1. The working folder is not `/`, `$HOME`, or a parent of `$HOME`, and on WSL not `/mnt/<drive>`, `/mnt/<drive>/Users` or a Windows user folder (B9).
2. No read-write mount, the working folder included, is `$HOME` or a parent of it, and none equals, contains or sits inside a protected path from S1 or B9.
3. No mount in any mode is a daemon socket or a folder containing one (S1), or `/run`, `/run/WSL` or `/mnt/wslg` (B9).
4. `pass` never includes `WSL_INTEROP`, `SSH_AUTH_SOCK`, `GPG_AGENT_INFO` or `DBUS_SESSION_BUS_ADDRESS`. Each hands the program a way out of the box.
5. The program isn't a Windows executable (B9).
6. `[extra]` paths exist; `[home]` paths are created if missing; no destination is listed twice.
7. Profile files are owned by the user and not group- or world-writable; `~/.config/box` is 0700.
8. No component of a mount destination under a read-write mount is a symlink (B4).
9. bwrap comes from `/usr/local/bin` or `/usr/bin`, is owned by root and isn't writable by the user (B7).
10. box is not already running inside box (S12).
11. Fail closed: if bwrap is missing, the probe fails or exec fails, print the reason and exit 125. Never run the program unsandboxed.

### 4.6 Program lookup (S4)

1. A `NAME` containing `/` is resolved from the working folder; otherwise `exec.LookPath` searches the host `PATH`. Not found → 127; not executable or a Windows program → 126.
2. `EvalSymlinks`. If the real file is outside the system paths, mount its directory read-only. If it was reached through a symlink in a different directory outside the system paths, mount that directory too.
3. If the file starts with `#!`, take the interpreter; for `/usr/bin/env X`, look X up on the host `PATH`. Resolve the interpreter, and if it's outside the system paths, mount its prefix (the folder above `bin/`). If that prefix contains `pyvenv.cfg`, also mount the base prefix named by its `home =` line. Follow one shebang level only.
4. Run the invoked path, not the resolved one, so `argv[0]`, venv detection and relative lookups behave as they do outside.
5. The profile key is the basename of `NAME`.

### 4.7 Project layout

```
box/
  go.mod                        module github.com/asx8678/box, go 1.27
  main.go                       cli.Run(os.Args) → os.Exit
  internal/cli/                 run.go (flags, dispatch, resolution), list.go, doctor.go
  internal/profile/             profile.go, validate.go, paths.go, folders.go, suggest.go (S13),
                                presets.go, presets/*.toml
  internal/host/                host.go       OS access behind an interface (Getenv, Getwd, EvalSymlinks, Lstat, LookPath, ReadFile),
                                              used by both profile (canonicalization) and sandbox; fake.go for tests
  internal/sandbox/             plan.go       mount list, depth sort, environment, PATH
                                args.go       plan → bwrap arguments; dry-run rendering
                                resolve.go    section 4.6
                                wsl.go        WSL detection and the B9 rules
                                homeprep_linux.go, probe_linux.go, landlock_linux.go
                                exec_linux.go, exec_other.go
                                init_linux.go only if B3 chooses the init
  internal/tui/                 editor.go, widgets.go, complete.go, picker.go, confirm.go
  test/e2e/                     e2e_test.go (//go:build linux) and a small probe helper
  Makefile                      build, build-linux (amd64 + arm64), test, test-linux, install
  .github/workflows/ci.yml      optional (B1)
```

The `Host` interface is the main testing seam. The whole lookup → plan → arguments pipeline runs against a fake filesystem in unit tests, on macOS, including WSL paths.

Dependencies: `charm.land/bubbletea/v2` v2.0.10, `charm.land/bubbles/v2` v2.2.1, `charm.land/lipgloss/v2` v2.0.6, `github.com/BurntSushi/toml` v1.6.0 and `golang.org/x/sys`. The pty helper for tests is either `github.com/creack/pty` or about 40 lines over `x/sys`. Build with `CGO_ENABLED=0 go build -trimpath -ldflags='-s -w'`; the static binary is also what the B3 init needs.

### 4.8 Programs and platforms

**Ubuntu on WSL2 (target)**
- **Setup:** `sudo apt install bubblewrap` (0.9.0 on 24.04, unfixed for the CVE, so B4 matters) and nothing else: no AppArmor step. `box --doctor` suggests a newer bwrap in `/usr/local/bin`.
- **Kernels:** WSL ships a 6.6 line and a 6.18 line. Both have user namespaces and Landlock, and neither allows legacy TIOCSTI. Only 6.18 supports the abstract-socket scope (S3).
- **Interop:** as in B9.
- **Network:** as in S3. NAT mode reaches the LAN and the Windows host; mirrored mode also reaches Windows localhost services.
- **Limitations to document:** there's no GPU inside the box, because `/dev/dxg` isn't in the minimal `/dev`. Projects under `/mnt/c` work but are slow, as they are outside box.

**Native Ubuntu 24.04–26.04, and CI**
- **AppArmor:** the user-namespace restriction is on. Fix it by loading `bwrap-userns-restrict` (25.04 and later load it by default) or by setting `sudo sysctl -w kernel.apparmor_restrict_unprivileged_userns=0`. Nested sandboxes such as Kiro Crew's need the sysctl.
- **Probe errors:** "setting up uid map: Permission denied", or "loopback: Failed RTM_NEWADDR: Operation not permitted" with `--unshare-net`. The probe maps them to the fix.
- **GitHub Actions `ubuntu-24.04`:** add the sysctl step, as bubblewrap's own CI does (PR #728). `ubuntu-latest` moves to 26.04 between Oct 19 and Nov 19, 2026.

**Presets (data files only)**
- **`kiro-cli`:**
  - network on;
  - `~/.local/share/kiro-cli` read-write, and `~/.kiro` per D3;
  - suggested variables `KIRO_API_KEY` and `AWS_PROFILE`.
  - Program lookup mounts `~/.local/bin`, which covers the helper binaries. Log in with `kiro-cli login --use-device-flow` inside the box.
  - MCP servers need their runtimes (`node`/`npx`, `uv`/`uvx`) to be visible: fine when they're under `/usr`, otherwise tick their folders.
- **`kirocrew`:** everything in `kiro-cli`, plus:
  - `~/.local/bin` read-only (for `kiro-cli`);
  - `~/.kiro` read-write (Crew writes `agents/` and `settings/mcp.json`) and `~/.kiro/crew` read-write;
  - `~/workplace/kirocrew-workspace` read-write, created if missing;
  - network on, and nested sandboxes allowed (S10).
  - The venv comes from shebang handling.
- **`bash`, `sh`, `zsh`:** network off, nothing extra.
- **Everything else, Burrito apps included:** the generic default. Burrito's payload unpacks into the private home and is reused on later runs.

## 5. Estimate

| Package | Go lines | Test lines | Notes |
| --- | --- | --- | --- |
| cli | ~450 | ~250 | flags, dispatch, resolution order, list, reset, doctor |
| profile | ~600 | ~450 | schema, load/save, safety rules, folder memory, presets, folder suggestions |
| sandbox | ~850 (+150 for the init) | ~550 unit, ~500 e2e | lookup, plan, arguments, WSL rules, probe, mount-point preparation, exec |
| tui | ~1,150 | ~400 | editor, widgets, completion, preview, picker, confirmation |
| **Total** | **~3,050–3,200** | **~2,150** | the architecture plan estimated ~1,500 including the TUI |

Time for one developer: 11–14 working days (per-milestone figures in section 6). Most of the uncertainty is in the TUI (milestone 4) and in the real-program runs (milestone 6). box works from the command line with preset or hand-written profiles after milestone 3, so the TUI can slip without blocking use.

## 6. Milestones

Each milestone ends with something usable and a check that proves it.

**M0. WSL environment and spikes** (1–1.5 days, WSL)
- [ ] On the WSL machine: `sudo apt install bubblewrap`, Go 1.27.1 from go.dev (Ubuntu's `golang` package is older), and a clone of the repository. Record `uname -r`, `bwrap --version`, `/sys/kernel/security/lsm` and `/proc/sys/dev/tty/legacy_tiocsti`.
- [ ] `go mod init github.com/asx8678/box` with `go 1.27`, a Makefile (`build-linux` for amd64 and arm64, `test`, `test-linux`), and a CI skeleton if wanted.
- [ ] Terminal spike with a hand-written bwrap command (without `--new-session`) and a small pty harness. Check that a resize reaches the program as `SIGWINCH`, that `/dev/tty` opens, and that `bash` job control works. Then try Ctrl+C in a Python REPL, and Ctrl+C while Kiro CLI streams a reply.
- [ ] Interop spike, with box's layout (fresh `/run`, clean environment):
  - check that `cmd.exe` and a Windows `.exe` copied into the project fail to start;
  - list abstract sockets with `ss -xlp | grep @`;
  - check whether an unprivileged process can reach Windows host services over `AF_VSOCK`. This is unverified; if it can, box needs a seccomp filter against `socket(AF_VSOCK)`.
- [ ] On a 6.18 kernel, check that a scope-only Landlock ruleset applied before exec leaves bwrap able to mount (S3).
- [ ] Re-run the architecture plan's "Tested" facts as scripts; they become end-to-end tests in M2.
- **Done when** the B3 choice (plain exec or init) and the S3 choice (Landlock or not) are written down in `docs/decisions.md`, along with the WSL facts.

**M1. Profiles and argument list** (2 days, macOS)
- [ ] `profile`: types; TOML load and save (temp file + rename, files 0600, folders 0700); ownership checks; name validation; `~` and XDG expansion; canonicalization through `Host`; the safety rules (WSL rules included); duplicate check.
- [ ] Embedded presets (section 4.8) and the generic default.
- [ ] `host`: the OS interface and a fake filesystem for tests. It gets its own package so that `profile` and `sandbox` can both use it without an import cycle.
- [ ] `sandbox`: the plan builder (mount list, depth sort, environment, `PATH`); argument rendering; shell-quoted dry-run output.
- [ ] Tests:
  - a golden argument list for each preset and for the generic default, against the fake host;
  - a refusing test for each safety rule;
  - nested-mount ordering;
  - a symlink to `~/.ssh`;
  - Windows paths;
  - the environment contains only allowed variables;
  - quoting round-trips.
- **Done when** `go test ./...` passes on macOS and a reviewer has read the golden files.

**M2. Run it** (2–2.5 days, WSL)
- [ ] Program lookup (section 4.6), including shebang and venv handling and refusing Windows programs.
- [ ] `/run/box/profile` as a memfd; the probe and its cache; mount-point preparation (B4); the TIOCSTI decision (B2); the `O_NONBLOCK` reset; exec that fails closed; the `exec_other.go` stub.
- [ ] The init from B3, if M0 chose it.
- [ ] `cli`: `box <program>`, `-p`, `--dry-run`, `--net/--no-net`, `--no-tui`, `--doctor`, exit codes.
- [ ] The end-to-end suite from section 7, green on WSL.
- **Done when** `box --no-tui -p default bash`, run in a project, gives a shell that sees only the project, follows terminal resizes, has job control, and can't start Windows programs.

**M3. Presets, folder memory, list and reset** (1 day)
- [ ] Preset files for `kiro-cli`, `kirocrew` and shells; the generic default; folder suggestions (S13).
- [ ] `folders.toml` and the resolution order (section 4.3), with clear errors when there's no terminal.
- [ ] `-l`; `-r` with `-y` and a separate question about the private home; `-n NAME --no-tui`.
- **Done when** a test covers every step of the resolution order, and `box -l` and `box -r` work on WSL.

**M4. TUI editor** (3–4 days, mostly on macOS with `--dry-run`)
- [ ] Widgets drawn as Lip Gloss layers with IDs, so clicks resolve through `Compositor.Hit`. Keyboard focus ring: Tab, Shift+Tab, arrow keys, Space, Enter, Esc.
- [ ] All sections from the mockup:
  - project folder mode and the network switch;
  - program folders with an rw/ro toggle and the S13 suggestions;
  - extra folders (add, remove);
  - environment variables (tick, add);
  - profile name and buttons.
- [ ] The add-folder input: suggestions recomputed from the directory being typed; safety rules checked on every keystroke, with errors shown in red.
- [ ] The preview pane: a viewport showing the dry-run output.
- [ ] First run, `-n` and `-e` flows. Save validates, writes atomically, then runs; Cancel exits 125.
- [ ] A layout that works at 80×24 and in narrower terminals. Also check mouse clicks in Windows Terminal.
- [ ] Model tests: key and mouse messages in, resulting profile out. Cover a click on every widget type, adding and removing folders, and an invalid path blocking Save.
- **Done when** a new user can create a working profile for an unknown program using only the mouse, and again using only the keyboard.

**M5. Picker, confirmation and polish** (1 day)
- [ ] The profile picker and the reset confirmation screen.
- [ ] Probe error text mapped to fixes (AppArmor, user namespaces disabled, bwrap missing or untrusted).
- [ ] The Landlock scope, if M0 chose it.
- [ ] `--help`, `--version` (from build info), and a README. It covers what network on means on WSL, interop, git protection and Ctrl+C behaviour.

**M6. Real programs, one of each kind** (1.5–2 days, WSL)
- [ ] A dynamically linked program with helper binaries: Kiro CLI. Log in with device flow, chat, press Ctrl+C during a reply, resize the terminal, and start an MCP server through `npx` or `uvx`. Also list what it writes under `~/.kiro` (input to D3).
- [ ] A Python app in a venv: Kiro Crew. Run `kirocrew doctor`, reach the gateway at `127.0.0.1:5476` from the host, and run its nested sandbox.
- [ ] A Node program installed under `~/.nvm` or `~/.local`.
- [ ] A self-extracting program: a Burrito app. The first run unpacks into the private home, and the second run reuses it.
- [ ] A static binary and a shell.
- [ ] For each program:
  - confirm that `~/.ssh`, sibling projects and `/mnt/c` are invisible;
  - note what failed, and whether a generic rule or only a preset fixes it. Prefer generic rules.
- **Done when** every one runs with at most a preset or a few ticked folders.

## 7. Test plan

**Unit tests (macOS and Linux)** cover everything up to the argument list, because that is where the security lives:
- golden argument lists per preset;
- one refusing test per safety rule;
- depth ordering;
- canonicalization;
- the Windows-path and Windows-program rules;
- environment filtering;
- dry-run quoting;
- the resolution order;
- the B2 decision given each `legacy_tiocsti` value.

TUI tests work at the model level (send key and mouse messages, check the resulting profile), not with screen snapshots.

**End-to-end tests (Linux, skipped when the probe fails)** run the real binary with real bwrap and a probe inside the box. From the architecture plan:
- the program can write the project folder and the private home;
- it cannot see the real home, `~/.ssh`, a sibling project or `~/.config/box`;
- it cannot write `/usr`;
- only the allowed environment variables exist;
- network works with `--net` and fails with `--no-net`;
- arguments with spaces and quotes arrive intact;
- box refuses to start in `/` and `$HOME`, and exits 125 when bwrap is missing.

New in this plan:
- `ioctl(TIOCSTI)` fails; a resize reaches the program; `/dev/tty` opens; `bash` has job control;
- a read-only extra folder inside a read-write project stays read-only (B5);
- `.git/config` and `.git/hooks` are read-only while `git commit` works (S2);
- a symlink planted in the private home or the project makes box refuse to run (B4);
- an extra folder that is a symlink to `~/.ssh` is refused (B6);
- a Docker socket is refused even read-only (S1);
- exit codes 125, 126, 127, the program's own code, and 128+N after a signal;
- after the TUI, stdin is blocking and no stray input reaches the program (S9);
- nested sandboxes work by default and fail with `strict` (S10).

On WSL only (skipped unless `/proc/sys/fs/binfmt_misc/WSLInterop` exists):
- `WSL_INTEROP` and `/run/WSL` are absent inside;
- a copied Windows `.exe` fails to start;
- `box cmd.exe` exits 126;
- `/mnt/<drive>` entries are gone from `PATH`.

**CI** (optional): GitHub Actions on `ubuntu-24.04` with the sysctl step, running the unit and end-to-end suites. It can't run the WSL-only tests; those run on the WSL machine.

## 8. Risks

The architecture plan's risk table still applies. New or changed:

| Risk | Mitigation |
| --- | --- |
| Terminal behaviour inside the box (resize, Ctrl+C, `/dev/tty`) | B2 and B3; the M0 spike decides before any code depends on it |
| Ubuntu's bwrap stays vulnerable to CVE-2026-87766 | The B4 guard in box; `box --doctor` suggests a newer bwrap |
| Windows interop runs programs outside the sandbox | B9 rules and WSL-only end-to-end tests |
| Windows host services reachable over `AF_VSOCK` (unverified) | Checked in the M0 spike; seccomp against `socket(AF_VSOCK)` if needed |
| A read-write `~/.kiro` lets a prompt-injected agent add MCP servers (`settings/mcp.json`) or hooks that run *unsandboxed* the next time Kiro CLI is used outside box | `~/.kiro` read-only for `kiro-cli` (D3). Kiro Crew needs it writable, so its preset accepts that risk and says so |
| Nested sandboxes allowed by default (S10) | They add nothing a normal process on the host can't do; `sandbox.strict` for programs that don't need them |
| MCP servers need `node`, `npx` or `uv` inside the box, sometimes installed under `~/.nvm` or `~/.local` | `PATH` built from visible directories (S4); the TUI suggests folders |
| Programs that keep secrets in the desktop keyring break, because `/run/user` is hidden | Check in M6; document it |
| Network on reaches the LAN, the Windows host and abstract sockets | S3: warning, Landlock on 6.18 kernels, then the v2 proxy |
| The TUI takes longer than planned | box is usable without it after M3 |

## 9. Decisions needed

- **D1. Where the Linux work happens:** the WSL machine for M0, M2, M3 and M6 (recommended, ideally with Claude Code running inside WSL), plus GitHub Actions as an optional gate?
- **D2. Ctrl+C:** decide after the M0 spike. Build the init (B3 b) if Kiro CLI is affected; otherwise document the limitation.
- **D3. `~/.kiro` for `kiro-cli`:** read-only (recommended, per the risk above) or read-write, as in the architecture plan? Kiro Crew needs it read-write regardless.
- **D4. Git protection (S2) on by default:** recommended yes.
- **D5. Module path:** `github.com/asx8678/box`, matching the repository?
- **D6. Platforms:** Ubuntu 24.04 or later on WSL2 as the supported target, with native Linux supported on a best-effort basis (plus the AppArmor step on Ubuntu)?

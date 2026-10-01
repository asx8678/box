# Batch 1 plan: security and lost-work fixes (backlog items 1–24)

**Status: done** on branch `batch-1`, one commit per group (A `55da810`, D `7659337`, B `0af6af0`,
C `7c4026d`, E `ad6dfa3`, F `57f43ba`). Unit tests, vet and the Linux build pass on macOS. Still
to do on the WSL machine: `make test-linux`, which runs the new end-to-end tests for items 3, 6,
8, 9 and 10.

1 October 2026 · scope checked against the code on main (`c0911a5`, plus the uncommitted simplification)

Batch 1 is 24 items: 21 tagged "now" and 3 tagged "later" (11, 13, 15). This plan does the 21 "now"
items and moves 11, 13 and 15 to batch 5, where the other hardening is. They are real, but each
needs more design than a fix, and nothing else in this batch depends on them.

The work is grouped by where it lands in the code, not by item number, so each group is one
commit with its own tests. Groups A to C change what the sandbox allows and come first.

## Decisions (settled 1 October 2026: all four recommendations accepted)

| # | Question | Recommendation |
|---|---|---|
| D1 | Item 3: bind all of `.git` read-only? Then the program can't commit, and agents like Kiro often do. | Make `protect_git` a mode: `"full"` (all of `.git` read-only, the new default) or `"hooks"` (today's config + hooks only, for profiles that need commits). Old `true` loads as `"hooks"`, so existing profiles keep working. |
| D2 | Item 4: a project with no `.git` yet. The program can `git init` and add a hook that runs the next time you use git outside. | With `"full"`, mount an empty read-only `.git` when none exists, so `git init` fails inside the box. Document it. |
| D3 | Item 21: `--dry-run` opens the editor and you press Save. Save the profile or not? | Save it (that is what the button says) and print "saved profile X; nothing ran". Item 53 later renames the button under dry run. |
| D4 | Item 7: root. Refuse, or drop capabilities? | Refuse uid 0, with a clear message. Dropping capabilities correctly is more code for a case nobody needs. |

## Group A: the hand-over to bwrap (items 6, 7, 9, 10)

Files: `internal/sandbox/exec_linux.go`, `args.go`, `main.go`, `internal/cli/run.go`

- **6, `LD_PRELOAD` reaches bwrap.** Confirmed in code: `Exec` passes the program's environment,
  including `env.set`, as bwrap's own environment, so `LD_PRELOAD` from a profile loads into bwrap
  before any sandbox exists.
  Fix: exec bwrap with an empty environment and set the program's variables inside with
  `--setenv NAME VALUE`. These go in a memfd read with bwrap's `--args FD`, not in argv, because
  argv is readable by every user through `/proc/PID/cmdline` and the values include API keys.
  The dry run shows them as ordinary `--setenv` lines (values hidden per item 1) and drops `env -i`.
  Golden files change; review the diff.
- **7, root.** Refuse to start when `os.Getuid() == 0`, before anything else. (D4)
- **9, `box --box-init -- prog` runs with no sandbox.** `Init` refuses unless `/run/box/profile`
  exists. Only a sandbox has that file: `/run` is root's, so it can't be faked outside.
- **10, inherited file descriptors.** Before creating the memfds, mark every fd from 3 up
  close-on-exec with `close_range(3, ~0, CLOSE_RANGE_CLOEXEC)`. Fall back to walking
  `/proc/self/fd` on kernels before 5.11. The memfds are made afterwards without the flag, so
  only they pass.

Tests: unit tests for the args memfd contents and the dry-run text; golden updates. Real checks
need Linux: an e2e test that `LD_PRELOAD` in `env.set` doesn't reach bwrap, and one that an open
fd 5 isn't visible inside.

## Group B: what may be mounted (items 5, 12, 14, 16)

Files: `internal/profile/safety.go`, `internal/sandbox/plan.go`, `resolve.go`

- **5, a script steers the mounts.** Confirmed: `Lookup` trusts the shebang and `pyvenv.cfg` of
  the program file. A script in a writable folder (the project) can name `~/.ssh/x` as its
  interpreter, and box mounts `~/.ssh` read-only on the next run.
  Fix: a new `Protected.CheckRead`, refusing secret folders (`~/.ssh`, `~/.gnupg`, `~/.aws`,
  `~/.kube`, box's own config and data, and the whole home). Apply it to every folder from
  `Lookup` and from tools. The refusal names the script and the line that caused it.
- **12, PATH folders protected only under home.** Add every absolute PATH entry to `NoWrite`,
  not only those under `$HOME`. `/opt/tool/bin` owned by you is as dangerous as `~/.local/bin`.
- **14, box's own config and data mountable read-only.** Add `Config`, `Data` and `State` to
  `NoMount`. The private home is bound by box itself, so it doesn't go through this check.
- **16, missing protected files.** Add `~/.bash_aliases`, `~/.inputrc`, `~/.aws`, `~/.kube`,
  `~/.docker`, `~/.npmrc`, `~/.config/pip`, `~/.vimrc`, `~/.config/nvim`, `~/.tmux.conf` and
  `~/.config/Code`. Resolve the `.config/…` entries under `XDG_CONFIG_HOME` when it is set.

Tests: table tests with `host.Fake` for each new refusal, including the shebang-to-`~/.ssh` case.

## Group C: git protection (items 3, 4)

Files: `internal/profile/profile.go` (schema), `validate.go`, `internal/sandbox/plan.go`, presets

- **3** Depends on D1. `"full"` binds `.git` read-only, then `"hooks"` keeps today's behaviour.
  Close the bypasses the review found: `.git/commondir`, the index, `HEAD` and `.git/modules`
  are all covered once the whole folder is read-only.
- **4** Depends on D2. Also covers:
  - a `.git` *file* (worktree or submodule): resolve its `gitdir:` and protect that folder,
    refusing it if it is outside the project and not already read-only;
  - nested repos: protect every `.git` found in the project to a bounded depth (say 4 levels,
    500 folders) and print a note if the scan stopped early.

  New repos the program creates in subfolders can't be prevented this way. Write that down in
  the README together with item 85.

Tests: plan tests for each layout (plain repo, no repo, `.git` file, nested repo, symlinked
`.git`); golden update for the new default.

## Group D: secrets in output (item 1)

Files: `internal/sandbox/args.go`, `plan.go`

- **1** The plan records which variables came from the host (`env.pass`, the locale, `TERM`).
  The dry run and Preview print those as `--setenv NAME "$NAME"`, so a pasted command still
  works and the screen shows no key. `env.set` values are written by you in the profile, so
  they are shown.

Tests: a dry-run test with `FOO_API_KEY=secret` that asserts "secret" never appears.

## Group E: command line (items 8, 21, 22, 23)

Files: `internal/cli/run.go`, `manage.go`, `internal/sandbox/plan.go`, `test/e2e`

- **8, `strict` makes bwrap refuse.** bwrap requires `--unshare-user` with `--disable-userns`,
  and `--unshare-all` only *tries* it. Add `--unshare-user` when strict. Fix the e2e test: today
  it passes on any failure, so make it first assert that the program ran, then that nesting
  failed. Needs Linux to confirm.
- **21, the editor opens before the folder check.** Run the workdir checks (`CheckWorkdir`,
  `CheckMount`) before choosing a profile, so running in `~` fails at once, not after editing.
  The dry-run part depends on D3.
- **22, a damaged `folders.toml` blocks everything.** On a parse error, warn, rename it to
  `folders.toml.bad` and continue with empty memory. A wrong owner or mode stays a refusal, as
  for profiles.
- **23, `/dev/null` counts as a terminal.** Use `term.IsTerminal` from `charmbracelet/x/term`
  (already in the module graph; it becomes a direct dependency) instead of the char-device test.

## Group F: the screens (items 2, 17, 18, 19, 20, 24)

Files: `internal/tui/*`, `internal/cli/manage.go`

- **2 and 20 share a cause:** keys typed before a screen appears are read by it. A double-tapped
  `y` in `box -r` answers the second question; an early Enter saves the preset and runs.
  Fix in one place: before each TUI program starts, flush pending terminal input, and ignore
  key presses for the first 200 ms after the first frame is drawn. The second reset question
  also starts with No focused (it already does) and gets the flush.
- **17, Save & run drops text in an open add box.** On Save, Preview or Enter elsewhere, submit
  a non-empty open input first. If it is invalid, show its error and don't save.
- **18, Cancel can't be confirmed from the keyboard.** Confirmed: any key except Esc resets
  `armed`, so Enter on Cancel arms it again forever. Only reset `armed` when something other
  than Cancel or Esc is activated.
- **19, focused Cancel looks like Save & run.** Give the focus of a secondary button its own
  style (the normal fill, bold, with ‹ › markers) so only the primary button is orange.
  Item 30 in batch 2 does the full marker pass.
- **24, control characters in names.** One `printable` helper replaces control characters and
  escape sequences with visible escapes (`\x1b`). Apply it to everything from disk or the user
  that the editor, picker, confirm screen, `-l` and launch line draw.

Tests: TUI unit tests for 17, 18 and 20 (keys sent before the first frame); a snapshot that a
folder name with `\x1b[31m` is drawn escaped.

## Order and checks

1. Commit the current simplification on its own.
2. Settle D1 to D4.
3. Groups A, B, C, D, E, F, one commit each. After each: `make test`, `make vet`,
   `GOOS=linux go vet ./...`, and a reviewed golden diff where plans change.
4. On the WSL machine: `make test-linux` for the items only Linux can check (6, 8, 10, and 7's
   message). Until then, mark those items "done, not verified on Linux" in the backlog.
5. Tick the items in `docs/backlog.md`.

## Moved to batch 5

- **11** The planted-symlink check only knows this run's writable folders. A full fix needs
  knowledge across profiles and runs.
- **13** WSL rules generalised beyond `/mnt/<letter>` and AppData (read `wsl.conf`'s `root`,
  protect the Startup folder).
- **15** A folder holding a socket. Needs a bounded scan and a decision on how deep to look.

Rough size: groups A, B and C are about half the work; D, E and F are small. About 600–900
changed lines including tests.

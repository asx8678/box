# box — Decisions

Choices the implementation plan leaves to evidence from a real Linux/WSL machine (milestone 0). Each one has a switch in the code, so the decision is a default to flip rather than code to write.

| # | Question | Switch | Default now | How to decide | Decision |
| --- | --- | --- | --- | --- | --- |
| B3 | Does Ctrl+C in a cooked-mode program end the whole box? (It should, from bwrap's source.) | `[sandbox] init` | off | Run `go test -run TestCtrlC ./test/e2e` on WSL, then press Ctrl+C while `box kiro-cli` streams a reply. If the box dies, turn `init` on in the presets, or make it the default. | open |
| S3 | Does a Landlock scope-only ruleset, applied before exec, still let bwrap mount? | `[sandbox] landlock` | off | On a 6.12+ kernel (the WSL 6.18 line), `box --doctor` shows the ABI; run `go test -run TestLandlock ./test/e2e`. If it passes, make it the default when network is on. | open |
| B9 | Can a sandboxed program reach Windows host services over `AF_VSOCK`? | seccomp filter | blocked on WSL | Nothing to decide: blocking it costs normal programs nothing. Confirm with `go test -run TestVsock ./test/e2e` on WSL. | blocked |
| B2 | Terminal injection on the target | kernel, then seccomp | kernel on WSL | `box --doctor` shows which one applies. | kernel |
| D3 | `~/.kiro` read-only for kiro-cli? | preset | read-only | Run Kiro CLI inside box and see what it writes under `~/.kiro`. | open |

Record the kernel (`uname -r`), `bwrap --version` and the date next to each decision when it's made.

## Evidence so far

**GitHub Actions, Ubuntu 24.04 (x86), main at `0844819`:** `box --doctor` passed and the end-to-end suite passed with `BOX_E2E_REQUIRED=1`, so nothing was skipped except Landlock (kernel older than 6.12) and the WSL-only tests. That includes `TestCtrlC/init`: with `sandbox.init` on, the program survives Ctrl+C. The `TestCtrlC/plain` result is only in the job log.

**A real WSL2 machine, from the deleted `feat/box-implementation` branch** (a separate implementation of the same plan, so it says what the host allows, not how main behaves there):

| Fact | Value |
| --- | --- |
| Kernel | 6.6.87.2-microsoft-standard-WSL2 |
| bubblewrap | 0.9.0, `/usr/bin/bwrap`, root-owned (unpatched for CVE-2026-87766, so main's mount-point guard matters) |
| `legacy_tiocsti` | 0: the kernel blocks terminal injection, so no `--new-session` |
| AppArmor user-namespace knob | absent (nothing to set up on WSL) |
| Boundary checks | 10/10 with that branch's check script: project rw, `~/.ssh` hidden, `/usr` ro, env filtered, exit code 7, signal 143, `/dev/tty`, network off and `--net` |

That branch's build machine was itself a nested sandbox where bwrap couldn't create namespaces (and `/usr/bin/bwrap` showed as owned by `nobody`). main's trusted-bwrap rule refuses such a bwrap, which is correct: there, sandboxes can't work anyway.

Next on the WSL machine: `./scripts/m6-check.sh` (the same 10 checks, adapted to main) and `docs/m6-runbook.md`.

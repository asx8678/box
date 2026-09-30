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

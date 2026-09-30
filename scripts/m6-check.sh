#!/usr/bin/env bash
# M6 boundary check: quick checks of the real sandbox with bash, on a host
# where `box --doctor` ends with "ok: box can run sandboxes here". It uses a
# throwaway HOME, so your real ~/.config/box is untouched. Interactive apps
# (Kiro CLI, Kiro Crew, Burrito) are covered by docs/m6-runbook.md.
#
# Usage:  ./scripts/m6-check.sh        (BOX=path/to/box to test another build)
set -u

repo="$(cd "$(dirname "$0")/.." && pwd)"
BOX="${BOX:-$repo/bin/box}"
[ -x "$BOX" ] || (cd "$repo" && make build >/dev/null) || { echo "build failed"; exit 2; }

pass=0; fail=0
ok()  { printf '  \033[32mPASS\033[0m %s\n' "$1"; pass=$((pass+1)); }
bad() { printf '  \033[31mFAIL\033[0m %s\n' "$1"; fail=$((fail+1)); }

work="$(mktemp -d)"; trap 'rm -rf "$work"' EXIT
proj="$work/home/code/proj"; mkdir -p "$proj"; echo hi > "$proj/hello.txt"
env_base=( "HOME=$work/home" "PATH=/usr/local/bin:/usr/bin:/bin" "TERM=${TERM:-xterm}" )
runbox() { ( cd "$proj" && env -i "${env_base[@]}" "$BOX" --no-tui "$@" ); }

echo "== M6 boundary checks (real bwrap) =="

# 1. bwrap must work here, else the rest is meaningless.
if (cd "$proj" && env -i "${env_base[@]}" "$BOX" --doctor) | grep -q "ok: box can run sandboxes here"; then
  ok "bwrap can create a sandbox"
else
  bad "box --doctor failed: run on a host with user namespaces"; exit 1
fi
# Save the bash preset (network off) as this throwaway home's profile.
runbox -n default bash -c true >/dev/null 2>&1

# 2. can read + write the project.
out="$(runbox bash -c 'echo written > out.txt && cat hello.txt')"
[ "$out" = "hi" ] && [ -f "$proj/out.txt" ] && ok "project readable + writable" || bad "project rw ($out)"

# 3. ~/.ssh invisible (create a real one in the throwaway HOME to be sure).
mkdir -p "$work/home/.ssh"; echo secret > "$work/home/.ssh/id"
out="$(runbox bash -c 'test -e ~/.ssh && echo LEAK || echo safe')"
[ "$out" = "safe" ] && ok "~/.ssh invisible" || bad "~/.ssh check ($out)"

# 4. /usr read-only.
out="$(runbox bash -c 'touch /usr/x 2>/dev/null && echo WROTE || echo ro')"
[ "$out" = "ro" ] && ok "/usr read-only" || bad "/usr writable"

# 5. environment filtered: an unlisted variable must not appear.
out="$(cd "$proj" && env -i "${env_base[@]}" SECRET_XYZ=leak "$BOX" --no-tui bash -c 'echo ${SECRET_XYZ:-gone}')"
[ "$out" = "gone" ] && ok "unlisted env var filtered" || bad "env leak ($out)"

# 6. exit code passthrough.
runbox bash -c 'exit 7'; [ $? -eq 7 ] && ok "exit code passthrough (7)" || bad "exit code"

# 7. signal -> 128+N.
runbox bash -c 'kill -TERM $$'; c=$?; [ $c -eq 143 ] && ok "signal exit (143)" || bad "signal exit ($c)"

# 8. /dev/tty present (no --new-session when the kernel blocks TIOCSTI).
out="$(runbox bash -c 'test -c /dev/tty && echo tty || echo notty')"
[ "$out" = "tty" ] && ok "/dev/tty available" || bad "/dev/tty ($out)"

# 9. network off by default (bash preset): no route out.
out="$(runbox bash -c 'timeout 3 bash -c "echo > /dev/tcp/1.1.1.1/53" 2>/dev/null && echo NET || echo nonet')"
[ "$out" = "nonet" ] && ok "network off by default" || bad "network reachable ($out)"

# 10. network on for one run with --net.
out="$(runbox --net bash -c 'timeout 4 bash -c "echo > /dev/tcp/1.1.1.1/53" 2>/dev/null && echo NET || echo nonet')"
[ "$out" = "NET" ] && ok "network reachable with --net" || bad "--net unreachable ($out) (fine if this host has no network)"

echo
echo "== $pass passed, $fail failed =="
[ $fail -eq 0 ]

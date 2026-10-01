# box v2 — per-domain network allowlist (design)

Draft (ported from the `feat/box-implementation` branch, with the proxy transport corrected in section 4) · builds on [implementation-plan.md](implementation-plan.md) (§8 "Later
(v2)", prior-art §2, and the S3 / network risk rows).

v1's network is all-or-nothing: `network = true` adds `--share-net`, giving the
program the whole WSL VM network — the LAN, the Windows host, and the VM's
abstract Unix sockets (S3). v2 replaces that with a **per-domain allowlist**: the
sandbox gets its own empty network namespace and its only way out is a single
Unix socket to a proxy box runs on the host, which permits exactly the domains
the profile names. This is the proxy half of Anthropic's sandbox-runtime, which
the plan says box can copy; box does **not** need their `AF_UNIX` seccomp filter,
because box mounts only what it names, so `/run/WSL` and friends are never
visible.

## 1. Threat it closes

With `--share-net`, a prompt-injected agent inside the box can reach any host on
the LAN, the Windows host's services (mirrored-mode localhost), and abstract
Unix sockets in the VM. v1's only mitigations are "turn network off" and, on a
6.18 kernel, a Landlock abstract-socket scope (S3). v2 makes "on" mean *only
these domains*, so the useful case (Kiro CLI reaching its API, `npm`/`uvx`
reaching a registry) works without opening the LAN.

## 2. Profile schema (implemented)

The schema, the editor and the dry run are on main. The proxy (sections 3 to 5)
is not, so box previews and saves a restricted profile but refuses to run it.

```toml
network = "restricted"    # "off" | "restricted" | "on"

[allow]
groups = ["docs-microsoft", "aws"]
hosts  = ["wiki.mycompany.com", "*.internal.example.com", "registry.example.com:8443"]
```

- `off` / `on` keep v1 semantics (`--unshare-all` alone / `+ --share-net`).
  The old `network = true/false` still loads, as `on`/`off`.
- `restricted` engages the proxy. It always allows the **program's own
  servers** (where it signs in and where its model runs), so restricting can't
  lock the program out; then the listed groups; then `hosts`.
- `groups` name lists that ship with box in
  [`internal/profile/netgroups.toml`](../internal/profile/netgroups.toml):
  official documentation (`docs-…`), services (`aws`, `azure-devops`,
  `azure-artifacts`) and each program's own servers. They are data, taken from
  the vendors' published firewall lists.
- `hosts` are the profile's custom entries (the editor's "Custom" row, which is
  there whatever else is ticked): a host name, `*.suffix`, or an IP address
  (IPv4 or IPv6), each optionally with `:port` (`[2001:db8::1]:443` for IPv6).
  A leading `*.` matches one or more leading labels and needs a suffix of two
  labels or more. Ranges such as `10.0.0.0/24` aren't supported. Default port
  set: 80, 443.
- This differs from the first draft's `[network]` table: TOML can't hold both
  `network = true` and `[network]`, and old profiles must keep loading.

## 3. Runtime shape

```
┌ host ─────────────────────────────────────────────────────────┐
│  box (parent, before exec)                                      │
│    1. opens a listening Unix socket at $XDG_RUNTIME/box/<id>.sock│
│    2. starts the egress proxy goroutine on it                   │
│    3. execs bwrap with --unshare-net and that socket bound in   │
│                                                                 │
│  egress proxy (in the box process, host network namespace)      │
│    - speaks SOCKS5 (or HTTP CONNECT) on the Unix socket         │
│    - on CONNECT host:port, checks host against the allowlist    │
│    - allowed  → dials the real host, splices bytes              │
│    - denied   → replies "connection refused", logs the attempt  │
└─────────────────────────────────────────────────────────────────┘
        │ only channel out of the box: the Unix socket
┌ sandbox (--unshare-net: empty netns, just lo) ──────────────────┐
│  program sees no default route; DNS + TCP go through            │
│  a bridge on 127.0.0.1:1080 inside the box forwards to the      │
│  socket; ALL_PROXY=socks5h://127.0.0.1:1080 (set by box)        │
└─────────────────────────────────────────────────────────────────┘
```

Key facts (from the plan):
- `--unshare-all` already includes `--unshare-net`; v2 simply does **not** add
  `--share-net`, and instead binds the proxy socket in and sets the proxy env.
- Because the netns is empty, the ONLY egress is the Unix socket — there is no
  route to the LAN, the Windows host, or VM abstract sockets. This is what makes
  the allowlist a real boundary rather than advice.
- box stays exec-and-vanish for the *program*, but v2 needs a **live parent**
  to run the proxy. So under `allowlist` box does NOT `syscall.Exec`; it forks
  bwrap as a child, runs the proxy, and waits — exiting with the child's status.
  The B3 init (`sandbox.init`) already exists on main; v2 turns it on for
  `allowlist` profiles, because it also runs the in-box bridge (section 4).

## 4. How the program is pointed at the proxy

Almost no client can use a SOCKS or HTTP proxy through a Unix-socket URL, so
the socket alone isn't enough (the branch draft assumed
`ALL_PROXY=socks5://unix:/…`, which curl, npm, pip and Go don't accept).
sandbox-runtime solves this with socat inside the sandbox; box can do it with
its own binary:

1. **Bridge**: box's in-sandbox init (the B3 `--box-init`, which v2 always
   uses) listens on `127.0.0.1:1080` (SOCKS5) and `127.0.0.1:3128` (HTTP
   CONNECT) inside the sandbox's own network namespace, and forwards each
   connection to the proxy socket bound at `/run/box/egress.sock`. No extra
   tool, and the loopback only exists inside the box.
2. **Env**: box sets `ALL_PROXY=socks5h://127.0.0.1:1080` and
   `HTTP_PROXY`/`HTTPS_PROXY=http://127.0.0.1:3128`, plus the lowercase forms.
   Go, curl, npm, pip and uv honour these.
3. **DNS**: `socks5h` and CONNECT send the host *name*, so the proxy resolves
   names host-side, the box needs no resolver, and `/etc/resolv.conf` can be
   dropped from the mount set in `allowlist` mode.

A client that ignores the proxy env simply fails to connect (empty netns), which
is fail-closed — the safe direction.

## 5. Allowlist matching

- Exact host, or `*.suffix` (one-or-more leading labels; `*.` never matches the
  bare suffix).
- Optional `:port`; absent means {80, 443}.
- Matching is on the **name in the CONNECT request**, before any DNS, so a
  program cannot bypass by pre-resolving to an IP: box refuses a CONNECT whose
  target is a literal IP unless the profile lists that IP explicitly.
- Every allow/deny is logged (host, port, verdict) to `~/.local/state/box/`
  for `box --doctor --net-log` review.

## 6. TUI

Implemented: Network is `off · restricted · on`. Choosing `restricted` opens an
"Allowed network" section: the program's own servers (ticked, can't be
unticked), a Documentation row with an All box, a Services row, and a Custom
row with "+ Add custom" for a domain or IP address (each entry checked: no `*`
alone, no `*.com`, a real address). A group
that also reaches other people's accounts says so in its help line.

## 7. Testing

Unit (no bwrap):
- allowlist matcher: exact, `*.`, port defaulting, IP-literal refusal.
- SOCKS5 CONNECT parse/allow/deny against a fake dialer.
- schema round-trip; `network=true` → `share` back-compat.

E2E (real bwrap, userns host):
- `allow=["example.com"]`: `curl https://example.com` works, `curl https://<other>`
  is refused, and the LAN / host gateway are unreachable.
- no `--share-net` in the arg list under `allowlist`.
- proxy dies with the box (no orphan on the host socket).

## 8. Open questions

- **SOCKS5 vs HTTP CONNECT** as the socket protocol. SOCKS5 covers non-HTTP TCP;
  HTTP CONNECT is simpler but proxy-env only. Lean SOCKS5.
- **UDP / QUIC**: SOCKS5 UDP-associate is fiddly; v2.0 can start TCP-only and
  document that HTTP/3 falls back to TCP for allowed hosts.
- **Latency**: splicing through a Unix socket + host dial adds a hop; measure
  against `--share-net` on the M6 programs.
- **Bridge ports** collide with a program that listens on 1080/3128 itself;
  pick them per run and pass them in the env if that turns out to matter.
- **Proxy as a subcommand** (`box --egress-proxy`) so it can be a separate
  audited binary, vs a goroutine in the parent. Goroutine is simpler and keeps
  "one binary".

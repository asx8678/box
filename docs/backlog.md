# box backlog, 1 October 2026

100 items from a full review by three independent reviewers plus my own checks. Items marked "reproduced" were re-run against the real code. Tags: effort S, M or L, then now, later or drop.

## Fix first: security and lost work
1. Dry run and Preview print the real values of passed variables such as API keys. Print `NAME="$NAME"`. Reproduced. S, now
2. `box -r`: a double-tapped `y` also answers the second question and deletes the private home. Reproduced. S, now
3. Git protection is bypassable in an ordinary repo through `.git/commondir`, the index, `HEAD` and `.git/modules`. Bind all of `.git` read-only by default. M, now
4. Git protection skips a folder with no `.git` yet, a `.git` file, subfolders and nested repos. Reproduced. M, now
5. A script in a writable folder can make box mount `~/.ssh` next run, through its shebang and `pyvenv.cfg`. M, now
6. `LD_PRELOAD` and `LD_LIBRARY_PATH` from a profile reach bubblewrap itself. Reproduced. M, now
7. Run as root, the program keeps all capabilities. Refuse uid 0 or drop them. S, now
8. `strict = true` likely makes bwrap refuse to start; its test passes on any failure. S, now
9. `box --box-init -- prog` runs the program with no sandbox. S, now
10. File descriptors box inherited pass into the sandbox. S, now
11. The planted-symlink check only knows this run's writable folders. M, later
12. PATH folders are protected only under the home folder. S, now
13. WSL rules match only `/mnt/<letter>` and protect only AppData. M, later
14. box's own config and data can be mounted read-only into a box. Reproduced. S, now
15. A folder holding a socket is accepted, only the socket itself is refused. S, later
16. The protected list misses `~/.bash_aliases`, `~/.aws`, `~/.kube` and others, and ignores XDG_CONFIG_HOME. S, now
17. Text typed in an add box is thrown away by clicking Save & run. Reproduced. S, now
18. Cancel can't be confirmed from the keyboard. Reproduced. S, now
19. A focused Cancel looks exactly like Save & run. Reproduced. S, now
20. Keys typed before the screen appears are obeyed: an early Enter accepts the preset and runs. S, now
21. The editor opens before the folder check, and `--dry-run` silently discards edits. S, now
22. A damaged `folders.toml` blocks every command, including `-l` and `-r`. Reproduced. S, now
23. `/dev/null` is taken for a terminal. Reproduced. S, now
24. Control characters in folder names are drawn raw in the editor. S, now

## The screen
25. Move Save & run up: put it in the header and let the footer follow the content. S, now
26. At 80x24 half the screen is chrome. One-line header and no spacer rows show 18 setting rows, not 6. S, now
27. A quick-start first screen: summary plus Change settings, Cancel, Save & run. M, later
28. Terminals under 50 columns are clipped; Save & run is cut off. M, later
29. Very short terminals show only the banner; say "terminal too small". S, later
30. State shown by colour alone: chosen option, focused button, button shape. Add markers. M, now
31. Help text is cut at two lines with no "…"; the Azure cautions are lost at 80 columns. S, now
32. The editor and `-l` hide tools, set variables, git protection and sandbox switches. M, now
33. Preview: add a plain summary, a copy key, and fix its keys on narrow screens. M, later
34. The banner cuts the folder path; wide characters and long words break rows. S, later
35. On a light terminal the text boxes keep dark colours. S, now
36. The ▾ on the access pill suggests a menu; unticked rows keep a coloured pill. S, later

## Editor behaviour
37. Up and Down move like Tab, not by row. `j`, `k`, PgUp and PgDn do nothing. Reproduced. M, now
38. Add folder: error on every partial name, Enter ignores the match, a paste collides with `~/`. Reproduced. S, now
39. Errors go stale, and paste skips the live check. Reproduced. S, now
40. Footer key hints are wrong while an add box is open. S, later
41. Enter in the name box saves and runs; Esc there leaves the editor. Reproduced. S, now
42. Save errors speak TOML and don't jump to the row; the same folder twice is accepted. M, later
43. Suggested folders start read-write. Reproduced. S, now
44. Wording batch: "a add", "+ Add custom", "enter press", "is blocked" under "not enforced". S, later
45. Profile-name errors don't state the rule. S, later
46. Ctrl+C discards changes without the guard Esc has. S, drop
47. After a resize the focus can be off-screen; a sideways wheel scrolls down. S, later
48. Clicking in a text box doesn't place the cursor; no undo after ✕. S, drop

## Picker, reset, launch
49. Picker: hovering changes what Run runs, and only the name is clickable. S, now
50. Picker doesn't scroll, and 1–9 can't reach a tenth profile. S, later
51. Picker says "Run" under `-e`; titles repeat "box ·". S, now
52. Picker can't edit or create a profile. M, later
53. Add a Save-only button, honest labels under dry run and restricted, and say "saved" on refusal. S, now
54. Launch: a key to skip the hold, a setting for its length, and a window title. S, later

## Command line
55. No way to delete one profile. S, now
56. A one-off `-p NAME` silently becomes the folder's default. S, now
57. Folder memory: never pruned, lost between two runs, `-l` doesn't mark the current folder or orphaned homes. M, later
58. Flags that don't apply are ignored; `--no-tui` doesn't stop the reset screen. S, now
59. `-n NAME` copies `default` in the editor but the preset with `--no-tui`. S, later
60. Misleading messages: empty program name, `./tool.sh` hint, `/tmp`, a dangling protected symlink. Reproduced. S, now
61. `--doctor` prints invented facts when the probe stops early. S, now
62. An invalid or symlinked profile can't be opened with `-e` or seen in `-l`. M, later
63. `env.set` written as a string is silently ignored; a missing `version` is accepted. Reproduced. S, now
64. False refusals: project equal to a profile folder, a tool in home, symlinked folders. M, later
65. Program lookup mounts the whole parent folder, such as `~/Downloads`. S, later

## Profiles and data
66. Saved profiles have empty tables and no comments; preset notes never reach the user. M, later
67. Version policy: bump on format change and say "written by a newer box". S, now
68. Kiro presets pass AWS_PROFILE with no `~/.aws`. S, now
69. Git inside the box has no identity and can't set upstream. Suggest `~/.gitconfig` read-only and document it. S, now
70. Offer credential folders read-only for ticked services; never read-write. M, later
71. Claude Code preset; a missing file path is created as a folder. M, later
72. Hide files inside the project, such as `.env`. M, later
73. Programs with files beside `bin/` only get `bin`. M, later
74. Team-wide presets and network groups from a shared file. M, later
75. Remove the no-op shell presets; offer proxy variables; edit set variables. S, later

## Network
76. Build the proxy, so "restricted" is enforced. L, now
77. Decide whether to hide "restricted" until then. S, now
78. The proxy must refuse names that resolve to private addresses. With 76
79. Blocked-hosts log with one-click allow. M, with 76
80. Groups: package registries, GitHub, `cli.kiro.dev`, `*.api.aws`, sources for docs groups. S, now
81. Host check accepts hex forms and `*.co.uk`; the AWS group also matches any rented server. S, later
82. Address ranges, UDP, git over SSH. M, later
83. Show a group's full host list; keep the data fresh. S, later

## Hardening
84. A general seccomp denylist. M, later
85. Other project files run code outside, such as husky and editor tasks. Document; offer read-only project plus output folder. S, now
86. The init mishandles the terminal in several cases; decide B3. M, later
87. The program shares box's process group; two boxes on one profile race. M, later
88. Host details are visible inside; no resource limits. M, later
89. Open decisions S3 and D3. S, later

## Tests, docs, release
90. Run the e2e suite and runbook on real WSL2. M, now
91. CI never loads the seccomp filter into a kernel. S, now
92. Push so CI runs the new Linux tests. S, now
93. Profile selection has no tests; e2e never covers creation, reset or memory. M, now
94. CI hygiene: pin actions, vulnerability scan, more distros, screen snapshots. M, later
95. README: stale status, claims broader than the code, missing first-day steps. S, now
96. Mark the design docs as history. S, later
97. Tidy: redundant check script, one receiver name. S, later
98. LICENSE and SECURITY.md. S, now
99. A tagged release with binaries and checksums, Go 1.26, uninstall. M, now
100. An audit log of runs for teams. M, later

## Plan for doing all 100 (saved 2026-10-01, not started)

Work in batches, commit after each, full tests in between:
1. Items 1–24: security and lost-work fixes.
2. Items 25–48: screen and editor.
3. Items 49–75: picker, command line, profiles.
4. Items 76–83: the proxy that enforces "restricted", then the groups.
5. Items 84–100: hardening, tests, docs, release.

Needs a real WSL2 machine, can't be done from macOS: 90 and the decisions in 89.

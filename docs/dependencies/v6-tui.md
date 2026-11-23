# v6 TUI presentation dependencies

Inventory of the six presentation modules added for v6, their versions, existing versions in `go.mod`, and their transitive dependencies. Obtained from `go get <module>@<version>` for each module in a scratch copy of go.mod, followed by `go.mod` diff to identify additions and upgrades (2026-11-23).

## Direct modules

### github.com/charmbracelet/lipgloss

- Module path: `github.com/charmbracelet/lipgloss`
- Version to pin: `v1.1.0`
- Current version in `go.mod`: already present at `v1.1.0`
- License: MIT
- Transitive additions/upgrades: None (all transitive modules already in `go.mod`)

### github.com/charmbracelet/bubbles

- Module path: `github.com/charmbracelet/bubbles`
- Version to pin: `v1.0.0`
- Current version in `go.mod`: already present at `v1.0.0`
- License: MIT
- Transitive additions/upgrades: None (all transitive modules already in `go.mod`)

### github.com/charmbracelet/glamour

- Module path: `github.com/charmbracelet/glamour`
- Version to pin: `v1.0.0`
- Current version in `go.mod`: not present (NEW)
- License: MIT
- Transitive modules added/upgraded:
  - `github.com/charmbracelet/lipgloss@v1.1.1-0.20250404203927-76690c660834` (MIT, upgrade from v1.1.0)
  - `github.com/alecthomas/chroma/v2@v2.20.0` (MIT, new)
  - `github.com/aymerick/douceur@v0.2.0` (MIT, new)
  - `github.com/charmbracelet/x/exp/slice@v0.0.0-20250327172914-2fdc97757edf` (MIT, new)
  - `github.com/dlclark/regexp2@v1.11.5` (MIT, new)
  - `github.com/gorilla/css@v1.0.1` (BSD-3-Clause, new)
  - `github.com/microcosm-cc/bluemonday@v1.0.27` (BSD-3-Clause, new)
  - `github.com/muesli/reflow@v0.3.0` (MIT, new)
  - `github.com/yuin/goldmark@v1.7.13` (MIT, upgrade from v1.4.13)
  - `github.com/yuin/goldmark-emoji@v1.0.6` (MIT, new)
  - `golang.org/x/mod@v0.28.0` (BSD-3-Clause, upgrade from v0.26.0)
  - `golang.org/x/net@v0.44.0` (BSD-3-Clause, upgrade from v0.42.0)
  - `golang.org/x/sync@v0.17.0` (BSD-3-Clause, upgrade from v0.16.0)
  - `golang.org/x/term@v0.36.0` (BSD-3-Clause, new)
  - `golang.org/x/text@v0.30.0` (BSD-3-Clause, upgrade from v0.28.0)
  - `golang.org/x/tools@v0.37.0` (BSD-3-Clause, upgrade from v0.35.0)

### github.com/charmbracelet/huh

- Module path: `github.com/charmbracelet/huh`
- Version to pin: `v1.0.0`
- Current version in `go.mod`: not present (NEW)
- License: MIT
- Transitive modules added:
  - `github.com/catppuccin/go@v0.3.0` (MIT, new)
  - `github.com/charmbracelet/x/exp/strings@v0.0.0-20240722160745-212f7b056ed0` (MIT, new)
  - `github.com/creack/pty@v1.1.24` (MIT, new)
  - `github.com/charmbracelet/x/conpty@v0.1.0` (MIT, new)
  - `github.com/charmbracelet/x/termios@v0.1.1` (MIT, new)
  - `github.com/charmbracelet/x/xpty@v0.1.2` (MIT, new)
  - `github.com/mitchellh/hashstructure/v2@v2.0.2` (MIT, new)

### github.com/charmbracelet/harmonica

- Module path: `github.com/charmbracelet/harmonica`
- Version to pin: `v0.2.0`
- Current version in `go.mod`: not present (NEW)
- License: MIT
- Transitive modules: None (all dependencies already present)

### github.com/lrstanley/bubblezone

- Module path: `github.com/lrstanley/bubblezone`
- Version to pin: `v1.0.0`
- Current version in `go.mod`: not present (NEW)
- License: MIT
- Transitive modules: None (all dependencies already in `go.mod`)

## Compatibility with existing versions

**Existing Bubble Tea, Bubbles and Lip Gloss:**
- Bubble Tea: currently `v1.3.10`, required by existing tests. Glamour requires `v1.3.10` (compatible). Huh requires `v1.3.6` (compatible; `v1.3.10` is later). No version conflict.
- Bubbles: currently `v1.0.0`. Both Glamour and Huh depend on Bubbles. Under Go's minimal version selection (MVS), the highest required version is selected. MVS keeps `v1.0.0` as the pinned version. No conflict.
- Lip Gloss: currently `v1.1.0`. Glamour requires `v1.1.1-0.20250404203927-76690c660834` (a pre-release). Huh is compatible with `v1.1.0`. Under MVS, the pre-release version `v1.1.1-0.20250404203927-76690c660834` is selected as it is higher than v1.1.0. This is an upgrade within the v1.1 series and poses no major version conflict.

## Operator decision

The operator accepted all six modules, and the transitive modules listed here, on 2026-11-22, before this inventory was written. The next task adds them without a further decision. Anything in this inventory the operator may want to revisit is listed under Notes.

## Notes

**Process and system-level dependencies:**
- Huh brings in PTY and process control libraries: `github.com/creack/pty@v1.1.24`, `github.com/charmbracelet/x/conpty@v0.1.0`, `github.com/charmbracelet/x/termios@v0.1.1`, and `github.com/charmbracelet/x/xpty@v0.1.2`. These provide access to terminal pseudo-devices and process spawning on the host, adding system-level dependencies on PTY device access and process control APIs.

**Pre-release and standard library upgrades:**
- Glamour requires `github.com/charmbracelet/lipgloss@v1.1.1-0.20250404203927-76690c660834`, a pre-release version, which is an upgrade from the currently pinned `v1.1.0`.
- Glamour brings multiple upgrades to standard library modules: `golang.org/x/mod@v0.28.0`, `golang.org/x/net@v0.44.0`, `golang.org/x/sync@v0.17.0`, `golang.org/x/term@v0.36.0`, `golang.org/x/text@v0.30.0`, and `golang.org/x/tools@v0.37.0`. These are compatible upgrades standard for TUI applications.

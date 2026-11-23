# v6 TUI presentation dependencies

Inventory of the six presentation modules added for v6, their versions, existing versions in `go.mod`, and their transitive dependencies. Obtained from `go get` for each module in a scratch copy of the module, followed by `go.mod` diff to identify additions and upgrades (2026-11-23).

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
  - `github.com/alecthomas/chroma/v2@v2.20.0` (MIT)
  - `github.com/aymerick/douceur@v0.2.0` (MIT)
  - `github.com/dlclark/regexp2@v1.11.0` (MIT)
  - `github.com/gorilla/css@v1.0.1` (BSD-3-Clause)
  - `github.com/microcosm-cc/bluemonday@v1.0.27` (BSD-3-Clause)
  - `github.com/muesli/reflow@v0.3.0` (MIT)
  - `github.com/yuin/goldmark@v1.7.13` (MIT)
  - `github.com/yuin/goldmark-emoji@v1.0.6` (MIT)
  - `golang.org/x/net@v0.38.0` (BSD-3-Clause, upgrade)
  - `golang.org/x/term@v0.36.0` (BSD-3-Clause, upgrade)

### github.com/charmbracelet/huh

- Module path: `github.com/charmbracelet/huh`
- Version to pin: `v1.0.0`
- Current version in `go.mod`: not present (NEW)
- License: MIT
- Transitive modules added:
  - `github.com/catppuccin/go@v0.3.0` (MIT)
  - `github.com/charmbracelet/x/conpty@v0.1.0` (MIT)
  - `github.com/charmbracelet/x/errors@v0.0.0-20240508181413-e8d8b6e2de86` (MIT)
  - `github.com/charmbracelet/x/termios@v0.1.1` (MIT)
  - `github.com/charmbracelet/x/xpty@v0.1.2` (MIT)
  - `github.com/charmbracelet/x/exp/slice@v0.0.0-...` (MIT)
  - `github.com/charmbracelet/x/exp/strings@v0.0.0-20240722160745-212f7b056ed0` (MIT)
  - `github.com/creack/pty@v1.1.24` (MIT)
  - `github.com/mitchellh/hashstructure/v2@v2.0.2` (MIT)
  - `golang.org/x/exp@v0.0.0-20231006140011-7918f672742d` (BSD-3-Clause)
  - `golang.org/x/mod@v0.21.0` (BSD-3-Clause)
  - `golang.org/x/sync@v0.11.0` (BSD-3-Clause)
  - `golang.org/x/sys@v0.28.0` (BSD-3-Clause, upgrade)
  - `golang.org/x/text@v0.21.0` (BSD-3-Clause)
  - `golang.org/x/tools@v0.26.0` (BSD-3-Clause)

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
- Bubbles: currently `v1.0.0`. Glamour's transitive dependency chain via lipgloss and other modules maintains `v1.0.0` as the highest required version. Under Go's minimal version selection (MVS), `v1.0.0` is kept. No conflict.
- Lip Gloss: currently `v1.1.0`. Glamour requires `v1.1.1-0.20250404203927-76690c660834` as a transitive dependency. Huh is compatible with `v1.1.0`. Glamour's pre-release version is an upgrade within the v1.1 series and poses no major version conflict.

## Operator decision

The operator accepted all six modules, and the transitive modules listed here, on 2026-11-22, before this inventory was written. The next task adds them without a further decision. Anything in this inventory the operator may want to revisit is listed under Notes.

## Notes

**Process and system dependencies:**
- Huh brings in `github.com/creack/pty` and `github.com/charmbracelet/x/conpty`, `github.com/charmbracelet/x/xpty`, which are PTY and process-spawning libraries for terminal control. These add system-level dependencies on PTY device access.

**Standard library upgrades:**
- Glamour requires `github.com/charmbracelet/lipgloss@v1.1.1-0.20250404203927-76690c660834`, a pre-release version of lipgloss, which is a minor pre-release upgrade from the currently pinned `v1.1.0`.
- Multiple transitive dependencies require newer versions of `golang.org/x/mod`, `golang.org/x/net`, `golang.org/x/sync`, `golang.org/x/sys`, `golang.org/x/text`, and `golang.org/x/tools`. These are compatible upgrades standard for TUI applications.

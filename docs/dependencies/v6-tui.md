# v6 TUI presentation dependencies

Inventory of the six presentation modules added for v6, their versions, existing versions in `go.mod`, and their transitive dependencies. Obtained from `go get <module>@<version>` for each module in a scratch copy of go.mod, followed by a `go.mod` diff to identify additions and upgrades, and `go mod graph` (and `go list -m all`) in the same scratch copy to identify modules that appear only as graph requirements and are pruned from `go.mod` (2026-11-23).

## Direct modules

### github.com/charmbracelet/lipgloss

- Module path: `github.com/charmbracelet/lipgloss`
- Version to pin: `v1.1.1-0.20250404203927-76690c660834`
- Current version in `go.mod`: present at `v1.1.0` (upgraded by Glamour)
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
- Transitive modules added/upgraded (from go.mod diff):
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
- Graph-only modules (transitive, not in go.mod):
  - `github.com/alecthomas/assert/v2@v2.11.0` (MIT, via chroma)
  - `github.com/alecthomas/repr@v0.5.1` (MIT, via chroma)
  - `github.com/hexops/gotextdiff@v1.0.3` (BSD-3-Clause, via chroma)
  - `golang.org/x/crypto@v0.42.0` (BSD-3-Clause, via x/net)
  - `golang.org/x/telemetry@v0.0.0-20250908211612-aef8a434d053` (BSD-3-Clause, via x/tools)

### github.com/charmbracelet/huh

- Module path: `github.com/charmbracelet/huh`
- Version to pin: `v1.0.0`
- Current version in `go.mod`: not present (NEW)
- License: MIT
- Transitive modules added (from go.mod diff):
  - `github.com/catppuccin/go@v0.3.0` (MIT, new)
  - `github.com/charmbracelet/x/exp/strings@v0.0.0-20240722160745-212f7b056ed0` (MIT, new)
  - `github.com/mitchellh/hashstructure/v2@v2.0.2` (MIT, new)
- Graph-only modules (transitive, not in go.mod):
  - `github.com/charmbracelet/x/errors@v0.0.0-20240508181413-e8d8b6e2de86` (MIT, via huh)
  - `github.com/creack/pty@v1.1.24` (MIT, via huh)
  - `github.com/charmbracelet/x/conpty@v0.1.0` (MIT, via huh)
  - `github.com/charmbracelet/x/termios@v0.1.1` (MIT, via huh)
  - `github.com/charmbracelet/x/xpty@v0.1.2` (MIT, via huh)

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
- Bubble Tea: currently `v1.3.10`, required by existing tests. Huh requires `v1.3.6` (compatible; `v1.3.10` is later). bubblezone requires `v1.3.4` (compatible). Glamour has no direct Bubble Tea requirement in its go.mod. MVS selects `v1.3.10`. No conflict.
- Bubbles: currently `v1.0.0`. Huh requires Bubbles; bubblezone does not. Under Go's minimal version selection (MVS), the highest required version is selected. MVS keeps `v1.0.0` as the pinned version. No conflict.
- Lip Gloss: currently `v1.1.0`. Glamour requires `v1.1.1-0.20250404203927-76690c660834` (a pre-release via go.mod). Huh and bubblezone both directly require Lip Gloss `v1.1.0`. Under MVS, `v1.1.1-0.20250404203927-76690c660834` is selected as it is higher than `v1.1.0`. This upgrade is within the v1.1 pre-release series and poses no major version conflict.

## Operator decision

The operator accepted all six modules, and the transitive modules listed here, on 2026-11-22, before this inventory was written. The next task adds them without a further decision. Anything in this inventory the operator may want to revisit is listed under Notes.

## Notes

**Archived module:**
- `github.com/mitchellh/hashstructure/v2@v2.0.2`, added by Huh, comes from an upstream repository (`github.com/mitchellh/hashstructure`) that its owner has archived. It is MIT licensed and has no further releases expected.

**Graph-only module dependencies (not in go.mod):**
- Huh depends on PTY and process control modules via go mod graph: `github.com/creack/pty`, `github.com/charmbracelet/x/conpty`, `github.com/charmbracelet/x/termios`, and `github.com/charmbracelet/x/xpty`. These are pruned from go.mod under Go's minimal version selection but are still requirements in the module graph. They are not added to `go.mod` and are not built into syndic, so they add no runtime capability.
- `github.com/alecthomas/assert/v2`, `github.com/alecthomas/repr` and `github.com/hexops/gotextdiff` are test-only requirements of chroma. They appear in the module graph, are pruned from go.mod, and are not built into the application.

**Pre-release version and `golang.org/x` upgrades:**
- Glamour requires `github.com/charmbracelet/lipgloss@v1.1.1-0.20250404203927-76690c660834`, a pre-release version, which is an upgrade from the currently pinned `v1.1.0`.
- Glamour upgrades `golang.org/x/mod` (`v0.28.0`), `golang.org/x/net` (`v0.44.0`), `golang.org/x/sync` (`v0.17.0`), `golang.org/x/text` (`v0.30.0`) and `golang.org/x/tools` (`v0.37.0`), and adds `golang.org/x/term@v0.36.0`. These are `golang.org/x` modules, not the standard library.
- Glamour and x/tools bring in updates to `golang.org/x/crypto` and `golang.org/x/telemetry` via the module graph.

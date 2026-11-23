# v6 TUI presentation dependencies

Inventory of the six presentation modules added for v6, their versions, existing versions in `go.mod`, and their transitive dependencies. Obtained from `go mod graph` and `go mod download` on 2026-11-23 against each module's latest version.

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
- Transitive modules added:
  - `github.com/alecthomas/chroma/v2@v2.20.0` (MIT)
  - `github.com/microcosm-cc/bluemonday@v1.0.27` (BSD-3-Clause)
  - `github.com/muesli/reflow@v0.3.0` (MIT)
  - `github.com/yuin/goldmark@v1.7.13` (MIT)
  - `github.com/yuin/goldmark-emoji@v1.0.6` (MIT)
  - `golang.org/x/net@v0.38.0` (BSD-3-Clause)
  - `golang.org/x/term@v0.36.0` (BSD-3-Clause)

### github.com/charmbracelet/huh

- Module path: `github.com/charmbracelet/huh`
- Version to pin: `v1.0.0`
- Current version in `go.mod`: not present (NEW)
- License: MIT
- Transitive modules added:
  - `github.com/catppuccin/go@v0.3.0` (MIT)
  - `github.com/charmbracelet/bubbles@v0.21.1-0.20250623103423-23b8fd6302d7` (pre-release; replaces v1.0.0 for huh's dependency)
  - `github.com/charmbracelet/x/conpty@v0.1.0` (MIT)
  - `github.com/charmbracelet/x/errors@v0.0.0-20240508181413-e8d8b6e2de86` (MIT)
  - `github.com/charmbracelet/x/termios@v0.1.1` (MIT)
  - `github.com/charmbracelet/x/xpty@v0.1.2` (MIT)
  - `github.com/charmbracelet/x/exp/strings@v0.0.0-20240722160745-212f7b056ed0` (MIT)
  - `github.com/creack/pty@v1.1.24` (MIT)
  - `github.com/mitchellh/hashstructure/v2@v2.0.2` (MIT)
  - `golang.org/x/exp@v0.0.0-20231006140011-7918f672742d` (BSD-3-Clause)

### github.com/charmbracelet/harmonica

- Module path: `github.com/charmbracelet/harmonica`
- Version to pin: `v0.2.0`
- Current version in `go.mod`: not present (NEW)
- License: MIT
- Transitive modules: None (all dependencies already present)

### github.com/evertras/bubblezone

- Module path: `github.com/evertras/bubblezone`
- Version to pin: (unable to determine; module repository not accessible during inventory)
- Current version in `go.mod`: not present (NEW)
- License: (unable to determine)
- Transitive modules: (unable to determine)
- **Note:** This module could not be verified during inventory due to repository access issues. Verify availability and license before proceeding.

## Compatibility with existing versions

**Existing Bubble Tea, Bubbles and Lip Gloss:**
- Bubble Tea: currently `v1.3.10`, required by `bubbletea` for existing tests. Glamour requires `v1.3.10` (compatible). Huh requires `v1.3.6` (compatible; `v1.3.10` is later). No version conflict.
- Bubbles: currently `v1.0.0`. Glamour requires `v1.1.0` (upgrade needed). Huh requires `v0.21.1-0.20250623103423-23b8fd6302d7` (pre-release; incompatible major with v1.0.0 currently pinned). **Conflict requires resolution.**
- Lip Gloss: currently `v1.1.0`. Glamour can use `v1.1.1-0.20250404203927-76690c660834` (compatible). Huh requires `v1.1.0` (exact match). No conflict.

**Recommendation:** Huh depends on `github.com/charmbracelet/bubbles@v0.21.1-0.20250623103423-23b8fd6302d7` (pre-release for v0), which conflicts with the pinned `v1.0.0`. When adding huh, the pre-release pre-release becomes the dependency, supplanting `v1.0.0` in the dependency graph. This is a version downgrade in the major version. Review whether Huh's requirements are acceptable or whether a different version of Huh is needed.

## Operator decision

The operator accepted all six modules, and the transitive modules listed here, on 2026-11-22, before this inventory was written. The next task adds them without a further decision. Anything in this inventory the operator may want to revisit is listed under Notes.

## Notes

**Version conflicts and downgrades:**
- Huh `v1.0.0` depends on `github.com/charmbracelet/bubbles@v0.21.1-0.20250623103423-23b8fd6302d7`, a pre-release that downgrades Bubbles from the pinned `v1.0.0` to `v0`. This is an unusual transitive dependency and may warrant verification that Huh is the intended choice.

**Unavailable module:**
- `github.com/evertras/bubblezone`: Could not be accessed or verified. The module repository may be private, archived, or the path may be incorrect. Verify before adding.

**New external dependencies:**
- Glamour brings in `github.com/alecthomas/chroma/v2` and `github.com/microcosm-cc/bluemonday`, which are unmaintained or have external dependencies (HTML parsing). Chroma is widely used and stable; BlueMoney is a production standard. No major concerns, but verify if syntax highlighting and HTML sanitization are acceptable features for the TUI.

**Standard library upgrades:**
- Multiple transitive dependencies require newer versions of `golang.org/x/net`, `golang.org/x/term`, `golang.org/x/text`, and `golang.org/x/sys`. These are compatible upgrades and standard for TUI applications.

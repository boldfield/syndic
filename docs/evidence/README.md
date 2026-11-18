# Evidence

Redacted endpoint probe exports produced by the `syndic probe` command. Each file records what one endpoint and model were observed to support, under the redaction rule in `internal/evidence/RULE.md`. Raw probe records, which may contain model inventories, headers and infrastructure detail, are written outside the repository and are never committed.

## Running a probe

To generate a probe export for a new endpoint, use:

```bash
syndic probe --endpoint my-endpoint --model gpt-4o
```

The command requires a roster file (default `./syndic.toml` or `$XDG_CONFIG_HOME/syndic/syndic.toml`), writes the redacted export to `./docs/evidence/`, and stores the raw record under `$XDG_DATA_HOME/syndic/evidence-raw`. The probe result is recorded in the journal database (default `$XDG_DATA_HOME/syndic/journal.sqlite`).

Flags:
- `--roster PATH`: path to the roster TOML file
- `--endpoint NAME`: endpoint profile name (required)
- `--model NAME`: model name (required)
- `--raw-dir PATH`: directory for raw records
- `--export-dir PATH`: directory for redacted exports
- `--journal PATH`: path to journal database
- `--secrets-dir PATH`: directory for file secret references
- `--context-tokens N`: assumed context length for over-length test
- `--skip-overlength`: do not run the over-length test

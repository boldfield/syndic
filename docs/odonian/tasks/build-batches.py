#!/usr/bin/env python3
"""Assemble Odonian task batch JSON from the A-NN-*.md task files.

Usage: python3 build-batches.py [--document-id UUID] [--uuid A-01=UUID ...]

Reads front matter (key, title, model, review_models, escalate, agent_merge,
track, depends_on) and uses the body below the front matter as `spec`,
verbatim. Writes one batch-N.json per entry in BATCHES next to this script.

Cross-batch depends_on entries become FILL_UUID_<key> placeholders unless a
--uuid mapping is supplied. document_id is a placeholder unless --document-id
is supplied. Re-run after each batch is posted, passing the UUIDs Odonian
returned, so the next batch file is complete.
"""
import argparse, json, pathlib, re, sys

HERE = pathlib.Path(__file__).parent
BATCHES = [
    ["A-01", "A-02"],
    ["A-03a", "A-04a"],
    ["A-03b", "A-04b"],
    ["A-03c", "A-05", "A-06"],
    ["A-07a", "A-07b"],
    ["A-08"],
]


def parse(path):
    text = path.read_text(encoding="utf-8")
    m = re.match(r"^---\n(.*?)\n---\n(.*)$", text, re.S)
    if not m:
        sys.exit(f"{path}: missing front matter")
    fm, body = m.group(1), m.group(2).lstrip("\n")
    meta = {}
    for line in fm.splitlines():
        k, _, v = line.partition(":")
        v = v.strip()
        if v.startswith("["):
            v = [x.strip().strip('"') for x in v.strip("[]").split(",") if x.strip()]
        elif v in ("true", "false"):
            v = v == "true"
        else:
            v = v.strip('"')
        meta[k.strip()] = v
    return meta, body


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("--document-id", default="FILL_DOCUMENT_UUID")
    ap.add_argument("--uuid", action="append", default=[], help="KEY=UUID for tasks already created")
    args = ap.parse_args()
    known = dict(u.split("=", 1) for u in args.uuid)

    tasks = {}
    for p in sorted(HERE.glob("A-*.md")):
        meta, body = parse(p)
        tasks[meta["key"]] = (meta, body)

    for i, keys in enumerate(BATCHES, 1):
        batch = []
        for k in keys:
            meta, body = tasks[k]
            deps = []
            for d in meta.get("depends_on", []):
                if d in keys:
                    deps.append(d)  # batch-local key
                else:
                    deps.append(known.get(d, f"FILL_UUID_{d}"))
            batch.append({
                "key": k,
                "title": meta["title"],
                "spec": body,
                "document_id": args.document_id,
                "model": meta.get("model", "haiku"),
                "review_models": meta.get("review_models", ["opus"]),
                "escalate": meta.get("escalate", True),
                "agent_merge": meta.get("agent_merge", False),
                "track": meta.get("track", "build"),
                "depends_on": deps,
            })
        out = HERE / f"batch-{i}.json"
        out.write_text(json.dumps(batch, indent=2, ensure_ascii=False) + "\n", encoding="utf-8")
        print(f"wrote {out.name}: {', '.join(keys)}")


if __name__ == "__main__":
    main()

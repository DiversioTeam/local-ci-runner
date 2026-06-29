# Docs index

Use this directory as the durable harness layer for `local-ci-runner`.

## Start here

1. [`../AGENTS.md`](../AGENTS.md) — short repo map, commands, and rules
2. [`../README.md`](../README.md) — human-first overview
3. [`contracts.md`](./contracts.md) — wire contracts and artifact schema
4. [`architecture.md`](./architecture.md) — engine/write/read path model
5. [`inspection.md`](./inspection.md) — operator debugging and read-back model
6. [`quality/gates.md`](./quality/gates.md) — local checks and release CI
7. [`runbooks/development.md`](./runbooks/development.md) — everyday dev loop
8. [`../cmd/local-ci/MANUAL.md`](../cmd/local-ci/MANUAL.md) — long-form CLI manual

## Mental model

```text
README.md   -> what the project is
AGENTS.md   -> where truth lives and how to work safely
docs/*      -> durable topic detail
MANUAL.md   -> full binary/operator surface
```

## Topics

- `contracts.md` — `.local-ci.toml`, planner stdout, run artifacts, events, GitHub posting
- `architecture.md` — engine boundaries, persisted state, inspection fallback, resume safety
- `inspection.md` — `runs`, `show`, `logs`, `publish` from first principles
- `quality/gates.md` — required commands, release workflow, common failure modes
- `runbooks/development.md` — daily contributor loop, examples, release helper pointers

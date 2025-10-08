# README Integration Tests

The internal README manager ships with an offline integration harness that exercises the most important scenarios end-to-end: repository bootstrapping, change detection, reuse paths, tagging, and state persistence.

## Prerequisites
- Go toolchain available in `PATH`
- `git` installed
- Fixture repository `tests/repositories/undici` present (run `git submodule update --init --recursive` once)

## Running
From the repository root:

```bash
mct/tests/run-readme-integration.sh
```

Set `KEEP_README_TEST_TMP=true` to keep the temporary worktree under `mct/tests/tmp/` for inspection after the run.

## Unit Tests (mct module)

For changes under `mct/` that don’t require the full integration suite, run the standard Go test sweep from the module directory:

```bash
cd mct
go test ./...
```

To avoid sandbox permission issues in constrained environments, you can direct the Go build cache into the repository tree:

```bash
cd mct
GOCACHE=$(pwd)/.gocache go test ./...
```

Remember to remove the temporary cache afterwards if you use that pattern:

```bash
rm -rf mct/.gocache
```

## What the Script Does
1. **Builds a fresh CLI binary** into an ephemeral directory and exports the LLM/README stub environment variables so no external services are needed.
2. **Clones the undici fixture** into a clean working copy, wipes any prior `.machtiani` state, and points `$HOME` to an isolated location so the README sidecar repo starts from scratch.
3. **Executes five sequential scenarios**, each invoking `mct prompt --mode=answer-only` (which triggers README management before returning):
   - `initial-generation`: first commit, expect a new `.machtiani/artifacts/readme` repo with a commit and `oid-<commit>` tag.
   - `significant-change`: second commit with code changes, expect regeneration, fresh commit, updated tag, and README under 600 words.
   - `repeated-commit`: rerun on the same commit, expect no new commit, only tag reuse.
   - `docs-only`: temporary branch with only documentation edits, expect skip + retag.
   - `latest-progress`: newest commit, expect another regenerated README and tag.
4. **Validates invariants after each scenario**:
   - `.state/last_project_commit` matches the head commit processed.
   - README commit counts move only when regeneration is expected.
   - Content equality/inequality matches the expectation, and generated README respects the 600-word limit.
   - Tags `oid-<project_commit>` exist and point at the recorded README commit.
5. **Confirms historic tags remain resolvable** at the end of the run.

Use this harness whenever you touch `internal/readme`, the CLI wiring that invokes it, or the stubbed LLM pathways.

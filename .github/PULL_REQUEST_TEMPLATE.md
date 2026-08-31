<!--
Thanks for sending a PR. A few notes before you submit:

- For non-trivial changes, please open an issue first (see CONTRIBUTING.md → Pull Request Process).
- Keep one logical change per PR. If you're tempted to write "and also …" in the description, that's a sign to split.
- Security fixes go through the private channel in SECURITY.md, not a public PR.
-->

## Summary

<!-- What does this PR change, and why? Link the issue (`Fixes #123` or `Refs #123`). -->

## Type of change

<!-- Check one or more. -->

- [ ] Bug fix (regression test included)
- [ ] New feature
- [ ] Behavioural change to an existing feature
- [ ] Refactor / internal cleanup (no behaviour change)
- [ ] Documentation only
- [ ] Build / CI / tooling
- [ ] Other (describe below)

## Spec / requirements alignment

<!--
Cite the spec or requirement IDs this PR is anchored to. Examples:
- Implements REQ-IDEM-04 (release in-flight key on transient errors)
- Refines BR-MET-08 (un-prefixed registerer for community-portable metrics)
- Closes a gap in api/opentams-api-v1.yaml — schema X said …, code did …
- Doesn't touch a documented requirement (refactor / internal-only).
-->

## Tests

<!--
Every behavioural change needs tests (CONTRIBUTING.md → Tests Required for Behavioural Changes).
Describe what you added or extended, and how to run them.
-->

- [ ] Added / updated unit tests under `pkg/` or `internal/`
- [ ] Added / updated integration tests (`-tags=integration`)
- [ ] Manual testing only (explain why automated coverage isn't feasible)
- [ ] N/A (docs / refactor with no behaviour change)

```bash
# CI runs the same gate automatically on every PR. To replicate locally:
make ci   # build + vet + lint + race-checked tests

# Or the underlying commands (matches CONTRIBUTING.md):
go build ./...
go vet ./...
go test -race -count=1 ./pkg/... ./internal/... ./cmd/...
```

## Documentation

- [ ] Updated package godoc for any changed public API
- [ ] Updated `README.md` for user-visible behaviour or configuration
- [ ] Updated `docs/requirements.md` for changed requirements / acceptance criteria
- [ ] Updated functional-design / business-rules docs under `docs/design/` if applicable
- [ ] N/A

## Backwards compatibility

<!--
- Any breaking changes to public packages, HTTP responses, configuration, or schema?
- If yes, what's the migration path? (Before the first stable release we don't owe deprecation aliases, but we do owe a clear migration note.)
- If no, say "no breaking changes" — that's the answer reviewers most often want.
-->

## Checklist

- [ ] PR title follows Conventional Commits (e.g. `fix(idempotency): release in-flight on transient errors`)
- [ ] `make ci` passes locally (build + vet + lint + race-checked tests) — same gate CI applies
- [ ] No new lint findings introduced by this change (the existing main has a known legacy lint backlog being cleaned up separately)
- [ ] Self-review done — there are no leftover `// TODO`, debug `fmt.Println`, or commented-out blocks I'd want a reviewer to catch
- [ ] I agree to license this contribution under the project's [Apache 2.0](../LICENSE) terms (CONTRIBUTING.md → Licensing)

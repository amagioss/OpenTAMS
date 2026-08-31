---
status: "accepted"
date: 2026-08-31
decision-makers: OpenTAMS maintainers
consulted: OpenTAMS maintainers
informed: OpenTAMS contributors
---

# Run CI on GitHub-hosted runners, with every action pinned by commit SHA

## Context and Problem Statement

OpenTAMS was built inside one company, and its CI ran on that company's self-hosted
runners. As a public project, that arrangement fails in two ways.

An external contributor's pull request cannot run on runners they cannot reach, so their
first experience is a queue that never drains. And a self-hosted runner executing arbitrary
code from a fork is a serious risk to the network it sits in.

There is a second, independent question. A workflow step written as `uses: some/action@v4`
resolves a tag, and a tag can be moved to a different commit by whoever owns the
repository. Every such step is a live dependency on someone else's mutable reference.

Where does CI run, and how are third-party actions referenced?

## Considered Options

* Keep self-hosted runners, and restrict fork pull requests
* Self-hosted for maintainer branches, hosted for forks
* GitHub-hosted runners for everything
* Reference actions by tag, by major version, or by commit SHA

## Decision Outcome

Chosen options: "GitHub-hosted runners for everything", and "by commit SHA".

Every job in all ten workflows declares `runs-on: ubuntu-latest`. No workflow names a
self-hosted label. A contributor's pull request runs the same way a maintainer's does, on
infrastructure neither of them owns.

All 50 third-party action references across 23 distinct actions are pinned to a full
40-character commit SHA, with the human-readable version in a trailing comment:

```yaml
uses: actions/checkout@11d5960a326750d5838078e36cf38b85af677262 # v4.4.0
```

A moved tag then changes nothing, and an upgrade is a reviewable diff.

The same reasoning extends past actions. The gitleaks scan downloads a pinned release
archive and checks its SHA-256 before running it, rather than trusting a marketplace action
whose licence check fails for organisation-owned repositories.

Two jobs are gated on `github.event.repository.visibility == 'public'`: CodeQL and
dependency review. Both need GitHub Advanced Security, which a private repository on this
plan does not have. The gate means they stay green while the repository is private and start
working the moment it is published, with no workflow edit.

### Consequences

* Good, because an external contributor gets CI results on their first pull request.
* Good, because fork code never executes inside a private network.
* Good, because a compromised upstream action cannot reach this repository by moving a tag.
* Good, because upgrades are visible. A SHA change is a diff a reviewer can question.
* Good, because gating on visibility means publishing the repository needs no CI change.
* Bad, because pinned actions do not receive upstream fixes automatically. Dependabot has
  to propose each bump, and an unattended repository drifts behind.
* Bad, because hosted runners are slower for integration tests that start containers, and
  their concurrency limits are not ours to raise.
* Bad, because the SHA hides what the version is. The trailing comment is the only readable
  signal, and nothing enforces that it is accurate.

## More Information

* All ten workflows: [`.github/workflows/`](../../.github/workflows/).
* Reporting policy and scanner scope: [`../../SECURITY.md`](../../SECURITY.md).

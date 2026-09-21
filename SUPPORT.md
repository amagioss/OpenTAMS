# Support

OpenTAMS is an open-source project developed and maintained by
[Amagi Media Labs Limited](https://www.amagi.com). This software is provided
"AS IS", without technical support, warranties, or guarantees of any kind
whatsoever. There is no paid support offering attached to this repository, and
no service-level agreement. Everything below is best-effort by the maintainers.

## Pick the right channel

| You want to | Go to |
|---|---|
| Ask how to do something, or whether something is supported | [GitHub Discussions](https://github.com/amagioss/opentams/discussions) |
| Report a bug | [New issue → Bug report](https://github.com/amagioss/opentams/issues/new/choose) |
| Request a feature or a TAMS endpoint we don't implement yet | [New issue → Feature request](https://github.com/amagioss/opentams/issues/new/choose) |
| Report a security vulnerability | [`SECURITY.md`](SECURITY.md) — **never a public issue** |
| Report Code-of-Conduct behaviour | `conduct@amagi.com`, per [`CODE_OF_CONDUCT.md`](CODE_OF_CONDUCT.md) |
| Contribute a change | [`CONTRIBUTING.md`](CONTRIBUTING.md) |

## Read these first

Most questions are answered by something already written down:

| Question | Document |
|---|---|
| How do I run it locally? | [Quickstart](README.md#quickstart) — one command brings up Postgres, MinIO, and the server |
| What environment variables exist? | [`docs/configuration.md`](docs/configuration.md) |
| Which parts of TAMS does it actually implement? | [`docs/conformance.md`](docs/conformance.md) — including the known gaps |
| Why does it behave differently from the TAMS spec here? | [`docs/adr/`](docs/adr/) — each divergence has an ADR |
| How do I use the CLI? | [`tamsctl`](README.md#cli-tamsctl) |
| How do I deploy it? | [`docs/deployment/`](docs/deployment/) |
| What does this release contain, and is it signed? | [`docs/deployment/released-images.md`](docs/deployment/released-images.md) |
| What should I work on first? | [`docs/good-first-issues.md`](docs/good-first-issues.md) |

The full documentation index is [`docs/README.md`](docs/README.md).

## Filing a good bug report

The issue template asks for these, and a report without them usually costs a
round-trip before anyone can act on it:

- **Version** — release tag, image tag, or commit SHA. `tamsctl version` prints
  build info for the CLI; the server has no `version` subcommand yet, so quote
  the image tag or the commit you built from.
- **Deployment** — local `docker-compose`, Kubernetes, ECS, bare binary.
- **What you did** — the request, ideally as a `curl` command or a `tamsctl`
  invocation.
- **What happened** — the response body and status, plus the relevant server
  logs. OpenTAMS emits structured JSON logs; include the `request_id` and we can
  correlate.
- **What you expected instead**, and where that expectation comes from (the TAMS
  spec, our docs, a previous version).

If the behaviour contradicts the TAMS specification, quote the part of the spec
you are relying on. If it contradicts our own docs, say which document — that is
a documentation bug at minimum, and we want to know either way.

## Response expectations

Set honestly, because a promise we cannot keep is worse than no promise:

| | |
|---|---|
| Security reports | Acknowledged within 3 business days — this is the one channel with a committed timeline. See [`SECURITY.md`](SECURITY.md). |
| Issues and pull requests | Usually a few business days. |
| Discussions | Best-effort. |

If something has gone quiet for more than a week, **ping the thread**. That is a
signal we lost it, not that we are ignoring you, and a nudge is welcome rather
than rude.

## What is supported

Only the latest `v0.x.y` release receives fixes. `main` is best-effort. The full
support matrix is at the top of [`SECURITY.md`](SECURITY.md).

The HTTP surface, the configuration variables, and the public Go packages are
all treated as unstable: any of them may break on a minor version bump. Release
notes call each break out explicitly. See
[Versioning policy](CONTRIBUTING.md#versioning-policy).

## Commercial support

Amagi does not currently offer commercial support for OpenTAMS. If that changes,
it will be announced here rather than assumed.

# Maintainers

This file is the authoritative roster. [`GOVERNANCE.md`](GOVERNANCE.md)
describes what these roles may decide and how the roster changes;
[`.github/CODEOWNERS`](.github/CODEOWNERS) turns the roster into automatic
review requests.

## Current maintainers

| Name | GitHub | Affiliation | Area |
|---|---|---|---|
| Akshay Narayan Pai | [@Akshay-Narayan-Pai](https://github.com/Akshay-Narayan-Pai) | Amagi Media Labs | Overall; API contract, release and supply chain |
| Hamza U | [@hamza-u](https://github.com/hamza-u) | Amagi Media Labs | Store and metastore, migrations |
| Yash Bagdi | [@yashbagdi](https://github.com/yashbagdi) | Amagi Media Labs | Deployment, Terraform, Helm |
| Naveen Rathore | [@naveenrathore81](https://github.com/naveenrathore81) | Amagi Media Labs | Service layer, observability |

Maintainers are listed with their affiliation so that reviewers and users can
see where the project's decision-making currently sits. OpenTAMS is presently
maintained entirely by Amagi employees — see
[Concentration of maintainership](GOVERNANCE.md#concentration-of-maintainership)
in the governance document for what the project intends to do about that.

## Emeritus maintainers

None yet. Maintainers who step down are listed here rather than deleted, so
that the project's history stays legible.

## What a maintainer does

- Reviews and merges pull requests, including the authority to approve changes
  to the API contract, database migrations, and release tooling.
- Triages incoming issues and security reports.
- Participates in release decisions.
- Is expected to respond, or hand off explicitly, rather than let a review go
  silent.

Maintainers are not required to review every area. The **Area** column above is
a routing hint, not a restriction, and it does not create an exclusive right of
review over that area.

## Reaching the maintainers

For most things, use the public channels in [`SUPPORT.md`](SUPPORT.md) — a
public issue is more useful than a direct message, because the answer helps the
next person who hits the same thing.

Use a private channel when the subject genuinely cannot be public:

- **Security vulnerabilities** — the reporting channels in
  [`SECURITY.md`](SECURITY.md). Not a public issue.
- **Code-of-Conduct concerns** — `conduct@amagi.com`, as described in
  [`CODE_OF_CONDUCT.md`](CODE_OF_CONDUCT.md).

## Becoming a maintainer

See [Adding a maintainer](GOVERNANCE.md#adding-a-maintainer). The short version:
sustained, high-quality contribution over time — code, review, triage, or
documentation — followed by a nomination from an existing maintainer and lazy
consensus among the rest. There is no contribution quota, and non-code
contribution counts.

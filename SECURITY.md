# Security Policy

Only the latest released version receives security fixes.

| Version | Supported          |
|---------|--------------------|
| Latest `v0.x.y` release | Yes (until superseded by the next minor) |
| Older `v0.x.y` releases | No                 |
| `main` (unreleased)     | Best-effort, not formally supported |

## Reporting a Vulnerability

**Please do not file a public GitHub issue for security reports.** Public reports give attackers a window before a fix is available.

We accept reports through two channels — pick whichever is easier for you:

### Channel 1 — GitHub Private Vulnerability Reporting (preferred)

1. Go to the repository's **Security** tab: <https://github.com/amagioss/opentams/security>
2. Click **Report a vulnerability**.
3. GitHub will create a private security advisory visible only to you and the maintainers.

GitHub's private reporting flow lets us collaborate on a fix, request a CVE, and coordinate disclosure inside one tracker. This is the path we prefer when you have a GitHub account.

### Channel 2 — Email

Send a report to **security@amagi.com**.

For sensitive findings we encourage using GitHub Private Vulnerability Reporting (Channel 1) instead — it encrypts in transit and keeps the report, fix, and coordinated disclosure inside one tracker. If you must use email, please avoid attaching exploit binaries or sensitive customer data to plaintext mail; a brief description that lets us reproduce is enough to get the conversation started.

Whatever channel you use, please include:

- A description of the vulnerability and the affected component (`pkg/...`, `internal/...`, an API endpoint, a deployment manifest, etc.).
- Steps to reproduce — a minimal proof of concept, request payload, or test case is ideal.
- The version (image tag, release tag, or commit SHA) and deployment context (Docker, Kubernetes, etc.) where you observed it.
- Your assessment of impact (data exposure, privilege escalation, denial of service, etc.) if you have one — we'll re-assess regardless.
- Whether you'd like public credit and, if so, how to credit you.

### Response Timeline

| Stage | Target |
|-------|--------|
| Acknowledgement of receipt | Within **3 business days** |
| Initial triage (severity, scope, reproduction confirmed) | Within **7 business days** of acknowledgement |
| Coordinated disclosure window | Up to **90 days** from triage; we aim to ship fixes much earlier for high-severity issues |

If we cannot meet a target, we will tell you why and propose a revised timeline rather than go silent. If you do not hear back within the acknowledgement window, please re-send — mail can get filtered.

### What We Ask of Reporters

- Give us reasonable time to investigate and ship a fix before public disclosure (the 90-day window above).
- Avoid testing against production OpenTAMS deployments you do not own. The repository ships a `deployments/docker/docker-compose.yml` that brings up a complete local stack for safe testing.
- Do not exfiltrate data, pivot to other systems, or otherwise expand impact while validating a finding.

### What You Can Expect From Us

- We will keep you informed of the fix's progress.
- We will publish a [GitHub Security Advisory](https://github.com/amagioss/opentams/security/advisories) for every confirmed vulnerability, with a CVE where applicable.
- We will credit reporters in the advisory and in the release notes (unless you ask to remain anonymous).
- We will not pursue legal action against reporters who follow the guidelines above in good faith.

## Out of Scope

The following are not considered vulnerabilities for the purposes of this policy:

- Findings against versions older than the latest `v0.x.y` release.
- Issues that depend on a misconfigured deployment outside OpenTAMS's documented configuration surface (e.g. an operator disabling `DB_SSLMODE` and exposing the DB to the internet).
- Findings against third-party dependencies — please report those upstream and let us know so we can pull a patched version.
- Denial-of-service via unbounded request volume (rate limiting is the operator's responsibility per the deployment guide).

If you are unsure whether something is in scope, **err on the side of reporting it** — we would rather triage and decline than miss a real issue.

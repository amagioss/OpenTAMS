# Third-Party Software Licenses

OpenTAMS (Copyright © 2026 Amagi Media Labs Limited) is licensed under the Apache License 2.0.
The full text is in the [`LICENSE`](../LICENSE) file at the root of this
repository.

OpenTAMS uses third-party open-source software components. Each component
remains subject to its respective copyright and license terms.

The files in this directory identify the applicable third-party components,
versions, licenses, copyright notices, and license conditions.

| License | Components | Notices |
|---|---|---|
| MIT | 34 | [`MIT.txt`](MIT.txt) |
| Apache-2.0 | 31 | [`Apache-2.0.txt`](Apache-2.0.txt) |
| BSD-3-Clause | 11 | [`BSD-3-Clause.txt`](BSD-3-Clause.txt) |
| 0BSD | 1 | [`0BSD.txt`](0BSD.txt) |

A dual-licensed module is listed under every license it is distributed
under, so these counts add up to more than the module total below.

## Scope

These are the 73 modules linked into the released `opentams` and `tamsctl`
binaries and the published container images — every module reachable from
`./cmd/...`. Go links statically, so all of them are redistributed inside
those artefacts.

Modules used only to build or test OpenTAMS are not listed. They are never
redistributed, so no attribution obligation attaches to them.

## Layout

Each file lists its components first, with the module path, version and
copyright notice, and then gives the license terms once. A component whose
license text differs from the standard text for its family is reproduced in
full under its own entry instead.

## Regenerating

These files are generated from the Go module cache. Do not edit them by hand.

```bash
make notices        # rewrite this directory
make notices-check  # fail if it is stale
```

Regenerate after any dependency change. CI runs `make notices-check`.

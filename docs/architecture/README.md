# OpenTAMS Architecture

This directory is the high-level design home for OpenTAMS. It explains the system shape, the storage model, and the API flows without requiring a reader to understand the Go package layout first.

Start here:

- [Overview](overview.md) - system context, control plane vs data plane, storage ownership, and deployment shape.
- [Data flows](data-flows.md) - request sequences for the main TAMS workflows.
- [Metadata model](metadata-model.md) - PostgreSQL entities, relationships, and object ownership semantics.
- [API conformance](../conformance.md) - endpoint-by-endpoint implementation status.
- [Configuration](../configuration.md) - deployment knobs and environment variables.

For contributors who need package boundaries, import direction, and where to start reading the code, see the [codebase guide](../development/codebase.md).

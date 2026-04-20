# Architecture Decision Records (ADRs)

This directory contains Architecture Decision Records documenting significant
technical decisions.

## What are ADRs?

ADRs document **technical implementation decisions** for specific architectural
components. Each ADR focuses on a single decision and includes:

- **Context**: The problem or constraint that led to this decision
- **Decision**: What was chosen and why
- **Consequences**: Trade-offs, pros, and cons
- **Alternatives**: Other options that were considered

## Creating a New ADR

```bash
docz create adr "Your ADR Title"
```

## ADR Status

- **Proposed**: Under discussion, not yet approved
- **Accepted**: Approved and being implemented or already implemented
- **Deprecated**: No longer relevant or superseded
- **Superseded by ADR-XXXX**: Replaced by another ADR

<!-- BEGIN DOCZ AUTO-GENERATED -->
## All ADRs

| ID | Title | Status | Date | Author | Link |
|----|-------|--------|------|--------|------|
| ADR-0001 | Use Go and the standard library net/http for rfc-api | Proposed | 2026-04-18 | Donald Gifford | [0001-use-go-and-stdlib-net-http-for-rfc-api.md](0001-use-go-and-stdlib-net-http-for-rfc-api.md) |
| ADR-0002 | Use PostgreSQL as the rfc-api datastore | Proposed | 2026-04-18 | Donald Gifford | [0002-use-postgresql-as-the-rfc-api-datastore.md](0002-use-postgresql-as-the-rfc-api-datastore.md) |
| ADR-0003 | Use Meilisearch for rfc-api search | Proposed | 2026-04-18 | Donald Gifford | [0003-use-meilisearch-for-rfc-api-search.md](0003-use-meilisearch-for-rfc-api-search.md) |
<!-- END DOCZ AUTO-GENERATED -->

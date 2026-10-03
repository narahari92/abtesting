# Variant Service

A multi-tenant experimentation service: a small JavaScript snippet decides which variant a visitor sees, records exposures and conversions, and reports which variant is winning. Assignment is a deterministic hash evaluated in the browser against a cached per-site payload, so the service's origin is never on the page-render path.

Work in progress. The plan is in [`spec/implementation-plan.md`](spec/implementation-plan.md); the design is in [`spec/technical-plan.md`](spec/technical-plan.md).

## Develop

```sh
make build        # bin/server
make test         # Go and JS tests
make lint         # go vet + gofmt
```

# Backend conventions

Read the root AGENTS.md and docs/standards/api.md. The module has one go.mod. Keep application policy independent from database and HTTP adapters. Never expose generated wire structs as persistence models.

Run gofmt, go vet and go test -race ./... for backend changes. Tests use *_test.go beside the behavior; real dependency tests require an integration build tag and explicit test configuration. Never silently pass an integration check without the dependency.

Use UTC timestamps, context cancellation and bounded operations. Published state is immutable, every mutation has a transaction boundary and every project operation is authorized. Keep errors safe at the HTTP boundary. Use parameterized SQL and test concurrent update failure paths.

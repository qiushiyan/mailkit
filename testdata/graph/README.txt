DOC-DERIVED. These Graph responses were written from the Microsoft Graph
reference, not recorded from a live tenant (consent pending as of
2026-08-22). They pin the adapter's translation of the documented shapes.
Replace them with `go test -tags live -record ./internal/graph` once a
tenant token exists; until then a pass here is evidence about the
documentation, not about Graph.

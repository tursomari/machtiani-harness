# Testing snippet-discovery

## Unit tests
From this directory:
```bash
go test ./internal/...
```

## End-to-end tests
The e2e tests build a temporary binary with a stubbed LLM and run it against a fixture repo:
```bash
go test ./e2e -tags e2e_stub_llm
```

The stubbed LLM responses are provided via the `SNIPPET_DISCOVERY_E2E_RESPONSES` environment variable inside the tests.

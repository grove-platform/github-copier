# test-llm

Smoke-test the operator UI's LLM client against the configured provider.

## Purpose

Verify end-to-end that:

- The provider URL and API key are reachable from your machine
- Auth headers are accepted (direct Anthropic API or APIM-fronted gateway)
- The active model responds to a real rule-suggester prompt and returns valid JSON

Useful after rotating `ANTHROPIC_API_KEY`, changing `LLM_BASE_URL`, or pointing at a new gateway.

## Build

```bash
go build -o test-llm ./cmd/test-llm
```

## Usage

```bash
./test-llm [-env <path>] [-timeout <duration>]
```

The tool reads standard env vars — `LLM_PROVIDER`, `LLM_BASE_URL`, `LLM_MODEL`, `ANTHROPIC_API_KEY` — from the process environment. Use `-env` to load a `.env`-style file first. Inline env vars on the command line override file values.

## Examples

Smoke-test against the local `.env.test`:

```bash
./test-llm -env .env.test
```

Override the key without editing the env file:

```bash
ANTHROPIC_API_KEY='sk-...' ./test-llm -env .env.test
```

Test Ollama locally:

```bash
LLM_PROVIDER=ollama LLM_BASE_URL=http://localhost:11434 LLM_MODEL=qwen2.5-coder:7b ./test-llm
```

## Output

On success:

```
Provider: anthropic
Base URL: https://grove-gateway-prod.azure-api.net/grove-foundry-prod/anthropic
Model:    claude-haiku-4-5
API key:  sk-a…xyz9

✅ Ping OK
✅ ListModels: 3 models
   - claude-opus-4-7
   - claude-sonnet-4-6
   - claude-haiku-4-5-20251001
✅ GenerateJSON parsed OK:
   {
     "transform_type": "move",
     "transform_from": "agg/python/models",
     ...
   }

🎉 All checks passed — the LLM provider is reachable and usable.
```

## Exit Codes

| Code | Meaning                              |
|------|--------------------------------------|
| 0    | All checks passed                    |
| 1    | Any failure (auth, network, parsing) |

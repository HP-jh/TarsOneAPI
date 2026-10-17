# TarsOneAPI

MCP integration for OneAPI (OpenAI-compatible API management).

## Tools

- `oneapi_models` — List models managed by OneAPI
- `oneapi_chat` — Chat completion via OneAPI

## Quick Start

```bash
go build -o tarsoneapi
TARS_MCP_ENDPOINT=https://api.example.com TARS_MCP_API_KEY=xxx ./tarsoneapi
```

## Environment Variables

- `TARS_MCP_ENDPOINT` — upstream API endpoint
- `TARS_MCP_API_KEY` — upstream API key
- `TARS_MCP_MODE` — `stdio` (default) or `http`

## License

MIT

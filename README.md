# Vision MCP Server

[![Go Report Card](https://goreportcard.com/badge/github.com/yourusername/vision-mcp)](https://goreportcard.com/report/github.com/yourusername/vision-mcp)
[![License: MIT](https://img.shields.io/badge/License-MIT-yellow.svg)](LICENSE)

A high-performance, secure **Model Context Protocol (MCP)** server written in Go that empowers text-only Large Language Models (such as DeepSeek-V3, Qwen-2.5, or text-only Llama models) to "see" and analyze local image files by calling an external OpenAI or OpenAI-compatible Vision Model.

---

## Why Use This? (Intended Use Case)

This MCP server is **specifically designed for text-only Large Language Models** that do not natively support image inputs (multimodality). 

> [!NOTE]
> Native multimodal models (like Claude Sonnet 4.6, GPT-5.5, or Kimi-K2.6) do not need this MCP server because they can already process images directly.
> 
> Instead, this server acts as an **eye for text-only models**: when a text-only model needs to understand a local image, it calls the `describe_image` tool. This server reads the local image, calls an external vision API (such as GPT-4o or Moonshot/Kimi vision model) to get a rich textual description, and feeds that description back to the text-only model, allowing it to reason about the image seamlessly.

---

## Features

- 🛠️ **MCP Tools**: Implements the Model Context Protocol `tools/list` and `tools/call` specs. Exposes a robust `describe_image` tool to the LLM client.
- 🔒 **Security-First Directory Sandbox**: Strictly validates file paths using absolute resolution against the current working directory. Prevents directory traversal attacks (`../..`) to protect sensitive host files.
- 🧵 **Thread-Safe I/O**: Protects standard output (`stdout`) using a `sync.Mutex` lock to guarantee JSON-RPC packet integrity during highly concurrent tool invocations.
- 🌐 **Provider Flexible**: Built to work with OpenAI's API or any OpenAI-compatible custom endpoints (such as DeepSeek, local LLM gateways, etc.).

---

## Prerequisites

- **Go 1.21+** (if compiling from source)
- An **OpenAI API Key** (or API keys for any OpenAI-compatible provider)

---

## Installation & Build

Compile the server executable locally from the workspace root:

```bash
go build -o vision-mcp main.go
```

To run the server manually in `stdio` mode (mainly for testing, it expects JSON-RPC input via stdin):

```bash
./vision-mcp
```

---

## Configuration

The server is configured entirely using **environment variables**. They can be supplied via your shell, system daemon, or the MCP client configuration file (e.g., Claude Desktop, OpenCode).

### Provider Priority
1. **Anthropic** (Priority 1): If `ANTHROPIC_API_KEY` is configured, vision requests will be sent to the Anthropic Messages API (`/v1/messages`).
2. **OpenAI / Compatible** (Priority 2 / Fallback): If `ANTHROPIC_API_KEY` is not set, vision-mcp falls back to `OPENAI_API_KEY` using the OpenAI Chat Completions API (`/chat/completions`).

| Environment Variable | Description | Default |
| :--- | :--- | :--- |
| **`ANTHROPIC_API_KEY`** | Anthropic API Key. **Prioritized if set**. | None |
| `ANTHROPIC_BASE_URL` | Base URL for Anthropic API. | `https://api.anthropic.com/v1` |
| `ANTHROPIC_DEFAULT_MODEL` | Anthropic model ID to use for vision. | `claude-3-5-sonnet-20241022` |
| `ANTHROPIC_CUSTOM_HEADERS`| Custom HTTP headers for Anthropic requests (e.g. `X-Organization-Id: 301` or JSON `{"X-Org": "301"}`). | None |
| **`OPENAI_API_KEY`** | OpenAI or compatible API Key. **Used if Anthropic is not configured**. | None |
| `OPENAI_BASE_URL` | Base URL for OpenAI compatible endpoint. | `https://api.openai.com/v1` |
| `OPENAI_DEFAULT_MODEL` | Model ID to use for OpenAI endpoint. | `gpt-4o` |
| `OPENAI_CUSTOM_HEADERS` | Custom HTTP headers for OpenAI requests. | None |

---

## MCP Integration

### OpenCode

To use this server with OpenCode, add the server configuration to your OpenCode `config.json` file:

#### Example 1: Using Anthropic (Direct API / Custom Headers)
```json
{
  "$schema": "https://opencode.ai/config.json",
  "mcp": {
    "vision-mcp": {
      "type": "local",
      "command": ["/home/horsepower/src/vision-mcp/vision-mcp"],
      "environment": {
        "ANTHROPIC_API_KEY": "your-anthropic-api-key-here",
        "ANTHROPIC_BASE_URL": "https://api.anthropic.com/v1",
        "ANTHROPIC_DEFAULT_MODEL": "claude-3-5-sonnet-20241022",
        "ANTHROPIC_CUSTOM_HEADERS": "X-Organization-Id: 301"
      },
      "enabled": true
    }
  }
}
```

#### Example 2: Using OpenAI / OpenAI-Compatible Provider (Fallback)
```json
{
  "$schema": "https://opencode.ai/config.json",
  "mcp": {
    "vision-mcp": {
      "type": "local",
      "command": ["/home/horsepower/src/vision-mcp/vision-mcp"],
      "environment": {
        "OPENAI_BASE_URL": "https://api.moonshot.cn/v1",
        "OPENAI_API_KEY": "your-openai-or-compatible-api-key-here",
        "OPENAI_DEFAULT_MODEL": "kimi-k2.6"
      },
      "enabled": true
    }
  }
}
```

Make sure to replace `/home/horsepower/src/vision-mcp/vision-mcp` with the absolute path to your compiled binary on your local machine.

---

## Exposed Tools

### `describe_image`

Analyzes a local image file using the configured vision model and a user prompt.

**Arguments:**
- `filePath` (string, required): Absolute path or relative path (from the current working directory) to the target image file. Supported formats: `.png`, `.jpg`, `.jpeg`, `.webp`, `.gif`.
- `prompt` (string, required): A detailed question or instruction for the vision model about the image (e.g. *"What text is visible in this invoice screenshot?"*).

---

## Concurrency & Security Architecture

### Path Traversal Defense
The server guarantees that files requested by the LLM client do not escape the sandbox boundary:
1. Resolves the current working directory (`.`) to its physical absolute path.
2. Combines the target file path with the absolute workspace root and resolves it to its physical absolute path (`filepath.Abs`).
3. Uses `filepath.Rel` to check relative relations. If the target path goes above the root boundary, access is blocked immediately with an error returned to the model.

### Stdio Lock Protection
Go's concurrent model execution executes `tools/call` in independent Goroutines. Standard standard input/output is shared. To prevent interleaved JSON outputs, a `sync.Mutex` locks `os.Stdout` writing globally.

---

## License

This project is licensed under the MIT License.

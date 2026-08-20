package main

import (
	"bufio"
	"bytes"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// MCP JSON-RPC Types
type JSONRPCRequest struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      any             `json:"id,omitempty"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params,omitempty"`
}

type JSONRPCResponse struct {
	JSONRPC string `json:"jsonrpc"`
	ID      any    `json:"id"`
	Result  any    `json:"result,omitempty"`
	Error   *Error `json:"error,omitempty"`
}

type Error struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

// Tool Definition Types
type Tool struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	InputSchema any    `json:"inputSchema"`
}

type ToolsListResult struct {
	Tools []Tool `json:"tools"`
}

// Call Tool Types
type ToolCallParams struct {
	Name      string          `json:"name"`
	Arguments json.RawMessage `json:"arguments"`
}

type DescribeImageArgs struct {
	FilePath string `json:"filePath"`
	Prompt   string `json:"prompt"`
}

type ToolCallResponseContent struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

type ToolCallResponseResult struct {
	Content []ToolCallResponseContent `json:"content"`
	IsError bool                      `json:"isError"`
}

// OpenAI API Payload Types
type OpenAIRequestContent struct {
	Type     string          `json:"type"`
	Text     string          `json:"text,omitempty"`
	ImageURL *OpenAIImageURL `json:"image_url,omitempty"`
}

type OpenAIImageURL struct {
	URL string `json:"url"`
}

type OpenAIRequestMessage struct {
	Role    string                 `json:"role"`
	Content []OpenAIRequestContent `json:"content"`
}

type OpenAIRequest struct {
	Model    string                 `json:"model"`
	Messages []OpenAIRequestMessage `json:"messages"`
}

type OpenAIResponse struct {
	Choices []struct {
		Message struct {
			Content string `json:"content"`
		} `json:"message"`
	} `json:"choices"`
	Error *struct {
		Message string `json:"message"`
	} `json:"error"`
}

// Anthropic API Payload Types
type AnthropicSource struct {
	Type      string `json:"type"`
	MediaType string `json:"media_type"`
	Data      string `json:"data"`
}

type AnthropicContent struct {
	Type   string           `json:"type"`
	Text   string           `json:"text,omitempty"`
	Source *AnthropicSource `json:"source,omitempty"`
}

type AnthropicMessage struct {
	Role    string             `json:"role"`
	Content []AnthropicContent `json:"content"`
}

type AnthropicRequest struct {
	Model     string             `json:"model"`
	MaxTokens int                `json:"max_tokens"`
	Messages  []AnthropicMessage `json:"messages"`
}

type AnthropicResponse struct {
	Content []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	} `json:"content"`
	Error *struct {
		Type    string `json:"type"`
		Message string `json:"message"`
	} `json:"error"`
}

var (
	stdoutMu  sync.Mutex
	requestWg sync.WaitGroup
)

func logErr(format string, args ...any) {
	fmt.Fprintf(os.Stderr, "[vision-mcp] "+format+"\n", args...)
}

func main() {
	logErr("Vision MCP Server starting up...")

	reader := bufio.NewReader(os.Stdin)
	for {
		line, err := reader.ReadBytes('\n')
		if err != nil {
			if err == io.EOF {
				logErr("Stdin reached EOF. Waiting for in-flight requests...")
				break
			}
			logErr("Error reading stdin: %v", err)
			break
		}

		line = bytes.TrimSpace(line)
		if len(line) == 0 {
			continue
		}

		var req JSONRPCRequest
		if err := json.Unmarshal(line, &req); err != nil {
			logErr("Failed to unmarshal request: %v. Line: %s", err, string(line))
			sendErrorResponse(nil, -32700, "Parse error")
			continue
		}

		handleRequest(&req)
	}

	requestWg.Wait()
	logErr("Exiting gracefully.")
}

func handleRequest(req *JSONRPCRequest) {
	switch req.Method {
	case "initialize":
		res := map[string]any{
			"protocolVersion": "2024-11-05",
			"capabilities": map[string]any{
				"tools": map[string]any{},
			},
			"serverInfo": map[string]any{
				"name":    "vision-mcp",
				"version": "0.1.0",
			},
		}
		sendSuccessResponse(req.ID, res)

	case "notifications/initialized":
		logErr("Client acknowledged initialization.")

	case "tools/list":
		res := ToolsListResult{
			Tools: []Tool{
				{
					Name:        "describe_image",
					Description: "Analyze a local image file using a vision model with a custom prompt",
					InputSchema: map[string]any{
						"type": "object",
						"properties": map[string]any{
							"filePath": map[string]any{
								"type":        "string",
								"description": "Absolute path or relative path from workspace root to the image file",
							},
							"prompt": map[string]any{
								"type":        "string",
								"description": "Prompt or question to ask the vision model about the image",
							},
						},
						"required": []string{"filePath", "prompt"},
					},
				},
			},
		}
		sendSuccessResponse(req.ID, res)

	case "tools/call":
		var params ToolCallParams
		if err := json.Unmarshal(req.Params, &params); err != nil {
			sendErrorResponse(req.ID, -32602, "Invalid params")
			return
		}

		if params.Name == "describe_image" {
			var args DescribeImageArgs
			if err := json.Unmarshal(params.Arguments, &args); err != nil {
				sendErrorResponse(req.ID, -32602, "Invalid arguments")
				return
			}
			requestWg.Add(1)
			go func() {
				defer requestWg.Done()
				result := executeDescribeImage(args)
				sendSuccessResponse(req.ID, result)
			}()
		} else {
			sendErrorResponse(req.ID, -32601, fmt.Sprintf("Tool not found: %s", params.Name))
		}

	case "ping":
		sendSuccessResponse(req.ID, map[string]any{})

	default:
		if req.ID != nil {
			sendErrorResponse(req.ID, -32601, "Method not found")
		}
	}
}

func executeDescribeImage(args DescribeImageArgs) ToolCallResponseResult {
	// 1. Path & File System Security Validation
	// Prevent directory traversal attacks by ensuring the path resides within the allowed directory.
	// The allowed sandbox root is strictly the current working directory.
	allowedRoot, err := filepath.Abs(".")
	if err != nil {
		return errorToolResult("Server internal error: cannot resolve workspace path")
	}

	// Resolve the absolute path of the target image
	var targetPath string
	if filepath.IsAbs(args.FilePath) {
		targetPath = filepath.Clean(args.FilePath)
	} else {
		targetPath = filepath.Clean(filepath.Join(allowedRoot, args.FilePath))
	}

	absTargetPath, err := filepath.Abs(targetPath)
	if err != nil {
		return errorToolResult("Invalid image path format")
	}

	// Verify that target path is under allowedRoot
	rel, err := filepath.Rel(allowedRoot, absTargetPath)
	if err != nil || strings.HasPrefix(rel, "..") || filepath.IsAbs(rel) {
		return errorToolResult("Access denied: File is outside of the workspace sandbox")
	}

	// 2. Read file content
	file, err := os.Open(absTargetPath)
	if err != nil {
		return errorToolResult(fmt.Sprintf("Failed to open file: %v", err))
	}
	defer file.Close()

	fileBytes, err := io.ReadAll(file)
	if err != nil {
		return errorToolResult(fmt.Sprintf("Failed to read file contents: %v", err))
	}

	if len(fileBytes) == 0 {
		return errorToolResult("Image file is empty")
	}

	// 3. Determine MIME type based on extension
	mimeType := getMimeType(absTargetPath)
	if mimeType == "" {
		return errorToolResult("Unsupported or unidentified image type. Must be png, jpeg, jpg, webp, or gif.")
	}

	// 4. Encode as Base64
	base64Data := base64.StdEncoding.EncodeToString(fileBytes)

	// 5. Select Provider: Prioritize Anthropic if configured, fallback to OpenAI
	anthropicKey := os.Getenv("ANTHROPIC_API_KEY")
	anthropicBaseURL := os.Getenv("ANTHROPIC_BASE_URL")
	if anthropicBaseURL == "" {
		anthropicBaseURL = os.Getenv("ANTHROPIC_URL")
	}

	openAIKey := os.Getenv("OPENAI_API_KEY")

	if anthropicKey != "" || anthropicBaseURL != "" {
		return callAnthropicVision(anthropicKey, args.Prompt, mimeType, base64Data)
	} else if openAIKey != "" {
		return callOpenAIVision(openAIKey, args.Prompt, mimeType, base64Data)
	} else {
		return errorToolResult("Neither ANTHROPIC_API_KEY nor OPENAI_API_KEY is configured. Please provide an API key.")
	}
}

func callAnthropicVision(apiKey, prompt, mimeType, base64Data string) ToolCallResponseResult {
	baseURL := os.Getenv("ANTHROPIC_BASE_URL")
	if baseURL == "" {
		baseURL = os.Getenv("ANTHROPIC_URL")
	}
	if baseURL == "" {
		baseURL = "https://api.anthropic.com/v1"
	}
	baseURL = strings.TrimRight(baseURL, "/")

	modelID := os.Getenv("ANTHROPIC_DEFAULT_MODEL")
	if modelID == "" {
		modelID = os.Getenv("ANTHROPIC_MODEL")
	}
	if modelID == "" {
		modelID = "claude-3-5-sonnet-20241022"
	}

	logErr("Requesting Anthropic vision model '%s' via %s...", modelID, baseURL)

	payload := AnthropicRequest{
		Model:     modelID,
		MaxTokens: 1024,
		Messages: []AnthropicMessage{
			{
				Role: "user",
				Content: []AnthropicContent{
					{
						Type: "image",
						Source: &AnthropicSource{
							Type:      "base64",
							MediaType: mimeType,
							Data:      base64Data,
						},
					},
					{
						Type: "text",
						Text: prompt,
					},
				},
			},
		},
	}

	payloadBytes, err := json.Marshal(payload)
	if err != nil {
		return errorToolResult(fmt.Sprintf("Failed to construct Anthropic API request: %v", err))
	}

	var apiURL string
	if strings.HasSuffix(baseURL, "/messages") {
		apiURL = baseURL
	} else if strings.HasSuffix(baseURL, "/v1") {
		apiURL = baseURL + "/messages"
	} else {
		apiURL = baseURL + "/v1/messages"
	}

	httpReq, err := http.NewRequest("POST", apiURL, bytes.NewBuffer(payloadBytes))
	if err != nil {
		return errorToolResult(fmt.Sprintf("Failed to initialize HTTP client: %v", err))
	}

	httpReq.Header.Set("Content-Type", "application/json")
	if apiKey != "" {
		httpReq.Header.Set("x-api-key", apiKey)
	}
	httpReq.Header.Set("anthropic-version", "2023-06-01")

	// Apply custom headers if configured (e.g. ANTHROPIC_CUSTOM_HEADERS="X-Organization-Id: 301")
	applyCustomHeaders(httpReq, os.Getenv("ANTHROPIC_CUSTOM_HEADERS"))

	client := &http.Client{
		Timeout: 60 * time.Second,
	}

	resp, err := client.Do(httpReq)
	if err != nil {
		return errorToolResult(fmt.Sprintf("Failed to call Anthropic vision API: %v", err))
	}
	defer resp.Body.Close()

	respBytes, err := io.ReadAll(resp.Body)
	if err != nil {
		return errorToolResult(fmt.Sprintf("Failed to read Anthropic API response body: %v", err))
	}

	if resp.StatusCode != http.StatusOK {
		var anthropicErr AnthropicResponse
		_ = json.Unmarshal(respBytes, &anthropicErr)
		errMsg := fmt.Sprintf("Anthropic API returned status %d", resp.StatusCode)
		if anthropicErr.Error != nil && anthropicErr.Error.Message != "" {
			errMsg = fmt.Sprintf("Anthropic API Error (%d): %s", resp.StatusCode, anthropicErr.Error.Message)
		} else {
			errMsg = fmt.Sprintf("Anthropic API Error (%d): %s", resp.StatusCode, string(respBytes))
		}
		return errorToolResult(errMsg)
	}

	var apiResponse AnthropicResponse
	if err := json.Unmarshal(respBytes, &apiResponse); err != nil {
		return errorToolResult(fmt.Sprintf("Failed to parse Anthropic API response: %v", err))
	}

	var resultTexts []string
	for _, item := range apiResponse.Content {
		if item.Type == "text" && item.Text != "" {
			resultTexts = append(resultTexts, item.Text)
		}
	}

	if len(resultTexts) == 0 {
		return errorToolResult("Anthropic API returned successfully, but returned zero text content")
	}

	return ToolCallResponseResult{
		Content: []ToolCallResponseContent{
			{
				Type: "text",
				Text: strings.Join(resultTexts, "\n"),
			},
		},
		IsError: false,
	}
}

func callOpenAIVision(apiKey, prompt, mimeType, base64Data string) ToolCallResponseResult {
	baseURL := os.Getenv("OPENAI_BASE_URL")
	if baseURL == "" {
		baseURL = "https://api.openai.com/v1"
	}
	baseURL = strings.TrimRight(baseURL, "/")

	modelID := os.Getenv("OPENAI_DEFAULT_MODEL")
	if modelID == "" {
		modelID = "gpt-5.4"
	}

	logErr("Requesting OpenAI vision model '%s' via %s...", modelID, baseURL)

	dataURL := fmt.Sprintf("data:%s;base64,%s", mimeType, base64Data)

	payload := OpenAIRequest{
		Model: modelID,
		Messages: []OpenAIRequestMessage{
			{
				Role: "user",
				Content: []OpenAIRequestContent{
					{
						Type: "text",
						Text: prompt,
					},
					{
						Type: "image_url",
						ImageURL: &OpenAIImageURL{
							URL: dataURL,
						},
					},
				},
			},
		},
	}

	payloadBytes, err := json.Marshal(payload)
	if err != nil {
		return errorToolResult(fmt.Sprintf("Failed to construct API request: %v", err))
	}

	var apiURL string
	if strings.HasSuffix(baseURL, "/chat/completions") {
		apiURL = baseURL
	} else {
		apiURL = fmt.Sprintf("%s/chat/completions", baseURL)
	}

	httpReq, err := http.NewRequest("POST", apiURL, bytes.NewBuffer(payloadBytes))
	if err != nil {
		return errorToolResult(fmt.Sprintf("Failed to initialize HTTP client: %v", err))
	}

	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Authorization", fmt.Sprintf("Bearer %s", apiKey))

	// Apply custom headers if configured
	applyCustomHeaders(httpReq, os.Getenv("OPENAI_CUSTOM_HEADERS"))

	client := &http.Client{
		Timeout: 300 * time.Second,
	}

	resp, err := client.Do(httpReq)
	if err != nil {
		return errorToolResult(fmt.Sprintf("Failed to call vision API: %v", err))
	}
	defer resp.Body.Close()

	respBytes, err := io.ReadAll(resp.Body)
	if err != nil {
		return errorToolResult(fmt.Sprintf("Failed to read API response body: %v", err))
	}

	if resp.StatusCode != http.StatusOK {
		var openAIError OpenAIResponse
		_ = json.Unmarshal(respBytes, &openAIError)
		errMsg := fmt.Sprintf("API returned status %d", resp.StatusCode)
		if openAIError.Error != nil && openAIError.Error.Message != "" {
			errMsg = fmt.Sprintf("API Error (%d): %s", resp.StatusCode, openAIError.Error.Message)
		} else {
			errMsg = fmt.Sprintf("API Error (%d): %s", resp.StatusCode, string(respBytes))
		}
		return errorToolResult(errMsg)
	}

	var apiResponse OpenAIResponse
	if err := json.Unmarshal(respBytes, &apiResponse); err != nil {
		return errorToolResult(fmt.Sprintf("Failed to parse API response: %v", err))
	}

	if len(apiResponse.Choices) == 0 {
		return errorToolResult("API returned successfully, but returned zero content choices")
	}

	resultText := apiResponse.Choices[0].Message.Content
	return ToolCallResponseResult{
		Content: []ToolCallResponseContent{
			{
				Type: "text",
				Text: resultText,
			},
		},
		IsError: false,
	}
}

func applyCustomHeaders(req *http.Request, rawHeaders string) {
	rawHeaders = strings.TrimSpace(rawHeaders)
	if rawHeaders == "" {
		return
	}

	// 1. Check if rawHeaders is JSON formatted, e.g. {"X-Organization-Id": "301"}
	if strings.HasPrefix(rawHeaders, "{") && strings.HasSuffix(rawHeaders, "}") {
		var jsonMap map[string]any
		if err := json.Unmarshal([]byte(rawHeaders), &jsonMap); err == nil {
			for k, v := range jsonMap {
				k = strings.TrimSpace(k)
				valStr := fmt.Sprintf("%v", v)
				if k != "" && valStr != "" {
					req.Header.Set(k, valStr)
				}
			}
			return
		}
	}

	// 2. Delimiter-separated format (e.g. newline, comma, semicolon)
	// Example: "X-Organization-Id: 301" or "X-Org: 301, X-Workspace-Id: 42"
	lines := strings.FieldsFunc(rawHeaders, func(r rune) bool {
		return r == '\n' || r == '\r' || r == ',' || r == ';'
	})

	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		parts := strings.SplitN(line, ":", 2)
		if len(parts) == 2 {
			k := strings.TrimSpace(parts[0])
			v := strings.TrimSpace(parts[1])
			if k != "" {
				req.Header.Set(k, v)
			}
		}
	}
}

func getMimeType(filePath string) string {
	ext := strings.ToLower(filepath.Ext(filePath))
	switch ext {
	case ".png":
		return "image/png"
	case ".jpg", ".jpeg":
		return "image/jpeg"
	case ".webp":
		return "image/webp"
	case ".gif":
		return "image/gif"
	default:
		return ""
	}
}

func errorToolResult(message string) ToolCallResponseResult {
	logErr("Error processing describe_image: %s", message)
	return ToolCallResponseResult{
		Content: []ToolCallResponseContent{
			{
				Type: "text",
				Text: fmt.Sprintf("Error: %s", message),
			},
		},
		IsError: true,
	}
}

func sendSuccessResponse(id any, result any) {
	resp := JSONRPCResponse{
		JSONRPC: "2.0",
		ID:      id,
		Result:  result,
	}
	sendResponse(&resp)
}

func sendErrorResponse(id any, code int, message string) {
	resp := JSONRPCResponse{
		JSONRPC: "2.0",
		ID:      id,
		Error: &Error{
			Code:    code,
			Message: message,
		},
	}
	sendResponse(&resp)
}

func sendResponse(resp *JSONRPCResponse) {
	data, err := json.Marshal(resp)
	if err != nil {
		logErr("Failed to marshal JSON-RPC response: %v", err)
		return
	}
	// Append newline to demarcate message boundary
	data = append(data, '\n')

	stdoutMu.Lock()
	defer stdoutMu.Unlock()
	_, _ = os.Stdout.Write(data)
}

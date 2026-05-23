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

var (
	stdoutMu sync.Mutex
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
				logErr("Stdin reached EOF. Exiting gracefully.")
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
			go func() {
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
	dataURL := fmt.Sprintf("data:%s;base64,%s", mimeType, base64Data)

	// 5. Read Configuration from environment variables
	baseURL := os.Getenv("OPENAI_BASE_URL")
	if baseURL == "" {
		baseURL = "https://api.openai.com/v1"
	}
	baseURL = strings.TrimSuffix(baseURL, "/")

	apiKey := os.Getenv("OPENAI_API_KEY")
	if apiKey == "" {
		return errorToolResult("OPENAI_API_KEY environment variable is not configured. Please supply an API key.")
	}

	modelID := os.Getenv("OPENAI_DEFAULT_MODEL")
	if modelID == "" {
		modelID = "gpt-5.4"
	}

	logErr("Requesting vision model '%s' via %s...", modelID, baseURL)

	// 6. Build the payload
	payload := OpenAIRequest{
		Model: modelID,
		Messages: []OpenAIRequestMessage{
			{
				Role: "user",
				Content: []OpenAIRequestContent{
					{
						Type: "text",
						Text: args.Prompt,
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

	// 7. Execute the API request
	apiURL := fmt.Sprintf("%s/chat/completions", baseURL)
	httpReq, err := http.NewRequest("POST", apiURL, bytes.NewBuffer(payloadBytes))
	if err != nil {
		return errorToolResult(fmt.Sprintf("Failed to initialize HTTP client: %v", err))
	}

	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Authorization", fmt.Sprintf("Bearer %s", apiKey))

	client := &http.Client{
		Timeout: 60 * time.Second,
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

package api

import (
	"bytes"
	"context" // Import context for cancellation
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/ioutil"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"reflect" // Imported for deep equality check
	"strings"
	"sync"
	"time"

	"github.com/charmbracelet/glamour"
	"github.com/tursomari/machtiani/mct/internal/logging" // Import the logging client
	"github.com/tursomari/machtiani/mct/internal/utils"
)

var (
	HeadOID               string = "none"
	BuildDate             string = "unknown"
	MachtianiURL          string = "http://localhost:5071"
	RepoManagerURL        string = "http://localhost:5070"
	MachtianiGitRemoteURL string = "none"
)

func extractRepoName(projectURL string) string {
	parts := strings.Split(projectURL, "/")
	for i := len(parts) - 1; i >= 0; i-- {
		if parts[i] != "" {
			return parts[i]
		}
	}
	return projectURL
}

var (
	renderer     *glamour.TermRenderer
	rendererOnce sync.Once
	rendererErr  error
)

const (
	CONTENT_TYPE_KEY     = "Content-Type"
	CONTENT_TYPE_VALUE   = "application/json"
	API_GATEWAY_HOST_KEY = "X-RapidAPI-Key"
)

type AddRepositoryResponse struct {
	Message                string `json:"message"`
	FullPath               string `json:"full_path"`
	ApiKeyProvided         bool   `json:"api_key_provided"`
	LlmModelApiKeyProvided bool   `json:"llm_model_api_key_provided"`
}

type DeleteStoreResponse struct {
	Message string `json:"message"`
}

type LoadResponse struct {
	EmbeddingTokens int `json:"embedding_tokens"`
	InferenceTokens int `json:"inference_tokens"`
}

type StatusResponse struct {
	LockFilePresent  bool    `json:"lock_file_present"`
	LockTimeDuration float64 `json:"lock_time_duration"`
	ErrorLogs        string  `json:"error_logs"` // New field added
}

// PatchedFile represents a single patched file from agent-patch
type PatchedFile struct {
	Path         string `json:"path"`
	Content      string `json:"content"`
	OriginalFile string `json:"original_file"`
}

// PatchedFilesData stores information about patched files from agent-patch
type PatchedFilesData struct {
	Status       string        `json:"status"`
	Message      string        `json:"message"`
	Project      string        `json:"project"`
	Model        string        `json:"model"`
	Head         string        `json:"head"`
	PatchedFiles []PatchedFile `json:"patched_files"`
}

// UpdateFileContent represents file content updates (deprecated but kept for compatibility)
type UpdateFileContent struct {
	UpdatedContent string   `json:"updated_content"`
	Errors         []string `json:"errors"`
}

// NewFilesData stores information about suggested new files
type NewFilesData struct {
	NewContent   map[string]string `json:"new_content"`
	NewFilePaths []string          `json:"new_file_paths"`
	Errors       []string          `json:"errors"`
}

type GenerateResponseResult struct {
	LlmModelResponse      string                       `json:"llm_model_response"`
	RawResponse           string                       `json:"llm_model_response"`
	RetrievedFilePaths    []string                     `json:"retrieved_file_paths"`
	UpdateContentResponse map[string]UpdateFileContent `json:"update_content_response"` // Deprecated
	PatchedFiles          *PatchedFilesData            `json:"patched_files,omitempty"`
	HeadCommitHash        string                       `json:"head_commit_hash"`
	NewFiles              *NewFilesData                `json:"new_files,omitempty"`
}

// EstimateTokenCount calls the token-count endpoint and returns embedding and inference token counts.
func EstimateTokenCount(codeURL string, name string, apiKey *string) (int, int, error) {
	countTokenRequestData := map[string]interface{}{
		"codehost_url": codeURL,
		"project_name": name,
		"vcs_type":     "git",
		"api_key":      apiKey,
	}

	repoManagerURL := RepoManagerURL
	if repoManagerURL == "" {
		return 0, 0, fmt.Errorf("MACHTIANI_REPO_MANAGER_URL environment variable is not set")
	}
	// Convert data to JSON
	countTokenRequestJson, err := json.Marshal(countTokenRequestData)
	if err != nil {
		return 0, 0, fmt.Errorf("error marshaling JSON: %w", err)
	}

	tokenCountEmbedding, tokenCountInference, err := getTokenCount(fmt.Sprintf("%s/add-repository/", repoManagerURL), bytes.NewBuffer(countTokenRequestJson))
	if err != nil {
		return 0, 0, fmt.Errorf("error getting token count: %w", err)
	}

	return tokenCountEmbedding, tokenCountInference, nil
}

func AddRepository(codeURL, name string, apiKey *string, openAIAPIKey, repoManagerURL, llmModelBaseURL string, force bool, headCommitHash string, useMockLLM bool, amplificationLevel string, depthLevel int, llmThreads int, model string) (AddRepositoryResponse, error) {
	// Load config and ignore files first
	config, ignoreFiles, err := utils.LoadConfigAndIgnoreFiles()
	if err != nil {
		return AddRepositoryResponse{}, err
	}

	addRepositoryRequestData := map[string]interface{}{
		"codehost_url":        codeURL,
		"project_name":        name,
		"vcs_type":            "git",
		"api_key":             apiKey,
		"llm_model_api_key":   openAIAPIKey,
		"llm_model_base_url":  llmModelBaseURL,
		"llm_model":           model,
		"ignore_files":        ignoreFiles,
		"head":                headCommitHash,
		"use_mock_llm":        useMockLLM,
		"amplification_level": amplificationLevel,
		"depth_level":         depthLevel,
		"llm_threads":         llmThreads,
	}

	// Only add llm_threads if it's greater than 0
	if llmThreads > 0 {
		addRepositoryRequestData["llm_threads"] = llmThreads
	}

	// Convert data to JSON
	addRepositoryRequestJson, err := json.Marshal(addRepositoryRequestData)
	if err != nil {
		return AddRepositoryResponse{}, fmt.Errorf("error marshaling JSON: %w", err)
	}

	// Proceed with sending the POST request
	req, err := http.NewRequest("POST", fmt.Sprintf("%s/add-repository/", repoManagerURL), bytes.NewBuffer(addRepositoryRequestJson))
	if err != nil {
		return AddRepositoryResponse{}, fmt.Errorf("error creating request: %w", err)
	}

	// Set API Gateway headers if not blank
	if config.Environment.APIGatewayHostValue != "" {
		req.Header.Set(API_GATEWAY_HOST_KEY, config.Environment.APIGatewayHostValue)
	}
	req.Header.Set(CONTENT_TYPE_KEY, CONTENT_TYPE_VALUE)

	client := &http.Client{
		Timeout: 60 * time.Minute, // Increased to 60 minutes
	}
	resp, err := client.Do(req) // Use the client to execute the request
	if err != nil {
		return AddRepositoryResponse{}, fmt.Errorf("error sending request to add repository: %w", err)
	}
	defer resp.Body.Close()

	// Handle the response
	if resp.StatusCode != http.StatusOK {
		body, _ := ioutil.ReadAll(resp.Body)
		return AddRepositoryResponse{}, fmt.Errorf("error adding repository: %s", body)
	}
	// Successfully added the repository, decode the response into the defined struct
	var responseMessage AddRepositoryResponse
	if err := json.NewDecoder(resp.Body).Decode(&responseMessage); err != nil {
		return AddRepositoryResponse{}, fmt.Errorf("error decoding response: %w", err)
	}

	return responseMessage, nil

}

// FetchAndCheckoutBranch sends a request to fetch and checkout a branch.
func FetchAndCheckoutBranch(codeURL, name, branchName string, apiKey *string, modelAPIKey *string, modelBaseURL *string, model *string, force bool, headCommitHash string, useMockLLM bool, amplificationLevel string, depthLevel int, llmThreads int) (string, error) {
	config, ignoreFiles, err := utils.LoadConfigAndIgnoreFiles()
	if err != nil {
		return "", err
	}

	repoManagerURL := RepoManagerURL
	if repoManagerURL == "" {
		return "", fmt.Errorf("MACHTIANI_REPO_MANAGER_URL environment variable is not set")
	}

	// Initialize and start the spinner
	spinner := NewSpinnerController()
	spinner.Start()
	defer spinner.Stop() // Ensure spinner stops on exit

	fetchAndCheckoutBranchRequestData := map[string]interface{}{
		"codehost_url":        codeURL,
		"project_name":        name,
		"api_key":             apiKey,
		"llm_model_api_key":   modelAPIKey,
		"llm_model_base_url":  modelBaseURL,
		"llm_model":           model,
		"ignore_files":        ignoreFiles,
		"head":                headCommitHash,
		"use_mock_llm":        useMockLLM,
		"amplification_level": amplificationLevel,
		"depth_level":         depthLevel,
		"commit_oid":          headCommitHash,
	}

	// Only add branch_name to the request if it's provided and not empty

	if branchName != "" {
		fetchAndCheckoutBranchRequestData["branch_name"] = branchName
	}

	// Only add llm_threads if it's greater than 0
	if llmThreads > 0 {
		fetchAndCheckoutBranchRequestData["llm_threads"] = llmThreads
	}

	fetchAndCheckoutBranchRequestJson, err := json.Marshal(fetchAndCheckoutBranchRequestData)
	if err != nil {
		return "", fmt.Errorf("error marshaling JSON: %w", err)
	}

	req, err := http.NewRequest("POST", fmt.Sprintf("%s/fetch-and-checkout/", repoManagerURL), bytes.NewBuffer(fetchAndCheckoutBranchRequestJson))
	if err != nil {
		return "", fmt.Errorf("error creating request: %w", err)
	}

	// Set API Gateway headers if not blank
	if config.Environment.APIGatewayHostValue != "" {
		req.Header.Set(API_GATEWAY_HOST_KEY, config.Environment.APIGatewayHostValue)
	}
	req.Header.Set(CONTENT_TYPE_KEY, CONTENT_TYPE_VALUE)

	client := &http.Client{
		Timeout: 60 * time.Minute, // Increased to 60 minutes
	}
	resp, err := client.Do(req)
	if err != nil {
		// Detect connection errors and provide a helpful message
		if errors.Is(err, io.EOF) || strings.Contains(err.Error(), "connection refused") {
			return "", fmt.Errorf("could not connect to repository manager at %s. Is the service running? Underlying error: %w", repoManagerURL, err)
		}
		return "", fmt.Errorf("error making request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := ioutil.ReadAll(resp.Body)
		return "", fmt.Errorf("error: received status code %d from the server: %s", resp.StatusCode, body)
	}

	body, err := ioutil.ReadAll(resp.Body)
	if err != nil {
		return "", fmt.Errorf("error reading response body: %w", err)
	}

	type SyncResponse struct {
		Message     string `json:"message"`
		BranchName  string `json:"branch_name"`
		ProjectName string `json:"project_name"`
	}

	var syncResp SyncResponse
	if err := json.Unmarshal(body, &syncResp); err != nil {
		// Fallback to original format if parsing fails
		log.Printf("Error parsing sync response: %v", err)
		return fmt.Sprintf("Successfully synced the repository: %s.\nServer response: %s", name, string(body)), nil
	}

	repoName := extractRepoName(syncResp.ProjectName)
	formattedMessage := fmt.Sprintf("Successfully synced '%s' branch of %s to the chat service\n - service message: %s",
		syncResp.BranchName, repoName, syncResp.Message)

	return formattedMessage, nil
}

func DeleteStore(projectName string, codehostURL string, vcsType string, apiKey *string, repoManagerURL string, force bool) (DeleteStoreResponse, error) {
	config, _, err := utils.LoadConfigAndIgnoreFiles()
	if err != nil {
		return DeleteStoreResponse{}, err
	}

	if force || utils.ConfirmProceed() {
		// This uses the old simple spinner, which is fine for this context.
		done := make(chan bool)
		go utils.Spinner(done)
		defer func() { done <- true }()

		// Prepare the data to be sent in the request
		data := map[string]interface{}{
			"project_name": projectName,
			"codehost_url": codehostURL,
			"vcs_type":     vcsType,
			"api_key":      apiKey,
		}

		jsonData, err := json.Marshal(data)
		if err != nil {
			return DeleteStoreResponse{}, fmt.Errorf("error marshaling JSON: %w", err)
		}

		req, err := http.NewRequest("POST", fmt.Sprintf("%s/delete-store/", repoManagerURL), bytes.NewBuffer(jsonData))
		if err != nil {
			return DeleteStoreResponse{}, fmt.Errorf("error creating request: %w", err)
		}

		// Set API Gateway headers if not blank
		if config.Environment.APIGatewayHostValue != "" {
			req.Header.Set(API_GATEWAY_HOST_KEY, config.Environment.APIGatewayHostValue)
		}
		req.Header.Set(CONTENT_TYPE_KEY, CONTENT_TYPE_VALUE)

		client := &http.Client{
			Timeout: 60 * time.Minute, // Increased to 60 minutes
		}
		resp, err := client.Do(req)
		if err != nil {
			return DeleteStoreResponse{}, fmt.Errorf("error sending request to delete store: %w", err)
		}
		defer resp.Body.Close()

		if resp.StatusCode != http.StatusOK {
			body, _ := ioutil.ReadAll(resp.Body)
			return DeleteStoreResponse{}, fmt.Errorf("error deleting store: %s", body)
		}

		var responseMessage DeleteStoreResponse
		if err := json.NewDecoder(resp.Body).Decode(&responseMessage); err != nil {
			return DeleteStoreResponse{}, fmt.Errorf("error decoding response: %w", err)
		}

		return responseMessage, nil

	} else {
		abortedResponse := DeleteStoreResponse{
			Message: "Operation aborted by user",
		}

		return abortedResponse, nil
	}
}

func init() {
	rendererOnce.Do(func() {
		renderer, rendererErr = glamour.NewTermRenderer(
			glamour.WithAutoStyle(),
			glamour.WithPreservedNewLines(),
		)
		if rendererErr != nil {
			log.Fatalf("Error creating renderer: %v", rendererErr)
		}
	})
}

// GenerateResponse sends a chat request to the Machtiani API, with option to disable agent file retrieval when noCodex is true.
func GenerateResponse(
	prompt, project, mode, model, matchStrength string,
	agentModel string,
	force bool,
	headCommitHash string,
	noCodex bool,
	verbose bool,
) (*GenerateResponseResult, error) {
	config, ignoreFiles, err := utils.LoadConfigAndIgnoreFiles()
	if err != nil {
		log.Fatalf("Error loading config: %v", err)
	}

	codehostURL, err := utils.GetCodehostURLFromCurrentRepository()
	if err != nil {
		return nil, fmt.Errorf("failed to get codehost URL: %w", err)
	}

	payload := map[string]interface{}{
		"prompt":                   prompt,
		"project":                  project,
		"mode":                     mode,
		"model":                    model,
		"agent_model":              agentModel, // Add agent_model to payload
		"match_strength":           matchStrength,
		"llm_model_api_key":        config.Environment.ModelAPIKey,
		"llm_model_api_key_other":  config.Environment.ModelAPIKeyOther,
		"llm_model_base_url":       config.Environment.ModelBaseURL,
		"codehost_api_key":         config.Environment.CodeHostAPIKey,
		"codehost_url":             codehostURL,
		"head_commit_hash":         headCommitHash,
		"nocodex":                  noCodex,
		"ignore_files":             ignoreFiles,
		"llm_model_base_url_other": config.Environment.ModelBaseURLOther,
	}

	payloadBytes, err := json.Marshal(payload)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal JSON: %w", err)
	}

	endpoint := MachtianiURL
	if endpoint == "" {
		return nil, fmt.Errorf("MACHTIANI_URL environment variable is not set")
	}

	req, err := http.NewRequest("POST", fmt.Sprintf("%s/generate-response/", endpoint), bytes.NewBuffer(payloadBytes))
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}

	if config.Environment.APIGatewayHostValue != "" {
		req.Header.Set(API_GATEWAY_HOST_KEY, config.Environment.APIGatewayHostValue)
	}
	req.Header.Set(CONTENT_TYPE_KEY, CONTENT_TYPE_VALUE)

	sessionID := os.Getenv("MACHTIANI_SESSION_ID")
	if sessionID != "" {
		req.Header.Set("X-Session-ID", sessionID)
	}

	answerOnlyMode := mode == "answer-only"

	// --- Header Calculation ---
	// Calculate the header text first, so it's ready when the progress UI is torn down.
	var header string
	if answerOnlyMode {
		header = prompt
	} else if strings.HasPrefix(strings.TrimSpace(prompt), "# User") {
		header = fmt.Sprintf("%s\n# Assistant\n\n", prompt)
	} else {
		header = fmt.Sprintf("# User\n\n%s\n\n# Assistant\n\n", prompt)
	}

	// --- Progress UI Logic ---
	var spinner *SpinnerController
	var stopPolling context.CancelFunc

	if !answerOnlyMode && sessionID != "" {
		spinner = NewSpinnerController()
		spinner = NewSpinnerControllerWithVerbose(verbose)
		spinner.Start()

		var pollCtx context.Context
		pollCtx, stopPolling = context.WithCancel(context.Background())

		go pollForProgress(pollCtx, sessionID, spinner, verbose)
	}

	cleanupProgressUI := func() {
		if stopPolling != nil {
			stopPolling()
			stopPolling = nil
		}
		if spinner != nil {
			spinner.Stop()
			spinner = nil
		}
	}
	defer cleanupProgressUI()
	// --- END: Progress UI Logic ---

	client := &http.Client{
		Timeout: 60 * time.Minute,
	}

	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("failed to make API request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusUnprocessableEntity {
		body, _ := ioutil.ReadAll(resp.Body)
		return nil, fmt.Errorf("unprocessable entity: %s", body)
	}

	var completeResponse, rawResponse, answerOnlyBuffer strings.Builder
	var retrievedFilePaths []string
	var tokenBuffer, codeBlockBuffer bytes.Buffer
	var inCodeBlock bool
	updateContentResponse := make(map[string]UpdateFileContent) // Keep for backward compatibility
	decoder := json.NewDecoder(resp.Body)

	var newFilesResult *NewFilesData
	var patchedFilesResult *PatchedFilesData
	var progressUICleaned bool

	for {
		var chunk map[string]interface{}
		if err := decoder.Decode(&chunk); err == io.EOF {
			break
		} else if err != nil {
			return nil, fmt.Errorf("failed to decode JSON response: %w", err)
		}

		if errMsg, ok := chunk["error"].(string); ok {
			cleanupProgressUI()
			return nil, fmt.Errorf("API error: %s", errMsg)
		}

		if token, ok := chunk["token"].(string); ok {
			if !progressUICleaned {
				cleanupProgressUI()
				progressUICleaned = true

				if !answerOnlyMode {
					if err := renderMarkdown(header); err != nil {
						return nil, fmt.Errorf("failed to render header: %w", err)
					}
				}
				completeResponse.WriteString(header)
				rawResponse.WriteString(header)
			}
			tokenBuffer.WriteString(token)
			rawResponse.WriteString(token)

			if answerOnlyMode {
				answerOnlyBuffer.WriteString(token)
				continue
			}

			content := tokenBuffer.String()
			for {
				idx := strings.Index(content, "\n\n")
				if idx == -1 {
					break
				}
				block := content[:idx]
				remainingContent := content[idx+2:]
				lines := strings.Split(block, "\n")
				for _, line := range lines {
					trimmedLine := strings.TrimSpace(line)
					if strings.HasPrefix(trimmedLine, "```") {
						inCodeBlock = !inCodeBlock
					}
				}
				if inCodeBlock {
					codeBlockBuffer.WriteString(block + "\n\n")
				} else {
					if codeBlockBuffer.Len() > 0 {
						codeBlockBuffer.WriteString(block)
						if err := renderMarkdown(codeBlockBuffer.String()); err != nil {
							log.Printf("Error rendering code block: %v", err)
						}
						completeResponse.WriteString(codeBlockBuffer.String())
						codeBlockBuffer.Reset()
					} else {
						trimmedBlock := strings.TrimRight(block, "\r\n")
						trimmedBlock = strings.ReplaceAll(trimmedBlock, "\r\n", "\n")
						if err := renderMarkdown(trimmedBlock); err != nil {
							log.Printf("Error rendering block: %v", err)
						}
						completeResponse.WriteString(trimmedBlock)
					}
				}
				content = remainingContent
				tokenBuffer.Reset()
				tokenBuffer.WriteString(content)
			}
		}

		if paths, ok := chunk["retrieved_file_paths"]; ok {
			pathsJSON, err := json.Marshal(paths)
			if err != nil {
				return nil, fmt.Errorf("failed to marshal retrieved_file_paths: %w", err)
			}
			pathList := []string{}
			if err := json.Unmarshal(pathsJSON, &pathList); err != nil {
				return nil, fmt.Errorf("failed to unmarshal retrieved_file_paths: %w", err)
			}
			retrievedFilePaths = append(retrievedFilePaths, pathList...)
		}

		// Handle patched_files from agent-patch
		if patchedData, ok := chunk["patched_files"]; ok {
			patchedJSON, err := json.Marshal(patchedData)
			if err != nil {
				log.Printf("Error marshaling patched_files: %v", err)
				continue
			}
			var patchedFiles PatchedFilesData
			if err := json.Unmarshal(patchedJSON, &patchedFiles); err != nil {
				log.Printf("Error unmarshaling patched_files: %v", err)
				continue
			}
			patchedFilesResult = &patchedFiles
		}

		// Keep backward compatibility with updated_file_contents
		if updated, ok := chunk["updated_file_contents"]; ok {
			updatedJSON, err := json.Marshal(updated)
			if err != nil {
				return nil, fmt.Errorf("failed to marshal updated_file_contents: %w", err)
			}
			updatedMap := map[string]UpdateFileContent{}
			if err := json.Unmarshal(updatedJSON, &updatedMap); err != nil {
				return nil, fmt.Errorf("failed to unmarshal updated_file_contents: %w", err)
			}
			for path, updateObj := range updatedMap {
				updateContentResponse[path] = updateObj
			}
		}

		if newFilesData, ok := chunk["new_files"]; ok {
			newFilesJSON, err := json.Marshal(newFilesData)
			if err != nil {
				log.Printf("Error marshaling new_files: %v", err)
				continue
			}
			var newFiles NewFilesData
			if err := json.Unmarshal(newFilesJSON, &newFiles); err != nil {
				log.Printf("Error unmarshaling new_files: %v", err)
				continue
			}
			if len(newFiles.NewFilePaths) == 0 && len(newFiles.NewContent) > 0 {
				for path := range newFiles.NewContent {
					newFiles.NewFilePaths = append(newFiles.NewFilePaths, path)
				}
			}
			newFilesResult = &newFiles
		}
	}

	if tokenBuffer.Len() > 0 {
		remainingContent := tokenBuffer.String()
		trimmedContent := strings.TrimRight(remainingContent, "\r\n")
		trimmedContent = strings.ReplaceAll(trimmedContent, "\r\n", "\n")
		if !answerOnlyMode {
			if err := renderMarkdown(trimmedContent); err != nil {
				log.Printf("Error rendering remaining content: %v", err)
			}
		}
		completeResponse.WriteString(trimmedContent)
	}

	if len(retrievedFilePaths) > 0 {
		retrievedFilePathsMarkdown := "\n\n---\n\n# Retrieved File Paths\n\n"
		for _, path := range retrievedFilePaths {
			retrievedFilePathsMarkdown += fmt.Sprintf("- %s\n", path)
		}
		completeResponse.WriteString(retrievedFilePathsMarkdown)
		rawResponse.WriteString(retrievedFilePathsMarkdown)
		if answerOnlyMode {
			answerOnlyBuffer.WriteString(retrievedFilePathsMarkdown)
		} else {
			if err := renderMarkdown(retrievedFilePathsMarkdown); err != nil {
				log.Printf("Error rendering retrieved file paths: %v", err)
			}
		}
	}

	if answerOnlyMode {
		fmt.Println(answerOnlyBuffer.String())
	}

	result := &GenerateResponseResult{
		LlmModelResponse:      completeResponse.String(),
		RawResponse:           rawResponse.String(),
		RetrievedFilePaths:    retrievedFilePaths,
		UpdateContentResponse: updateContentResponse, // Keep for backward compatibility
		PatchedFiles:          patchedFilesResult,
		HeadCommitHash:        headCommitHash,
		NewFiles:              newFilesResult,
	}

	_, err = result.WriteNewFiles()
	if err != nil {
		log.Printf("Error writing patch files for new files: %v", err)
	}

	return result, nil
}

func renderMarkdown(content string) error {
	if rendererErr != nil {
		return rendererErr
	}
	out, err := renderer.Render(content)
	if err != nil {
		log.Printf("Error rendering Markdown: %v", err)
		return err
	}
	out = strings.TrimRight(out, "\n")
	fmt.Print(out)
	return nil
}

func getTokenCount(endpoint string, buffer *bytes.Buffer) (int, int, error) {
	config, err := utils.LoadConfig()
	if err != nil {
		log.Fatalf("Error loading config: %v", err)
	}

	req, err := http.NewRequest("POST", fmt.Sprintf("%stoken-count", endpoint), buffer)
	if err != nil {
		return 0, 0, fmt.Errorf("error creating request: %w", err)
	}
	req.Header.Set(CONTENT_TYPE_KEY, CONTENT_TYPE_VALUE)

	if config.Environment.APIGatewayHostValue != "" {
		req.Header.Set(API_GATEWAY_HOST_KEY, config.Environment.APIGatewayHostValue)
	}

	client := &http.Client{Timeout: 60 * time.Minute}
	response, err := client.Do(req)
	if err != nil {
		return 0, 0, fmt.Errorf("error sending request to token count endpoint: %w", err)
	}
	defer response.Body.Close()

	if response.StatusCode != http.StatusOK {
		body, _ := ioutil.ReadAll(response.Body)
		return 0, 0, fmt.Errorf("error getting token count: %s", body)
	}

	body, err := ioutil.ReadAll(response.Body)
	if err != nil {
		return 0, 0, fmt.Errorf("error reading response body: %v", err)
	}

	var tokenCountResponse LoadResponse
	if err := json.Unmarshal(body, &tokenCountResponse); err != nil {
		return 0, 0, fmt.Errorf("error decoding response: %w", err)
	}

	return tokenCountResponse.EmbeddingTokens, tokenCountResponse.InferenceTokens, nil
}

func CheckStatus(codehostURL string) (StatusResponse, error) {
	config, _, err := utils.LoadConfigAndIgnoreFiles()
	if err != nil {
		return StatusResponse{}, err
	}

	repoManagerURL := RepoManagerURL
	if repoManagerURL == "" {
		return StatusResponse{}, fmt.Errorf("MACHTIANI_REPO_MANAGER_URL environment variable is not set")
	}

	statusURL := fmt.Sprintf("%s/status?codehost_url=%s", repoManagerURL, codehostURL)

	req, err := http.NewRequest("GET", statusURL, nil)
	if err != nil {
		return StatusResponse{}, fmt.Errorf("error creating request: %w", err)
	}
	if config.Environment.APIGatewayHostValue != "" {
		req.Header.Set(API_GATEWAY_HOST_KEY, config.Environment.APIGatewayHostValue)
	}
	req.Header.Set(CONTENT_TYPE_KEY, CONTENT_TYPE_VALUE)

	client := &http.Client{Timeout: 60 * time.Minute}
	resp, err := client.Do(req)
	if err != nil {
		return StatusResponse{}, fmt.Errorf("error sending request to status endpoint: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := ioutil.ReadAll(resp.Body)
		return StatusResponse{}, fmt.Errorf("error checking status: %s", body)
	}

	var statusResponse StatusResponse
	if err := json.NewDecoder(resp.Body).Decode(&statusResponse); err != nil {
		return StatusResponse{}, fmt.Errorf("error decoding status response: %w", err)
	}

	return statusResponse, nil
}

func GetInstallInfo() (bool, string, error) {
	config, _, err := utils.LoadConfigAndIgnoreFiles()
	if err != nil {
		return false, "", fmt.Errorf("error loading config: %w", err)
	}

	machtianiURL := MachtianiURL
	if machtianiURL == "" {
		return false, "", fmt.Errorf("MACHTIANI_URL environment variable is not set")
	}
	endpoint := fmt.Sprintf("%s/get-head-oid", machtianiURL)

	req, err := http.NewRequest("GET", endpoint, nil)
	if err != nil {
		return false, "", fmt.Errorf("error creating request: %w", err)
	}
	if config.Environment.APIGatewayHostValue != "" {
		req.Header.Set(API_GATEWAY_HOST_KEY, config.Environment.APIGatewayHostValue)
	}
	req.Header.Set(CONTENT_TYPE_KEY, CONTENT_TYPE_VALUE)
	client := &http.Client{
		Timeout: 20 * time.Second,
	}

	resp, err := client.Do(req)
	if err != nil {
		return false, "", fmt.Errorf("error sending request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := ioutil.ReadAll(resp.Body)
		return false, "", fmt.Errorf("error: received status code %d from the server: %s", resp.StatusCode, body)
	}

	var response map[string]string
	if err := json.NewDecoder(resp.Body).Decode(&response); err != nil {
		return false, "", fmt.Errorf("error decoding response: %w", err)
	}

	returnedHeadOID, ok := response["head_oid"]
	if !ok {
		return false, "", fmt.Errorf("response does not contain head_oid")
	}
	message, ok := response["message"]
	if !ok {
		return false, "", fmt.Errorf("response does not contain message")
	}

	return returnedHeadOID == HeadOID, message, nil
}

// WritePatchesToFiles writes patch files from the PatchedFilesData to the .machtiani/patches directory
func (res *GenerateResponseResult) WritePatchesToFiles() error {
	if res.PatchedFiles == nil || len(res.PatchedFiles.PatchedFiles) == 0 {
		return nil
	}

	var outputBuffer bytes.Buffer
	outputBuffer.WriteString("\n")
	timestamp := time.Now().Format("20060102_150405")

	patchesDir := ".machtiani/patches"
	if err := utils.EnsureDirExists(patchesDir); err != nil {
		return fmt.Errorf("failed to ensure patches directory exists: %w", err)
	}

	for _, patchFile := range res.PatchedFiles.PatchedFiles {
		if len(strings.TrimSpace(patchFile.Content)) == 0 {
			outputBuffer.WriteString(fmt.Sprintf("Skipping patch creation for %s as patch content is empty.\n", patchFile.OriginalFile))
			continue
		}

		safeFilename := strings.ReplaceAll(patchFile.OriginalFile, "/", "_")
		safeFilename = strings.ReplaceAll(safeFilename, ":", "_")
		patchFileName := fmt.Sprintf("%s_%s.patch", safeFilename, timestamp)

		fullPatchPath := filepath.Join(patchesDir, patchFileName)
		err := ioutil.WriteFile(fullPatchPath, []byte(patchFile.Content), 0644)
		if err != nil {
			log.Printf("Failed to write patch to file %s: %v", fullPatchPath, err)
			outputBuffer.WriteString(fmt.Sprintf("Error writing patch for %s to %s\n", patchFile.OriginalFile, fullPatchPath))
		} else {
			outputBuffer.WriteString(fmt.Sprintf("Wrote patch for %s to %s\n", patchFile.OriginalFile, fullPatchPath))
		}
	}

	if outputBuffer.Len() > 1 {
		fmt.Print(outputBuffer.String())
	}
	return nil
}

// WritePatchToFile - kept for backward compatibility with old updated_file_contents format
func (res *GenerateResponseResult) WritePatchToFile() error {
	// If we have new patched files data, use that
	if res.PatchedFiles != nil && len(res.PatchedFiles.PatchedFiles) > 0 {
		return res.WritePatchesToFiles()
	}

	// Fallback to old format for backward compatibility
	if len(res.UpdateContentResponse) == 0 {
		return nil
	}
	var outputBuffer bytes.Buffer
	outputBuffer.WriteString("\n")
	timestamp := time.Now().Format("20060102_150405")
	for filename, update := range res.UpdateContentResponse {
		skip := false
		for _, errMsg := range update.Errors {
			if len(strings.TrimSpace(errMsg)) > 0 {
				log.Printf("Error received for file %s: %s", filename, errMsg)
				skip = true
				break
			}
		}
		if skip {
			outputBuffer.WriteString(fmt.Sprintf("Skipping patch creation for %s due to errors during generation.\n", filename))
			continue
		}
		if len(strings.TrimSpace(update.UpdatedContent)) == 0 {
			outputBuffer.WriteString(fmt.Sprintf("Skipping patch creation for %s as updated content is empty.\n", filename))
			continue
		}
		safeFilename := strings.ReplaceAll(filename, "/", "_")
		safeFilename = strings.ReplaceAll(safeFilename, ":", "_")
		patchFileName := fmt.Sprintf("%s_%s.patch", safeFilename, timestamp)
		patchesDir := ".machtiani/patches"
		if err := utils.EnsureDirExists(patchesDir); err != nil {
			return fmt.Errorf("failed to ensure patches directory exists: %w", err)
		}
		fullPatchPath := fmt.Sprintf("%s/%s", patchesDir, patchFileName)
		err := ioutil.WriteFile(fullPatchPath, []byte(update.UpdatedContent), 0644)
		if err != nil {
			log.Printf("Failed to write patch to file %s: %v", fullPatchPath, err)
			outputBuffer.WriteString(fmt.Sprintf("Error writing patch for %s to %s\n", filename, fullPatchPath))
		} else {
			outputBuffer.WriteString(fmt.Sprintf("Wrote patch for %s to %s\n", filename, fullPatchPath))
		}
	}
	if outputBuffer.Len() > 1 {
		fmt.Print(outputBuffer.String())
	}
	return nil
}

type SpinnerController struct {
	done           chan bool
	spinning       bool
	mutex          sync.Mutex
	messages       []string
	completedSteps map[string]bool
	startedSteps   map[string]bool
	linesDrawn     int
	verbose        bool
	debugMessages  []string
}

func NewSpinnerControllerWithVerbose(verbose bool) *SpinnerController {
	return &SpinnerController{
		done:           make(chan bool),
		messages:       []string{},
		completedSteps: make(map[string]bool),
		startedSteps:   make(map[string]bool),
		verbose:        verbose,
		debugMessages:  []string{},
	}
}

func NewSpinnerController() *SpinnerController {
	return NewSpinnerControllerWithVerbose(false)
}

func (s *SpinnerController) AddDebugMessage(message string) {
	s.mutex.Lock()
	defer s.mutex.Unlock()
	if s.verbose {
		s.debugMessages = append(s.debugMessages, message)
		// Keep only the last 10 debug messages to avoid overwhelming output
		if len(s.debugMessages) > 10 {
			s.debugMessages = s.debugMessages[len(s.debugMessages)-10:]
		}
	}
}

func (s *SpinnerController) UpdateProgress(stepName string, completed bool) {
	s.mutex.Lock()
	defer s.mutex.Unlock()

	if completed {
		s.completedSteps[stepName] = true
		// When a step completes, it's also implicitly started
		s.startedSteps[stepName] = true
	} else {
		// This is a "started" event
		s.startedSteps[stepName] = true
	}

	// Update messages based on progress
	s.updateProgressMessages()
}

func (s *SpinnerController) updateProgressMessages() {
	// Define the ordered steps and their display names
	orderedSteps := []struct {
		key     string
		display string
	}{
		{"planning", "Answering"},
		{"permissions", "Checking permissions"},
		{"inferring", "Inferring relevant files"},
		{"categorizing", "Categorizing files"},
		{"filtering", "Filtering files"},
		{"consulting", "Consulting agent"},
		{"integrating", "Integrating agent results"},
		{"generating", "Generating response"},
	}

	var newMessages []string
	for _, step := range orderedSteps {
		if s.completedSteps[step.key] {
			newMessages = append(newMessages, "✓ "+step.display)
		} else if s.stepHasStarted(step.key) {
			if step.display == "Answering" {
				newMessages = append(newMessages, step.display)
			} else {
				newMessages = append(newMessages, "  "+step.display)
			}
		}
		// If neither completed nor started, the step won't show at all
	}

	s.messages = newMessages
}

func (s *SpinnerController) stepHasStarted(stepKey string) bool {
	// Only show steps that have been explicitly marked as started
	return s.startedSteps[stepKey]
}

func (s *SpinnerController) Start() {
	s.mutex.Lock()
	defer s.mutex.Unlock()
	if !s.spinning {
		s.spinning = true
		s.done = make(chan bool)
		go s.renderLoop()
	}
}

func (s *SpinnerController) UpdateMessages(newMessages []string) {
	s.mutex.Lock()
	defer s.mutex.Unlock()
	if reflect.DeepEqual(s.messages, newMessages) {
		return
	}
	s.messages = newMessages
}

func (s *SpinnerController) Stop() {
	s.mutex.Lock()
	defer s.mutex.Unlock()
	if s.spinning {
		close(s.done)
		s.spinning = false
	}
}

func (s *SpinnerController) renderLoop() {
	spinnerChars := []rune{'⠋', '⠙', '⠹', '⠸', '⠼', '⠴', '⠦', '⠧', '⠇', '⠏'}
	i := 0
	for {
		select {
		case <-s.done:
			s.clearLines()
			return
		case <-time.After(100 * time.Millisecond):
			s.mutex.Lock()
			s.clearLines()

			fmt.Printf("\r%c", spinnerChars[i])
			s.linesDrawn = 1

			// Show progress messages
			for _, msg := range s.messages {
				fmt.Printf("  %s\n", msg)
				s.linesDrawn++
			}

			// Show debug messages if verbose mode is enabled
			if s.verbose && len(s.debugMessages) > 0 {
				for _, debugMsg := range s.debugMessages {
					fmt.Printf("Debug: %s\n", debugMsg)
					s.linesDrawn++
				}
			}

			i = (i + 1) % len(spinnerChars)
			s.mutex.Unlock()
		}
	}
}

func (s *SpinnerController) clearLines() {
	if s.linesDrawn > 0 {
		// Move to beginning of current line and clear each line we drew
		fmt.Print("\r") // Move to beginning of current line
		for i := 0; i < s.linesDrawn; i++ {
			if i > 0 {
				fmt.Print("\033[1A") // Move up one line only after the first iteration
			}
			fmt.Print("\033[2K") // Clear entire line
		}
		s.linesDrawn = 0
	}
}

var progressMessageMap = map[string]string{
	"generate_response":        "Answering",
	"pull_access_check":        "Checking permissions",
	"infer_file_call":          "Inferring relevant files",
	"infer_file_results":       "Categorizing files",
	"retrieve_file_contents":   "Reading file contents",
	"filter_relevance":         "Filtering files",
	"llm_request_final_answer": "Generating response",
	"agent_codex_call":         "Consulting agent",
	"agent_patch_operations":   "Processing patches", // Updated to reflect agent-patch operations
}

func pollForProgress(ctx context.Context, sessionID string, spinner *SpinnerController, verbose bool) {
	loggingClient := logging.NewLoggingClient("")

	// Map log types to progress steps
	logTypeToStep := map[string]string{
		"generate_response":        "planning",
		"pull_access_check":        "permissions",
		"infer_file_call":          "inferring",
		"infer_file_results":       "categorizing",
		"filter_relevance":         "filtering",
		"filter_relevance_call":    "filtering",
		"agent_codex_call":         "consulting",
		"agent_patch_operations":   "integrating", // Updated to match agent-patch operations
		"llm_request_final_answer": "generating",
	}

	ticker := time.NewTicker(300 * time.Millisecond)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			logsResponse, err := loggingClient.GetSessionLogs(sessionID, "", "", "")
			if err != nil {
				if verbose {
					spinner.AddDebugMessage(fmt.Sprintf("Error getting logs: %v", err))
				}
				time.Sleep(2000 * time.Millisecond)
				continue
			}

			if len(logsResponse.Logs) > 0 && verbose {
				spinner.AddDebugMessage(fmt.Sprintf("Got %d logs", len(logsResponse.Logs)))
			}

			// Process logs and update progress
			for _, log := range logsResponse.Logs {
				if verbose {
					spinner.AddDebugMessage(fmt.Sprintf("Log type=%s status=%s", log.Type, log.Status))
				}

				// Map log types to progress steps
				if stepName, exists := logTypeToStep[log.Type]; exists {
					if log.Status == "completed" {
						spinner.UpdateProgress(stepName, true)
					} else if log.Status == "started" {
						spinner.UpdateProgress(stepName, false)
					}
				}
			}

			time.Sleep(2000 * time.Millisecond)
		}
	}
}

func (res *GenerateResponseResult) WriteNewFiles() (string, error) {
	var outputBuffer bytes.Buffer
	var filesWritten int
	var filesSkipped int

	if res.NewFiles == nil || len(res.NewFiles.NewContent) == 0 {
		return "", nil // No data or content, no output, no error.
	}

	outputBuffer.WriteString(fmt.Sprintf("Creating %d suggested new files...\n", len(res.NewFiles.NewContent)))

	for path, content := range res.NewFiles.NewContent {
		if strings.TrimSpace(content) == "" {
			outputBuffer.WriteString(fmt.Sprintf("- %s (skipped - empty content)\n", path))
			filesSkipped++
			continue
		}

		if _, err := os.Stat(path); err == nil {
			outputBuffer.WriteString(fmt.Sprintf("- %s (skipped - file already exists)\n", path))
			filesSkipped++
			continue
		} else if !os.IsNotExist(err) {
			log.Printf("Error checking file %s: %v", path, err)
			outputBuffer.WriteString(fmt.Sprintf("- %s (error checking existence: %v)\n", path, err))
			continue
		}
		dir := filepath.Dir(path)
		if err := os.MkdirAll(dir, 0755); err != nil {
			log.Printf("Error creating directories for %s: %v", path, err)
			outputBuffer.WriteString(fmt.Sprintf("- %s (error creating directories: %v)\n", path, err))
			continue
		}
		if err := ioutil.WriteFile(path, []byte(content), 0644); err != nil {
			log.Printf("Error writing file %s: %v", path, err)
			outputBuffer.WriteString(fmt.Sprintf("- %s (error writing file: %v)\n", path, err))
			continue
		}
		outputBuffer.WriteString(fmt.Sprintf("- Created %s\n", path))
		filesWritten++
	}

	// Add the final summary line
	outputBuffer.WriteString(
		fmt.Sprintf("\nNew files processing complete - %d written, %d skipped\n", filesWritten, filesSkipped),
	)
	return outputBuffer.String(), nil
}

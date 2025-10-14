package logging

import (
	"encoding/json"
	"fmt"
	"io/ioutil"
	"net/http"
	"net/url"
	"time"
)

// LogEntry represents a single log entry from the logging service
type LogEntry struct {
	SessionID   string                 `json:"session_id"`
	Project     string                 `json:"project"`
	ServiceName string                 `json:"service_name"`
	Type        string                 `json:"type"`
	ActionID    string                 `json:"action_id"`
	Status      string                 `json:"status"`
	StartTime   string                 `json:"start_time"`
	EndTime     string                 `json:"end_time"`
	Messages    []string               `json:"messages"`
	Errors      []string               `json:"errors"`
	Metadata    map[string]interface{} `json:"metadata"`
}

// SessionLogsResponse represents the response from the session logs endpoint
type SessionLogsResponse struct {
	SessionID string     `json:"session_id"`
	Logs      []LogEntry `json:"logs"`
}

// ActiveSessionsResponse represents the response from the active sessions endpoint
type ActiveSessionsResponse struct {
	ActiveSessions []string `json:"active_sessions"`
}

// HealthResponse represents the response from the health endpoint
type HealthResponse struct {
	Status    string `json:"status"`
	Timestamp string `json:"timestamp"`
}

// LoggingClient handles communication with the machtiani-logging service
type LoggingClient struct {
	BaseURL    string
	HTTPClient *http.Client
}

func NewLoggingClient(baseURL string) *LoggingClient {
	if baseURL == "" {
		baseURL = "http://localhost:5073" // Use localhost instead of machtiani-logging
	}

	return &LoggingClient{
		BaseURL: baseURL,
		HTTPClient: &http.Client{
			Timeout: 30 * time.Second,
		},
	}
}

// GetSessionLogs retrieves logs for a specific session with optional filtering
func (c *LoggingClient) GetSessionLogs(sessionID string, logType, status, actionID string) (*SessionLogsResponse, error) {
	if sessionID == "" {
		return nil, fmt.Errorf("session ID is required")
	}

	// Build URL with query parameters
	endpoint := fmt.Sprintf("%s/logs/session/%s", c.BaseURL, url.PathEscape(sessionID))
	reqURL, err := url.Parse(endpoint)
	if err != nil {
		return nil, fmt.Errorf("failed to parse URL: %w", err)
	}

	// Add query parameters for filtering
	query := reqURL.Query()
	if logType != "" {
		query.Set("type", logType)
	}
	if status != "" {
		query.Set("status", status)
	}
	if actionID != "" {
		query.Set("action_id", actionID)
	}
	reqURL.RawQuery = query.Encode()

	// Make GET request
	resp, err := c.HTTPClient.Get(reqURL.String())
	if err != nil {
		return nil, fmt.Errorf("failed to make request: %w", err)
	}
	defer resp.Body.Close()

	// Read response body
	body, err := ioutil.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("failed to read response body: %w", err)
	}

	// Handle non-200 status codes
	if resp.StatusCode != http.StatusOK {
		if resp.StatusCode == http.StatusNotFound {
			return nil, fmt.Errorf("session not found or not in active memory")
		}
		return nil, fmt.Errorf("request failed with status %d: %s", resp.StatusCode, string(body))
	}

	// Parse JSON response
	var sessionLogs SessionLogsResponse
	if err := json.Unmarshal(body, &sessionLogs); err != nil {
		return nil, fmt.Errorf("failed to parse JSON response: %w", err)
	}

	return &sessionLogs, nil
}

// GetActiveSessions retrieves the list of active session IDs
func (c *LoggingClient) GetActiveSessions() (*ActiveSessionsResponse, error) {
	endpoint := fmt.Sprintf("%s/logs/sessions/active", c.BaseURL)

	resp, err := c.HTTPClient.Get(endpoint)
	if err != nil {
		return nil, fmt.Errorf("failed to make request: %w", err)
	}
	defer resp.Body.Close()

	body, err := ioutil.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("failed to read response body: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("request failed with status %d: %s", resp.StatusCode, string(body))
	}

	var activeSessions ActiveSessionsResponse
	if err := json.Unmarshal(body, &activeSessions); err != nil {
		return nil, fmt.Errorf("failed to parse JSON response: %w", err)
	}

	return &activeSessions, nil
}

// HealthCheck checks if the logging service is healthy
func (c *LoggingClient) HealthCheck() (*HealthResponse, error) {
	endpoint := fmt.Sprintf("%s/health", c.BaseURL)

	resp, err := c.HTTPClient.Get(endpoint)
	if err != nil {
		return nil, fmt.Errorf("failed to make request: %w", err)
	}
	defer resp.Body.Close()

	body, err := ioutil.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("failed to read response body: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("health check failed with status %d: %s", resp.StatusCode, string(body))
	}

	var health HealthResponse
	if err := json.Unmarshal(body, &health); err != nil {
		return nil, fmt.Errorf("failed to parse JSON response: %w", err)
	}

	return &health, nil
}

// FilterLogsByType filters a slice of log entries by type
func FilterLogsByType(logs []LogEntry, logType string) []LogEntry {
	var filtered []LogEntry
	for _, log := range logs {
		if log.Type == logType {
			filtered = append(filtered, log)
		}
	}
	return filtered
}

// FilterLogsByStatus filters a slice of log entries by status
func FilterLogsByStatus(logs []LogEntry, status string) []LogEntry {
	var filtered []LogEntry
	for _, log := range logs {
		if log.Status == status {
			filtered = append(filtered, log)
		}
	}
	return filtered
}

// FilterLogsByActionID filters a slice of log entries by action ID
func FilterLogsByActionID(logs []LogEntry, actionID string) []LogEntry {
	var filtered []LogEntry
	for _, log := range logs {
		if log.ActionID == actionID {
			filtered = append(filtered, log)
		}
	}
	return filtered
}

// GetLogsByType is a convenience method that gets session logs filtered by type
func (c *LoggingClient) GetLogsByType(sessionID, logType string) ([]LogEntry, error) {
	response, err := c.GetSessionLogs(sessionID, logType, "", "")
	if err != nil {
		return nil, err
	}
	return response.Logs, nil
}

// GetLogsByStatus is a convenience method that gets session logs filtered by status
func (c *LoggingClient) GetLogsByStatus(sessionID, status string) ([]LogEntry, error) {
	response, err := c.GetSessionLogs(sessionID, "", status, "")
	if err != nil {
		return nil, err
	}
	return response.Logs, nil
}

// GetLogsByActionID is a convenience method that gets session logs filtered by action ID
func (c *LoggingClient) GetLogsByActionID(sessionID, actionID string) ([]LogEntry, error) {
	response, err := c.GetSessionLogs(sessionID, "", "", actionID)
	if err != nil {
		return nil, err
	}
	return response.Logs, nil
}

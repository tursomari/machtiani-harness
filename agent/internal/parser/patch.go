package parser

// ExtractPatchJSONPayload scans a planner response body and extracts the outermost
// JSON object. It tolerates leading/trailing text or code fences and returns the
// raw bytes of the JSON object. Returns error if none found or invalid JSON.
func ExtractPatchJSONPayload(body string) ([]byte, error) {
	return ExtractJSONObjectPayload(body)
}

# Obstacle Detection and Handling

This document describes how to detect and handle obstacles (auth dialogs, captchas, login walls) from Skyvern task responses.

## Response Shapes

### HTTP Basic Auth Failure

When the agent encounters an HTTP basic auth dialog it cannot satisfy, the response shape is:

```json
{
  "status": "failed",
  "failure_reason": "Failed to navigate to URL. net::ERR_INVALID_AUTH_CREDENTIALS",
  "failure_category": [
    {
      "category": "NAVIGATION_FAILURE",
      "confidence_float": 0.9
    }
  ]
}
```

### Other Failure Categories

Additional failure categories may appear for different obstacle types (e.g., captchas, bot detection, login walls). The `failure_category` array can contain multiple entries, each with a `category` string and a `confidence_float` between 0 and 1.

### Completed Tasks with No Extracted Data

A task may have `"status": "completed"` but `extracted_information` may be `null`. This occurs when the request did not include a `data_extraction_schema` or `data_extraction_goal` field — the agent successfully performed the navigation steps but had no schema to extract structured data against. In these cases the task result still contains the raw action history and screenshots.

## Recommended Workflow

When an obstacle is detected:

1. The **shell-agent** should report the obstacle to the planner, including:
   - The target URL that triggered the obstacle
   - The `failure_category` entries (category and confidence)
   - The `failure_reason` string
   - Whether the obstacle appears to be auth-related, captcha-based, or a login wall

2. The agent should **not** attempt to bypass the obstacle automatically. Do not try credential stuffing, captcha solving, or scripted workarounds.

3. The **planner** should surface the obstacle to the **user** for manual clearance. This aligns with Skyvern's self-hosted captcha handling guidance and the Take Control feature, which allows a human operator to intervene on the host machine and resolve the obstacle (e.g., completing a login, solving a captcha, or dismissing a dialog).

### Self-Hosted Captcha Handling

As documented in `third_party/skyvern/docs/`, self-hosted deployments do not include automatic captcha solving. When a captcha or similar obstacle is encountered, the Skyvern agent pauses and waits for manual intervention. The user can use the Take Control feature to interact with the browser directly and resume the task once the obstacle is cleared.

package discovery

import (
	"bytes"
	"fmt"
	"strconv"
)

type outputBlockOptions struct {
	header            string
	footer            string
	truncationMessage string
	maxLines          int
}

type outputBlockResult struct {
	formatted     string
	truncated     bool
	linesIncluded int
}

func formatOutputBlock(lines []string, opts outputBlockOptions, truncated bool) outputBlockResult {
	var body bytes.Buffer
	for i, line := range lines {
		if i > 0 {
			body.WriteByte('\n')
		}
		body.WriteString(line)
	}

	var buf bytes.Buffer
	buf.WriteString(opts.header)
	if body.Len() > 0 {
		buf.Write(body.Bytes())
		buf.WriteByte('\n')
	}
	if truncated {
		buf.WriteString(opts.truncationMessage)
		if opts.truncationMessage != "" && opts.truncationMessage[len(opts.truncationMessage)-1] != '\n' {
			buf.WriteByte('\n')
		}
	}
	buf.WriteString(opts.footer)

	return outputBlockResult{
		formatted:     buf.String(),
		truncated:     truncated,
		linesIncluded: len(lines),
	}
}

func formatNumberedLines(lines []string, startLine int) []string {
	if len(lines) == 0 {
		return nil
	}
	width := len(strconv.Itoa(startLine + len(lines) - 1))
	if width < 2 {
		width = 2
	}
	result := make([]string, len(lines))
	for i, line := range lines {
		result[i] = fmt.Sprintf("%*d: %s", width, startLine+i, line)
	}
	return result
}

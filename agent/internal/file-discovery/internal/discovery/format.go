package discovery

import "bytes"

type outputBlockOptions struct {
	header             string
	footer             string
	truncationMessage  string
	maxLines           int
	maxBytes           int
	countLinesFromBody bool
}

type outputBlockResult struct {
	formatted     string
	bodyBytes     int
	truncated     bool
	linesIncluded int
}

func formatOutputBlock(lines []string, opts outputBlockOptions) outputBlockResult {
	var body bytes.Buffer
	truncated := false
	linesIncluded := 0

	switch {
	case opts.maxBytes > 0:
		for i, line := range lines {
			if i > 0 {
				body.WriteByte('\n')
			}
			body.WriteString(line)
			linesIncluded++
			if body.Len() > opts.maxBytes {
				truncated = true
				break
			}
		}
		if truncated {
			b := body.Bytes()
			if opts.maxBytes < len(b) {
				b = b[:opts.maxBytes]
			}
			body.Reset()
			body.Write(b)
			if opts.countLinesFromBody {
				linesIncluded = 0
				if body.Len() > 0 {
					for _, c := range body.Bytes() {
						if c == '\n' {
							linesIncluded++
						}
					}
					linesIncluded++
				}
			}
		}
	default:
		if opts.maxLines > 0 && len(lines) > opts.maxLines {
			truncated = true
			lines = lines[:opts.maxLines]
		}
		for i, line := range lines {
			if i > 0 {
				body.WriteByte('\n')
			}
			body.WriteString(line)
		}
		linesIncluded = len(lines)
	}

	var buf bytes.Buffer
	buf.WriteString(opts.header)
	buf.Write(body.Bytes())
	if body.Len() > 0 {
		buf.WriteByte('\n')
	}
	if truncated {
		buf.WriteString(opts.truncationMessage)
	}
	buf.WriteString(opts.footer)

	return outputBlockResult{
		formatted:     buf.String(),
		bodyBytes:     body.Len(),
		truncated:     truncated,
		linesIncluded: linesIncluded,
	}
}

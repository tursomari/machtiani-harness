package contextbuilder

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

const (
	defaultPerFileCap = 100 * 1024      // 100 KB
	defaultTotalCap   = 2 * 1024 * 1024 // 2 MB
)

type Options struct {
	PerFileCap int
	TotalCap   int
}

func (o Options) withDefaults() Options {
	if o.PerFileCap <= 0 {
		o.PerFileCap = defaultPerFileCap
	}
	if o.TotalCap <= 0 {
		o.TotalCap = defaultTotalCap
	}
	return o
}

// Build concatenates the user prompt and the contents of the discovered files
// into a single prompt suitable for an LLM call.
func Build(userPrompt string, relPaths []string, opts Options) (string, []string) {
	opts = opts.withDefaults()
	var b strings.Builder
	b.WriteString(userPrompt)
	b.WriteString("\n\nHere are the relevant files:\n")

	included := make([]string, 0, len(relPaths))
	total := 0

	for _, p := range relPaths {
		if total >= opts.TotalCap {
			break
		}
		// Safety: ensure relative path and no traversal
		if filepath.IsAbs(p) || strings.Contains(p, "..") || strings.Contains(p, "\\") {
			continue
		}

		// Attempt to read with per-file cap
		f, err := os.Open(p)
		header := fmt.Sprintf("\n\n### %s\n\n", p)
		b.WriteString(header)
		included = append(included, p)
		if err != nil {
			b.WriteString("```\n[ERROR: could not read file]\n``" + "`\n")
			continue
		}
		defer f.Close()

		// Read up to cap or until EOF
		remainingBudget := opts.TotalCap - total
		perFileBudget := opts.PerFileCap
		if remainingBudget < perFileBudget {
			perFileBudget = remainingBudget
		}
		if perFileBudget <= 0 {
			break
		}

		bufSize := perFileBudget
		if bufSize < 4096 {
			bufSize = 4096
		}
		buf := make([]byte, 0, perFileBudget)
		tmp := make([]byte, bufSize)
		var readErr error
		var totalRead int
		for totalRead < perFileBudget {
			n, err := f.Read(tmp)
			if n > 0 {
				space := perFileBudget - totalRead
				if n > space {
					n = space
				}
				buf = append(buf, tmp[:n]...)
				totalRead += n
			}
			if err == io.EOF {
				readErr = nil
				break
			}
			if err != nil {
				readErr = err
				break
			}
		}

		b.WriteString("```\n")
		b.Write(buf)
		if readErr == nil {
			// If there are more bytes in file than read, mark truncated
			// We can stat file size to know
			if st, statErr := f.Stat(); statErr == nil {
				if st.Size() > int64(totalRead) {
					b.WriteString("\n[TRUNCATED]\n")
				}
			}
		} else {
			b.WriteString("\n[READ ERROR: ")
			b.WriteString(readErr.Error())
			b.WriteString("]\n")
		}
		b.WriteString("``" + "`\n")
		total += totalRead
	}

	return b.String(), included
}

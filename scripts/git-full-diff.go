package main

import (
	"bufio"
	"fmt"
	"io/ioutil"
	"os"
	"os/exec"
	"strings"
)

// ANSI Color Codes
const (
	ColorReset   = "\033[0m"
	ColorRed     = "\033[31m"
	ColorGreen   = "\033[32m"
	ColorDim     = "\033[2m"
	ColorMagenta = "\033[35m"
)

func main() {
	if len(os.Args) != 2 {
		fmt.Printf("Usage: %s <path/to/file>\n", os.Args[0])
		os.Exit(1)
	}

	filePath := os.Args[1]

	// 1. Check if file exists in git
	if err := runSilent("git", "ls-files", "--error-unmatch", filePath); err != nil {
		fmt.Printf("Error: '%s' is not tracked by Git.\n", filePath)
		os.Exit(1)
	}

	// 2. Get content of HEAD version
	oldContent, err := exec.Command("git", "show", "HEAD:"+filePath).Output()
	if err != nil {
		fmt.Printf("Error obtaining HEAD version: %v\n", err)
		os.Exit(1)
	}

	// 3. Create temp file for OLD version
	tmpFile, err := ioutil.TempFile("", "git-diff-old-")
	if err != nil {
		panic(err)
	}
	defer os.Remove(tmpFile.Name())

	if _, err := tmpFile.Write(oldContent); err != nil {
		panic(err)
	}
	tmpFile.Close()

	// 4. Run git diff between TMP (Old) and FileOnDisk (New)
	// We use -U99999999 to force the diff to show the entire file context
	// We use --no-index to allow diffing a file outside of index vs inside
	cmd := exec.Command("git", "diff", "--no-index", "--unified=99999999", tmpFile.Name(), filePath)
	
	// git diff returns exit code 1 if differences are found, which is expected here.
	// We only care about the stdout.
	out, _ := cmd.Output() 
	diffOutput := string(out)

	processDiff(diffOutput)
}

func processDiff(diffData string) {
	scanner := bufio.NewScanner(strings.NewReader(diffData))
	
	// Helper to track line number in the NEW file (Working Tree)
	newLineNum := 0

	// State to skip the diff headers
	inHeader := true

	for scanner.Scan() {
		line := scanner.Text()

		// Skip diff metadata headers
		if inHeader {
			if strings.HasPrefix(line, "@@") {
				inHeader = false
			}
			continue
		}

		if len(line) == 0 {
			// Empty context line
			newLineNum++
			fmt.Printf("%s%6d  %s%s\n", ColorDim, newLineNum, " ", ColorReset)
			continue
		}

		prefix := line[0]
		content := line[1:]

		switch prefix {
		case ' ':
			// Unchanged line
			newLineNum++
			fmt.Printf("%6d    %s\n", newLineNum, content)
		case '+':
			// Added line (Exists in Working Tree)
			newLineNum++
			fmt.Printf("%s%6d  + %s%s\n", ColorGreen, newLineNum, content, ColorReset)
		case '-':
			// Deleted line (Does NOT exist in Working Tree, so no Line Number)
			// We print '-' or the old line number if you tracked it, but standard
			// cat -n logic implies this line doesn't have a number in the current file.
			fmt.Printf("%s        - %s%s\n", ColorRed, content, ColorReset)
		}
	}
}

func runSilent(name string, args ...string) error {
	cmd := exec.Command(name, args...)
	cmd.Stdout = ioutil.Discard
	cmd.Stderr = ioutil.Discard
	return cmd.Run()
}

package agents

import (
	"strings"
	"testing"
)

// heredocBody is the shared heredoc content used by heredoc tests.
const heredocBody = `cat > /tmp/verify_llm_claims.sh << SCRIPT
#!/bin/bash
echo "It is a test script"
echo '"double" quotes inside single'
SCRIPT`

func TestParseXMLCommandFailureHeredoc(t *testing.T) {
	content := `<command>
` + heredocBody + `
</command>`
	cmd, _, err := parseXMLCommand(content, "command")
	if err != nil {
		t.Fatalf("parseXMLCommand should succeed: %v", err)
	}
	expected := heredocBody
	if cmd != expected {
		t.Errorf("expected:\n%s\ngot:\n%s", expected, cmd)
	}
}

func TestParseXMLCommandFailureApostrophe(t *testing.T) {
	content := `<command>
machtiani-forge SELECT * FROM table WHERE name = 'O''Brien'
</command>`
	cmd, _, err := parseXMLCommand(content, "command")
	if err != nil {
		t.Fatalf("parseXMLCommand should succeed: %v", err)
	}
	expected := "machtiani-forge SELECT * FROM table WHERE name = 'O''Brien'"
	if cmd != expected {
		t.Errorf("expected %q, got %q", expected, cmd)
	}
}

func TestParseXMLCommandFailureEscapedQuotes(t *testing.T) {
	content := `<command>
echo "He said \"hello\" to me"
</command>`
	cmd, _, err := parseXMLCommand(content, "command")
	if err != nil {
		t.Fatalf("parseXMLCommand should succeed: %v", err)
	}
	expected := `echo "He said \"hello\" to me"`
	if cmd != expected {
		t.Errorf("expected %q, got %q", expected, cmd)
	}
}

func TestParseXMLCommandNormal(t *testing.T) {
	content := `<command>
echo hello
</command>`
	cmd, _, err := parseXMLCommand(content, "command")
	if err != nil {
		t.Fatalf("parseXMLCommand should succeed: %v", err)
	}
	if cmd != "echo hello" {
		t.Errorf("expected %q, got %q", "echo hello", cmd)
	}
}

func TestParseXMLCommandHeredocSuffixed(t *testing.T) {
	content := `<command-foo>
` + heredocBody + `
</command-foo>`
	cmd, _, err := parseXMLCommand(content, "command-foo")
	if err != nil {
		t.Fatalf("parseXMLCommand should succeed: %v", err)
	}
	expected := heredocBody
	if cmd != expected {
		t.Errorf("expected:\n%s\ngot:\n%s", expected, cmd)
	}
}

func TestParseXMLCommandApostropheSuffixed(t *testing.T) {
	content := `<command-foo>
machtiani-forge SELECT * FROM table WHERE name = 'O''Brien'
</command-foo>`
	cmd, _, err := parseXMLCommand(content, "command-foo")
	if err != nil {
		t.Fatalf("parseXMLCommand should succeed: %v", err)
	}
	expected := "machtiani-forge SELECT * FROM table WHERE name = 'O''Brien'"
	if cmd != expected {
		t.Errorf("expected %q, got %q", expected, cmd)
	}
}

func TestParseXMLCommandEscapedQuotesSuffixed(t *testing.T) {
	content := `<command-foo>
echo "He said \"hello\" to me"
</command-foo>`
	cmd, _, err := parseXMLCommand(content, "command-foo")
	if err != nil {
		t.Fatalf("parseXMLCommand should succeed: %v", err)
	}
	expected := `echo "He said \"hello\" to me"`
	if cmd != expected {
		t.Errorf("expected %q, got %q", expected, cmd)
	}
}

func TestParseXMLCommandNormalSuffixed(t *testing.T) {
	content := `<command-foo>
echo hello
</command-foo>`
	cmd, _, err := parseXMLCommand(content, "command-foo")
	if err != nil {
		t.Fatalf("parseXMLCommand should succeed: %v", err)
	}
	if cmd != "echo hello" {
		t.Errorf("expected %q, got %q", "echo hello", cmd)
	}
}

func TestParseXMLCommandANSICQuoting(t *testing.T) {
	content := `<command>
echo $'hello\nworld'
</command>`
	cmd, _, err := parseXMLCommand(content, "command")
	if err != nil {
		t.Fatalf("parseXMLCommand should succeed: %v", err)
	}
	expected := `echo $'hello\nworld'`
	if cmd != expected {
		t.Errorf("expected %q, got %q", expected, cmd)
	}
}

func TestParseXMLCommandLiteralTagAsText(t *testing.T) {
	content := `<command-foo>
echo "use the command-foo tag"
</command-foo>`
	cmd, _, err := parseXMLCommand(content, "command-foo")
	if err != nil {
		t.Fatalf("parseXMLCommand should succeed: %v", err)
	}
	expected := `echo "use the command-foo tag"`
	if cmd != expected {
		t.Errorf("expected %q, got %q", expected, cmd)
	}
}

func TestParseXMLCommandMissingOpeningTag(t *testing.T) {
	content := `echo hello
</command>`
	_, _, err := parseXMLCommand(content, "command")
	if err == nil {
		t.Fatal("parseXMLCommand should fail with missing opening tag")
	}
}

func TestParseXMLCommandMissingClosingTag(t *testing.T) {
	content := `<command>
echo hello`
	_, _, err := parseXMLCommand(content, "command")
	if err == nil {
		t.Fatal("parseXMLCommand should fail with missing closing tag")
	}
}

func TestParseXMLCommandMismatchedTags(t *testing.T) {
	content := `<command-foo>
echo hello
</command-bar>`
	_, _, err := parseXMLCommand(content, "command-foo")
	if err == nil {
		t.Fatal("parseXMLCommand should fail with mismatched tags")
	}
}

func TestParseXMLCommandEmptyBlock(t *testing.T) {
	content := `<command>   </command>`
	cmd, _, _ := parseXMLCommand(content, "command")
	if cmd != "" {
		t.Errorf("expected empty string, got %q", cmd)
	}
}

func TestParseXMLCommandLeadingTrailingCommentary(t *testing.T) {
	content := `Some text before
<command>
echo hello
</command>
Some text after`
	cmd, corrections, err := parseXMLCommand(content, "command")
	if err != nil {
		t.Fatalf("parseXMLCommand should succeed: %v", err)
	}
	if cmd != "echo hello" {
		t.Errorf("expected %q, got %q", "echo hello", cmd)
	}
	if len(corrections) == 0 {
		t.Error("expected non-empty corrections slice for leading/trailing commentary")
	}
}

func TestParseXMLCommandMctForgePreservesSimpleHashLines(t *testing.T) {
	content := `<command>
machtiani-forge do something
# This is a comment
</command>`
	cmd, _, err := parseXMLCommand(content, "command")
	if err != nil {
		t.Fatalf("parseXMLCommand should succeed: %v", err)
	}
	if !strings.HasPrefix(cmd, "machtiani-forge") {
		t.Errorf("expected command to start with machtiani-forge, got: %s", cmd)
	}
	if !strings.Contains(cmd, "# This is a comment") {
		t.Errorf("expected hash line to be preserved, got: %s", cmd)
	}
	if strings.Contains(cmd, "&&") {
		t.Errorf("expected no && joining, got: %s", cmd)
	}
}

func TestParseXMLCommandMultiLineNoJoin(t *testing.T) {
	content := `<command>
echo one
echo two
echo three
</command>`
	cmd, _, err := parseXMLCommand(content, "command")
	if err != nil {
		t.Fatalf("parseXMLCommand should succeed: %v", err)
	}
	if !strings.Contains(cmd, "\n") {
		t.Error("expected newlines to be preserved")
	}
	if strings.Contains(cmd, "&&") {
		t.Errorf("expected no && joining, got: %s", cmd)
	}
}

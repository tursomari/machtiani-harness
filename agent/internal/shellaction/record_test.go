package shellaction

import "testing"

func TestParseJournalHoldsPartialTrailingLineAndRejectsCompleteCorruption(t *testing.T) {
	data := []byte("{\"v\":1,\"turn\":2,\"seq\":1,\"command\":\"one\"}\n{\"v\":1,\"turn\":2")
	records, err := ParseJournal(data)
	if err != nil {
		t.Fatalf("ParseJournal(partial) error = %v", err)
	}
	if len(records) != 1 || records[0].Command != "one" {
		t.Fatalf("ParseJournal(partial) = %#v", records)
	}

	if _, err := ParseJournal([]byte("{broken}\n")); err == nil {
		t.Fatal("ParseJournal(complete corruption) error = nil")
	}
}

func TestEncodeRecordUsesOneNewlineFramedObject(t *testing.T) {
	data, err := EncodeRecord(Record{Version: 1, SessionID: "session", Turn: 4, Sequence: 9, Command: "echo ok"})
	if err != nil {
		t.Fatal(err)
	}
	if len(data) == 0 || data[len(data)-1] != '\n' {
		t.Fatalf("EncodeRecord() = %q, want trailing newline", data)
	}
	records, err := ParseJournal(data)
	if err != nil || len(records) != 1 || records[0].Sequence != 9 {
		t.Fatalf("round trip = %#v, %v", records, err)
	}
}

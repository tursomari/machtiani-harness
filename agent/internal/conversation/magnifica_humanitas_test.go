package conversation

import (
	"encoding/json"
	"testing"
)

func TestConversationMagnificaHumanitasMarshalRoundTrip(t *testing.T) {
	conv := New("magnifica-round-trip", "Preserve the selected quote")
	conv.MagnificaHumanitas = &MagnificaHumanitas{
		Paragraph: 42,
		Line:      3,
		Quote:     "Humanity is our shared work.",
	}

	data, err := conv.Marshal()
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	loaded, err := Unmarshal(data)
	if err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if loaded.MagnificaHumanitas == nil {
		t.Fatal("MagnificaHumanitas was not preserved")
	}
	if *loaded.MagnificaHumanitas != *conv.MagnificaHumanitas {
		t.Fatalf("MagnificaHumanitas = %#v, want %#v", loaded.MagnificaHumanitas, conv.MagnificaHumanitas)
	}

	clone := conv.MagnificaHumanitas.Clone()
	if clone == conv.MagnificaHumanitas || *clone != *conv.MagnificaHumanitas {
		t.Fatalf("Clone() = %#v, want an equal independent copy of %#v", clone, conv.MagnificaHumanitas)
	}
}

func TestConversationMarshalOmitsNilMagnificaHumanitas(t *testing.T) {
	data, err := New("magnifica-omitted", "No quote selected").Marshal()
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}

	var object map[string]json.RawMessage
	if err := json.Unmarshal(data, &object); err != nil {
		t.Fatalf("decode marshaled conversation: %v", err)
	}
	if _, ok := object["magnifica_humanitas"]; ok {
		t.Fatalf("nil magnifica_humanitas was serialized: %s", data)
	}
}

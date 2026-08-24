package session

import (
	"testing"

	"github.com/tursomari/machtiani/agent/internal/ui"
)

func TestEmitSessionStartedEventCarriesMagnificaGate(t *testing.T) {
	for _, enabled := range []bool{false, true} {
		t.Run(map[bool]string{false: "disabled", true: "enabled"}[enabled], func(t *testing.T) {
			bus := ui.NewEventBus(1)
			defer bus.Close()
			events := bus.Subscribe()

			emitSessionStartedEvent(bus, ui.SessionStartedEvent{SessionID: "test-session"}, Config{
				MagnificaHumanitas: enabled,
			})

			event := (<-events).(ui.SessionStartedEvent)
			if event.MagnificaHumanitas != enabled {
				t.Fatalf("MagnificaHumanitas = %t, want %t", event.MagnificaHumanitas, enabled)
			}
		})
	}
}

package session

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/tursomari/machtiani/agent/internal/conversation"
)

func TestConversationFileAtomicWrites(t *testing.T) {
	path := filepath.Join(t.TempDir(), "conversation.json")
	payloadA := marshalAtomicTestConversation(t, "atomic-a", strings.Repeat("payload-a-", 32*1024))
	payloadB := marshalAtomicTestConversation(t, "atomic-b", strings.Repeat("payload-b-", 32*1024))

	const (
		writeCount  = 64
		readerCount = 4
	)
	done := make(chan struct{})
	errCh := make(chan error, readerCount+1)
	var wg sync.WaitGroup

	wg.Add(1)
	go func() {
		defer wg.Done()
		defer close(done)
		for i := 0; i < writeCount; i++ {
			payload := payloadA
			if i%2 == 1 {
				payload = payloadB
			}
			if err := writeConversationFileAtomic(path, payload); err != nil {
				errCh <- fmt.Errorf("atomic write %d: %w", i, err)
				return
			}
		}
	}()

	for i := 0; i < readerCount; i++ {
		wg.Add(1)
		go func(reader int) {
			defer wg.Done()
			seen := false
			for {
				data, err := os.ReadFile(path)
				if err != nil {
					if errors.Is(err, os.ErrNotExist) && !seen {
						select {
						case <-done:
							errCh <- fmt.Errorf("reader %d: conversation missing after writer completed", reader)
							return
						default:
							runtime.Gosched()
							continue
						}
					}
					errCh <- fmt.Errorf("reader %d: read conversation after publication: %w", reader, err)
					return
				}
				seen = true
				if _, err := conversation.Unmarshal(data); err != nil {
					errCh <- fmt.Errorf("reader %d: observed invalid conversation: %w", reader, err)
					return
				}
				if !bytes.Equal(data, payloadA) && !bytes.Equal(data, payloadB) {
					errCh <- fmt.Errorf("reader %d: observed unexpected complete payload of %d bytes", reader, len(data))
					return
				}
				select {
				case <-done:
					return
				default:
				}
			}
		}(i)
	}

	wg.Wait()
	close(errCh)
	for err := range errCh {
		t.Error(err)
	}
}

func TestConversationRecorderSaveAtomicVisible(t *testing.T) {
	path := filepath.Join(t.TempDir(), "conversation.json")
	recorder := newConversationRecorder(
		nil,
		"atomic-recorder",
		strings.Repeat("recorder-payload-", 32*1024),
		path,
		false,
		nil,
		false,
	)
	if err := recorder.Load(); err != nil {
		t.Fatalf("Load: %v", err)
	}

	const saveCount = 64
	done := make(chan struct{})
	errCh := make(chan error, 2)
	var published atomic.Bool
	var wg sync.WaitGroup

	wg.Add(1)
	go func() {
		defer wg.Done()
		defer close(done)
		for i := 0; i < saveCount; i++ {
			if err := recorder.Save(); err != nil {
				errCh <- fmt.Errorf("Save %d: %w", i, err)
				return
			}
			published.Store(true)
		}
	}()

	wg.Add(1)
	go func() {
		defer wg.Done()
		for {
			data, err := os.ReadFile(path)
			if err != nil {
				if errors.Is(err, os.ErrNotExist) && !published.Load() {
					select {
					case <-done:
						errCh <- errors.New("conversation missing after Save completed")
						return
					default:
						runtime.Gosched()
						continue
					}
				}
				errCh <- fmt.Errorf("read conversation after first Save: %w", err)
				return
			}
			if _, err := conversation.Unmarshal(data); err != nil {
				errCh <- fmt.Errorf("observed invalid recorder conversation: %w", err)
				return
			}
			select {
			case <-done:
				return
			default:
			}
		}
	}()

	wg.Wait()
	close(errCh)
	for err := range errCh {
		t.Error(err)
	}
}

func marshalAtomicTestConversation(t *testing.T, sessionID, goal string) []byte {
	t.Helper()
	data, err := conversation.New(sessionID, goal).Marshal()
	if err != nil {
		t.Fatalf("marshal test conversation: %v", err)
	}
	return data
}

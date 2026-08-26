package command

import (
	"sync"
	"testing"
)

func TestExecutorReturnsStoredResultForDuplicateIdempotencyKey(t *testing.T) {
	executor := NewExecutor(NewMemoryStore())
	first, err := executor.Execute(Command{
		ID:             "cmd_1",
		Type:           SystemPing,
		Version:        1,
		IdempotencyKey: "service_1:ping",
		Payload:        []byte(`{}`),
	})
	if err != nil {
		t.Fatalf("first execution failed: %v", err)
	}

	second, err := executor.Execute(Command{
		ID:             "cmd_2",
		Type:           SystemPing,
		Version:        1,
		IdempotencyKey: "service_1:ping",
		Payload:        []byte(`{}`),
	})
	if err != nil {
		t.Fatalf("duplicate execution failed: %v", err)
	}
	if second.CommandID != first.CommandID {
		t.Fatalf("expected stored command id %q, got %q", first.CommandID, second.CommandID)
	}
}

func TestExecutorRejectsUnknownCommand(t *testing.T) {
	executor := NewExecutor(NewMemoryStore())
	_, err := executor.Execute(Command{
		ID:             "cmd_1",
		Type:           "shell.execute",
		Version:        1,
		IdempotencyKey: "service_1:unsafe",
		Payload:        []byte(`{}`),
	})
	if err == nil {
		t.Fatal("expected unsupported command to fail")
	}
}

func TestExecutorSerializesDuplicateCommands(t *testing.T) {
	executor := NewExecutor(NewMemoryStore())
	command := Command{
		ID:             "cmd_1",
		Type:           SystemPing,
		Version:        1,
		IdempotencyKey: "service_1:concurrent-ping",
		Payload:        []byte(`{}`),
	}

	var results [2]Result
	var errors [2]error
	var group sync.WaitGroup
	group.Add(2)
	for index := range results {
		go func(index int) {
			defer group.Done()
			results[index], errors[index] = executor.Execute(command)
		}(index)
	}
	group.Wait()

	for _, err := range errors {
		if err != nil {
			t.Fatalf("concurrent execution failed: %v", err)
		}
	}
	if results[0].CommandID != results[1].CommandID {
		t.Fatalf("expected the stored result, got %q and %q", results[0].CommandID, results[1].CommandID)
	}
}

package taskevents

import (
	"context"
	"encoding/json"
	"testing"
)

func TestReplayIndependentConsumersAndRestart(t *testing.T) {
	dir := t.TempDir()
	ctx := context.Background()
	log, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	for i := uint64(1); i <= 3; i++ {
		if err := log.Publish(ctx, Event{TaskID: "task", EventID: i, MessageSeq: 4, Type: "message", Data: json.RawMessage(`{"delta":"text"}`)}); err != nil {
			t.Fatal(err)
		}
	}
	for range 2 {
		sub, err := log.Subscribe(ctx, "task")
		if err != nil {
			t.Fatal(err)
		}
		for i := uint64(1); i <= 3; i++ {
			event, err := sub.Next(ctx)
			if err != nil || event.EventID != i {
				t.Fatalf("%+v %v", event, err)
			}
		}
		sub.Close()
	}
	log.Close()
	log, err = Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer log.Close()
	last, err := log.Last(ctx, "task")
	if err != nil || last != 3 {
		t.Fatalf("%d %v", last, err)
	}
	if err := log.Purge(ctx, "task"); err != nil {
		t.Fatal(err)
	}
	last, err = log.Last(ctx, "task")
	if err != nil || last != 0 {
		t.Fatalf("%d %v", last, err)
	}
}

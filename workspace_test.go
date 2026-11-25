package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Cdners/S3S/internal/core"
	"github.com/Cdners/S3S/internal/store"
)

func TestConnectionBridge(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "workspace.sqlite3"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	w := &Workspace{store: db}
	ctx := context.Background()
	saved := w.SaveConnection(ctx, core.Connection{Name: "Test profile", Provider: core.R2})
	if saved.Error != nil || saved.Data == nil || saved.Data.ID == "" {
		t.Fatalf("save: %+v", saved)
	}
	list := w.Connections(ctx)
	if list.Error != nil || len(list.Data) != 1 || list.Data[0] != *saved.Data {
		t.Fatalf("list: %+v", list)
	}
	invalid := w.SaveConnection(ctx, core.Connection{})
	if invalid.Data != nil || invalid.Error == nil || invalid.Error.Code != core.InvalidConnection {
		t.Fatalf("invalid response: %+v", invalid)
	}
}

func TestBridgeErrorWireFormat(t *testing.T) {
	detail := &core.Error{
		Code: core.Conflict, Params: map[string]string{"part": "7"},
		RequestID: "request-42", ProviderCode: "Conflict", Retryable: true,
	}
	resp := failure[[]core.Task](fmt.Errorf("internal detail: %w", detail), core.Storage)
	wire, err := json.Marshal(resp)
	if err != nil {
		t.Fatal(err)
	}
	var decoded response[[]core.Task]
	if err := json.Unmarshal(wire, &decoded); err != nil {
		t.Fatal(err)
	}
	e := decoded.Error
	if e == nil || e.Code != detail.Code || e.Params["part"] != "7" ||
		e.RequestID != detail.RequestID || e.ProviderCode != detail.ProviderCode || !e.Retryable ||
		strings.Contains(string(wire), "internal detail") {
		t.Fatalf("unexpected response %s", wire)
	}
	raw := errors.New("private driver detail")
	wire, err = json.Marshal(failure[any](raw, core.Storage))
	if err != nil || strings.Contains(string(wire), raw.Error()) || !strings.Contains(string(wire), string(core.Storage)) {
		t.Fatalf("unsanitized or missing fallback: %s, %v", wire, err)
	}
}

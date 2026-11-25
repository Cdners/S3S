package main

import (
	"context"
	"embed"
	"log"
	"log/slog"
	"os"
	"path/filepath"

	"github.com/Cdners/S3S/internal/store"
	"github.com/Cdners/S3S/internal/transfer"
	"github.com/gofrs/flock"
	"github.com/wailsapp/wails/v3/pkg/application"
)

//go:embed all:frontend/dist
var assets embed.FS

// Open the per-user database before exposing the workspace to the renderer.
// A storage failure still allows the UI to open and explain the error in its locale.
func openWorkspace() *Workspace {
	dir, err := os.UserConfigDir()
	if err != nil {
		return &Workspace{}
	}
	return openWorkspaceAt(filepath.Join(dir, "S3S"))
}

func openWorkspaceAt(dir string) *Workspace {
	if err := os.MkdirAll(dir, 0700); err != nil {
		return &Workspace{}
	}
	lock := flock.New(filepath.Join(dir, "workspace.lock"))
	owned, err := lock.TryLock()
	if err == nil && !owned {
		return &Workspace{openIssue: "error.workspaceInUse"}
	}
	if err != nil {
		return &Workspace{}
	}
	db, err := store.Open(filepath.Join(dir, "workspace.sqlite3"))
	if err != nil {
		lock.Unlock()
		return &Workspace{}
	}
	engine := transfer.New(db)
	if err := engine.Recover(context.Background()); err != nil {
		db.Close()
		lock.Unlock()
		return &Workspace{}
	}
	life, stop := context.WithCancel(context.Background())
	return &Workspace{life: life, stop: stop, store: db, engine: engine, lock: lock, active: make(map[string]worker)}
}

func main() {
	workspace := openWorkspace()
	app := application.New(application.Options{
		Name: "S3S",
		// Framework debug traces include binding arguments. Credential provisioning
		// is write-only, so use our structured errors rather than wire-level logs.
		Logger: slog.New(slog.DiscardHandler),
		Assets: application.AssetOptions{
			Handler: application.AssetFileServerFS(assets),
		},
		Services:   []application.Service{application.NewService(workspace)},
		Linux:      application.LinuxOptions{ApplicationID: "io.github.cdners.s3s"},
		Mac:        application.MacOptions{ApplicationShouldTerminateAfterLastWindowClosed: true},
		OnShutdown: workspace.close,
	})
	// The desktop uses in-window navigation. Avoid the framework's default English menu.
	app.Menu.Set(application.NewMenu())
	window := app.Window.NewWithOptions(application.WebviewWindowOptions{
		Title: "S3S",
		Width: 1200, Height: 800,
		MinWidth: 760, MinHeight: 560,
		DefaultContextMenuDisabled: true,
		URL:                        "/",
	})

	workspace.chooseDestination = func(title string) (string, error) {
		dialog := app.Dialog.SaveFile()
		dialog.SetOptions(&application.SaveFileDialogOptions{Title: title, Window: window, CanCreateDirectories: true})
		return dialog.PromptForSingleSelection()
	}

	if err := app.Run(); err != nil {
		log.Print("desktop.startFailed")
		os.Exit(1)
	}
}

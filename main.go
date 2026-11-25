package main

import (
	"embed"
	"log"
	"os"
	"path/filepath"

	"github.com/Cdners/S3S/internal/store"
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
	dir = filepath.Join(dir, "S3S")
	if err := os.MkdirAll(dir, 0700); err != nil {
		return &Workspace{}
	}
	db, err := store.Open(filepath.Join(dir, "workspace.sqlite3"))
	if err != nil {
		return &Workspace{}
	}
	return &Workspace{store: db}
}

func main() {
	workspace := openWorkspace()
	app := application.New(application.Options{
		Name: "S3S",
		Assets: application.AssetOptions{
			Handler: application.AssetFileServerFS(assets),
		},
		Services: []application.Service{application.NewService(workspace)},
		Linux:    application.LinuxOptions{ApplicationID: "io.github.cdners.s3s"},
		Mac:      application.MacOptions{ApplicationShouldTerminateAfterLastWindowClosed: true},
		OnShutdown: func() {
			if workspace.store != nil {
				workspace.store.Close()
			}
		},
	})
	// M0 uses in-window navigation. Avoid the framework's default English menu.
	app.Menu.Set(application.NewMenu())
	app.Window.NewWithOptions(application.WebviewWindowOptions{
		Title: "S3S",
		Width: 1200, Height: 800,
		MinWidth: 760, MinHeight: 560,
		DefaultContextMenuDisabled: true,
		URL:                        "/",
	})

	if err := app.Run(); err != nil {
		log.Print("desktop.startFailed")
		os.Exit(1)
	}
}

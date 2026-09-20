package main

import (
	"context"
	"embed"
	"fmt"
	"log"
	"strconv"
	"time"

	"ghinbox/internal/app"
	"ghinbox/internal/pipeline"
	"ghinbox/internal/services"

	"github.com/wailsapp/wails/v3/pkg/application"
)

// The production frontend build in frontend/dist is embedded into the binary.
// frontend/dist/.gitkeep keeps the directive valid before the first frontend build.
//
//go:embed all:frontend/dist
var assets embed.FS

//go:embed build/appicon.png
var appIcon []byte

const syncInterval = 3 * time.Minute

func main() {
	var wails *application.App
	core, err := app.Open(func(name string, data any) {
		if wails != nil {
			wails.Event.Emit(name, data)
		}
	})
	if err != nil {
		log.Fatal(err)
	}
	defer core.Close()

	wails = application.New(application.Options{
		Name:        "GH Inbox",
		Description: "AI-triaged GitHub notifications dashboard",
		Services: []application.Service{
			application.NewService(&services.AccountsService{App: core}),
			application.NewService(&services.InboxService{App: core}),
			application.NewService(&services.MineService{App: core}),
			application.NewService(&services.DiagnosticsService{App: core}),
		},
		Assets: application.AssetOptions{
			Handler: application.AssetFileServerFS(assets),
		},
		Mac: application.MacOptions{
			ApplicationShouldTerminateAfterLastWindowClosed: true,
		},
	})

	window := wails.Window.NewWithOptions(application.WebviewWindowOptions{
		Title:            "GH Inbox",
		Width:            1200,
		Height:           800,
		BackgroundColour: application.NewRGB(250, 250, 250),
		URL:              "/",
	})

	setupTray(wails, window, core)

	core.StartScheduler(syncInterval)
	// Warm the inbox shortly after launch without blocking the window.
	go func() {
		time.Sleep(2 * time.Second)
		if _, err := core.Pipe.SyncAll(context.Background(), false); err != nil {
			core.Logger.Warn("startup sync", "err", err)
		}
	}()

	if err := wails.Run(); err != nil {
		log.Fatal(err)
	}
}

// setupTray adds a tray icon whose label is the unread count, with a small menu.
func setupTray(wails *application.App, window *application.WebviewWindow, core *app.App) {
	tray := wails.SystemTray.New()
	tray.SetIcon(appIcon)
	tray.SetTooltip("GH Inbox")

	menu := wails.NewMenu()
	menu.Add("Open GH Inbox").OnClick(func(*application.Context) {
		window.Show()
		window.Focus()
	})
	menu.Add("Sync now").OnClick(func(*application.Context) {
		go func() {
			if _, err := core.Pipe.SyncAll(context.Background(), false); err != nil {
				core.Logger.Warn("tray sync", "err", err)
			}
		}()
	})
	menu.AddSeparator()
	menu.Add("Quit").OnClick(func(*application.Context) { wails.Quit() })
	tray.SetMenu(menu)

	refresh := func() {
		c, err := core.DB.Counts(context.Background(), 0)
		if err != nil {
			return
		}
		tray.SetLabel(strconv.Itoa(c.Unread))
		tray.SetTooltip(fmt.Sprintf("GH Inbox — %d unread", c.Unread))
	}
	wails.Event.On(pipeline.EventInboxUpdated, func(*application.CustomEvent) { refresh() })
	refresh()
}

package main

import (
	"context"
	"embed"
	"fmt"
	"log"
	"strconv"
	"time"

	"gitinbox/internal/app"
	"gitinbox/internal/pipeline"
	"gitinbox/internal/services"

	"github.com/wailsapp/wails/v3/pkg/application"
	"github.com/wailsapp/wails/v3/pkg/services/notifications"
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
	notifier := notifications.New()
	seq := 0
	core, err := app.Open(func(name string, data any) {
		if wails != nil {
			wails.Event.Emit(name, data)
		}
	}, func(n pipeline.Notification) {
		seq++
		if err := notifier.SendNotification(notifications.NotificationOptions{ID: fmt.Sprintf("gitinbox-%d-%d", time.Now().Unix(), seq), Title: n.Title, Body: n.Body, Data: map[string]any{"url": n.URL}}); err != nil {
			log.Printf("notification: %v", err)
		}
	})
	if err != nil {
		log.Fatal(err)
	}
	defer core.Close()

	wails = application.New(application.Options{
		Name:        "GitInbox",
		Description: "AI-triaged GitHub notifications dashboard",
		Services: []application.Service{
			application.NewService(&services.AccountsService{App: core}),
			application.NewService(&services.InboxService{App: core}),
			application.NewService(&services.MineService{App: core}),
			application.NewService(&services.DiagnosticsService{App: core}),
			application.NewService(&services.WatchesService{App: core}),
			application.NewService(&services.JudgeService{App: core}),
			application.NewService(&services.ImpactService{App: core}),
			application.NewService(&services.ProfilesService{App: core}),
			application.NewService(&services.ProseService{App: core}),
			application.NewService(&services.EvalService{App: core}),
			application.NewService(&services.AgentService{App: core}),
			application.NewService(notifier),
		},
		Assets: application.AssetOptions{
			Handler: application.AssetFileServerFS(assets),
		},
		Mac: application.MacOptions{
			ApplicationShouldTerminateAfterLastWindowClosed: true,
		},
	})

	window := wails.Window.NewWithOptions(application.WebviewWindowOptions{
		Title:            "GitInbox",
		Width:            1200,
		Height:           800,
		BackgroundColour: application.NewRGB(250, 250, 250),
		URL:              "/",
	})

	setupTray(wails, window, core)

	core.StartScheduler(syncInterval)
	if as, err := core.Pipe.AgentSettings(context.Background()); err == nil && as.Enabled && as.AutoStart {
		go func() {
			if err := core.StartAgent(context.Background()); err != nil {
				core.Logger.Warn("agent autostart", "err", err)
			}
		}()
	}
	// Warm the inbox shortly after launch without blocking the window.
	go func() {
		time.Sleep(2 * time.Second)
		if _, _, err := core.Pipe.SyncAndJudge(context.Background(), false); err != nil {
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
	tray.SetTooltip("GitInbox")

	menu := wails.NewMenu()
	menu.Add("Open GitInbox").OnClick(func(*application.Context) {
		window.Show()
		window.Focus()
	})
	menu.Add("Sync now").OnClick(func(*application.Context) {
		go func() {
			if _, _, err := core.Pipe.SyncAndJudge(context.Background(), false); err != nil {
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
		tray.SetTooltip(fmt.Sprintf("GitInbox — %d unread", c.Unread))
	}
	wails.Event.On(pipeline.EventInboxUpdated, func(*application.CustomEvent) { refresh() })
	refresh()
}

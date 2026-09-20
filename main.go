package main

import (
	"embed"
	"log"

	"ghinbox/internal/services"

	"github.com/wailsapp/wails/v3/pkg/application"
)

// The production frontend build in frontend/dist is embedded into the binary.
// frontend/dist/.gitkeep keeps the directive valid before the first frontend build.
//
//go:embed all:frontend/dist
var assets embed.FS

func main() {
	app := application.New(application.Options{
		Name:        "GH Inbox",
		Description: "AI-triaged GitHub notifications dashboard",
		Services: []application.Service{
			application.NewService(&services.DiagnosticsService{}),
		},
		Assets: application.AssetOptions{
			Handler: application.AssetFileServerFS(assets),
		},
		Mac: application.MacOptions{
			ApplicationShouldTerminateAfterLastWindowClosed: true,
		},
	})

	app.Window.NewWithOptions(application.WebviewWindowOptions{
		Title:            "GH Inbox",
		Width:            1200,
		Height:           800,
		BackgroundColour: application.NewRGB(250, 250, 250),
		URL:              "/",
	})

	if err := app.Run(); err != nil {
		log.Fatal(err)
	}
}

package main

import (
	"kb/client"
	"kb/ui"

	"fyne.io/fyne/v2/app"
)

func main() {
	// Create the core Fyne app
	appInstance := app.New()

	// Create the logical client (needed early for discovery)
	logicalClient := client.NewClient()

	// Create the UI handler, passing the app and client
	appUI := ui.NewAppUI(appInstance, logicalClient)

	// Run the UI (which shows the main window and starts the event loop)
	appUI.Run()
}

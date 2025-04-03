#!/bin/bash

# Exit immediately if a command exits with a non-zero status.
set -e

echo "--- Preparing Production macOS Build (.app) ---"

# Check for fyne command
if ! command -v fyne &> /dev/null; then
    echo "Error: 'fyne' command not found." >&2
    echo "Please install it first by running: go install fyne.io/fyne/v2/cmd/fyne@latest" >&2
    exit 1
fi

echo "Found 'fyne' command."

# Define icon path (optional - use default if not set or file not found)
ICON_PATH="icon.png" # Default icon name

# Package the application for macOS
echo "Running 'fyne package -os darwin'..."
if [ -f "$ICON_PATH" ]; then
    echo "Using icon: $ICON_PATH"
    fyne package -os darwin -icon "$ICON_PATH"
else
    echo "Icon '$ICON_PATH' not found, using default icon."
    fyne package -os darwin
fi

PACKAGE_EXIT_CODE=$?

# Check build result
if [ $PACKAGE_EXIT_CODE -ne 0 ]; then
    echo "Error: 'fyne package' failed with exit code $PACKAGE_EXIT_CODE" >&2
    exit $PACKAGE_EXIT_CODE
else
    echo "'fyne package' completed successfully."
    echo "Created: kbx.app" # Assuming the output app name matches the directory name
    echo "You can now move 'kbx.app' to your /Applications folder."
    # Announce completion using macOS text-to-speech
    if command -v say &> /dev/null; then
        say "Production build complete"
    fi
fi

exit 0 
#!/bin/bash

# Exit immediately if a command exits with a non-zero status.
set -e

# --- Check Prerequisites ---

# Check for Go
if ! command -v go &> /dev/null; then
    echo "Error: Go is not installed or not found in PATH. Please install Go." >&2
    exit 1
fi

# --- Configure Zlib --- 

# Check for Homebrew (common on macOS for zlib)
if command -v brew &> /dev/null; then
    # Check if zlib is installed via Homebrew
    if brew list zlib &> /dev/null; then
        ZLIB_PREFIX=$(brew --prefix zlib)
        echo "Found zlib installed via Homebrew at: $ZLIB_PREFIX"
        export CGO_CPPFLAGS="-I${ZLIB_PREFIX}/include"
        export CGO_LDFLAGS="-L${ZLIB_PREFIX}/lib"
        echo "Set CGO_CPPFLAGS=$CGO_CPPFLAGS"
        echo "Set CGO_LDFLAGS=$CGO_LDFLAGS"
    else
        echo "Warning: zlib not found via Homebrew. CGo might use system libraries if available." >&2
        echo "Consider running: brew install zlib" >&2
        # Proceeding without setting flags - CGo might find it automatically
    fi
else 
    echo "Warning: Homebrew not found. Cannot automatically configure zlib path." >&2
    echo "CGo will attempt to find system zlib. If build fails, ensure zlib development headers/libraries are installed." >&2
fi

# --- Build Steps ---

# Tidy dependencies
echo "Running go mod tidy..."
go mod tidy
echo "go mod tidy completed successfully."

# Run the build
echo "Running go build..."
go build
BUILD_EXIT_CODE=$?

# Check build result
if [ $BUILD_EXIT_CODE -ne 0 ]; then
    echo "Error: go build failed with exit code $BUILD_EXIT_CODE" >&2
    exit $BUILD_EXIT_CODE
else
    echo "go build completed successfully."
    # Announce completion using macOS text-to-speech
    if command -v say &> /dev/null; then
        say "Build complete"
    fi
fi

exit 0 
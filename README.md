# Keyboard Sharing App

A cross-platform keyboard sharing application that allows you to share your keyboard input between different computers on the same network. Built with Go and Fyne.

## Features

- Share keyboard input between Windows and macOS
- Simple and intuitive GUI
- Works over local network
- Real-time keyboard event transmission

## Requirements

- Go 1.21 or later
- Windows or macOS operating system
- Network connection between devices

## Installation

1. Clone this repository:
```bash
git clone <repository-url>
cd kb
```

2. Install dependencies:
```bash
go mod download
```

3. Build the application:
```bash
go build
```

## Usage

1. Start the application on both computers:
```bash
./kb
```

2. On the source computer (the one whose keyboard you want to share):
   - Click "Start Server"
   - The server will start listening on port 8080
   - Note the IP address of this computer

3. On the target computer:
   - Click "Start Client"
   - Enter the server's IP address in the format `IP:8080` (e.g., `192.168.1.100:8080`)
   - Click "Connect"

4. The keyboard input from the server computer will now be replicated on the client computer.

5. To stop sharing:
   - Click the "Stop Server" button on the server computer
   - Click the "Disconnect" button on the client computer

## Security Note

This application is designed for use on trusted local networks only. It does not implement any encryption or authentication mechanisms.

## License

MIT License 
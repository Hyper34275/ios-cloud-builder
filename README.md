# Builder

Build and develop iOS apps from Windows, Linux, or any platform.

Builder is a CLI tool for iOS development without a Mac. It uses GitHub Actions for remote builds and [MobAI](https://mobai.run) for on-device development.

## Features

- **Build from anywhere**: Build any iOS app (native, Flutter, React Native) via GitHub Actions
- **Flutter dev tools**: Hot reload on real iOS devices from Windows (React Native coming soon)
- **Simple setup**: One command to add the workflow to your repo
- **Code signing**: Optional signing with your certificate and provisioning profile
- **Device integration**: Install and run apps via MobAI

## How It Works

```
Your Repository                  GitHub Actions (macOS)
 └─ .github/workflows/            └─ ios-build.yml
     └─ ios-build.yml                 ├─ Checkout code
                                      ├─ Build with Xcode
builder ios build ───────────────────► Upload IPA artifact
     │
     └─ Downloads IPA ◄─────────────── artifact: ipa
```

## Quick Start

### 1. Authenticate with GitHub

```bash
builder auth github
```

### 2. Initialize (in your project directory)

```bash
cd your-ios-project
builder init
```

This detects your GitHub repo, creates the workflow files, and offers to commit, push, and trigger your first build - all interactively.

### 3. Build

```bash
builder ios build
```

The CLI triggers the workflow and downloads the IPA to `./dist/`.

## Supported Frameworks

| Framework | iOS Path | Auto-detected |
|-----------|----------|---------------|
| Native iOS/Swift | `.` (root) | Yes |
| React Native | `ios/` | Yes |
| Expo (ejected) | `ios/` | Yes |
| Flutter | `ios/` | Yes |
| Cordova/Ionic | `platforms/ios/` | Yes |

## Installation

### Windows (WSL recommended)

We recommend using [WSL](https://learn.microsoft.com/en-us/windows/wsl/install) for the best experience:

```bash
# Inside WSL
curl -sSL https://raw.githubusercontent.com/MobAI-App/ios-builder/main/install.sh | bash
```

Or download `builder.exe` from [Releases](https://github.com/MobAI-App/ios-builder/releases) and add to PATH.

### macOS/Linux

```bash
curl -sSL https://raw.githubusercontent.com/MobAI-App/ios-builder/main/install.sh | bash
```

### From Source

```bash
git clone https://github.com/MobAI-App/ios-builder.git
cd ios-builder
go build -o builder ./cmd/builder
```

## Commands

```bash
# Setup
builder auth github           # Authenticate with GitHub
builder init                  # Set up workflow in current repo

# Building
builder ios build             # Trigger build and download IPA to ./dist/
builder ios build --unsigned  # Build without code signing (if signing is configured)

# Development (requires MobAI)
builder dev flutter           # Install and launch app with Flutter hot reload
builder dev flutter --ipa <path>  # Use specific IPA
builder dev flutter --skip-install --bundle-id <id>  # Use already installed app

# Code signing
builder signing setup         # Set up code signing secrets
```

## Configuration

`builder.json`:

```json
{
  "project": "MyApp",
  "platform": "ios",
  "github": {
    "owner": "username",
    "repo": "my-ios-app"
  },
  "ios": {
    "path": "ios",
    "scheme": "",
    "signing": true
  },
  "mobai": {
    "url": "http://localhost:8686",
    "device_id": ""
  }
}
```

### MobAI Configuration

| Field | Description | Default |
|-------|-------------|---------|
| `mobai.url` | MobAI API URL | `http://localhost:8686` |
| `mobai.device_id` | Preferred device ID (uses first available if empty) | `""` |

**WSL users**: MobAI runs on Windows, so you need to:

1. In MobAI, go to **Integrations → API server** and enable **Allow external connections**
2. Point to your Windows host IP in `builder.json`:

```json
{
  "mobai": {
    "url": "http://$(hostname).local:8686"
  }
}
```

Or find your Windows host IP and use it directly:

```bash
# Get Windows host IP from WSL
cat /etc/resolv.conf | grep nameserver | awk '{print $2}'
```

```json
{
  "mobai": {
    "url": "http://172.x.x.x:8686"
  }
}
```

## Code Signing

By default, builds are unsigned. To enable code signing:

```bash
builder signing setup
```

This uploads your certificate and provisioning profile to GitHub Secrets:
- `IOS_CERTIFICATE` - Base64-encoded .p12 file
- `IOS_CERTIFICATE_PASSWORD` - Certificate password
- `IOS_PROVISIONING_PROFILE` - Base64-encoded .mobileprovision file

Once configured, `builder ios build` will produce signed IPAs. Use `--unsigned` to skip signing.

## Installing the IPA

Use [MobAI](https://mobai.run) to sign and install your IPA directly to your device. MobAI handles code signing automatically and works with both signed and unsigned builds.

## Flutter Development on Windows

Builder supports Flutter hot reload on Windows using [MobAI](https://mobai.run) for iOS device control. This allows you to develop Flutter apps for iOS without a Mac.

### Setup

1. Download and install [MobAI](https://mobai.run/download), then connect your iOS device
2. Build your app:
   ```bash
   builder ios build
   ```
   This creates an IPA in `./dist/`
3. Start development with hot reload:
   ```bash
   builder dev flutter
   ```
   MobAI will guide you through installation. Re-signing requires an iCloud account - we highly recommend creating a new one at [icloud.com](https://icloud.com) instead of using your primary account. If you re-sign, note the new bundle ID (includes team ID suffix, e.g., `com.example.myapp.TEAMID`).

### Subsequent Runs

Once the app is installed, skip the install step:
```bash
builder dev flutter --skip-install --bundle-id com.example.myapp.TEAMID
```

### When to Rebuild

- **Native code changes** (Swift, Objective-C, Podfile, native dependencies): Run `builder ios build` and reinstall
- **Dart code changes only**: No rebuild needed - hot reload handles it automatically

If you don't see your recent Dart changes after launching, press `R` in the terminal to perform a hot restart.

### Troubleshooting

**App won't launch / connection error**
- Close the app on your device before running `builder dev flutter`
- Reconnect the device (unplug/replug USB)
- Restart MobAI
- Run `builder mobai ping` to verify connection

**"No devices found" error**
- Ensure MobAI is running and device is connected
- Only physical iOS devices are supported (no simulators)

**Hot reload not working**
- Make sure you're using the correct bundle ID (with team ID suffix)
- Try hot restart with `R` key
- Check that MobAI shows the device as connected

## Build Limits

GitHub Actions free tier:
- 2,000 minutes/month (macOS uses 10x multiplier = ~200 effective minutes)
- Approximately 15-20 builds per month

## License

[MIT License](LICENSE)

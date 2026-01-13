# Development

## Build

Build for all platforms (Windows, macOS, Linux - x64 & arm64):

```bash
make all
```

Build for specific platform:

```bash
make windows   # Windows x64 + arm64
make darwin    # macOS x64 + arm64
make linux     # Linux x64 + arm64
```

Other commands:

```bash
make clean     # Clean bin/ directory
make run       # Run locally with go run
make docker    # Build Docker image
```

## Docker

Build and push Docker image:

```bash
docker build -t yamiodymel/chaturbate-dvr:2.0.0 .
docker push yamiodymel/chaturbate-dvr:2.0.0
docker image tag yamiodymel/chaturbate-dvr:2.0.0 yamiodymel/chaturbate-dvr:latest
docker push yamiodymel/chaturbate-dvr:latest
```

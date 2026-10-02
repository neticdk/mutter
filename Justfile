version := `git describe --tags --always --dirty 2>/dev/null || echo dev`

# The OAuth client and topic are baked in when set, as in releases.
ldflags := "-X main.version=" + version + " -X main.clientID=" + env("MUTTER_CLIENT_ID", "") + " -X main.clientSecret=" + env("MUTTER_CLIENT_SECRET", "") + " -X main.topic=" + env("MUTTER_TOPIC", "")

# List the recipes.
default:
    @just --list

# Run everything CI runs.
check: fmt-check lint sec test

# Run the tests with the race detector.
test:
    go test -race ./...

# Run golangci-lint with .golangci.yaml.
lint:
    golangci-lint run ./...

# Run gosec on its own, which also reports what golangci-lint's gosec skips.
sec:
    gosec -quiet ./...

# Format the code with gofumpt and goimports.
fmt:
    golangci-lint fmt ./...

# Fail when the code isn't formatted.
fmt-check:
    golangci-lint fmt --diff ./...

# Modernize the code with go fix.
fix:
    go fix ./...

# Build ./mutter for this machine.
build:
    go build -ldflags '{{ ldflags }}' -o mutter .

# Build and run, passing args to mutter.
run *args: build
    ./mutter {{ args }}

# Build every release target into dist/ without publishing.
snapshot:
    goreleaser build --snapshot --clean

# Tidy go.mod and go.sum.
tidy:
    go mod tidy

# Remove build output.
clean:
    rm -rf dist mutter

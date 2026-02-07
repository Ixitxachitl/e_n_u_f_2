param(
    [string]$Target = "windows",
    [switch]$Run,
    [switch]$Clean
)

$version = (Get-Content "$PSScriptRoot\..\VERSION").Trim()
$commit = git rev-parse --short HEAD

$ldflags = "-X main.Version=$version -X main.CommitSHA=$commit"

if ($Clean) {
    go clean
}

switch ($Target) {
    "windows" {
        go build -ldflags $ldflags -o twitchbot.exe ./cmd/bot
        if ($Run -and $LASTEXITCODE -eq 0) {
            .\twitchbot.exe
        }
    }
    "pi" {
        $env:GOOS = "linux"
        $env:GOARCH = "arm64"
        $env:CGO_ENABLED = "0"
        go build -ldflags $ldflags -o twitchbot-linux-arm64 ./cmd/bot
    }
    "launcher" {
        go build -ldflags "-H=windowsgui" -o e_n_u_f.exe ./cmd/launcher
    }
}

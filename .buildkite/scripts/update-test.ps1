# suppress progress output which can slow down downloading significantly
$global:ProgressPreference = "SilentlyContinue"

# prevent Invoke-WebRequest error: The request was aborted: Could not create SSL/TLS secure channel
[Net.ServicePointManager]::SecurityProtocol = [Net.SecurityProtocolType]::Tls12

$destination = "C:\protoc"
$protoUri = "https://github.com/protocolbuffers/protobuf/releases/download/v3.19.4/protoc-3.19.4-win64.zip"

function setupProto() {
    Write-Host "~~~ Setting up proto"
    if (-Not (Test-Path -Path $destination)) {
        New-Item -ItemType Directory -Path $destination | Out-Null
    }

    Invoke-WebRequest -Uri $protoUri -OutFile "protoc.zip"
    Expand-Archive -Path "protoc.zip" -DestinationPath $destination -Force

    $protocBinPath = Join-Path $destination "bin"
    if (-Not ($ENV:Path -contains $protocBinPath)) {
        $ENV:Path += ";$protocBinPath"
    }

    mage -debug update
}

function runUnitTests() {
    Write-Host "Running tests"
    go test -race ./...| Tee-Object tests-report.txt

    try {
        Get-Content "tests-report.txt" | go-junit-report > junit-report.xml
    } catch {
        Write-Error "Failed to create junit-report.xml"
        exit 1
    }
}

setupProto
runUnitTests
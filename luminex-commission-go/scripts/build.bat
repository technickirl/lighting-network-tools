@echo off
title Luminex Commission - Build
cd /d "%~dp0\.."
where go >nul 2>&1 || (echo Go is not installed. Get it from https://go.dev/dl/ && pause && exit /b 1)
if not exist dist mkdir dist
set CGO_ENABLED=0
set GOOS=windows
set GOARCH=amd64
rem Version = latest v* tag without the v; also compiled into the binary (main.version).
set VERSION=
for /f "delims=" %%v in ('git describe --tags --abbrev=0 2^>nul') do set VERSION=%%v
if defined VERSION set VERSION=%VERSION:v=%
if not defined VERSION set VERSION=dev
go build -trimpath -ldflags "-s -w -X main.version=%VERSION%" -o dist\LuminexCommission-%VERSION%-win-x64.exe .
if %errorlevel% neq 0 (echo BUILD FAILED && pause && exit /b 1)
echo SUCCESS: dist\LuminexCommission-%VERSION%-win-x64.exe
pause

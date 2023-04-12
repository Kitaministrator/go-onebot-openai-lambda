@echo off
set BINARY_NAME=go-onebot-openai-lambda
set ZIP_NAME=%BINARY_NAME%.zip
set GOOS=linux
set GOARCH=amd64
set CGO_ENABLED=0

echo Building for %GOOS%/%GOARCH%...
go build -o %BINARY_NAME% %BINARY_NAME%.go

echo Compressing binary into %ZIP_NAME%...
:: powershell Compress-Archive -Path %BINARY_NAME% -DestinationPath %ZIP_NAME%
powershell Compress-Archive .\%BINARY_NAME% .\%ZIP_NAME% -Force
del .\go-onebot-openai-lambda

echo Done.
# Build the console-free Windows GUI exe.
go build -trimpath -ldflags "-H windowsgui" -o vscode-load-llama.exe .

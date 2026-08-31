package cdp

import (
	"io"
	"log/slog"
)

var discardLog = slog.New(slog.NewTextHandler(io.Discard, nil))

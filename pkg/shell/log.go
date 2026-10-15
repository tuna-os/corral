package shell

import (
	"context"
	"errors"
	"log/slog"
	"os/exec"
	"time"

	"github.com/tuna-os/corral/pkg/logging"
)

// binder is a Runner that can carry a context to the commands it runs.
type binder interface {
	bind(ctx context.Context) Runner
}

// Bind returns r bound to ctx: the commands it runs log the request ID that
// ctx carries. Only ctx's values travel — its cancellation does not, so a
// client that disconnects never kills a mutation halfway, and a task that
// outlives its request keeps running. Runners from outside this package are
// returned unchanged.
func Bind(ctx context.Context, r Runner) Runner {
	if b, ok := r.(binder); ok {
		return b.bind(context.WithoutCancel(ctx))
	}
	return r
}

// outputLimit caps how much of a failed command's output a record carries.
const outputLimit = 512

// logCommand records one finished command at debug level. Arguments and
// output are redacted; stdin is reported by size only, because it carries
// manifests and cloud-init user data.
func logCommand(ctx context.Context, name string, args []string, stdinBytes int, start time.Time, out []byte, err error) {
	if ctx == nil {
		ctx = context.Background()
	}
	logger := slog.Default()
	if !logger.Enabled(ctx, slog.LevelDebug) {
		return
	}
	attrs := []slog.Attr{
		slog.String("cmd", logging.Command(name, args)),
		slog.Duration("duration", time.Since(start)),
	}
	if stdinBytes > 0 {
		attrs = append(attrs, slog.Int("stdin_bytes", stdinBytes))
	}
	if err != nil {
		var exit *exec.ExitError
		if errors.As(err, &exit) {
			attrs = append(attrs, slog.Int("exit_code", exit.ExitCode()))
		}
		attrs = append(attrs, slog.String("error", logging.Redact(err.Error())))
		if len(out) > 0 {
			if len(out) > outputLimit {
				out = out[len(out)-outputLimit:]
			}
			attrs = append(attrs, slog.String("output", logging.Redact(string(out))))
		}
	}
	logger.LogAttrs(ctx, slog.LevelDebug, "exec", attrs...)
}

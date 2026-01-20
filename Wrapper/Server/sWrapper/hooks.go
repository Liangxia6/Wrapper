package wrapper

import (
	"os"
	"os/signal"
	"syscall"
)

// InstallRebindOnUSR2 installs a SIGUSR2 handler that calls m.Rebind().
// This is meant to be used inside the container after CRIU restore.
func InstallRebindOnUSR2(m *MigratableUDP) (stop func()) {
	ch := make(chan os.Signal, 2)
	signal.Notify(ch, syscall.SIGUSR2)
	stop = func() {
		signal.Stop(ch)
		close(ch)
	}
	go func() {
		for range ch {
			_ = m.Rebind()
		}
	}()
	return stop
}

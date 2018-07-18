//+build linux

package log4go

import(
	"os"
	"os/signal"
	"syscall"
)

func notifySignals(s chan os.Signal){
	signal.Notify(s,
		syscall.SIGQUIT,
		syscall.SIGKILL,
		syscall.SIGINT,
		syscall.SIGTERM,
		syscall.SIGTSTP,
	)
}
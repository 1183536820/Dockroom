package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"net/http"
	"os"
	"os/signal"

	"dockroom/internal/cli"
	"dockroom/internal/desktop"
	"dockroom/internal/host"
)

func main() {
	if e := run(); e != nil {
		if len(os.Args) == 1 && os.Getenv("DOCKROOM_TEST_DESKTOP_BASE") == "" {
			desktop.ShowError(e)
		} else {
			fmt.Fprintln(os.Stderr, e)
		}
		os.Exit(1)
	}
}

func run() error {
	if len(os.Args) == 1 {
		return desktop.Run()
	}
	f := flag.NewFlagSet(os.Args[1], flag.ContinueOnError)
	dir := f.String("data", "", "explicit Hub state directory")
	user := f.String("user", "admin", "local user ID")
	hostname := f.String("host", "localhost", "server certificate name or IP")
	origin := f.String("origin", "https://localhost:8442", "public browser HTTPS origin")
	browser := f.String("listen", "127.0.0.1:8442", "browser HTTPS listener")
	device := f.String("device-listen", "127.0.0.1:8443", "device mTLS listener")
	if e := f.Parse(os.Args[2:]); e != nil {
		return e
	}
	if *dir == "" || f.NArg() != 0 {
		return errors.New("explicit --data required")
	}
	switch os.Args[1] {
	case "init", "user-add":
		s, e := cli.Secret("设置本地账号口令（至少 12 字节）：")
		if e != nil {
			return e
		}
		return host.AddUser(*dir, *user, s, os.Args[1] == "init", *hostname)
	case "serve":
		r, e := host.Start(*dir, *origin, *browser, *device)
		if e != nil {
			return e
		}
		defer r.Close()
		fmt.Printf("browser HTTPS %s; Agent mTLS %s\n", *browser, *device)
		ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
		defer stop()
		select {
		case e = <-r.Done:
			if !errors.Is(e, http.ErrServerClosed) {
				return e
			}
		case <-ctx.Done():
		}
		return nil
	default:
		return errors.New("unknown Hub subcommand")
	}
}

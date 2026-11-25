package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"

	"dockroom/internal/agent/fs"
	"dockroom/internal/agent/service"
	"dockroom/internal/cli"
)

func main() {
	if e := run(); e != nil {
		fmt.Fprintln(os.Stderr, e)
		os.Exit(1)
	}
}
func run() error {
	if len(os.Args) < 2 {
		return errors.New("usage: agent pin|enroll|serve [flags]")
	}
	f := flag.NewFlagSet(os.Args[1], flag.ContinueOnError)
	dir := f.String("data", "", "explicit Agent state directory")
	path := f.String("path", "", "absolute root path to pin locally")
	if e := f.Parse(os.Args[2:]); e != nil {
		return e
	}
	if f.NArg() != 0 {
		return errors.New("unexpected argument")
	}
	if os.Args[1] == "pin" || os.Args[1] == "prepare-write" {
		id, e := fs.Pin(*path)
		if e == nil && os.Args[1] == "prepare-write" {
			id, e = fs.PrepareWrite(fs.Root{Path: *path, Identity: id})
		}
		if e == nil {
			fmt.Println(id)
		}
		return e
	}
	if *dir == "" {
		return errors.New("explicit --data required")
	}
	r, e := os.Open(filepath.Join(*dir, "config.json"))
	if e != nil {
		return e
	}
	defer r.Close()
	d := json.NewDecoder(io.LimitReader(r, 256<<10))
	d.DisallowUnknownFields()
	var config service.Config
	if e = d.Decode(&config); e != nil {
		return e
	}
	if d.Decode(new(any)) != io.EOF {
		return errors.New("invalid Agent config")
	}
	switch os.Args[1] {
	case "enroll":
		if _, e := service.NewBrowser(config); e != nil {
			return e
		}
		code, e := cli.Secret("一次性接入码：")
		if e != nil {
			return e
		}
		fingerprint, e := service.Enroll(*dir, config, code)
		if e != nil {
			return e
		}
		fmt.Println(fingerprint)
		return nil
	case "serve":
		ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
		defer stop()
		return service.Run(ctx, *dir, config)
	default:
		return errors.New("unknown Agent subcommand")
	}
}

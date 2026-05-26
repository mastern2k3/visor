package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	"github.com/nitzanz/visor/internal/discovery"
	"github.com/nitzanz/visor/internal/focus"
	"github.com/nitzanz/visor/internal/hookpayload"
	"github.com/nitzanz/visor/internal/ipc"
	"github.com/nitzanz/visor/internal/paths"
	"github.com/nitzanz/visor/internal/state"
	"github.com/nitzanz/visor/internal/transcript"
)

func runDaemon(args []string) {
	fs := flag.NewFlagSet("daemon", flag.ExitOnError)
	verbose := fs.Bool("v", false, "verbose logging")
	_ = fs.Parse(args)

	level := slog.LevelInfo
	if *verbose {
		level = slog.LevelDebug
	}
	log := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: level}))

	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()

	store := state.NewStore()

	w := discovery.New(paths.ProjectsDir(), log)
	go func() {
		if err := w.Run(ctx); err != nil {
			log.Error("discovery", "err", err)
			cancel()
		}
	}()

	// Transcript consumer: every discovery event re-parses appended bytes and
	// folds them into the session store.
	go func() {
		for ev := range w.Events() {
			sess := store.UpsertByPath(ev.Path)
			lines, newOffset, err := transcript.ParseAppended(ev.Path, sess.Offset)
			if err != nil {
				log.Warn("parse", "path", ev.Path, "err", err)
				continue
			}
			if len(lines) == 0 {
				continue
			}
			changed := store.ApplyTranscript(ev.Path, lines, newOffset, ev.IsInitial)
			if changed && !ev.IsInitial {
				log.Debug("activity changed", "id", sess.ID, "path", ev.Path)
			}
		}
	}()

	sock := paths.Socket()
	log.Info("daemon up", "socket", sock, "projects", paths.ProjectsDir())

	if err := ipc.Serve(ctx, sock, log, makeHandler(store, log)); err != nil {
		log.Error("ipc", "err", err)
		os.Exit(1)
	}
}

func makeHandler(store *state.Store, log *slog.Logger) ipc.Handler {
	return func(ctx context.Context, req ipc.Request) ipc.Response {
		switch req.Cmd {
		case "list", "json":
			b, err := store.MarshalSnapshot()
			if err != nil {
				return ipc.Response{Error: err.Error()}
			}
			return ipc.Response{OK: true, Data: b}
		case "watch":
			sub := store.Subscribers().Add()
			// Detach when the connection's context ends. The IPC layer drives
			// the stream; we just need to clean up the subscription.
			go func() {
				<-ctx.Done()
				store.Subscribers().Remove(sub)
			}()
			return ipc.Response{OK: true, Stream: sub.Chan()}
		case "dismiss":
			if !store.Dismiss(req.ID) {
				return ipc.Response{Error: "no such session"}
			}
			return ipc.Response{OK: true}
		case "ack":
			if !store.Acknowledge(req.ID) {
				return ipc.Response{Error: "no such session"}
			}
			return ipc.Response{OK: true}
		case "jump":
			sess, ok := store.Get(req.ID)
			if !ok {
				return ipc.Response{Error: "no such session"}
			}
			// Dispatch in a goroutine so a slow X conn doesn't block the IPC reply.
			go func() {
				t := focus.Target{
					WM:       sess.WM,
					WindowID: sess.WindowID,
					TmuxPane: sess.TmuxPane,
					PID:      sess.PID,
				}
				if err := focus.Dispatch(t); err != nil {
					log.Warn("jump", "id", req.ID, "err", err)
				} else {
					log.Info("jump", "id", req.ID, "window_id", sess.WindowID, "tmux_pane", sess.TmuxPane)
				}
			}()
			return ipc.Response{OK: true}
		case "hook":
			return handleHook(store, log, req)
		default:
			return ipc.Response{Error: fmt.Sprintf("unknown cmd %q", req.Cmd)}
		}
	}
}

func handleHook(store *state.Store, log *slog.Logger, req ipc.Request) ipc.Response {
	var p hookpayload.Enriched
	if err := json.Unmarshal(req.Body, &p); err != nil {
		return ipc.Response{Error: "bad hook payload: " + err.Error()}
	}
	sess := store.ApplyHook(req.Hook, p)
	if sess != nil {
		log.Debug("hook", "event", req.Hook, "matcher", p.Matcher, "id", sess.ID, "pid", sess.PID, "wm", sess.WM)
	}
	return ipc.Response{OK: true}
}

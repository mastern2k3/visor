package x11

import (
	"bufio"
	"encoding/json"
	"fmt"
	"net"

	"github.com/nitzanz/visor/internal/ipc"
	"github.com/nitzanz/visor/internal/paths"
)

// sessionView is the minimal session shape the dock needs. Keeping it
// local (instead of importing state.Snapshot) decouples the dock from
// daemon internals — schema changes to the IPC payload land here.
type sessionView struct {
	ID         string `json:"id"`
	Activity   string `json:"activity"`
	Attention  string `json:"attention"`
	Waiting    string `json:"waiting"`
	DisplayCWD string `json:"display_cwd"`
	Title      string `json:"title"`
}

// subscribe opens a long-lived connection to the visor daemon and sends
// every snapshot it receives on `out`. Returns when the connection drops
// or the daemon's subscription channel closes.
func subscribe(out chan<- []sessionView) error {
	c, err := net.Dial("unix", paths.Socket())
	if err != nil {
		return fmt.Errorf("dial visor socket: %w (is the daemon running?)", err)
	}
	defer c.Close()

	req := ipc.Request{Cmd: "watch"}
	b, _ := json.Marshal(req)
	b = append(b, '\n')
	if _, err := c.Write(b); err != nil {
		return err
	}

	br := bufio.NewReader(c)
	// First line: ack response (we ignore the contents)
	if _, err := br.ReadBytes('\n'); err != nil {
		return err
	}

	for {
		line, err := br.ReadBytes('\n')
		if len(line) > 0 {
			var snap []sessionView
			if jerr := json.Unmarshal(line, &snap); jerr == nil {
				out <- snap
			}
		}
		if err != nil {
			return err
		}
	}
}

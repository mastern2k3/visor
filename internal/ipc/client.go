package ipc

import (
	"bufio"
	"encoding/json"
	"errors"
	"net"
	"time"
)

// Call performs a one-shot request/response on the daemon socket.
func Call(sockPath string, req Request) (Response, error) {
	c, err := net.DialTimeout("unix", sockPath, 2*time.Second)
	if err != nil {
		return Response{}, err
	}
	defer c.Close()
	_ = c.SetDeadline(time.Now().Add(5 * time.Second))
	b, err := json.Marshal(req)
	if err != nil {
		return Response{}, err
	}
	b = append(b, '\n')
	if _, err := c.Write(b); err != nil {
		return Response{}, err
	}
	br := bufio.NewReader(c)
	line, err := br.ReadBytes('\n')
	if err != nil && len(line) == 0 {
		return Response{}, err
	}
	var resp Response
	if err := json.Unmarshal(line, &resp); err != nil {
		return Response{}, err
	}
	if !resp.OK && resp.Error != "" {
		return resp, errors.New(resp.Error)
	}
	return resp, nil
}

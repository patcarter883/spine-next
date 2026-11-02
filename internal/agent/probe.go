package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"time"

	"github.com/patcarter883/spine-next/internal/acp"
	"github.com/patcarter883/spine-next/internal/exec"
)

// ProbeResult describes an agent's handshake.
type ProbeResult struct {
	Agent    string   `json:"agent"`
	Version  string   `json:"version"`
	Protocol int      `json:"protocol"`
	Modes    []string `json:"modes"`
}

type nopHandler struct{}

func (nopHandler) Request(context.Context, string, json.RawMessage) (any, *acp.RPCError) {
	return nil, &acp.RPCError{Code: acp.CodeMethodNotFound, Message: "probe"}
}
func (nopHandler) Notify(context.Context, string, json.RawMessage) {}

// Probe starts the adapter in dir, performs initialize and session/new, and
// stops it. No prompt is sent, so no model tokens are spent.
func Probe(ctx context.Context, a Adapter, dir string) (ProbeResult, error) {
	var res ProbeResult
	inR, inW, err := os.Pipe()
	if err != nil {
		return res, err
	}
	outR, outW := io.Pipe()
	var stderr tail
	conn := acp.NewConn(inW, nopHandler{})
	ctx, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()
	done := make(chan error, 1)
	go func() {
		_, err := exec.Run(ctx, exec.Cmd{Argv: a.Argv, Dir: dir, Env: buildEnv(a), StdinFile: inR, Stdout: outW, Stderr: &stderr, Grace: 2 * time.Second})
		outW.Close()
		done <- err
	}()
	go conn.Serve(ctx, outR)
	defer func() { inW.Close(); cancel(); <-done }()
	var ir acp.InitializeResponse
	if err := conn.Call(ctx, acp.MethodInitialize, acp.InitializeRequest{ProtocolVersion: acp.ProtocolVersion,
		ClientInfo: &acp.Implementation{Name: "spine-probe", Version: "1"}}, &ir); err != nil {
		return res, fmt.Errorf("initialize: %w (stderr: %s)", err, trunc(stderr.String(), 500))
	}
	res.Protocol = ir.ProtocolVersion
	if ir.AgentInfo != nil {
		res.Agent, res.Version = ir.AgentInfo.Name, ir.AgentInfo.Version
	}
	if ir.ProtocolVersion != acp.ProtocolVersion {
		return res, fmt.Errorf("%w: agent speaks ACP v%d", ErrProtocol, ir.ProtocolVersion)
	}
	var ns acp.NewSessionResponse
	if err := conn.Call(ctx, acp.MethodSessionNew, acp.NewSessionRequest{Cwd: dir, McpServers: []any{}}, &ns); err != nil {
		return res, fmt.Errorf("session/new: %w (stderr: %s)", err, trunc(stderr.String(), 500))
	}
	if ns.Modes != nil {
		for _, m := range ns.Modes.AvailableModes {
			res.Modes = append(res.Modes, m.ID)
		}
	}
	return res, nil
}

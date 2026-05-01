//go:build !linux

package awg

import "context"

// CLIExecutor is a non-Linux stub that always reports ErrNotImplemented.
// Use FakeExecutor for cross-platform development and tests.
type CLIExecutor struct {
	AwgBinary      string
	AwgQuickBinary string
	RenderedDir    string
}

func NewCLIExecutor() *CLIExecutor { return &CLIExecutor{} }

func (c *CLIExecutor) SyncConf(context.Context, string, string) error { return ErrNotImplemented }
func (c *CLIExecutor) SetPeer(context.Context, string, PeerSpec) error { return ErrNotImplemented }
func (c *CLIExecutor) RemovePeer(context.Context, string, string) error { return ErrNotImplemented }
func (c *CLIExecutor) ShowDump(context.Context, string) (InterfaceRuntime, []PeerRuntime, error) {
	return InterfaceRuntime{}, nil, ErrNotImplemented
}
func (c *CLIExecutor) ShowConf(context.Context, string) (string, error)              { return "", ErrNotImplemented }
func (c *CLIExecutor) InterfaceUp(context.Context, string, string) error             { return ErrNotImplemented }
func (c *CLIExecutor) InterfaceDown(context.Context, string) error                   { return ErrNotImplemented }

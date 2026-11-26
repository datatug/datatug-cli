//go:build !linux

package sshdemo

import (
	"context"
	"errors"
	"io"
	"time"
)

var errLinuxOnly = errors.New("the SSH demo server runs on Linux only")

func RunServer(context.Context, Config) error                            { return errLinuxOnly }
func RunEdge(context.Context, EdgeConfig) error                          { return errLinuxOnly }
func CheckHealth(context.Context, string, time.Duration) error           { return errLinuxOnly }
func CheckHealthProxyLocal(context.Context, string, time.Duration) error { return errLinuxOnly }
func PrintHostKey(string, io.Writer) error                               { return errLinuxOnly }

// Copyright 2013 Canonical Ltd.
// Licensed under the AGPLv3, see LICENCE file for details.

package main

import (
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"io/ioutil"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/juju/cmd"
	"github.com/juju/errors"
	"github.com/juju/gnuflag"
	jujuos "github.com/juju/os"
	"github.com/juju/os/series"
	"github.com/juju/utils/exec"
	"gopkg.in/juju/names.v3"
	"gopkg.in/yaml.v2"

	"github.com/juju/juju/agent"
	"github.com/juju/juju/caas"
	jujucmd "github.com/juju/juju/cmd"
	cmdutil "github.com/juju/juju/cmd/jujud/util"
	"github.com/juju/juju/core/machinelock"
	"github.com/juju/juju/juju/paths"
	"github.com/juju/juju/juju/sockets"
	"github.com/juju/juju/worker/uniter"
)

type RunCommand struct {
	cmd.CommandBase
	dataDir         string
	MachineLock     machinelock.Lock
	unit            names.UnitTag
	commands        string
	showHelp        bool
	noContext       bool
	forceRemoteUnit bool
	relationId      string
	remoteUnitName  string
}

const runCommandDoc = `
Run the specified commands in the hook context for the unit.

unit-name can be either the unit tag:
 i.e.  unit-ubuntu-0
or the unit id:
 i.e.  ubuntu/0

If --no-context is specified, the <unit-name> positional
argument is not needed.

If the there's one and only one unit on this host, <unit-name>
is automatically inferred and the positional argument is not needed.

The commands are executed with '/bin/bash -s', and the output returned.
`

// Info returns usage information for the command.
func (c *RunCommand) Info() *cmd.Info {
	return jujucmd.Info(&cmd.Info{
		Name:    "juju-run",
		Args:    "[<unit-name>] <commands>",
		Purpose: "run commands in a unit's hook context",
		Doc:     runCommandDoc,
	})
}

func (c *RunCommand) SetFlags(f *gnuflag.FlagSet) {
	f.BoolVar(&c.noContext, "no-context", false, "do not run the command in a unit context")
	f.StringVar(&c.relationId, "r", "", "run the commands for a specific relation context on a unit")
	f.StringVar(&c.relationId, "relation", "", "")
	f.StringVar(&c.remoteUnitName, "remote-unit", "", "run the commands for a specific remote unit in a relation context on a unit")
	f.BoolVar(&c.forceRemoteUnit, "force-remote-unit", false, "run the commands for a specific relation context, bypassing the remote unit check")
}

func (c *RunCommand) Init(args []string) error {
	// make sure we aren't in an existing hook context
	if contextId, err := getenv("JUJU_CONTEXT_ID"); err == nil && contextId != "" {
		return fmt.Errorf("juju-run cannot be called from within a hook, have context %q", contextId)
	}
	if !c.noContext {
		if len(args) < 1 {
			return fmt.Errorf("missing unit-name")
		}
		var unitName = args[0]
		// If the command line param is a unit id (like application/2) we need to
		// change it to the unit tag as that is the format of the agent directory
		// on disk (unit-application-2).
		if names.IsValidUnit(unitName) {
			c.unit = names.NewUnitTag(unitName)
			args = args[1:]
		} else {
			var err error
			c.unit, err = names.ParseUnitTag(unitName)
			if err == nil {
				args = args[1:]
			} else {
				// If arg[0] is neither a unit name not unit tag, perhaps
				// we are running where there's only one unit, in which case
				// we can safely use that.
				var err2 error
				c.unit, err2 = c.maybeGetUnitTag()
				if err2 != nil {
					return errors.Trace(err)
				}
			}
		}
	}
	if len(args) < 1 {
		return fmt.Errorf("missing commands")
	}
	c.commands, args = args[0], args[1:]
	return cmd.CheckEmpty(args)
}

// maybeGetUnitTag looks at the contents of the agents directory
// and if there's one (and only one) valid unit tag there, returns it.
func (c *RunCommand) maybeGetUnitTag() (names.UnitTag, error) {
	dataDir := c.dataDir
	if dataDir == "" {
		// We don't care about errors here. This is a fallback and
		// if there's an issue, we'll exit back to the use anyway.
		hostSeries, _ := series.HostSeries()
		dataDir, _ = paths.DataDir(hostSeries)
	}
	agentDir := filepath.Join(dataDir, "agents")
	files, _ := ioutil.ReadDir(agentDir)
	var unitTags []names.UnitTag
	for _, f := range files {
		if f.IsDir() {
			unitTag, err := names.ParseUnitTag(f.Name())
			if err == nil {
				unitTags = append(unitTags, unitTag)
			}
		}
	}
	if len(unitTags) == 1 {
		return unitTags[0], nil
	}
	return names.UnitTag{}, errors.New("no unit")
}

func (c *RunCommand) Run(ctx *cmd.Context) error {
	var result *exec.ExecResponse
	var err error
	if c.noContext {
		result, err = c.executeNoContext()
	} else {
		result, err = c.executeInUnitContext()
	}
	if err != nil {
		return errors.Trace(err)
	}

	ctx.Stdout.Write(result.Stdout)
	ctx.Stderr.Write(result.Stderr)
	return cmd.NewRcPassthroughError(result.Code)
}

func (c *RunCommand) getSocket(op *caas.OperatorClientInfo) (sockets.Socket, error) {
	if op == nil {
		paths := uniter.NewPaths(cmdutil.DataDir, c.unit, nil)
		return paths.Runtime.LocalJujuRunSocket.Client, nil
	}

	baseDir := agent.Dir(cmdutil.DataDir, c.unit)
	caCertFile := filepath.Join(baseDir, caas.CACertFile)
	caCert, err := ioutil.ReadFile(caCertFile)
	if err != nil {
		return sockets.Socket{}, errors.Annotatef(err, "reading %s", caCertFile)
	}
	rootCAs := x509.NewCertPool()
	if ok := rootCAs.AppendCertsFromPEM(caCert); ok == false {
		return sockets.Socket{}, errors.Errorf("invalid ca certificate")
	}

	application, err := names.UnitApplication(c.unit.Id())
	if err != nil {
		return sockets.Socket{}, errors.Trace(err)
	}

	socketConfig := &uniter.SocketConfig{
		ServiceAddress: op.ServiceAddress,
		TLSConfig: &tls.Config{
			RootCAs:    rootCAs,
			ServerName: application,
		},
	}
	paths := uniter.NewPaths(cmdutil.DataDir, c.unit, socketConfig)
	return paths.Runtime.RemoteJujuRunSocket.Client, nil
}

func (c *RunCommand) executeInUnitContext() (*exec.ExecResponse, error) {
	unitDir := agent.Dir(cmdutil.DataDir, c.unit)
	logger.Debugf("looking for unit dir %s", unitDir)
	// make sure the unit exists
	_, err := os.Stat(unitDir)
	if os.IsNotExist(err) {
		return nil, errors.Errorf("unit %q not found on this machine", c.unit.Id())
	} else if err != nil {
		return nil, errors.Trace(err)
	}

	relationId, err := checkRelationId(c.relationId)
	if err != nil {
		return nil, errors.Trace(err)
	}

	if len(c.remoteUnitName) > 0 && relationId == -1 {
		return nil, errors.Errorf("remote unit: %s, provided without a relation", c.remoteUnitName)
	}

	// juju-run on k8s uses an operator yaml file
	infoFilePath := filepath.Join(unitDir, caas.OperatorClientInfoFile)
	infoFileBytes, err := ioutil.ReadFile(infoFilePath)
	if err != nil && !os.IsNotExist(err) {
		return nil, errors.Annotatef(err, "reading %s", infoFilePath)
	}
	var operatorClientInfo *caas.OperatorClientInfo
	if infoFileBytes != nil {
		op := caas.OperatorClientInfo{}
		err = yaml.Unmarshal(infoFileBytes, &op)
		if err != nil {
			return nil, errors.Trace(err)
		}
		operatorClientInfo = &op
	}

	socket, err := c.getSocket(operatorClientInfo)
	if err != nil {
		return nil, errors.Annotate(err, "configuring juju run socket")
	}
	client, err := sockets.Dial(socket)
	if err != nil {
		return nil, errors.Annotate(err, "dialing juju run socket")
	}
	defer client.Close()

	var result exec.ExecResponse
	args := uniter.RunCommandsArgs{
		Commands:        c.commands,
		RelationId:      relationId,
		UnitName:        c.unit.Id(),
		RemoteUnitName:  c.remoteUnitName,
		ForceRemoteUnit: c.forceRemoteUnit,
	}
	if operatorClientInfo != nil {
		args.Token = operatorClientInfo.Token
	}
	err = client.Call(uniter.JujuRunEndpoint, args, &result)
	return &result, errors.Trace(err)
}

// appendProxyToCommands activates proxy settings on platforms
// that support this feature via the command line. Currently this
// will work on most GNU/Linux systems, but has no use in Windows
// where the proxy settings are taken from the registry or from
// application specific settings (proxy settings in firefox ignore
// registry values on Windows).
func (c *RunCommand) appendProxyToCommands() string {
	switch jujuos.HostOS() {
	case jujuos.Ubuntu:
		return `[ -f "/home/ubuntu/.juju-proxy" ] && . "/home/ubuntu/.juju-proxy"` + "\n" + c.commands
	default:
		return c.commands
	}
}

func (c *RunCommand) executeNoContext() (*exec.ExecResponse, error) {
	// Actually give juju-run a timeout now.
	// Say... 5 minutes.
	// TODO: Perhaps make this configurable later with
	// a command line arg.
	timeout := make(chan struct{})
	go func() {
		<-time.After(5 * time.Minute)
		close(timeout)
	}()
	releaser, err := c.MachineLock.Acquire(machinelock.Spec{
		Cancel: timeout,
		Worker: "juju-run",
	})
	if err != nil {
		return nil, errors.Trace(err)
	}
	defer releaser()

	runCmd := c.appendProxyToCommands()

	return exec.RunCommands(
		exec.RunParams{
			Commands: runCmd,
		})
}

// checkRelationId verifies that the relationId
// given by the user is of a valid syntax, it does
// not check that the relationId is a valid one. This
// is done by the NewRunner method that is part of
// the worker/uniter/runner/factory package.
func checkRelationId(value string) (int, error) {
	if len(value) == 0 {
		return -1, nil
	}

	trim := value
	if idx := strings.LastIndex(trim, ":"); idx != -1 {
		trim = trim[idx+1:]
	}
	id, err := strconv.Atoi(trim)
	if err != nil {
		return -1, errors.Errorf("invalid relation id")
	}
	return id, nil
}

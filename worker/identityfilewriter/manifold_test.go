// Copyright 2016 Canonical Ltd.
// Licensed under the AGPLv3, see LICENCE file for details.

package identityfilewriter_test

import (
	"github.com/juju/testing"
	jc "github.com/juju/testing/checkers"
	gc "gopkg.in/check.v1"
	"gopkg.in/juju/names.v3"
	"gopkg.in/juju/worker.v1"
	"gopkg.in/juju/worker.v1/dependency"

	"github.com/juju/juju/agent"
	apitesting "github.com/juju/juju/api/base/testing"
	"github.com/juju/juju/apiserver/params"
	"github.com/juju/juju/cmd/jujud/agent/engine/enginetest"
	"github.com/juju/juju/state/multiwatcher"
	"github.com/juju/juju/worker/identityfilewriter"
)

type ManifoldSuite struct {
	testing.IsolationSuite
	newCalled bool
}

var _ = gc.Suite(&ManifoldSuite{})

func (s *ManifoldSuite) SetUpTest(c *gc.C) {
	s.newCalled = false
	s.PatchValue(&identityfilewriter.NewWorker,
		func(a agent.Config) (worker.Worker, error) {
			s.newCalled = true
			return nil, nil
		},
	)
}

func (s *ManifoldSuite) TestMachine(c *gc.C) {
	config := identityfilewriter.ManifoldConfig(enginetest.AgentAPIManifoldTestConfig())
	_, err := enginetest.RunAgentAPIManifold(
		identityfilewriter.Manifold(config),
		&fakeAgent{tag: names.NewMachineTag("42")},
		mockAPICaller(multiwatcher.JobManageModel))
	c.Assert(err, jc.ErrorIsNil)
	c.Assert(s.newCalled, jc.IsTrue)
}

func (s *ManifoldSuite) TestMachineNotModelManagerErrors(c *gc.C) {
	config := identityfilewriter.ManifoldConfig(enginetest.AgentAPIManifoldTestConfig())
	_, err := enginetest.RunAgentAPIManifold(
		identityfilewriter.Manifold(config),
		&fakeAgent{tag: names.NewMachineTag("42")},
		mockAPICaller(multiwatcher.JobHostUnits))
	c.Assert(err, gc.Equals, dependency.ErrMissing)
	c.Assert(s.newCalled, jc.IsFalse)
}

func (s *ManifoldSuite) TestNonMachineAgent(c *gc.C) {
	config := identityfilewriter.ManifoldConfig(enginetest.AgentAPIManifoldTestConfig())
	_, err := enginetest.RunAgentAPIManifold(
		identityfilewriter.Manifold(config),
		&fakeAgent{tag: names.NewUnitTag("foo/0")},
		mockAPICaller(""))
	c.Assert(err, gc.ErrorMatches, "this manifold may only be used inside a machine or controller agent")
	c.Assert(s.newCalled, jc.IsFalse)
}

type fakeAgent struct {
	agent.Agent
	tag names.Tag
}

func (a *fakeAgent) CurrentConfig() agent.Config {
	return &fakeConfig{tag: a.tag}
}

type fakeConfig struct {
	agent.Config
	tag names.Tag
}

func (c *fakeConfig) Tag() names.Tag {
	return c.tag
}

func mockAPICaller(job multiwatcher.MachineJob) apitesting.APICallerFunc {
	return apitesting.APICallerFunc(func(objType string, version int, id, request string, arg, result interface{}) error {
		if res, ok := result.(*params.AgentGetEntitiesResults); ok {
			res.Entities = []params.AgentGetEntitiesResult{
				{Jobs: []multiwatcher.MachineJob{
					job,
				}}}
		}
		return nil
	})
}

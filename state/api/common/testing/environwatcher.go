// Copyright 2014 Canonical Ltd.
// Licensed under the AGPLv3, see LICENCE file for details.

package testing

import (
	jc "github.com/juju/testing/checkers"
	gc "launchpad.net/gocheck"

	"launchpad.net/juju-core/environs"
	"launchpad.net/juju-core/environs/config"
	"launchpad.net/juju-core/state"
	"launchpad.net/juju-core/state/api/watcher"
	statetesting "launchpad.net/juju-core/state/testing"
)

const (
	HasSecrets = true
	NoSecrets  = false
)

type Façade interface {
	WatchForEnvironConfigChanges() (watcher.NotifyWatcher, error)
	EnvironConfig() (*config.Config, error)
}

type EnvironWatcherTest struct {
	facade     Façade
	st         *state.State
	backing    *state.State
	hasSecrets bool
}

func NewEnvironWatcherTest(
	facade Façade,
	st *state.State,
	backing *state.State,
	hasSecrets bool) *EnvironWatcherTest {
	return &EnvironWatcherTest{facade, st, backing, hasSecrets}
}

func (s *EnvironWatcherTest) TestEnvironConfig(c *gc.C) {
	envConfig, err := s.st.EnvironConfig()
	c.Assert(err, gc.IsNil)

	conf, err := s.facade.EnvironConfig()
	c.Assert(err, gc.IsNil)

	// If the facade doesn't have secrets, we need to replace the config
	// values in our environment to compare against with the secrets replaced.
	if !s.hasSecrets {
		env, err := environs.New(envConfig)
		c.Assert(err, gc.IsNil)
		secretAttrs, err := env.Provider().SecretAttrs(envConfig)
		c.Assert(err, gc.IsNil)
		secrets := make(map[string]interface{})
		for key := range secretAttrs {
			secrets[key] = "not available"
		}
		envConfig, err = envConfig.Apply(secrets)
		c.Assert(err, gc.IsNil)
	}

	c.Assert(conf, jc.DeepEquals, envConfig)
}

func (s *EnvironWatcherTest) TestWatchForEnvironConfigChanges(c *gc.C) {
	envConfig, err := s.st.EnvironConfig()
	c.Assert(err, gc.IsNil)

	w, err := s.facade.WatchForEnvironConfigChanges()
	c.Assert(err, gc.IsNil)
	defer statetesting.AssertStop(c, w)
	wc := statetesting.NewNotifyWatcherC(c, s.backing, w)

	// Initial event.
	wc.AssertOneChange()

	// Change the environment configuration by updating an existing attribute, check it's detected.
	newAttrs := map[string]interface{}{"logging-config": "juju=ERROR"}
	err = s.st.UpdateEnvironConfig(newAttrs, nil, nil)
	c.Assert(err, gc.IsNil)
	wc.AssertOneChange()

	// Change the environment configuration by adding a new attribute, check it's detected.
	newAttrs = map[string]interface{}{"foo": "bar"}
	err = s.st.UpdateEnvironConfig(newAttrs, nil, nil)
	c.Assert(err, gc.IsNil)
	wc.AssertOneChange()

	// Change the environment configuration by removing an attribute, check it's detected.
	err = s.st.UpdateEnvironConfig(map[string]interface{}{}, []string{"foo"}, nil)
	c.Assert(err, gc.IsNil)
	wc.AssertOneChange()

	// Change it back to the original config.
	oldAttrs := map[string]interface{}{"logging-config": envConfig.AllAttrs()["logging-config"]}
	err = s.st.UpdateEnvironConfig(oldAttrs, nil, nil)
	c.Assert(err, gc.IsNil)
	wc.AssertOneChange()

	statetesting.AssertStop(c, w)
	wc.AssertClosed()
}

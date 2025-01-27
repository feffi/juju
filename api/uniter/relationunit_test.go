// Copyright 2013 Canonical Ltd.
// Licensed under the AGPLv3, see LICENCE file for details.

package uniter_test

import (
	jc "github.com/juju/testing/checkers"
	gc "gopkg.in/check.v1"
	"gopkg.in/juju/charm.v6"
	"gopkg.in/juju/names.v3"

	"github.com/juju/juju/api/uniter"
	"github.com/juju/juju/apiserver/params"
	"github.com/juju/juju/core/watcher/watchertest"
	"github.com/juju/juju/state"
)

// commonRelationSuiteMixin contains fields used by both relationSuite
// and relationUnitSuite. We're not just embeddnig relationUnitSuite
// into relationSuite to avoid running the former's tests twice.
type commonRelationSuiteMixin struct {
	mysqlMachine     *state.Machine
	mysqlApplication *state.Application
	mysqlCharm       *state.Charm
	mysqlUnit        *state.Unit

	stateRelation *state.Relation
}

type relationUnitSuite struct {
	uniterSuite
	commonRelationSuiteMixin
}

var _ = gc.Suite(&relationUnitSuite{})

func (m *commonRelationSuiteMixin) SetUpTest(c *gc.C, s uniterSuite) {
	// Create another machine, application and unit, so we can
	// test relations and relation units.
	m.mysqlMachine, m.mysqlApplication, m.mysqlCharm, m.mysqlUnit = s.addMachineAppCharmAndUnit(c, "mysql")

	// Add a relation, used by both this suite and relationSuite.
	m.stateRelation = s.addRelation(c, "wordpress", "mysql")
	err := m.stateRelation.SetSuspended(true, "")
	c.Assert(err, jc.ErrorIsNil)
}

func (s *relationUnitSuite) SetUpTest(c *gc.C) {
	s.uniterSuite.SetUpTest(c)
	s.commonRelationSuiteMixin.SetUpTest(c, s.uniterSuite)
}

func (s *relationUnitSuite) TearDownTest(c *gc.C) {
	s.uniterSuite.TearDownTest(c)
}

func (s *relationUnitSuite) getRelationUnits(c *gc.C) (*state.RelationUnit, *uniter.RelationUnit) {
	wpRelUnit, err := s.stateRelation.Unit(s.wordpressUnit)
	c.Assert(err, jc.ErrorIsNil)
	apiRelation, err := s.uniter.Relation(s.stateRelation.Tag().(names.RelationTag))
	c.Assert(err, jc.ErrorIsNil)
	// TODO(dfc)
	apiUnit, err := s.uniter.Unit(s.wordpressUnit.Tag().(names.UnitTag))
	c.Assert(err, jc.ErrorIsNil)
	apiRelUnit, err := apiRelation.Unit(apiUnit)
	c.Assert(err, jc.ErrorIsNil)
	return wpRelUnit, apiRelUnit
}

func (s *relationUnitSuite) TestRelation(c *gc.C) {
	_, apiRelUnit := s.getRelationUnits(c)

	apiRel := apiRelUnit.Relation()
	c.Assert(apiRel, gc.NotNil)
	c.Assert(apiRel.String(), gc.Equals, "wordpress:db mysql:server")
}

func (s *relationUnitSuite) TestEndpoint(c *gc.C) {
	_, apiRelUnit := s.getRelationUnits(c)

	apiEndpoint := apiRelUnit.Endpoint()
	c.Assert(apiEndpoint, gc.DeepEquals, uniter.Endpoint{
		charm.Relation{
			Name:      "db",
			Role:      "requirer",
			Interface: "mysql",
			Optional:  false,
			Limit:     1,
			Scope:     "global",
		},
	})
}

func (s *relationUnitSuite) TestEnterScopeSuccessfully(c *gc.C) {
	// NOTE: This test is not as exhaustive as the ones in state.
	// Here, we just check the success case, while the two error
	// cases are tested separately.
	wpRelUnit, apiRelUnit := s.getRelationUnits(c)
	s.assertInScope(c, wpRelUnit, false)

	err := apiRelUnit.EnterScope()
	c.Assert(err, jc.ErrorIsNil)
	s.assertInScope(c, wpRelUnit, true)
}

func (s *relationUnitSuite) TestEnterScopeErrCannotEnterScope(c *gc.C) {
	// Test the ErrCannotEnterScope gets forwarded correctly.
	// We need to enter the scope wit the other unit first.
	myRelUnit, err := s.stateRelation.Unit(s.mysqlUnit)
	c.Assert(err, jc.ErrorIsNil)
	err = myRelUnit.EnterScope(nil)
	c.Assert(err, jc.ErrorIsNil)
	s.assertInScope(c, myRelUnit, true)

	// Now we destroy mysqlApplication, so the relation is be set to
	// dying.
	err = s.mysqlApplication.Destroy()
	c.Assert(err, jc.ErrorIsNil)
	err = s.stateRelation.Refresh()
	c.Assert(err, jc.ErrorIsNil)
	c.Assert(s.stateRelation.Life(), gc.Equals, state.Dying)

	// Enter the scope with wordpressUnit.
	wpRelUnit, apiRelUnit := s.getRelationUnits(c)
	s.assertInScope(c, wpRelUnit, false)
	err = apiRelUnit.EnterScope()
	c.Assert(err, gc.NotNil)
	c.Check(err, jc.Satisfies, params.IsCodeCannotEnterScope)
	c.Check(err, gc.ErrorMatches, "cannot enter scope: unit or relation is not alive")
}

func (s *relationUnitSuite) TestEnterScopeErrCannotEnterScopeYet(c *gc.C) {
	// Test the ErrCannotEnterScopeYet gets forwarded correctly.
	// First we need to destroy the stateRelation.
	err := s.stateRelation.Destroy()
	c.Assert(err, jc.ErrorIsNil)

	// Now we create a subordinate of wordpressUnit and enter scope.
	subRel, _, loggingSub := s.addRelatedApplication(c, "wordpress", "logging", s.wordpressUnit)
	wpRelUnit, err := subRel.Unit(s.wordpressUnit)
	c.Assert(err, jc.ErrorIsNil)
	s.assertInScope(c, wpRelUnit, true)

	// Leave scope, destroy the subordinate and try entering again.
	err = wpRelUnit.LeaveScope()
	c.Assert(err, jc.ErrorIsNil)
	s.assertInScope(c, wpRelUnit, false)
	err = loggingSub.Destroy()
	c.Assert(err, jc.ErrorIsNil)

	apiUnit, err := s.uniter.Unit(s.wordpressUnit.Tag().(names.UnitTag))
	c.Assert(err, jc.ErrorIsNil)
	apiRel, err := s.uniter.Relation(subRel.Tag().(names.RelationTag))
	c.Assert(err, jc.ErrorIsNil)
	apiRelUnit, err := apiRel.Unit(apiUnit)
	c.Assert(err, jc.ErrorIsNil)
	err = apiRelUnit.EnterScope()
	c.Assert(err, gc.NotNil)
	c.Check(err, jc.Satisfies, params.IsCodeCannotEnterScopeYet)
	c.Check(err, gc.ErrorMatches, "cannot enter scope yet: non-alive subordinate unit has not been removed")
}

func (s *relationUnitSuite) TestLeaveScope(c *gc.C) {
	wpRelUnit, apiRelUnit := s.getRelationUnits(c)
	s.assertInScope(c, wpRelUnit, false)

	err := wpRelUnit.EnterScope(nil)
	c.Assert(err, jc.ErrorIsNil)
	s.assertInScope(c, wpRelUnit, true)

	err = apiRelUnit.LeaveScope()
	c.Assert(err, jc.ErrorIsNil)
	s.assertInScope(c, wpRelUnit, false)
}

func (s *relationUnitSuite) TestSettings(c *gc.C) {
	wpRelUnit, apiRelUnit := s.getRelationUnits(c)
	settings := map[string]interface{}{
		"some":  "settings",
		"other": "things",
	}
	err := wpRelUnit.EnterScope(settings)
	c.Assert(err, jc.ErrorIsNil)
	s.assertInScope(c, wpRelUnit, true)

	gotSettings, err := apiRelUnit.Settings()
	c.Assert(err, jc.ErrorIsNil)
	c.Assert(gotSettings.Map(), gc.DeepEquals, params.Settings{
		"some":  "settings",
		"other": "things",
	})
}

func (s *relationUnitSuite) TestReadSettings(c *gc.C) {
	// First try to read the settings which are not set.
	myRelUnit, err := s.stateRelation.Unit(s.mysqlUnit)
	c.Assert(err, jc.ErrorIsNil)
	err = myRelUnit.EnterScope(nil)
	c.Assert(err, jc.ErrorIsNil)
	s.assertInScope(c, myRelUnit, true)

	// Try reading - should be ok.
	wpRelUnit, apiRelUnit := s.getRelationUnits(c)
	s.assertInScope(c, wpRelUnit, false)
	gotSettings, err := apiRelUnit.ReadSettings("mysql/0")
	c.Assert(err, jc.ErrorIsNil)
	c.Assert(gotSettings, gc.HasLen, 0)

	// Now leave and re-enter scope with some settings.
	settings := map[string]interface{}{
		"some":  "settings",
		"other": "things",
	}
	err = myRelUnit.LeaveScope()
	c.Assert(err, jc.ErrorIsNil)
	s.assertInScope(c, myRelUnit, false)
	err = myRelUnit.EnterScope(settings)
	c.Assert(err, jc.ErrorIsNil)
	s.assertInScope(c, myRelUnit, true)
	gotSettings, err = apiRelUnit.ReadSettings("mysql/0")
	c.Assert(err, jc.ErrorIsNil)
	c.Assert(gotSettings, gc.DeepEquals, params.Settings{
		"some":  "settings",
		"other": "things",
	})
}

func (s *relationUnitSuite) TestReadSettingsInvalidUnitTag(c *gc.C) {
	// First try to read the settings which are not set.
	myRelUnit, err := s.stateRelation.Unit(s.mysqlUnit)
	c.Assert(err, jc.ErrorIsNil)
	err = myRelUnit.EnterScope(nil)
	c.Assert(err, jc.ErrorIsNil)
	s.assertInScope(c, myRelUnit, true)

	// Try reading - should be ok.
	wpRelUnit, apiRelUnit := s.getRelationUnits(c)
	s.assertInScope(c, wpRelUnit, false)
	_, err = apiRelUnit.ReadSettings("mysql")
	c.Assert(err, gc.ErrorMatches, "\"mysql\" is not a valid unit")
}

func (s *relationUnitSuite) TestWatchRelationUnits(c *gc.C) {
	// Enter scope with mysqlUnit.
	myRelUnit, err := s.stateRelation.Unit(s.mysqlUnit)
	c.Assert(err, jc.ErrorIsNil)
	err = myRelUnit.EnterScope(nil)
	c.Assert(err, jc.ErrorIsNil)
	s.assertInScope(c, myRelUnit, true)

	apiRel, err := s.uniter.Relation(s.stateRelation.Tag().(names.RelationTag))
	c.Assert(err, jc.ErrorIsNil)
	apiUnit, err := s.uniter.Unit(names.NewUnitTag("wordpress/0"))
	c.Assert(err, jc.ErrorIsNil)
	apiRelUnit, err := apiRel.Unit(apiUnit)
	c.Assert(err, jc.ErrorIsNil)

	// We just created the wordpress unit, make sure its event isn't still in the queue
	s.WaitForModelWatchersIdle(c, s.Model.UUID())

	w, err := apiRelUnit.Watch()
	c.Assert(err, jc.ErrorIsNil)
	wc := watchertest.NewRelationUnitsWatcherC(c, w, s.BackingState.StartSync)
	defer wc.AssertStops()

	// Initial event.
	wc.AssertChange([]string{"mysql/0"}, nil)

	// Leave scope with mysqlUnit, check it's detected.
	err = myRelUnit.LeaveScope()
	c.Assert(err, jc.ErrorIsNil)
	s.assertInScope(c, myRelUnit, false)
	wc.AssertChange(nil, []string{"mysql/0"})

	// Non-change is not reported.
	err = myRelUnit.LeaveScope()
	c.Assert(err, jc.ErrorIsNil)
	wc.AssertNoChange()
}

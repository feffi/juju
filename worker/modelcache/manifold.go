// Copyright 2018 Canonical Ltd.
// Licensed under the AGPLv3, see LICENCE file for details.

package modelcache

import (
	"github.com/juju/errors"
	"github.com/prometheus/client_golang/prometheus"
	"gopkg.in/juju/worker.v1"
	"gopkg.in/juju/worker.v1/dependency"

	"github.com/juju/juju/core/cache"
	"github.com/juju/juju/worker/gate"
	workerstate "github.com/juju/juju/worker/state"
)

// Logger describes the logging methods used in this package by the worker.
type Logger interface {
	IsTraceEnabled() bool
	Tracef(string, ...interface{})
	Errorf(string, ...interface{})
}

// ManifoldConfig holds the information necessary to run a model cache worker in
// a dependency.Engine.
type ManifoldConfig struct {
	StateName           string
	InitializedGateName string
	Logger              Logger

	PrometheusRegisterer prometheus.Registerer

	NewWorker func(Config) (worker.Worker, error)
}

// Validate validates the manifold configuration.
func (config ManifoldConfig) Validate() error {
	if config.StateName == "" {
		return errors.NotValidf("empty StateName")
	}
	if config.InitializedGateName == "" {
		return errors.NotValidf("empty InitializedGateName")
	}
	if config.Logger == nil {
		return errors.NotValidf("missing Logger")
	}
	if config.PrometheusRegisterer == nil {
		return errors.NotValidf("missing PrometheusRegisterer")
	}
	if config.NewWorker == nil {
		return errors.NotValidf("missing NewWorker func")
	}
	return nil
}

// Manifold returns a dependency.Manifold that will run a model cache
// worker. The manifold outputs a *cache.Controller, primarily for
// the apiserver to depend on and use.
func Manifold(config ManifoldConfig) dependency.Manifold {
	return dependency.Manifold{
		Inputs: []string{
			config.StateName,
			config.InitializedGateName,
		},
		Start:  config.start,
		Output: ExtractCacheController,
	}
}

// start is a method on ManifoldConfig because it's more readable than a closure.
func (config ManifoldConfig) start(context dependency.Context) (worker.Worker, error) {
	if err := config.Validate(); err != nil {
		return nil, errors.Trace(err)
	}
	var unlocker gate.Unlocker
	if err := context.Get(config.InitializedGateName, &unlocker); err != nil {
		return nil, errors.Trace(err)
	}
	var stTracker workerstate.StateTracker
	if err := context.Get(config.StateName, &stTracker); err != nil {
		return nil, errors.Trace(err)
	}

	pool, err := stTracker.Use()
	if err != nil {
		return nil, errors.Trace(err)
	}

	w, err := config.NewWorker(Config{
		InitializedGate:      unlocker,
		Logger:               config.Logger,
		WatcherFactory:       func() BackingWatcher { return pool.SystemState().WatchAllModels(pool) },
		PrometheusRegisterer: config.PrometheusRegisterer,
		Cleanup:              func() { _ = stTracker.Done() },
	})
	if err != nil {
		_ = stTracker.Done()
		return nil, errors.Trace(err)
	}
	return w, nil
}

// ExtractCacheController extracts a *cache.Controller from a *cacheWorker.
func ExtractCacheController(in worker.Worker, out interface{}) error {
	inWorker, _ := in.(*cacheWorker)
	if inWorker == nil {
		return errors.Errorf("in should be a %T; got %T", inWorker, in)
	}

	switch outPointer := out.(type) {
	case **cache.Controller:
		*outPointer = inWorker.controller
	default:
		return errors.Errorf("out should be *cache.Controller; got %T", out)
	}
	return nil
}

/*
Copyright 2025.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package deviceplugin

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/fsnotify/fsnotify"
	"github.com/go-logr/logr"
)

// Run serves every configured resource and re-registers all of them whenever
// kubelet restarts.
//
// kubelet deletes its own socket on shutdown and recreates it on start, and it
// forgets every plugin that registered before the restart. Watching the kubelet
// socket directory and re-registering on (re)creation is the standard way a
// device plugin survives a kubelet restart without the DaemonSet being bounced.
func Run(ctx context.Context, cfg *Config, log logr.Logger) error {
	if err := cfg.Validate(); err != nil {
		return err
	}
	// Both socket trees must exist before the first Allocate: kubelet's bind
	// mount of a per-device directory is created by us, but the tree itself is
	// shared with the CRA container, which mounts it at start.
	for _, dir := range []string{cfg.VhostUserDir, cfg.VirtioUserDir} {
		if err := os.MkdirAll(dir, dirMode); err != nil {
			return fmt.Errorf("creating socket tree %s: %w", dir, err)
		}
	}

	watcher, err := fsnotify.NewWatcher()
	if err != nil {
		return fmt.Errorf("creating kubelet socket watcher: %w", err)
	}
	defer watcher.Close()
	if err := watcher.Add(cfg.KubeletSocketDir); err != nil {
		return fmt.Errorf("watching %s: %w", cfg.KubeletSocketDir, err)
	}

	kubeletSocket := filepath.Join(cfg.KubeletSocketDir, kubeletSocketName)

	// The sockets the fast path creates need adopting for as long as the plugin
	// serves, independently of kubelet restarts.
	go adoptFastPathSockets(ctx, cfg, log)

	for {
		runCtx, cancel := context.WithCancel(ctx)
		done := servePlugins(runCtx, cfg, log)

		restart := false
		for !restart {
			select {
			case <-ctx.Done():
				cancel()
				<-done
				return nil
			case <-done:
				// servePlugins only finishes when its context is cancelled, so
				// reaching here without ctx.Done means the run context was
				// cancelled for a restart we are already handling below.
				restart = true
			case event := <-watcher.Events:
				// A recreated kubelet socket means kubelet restarted and has
				// forgotten us; tear the plugins down and register again.
				if event.Name == kubeletSocket && event.Op&fsnotify.Create == fsnotify.Create {
					log.Info("kubelet socket recreated, re-registering device plugins")
					restart = true
				}
			case err := <-watcher.Errors:
				log.Error(err, "kubelet socket watcher error")
			}
		}

		cancel()
		<-done
	}
}

// restartBackoff paces a resource's retry after its plugin stops, so a resource
// that can never be served produces a steady, readable error log, not a hot loop.
const restartBackoff = 2 * time.Second

// kubeletSocketName is the kubelet registration socket filename. It is spelled
// out rather than taken from pluginapi.KubeletSocket because that constant is
// an absolute path in some releases and a bare filename in others.
const kubeletSocketName = "kubelet.sock"

// servePlugins starts one Plugin per configured resource and returns a channel
// that yields once they have all stopped, carrying the joined errors.
//
// Each resource is retried on its own until the context is cancelled, and that
// retry is the point. Previously a plugin goroutine recorded its error and
// exited for good, so a resource that failed to register was simply never
// served again: the DaemonSet pod stayed Running, the healthy resource kept
// working, nothing was logged a second time, and the only symptom was that pods
// requesting the failed resource stayed Pending with a message about the
// scheduler rather than about the plugin. Retrying keeps the failure in the log
// and lets the resource recover on its own once the cause is gone.
//
// Retrying per resource rather than tearing the whole set down deliberately
// keeps one broken resource from taking a working one with it.
func servePlugins(ctx context.Context, cfg *Config, log logr.Logger) <-chan error {
	done := make(chan error, 1)

	var (
		wg   sync.WaitGroup
		mu   sync.Mutex
		errs []error
	)
	for _, resource := range cfg.Resources {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := serveResource(ctx, cfg, resource, log); err != nil {
				mu.Lock()
				errs = append(errs, err)
				mu.Unlock()
			}
		}()
	}

	go func() {
		wg.Wait()
		mu.Lock()
		defer mu.Unlock()
		done <- errors.Join(errs...)
	}()
	return done
}

// serveResource serves one resource, restarting it whenever it stops, until the
// context is cancelled. It returns the last error seen, or nil if it was only
// ever stopped by cancellation.
func serveResource(ctx context.Context, cfg *Config, resource string, log logr.Logger) error {
	name := cfg.ResourceName(resource)
	var last error
	for {
		err := NewPlugin(cfg, resource, log).Serve(ctx)
		if ctx.Err() != nil {
			return last
		}
		if err != nil {
			last = err
			log.Error(err, "device plugin stopped, retrying", "resource", name)
		} else {
			log.Info("device plugin stopped, retrying", "resource", name)
		}

		select {
		case <-ctx.Done():
			return last
		case <-time.After(restartBackoff):
		}
	}
}

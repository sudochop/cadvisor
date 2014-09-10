// Copyright 2014 Google Inc. All Rights Reserved.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

// Per-container manager.

package manager

import (
	"flag"
	"fmt"
	"sync"
	"time"

	"github.com/golang/glog"
	"github.com/google/cadvisor/container"
	"github.com/google/cadvisor/info"
	"github.com/google/cadvisor/storage"
)

// Housekeeping interval.
var HousekeepingInterval = flag.Duration("housekeeping_interval", 1*time.Second, "Interval between container housekeepings")

// Internal mirror of the external data structure.
type containerStat struct {
	Timestamp time.Time
	Data      *info.ContainerStats
}
type containerInfo struct {
	info.ContainerReference
	Subcontainers []info.ContainerReference
	Spec          *info.ContainerSpec
}

type containerData struct {
	handler       container.ContainerHandler
	info          containerInfo
	storageDriver storage.StorageDriver
	lock          sync.Mutex

	// Tells the container to stop.
	stop chan bool
}

func (c *containerData) Start() error {
	go c.housekeeping()
	return nil
}

func (c *containerData) Stop() error {
	c.stop <- true
	return nil
}

func (c *containerData) GetInfo() (*containerInfo, error) {
	// TODO(vmarmol): Consider caching this.
	// Get spec and subcontainers.
	err := c.updateSpec()
	if err != nil {
		return nil, err
	}
	err = c.updateSubcontainers()
	if err != nil {
		return nil, err
	}

	// Make a copy of the info for the user.
	c.lock.Lock()
	defer c.lock.Unlock()
	ret := c.info
	return &ret, nil
}

func NewContainerData(containerName string, driver storage.StorageDriver) (*containerData, error) {
	if driver == nil {
		return nil, fmt.Errorf("nil storage driver")
	}
	cont := &containerData{}
	handler, err := container.NewContainerHandler(containerName)
	if err != nil {
		return nil, err
	}
	cont.handler = handler
	ref, err := handler.ContainerReference()
	if err != nil {
		return nil, err
	}
	cont.info.Name = ref.Name
	cont.info.Aliases = ref.Aliases
	cont.storageDriver = driver
	cont.stop = make(chan bool, 1)

	return cont, nil
}

// Determine when the next housekeeping should occur.
func (self *containerData) nextHousekeeping(lastHousekeeping time.Time) time.Time {
	// For now, we just want to housekeep even HousekeepingInterval.
	// TODO(vmarmol): Housekeep less if there are no change in stats.
	// TODO(vishnuk): Housekeep less if there are no processes.
	return lastHousekeeping.Add(*HousekeepingInterval)
}

func (c *containerData) housekeeping() {
	// Long housekeeping is either 100ms or half of the housekeeping interval.
	longHousekeeping := 100 * time.Millisecond
	if *HousekeepingInterval/2 < longHousekeeping {
		longHousekeeping = *HousekeepingInterval / 2
	}

	// Housekeep every second.
	glog.Infof("Start housekeeping for container %q\n", c.info.Name)
	lastHousekeeping := time.Now()
	for {
		select {
		case <-c.stop:
			// Stop housekeeping when signaled.
			return
		default:
			// Perform housekeeping.
			start := time.Now()
			c.housekeepingTick()

			// Log if housekeeping took too long.
			duration := time.Since(start)
			if duration >= longHousekeeping {
				glog.V(1).Infof("Housekeeping(%s) took %s", c.info.Name, duration)
			}
		}

		// Schedule the next housekeeping. Sleep until that time.
		nextHousekeeping := c.nextHousekeeping(lastHousekeeping)
		if time.Now().Before(nextHousekeeping) {
			time.Sleep(nextHousekeeping.Sub(time.Now()))
		}
		lastHousekeeping = nextHousekeeping

	}
}

func (c *containerData) housekeepingTick() {
	err := c.updateStats()
	if err != nil {
		glog.Infof("Failed to update stats for container \"%s\": %s", c.info.Name, err)
	}
}

func (c *containerData) updateSpec() error {
	spec, err := c.handler.GetSpec()
	if err != nil {
		return err
	}
	c.lock.Lock()
	defer c.lock.Unlock()
	c.info.Spec = spec
	return nil
}

func (c *containerData) updateStats() error {
	stats, err := c.handler.GetStats()
	if err != nil {
		return err
	}
	if stats == nil {
		return nil
	}
	ref, err := c.handler.ContainerReference()
	if err != nil {
		return err
	}
	err = c.storageDriver.AddStats(ref, stats)
	if err != nil {
		return err
	}
	return nil
}

func (c *containerData) updateSubcontainers() error {
	subcontainers, err := c.handler.ListContainers(container.LIST_SELF)
	if err != nil {
		return err
	}
	c.lock.Lock()
	defer c.lock.Unlock()
	c.info.Subcontainers = subcontainers
	return nil
}
